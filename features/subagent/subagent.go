// Package subagent provides the built-in providers of the `subagent`
// capability and the `delegate` model tool (PRD A-6):
//
//   - "local": the task runs in a fresh session, either by a named existing
//     agent or by an ephemeral child agent ("<parent>-sub-<n>", the
//     parent's model order, a run mode no wider than the parent's) that is
//     deleted afterwards.
//   - "fork": the child session is forked from the parent session at the
//     latest turn boundary, so the child starts with the parent's context.
//
// A child is always a separate agent scope, so it never inherits the
// parent's agent-scoped (private) tools. Lineage is data: the child
// session's Meta.Parent points at the parent session and the parent
// session records subagent/spawned and subagent/finished events. The
// `delegate` tool derives the delegation depth from that lineage.
package subagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/subagent"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

// SessionKind is the session kind of child sessions.
const SessionKind = "subagent"

// ephemeralRole marks ephemeral child agents (used for crash cleanup).
const ephemeralRole = "ephemeral sub-agent of "

// lifecycleMu serializes agent Create/Delete issued by this package:
// concurrent creation/deletion of agents is not safe in the current agent
// loop (its driver map is unsynchronized; see the package report).
var lifecycleMu sync.Mutex

// ---------------------------------------------------------------- waiter

// waiter matches TurnEnded events to tagged inputs.
type waiter struct {
	mu      sync.Mutex
	ends    map[string]chan agentloop.TurnEndEvent
	running map[string]bool
}

func newWaiter(c *kernel.Context) *waiter {
	w := &waiter{ends: map[string]chan agentloop.TurnEndEvent{}, running: map[string]bool{}}
	agentloop.TurnStarted.Listen(c, func(e agentloop.TurnStartEvent) {
		w.mu.Lock()
		if _, ok := w.ends[e.Input.Tag]; ok {
			w.running[e.Input.Tag] = true
		}
		w.mu.Unlock()
	})
	agentloop.TurnEnded.Listen(c, func(e agentloop.TurnEndEvent) {
		w.mu.Lock()
		ch, ok := w.ends[e.Input.Tag]
		delete(w.running, e.Input.Tag)
		w.mu.Unlock()
		if ok {
			select {
			case ch <- e:
			default:
			}
		}
	})
	return w
}

func (w *waiter) expect(tag string) <-chan agentloop.TurnEndEvent {
	ch := make(chan agentloop.TurnEndEvent, 1)
	w.mu.Lock()
	w.ends[tag] = ch
	w.mu.Unlock()
	return ch
}

func (w *waiter) done(tag string) {
	w.mu.Lock()
	delete(w.ends, tag)
	delete(w.running, tag)
	w.mu.Unlock()
}

func (w *waiter) isRunning(tag string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running[tag]
}

// ---------------------------------------------------------------- runner

// runner implements both built-in providers.
type runner struct {
	c      *kernel.Context
	agents *agent.Registry
	w      *waiter
	seq    sync.Mutex
	n      map[string]int
}

type provider struct {
	name string
	r    *runner
	fork bool
}

func (p *provider) Name() string { return p.name }

func (p *provider) Spawn(ctx context.Context, req subagent.Request) (subagent.Result, error) {
	return p.r.spawn(ctx, p.name, p.fork, req)
}

func (r *runner) sessions() *session.Store { return r.agents.Sessions() }

// createEphemeral creates "<parent>-sub-<n>".
func (r *runner) createEphemeral(parent *agent.Agent, mode string) (*agent.Agent, error) {
	base := "sub"
	var spec agent.Spec
	if parent != nil {
		ps := parent.Spec()
		base = ps.ID + "-sub"
		spec.Models = ps.Models
		spec.Folder = ps.Folder
		mode = agent.Stricter(parent.Mode(), mode)
	}
	if mode == "" || mode == agent.ModeHost {
		mode = agent.ModeSandbox // children never start in host mode
	}
	spec.Mode = mode
	spec.Role = ephemeralRole + strings.TrimSuffix(base, "-sub")
	spec.Instructions = "You are a sub-agent. Complete the delegated task and reply with the result only.\n"
	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()
	for range 1000 {
		r.seq.Lock()
		r.n[base]++
		id := fmt.Sprintf("%s-%d", base, r.n[base])
		r.seq.Unlock()
		if _, exists := r.agents.Get(id); exists {
			continue
		}
		spec.ID, spec.Name = id, id
		a, err := r.agents.Create(spec)
		if err != nil && strings.Contains(err.Error(), "exists") {
			continue
		}
		return a, err
	}
	return nil, errors.New("no free sub-agent id")
}

