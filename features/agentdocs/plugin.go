package agentdocs

import (
	"context"
	"net/http"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/features/mcp"
	"github.com/hedeqiang/crewbase/kernel"
)

// Config of the plugin.
type Config struct {
	Dir string `json:"dir"`
}

// Plugin is @crewbase/agentdocs.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/agentdocs",
	Inject:      []string{agent.ServiceKey, mcp.ServiceKey, credentials.ServiceKey},
	Description: "Open Agent Docs: pages people and agents write together; each agent gets its own identity, and comments that call it become its tasks.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		s := &Service{c: c, dir: cfg.Dir, agents: kernel.Use[*agent.Registry](c, agent.ServiceKey), client: &http.Client{Timeout: 70 * time.Second}}
		s.load()
		c.Defer(s.stopListeners)
		c.Defer(s.stopLocal)
		// new agents get their identity; deleted ones lose it
		resync := func() {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				s.Sync(ctx)
			}()
		}
		agent.Created.Listen(c, func(_ context.Context, _ *agent.Agent) (struct{}, error) {
			resync()
			return struct{}{}, nil
		})
		agent.Deleted.Listen(c, func(string) { resync() })
		c.Go(func(ctx context.Context) {
			// after start: the agents are loaded
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			s.resumeLocal(ctx)
			s.Sync(ctx)
		})
		return c.Provide(ServiceKey, s)
	},
})

// APIPlugin is the settings page's API:
//
//	GET    /api/agentdocs            status
//	POST   /api/agentdocs            {url, token, workspace?, agents?, wake?} connect
//	PATCH  /api/agentdocs            {agents?, wake?}
//	POST   /api/agentdocs/sync       set agents up again
//	POST   /api/agentdocs/local      {email, password} 一键启动: install, run and connect a local server
//	POST   /api/agentdocs/local/update   move it to the latest release
//	POST   /api/agentdocs/embed      {url, signedIn}; signs this browser in when Crewbase holds the session
//	DELETE /api/agentdocs            disconnect (identities revoked; a local server stops, its data stays)
var APIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/agentdocs-api",
	Inject:      []string{gateway.ServiceKey, ServiceKey},
	Description: "Settings API of the Open Agent Docs connection.",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		s := kernel.Use[*Service](c, ServiceKey)
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/agentdocs",
			Handle: func(context.Context, *gateway.Request) (any, error) { return s.Status(), nil }})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/agentdocs",
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				var b ConnectRequest
				if err := r.Decode(&b); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				st, err := s.Connect(ctx, b)
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return st, nil
			}})
		g.Handle(c, gateway.Route{Method: "PATCH", Path: "/api/agentdocs",
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				var b struct {
					Agents *[]string `json:"agents"`
					Wake   *bool     `json:"wake"`
				}
				if err := r.Decode(&b); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				st, err := s.UpdateSettings(ctx, b.Agents, b.Wake)
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return st, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/agentdocs/local",
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				var b StartLocalRequest
				if err := r.Decode(&b); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 6*time.Minute)
				defer cancel()
				st, err := s.StartLocal(ctx, b)
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return st, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/agentdocs/local/update",
			Handle: func(ctx context.Context, _ *gateway.Request) (any, error) {
				ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 6*time.Minute)
				defer cancel()
				st, err := s.UpdateLocal(ctx)
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return st, nil
			}})
		// desktop only: the session goes to the browser of the computer the docs run on
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/agentdocs/embed",
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				e, ck := s.EmbedFor(ctx, r.HTTP)
				rp := &gateway.Reply{Data: e}
				if ck != nil {
					rp.Cookies = []*http.Cookie{ck}
				}
				return rp, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/agentdocs/sync",
			Handle: func(ctx context.Context, _ *gateway.Request) (any, error) {
				s.Sync(ctx)
				return s.Status(), nil
			}})
		g.Handle(c, gateway.Route{Method: "DELETE", Path: "/api/agentdocs",
			Handle: func(ctx context.Context, _ *gateway.Request) (any, error) {
				if err := s.Disconnect(ctx); err != nil {
					return nil, err
				}
				return s.Status(), nil
			}})
		return nil
	},
})
