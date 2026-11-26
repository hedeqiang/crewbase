// Package subagent defines the `subagent` capability: start a sub-agent for a
// task and wait for its result (PRD A-6, docs/04-architecture.md §6.4).
//
// Providers implement Provider: "local" (a fresh local session), "fork" (a
// session forked from the parent's), and later "acp" (hand one turn to an
// external CLI such as Claude Code or Codex). Every provider sits behind
// the same Service, which owns the shared admission rules:
//
//   - maximum delegation depth (lineage is data: the caller computes the
//     depth from recorded spawn events and passes it in Request.Depth);
//   - maximum concurrent sub-agents per parent;
//   - cancellation: when the caller's context ends (the parent's turn is
//     cancelled) Spawn returns at once with StopReason "cancelled" and the
//     provider is expected to stop its child; the admission slot is held
//     until the provider actually returns;
//   - a bounded diagnostic (≤ 4096 bytes, docs/06 §6).
//
// Withdrawing a provider only blocks new spawns; running children continue
// (docs/05 §8 rule 2).
package subagent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"unicode/utf8"

	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/kernel"
)

// ServiceKey is the service key of the subagent capability.
const ServiceKey = "subagent"

// Default admission limits.
const (
	DefaultMaxDepth      = 2
	DefaultMaxConcurrent = 3
	// MaxDiagnostic bounds Result.Diagnostic (bytes).
	MaxDiagnostic = 4096
)

// Stop reasons reported in Result.StopReason.
const (
	StopCompleted = "completed"
	StopFailed    = "failed"
	StopCancelled = "cancelled"
)

// Session event types recorded in the parent session (lineage as data).
const (
	TypeSpawned  = "subagent/spawned"
	TypeFinished = "subagent/finished"
)

// SpawnedData is the payload of subagent/spawned (written to the parent
// session). Child is the agent that ran the task, ChildSession its session.
type SpawnedData struct {
	Provider      string `json:"provider"`
	Parent        string `json:"parent"` // parent agent id ("" for non-agent callers, e.g. workflows)
	ParentSession string `json:"parentSession"`
	Child         string `json:"child"`
	ChildSession  string `json:"childSession"`
	Ephemeral     bool   `json:"ephemeral,omitempty"`
	Depth         int    `json:"depth"`
	Task          string `json:"task,omitempty"` // truncated
}

// FinishedData is the payload of subagent/finished (parent session).
type FinishedData struct {
	Child        string `json:"child"`
	ChildSession string `json:"childSession"`
	StopReason   string `json:"stopReason"`
	Diagnostic   string `json:"diagnostic,omitempty"`
}

// Request asks a provider to run one task.
type Request struct {
	// Parent is the delegating agent id (may be empty for non-agent callers).
	Parent string `json:"parent"`
	// ParentSession is the session the delegation happens in; the child's
	// lineage points at it.
	ParentSession string `json:"parentSession"`
	// Task is the instruction handed to the child.
	Task string `json:"task"`
	// Agent optionally names an existing agent to delegate to. Providers
	// that cannot target agents (e.g. acp) ignore or reject it.
	Agent string `json:"agent,omitempty"`
	// Mode is the requested run mode; providers never widen the parent's.
	Mode string `json:"mode,omitempty"`
	// Depth is the child's delegation depth (1 = direct child).
	Depth int `json:"depth"`
	// Worktree continues the work in an existing worktree (its id, from
	// the git feature): the sub-agent works on that branch.
	Worktree string `json:"worktree,omitempty"`
	// Models, when set, is the model order of an ephemeral child instead
	// of the parent's (e.g. the model arena: one child per model).
	Models []llm.ModelRef `json:"models,omitempty"`
	// Instructions, when set, replace an ephemeral child's default
	// sub-agent instructions.
	Instructions string `json:"instructions,omitempty"`
	// OnStart, when set, is told the child agent and session as soon as
	// they exist, before the task runs (to follow it live).
	OnStart func(child, childSession string) `json:"-"`
}

// Result is the uniform outcome of every provider.
type Result struct {
	Output     string `json:"output"`
	Structured any    `json:"structured,omitempty"`
	Diagnostic string `json:"diagnostic,omitempty"` // ≤ MaxDiagnostic bytes, no credentials or raw payloads
	StopReason string `json:"stopReason"`
	// Child / ChildSession identify where the work happened (for UI links).
	Child        string `json:"child,omitempty"`
	ChildSession string `json:"childSession,omitempty"`
}

// Provider runs one delegated task. Spawn must stop the child and return
// promptly when ctx is cancelled. Errors are for infrastructure failures
// (unknown agent, session store); a child that ran and failed is reported
// as a Result with StopReason "failed".
type Provider interface {
	Name() string
	Spawn(ctx context.Context, req Request) (Result, error)
}

// Admission errors (test with errors.Is).
var (
	ErrNoProvider    = errors.New("subagent: provider not available")
	ErrDepthExceeded = errors.New("subagent: maximum delegation depth reached")
	ErrBusy          = errors.New("subagent: too many concurrent sub-agents for this parent")
	ErrEmptyTask     = errors.New("subagent: task is required")
)

