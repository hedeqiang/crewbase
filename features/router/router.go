// Package router is router-default: each agent has an ordered model list;
// when a model is rate-limited, out of quota or failing, the next available
// model takes over (M-4). Exhausted models are skipped until their reset
// time; per open question #2 the preferred model is only re-selected at the
// start of a new turn (never mid-task).
package router

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/kernel"
)

// Exhausted describes the failure that triggered failover.
type Exhausted struct {
	Agent  *agent.Agent
	Failed llm.ModelRef
	Cause  error
}

// QuotaExhausted lets plugins pick the replacement model (Bail). The default
// listener walks the agent's model order.
var QuotaExhausted = kernel.NewBail[Exhausted, llm.ModelRef]("router/quota-exhausted")

// ModelState is the router's view of one model.
type ModelState struct {
	Model  string    `json:"model"`
	Until  time.Time `json:"until"`
	Reason string    `json:"reason"`
}

// Router implements agentloop.Router.
type Router struct {
	c    *kernel.Context
	llms *llm.Registry
	now  func() time.Time

	mu        sync.Mutex
	exhausted map[string]ModelState
	switches  int
	routes    []Route
}

func (r *Router) usable(ref llm.ModelRef) bool {
	if _, ok := r.llms.Get(ref.Provider); !ok {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.exhausted[ref.String()]
	return !ok || r.now().After(st.Until)
}

// Select returns the first usable model in the agent's order (falling back
// to the first model so the failure is reported rather than hidden).
func (r *Router) Select(a *agent.Agent) (llm.ModelRef, bool) {
	ms := a.Spec().Models
	if len(ms) == 0 {
		return llm.ModelRef{}, false
	}
	for _, m := range ms {
		if r.usable(m) && chatID(m) {
			return m, true
		}
	}
	return ms[0], true
}

// chatID: an agent's order is for conversation, so a model whose id says
// it only draws or films (left there by hand) is skipped.
func chatID(m llm.ModelRef) bool { return llm.ClassifyModel(m.Model) == llm.KindChat }

func backoff(err error, now time.Time) (time.Time, string) {
	var le *llm.Error
	kind := llm.KindOf(err)
	if e, ok := err.(*llm.Error); ok {
		le = e
	}
	switch kind {
	case llm.ErrQuotaExhausted:
		if le != nil && !le.ResetsAt.IsZero() {
			return le.ResetsAt, string(kind)
		}
		return now.Add(time.Hour), string(kind)
	case llm.ErrRateLimited:
		if le != nil && le.RetryAfter > 0 {
			return now.Add(le.RetryAfter), string(kind)
		}
		return now.Add(time.Minute), string(kind)
	case llm.ErrAuth, llm.ErrModelNotFound:
		return now.Add(10 * time.Minute), string(kind)
	default:
		return now.Add(30 * time.Second), string(kind)
	}
}

// Failover marks ref exhausted and picks the next model.
func (r *Router) Failover(ctx context.Context, a *agent.Agent, ref llm.ModelRef, cause error) (llm.ModelRef, bool) {
	until, reason := backoff(cause, r.now())
	r.mu.Lock()
	r.exhausted[ref.String()] = ModelState{Model: ref.String(), Until: until, Reason: reason}
	r.mu.Unlock()
	next, ok := QuotaExhausted.Run(kernel.For(r.c, a.ID()), ctx, Exhausted{Agent: a, Failed: ref, Cause: cause})
	if ok {
		r.mu.Lock()
		r.switches++
		r.mu.Unlock()
	}
	return next, ok
}

func (r *Router) nextInOrder(a *agent.Agent, failed llm.ModelRef) (llm.ModelRef, bool) {
	ms := a.Spec().Models
	start := 0
	for i, m := range ms {
		if m.String() == failed.String() {
			start = i + 1
			break
		}
	}
	// after the failed one first, then wrap to earlier ones
	for i := 0; i < len(ms); i++ {
		m := ms[(start+i)%len(ms)]
		if m.String() != failed.String() && r.usable(m) && chatID(m) {
			return m, true
		}
	}
	return llm.ModelRef{}, false
}

// States lists currently exhausted models.
func (r *Router) States() []ModelState {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []ModelState
	for _, s := range r.exhausted {
		if r.now().Before(s.Until) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

// Switches counts automatic switches since start.
func (r *Router) Switches() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.switches
}

// Reset clears a model's exhausted state (e.g. "re-check" in settings).
func (r *Router) Reset(model string) {
	r.mu.Lock()
	delete(r.exhausted, model)
	r.mu.Unlock()
}

// New creates a router (exported for tests).
func New(c *kernel.Context, llms *llm.Registry, now func() time.Time) *Router {
	if now == nil {
		now = time.Now
	}
	return &Router{c: c, llms: llms, now: now, exhausted: map[string]ModelState{}}
}

// Plugin is router-default.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/router-default",
	Inject:      []string{llm.ServiceKey},
	Description: "Per-agent model order with lossless failover.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[struct {
			// Routes by task type, e.g. [{"task":"summarize","model":"ollama/qwen3"}].
			Routes []Route `json:"routes"`
		}](c)
		if err != nil {
			return err
		}
		for _, rt := range cfg.Routes {
			if _, err := llm.ParseRef(rt.Model); err != nil {
				return err
			}
		}
		r := New(c, kernel.Use[*llm.Registry](c, llm.ServiceKey), nil)
		r.SetRoutes(cfg.Routes)
		QuotaExhausted.Listen(c, func(_ context.Context, e Exhausted) (llm.ModelRef, bool) {
			return r.nextInOrder(e.Agent, e.Failed)
		})
		var _ agentloop.Router = r
		var _ agentloop.InputSelector = r
		return c.Provide(agentloop.RouterKey, agentloop.Router(r))
	},
})
