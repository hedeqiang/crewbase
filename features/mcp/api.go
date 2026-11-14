package mcp

import (
	"context"
	"errors"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// Patch is the PATCH /api/mcp/{name} body; absent fields are unchanged.
// Env/header values echoed back as "********" keep their current value.
type Patch struct {
	Enabled   *bool              `json:"enabled"`
	Transport *string            `json:"transport"`
	Command   *[]string          `json:"command"`
	Env       *map[string]string `json:"env"`
	URL       *string            `json:"url"`
	Headers   *map[string]string `json:"headers"`
	Scope     *string            `json:"scope"`
	Trust     *string            `json:"trust"`
	// TrustAnnotations / Categories: see Server.
	TrustAnnotations *bool              `json:"trustAnnotations"`
	Categories       *map[string]string `json:"categories"`
}

// Apply applies p to s.
func (p Patch) Apply(s *Server) {
	if p.Enabled != nil {
		s.Enabled = *p.Enabled
	}
	if p.Transport != nil {
		s.Transport = *p.Transport
	}
	if p.Command != nil {
		s.Command = *p.Command
	}
	if p.Env != nil {
		s.Env = *p.Env
	}
	if p.URL != nil {
		s.URL = *p.URL
	}
	if p.Headers != nil {
		s.Headers = *p.Headers
	}
	if p.Scope != nil {
		s.Scope = *p.Scope
	}
	if p.Trust != nil {
		s.Trust = *p.Trust
	}
	if p.TrustAnnotations != nil {
		s.TrustAnnotations = *p.TrustAnnotations
	}
	if p.Categories != nil {
		s.Categories = *p.Categories
	}
}

func apiErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound):
		return gateway.NotFound()
	case errors.Is(err, ErrExists), errors.Is(err, ErrDeclared):
		return gateway.Conflict("%v", err)
	case errors.Is(err, ErrInvalid):
		return gateway.BadRequest("%v", err)
	}
	return err
}

// APIPlugin exposes MCP server management on the gateway (desktop only:
// adding a server authorizes a connector).
var APIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/mcp-api",
	Inject:      []string{gateway.ServiceKey, ServiceKey},
	Description: "Remote methods for MCP servers: list, add, edit, enable/disable, probe, tools.",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		s := kernel.Use[*Service](c, ServiceKey)
		checkScope := func(scope string) error {
			if scope == "" || scope == ScopeGlobal {
				return nil
			}
			if reg, ok := kernel.Lookup[*agent.Registry](c, agent.ServiceKey); ok {
				if _, ok := reg.Get(scope); !ok {
					return gateway.BadRequest("unknown agent %q", scope)
				}
			}
			return nil
		}
		StatusChanged.Listen(c, func(ev StatusEvent) { g.Hub().Publish("mcp", ev) })

		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/mcp",
			Handle: func(context.Context, *gateway.Request) (any, error) {
				return map[string]any{"servers": s.List()}, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/mcp",
			Validate: func(r *gateway.Request) error {
				var b Server
				if err := r.Decode(&b); err != nil {
					return err
				}
				if _, err := b.Normalize(); err != nil {
					return apiErr(err)
				}
				return checkScope(b.Scope)
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b Server
				_ = r.Decode(&b)
				v, err := s.Add(b)
				return v, apiErr(err)
			}})
		g.Handle(c, gateway.Route{Method: "PATCH", Path: "/api/mcp/{name}",
			Validate: func(r *gateway.Request) error {
				var p Patch
				if err := r.Decode(&p); err != nil {
					return err
				}
				if p.Scope != nil {
					return checkScope(*p.Scope)
				}
				return nil
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var p Patch
				_ = r.Decode(&p)
				v, err := s.Update(r.Params["name"], p.Apply)
				return v, apiErr(err)
			}})
		g.Handle(c, gateway.Route{Method: "DELETE", Path: "/api/mcp/{name}",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				return nil, apiErr(s.Remove(r.Params["name"]))
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/mcp/{name}/probe",
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				list, err := s.ProbeNamed(ctx, r.Params["name"])
				if errors.Is(err, ErrNotFound) {
					return nil, gateway.NotFound()
				}
				if err != nil {
					// A failed probe is a result, not an API error.
					return map[string]any{"ok": false, "error": err.Error(), "tools": []ToolInfo{}}, nil
				}
				return map[string]any{"ok": true, "tools": list}, nil
			}})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/mcp/{name}/tools",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				list, ok := s.Tools(r.Params["name"])
				if !ok {
					return nil, gateway.NotFound()
				}
				st, _ := s.Status(r.Params["name"])
				return map[string]any{"tools": list, "status": st}, nil
			}})
		return nil
	},
})
