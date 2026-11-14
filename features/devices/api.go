package devices

import (
	"context"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// Config of the plugin.
type Config struct {
	Dir string `json:"dir"`
}

// limiter allows n calls per window across all callers (pairing codes are
// short-lived and 8 characters of a 31-letter alphabet; this only stops
// hammering).
type limiter struct {
	mu     sync.Mutex
	n      int
	window time.Duration
	hits   []time.Time
}

func (l *limiter) allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	keep := l.hits[:0]
	for _, t := range l.hits {
		if now.Sub(t) < l.window {
			keep = append(keep, t)
		}
	}
	l.hits = keep
	if len(l.hits) >= l.n {
		return false
	}
	l.hits = append(l.hits, now)
	return true
}

// Plugin pairs devices and authenticates their tokens on the gateway:
//
//	POST   /api/devices/pairing          {tier}         → Pairing (desktop)
//	DELETE /api/devices/pairing/{code}                  (desktop)
//	POST   /api/pair                     {code, name, platform} → Paired (public, rate-limited)
//	GET    /api/devices                                → {devices, pending}
//	PATCH  /api/devices/{id}             {name}
//	DELETE /api/devices/{id}                            revoke
//	GET    /api/whoami                                 → {tier, device?}
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/devices",
	Inject:      []string{gateway.ServiceKey},
	Description: "Pair phones and other computers with one-time codes; device tiers; revoke.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		s, err := New(cfg.Dir)
		if err != nil {
			return err
		}
		s.SetRoles(func() (RoleResolver, bool) { return kernel.Lookup[RoleResolver](c, "members") })
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		g.AddVerifier(c, func(tok string) (string, string, bool) {
			v, ok := s.Verify(tok)
			if !ok {
				return "", "", false
			}
			return v.Tier, v.ID, true
		})
		emit := func(kind string, v View) {
			Changed.Emit(c.Host(), Event{Kind: kind, Device: v})
			g.Hub().Publish("devices.changed", Event{Kind: kind, Device: v})
		}
		redeemLimit := &limiter{n: 20, window: time.Minute}
		everyone := []string{TierMobile, TierViewer, "im"}

		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/devices/pairing",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b struct {
					Tier   string `json:"tier"`
					Member string `json:"member"`
				}
				if err := r.Decode(&b); err != nil {
					return nil, err
				}
				p, err := s.StartPairingFor(b.Tier, b.Member)
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return p, nil
			}})
		g.Handle(c, gateway.Route{Method: "DELETE", Path: "/api/devices/pairing/{code}",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				s.CancelPairing(r.Params["code"])
				return nil, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/pair", Public: true, MaxBody: 4 << 10,
			// The code is the credential; Validate checks shape and rate
			// only (it must not consume the code).
			Validate: func(r *gateway.Request) error {
				var b struct {
					Code string `json:"code"`
				}
				if err := r.Decode(&b); err != nil {
					return err
				}
				if len(normCode(b.Code)) != 8 {
					return gateway.BadRequest("%v", ErrBadCode)
				}
				if !redeemLimit.allow(time.Now()) {
					return &gateway.Error{Status: 429, Code: "rate_limited", Message: "too many attempts; wait a minute"}
				}
				return nil
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b struct {
					Code     string `json:"code"`
					Name     string `json:"name"`
					Platform string `json:"platform"`
				}
				_ = r.Decode(&b)
				p, err := s.Redeem(b.Code, b.Name, b.Platform, r.HTTP.Header.Get("X-Crewbase-Via"))
				if err != nil {
					return nil, &gateway.Error{Status: 401, Code: "bad_code", Message: err.Error()}
				}
				emit("paired", p.Device)
				return p, nil
			}})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/devices",
			Handle: func(context.Context, *gateway.Request) (any, error) {
				return map[string]any{"devices": s.List(), "pending": s.Pending()}, nil
			}})
		g.Handle(c, gateway.Route{Method: "PATCH", Path: "/api/devices/{id}",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b struct {
					Name string `json:"name"`
				}
				if err := r.Decode(&b); err != nil {
					return nil, err
				}
				v, err := s.Rename(r.Params["id"], b.Name)
				if err == ErrNotFound {
					return nil, gateway.NotFound()
				}
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				emit("renamed", v)
				return v, nil
			}})
		g.Handle(c, gateway.Route{Method: "DELETE", Path: "/api/devices/{id}",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				v, err := s.Revoke(r.Params["id"])
				if err == ErrNotFound {
					return nil, gateway.NotFound()
				}
				if err != nil {
					return nil, err
				}
				g.Kick(v.ID)
				emit("revoked", v)
				return nil, nil
			}})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/whoami", Devices: everyone,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				out := map[string]any{"tier": r.Device}
				if r.DeviceID != "" {
					if v, ok := s.Get(r.DeviceID); ok {
						out["device"] = v
					}
				}
				return out, nil
			}})
		return c.Provide(ServiceKey, s)
	},
})
