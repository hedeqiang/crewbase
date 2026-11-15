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
	case errors.Is(err, ErrUnknown), errors.Is(err, ErrMissing), errors.Is(err, ErrNoConnector):
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
		return nil
	},
})
