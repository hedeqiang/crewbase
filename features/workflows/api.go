package workflows

import (
	"context"

	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// APIPlugin exposes workflows and their runs on the gateway.
var APIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/workflows-api",
	Inject:      []string{gateway.ServiceKey, ServiceKey},
	Description: "Remote methods for workflows: definitions, runs, rerun, cancel.",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		s := kernel.Use[*Service](c, ServiceKey)
		mobile := []string{"mobile", "im"}

		decodeDef := func(r *gateway.Request) (Workflow, error) {
			var wf Workflow
			if err := r.Decode(&wf); err != nil {
				return wf, err
			}
			if id := r.Params["id"]; id != "" {
				wf.ID = id
			}
			return wf, nil
		}
		validateDef := func(r *gateway.Request) error {
			wf, err := decodeDef(r)
			if err != nil {
				return err
			}
			if _, err := s.Check(wf); err != nil {
				return gateway.BadRequest("%v", err)
			}
			return nil
		}

		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/workflows", Devices: mobile,
			Handle: func(context.Context, *gateway.Request) (any, error) {
				return map[string]any{"workflows": s.List()}, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/workflows", Validate: validateDef,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				wf, _ := decodeDef(r)
				out, err := s.Create(wf)
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return out, nil
			}})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/workflows/{id}", Devices: mobile,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				v, ok := s.Get(r.Params["id"])
				if !ok {
					return nil, gateway.NotFound()
				}
				return v, nil
			}})
		g.Handle(c, gateway.Route{Method: "PUT", Path: "/api/workflows/{id}", Validate: validateDef,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if _, ok := s.Get(r.Params["id"]); !ok {
					return nil, gateway.NotFound()
				}
				wf, _ := decodeDef(r)
				out, err := s.Update(r.Params["id"], wf)
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return out, nil
			}})
		g.Handle(c, gateway.Route{Method: "DELETE", Path: "/api/workflows/{id}",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if err := s.Delete(r.Params["id"]); err != nil {
					return nil, gateway.NotFound()
				}
				return nil, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/workflows/{id}/run", Devices: mobile,
			Validate: func(r *gateway.Request) error {
				var b struct {
					Input string `json:"input"`
				}
				return r.Decode(&b)
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if _, ok := s.Get(r.Params["id"]); !ok {
					return nil, gateway.NotFound()
				}
				var b struct {
					Input string `json:"input"`
				}
				_ = r.Decode(&b)
				rv, err := s.Start(r.Params["id"], b.Input)
				if err != nil {
					return nil, gateway.Conflict("%v", err)
				}
				return rv, nil
			}})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/workflows/{id}/runs", Devices: mobile,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				runs, err := s.Runs(r.Params["id"])
				if err != nil {
					return nil, gateway.NotFound()
				}
				return map[string]any{"runs": runs}, nil
			}})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/workflow-runs/{run}", Devices: mobile,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				rv, err := s.Run(r.Params["run"])
				if err != nil {
					return nil, gateway.NotFound()
				}
				return rv, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/workflow-runs/{run}/rerun",
			Validate: func(r *gateway.Request) error {
				var b struct {
					From string `json:"from"`
				}
				return r.Decode(&b)
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if _, err := s.Run(r.Params["run"]); err != nil {
					return nil, gateway.NotFound()
				}
				var b struct {
					From string `json:"from"`
				}
				_ = r.Decode(&b)
				rv, err := s.Rerun(r.Params["run"], b.From)
				if err != nil {
					return nil, gateway.Conflict("%v", err)
				}
				return rv, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/workflow-runs/{run}/cancel", Devices: mobile,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if _, err := s.Run(r.Params["run"]); err != nil {
					return nil, gateway.NotFound()
				}
				if err := s.Cancel(r.Params["run"]); err != nil {
					return nil, gateway.Conflict("%v", err)
				}
				return nil, nil
			}})
		return nil
	},
})
