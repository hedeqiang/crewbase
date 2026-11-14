package routines

import (
	"context"
	"strings"

	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// APIPlugin exposes routines and briefs on the gateway.
var APIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/routines-api",
	Inject:      []string{gateway.ServiceKey, ServiceKey},
	Description: "Remote methods for routines and the morning brief.",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		s := kernel.Use[*Service](c, ServiceKey)
		mobile := []string{"mobile", "im"}
		validate := func(r *gateway.Request) error {
			var b Routine
			if err := r.Decode(&b); err != nil {
				return err
			}
			if strings.TrimSpace(b.Name) == "" || strings.TrimSpace(b.Prompt) == "" || b.Agent == "" || b.Schedule == "" {
				return gateway.BadRequest("name, agent, prompt and schedule are required")
			}
			return nil
		}
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/routines", Devices: mobile,
			Handle: func(context.Context, *gateway.Request) (any, error) {
				l := s.List()
				if l == nil {
					l = []Routine{}
				}
				return map[string]any{"routines": l}, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/routines", Validate: validate,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b Routine
				_ = r.Decode(&b)
				out, err := s.Create(b)
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return out, nil
			}})
		g.Handle(c, gateway.Route{Method: "PATCH", Path: "/api/routines/{id}",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if _, ok := s.Get(r.Params["id"]); !ok {
					return nil, gateway.NotFound()
				}
				var b struct {
					Name     *string `json:"name"`
					Prompt   *string `json:"prompt"`
					Schedule *string `json:"schedule"`
					Enabled  *bool   `json:"enabled"`
					Mode     *string `json:"mode"`
				}
				if err := r.Decode(&b); err != nil {
					return nil, err
				}
				out, err := s.Update(r.Params["id"], func(x *Routine) {
					if b.Name != nil {
						x.Name = *b.Name
					}
					if b.Prompt != nil {
						x.Prompt = *b.Prompt
					}
					if b.Schedule != nil {
						x.Schedule = *b.Schedule
					}
					if b.Enabled != nil {
						x.Enabled = *b.Enabled
					}
					if b.Mode != nil {
						x.Mode = *b.Mode
					}
				})
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return out, nil
			}})
		g.Handle(c, gateway.Route{Method: "DELETE", Path: "/api/routines/{id}",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if err := s.Delete(r.Params["id"]); err != nil {
					return nil, gateway.NotFound()
				}
				return nil, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/routines/{id}/run", Devices: mobile,
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				if _, ok := s.Get(r.Params["id"]); !ok {
					return nil, gateway.NotFound()
				}
				if err := s.RunNow(context.WithoutCancel(ctx), r.Params["id"]); err != nil {
					return nil, gateway.Conflict("%v", err)
				}
				return nil, nil
			}})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/routines/{id}/runs", Devices: mobile,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				runs, err := s.Runs(r.Params["id"])
				if err != nil {
					return nil, gateway.NotFound()
				}
				if runs == nil {
					runs = []Run{}
				}
				return map[string]any{"runs": runs}, nil
			}})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/brief", Devices: mobile,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				// Live preview of what the next brief would contain, plus history.
				bs := s.Briefs()
				if bs == nil {
					bs = []Brief{}
				}
				return map[string]any{"preview": s.ComposeBrief(), "history": bs}, nil
			}})
		return nil
	},
})
