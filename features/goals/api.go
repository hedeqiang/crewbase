package goals

import (
	"context"
	"errors"

	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

type putBody struct {
	Objective string `json:"objective"`
	MaxRounds int    `json:"maxRounds"`
	Agent     string `json:"agent"`
	// Authorize defaults to true: a person setting a goal through the
	// desktop API is the authorisation. Send false to set it paused-for-review.
	Authorize *bool `json:"authorize"`
}

func apiErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNoGoal):
		return gateway.NotFound()
	case errors.Is(err, ErrComplete):
		return gateway.Conflict("%v", err)
	case errors.Is(err, ErrInvalid):
		return gateway.BadRequest("%v", err)
	}
	return err
}

// APIPlugin exposes goals on the gateway. Viewing and pausing are allowed
// from mobile devices; setting, authorising and resuming automation is
// desktop only.
var APIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/goals-api",
	Inject:      []string{gateway.ServiceKey, ServiceKey},
	Description: "Remote methods for session goals (get, set, authorize, pause, resume).",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		s := kernel.Use[*Service](c, ServiceKey)
		mobile := []string{"mobile", "im"}
		exists := func(r *gateway.Request) error {
			if _, err := s.session(r.Params["id"]); err != nil {
				return gateway.NotFound()
			}
			return nil
		}
		by := func(r *gateway.Request) string { return "api:" + r.Device }
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/sessions/{id}/goal", Devices: mobile,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if err := exists(r); err != nil {
					return nil, err
				}
				goal, err := s.Get(r.Params["id"])
				if err != nil {
					return nil, gateway.NotFound()
				}
				return map[string]any{"goal": goal}, nil
			}})
		g.Handle(c, gateway.Route{Method: "PUT", Path: "/api/sessions/{id}/goal",
			Validate: func(r *gateway.Request) error {
				var b putBody
				if err := r.Decode(&b); err != nil {
					return err
				}
				if b.Objective == "" {
					return gateway.BadRequest("objective is required")
				}
				if b.MaxRounds < 0 {
					return gateway.BadRequest("maxRounds must be positive")
				}
				return nil
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if err := exists(r); err != nil {
					return nil, err
				}
				var b putBody
				_ = r.Decode(&b)
				auth := b.Authorize == nil || *b.Authorize
				goal, err := s.Set(r.Params["id"], SetOptions{Objective: b.Objective, MaxRounds: b.MaxRounds, Agent: b.Agent, Authorize: auth, By: by(r)})
				if err != nil {
					return nil, apiErr(err)
				}
				return map[string]any{"goal": goal}, nil
			}})
		g.Handle(c, gateway.Route{Method: "DELETE", Path: "/api/sessions/{id}/goal",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if err := exists(r); err != nil {
					return nil, err
				}
				return nil, apiErr(s.Clear(r.Params["id"], by(r)))
			}})
		for _, x := range []struct {
			action  string
			devices []string
			fn      func(string, string) (*Goal, error)
		}{
			{"authorize", nil, s.Authorize},
			{"resume", nil, s.Resume},
			{"pause", mobile, s.Pause},
		} {
			g.Handle(c, gateway.Route{Method: "POST", Path: "/api/sessions/{id}/goal/" + x.action, Devices: x.devices,
				Handle: func(_ context.Context, r *gateway.Request) (any, error) {
					if err := exists(r); err != nil {
						return nil, err
					}
					goal, err := x.fn(r.Params["id"], by(r))
					if err != nil {
						return nil, apiErr(err)
					}
					return map[string]any{"goal": goal}, nil
				}})
		}
		return nil
	},
})
