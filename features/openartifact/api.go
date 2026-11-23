package openartifact

import (
	"context"
	"time"

	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// APIPlugin is Settings › Open Artifact (desktop only: it installs and runs
// a program on this computer).
//
//	GET  /api/open-artifact            status
//	GET  /api/open-artifact/latest     the newest version on npm
//	POST /api/open-artifact/install    {version?}  install (or update) and turn on
//	POST /api/open-artifact/enabled    {enabled}
var APIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/open-artifact-api",
	Inject:      []string{gateway.ServiceKey, ServiceKey},
	Description: "Settings › Open Artifact: install, update, turn on and off.",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		s := kernel.Use[*Service](c, ServiceKey)
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/open-artifact",
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) { return s.Status(ctx), nil }})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/open-artifact/latest",
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				v, err := s.Latest(ctx)
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return map[string]string{"version": v}, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/open-artifact/install",
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				var b struct {
					Version string `json:"version"`
				}
				if err := r.Decode(&b); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				// the download goes on if the page is closed
				ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
				defer cancel()
				st, err := s.Install(ctx, b.Version)
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return st, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/open-artifact/enabled",
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				var b struct {
					Enabled bool `json:"enabled"`
				}
				if err := r.Decode(&b); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				if err := s.SetEnabled(b.Enabled); err != nil {
					return nil, err
				}
				return s.Status(ctx), nil
			}})
		return nil
	},
})
