package plugins

import (
	"context"
	"errors"
	"strings"

	"github.com/hedeqiang/crewbase/bridge/process"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

func apiErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotInstalled):
		return gateway.NotFound()
	case errors.Is(err, ErrBadSource):
		return gateway.BadRequest("%v", err)
	}
	return gateway.Conflict("%v", err)
}

// APIPlugin exposes the plugin manager and marketplace on the gateway.
// Changing plugins is desktop-only; mobile devices may only look.
var APIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/plugin-manager-api",
	Inject:      []string{gateway.ServiceKey, ServiceKey},
	Description: "Remote methods for installing, approving and reloading plugins, and the marketplace.",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		m := kernel.Use[*Manager](c, ServiceKey)
		view := []string{"mobile"}

		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/plugins/installed", Devices: view,
			Handle: func(context.Context, *gateway.Request) (any, error) {
				return map[string]any{"plugins": m.Installed()}, nil
			}})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/market", Devices: view,
			Handle: func(ctx context.Context, _ *gateway.Request) (any, error) {
				ents, err := m.Market(ctx)
				if err != nil {
					return nil, gateway.Conflict("%v", err)
				}
				if ents == nil {
					ents = []MarketEntry{}
				}
				return map[string]any{"plugins": ents}, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/plugins/install",
			Validate: func(r *gateway.Request) error {
				var b InstallRequest
				if err := r.Decode(&b); err != nil {
					return err
				}
				if strings.TrimSpace(b.Source) == "" && b.Market == "" {
					return gateway.BadRequest("source or market is required")
				}
				return nil
			},
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				var b InstallRequest
				_ = r.Decode(&b)
				v, err := m.Install(context.WithoutCancel(ctx), b)
				return v, apiErr(err)
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/plugins/{name}/approve",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b struct {
					Permissions         *process.Permissions `json:"permissions"`
					TrustFutureVersions bool                 `json:"trustFutureVersions"`
				}
				if err := r.Decode(&b); err != nil {
					return nil, err
				}
				v, err := m.Approve(r.Params["name"], b.Permissions, b.TrustFutureVersions)
				return v, apiErr(err)
			}})
		for _, x := range []struct {
			path string
			on   bool
		}{{"/api/plugins/{name}/enable", true}, {"/api/plugins/{name}/disable", false}} {
			on := x.on
			g.Handle(c, gateway.Route{Method: "POST", Path: x.path,
				Handle: func(_ context.Context, r *gateway.Request) (any, error) {
					v, err := m.SetEnabled(r.Params["name"], on)
					return v, apiErr(err)
				}})
		}
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/plugins/{name}/reload",
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				v, err := m.Reload(context.WithoutCancel(ctx), r.Params["name"])
				return v, apiErr(err)
			}})
		g.Handle(c, gateway.Route{Method: "DELETE", Path: "/api/plugins/{name}",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				return nil, apiErr(m.Remove(r.Params["name"]))
			}})
		return nil
	},
})
