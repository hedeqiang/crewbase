package google

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/catalog"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// Environment overrides of the Google origins (local fakes; see Config).
const (
	EnvOAuthBase = "CREWBASE_GOOGLE_OAUTH_BASE"
	EnvAPIBase   = "CREWBASE_GOOGLE_API_BASE"
)

// Changed fires when the connection changes (connected products, account,
// needs-login).
var Changed = kernel.NewEmit[struct{}]("google/changed")

// New builds a service outside a host (tests).
func New(cfg Config, cs *credentials.Service, reg *tools.Registry, c *kernel.Context) *Service {
	s := &Service{c: c, cfg: cfg, ep: endpointsFor(cfg), creds: cs, reg: reg, tokenRef: cfg.TokenRef}
	if s.tokenRef == "" {
		s.tokenRef = DefaultTokenRef
	}
	s.load()
	return s
}

// ---------------------------------------------------------------- catalog connector

// connector adapts the service to catalog.Connector (builtin "google").
type connector struct{ s *Service }

var _ catalog.Connector = connector{}

func (c connector) State(product string) catalog.ConnectorState {
	v := c.s.Status()
	st := catalog.ConnectorState{Configured: v.Configured, Account: v.Email, NeedsLogin: v.NeedsLogin}
	for _, p := range v.Products {
		if p.ID == product {
			st.Installed, st.Scope = p.Connected, p.Scope
		}
	}
	if !v.Configured {
		st.Note = catalog.Text{
			Zh: "此版本尚未设置 Google 登录（缺少 OAuth 客户端）。",
			En: "Google sign-in is not set up in this build (no OAuth client).",
		}
	}
	return st
}

func (c connector) Connect(product, scope string) (string, error) { return c.s.Connect(product, scope) }

func (c connector) Disconnect(ctx context.Context, product string) error {
	return c.s.Disconnect(ctx, product)
}

// ---------------------------------------------------------------- plugins

// Plugin is @crewbase/google: the service, the connected products' tools
// and the catalog connector for builtin "google" entries.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/google",
	Inject:      []string{tools.ServiceKey},
	Optional:    []string{credentials.ServiceKey},
	Description: "Built-in Google connector: Gmail, Calendar, Drive, Docs, Sheets and Slides with Google sign-in (no local install).",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		if cfg.OAuthBase == "" {
			cfg.OAuthBase = os.Getenv(EnvOAuthBase)
		}
		if cfg.APIBase == "" {
			cfg.APIBase = os.Getenv(EnvAPIBase)
		}
		reg := kernel.Use[*tools.Registry](c, tools.ServiceKey)
		s := New(cfg, nil, reg, c)
		s.changed = func() { Changed.Emit(c, struct{}{}) }
		if err := c.Provide(ServiceKey, s); err != nil {
			return err
		}
		if err := c.Provide(catalog.ConnectorKey("google"), catalog.Connector(connector{s})); err != nil {
			return err
		}
		s.syncTools()
		return nil
	},
})

type connectBody struct {
	Product string `json:"product"`
	Scope   string `json:"scope,omitempty"`
}

func apiErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrUnknown):
		return gateway.NotFound()
	case errors.Is(err, ErrNotConfigured), errors.Is(err, ErrNoRedirect):
		return &gateway.Error{Status: 409, Code: "not_configured", Message: err.Error()}
	case errors.Is(err, ErrState):
		return &gateway.Error{Status: 400, Code: "unknown_state", Message: err.Error()}
	}
	return err
}

// APIPlugin exposes the connector on the gateway. Connecting and
// disconnecting are desktop-only (they authorize a connector).
var APIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/google-api",
	Inject:      []string{gateway.ServiceKey, ServiceKey},
	Description: "Remote methods for the Google connector: status, connect (sign-in URL), sign-in callback, disconnect.",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		s := kernel.Use[*Service](c, ServiceKey)
		if a := g.Addr(); a != "" {
			s.SetRedirectBase(a)
		}
		gateway.Listening.Listen(c, func(a string) { s.SetRedirectBase(a) })
		// no account details in the push: clients refetch
		Changed.Listen(c, func(struct{}) { g.Hub().Publish("google", nil) })

		checkScope := func(scope string) error {
			if scope == "" || scope == "global" {
				return nil
			}
			if strings.HasPrefix(scope, "team:") {
				type teams interface{ Exists(id string) bool }
				if ts, ok := kernel.Lookup[teams](c, "teams"); ok && !ts.Exists(strings.TrimPrefix(scope, "team:")) {
					return gateway.BadRequest("unknown team %q", strings.TrimPrefix(scope, "team:"))
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

		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/google",
			Handle: func(context.Context, *gateway.Request) (any, error) { return s.Status(), nil }})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/google/connect",
			Validate: func(r *gateway.Request) error {
				var b connectBody
				if err := r.Decode(&b); err != nil {
					return err
				}
				if _, ok := productByID(b.Product); !ok {
					return gateway.BadRequest("unknown product %q", b.Product)
				}
				return checkScope(b.Scope)
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b connectBody
				_ = r.Decode(&b)
				u, err := s.Connect(b.Product, b.Scope)
				if err != nil {
					return nil, apiErr(err)
				}
				return map[string]string{"authUrl": u}, nil
			}})
		// Google redirects the browser here. The state is the credential:
		// only a waiting sign-in's state is accepted, once.
		g.Handle(c, gateway.Route{Method: "GET", Path: CallbackPath, Public: true,
			Validate: func(r *gateway.Request) error {
				if !s.Pending(r.HTTP.URL.Query().Get("state")) {
					return &gateway.Error{Status: 401, Code: "unknown_state", Message: "no Google sign-in is waiting for this response"}
				}
				return nil
			},
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				q := r.HTTP.URL.Query()
				err := s.Finish(ctx, q.Get("state"), q.Get("code"), q.Get("error"))
				res := "connected"
				if err != nil {
					res = "failed"
					c.Log().Warn("google: sign-in did not complete", "err", err)
				}
				return &gateway.Redirect{Path: "/#/settings/market?google=" + res}, nil
			}})
		// When the browser that signed in is not on this computer (relay),
		// its redirect to 127.0.0.1 fails; the person pastes that address.
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/google/finish",
			Validate: func(r *gateway.Request) error {
				var b struct {
					URL string `json:"url"`
				}
				return r.Decode(&b)
			},
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				var b struct {
					URL string `json:"url"`
				}
				_ = r.Decode(&b)
				if err := s.FinishURL(ctx, b.URL); err != nil {
					return nil, apiErr(err)
				}
				return s.Status(), nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/google/disconnect",
			Validate: func(r *gateway.Request) error {
				var b connectBody
				if err := r.Decode(&b); err != nil {
					return err
				}
				if _, ok := productByID(b.Product); b.Product != "" && !ok {
					return gateway.BadRequest("unknown product %q", b.Product)
				}
				return nil
			},
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				var b connectBody
				_ = r.Decode(&b)
				err := s.Disconnect(ctx, b.Product)
				return s.Status(), apiErr(err)
			}})
		return nil
	},
})