func (r *runner) deleteAgent(id string) {
	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()
	_ = r.agents.Delete(id)
}

// latestTurnBoundary returns the seq of the parent session's last turn/end.
func latestTurnBoundary(ss *session.Session) int64 {
	if e, ok := ss.Last(session.TypeTurnEnd); ok {
		return e.Seq
	}
	return 0
}

func (r *runner) spawn(ctx context.Context, provName string, fork bool, req subagent.Request) (subagent.Result, error) {
	var parent *agent.Agent
	if req.Parent != "" {
		a, ok := r.agents.Get(req.Parent)
		if !ok {
			return subagent.Result{}, fmt.Errorf("parent agent %s not found", req.Parent)
		}
		parent = a
	}
	var pss *session.Session
	if req.ParentSession != "" {
		ss, err := r.sessions().Get(req.ParentSession)
		if err != nil {
			return subagent.Result{}, fmt.Errorf("parent session: %w", err)
		}
		pss = ss
	}
	if fork && pss == nil {
		return subagent.Result{}, errors.New("fork needs a parent session")
	}
	mode := req.Mode
	if parent != nil {
		mode = agent.Stricter(parent.Mode(), mode)
	}

	// Pick the child agent.
	var child *agent.Agent
	ephemeral := req.Agent == ""
	if ephemeral {
		a, err := r.createEphemeral(parent, mode)
		if err != nil {
			return subagent.Result{}, fmt.Errorf("create sub-agent: %w", err)
		}
		child = a
		defer r.deleteAgent(a.ID())
	} else {
		a, ok := r.agents.Get(req.Agent)
		if !ok {
			return subagent.Result{}, fmt.Errorf("unknown agent %q", req.Agent)
		}
		if req.Agent == req.Parent {
			return subagent.Result{}, errors.New("an agent cannot delegate to itself")
		}
		if pss != nil && slicesContains(Ancestors(r.sessions(), pss), req.Agent) {
			return subagent.Result{}, fmt.Errorf("agent %s is already waiting in this delegation chain", req.Agent)
		}
		child = a
	}

	// Create the child session, with lineage.
	title := "Sub-agent: " + subagent.Truncate(firstLine(req.Task), 60)
	meta := session.Meta{Kind: SessionKind, Title: title, Agents: []string{child.ID()}, Folder: child.Spec().Folder}
	var css *session.Session
	var err error
	if fork {
		css, err = r.sessions().Fork(pss, latestTurnBoundary(pss), meta)
	} else {
		meta.Parent = req.ParentSession
		css, err = r.sessions().Create(meta)
	}
	if err != nil {
		return subagent.Result{}, fmt.Errorf("child session: %w", err)
	}
	if pss != nil {
		if _, err := pss.Append(subagent.TypeSpawned, req.Parent, subagent.SpawnedData{
			Provider: provName, Parent: req.Parent, ParentSession: pss.ID(), Child: child.ID(), ChildSession: css.ID(),
			Ephemeral: ephemeral, Depth: req.Depth, Task: subagent.Truncate(req.Task, 500),
		}); err != nil {
			return subagent.Result{}, fmt.Errorf("record lineage: %w", err)
		}
	}

	res := r.run(ctx, child, css, req, mode)
	res.Child, res.ChildSession = child.ID(), css.ID()
	if pss != nil {
		_, _ = pss.Append(subagent.TypeFinished, req.Parent, subagent.FinishedData{
			Child: child.ID(), ChildSession: css.ID(), StopReason: res.StopReason, Diagnostic: subagent.Truncate(res.Diagnostic, 500),
		})
	}
	return res, nil
}

