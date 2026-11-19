package catalog

import (
	"context"
	"errors"
	"strings"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/features/mcp"
	"github.com/hedeqiang/crewbase/kernel"
)

func apiErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrUnknown), errors.Is(err, ErrMissing):
		return gateway.NotFound()
	case errors.Is(err, ErrInstalled), errors.Is(err, mcp.ErrExists):
		return gateway.Conflict("%v", err)
	case errors.Is(err, ErrInput), errors.Is(err, mcp.ErrInvalid):
		return gateway.BadRequest("%v", err)
	}
	return err
}

// APIPlugin exposes the catalog on the gateway (desktop only: installing
// a preset authorizes a connector, like POST /api/mcp).
var APIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/catalog-api",
	Inject:      []string{gateway.ServiceKey, ServiceKey},
	Description: "Remote methods for the built-in catalog: list with installed state, install, uninstall.",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		s := kernel.Use[*Service](c, ServiceKey)
		// checkScope rejects scopes naming an agent or team that does not
		// exist (the mcp service would otherwise wait for it forever).
		checkScope := func(scope string, skill bool) error {
			if scope == "" || scope == mcp.ScopeGlobal {
				return nil
			}
			if strings.HasPrefix(scope, mcp.TeamScope) {
				if skill {
					return gateway.BadRequest("skills are installed for everyone or for one agent")
				}
				type teams interface{ Exists(id string) bool }
				if ts, ok := kernel.Lookup[teams](c, "teams"); ok && !ts.Exists(strings.TrimPrefix(scope, mcp.TeamScope)) {
					return gateway.BadRequest("unknown team %q", strings.TrimPrefix(scope, mcp.TeamScope))
				}
				return nil
			}
			if reg, ok := kernel.Lookup[*agent.Registry](c, agent.ServiceKey); ok {
				if _, ok := reg.Get(scope); !ok {
					return gateway.BadRequest("unknown agent %q", scope)
				}
			}
			return nil
		}
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/catalog",
			Handle: func(context.Context, *gateway.Request) (any, error) {
				return s.List(), nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/catalog/{id}/install",
			Validate: func(r *gateway.Request) error {
				var b Request
				if err := r.Decode(&b); err != nil {
					return err
				}
				_, isSkill := skillInfo(r.Params["id"])
				if _, ok := preset(r.Params["id"]); !ok && !isSkill {
					return gateway.NotFound()
				}
				if !isSkill {
					if err := checkScope(b.Context, false); err != nil {
						return err
					}
				}
				return checkScope(b.Scope, isSkill)
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b Request
				_ = r.Decode(&b)
				v, err := s.Install(r.Params["id"], b)
				return v, apiErr(err)
			}})
		// ?agent=<id> removes a skill installed for that agent only.
		g.Handle(c, gateway.Route{Method: "DELETE", Path: "/api/catalog/{id}",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				return nil, apiErr(s.Uninstall(r.Params["id"], r.HTTP.URL.Query().Get("agent")))
			}})
		// Built-in sign-in (GitHub device flow): starts it, reports it, stops it.
		checkSignIn := func(r *gateway.Request) error {
			p, ok := preset(r.Params["id"])
			if !ok {
				return gateway.NotFound()
			}
			if p.SignIn == "" || s.gh == nil {
				return gateway.BadRequest("this item has no sign-in")
			}
			return nil
		}
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/catalog/{id}/signin",
			Validate: func(r *gateway.Request) error {
				if err := checkSignIn(r); err != nil {
					return err
				}
				var b Request
				if err := r.Decode(&b); err != nil {
					return err
				}
				if err := checkScope(b.Context, false); err != nil {
					return err
				}
				return checkScope(b.Scope, false)
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b Request
				_ = r.Decode(&b)
				if s.gh.clientID == "" {
					return nil, gateway.BadRequest("%v", ErrNoSignIn)
				}
				v, err := s.gh.Start(r.Params["id"], s.ConnectorScope(b))
				if err != nil && !errors.Is(err, ErrUnknown) && !errors.Is(err, ErrNoSignIn) {
					// GitHub refused or could not be reached: shown in the dialog
					return nil, &gateway.Error{Status: 502, Code: "signin_failed", Message: err.Error()}
				}
				if errors.Is(err, ErrNoSignIn) {
					return nil, gateway.BadRequest("%v", err)
				}
				return v, apiErr(err)
			}})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/catalog/{id}/signin",
			Validate: checkSignIn,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if v, ok := s.gh.View(r.Params["id"]); ok {
					return v, nil
				}
				return nil, gateway.NotFound()
			}})
		g.Handle(c, gateway.Route{Method: "DELETE", Path: "/api/catalog/{id}/signin",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if s.gh == nil || !s.gh.Cancel(r.Params["id"]) {
					return nil, gateway.NotFound()
				}
				return nil, nil
			}})
		return nil
	},
})
