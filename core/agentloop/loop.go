// Package agentloop is the default agent driver. For each agent it claims
// queued inputs and runs turns: each step assembles the prompt and tools,
// asks the router for a model, streams the response, records it, and runs
// tool calls through the pipeline until the model stops calling tools.
// The whole driver is a plugin and can be replaced.
package agentloop

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

// Router picks models for an agent. The `router` service implements it; if
// absent, the agent's first model is used and failover is disabled.
type Router interface {
	// Select picks the model at the start of a turn.
	Select(a *agent.Agent) (llm.ModelRef, bool)
	// Failover is called when ref failed with a failover-class error; it
	// returns the next usable model.
	Failover(ctx context.Context, a *agent.Agent, ref llm.ModelRef, err error) (llm.ModelRef, bool)
}

// InputSelector is optionally implemented by routers that route by task
// type (e.g. summaries and routine checks to a local model).
type InputSelector interface {
	SelectFor(a *agent.Agent, in agent.Input) (llm.ModelRef, bool)
}

// Compactor shrinks a session's model context (the `compaction` service).
type Compactor interface {
	Compact(ctx context.Context, a *agent.Agent, ss *session.Session, model llm.ModelRef, reason string) error
}

// Hydrator loads attachment parts right before a request (the
// `attachments` service): the bytes for files the adapter takes natively
// (llm.FileAccepter), extracted text for the rest. Without it, models see
// a note with the workspace path.
type Hydrator interface {
	Hydrate(ctx context.Context, workspace string, ad llm.Adapter, model string, req *llm.Request)
}

const (
	RouterKey      = "router"
	CompactionKey  = "compaction"
	AttachmentsKey = "attachments"
)

// Turn/step payloads (also session events).
type TurnStart struct {
	Input string       `json:"input"`
	Model llm.ModelRef `json:"model"`
}

type TurnEnd struct {
	Status string `json:"status"` // completed | failed | cancelled
	Error  string `json:"error,omitempty"`
	Steps  int    `json:"steps"`
}

type StepStart struct {
	Step  int          `json:"step"`
	Model llm.ModelRef `json:"model"`
	// ContextTokens is the estimated size of the request.
	ContextTokens int `json:"contextTokens"`
	ContextWindow int `json:"contextWindow"`
}

// StreamEvent carries live chunks to the UI (not logged; the final message is).
type StreamEvent struct {
	Agent   string    `json:"agent"`
	Session string    `json:"session"`
	Chunk   llm.Chunk `json:"chunk"`
}

// RequestArgs flow through agent/request.
type RequestArgs struct {
	Agent   *agent.Agent
	Session *session.Session
	Request *llm.Request
}

var (
	// PreStep can accept, rewrite or reject the claimed input.
	PreStep = kernel.NewWaterfall[*agent.Input, *agent.Input]("agent/pre-step")
	// Request adjusts request parameters (not the model choice).
	Request = kernel.NewWaterfall[RequestArgs, *llm.Request]("agent/request")
	// Response may rewrite a complete model response before it is logged
	// (redaction across stream chunks, tool-call argument scrubbing).
	Response = kernel.NewWaterfall[llm.Response, llm.Response]("agent/response")
	// RequestError notifies failures before routing.
	RequestError = kernel.NewEmit[error]("agent/request-error")
	// TurnStopping may return true to continue with another step (a
	// listener that does so must have injected new input).
	TurnStopping = kernel.NewSerial[*agent.Agent, bool]("agent/turn-stopping")
	Stream       = kernel.NewEmit[StreamEvent]("agent/stream")
	// TurnStarted fires after an input is claimed, before the first step.
	TurnStarted = kernel.NewEmit[TurnStartEvent]("agent/turn-start")
	TurnEnded   = kernel.NewEmit[TurnEndEvent]("agent/turn-end")
)

// TurnStartEvent reports a starting turn.
type TurnStartEvent struct {
	Agent   *agent.Agent
	Session *session.Session
	Input   agent.Input
}

// TurnEndEvent reports a finished turn.
type TurnEndEvent struct {
	Agent   string
	Session string
	Input   agent.Input
	// Text is the final assistant text of the turn.
	Text string
	TurnEnd
}

// Config of the loop.
type Config struct {
	MaxSteps         int     `json:"maxSteps"`
	CompactThreshold float64 `json:"compactThreshold"`
	DefaultWindow    int     `json:"defaultWindow"`
}

type loop struct {
	c        *kernel.Context
	cfg      Config
	agents   *agent.Registry
	llms     *llm.Registry
	toolsReg *tools.Registry
	prompts  *prompt.Assembler
}

