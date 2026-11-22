package routines

import (
	"context"
	"strings"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// access is the teams plugin's view of who sees what (nil without it: one
// person, everything is theirs).
type access interface {
	Session(deviceID, id string) bool
	Agents(deviceID string, ids []string, act bool) bool
}

// APIPlugin exposes routines and briefs on the gateway.
var APIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/routines-api",
	Inject:      []string{gateway.ServiceKey, ServiceKey},
	Description: "Remote methods for routines and the morning brief.",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		s := kernel.Use[*Service](c, ServiceKey)
		mobile := []string{"mobile", "im"}
		// a routine runs its agent: who cannot see the agent cannot see the
		// routine, who cannot work with it cannot run or change it
		acc := func() access { a, _ := kernel.Lookup[access](c, "access"); return a }
		sees := func(r *gateway.Request, agent string, act bool) bool {
			a := acc()
			return a == nil || a.Agents(r.DeviceID, []string{agent}, act)
		}
		mine := func(r *gateway.Request, act bool) error {
			rt, ok := s.Get(r.Params["id"])
			if !ok || !sees(r, rt.Agent, false) {
				return gateway.NotFound()
			}
			if act && !sees(r, rt.Agent, true) {
				return &gateway.Error{Status: 403, Code: "forbidden", Message: "you cannot run or change this routine"}
			}
			return nil
		}
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
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				l := []Routine{}
				for _, x := range s.List() {
					if sees(r, x.Agent, false) {
						l = append(l, x)
					}
				}
				return map[string]any{"routines": l}, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/routines", Validate: validate,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b Routine
				_ = r.Decode(&b)
				if !sees(r, b.Agent, true) {
					return nil, &gateway.Error{Status: 403, Code: "forbidden", Message: "you cannot give this agent a routine"}
				}
				out, err := s.Create(b)
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return out, nil
			}})
		g.Handle(c, gateway.Route{Method: "PATCH", Path: "/api/routines/{id}",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if err := mine(r, true); err != nil {
					return nil, err
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
				if err := mine(r, true); err != nil {
					return nil, err
				}
				if err := s.Delete(r.Params["id"]); err != nil {
					return nil, gateway.NotFound()
				}
				return nil, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/routines/{id}/run", Devices: mobile,
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				if err := mine(r, true); err != nil {
					return nil, err
				}
				if err := s.RunNow(context.WithoutCancel(ctx), r.Params["id"]); err != nil {
					return nil, gateway.Conflict("%v", err)
				}
				return nil, nil
			}})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/routines/{id}/runs", Devices: mobile,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if err := mine(r, false); err != nil {
					return nil, err
				}
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
				pv := s.ComposeBrief()
				// what this person may see of it
				if a := acc(); a != nil {
					keep := func(b Brief) Brief {
						out := Brief{Time: b.Time, Folded: b.Folded, NeedsYou: []approval.Request{}, Attention: []Run{}, Failed: []AgentIssue{}}
						for _, q := range b.NeedsYou {
							if a.Session(r.DeviceID, q.Session) {
								out.NeedsYou = append(out.NeedsYou, q)
							}
						}
						for _, x := range b.Attention {
							if rt, ok := s.Get(x.Routine); ok && a.Agents(r.DeviceID, []string{rt.Agent}, false) {
								out.Attention = append(out.Attention, x)
							}
						}
						for _, x := range b.Failed {
							if a.Agents(r.DeviceID, []string{x.Agent}, false) {
								out.Failed = append(out.Failed, x)
							}
						}
						return out
					}
					pv = keep(pv)
					for i := range bs {
						bs[i] = keep(bs[i])
					}
				}
				return map[string]any{"preview": pv, "history": bs}, nil
			}})
		return nil
	},
})
