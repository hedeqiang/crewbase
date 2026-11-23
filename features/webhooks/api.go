package webhooks

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/features/webhooks/verifiers"
	"github.com/hedeqiang/crewbase/kernel"
)

// APIPlugin is the Webhook settings page's API and the public intake.
// Every management route takes ?team= (the teams guard checks it).
//
//	POST   /api/webhooks/ingest/{id}               public: a provider's delivery (verified by the endpoint's secret)
//	GET    /api/webhooks?team=                     {endpoints, subscriptions, providers, events}
//	POST   /api/webhooks/endpoints?team=           EndpointInput → {endpoint, secret?}
//	PATCH  /api/webhooks/endpoints/{id}?team=
//	DELETE /api/webhooks/endpoints/{id}?team=
//	POST   /api/webhooks/endpoints/{id}/relay-token?team=   a new relay address
//	POST   /api/webhooks/subscriptions?team=       SubscriptionInput → {subscription, secret?}
//	PATCH  /api/webhooks/subscriptions/{id}?team=
//	DELETE /api/webhooks/subscriptions/{id}?team=
//	POST   /api/webhooks/subscriptions/{id}/test?team=
//	GET    /api/webhooks/deliveries?team=&of=&n=
//	GET    /api/webhooks/deliveries/{id}?team=
//	POST   /api/webhooks/deliveries/{id}/replay?team=   {dry}
//	GET    /api/webhooks/settings · PUT /api/webhooks/settings
var APIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/webhooks-api",
	Inject:      []string{gateway.ServiceKey, ServiceKey},
	Description: "The Webhook page's API and the public webhook intake.",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		s := kernel.Use[*Service](c, ServiceKey)
		team := func(r *gateway.Request) string { return r.HTTP.URL.Query().Get("team") }
		by := func(r *gateway.Request) string {
			if r.DeviceID == "" {
				return "person:owner"
			}
			return "person:" + r.Device
		}

		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/webhooks/ingest/{id}", Public: true, RawBody: true, MaxBody: MaxBody,
			Validate: func(r *gateway.Request) error {
				if _, ok := s.Endpoint("", r.Params["id"]); !ok {
					return gateway.NotFound()
				}
				return nil
			},
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				e, ok := s.Endpoint("", r.Params["id"])
				if !ok {
					return nil, gateway.NotFound()
				}
				// detached: the targets are only started, not awaited
				d, st := s.Ingest(context.WithoutCancel(ctx), e, r.HTTP.Header, r.Body, "direct")
				switch st {
				case http.StatusUnauthorized:
					return nil, &gateway.Error{Status: 401, Code: "unauthorized", Message: "signature did not verify"}
				case http.StatusNotFound:
					return nil, gateway.NotFound()
				case http.StatusServiceUnavailable:
					return nil, &gateway.Error{Status: 503, Code: "unavailable", Message: "the endpoint cannot verify right now"}
				}
				return map[string]any{"status": d.Status, "delivery": d.ID}, nil
			}})

		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/webhooks",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				return map[string]any{
					"endpoints": s.Endpoints(team(r)), "subscriptions": s.Subscriptions(team(r)),
					"providers": verifiers.Names(), "events": OutEvents(),
				}, nil
			}})

		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/webhooks/endpoints",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b EndpointInput
				if err := r.Decode(&b); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				e, secret, err := s.SaveEndpoint(team(r), "", by(r), b)
				if err != nil {
					return nil, apiErr(err)
				}
				return map[string]any{"endpoint": e, "secret": secret}, nil
			}})
		g.Handle(c, gateway.Route{Method: "PATCH", Path: "/api/webhooks/endpoints/{id}",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b EndpointInput
				if err := r.Decode(&b); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				e, secret, err := s.SaveEndpoint(team(r), r.Params["id"], by(r), b)
				if err != nil {
					return nil, apiErr(err)
				}
				return map[string]any{"endpoint": e, "secret": secret}, nil
			}})
		g.Handle(c, gateway.Route{Method: "DELETE", Path: "/api/webhooks/endpoints/{id}",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if err := s.DeleteEndpoint(team(r), r.Params["id"], by(r)); err != nil {
					return nil, apiErr(err)
				}
				return map[string]any{"ok": true}, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/webhooks/endpoints/{id}/relay-token",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				e, err := s.RotateRelay(team(r), r.Params["id"], by(r))
				if err != nil {
					return nil, apiErr(err)
				}
				return e, nil
			}})

		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/webhooks/subscriptions",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b SubscriptionInput
				if err := r.Decode(&b); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				x, secret, err := s.SaveSubscription(team(r), "", by(r), b)
				if err != nil {
					return nil, apiErr(err)
				}
				return map[string]any{"subscription": x, "secret": secret}, nil
			}})
		g.Handle(c, gateway.Route{Method: "PATCH", Path: "/api/webhooks/subscriptions/{id}",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b SubscriptionInput
				if err := r.Decode(&b); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				x, secret, err := s.SaveSubscription(team(r), r.Params["id"], by(r), b)
				if err != nil {
					return nil, apiErr(err)
				}
				return map[string]any{"subscription": x, "secret": secret}, nil
			}})
		g.Handle(c, gateway.Route{Method: "DELETE", Path: "/api/webhooks/subscriptions/{id}",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if err := s.DeleteSubscription(team(r), r.Params["id"], by(r)); err != nil {
					return nil, apiErr(err)
				}
				return map[string]any{"ok": true}, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/webhooks/subscriptions/{id}/test",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				d, err := s.SendTest(team(r), r.Params["id"], by(r))
				if err != nil {
					return nil, apiErr(err)
				}
				return d, nil
			}})

		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/webhooks/deliveries",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				n, _ := strconv.Atoi(r.HTTP.URL.Query().Get("n"))
				ds := s.Deliveries(team(r), r.HTTP.URL.Query().Get("of"), n)
				for i := range ds {
					ds[i].Body, ds[i].BodyText = nil, "" // the list stays light
				}
				return map[string]any{"deliveries": ds}, nil
			}})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/webhooks/deliveries/{id}",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				d, ok := s.Delivery(team(r), r.Params["id"])
				if !ok {
					return nil, gateway.NotFound()
				}
				return d, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/webhooks/deliveries/{id}/replay",
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				var b struct {
					Dry bool `json:"dry"`
				}
				_ = r.Decode(&b)
				d, err := s.Replay(context.WithoutCancel(ctx), team(r), r.Params["id"], by(r), b.Dry)
				if err != nil {
					return nil, apiErr(err)
				}
				return d, nil
			}})

		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/webhooks/settings",
			Handle: func(context.Context, *gateway.Request) (any, error) { return s.Settings(), nil }})
		g.Handle(c, gateway.Route{Method: "PUT", Path: "/api/webhooks/settings",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b Settings
				if err := r.Decode(&b); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				out, err := s.SetSettings(b, by(r))
				if err != nil {
					return nil, apiErr(err)
				}
				return out, nil
			}})

		Changed.Listen(c, func(ev ChangeEvent) { g.Hub().Publish("webhooks.changed", ev) })
		return nil
	},
})

func apiErr(err error) error {
	var e *Error
	switch {
	case errors.As(err, &e):
		return &gateway.Error{Status: e.Status, Code: "bad_request", Message: e.Msg}
	case errors.Is(err, ErrNotFound):
		return gateway.NotFound()
	}
	return gateway.BadRequest("%v", err)
}