// run enqueues the task for child in css and waits for the turn.
func (r *runner) run(ctx context.Context, child *agent.Agent, css *session.Session, req subagent.Request, mode string) subagent.Result {
	tag := "subagent:" + css.ID()
	ch := r.w.expect(tag)
	defer r.w.done(tag)
	from := req.Parent
	if from == "" {
		from = "delegator"
	}
	in, err := child.Enqueue(agent.Input{Session: css.ID(), Text: req.Task, From: from, Mode: mode, Tag: tag})
	if err != nil {
		return subagent.Result{StopReason: subagent.StopFailed, Diagnostic: "enqueue: " + err.Error()}
	}
	select {
	case e := <-ch:
		out := TurnOutput(css, e.Agent, in.ID)
		if out == "" {
			out = e.Text
		}
		switch e.Status {
		case "completed":
			return subagent.Result{Output: out, StopReason: subagent.StopCompleted, Diagnostic: e.Error}
		case "cancelled":
			return subagent.Result{Output: out, StopReason: subagent.StopCancelled, Diagnostic: "child turn cancelled"}
		default:
			return subagent.Result{Output: out, StopReason: subagent.StopFailed, Diagnostic: e.Error}
		}
	case <-ctx.Done():
		// Propagate cancellation to the child's turn.
		if !child.Cancel(in.ID) && r.w.isRunning(tag) {
			child.Stop()
			select {
			case <-ch:
			case <-time.After(5 * time.Second):
			}
		}
		return subagent.Result{StopReason: subagent.StopCancelled, Diagnostic: "parent cancelled"}
	}
}

// TurnOutput returns the final assistant text agentID produced for input
// inputID in ss (robust when several agents share the session).
func TurnOutput(ss *session.Session, agentID, inputID string) string {
	in := false
	text := ""
	for _, e := range ss.Events() {
		if e.Agent != agentID {
			continue
		}
		switch e.Type {
		case session.TypeTurnStart:
			var ts agentloop.TurnStart
			if e.Decode(&ts) == nil {
				in = ts.Input == inputID
			}
		case session.TypeAssistantMessage:
			if !in {
				continue
			}
			var m session.AssistantMessage
			if e.Decode(&m) == nil {
				if t := m.Message.Text(); t != "" {
					text = t
				}
			}
		case session.TypeTurnEnd:
			if in {
				return text
			}
		}
	}
	return text
}

// lineage returns the spawn record that created ss (from its parent session).
func lineage(store *session.Store, ss *session.Session) (subagent.SpawnedData, *session.Session, bool) {
	pid := ss.Meta().Parent
	if pid == "" {
		return subagent.SpawnedData{}, nil, false
	}
	pss, err := store.Get(pid)
	if err != nil {
		return subagent.SpawnedData{}, nil, false
	}
	evs := pss.Events()
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Type != subagent.TypeSpawned {
			continue
		}
		var d subagent.SpawnedData
		if evs[i].Decode(&d) == nil && d.ChildSession == ss.ID() {
			return d, pss, true
		}
	}
	return subagent.SpawnedData{}, nil, false
}

// Depth returns the delegation depth of ss (0 for a top-level session),
// derived from recorded subagent/spawned events.
func Depth(store *session.Store, ss *session.Session) int {
	if ss == nil {
		return 0
	}
	d, _, ok := lineage(store, ss)
	if !ok {
		return 0
	}
	return d.Depth
}

// Ancestors returns the agents waiting above ss in the delegation chain.
func Ancestors(store *session.Store, ss *session.Session) []string {
	var out []string
	for cur, guard := ss, 0; cur != nil && guard < 64; guard++ {
		d, pss, ok := lineage(store, cur)
		if !ok {
			break
		}
		if d.Parent != "" {
			out = append(out, d.Parent)
		}
		cur = pss
	}
	return out
}

func slicesContains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// ---------------------------------------------------------------- plugins

// runnerKey is an internal service shared by the provider plugins.
const runnerKey = "subagent.runner"

// RunnerPlugin owns the turn waiter and ephemeral-agent bookkeeping used by
// the local and fork providers. On activation it removes ephemeral agents
// left behind by a crash.
var RunnerPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/subagent-runner",
	Inject:      []string{agent.ServiceKey},
	Description: "Runs delegated tasks as agent turns (shared by subagent-local and subagent-fork).",
	Apply: func(c *kernel.Context) error {
		r := &runner{c: c, agents: kernel.Use[*agent.Registry](c, agent.ServiceKey), n: map[string]int{}}
		r.w = newWaiter(c)
		for _, a := range r.agents.List() {
			if strings.HasPrefix(a.Spec().Role, ephemeralRole) && strings.Contains(a.ID(), "-sub-") {
				r.deleteAgent(a.ID())
			}
		}
		return c.Provide(runnerKey, r)
	},
})

