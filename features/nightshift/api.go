package nightshift

import (
	"context"
	"errors"

	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// View is what the 夜间值班 page and the cards read.
type View struct {
	Config  Config   `json:"config"`
	Status  Status   `json:"status"`
	Shift   *Shift   `json:"shift"`
	Reports []Report `json:"reports"`
}

// APIPlugin exposes the night shift on the gateway:
//
//	GET  /api/nightshift                    the set-up, the shift, the reports
//	PUT  /api/nightshift                    change the set-up
//	POST /api/nightshift/end                end the shift now (with its report)
//	POST /api/nightshift/poll               look at GitHub now
//	POST /api/nightshift/reports/{id}/seen  fold a report's card away
var APIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/nightshift-api",
	Inject:      []string{gateway.ServiceKey, ServiceKey},
	Description: "Remote methods for 夜间值班.",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		s := kernel.Use[*Service](c, ServiceKey)
		// who cannot work with the agent cannot put it on duty
		type access interface {
			Agents(deviceID string, ids []string, act bool) bool
		}
		may := func(r *gateway.Request, agent string) bool {
			a, ok := kernel.Lookup[access](c, "access")
			return !ok || a == nil || agent == "" || a.Agents(r.DeviceID, []string{agent}, true)
		}
		view := func(ctx context.Context) View {
			s.Ensure(ctx)
			rs := s.Reports()
			return View{Config: s.Config(), Status: s.Status(ctx, true), Shift: s.Shift(), Reports: rs}
		}
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/nightshift",
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				if !may(r, s.Config().Agent) {
					return nil, gateway.NotFound()
				}
				return view(ctx), nil
			}})
		g.Handle(c, gateway.Route{Method: "PUT", Path: "/api/nightshift",
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				var b Config
				if err := r.Decode(&b); err != nil {
					return nil, err
				}
				if !may(r, b.Agent) || !may(r, s.Config().Agent) {
					return nil, &gateway.Error{Status: 403, Code: "forbidden", Message: "you cannot put this agent on duty"}
				}
				if _, err := s.Save(b); err != nil {
					var inv *ErrInvalid
					if errors.As(err, &inv) {
						return nil, gateway.BadRequest("%s", inv.Msg)
					}
					return nil, gateway.BadRequest("%v", err)
				}
				return view(ctx), nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/nightshift/end",
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				if !may(r, s.Config().Agent) {
					return nil, gateway.NotFound()
				}
				if _, err := s.End(); err != nil {
					return nil, gateway.Conflict("%v", err)
				}
				return view(ctx), nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/nightshift/poll",
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				if !may(r, s.Config().Agent) {
					return nil, gateway.NotFound()
				}
				if s.Shift() == nil {
					return nil, gateway.Conflict("not on duty")
				}
				go s.Poll(context.WithoutCancel(ctx))
				return map[string]bool{"started": true}, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/nightshift/reports/{id}/seen",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if !may(r, s.Config().Agent) || !s.MarkSeen(r.Params["id"]) {
					return nil, gateway.NotFound()
				}
				return nil, nil
			}})
		Changed.Listen(c, func(struct{}) { g.Hub().Publish("nightshift.changed", map[string]any{}) })
		return nil
	},
})