// Plugin is the default driver.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/agent-loop",
	Inject:      []string{agent.ServiceKey, llm.ServiceKey, tools.ServiceKey, prompt.ServiceKey},
	Description: "Default agent loop (turns, steps, failover, compaction trigger).",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		if cfg.MaxSteps <= 0 {
			cfg.MaxSteps = 50
		}
		if cfg.CompactThreshold <= 0 {
			cfg.CompactThreshold = 0.8
		}
		if cfg.DefaultWindow <= 0 {
			cfg.DefaultWindow = 128000
		}
		l := &loop{c: c, cfg: cfg,
			agents:   kernel.Use[*agent.Registry](c, agent.ServiceKey),
			llms:     kernel.Use[*llm.Registry](c, llm.ServiceKey),
			toolsReg: kernel.Use[*tools.Registry](c, tools.ServiceKey),
			prompts:  kernel.Use[*prompt.Assembler](c, prompt.ServiceKey),
		}
		var dmu sync.Mutex
		drivers := map[string]*kernel.Context{}
		start := func(a *agent.Agent) {
			dmu.Lock()
			defer dmu.Unlock()
			if _, ok := drivers[a.ID()]; ok {
				return
			}
			dc := c.Fork(a.ID())
			drivers[a.ID()] = dc
			dc.Go(func(ctx context.Context) { l.drive(ctx, a) })
		}
		agent.Created.Listen(c, func(_ context.Context, a *agent.Agent) (struct{}, error) {
			start(a)
			return struct{}{}, nil
		})
		agent.Deleted.Listen(c, func(id string) {
			dmu.Lock()
			dc, ok := drivers[id]
			delete(drivers, id)
			dmu.Unlock()
			if ok {
				dc.Dispose()
			}
		})
		for _, a := range l.agents.List() {
			start(a)
		}
		return nil
	},
})

