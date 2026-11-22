package workflows

import (
	"context"

	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// access is the teams plugin's view of who sees what (nil without it).
type access interface {
	Session(deviceID, id string) bool
	Agents(deviceID string, ids []string, act bool) bool
}

// stepAgents are the agents a workflow's steps use.
func stepAgents(steps []Step) []string {
	var out []string
	for _, st := range steps {
		if st.Agent != "" {
			out = append(out, st.Agent)
		}
		out = append(out, stepAgents(st.Parallel)...)
	}
	return out
}

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
		// a workflow runs its steps' agents: who cannot see one of them
		// cannot see the workflow, who cannot work with all of them cannot
		// run, change or cancel it; a run is a session (the teams guard and
		// the same agent check)
		acc := func() access { a, _ := kernel.Lookup[access](c, "access"); return a }
		sees := func(r *gateway.Request, wf Workflow, act bool) bool {
			a := acc()
			return a == nil || a.Agents(r.DeviceID, stepAgents(wf.Steps), act)
		}
		forbid := &gateway.Error{Status: 403, Code: "forbidden", Message: "you cannot run or change this workflow"}
		def := func(r *gateway.Request, act bool) (View, error) {
			v, ok := s.Get(r.Params["id"])
			if !ok || !sees(r, v.Workflow, false) {
				return View{}, gateway.NotFound()
			}
			if act && !sees(r, v.Workflow, true) {
				return View{}, forbid
			}
			return v, nil
		}
		run := func(r *gateway.Request, act bool) (RunView, error) {
			rv, err := s.Run(r.Params["run"])
			if err != nil {
				return RunView{}, gateway.NotFound()
			}
			if a := acc(); a != nil {
				if !a.Session(r.DeviceID, rv.ID) {
					return RunView{}, gateway.NotFound()
				}
				if v, ok := s.Get(rv.Workflow); ok && !sees(r, v.Workflow, act) {
					if act {
						return RunView{}, forbid
					}
					return RunView{}, gateway.NotFound()
				}
			}
			return rv, nil
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
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				out := []View{}
				for _, v := range s.List() {
					if sees(r, v.Workflow, false) {
						out = append(out, v)
					}
				}
				return map[string]any{"workflows": out}, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/workflows", Validate: validateDef,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				wf, _ := decodeDef(r)
				if !sees(r, wf, true) {
					return nil, forbid
				}
				out, err := s.Create(wf)
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return out, nil
			}})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/workflows/{id}", Devices: mobile,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				return def(r, false)
			}})
		g.Handle(c, gateway.Route{Method: "PUT", Path: "/api/workflows/{id}", Validate: validateDef,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if _, err := def(r, true); err != nil {
					return nil, err
				}
				wf, _ := decodeDef(r)
				if !sees(r, wf, true) {
					return nil, forbid
				}
				out, err := s.Update(r.Params["id"], wf)
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return out, nil
			}})
		g.Handle(c, gateway.Route{Method: "DELETE", Path: "/api/workflows/{id}",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if _, err := def(r, true); err != nil {
					return nil, err
				}
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
				if _, err := def(r, true); err != nil {
					return nil, err
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
				if _, err := def(r, false); err != nil {
					return nil, err
				}
				runs, err := s.Runs(r.Params["id"])
				if err != nil {
					return nil, gateway.NotFound()
				}
				return map[string]any{"runs": runs}, nil
			}})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/workflow-runs/{run}", Devices: mobile,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				return run(r, false)
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/workflow-runs/{run}/rerun",
			Validate: func(r *gateway.Request) error {
				var b struct {
					From string `json:"from"`
				}
				return r.Decode(&b)
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if _, err := run(r, true); err != nil {
					return nil, err
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
				if _, err := run(r, true); err != nil {
					return nil, err
				}
				if err := s.Cancel(r.Params["run"]); err != nil {
					return nil, gateway.Conflict("%v", err)
				}
				return nil, nil
			}})
		return nil
	},
})