// Limits are the shared admission limits.
type Limits struct {
	MaxDepth      int `json:"maxDepth"`
	MaxConcurrent int `json:"maxConcurrent"` // per parent
}

// Service is the `subagent` service: a registry of providers by name with
// shared admission.
type Service struct {
	limits Limits

	mu        sync.Mutex
	providers map[string]Provider
	order     []string
	active    map[string]int // parent key → running children
}

// NewService creates a service; zero limits take the defaults.
func NewService(l Limits) *Service {
	if l.MaxDepth <= 0 {
		l.MaxDepth = DefaultMaxDepth
	}
	if l.MaxConcurrent <= 0 {
		l.MaxConcurrent = DefaultMaxConcurrent
	}
	return &Service{limits: l, providers: map[string]Provider{}, active: map[string]int{}}
}

// Limits returns the admission limits.
func (s *Service) Limits() Limits { return s.limits }

// Register adds a provider for c's lifetime. The first registered provider
// is the default. A second provider with the same name fails.
func (s *Service) Register(c *kernel.Context, p Provider) error {
	name := p.Name()
	s.mu.Lock()
	if _, ok := s.providers[name]; ok {
		s.mu.Unlock()
		return fmt.Errorf("subagent provider %q already registered", name)
	}
	s.providers[name] = p
	s.order = append(s.order, name)
	s.mu.Unlock()
	c.Defer(func() {
		s.mu.Lock()
		if s.providers[name] == p {
			delete(s.providers, name)
			for i, n := range s.order {
				if n == name {
					s.order = append(s.order[:i:i], s.order[i+1:]...)
					break
				}
			}
		}
		s.mu.Unlock()
	})
	return nil
}

// Providers lists registered provider names (registration order).
func (s *Service) Providers() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.order...)
}

// Provider returns a provider by name ("" = "local" if present, else the
// first registered).
func (s *Service) Provider(name string) (Provider, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name == "" {
		if p, ok := s.providers["local"]; ok {
			return p, true
		}
		if len(s.order) == 0 {
			return nil, false
		}
		name = s.order[0]
	}
	p, ok := s.providers[name]
	return p, ok
}

// Active returns the number of running children of a parent key.
func (s *Service) Active(parent string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[parent]
}

func parentKey(req Request) string {
	if req.Parent != "" {
		return req.Parent
	}
	return "session:" + req.ParentSession
}

// Spawn admits req and runs it on the named provider, blocking until the
// child finishes or ctx ends.
func (s *Service) Spawn(ctx context.Context, provider string, req Request) (Result, error) {
	if req.Task == "" {
		return Result{}, ErrEmptyTask
	}
	if req.Depth <= 0 {
		req.Depth = 1
	}
	if req.Depth > s.limits.MaxDepth {
		return Result{}, fmt.Errorf("%w (%d)", ErrDepthExceeded, s.limits.MaxDepth)
	}
	p, ok := s.Provider(provider)
	if !ok {
		return Result{}, fmt.Errorf("%w: %q", ErrNoProvider, provider)
	}
	key := parentKey(req)
	s.mu.Lock()
	if s.active[key] >= s.limits.MaxConcurrent {
		s.mu.Unlock()
		return Result{}, fmt.Errorf("%w (max %d)", ErrBusy, s.limits.MaxConcurrent)
	}
	s.active[key]++
	s.mu.Unlock()
	release := func() {
		s.mu.Lock()
		if s.active[key]--; s.active[key] <= 0 {
			delete(s.active, key)
		}
		s.mu.Unlock()
	}
	if err := ctx.Err(); err != nil {
		release()
		return Result{StopReason: StopCancelled, Diagnostic: "cancelled before start"}, err
	}

	type out struct {
		r   Result
		err error
	}
	ch := make(chan out, 1)
	go func() {
		var o out
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					o = out{err: fmt.Errorf("subagent provider %s panicked: %v", p.Name(), rec)}
				}
			}()
			o.r, o.err = p.Spawn(ctx, req)
		}()
		// free the slot before the result is seen, so a caller that got the
		// result never finds the slot still taken
		release()
		ch <- o
	}()
	select {
	case o := <-ch:
		o.r.Diagnostic = Truncate(o.r.Diagnostic, MaxDiagnostic)
		if o.r.StopReason == "" {
			if o.err != nil {
				o.r.StopReason = StopFailed
			} else {
				o.r.StopReason = StopCompleted
			}
		}
		return o.r, o.err
	case <-ctx.Done():
		// The provider keeps its slot until it has actually stopped.
		return Result{StopReason: StopCancelled, Diagnostic: "parent cancelled"}, ctx.Err()
	}
}

// Truncate cuts s to at most n bytes on a UTF-8 boundary.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const ell = "…"
	cut := n - len(ell)
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ell
}

// Plugin provides the subagent service.
// Config: {"maxDepth": 2, "maxConcurrent": 3}.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/subagent",
	Description: "Sub-agent delegation: provider registry with shared admission (depth, concurrency, cancellation).",
	Apply: func(c *kernel.Context) error {
		l, err := kernel.Decode[Limits](c)
		if err != nil {
			return err
		}
		return c.Provide(ServiceKey, NewService(l))
	},
})
