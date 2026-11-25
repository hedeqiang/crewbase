package proxy

import (
	"context"
	"errors"
	"os"

	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/features/audit"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/internal/netproxy"
	"github.com/hedeqiang/crewbase/kernel"
)

// Config of @crewbase/proxy.
type Config struct {
	Dir string `json:"dir"` // the data directory (the setting in <dir>/proxy.json)
}

// Plugin is @crewbase/proxy. It loads right after the vault, before
// anything that goes online.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/proxy",
	Inject:      []string{credentials.ServiceKey},
	Description: "Settings › 网络代理: an HTTP, HTTPS or SOCKS5 proxy for every outbound connection (local addresses direct).",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		dir := cfg.Dir
		if dir == "" {
			if dir, err = os.MkdirTemp("", "crewbase-proxy-"); err != nil {
				return err
			}
		}
		s, err := Open(dir, func() (*credentials.Service, bool) {
			return kernel.Lookup[*credentials.Service](c, credentials.ServiceKey)
		})
		if err != nil {
			return err
		}
		c.Defer(func() { netproxy.Set(nil) })
		return c.Provide(ServiceKey, s)
	},
})

// APIPlugin is the 网络代理 page's API.
//
//	GET  /api/settings/proxy          {settings, env}   env: the environment's proxy (used while off)
//	PUT  /api/settings/proxy          Input → Settings
//	POST /api/settings/proxy/test     Input → Check     (not saved)
//	GET  /api/settings/proxy/detect   {found}           system proxy, environment, local proxy ports
var APIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/proxy-api",
	Inject:      []string{gateway.ServiceKey, ServiceKey},
	Description: "The 网络代理 page's API.",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		s := kernel.Use[*Service](c, ServiceKey)
		type view struct {
			Settings Settings `json:"settings"`
			Env      string   `json:"env,omitempty"`
		}
		bad := func(err error) error {
			if errors.Is(err, ErrInput) {
				return gateway.BadRequest("%v", err)
			}
			return err
		}
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/settings/proxy",
			Handle: func(context.Context, *gateway.Request) (any, error) {
				return view{Settings: s.Settings(), Env: EnvProxy()}, nil
			}})
		g.Handle(c, gateway.Route{Method: "PUT", Path: "/api/settings/proxy",
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				var in Input
				if err := r.Decode(&in); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				st, err := s.Set(ctx, in)
				if err != nil {
					return nil, bad(err)
				}
				if a, ok := kernel.Lookup[*audit.Service](c, audit.ServiceKey); ok {
					actor := "person:owner"
					if r.DeviceID != "" {
						actor = "person:" + r.Device
					}
					_, _ = a.Record(audit.Entry{Type: "settings/proxy", Actor: actor,
						Data: map[string]any{"enabled": st.Enabled, "type": st.Type, "host": st.Host, "port": st.Port}})
				}
				return view{Settings: st, Env: EnvProxy()}, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/settings/proxy/test",
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				var in Input
				if err := r.Decode(&in); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				out, err := s.Test(ctx, in)
				if err != nil {
					return nil, bad(err)
				}
				return out, nil
			}})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/settings/proxy/detect",
			Handle: func(ctx context.Context, _ *gateway.Request) (any, error) {
				return map[string]any{"found": Detect(ctx)}, nil
			}})
		return nil
	},
})
