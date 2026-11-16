package computertools

import (
	"context"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/computer"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// AppRow is one app of the settings table.
type AppRow struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Running bool   `json:"running"`
	// Blocked: blocked for everyone (default list or the person's);
	// Browser: refused in favour of the browser_* tools.
	Blocked bool `json:"blocked,omitempty"`
	Browser bool `json:"browser,omitempty"`
	// Perms are the per-agent settings ("ask" is not listed: it is the default).
	Perms map[string]string `json:"perms"`
}

// Overview is GET /api/computer.
type Overview struct {
	Available   bool      `json:"available"`
	Platform    string    `json:"platform"`
	Enabled     bool      `json:"enabled"`
	Permissions any       `json:"permissions"`
	Apps        []AppRow  `json:"apps"`
	Blocks      BlockList `json:"blocks"`
	State       State     `json:"state"`
	AppsError   string    `json:"appsError,omitempty"`
	Checked     time.Time `json:"checked"`
}

// Overview gathers the settings page.
func (s *Service) Overview(ctx context.Context) Overview {
	out := Overview{Available: true, Platform: runtime.GOOS, Enabled: s.Enabled(), Permissions: s.Permissions(),
		Apps: []AppRow{}, Blocks: s.Blocks(), State: s.State(), Checked: time.Now().UTC()}
	rows := map[string]*AppRow{}
	names := s.Names()
	row := func(id string) *AppRow {
		if r := rows[id]; r != nil {
			return r
		}
		n := names[id]
		if n == "" {
			n = id
		}
		r := &AppRow{ID: id, Name: n, Perms: map[string]string{}}
		rows[id] = r
		return r
	}
	apps, err := s.runningApps(ctx, true)
	if err != nil {
		out.AppsError = err.Error()
	}
	for _, a := range apps {
		r := row(a.ID)
		r.Name, r.Running = a.Name, true
	}
	for ag, m := range s.AgentPermissions() {
		for app, p := range m {
			row(app).Perms[ag] = p
		}
	}
	for _, r := range rows {
		r.Browser = IsBrowser(r.ID, r.Name)
		r.Blocked = s.blockedWhy(appOf(r)) != ""
		out.Apps = append(out.Apps, *r)
	}
	sort.Slice(out.Apps, func(i, j int) bool {
		a, b := out.Apps[i], out.Apps[j]
		if a.Running != b.Running {
			return a.Running
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return out
}

// APIPlugin exposes computer use to people: the settings page, stop and
// resume (any device may stop), and the live state for the desktop
// shell's indicator (SSE "computer.state").
var APIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/computer-api",
	Inject:      []string{gateway.ServiceKey, ServiceKey},
	Optional:    []string{agent.ServiceKey},
	Description: "Computer use API: settings (switch, per-agent app permissions, block list), stop / resume, live state.",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		s := kernel.Use[*Service](c, ServiceKey)
		mobile := []string{"mobile", "im"}
		knownAgent := func(id string) error {
			if reg, ok := kernel.Lookup[*agent.Registry](c, agent.ServiceKey); ok {
				if _, ok := reg.Get(id); !ok {
					return gateway.NotFound()
				}
			}
			return nil
		}

		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/computer",
			Handle: func(ctx context.Context, _ *gateway.Request) (any, error) { return s.Overview(ctx), nil }})
		g.Handle(c, gateway.Route{Method: "PATCH", Path: "/api/computer",
			Validate: func(r *gateway.Request) error {
				var in struct {
					Enabled *bool `json:"enabled"`
				}
				if err := r.Decode(&in); err != nil {
					return err
				}
				if in.Enabled == nil {
					return gateway.BadRequest("enabled (true or false) is required")
				}
				return nil
			},
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				var in struct {
					Enabled bool `json:"enabled"`
				}
				_ = r.Decode(&in)
				if err := s.SetEnabled(in.Enabled); err != nil {
					return nil, err
				}
				return s.Overview(ctx), nil
			}})
		g.Handle(c, gateway.Route{Method: "PUT", Path: "/api/computer/permissions",
			Validate: func(r *gateway.Request) error {
				var in struct {
					Agent, App, Permission string
				}
				if err := r.Decode(&in); err != nil {
					return err
				}
				if in.Agent == "" || in.App == "" {
					return gateway.BadRequest("agent and app are required")
				}
				switch in.Permission {
				case PermAllow, PermAsk, PermBlock:
				default:
					return gateway.BadRequest("permission must be allow, ask or block")
				}
				return knownAgent(in.Agent)
			},
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				var in struct {
					Agent, App, Permission string
				}
				_ = r.Decode(&in)
				if err := s.SetPermission(in.Agent, in.App, in.Permission); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return s.Overview(ctx), nil
			}})
		g.Handle(c, gateway.Route{Method: "PUT", Path: "/api/computer/blocked",
			Validate: func(r *gateway.Request) error {
				var in struct {
					Key     string `json:"key"`
					Blocked *bool  `json:"blocked"`
				}
				if err := r.Decode(&in); err != nil {
					return err
				}
				if strings.TrimSpace(in.Key) == "" || in.Blocked == nil {
					return gateway.BadRequest("key and blocked are required")
				}
				return nil
			},
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				var in struct {
					Key     string `json:"key"`
					Blocked bool   `json:"blocked"`
				}
				_ = r.Decode(&in)
				if err := s.SetBlocked(in.Key, in.Blocked); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return s.Overview(ctx), nil
			}})
		// Stop is open to every device: stopping is always safe.
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/computer/stop", Devices: mobile,
			Handle: func(context.Context, *gateway.Request) (any, error) {
				s.StopAll()
				return s.State(), nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/agents/{id}/computer/stop", Devices: mobile,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if err := knownAgent(r.Params["id"]); err != nil {
					return nil, err
				}
				s.Stop(r.Params["id"])
				return s.State(), nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/computer/resume",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var in struct {
					Agent string `json:"agent"`
				}
				if len(r.Body) > 0 {
					if err := r.Decode(&in); err != nil {
						return nil, err
					}
				}
				s.Resume(in.Agent)
				return s.State(), nil
			}})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/computer/state", Devices: mobile,
			Handle: func(context.Context, *gateway.Request) (any, error) { return s.State(), nil }})

		Changed.Listen(c, func(st State) { g.Hub().Publish("computer.state", st) })
		return nil
	},
})

func appOf(r *AppRow) computer.App { return computer.App{ID: r.ID, Name: r.Name} }
