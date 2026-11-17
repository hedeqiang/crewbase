// Package subscriptions exposes the management of subscription CLIs (the
// llm-acp adapters: Claude Code, Codex, Antigravity, Cursor CLI, custom
// ACP agents) on the gateway, for settings › 模型与订阅. The work is done
// by the acp service (providers/llm/acp, manage.go); changes apply without
// a restart.
//
//	GET    /api/subscriptions               every known CLI (disabled ones too)
//	POST   /api/subscriptions               add a custom CLI {id, name, command, env}
//	PATCH  /api/subscriptions/{id}          edit {name?, command?, env?, enabled?}
//	POST   /api/subscriptions/{id}/refresh  detect again and learn its models now
//	DELETE /api/subscriptions/{id}          custom: delete; built-in: disable
package subscriptions

import (
	"context"
	"errors"

	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/acp"
)

// Plugin registers the routes (it waits for the gateway and the acp
// service).
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/subscriptions-api",
	Inject:      []string{gateway.ServiceKey, acp.ServiceKey},
	Description: "Remote methods for managing subscription CLIs.",
	Apply: func(c *kernel.Context) error {
		register(c, kernel.Use[*gateway.Gateway](c, gateway.ServiceKey), kernel.Use[*acp.Service](c, acp.ServiceKey))
		return nil
	},
})

func register(c *kernel.Context, g *gateway.Gateway, s *acp.Service) {
	g.Handle(c, gateway.Route{Method: "GET", Path: "/api/subscriptions",
		Handle: func(ctx context.Context, _ *gateway.Request) (any, error) {
			return map[string]any{"subscriptions": s.List(ctx)}, nil
		}})
	g.Handle(c, gateway.Route{Method: "POST", Path: "/api/subscriptions",
		Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
			var in acp.NewCLI
			if err := r.Decode(&in); err != nil {
				return nil, err
			}
			v, err := s.Add(ctx, in)
			if err != nil {
				return nil, gateway.BadRequest("%s", err.Error())
			}
			return v, nil
		}})
	g.Handle(c, gateway.Route{Method: "PATCH", Path: "/api/subscriptions/{id}",
		Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
			var pt acp.Patch
			if err := r.Decode(&pt); err != nil {
				return nil, err
			}
			v, err := s.Update(ctx, r.Params["id"], pt)
			return v, apiErr(err)
		}})
	g.Handle(c, gateway.Route{Method: "POST", Path: "/api/subscriptions/{id}/refresh",
		Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
			v, err := s.Refresh(ctx, r.Params["id"])
			if errors.Is(err, acp.ErrNotFound) {
				return nil, gateway.NotFound()
			}
			// a failed probe still returns the CLI (its error says why)
			if err != nil && v.ID == "" {
				return nil, gateway.BadRequest("%s", err.Error())
			}
			return v, nil
		}})
	g.Handle(c, gateway.Route{Method: "DELETE", Path: "/api/subscriptions/{id}",
		Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
			deleted, err := s.Remove(ctx, r.Params["id"])
			if err != nil {
				return nil, apiErr(err)
			}
			return map[string]any{"deleted": deleted, "disabled": !deleted}, nil
		}})
}

func apiErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, acp.ErrNotFound):
		return gateway.NotFound()
	default:
		return gateway.BadRequest("%s", err.Error())
	}
}