func (l *loop) drive(ctx context.Context, a *agent.Agent) {
	for {
		for {
			in, ok := a.Claim()
			if !ok {
				break
			}
			l.turn(ctx, a, in)
			if ctx.Err() != nil {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-a.Wake():
		}
	}
}

func (l *loop) turn(parent context.Context, a *agent.Agent, in agent.Input) {
	ctx, cancel := context.WithCancel(parent)
	a.SetCurrent(in.ID, cancel)
	a.SetStatus(agent.StatusRunning, "")
	defer func() {
		a.SetCurrent("", nil)
		cancel()
	}()
	d := kernel.For(l.c, a.ID())
	ss, err := l.agents.Sessions().Get(in.Session)
	if err != nil {
		l.c.Log().Error("turn: session", "err", err)
		a.SetStatus(agent.StatusFailed, "session unavailable")
		TurnEnded.Emit(d, TurnEndEvent{Agent: a.ID(), Session: in.Session, Input: in, TurnEnd: TurnEnd{Status: "failed", Error: "session unavailable: " + err.Error()}})
		return
	}
	inp, err := PreStep.Run(d, ctx, &in, func(_ context.Context, x *agent.Input) (*agent.Input, error) { return x, nil })
	if err != nil || inp == nil {
		_, _ = ss.Append(session.TypeNotice, a.ID(), map[string]string{"text": fmt.Sprintf("input rejected: %v", err)})
		a.SetStatus(agent.StatusIdle, "")
		// Waiters (workflows, subagents) track inputs by turn end.
		TurnEnded.Emit(d, TurnEndEvent{Agent: a.ID(), Session: ss.ID(), Input: in, TurnEnd: TurnEnd{Status: "rejected", Error: fmt.Sprint(err)}})
		return
	}
	in = *inp
	if _, err := ss.Append(session.TypeUserMessage, "", session.UserMessage{Text: in.Text, From: in.From, To: a.ID(), Attachments: in.Attachments}); err != nil {
		a.SetStatus(agent.StatusFailed, err.Error())
		TurnEnded.Emit(d, TurnEndEvent{Agent: a.ID(), Session: ss.ID(), Input: in, TurnEnd: TurnEnd{Status: "failed", Error: "could not record input: " + err.Error()}})
		return
	}

	ref, ok := l.selectModel(a, in)
	if !ok {
		l.endTurn(a, ss, in, TurnEnd{Status: "failed", Error: "no model configured"})
		return
	}
	_, _ = ss.Append(session.TypeTurnStart, a.ID(), TurnStart{Input: in.ID, Model: ref})
	TurnStarted.Emit(d, TurnStartEvent{Agent: a, Session: ss, Input: in})

	steps := 0
	for steps < l.cfg.MaxSteps {
		if ctx.Err() != nil {
			l.endTurn(a, ss, in, TurnEnd{Status: "cancelled", Steps: steps})
			return
		}
		steps++
		var resp llm.Response
		ref, resp, err = l.step(ctx, a, ss, ref, steps)
		if err != nil {
			if ctx.Err() != nil {
				l.endTurn(a, ss, in, TurnEnd{Status: "cancelled", Steps: steps})
				return
			}
			l.endTurn(a, ss, in, TurnEnd{Status: "failed", Error: err.Error(), Steps: steps})
			return
		}
		calls := resp.Message.ToolCalls()
		stop := false
		for _, tc := range calls {
			if ctx.Err() != nil {
				break
			}
			exe := l.toolsReg.Run(ctx, l.c, tools.Call{
				ID: tc.ID, Name: tc.Name, Args: tc.Args, Agent: a.ID(), Session: ss,
				Mode: agent.Stricter(a.Mode(), in.Mode), Workspace: a.Workspace(),
			})
			stop = stop || exe.Result.StopTurn
		}
		_, _ = ss.Append(session.TypeStepEnd, a.ID(), map[string]int{"step": steps})
		if stop {
			break
		}
		if len(calls) > 0 {
			continue
		}
		more, _ := TurnStopping.Run(d, ctx, a)
		cont := false
		for _, m := range more {
			cont = cont || m
		}
		if !cont {
			break
		}
	}
	status := "completed"
	errMsg := ""
	if steps >= l.cfg.MaxSteps {
		errMsg = fmt.Sprintf("stopped after %d steps", steps)
	}
	l.endTurn(a, ss, in, TurnEnd{Status: status, Error: errMsg, Steps: steps})
}

func (l *loop) endTurn(a *agent.Agent, ss *session.Session, in agent.Input, te TurnEnd) {
	_, _ = ss.Append(session.TypeTurnEnd, a.ID(), te)
	if te.Status == "failed" {
		a.SetStatus(agent.StatusFailed, te.Error)
	} else {
		a.SetStatus(agent.StatusIdle, "")
	}
	text := ""
	if e, ok := ss.Last(session.TypeAssistantMessage); ok && e.Agent == a.ID() {
		var m session.AssistantMessage
		if e.Decode(&m) == nil {
			text = m.Message.Text()
		}
	}
	TurnEnded.Emit(kernel.For(l.c, a.ID()), TurnEndEvent{Agent: a.ID(), Session: ss.ID(), Input: in, Text: text, TurnEnd: te})
}

func (l *loop) router() (Router, bool) {
	return kernel.Lookup[Router](l.c, RouterKey)
}

func (l *loop) selectModel(a *agent.Agent, in agent.Input) (llm.ModelRef, bool) {
	if in.Model != nil {
		return *in.Model, true
	}
	if r, ok := l.router(); ok {
		if is, ok := r.(InputSelector); ok {
			return is.SelectFor(a, in)
		}
		return r.Select(a)
	}
	ms := a.Spec().Models
	if len(ms) == 0 {
		return llm.ModelRef{}, false
	}
	return ms[0], true
}

func (l *loop) window(ctx context.Context, ad llm.Adapter, ref llm.ModelRef) int {
	ms, err := ad.Models(ctx)
	if err == nil {
		for _, m := range ms {
			if m.ID == ref.Model && m.ContextWindow > 0 {
				return m.ContextWindow
			}
		}
	}
	return l.cfg.DefaultWindow
}

func (l *loop) build(a *agent.Agent, ss *session.Session, ref llm.ModelRef) llm.Request {
	return llm.Request{
		Model:       ref.Model,
		Effort:      ref.Effort,
		System:      l.prompts.Assemble(a.ID()),
		Messages:    session.DeriveMessages(ss.Events(), a.ID(), l.agents.Names()),
		Tools:       l.toolsReg.SchemasFor(a.ID(), ss.Meta().Kind),
		NativeTools: a.Spec().NativeTools,
		Agent:       a.ID(),
		Session:     ss.ID(),
		Workspace:   a.Workspace(),
	}
}

// step performs one model request with failover; returns the model that
// finally answered.
func (l *loop) step(ctx context.Context, a *agent.Agent, ss *session.Session, ref llm.ModelRef, n int) (llm.ModelRef, llm.Response, error) {
	d := kernel.For(l.c, a.ID())
	tried := map[string]bool{}
	compacted := false
	for {
		ad, ok := l.llms.Get(ref.Provider)
		if !ok {
			err := &llm.Error{Kind: llm.ErrUnavailable, Provider: ref.Provider, Message: "provider not configured"}
			next, ok2 := l.failover(ctx, a, ss, ref, err, tried)
			if !ok2 {
				return ref, llm.Response{}, err
			}
			ref = next
			continue
		}
		req := l.build(a, ss, ref)
		if h, ok := kernel.Lookup[Hydrator](l.c, AttachmentsKey); ok {
			h.Hydrate(ctx, a.Workspace(), ad, ref.Model, &req)
		}
		win := l.window(ctx, ad, ref)
		est := llm.EstimateRequest(req)
		if !compacted && float64(est) > l.cfg.CompactThreshold*float64(win) {
			if cp, ok := kernel.Lookup[Compactor](l.c, CompactionKey); ok {
				compacted = true
				if err := cp.Compact(ctx, a, ss, ref, "threshold"); err != nil {
					l.c.Log().Warn("compaction failed", "agent", a.ID(), "err", err)
				}
				continue
			}
		}
		reqp, err := Request.Run(d, ctx, RequestArgs{Agent: a, Session: ss, Request: &req}, func(_ context.Context, x RequestArgs) (*llm.Request, error) {
			return x.Request, nil
		})
		if err != nil {
			return ref, llm.Response{}, err
		}
		req = *reqp
		req.Model = ref.Model // the model choice is not adjustable here
		_, _ = ss.Append(session.TypeStepStart, a.ID(), StepStart{Step: n, Model: ref, ContextTokens: est, ContextWindow: win})

		resp, err := ad.Stream(ctx, req, func(ch llm.Chunk) error {
			out, err := llm.StreamChunk.Run(d, ctx, ch, func(_ context.Context, c llm.Chunk) (llm.Chunk, error) { return c, nil })
			if err != nil {
				return err
			}
			Stream.Emit(d, StreamEvent{Agent: a.ID(), Session: ss.ID(), Chunk: out})
			return nil
		})
		if err == nil {
			// Last chance to scrub the complete reply (and tool-call
			// arguments) before it is recorded and executed.
			out, rerr := Response.Run(d, ctx, resp, func(_ context.Context, r llm.Response) (llm.Response, error) { return r, nil })
			if rerr != nil {
				// Fail closed: the raw reply may hold what a listener
				// (redaction) was meant to remove; never log or run it.
				return ref, llm.Response{}, fmt.Errorf("response post-processing failed: %w", rerr)
			}
			resp = out
			if _, err := ss.Append(session.TypeAssistantMessage, a.ID(), session.AssistantMessage{Message: resp.Message, Model: ref, Usage: resp.Usage, Stop: resp.StopReason}); err != nil {
				return ref, resp, err
			}
			return ref, resp, nil
		}
		if ctx.Err() != nil {
			return ref, llm.Response{}, ctx.Err()
		}
		RequestError.Emit(d, err)
		if llm.KindOf(err) == llm.ErrContextOverflow && !compacted {
			if cp, ok := kernel.Lookup[Compactor](l.c, CompactionKey); ok {
				compacted = true
				if cerr := cp.Compact(ctx, a, ss, ref, "overflow"); cerr == nil {
					continue
				}
			}
		}
		if !llm.ShouldFailover(err) {
			return ref, llm.Response{}, err
		}
		next, ok := l.failover(ctx, a, ss, ref, err, tried)
		if !ok {
			return ref, llm.Response{}, fmt.Errorf("no available model: %w", err)
		}
		ref = next
	}
}

func (l *loop) failover(ctx context.Context, a *agent.Agent, ss *session.Session, ref llm.ModelRef, cause error, tried map[string]bool) (llm.ModelRef, bool) {
	tried[ref.String()] = true
	r, ok := l.router()
	if !ok {
		return ref, false
	}
	next, ok := r.Failover(ctx, a, ref, cause)
	if !ok || tried[next.String()] {
		return ref, false
	}
	var le *llm.Error
	reason := "error"
	if errors.As(cause, &le) {
		reason = string(le.Kind)
	}
	_, _ = ss.Append(session.TypeModelSwitched, a.ID(), session.ModelSwitchData{From: ref, To: next, Reason: reason, Detail: cause.Error()})
	return next, true
}

// WaitIdle blocks until the agent has no queued input and is not running,
// or the timeout passes. Useful for tests and the CLI.
func WaitIdle(a *agent.Agent, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, _ := a.Status()
		if (st == agent.StatusIdle || st == agent.StatusFailed || st == agent.StatusPaused) && len(a.Queue()) == 0 {
			// settle: a claim might be in flight
			time.Sleep(20 * time.Millisecond)
			st2, _ := a.Status()
			if st2 == st && len(a.Queue()) == 0 {
				return true
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
