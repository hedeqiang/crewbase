package relay

import (
	"context"
	"net/http"
	"strings"

	"github.com/hedeqiang/crewbase/features/devices"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// Config of the relay link. Enable with a patch:
//
//   - id: relay
//     disabled: false
//     config:
//     url: https://relay.example.org
type Config struct {
	URL string `json:"url"`
	Dir string `json:"dir"` // where relay-key.pem lives
}

// Plugin keeps the outbound relay link and exposes its status:
//
//	GET  /api/relay                 → Status (+ configured)
//	POST /api/relay/pair-link {code} → {url}: the link / QR content for a pairing code
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/relay",
	Inject:      []string{gateway.ServiceKey, devices.ServiceKey},
	Description: "Outbound WebSocket relay link: devices reach this computer with zero inbound ports, end-to-end encrypted.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		id, err := LoadOrCreateIdentity(cfg.Dir)
		if err != nil {
			return err
		}
		var h *Host
		if strings.TrimSpace(cfg.URL) != "" {
			if h, err = NewHost(id, cfg.URL, g, c.Log()); err != nil {
				return err
			}
			// public webhooks for endpoints that opted in (features/webhooks)
			h.SetHooks(func() []string {
				ts, _ := HookTokens.Run(c, context.Background(), struct{}{})
				return ts
			}, func(ctx context.Context, token string, hdr http.Header, body []byte) int {
				st, ok := HookIntake.Run(c, ctx, HookCall{Token: token, Header: hdr, Body: body})
				if !ok {
					return http.StatusNotFound
				}
				return st
			})
			HooksChanged.Listen(c, func(struct{}) { h.KickHooks() })
			c.Go(h.Run)
		}
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/relay",
			Handle: func(context.Context, *gateway.Request) (any, error) {
				if h == nil {
					return map[string]any{"configured": false, "hostId": id.HostID()}, nil
				}
				return map[string]any{"configured": true, "status": h.Status()}, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/relay/pair-link",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if h == nil {
					return nil, gateway.BadRequest("no relay is configured")
				}
				var b struct {
					Code string `json:"code"`
				}
				if err := r.Decode(&b); err != nil {
					return nil, err
				}
				return map[string]string{"url": h.PairURL(b.Code)}, nil
			}})
		return nil
	},
})