// LocalPlugin registers the "local" provider.
var LocalPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/subagent-local",
	Inject:      []string{subagent.ServiceKey, runnerKey},
	Description: "Sub-agent provider: run a task in a fresh local session (named agent or ephemeral child).",
	Apply: func(c *kernel.Context) error {
		r := kernel.Use[*runner](c, runnerKey)
		return kernel.Use[*subagent.Service](c, subagent.ServiceKey).Register(c, &provider{name: "local", r: r})
	},
})

// ForkPlugin registers the "fork" provider.
var ForkPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/subagent-fork",
	Inject:      []string{subagent.ServiceKey, runnerKey},
	Description: "Sub-agent provider: fork the parent session at the latest turn boundary.",
	Apply: func(c *kernel.Context) error {
		r := kernel.Use[*runner](c, runnerKey)
		return kernel.Use[*subagent.Service](c, subagent.ServiceKey).Register(c, &provider{name: "fork", r: r, fork: true})
	},
})

// DelegateArgs are the `delegate` tool arguments.
type DelegateArgs struct {
	Task     string `json:"task"`
	Agent    string `json:"agent,omitempty"`
	Mode     string `json:"mode,omitempty"`
	Provider string `json:"provider,omitempty"`
}

// DelegatePlugin registers the global `delegate` model tool.
var DelegatePlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/delegate",
	Inject:      []string{subagent.ServiceKey, tools.ServiceKey, agent.ServiceKey},
	Description: "The `delegate` tool: hand a task to a sub-agent and wait for its result.",
	Apply: func(c *kernel.Context) error {
		svc := kernel.Use[*subagent.Service](c, subagent.ServiceKey)
		agents := kernel.Use[*agent.Registry](c, agent.ServiceKey)
		kernel.Use[*tools.Registry](c, tools.ServiceKey).Register(c, &tools.Tool{
			Name: "delegate",
			Description: "Delegate a self-contained task to a sub-agent and wait for its result. " +
				"The sub-agent does not see your private tools. Use provider \"fork\" to give it this conversation's context.",
			Params: tools.Schema(map[string]any{
				"task":     tools.Prop("string", "complete instructions for the sub-agent"),
				"agent":    tools.Prop("string", "optional id of an existing agent to delegate to (default: a temporary sub-agent)"),
				"mode":     tools.Prop("string", "optional run mode: read-only or sandbox (never wider than yours)"),
				"provider": tools.Prop("string", "local (default) or fork"),
			}, "task"),
			Category: tools.CategoryNone,
			Execute: func(ctx context.Context, call tools.Call) (tools.Result, error) {
				var a DelegateArgs
				if err := call.Decode(&a); err != nil {
					return tools.Errorf("bad arguments: %v", err), nil
				}
				if strings.TrimSpace(a.Task) == "" {
					return tools.Errorf("task is required"), nil
				}
				switch a.Mode {
				case "", agent.ModeReadOnly, agent.ModeSandbox:
				case agent.ModeHost:
					return tools.Errorf("sub-agents cannot run in host mode"), nil
				default:
					return tools.Errorf("unknown mode %q", a.Mode), nil
				}
				switch a.Provider {
				case "", "local", "fork":
				default:
					return tools.Errorf("unknown provider %q (use local or fork)", a.Provider), nil
				}
				req := subagent.Request{
					Parent: call.Agent, Task: a.Task, Agent: a.Agent,
					Mode: agent.Stricter(call.Mode, a.Mode), Depth: 1,
				}
				if call.Session != nil {
					req.ParentSession = call.Session.ID()
					req.Depth = Depth(agents.Sessions(), call.Session) + 1
				}
				res, err := svc.Spawn(ctx, a.Provider, req)
				if err != nil {
					return tools.Result{Content: "delegation failed: " + err.Error(), IsError: true, Data: res}, nil
				}
				if res.StopReason != subagent.StopCompleted {
					msg := "sub-agent " + res.StopReason
					if res.Diagnostic != "" {
						msg += ": " + res.Diagnostic
					}
					if res.Output != "" {
						msg += "\npartial output:\n" + res.Output
					}
					return tools.Result{Content: msg, IsError: true, Data: res}, nil
				}
				return tools.Result{Content: res.Output, Data: res}, nil
			},
		})
		return nil
	},
})
