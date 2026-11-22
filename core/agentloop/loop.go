// Package agentloop is the default agent driver. For each agent it claims
// queued inputs and runs turns: each step assembles the prompt and tools,
// asks the router for a model, streams the response, records it, and runs
// tool calls through the pipeline until the model stops calling tools.
// The whole driver is a plugin and can be replaced.
package agentloop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	// Tag is the input's tag (e.g. GreetingTag), for views.
	Tag string `json:"tag,omitempty"`
}

// GreetingTag marks the input that has a new agent introduce itself. Its
// text is an instruction to the agent, recorded as an agent/inject (the
// person does not see it as something they said); the answer is a normal
// message.
const GreetingTag = "greeting"

// GreetingPrompt asks a new agent to introduce itself.
const GreetingPrompt = `The person has just created you and opened this conversation. Before they ask anything, introduce yourself to them, in the language of your name and instructions:
- one or two sentences on who you are and what you can do for them;
- what they should tell you or give you first to get good results (files, context, constraints);
- then two or three example requests they could send you, each on its own line starting with "> ", written as they would type it.
Keep it short (under 120 words), plain and friendly. Do not use any tools and do not start working on anything yet.`

type TurnEnd struct {
	Status string `json:"status"` // completed | failed | cancelled | interrupted (shutdown)
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
	// ResolveUse fills in what a message's / picks stand for (a skill's
	// instructions, an MCP prompt) before the message is recorded, and may
	// turn "/name" typed at its start into a pick. An error refuses the
	// message: the person asked for something by name.
	ResolveUse = kernel.NewWaterfall[*UseRequest, *UseRequest]("agent/resolve-use")
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
	// ToolSearch sets when deferred tools are held back for tool search
	// (default auto: more than 30 of them, or their schemas over 10% of
	// the model's context window). See toolsearch.go.
	ToolSearch tools.DeferConfig `json:"toolSearch"`
}

type loop struct {
	c        *kernel.Context
	cfg      Config
	agents   *agent.Registry
	llms     *llm.Registry
	toolsReg *tools.Registry
	prompts  *prompt.Assembler

	cur    sync.Map // agent id → agent.Input of the running turn
	parked parkBook
	offers offers
	sent   sentLog
	sentMu sync.Mutex // serialises sent.json read-modify-write
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
		l.toolsReg.Register(c, l.searchTool())
		l.toolsReg.Register(c, l.callTool())
		l.toolsReg.SetGate(l.gate)
		if err := c.Provide(ToolPlannerKey, ToolPlanner(l)); err != nil {
			return err
		}
		if err := c.Provide(SentRequestsKey, SentRequests(l)); err != nil {
			return err
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
		agent.QueueChanged.Listen(c, l.replyInsteadOfApproval)
		l.startParking()
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
	l.cur.Store(a.ID(), in)
	defer l.cur.Delete(a.ID())
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
		// a resumed turn rejected now is over: not resumed on every restart
		if in.Resumed {
			_, _ = ss.Append(session.TypeTurnEnd, a.ID(), TurnEnd{Status: "rejected"})
		}
		a.SetStatus(agent.StatusIdle, "")
		// Waiters (workflows, subagents) track inputs by turn end.
		TurnEnded.Emit(d, TurnEndEvent{Agent: a.ID(), Session: ss.ID(), Input: in, TurnEnd: TurnEnd{Status: "rejected", Error: fmt.Sprint(err)}})
		return
	}
	in = *inp
	if !in.Resumed && !in.Logged {
		if err := ResolveInput(d, ctx, &in); err != nil {
			_, _ = ss.Append(session.TypeNotice, a.ID(), map[string]string{"text": fmt.Sprintf("input rejected: %v", err)})
			a.SetStatus(agent.StatusIdle, "")
			TurnEnded.Emit(d, TurnEndEvent{Agent: a.ID(), Session: ss.ID(), Input: in, TurnEnd: TurnEnd{Status: "rejected", Error: err.Error()}})
			return
		}
	}
	if in.Resumed {
		// the message is already in the log; say what happened
		_, _ = ss.Append(session.TypeInject, a.ID(), session.InjectData{Source: "restart", Text: ResumeNote})
	} else if in.Logged {
		// recorded when it was posted
	} else if in.Tag == GreetingTag {
		// an instruction to the agent, not something the person said
		if _, err := ss.Append(session.TypeInject, a.ID(), session.InjectData{Source: GreetingTag, Text: in.Text}); err != nil {
			a.SetStatus(agent.StatusFailed, err.Error())
			TurnEnded.Emit(d, TurnEndEvent{Agent: a.ID(), Session: ss.ID(), Input: in, TurnEnd: TurnEnd{Status: "failed", Error: "could not record input: " + err.Error()}})
			return
		}
	} else if _, err := ss.Append(session.TypeUserMessage, "", session.UserMessage{Text: in.Text, From: in.From, To: a.ID(), Attachments: in.Attachments, Reply: in.Reply, Use: in.Use}); err != nil {
		a.SetStatus(agent.StatusFailed, err.Error())
		TurnEnded.Emit(d, TurnEndEvent{Agent: a.ID(), Session: ss.ID(), Input: in, TurnEnd: TurnEnd{Status: "failed", Error: "could not record input: " + err.Error()}})
		return
	}
	// messages sent in a row join this turn: one reply, not one per message
	l.join(ctx, a, ss, in)

	nameChat(a, in.Text)

	ref, ok := l.selectModel(a, in)
	if !ok {
		l.endTurn(a, ss, in, TurnEnd{Status: "failed", Error: "no model configured"})
		return
	}
	_, _ = ss.Append(session.TypeTurnStart, a.ID(), TurnStart{Input: in.ID, Model: ref, Tag: in.Tag})
	TurnStarted.Emit(d, TurnStartEvent{Agent: a, Session: ss, Input: in})

	steps := 0
	var watch stuckWatch
	for steps < l.cfg.MaxSteps {
		if ctx.Err() != nil {
			l.endTurn(a, ss, in, TurnEnd{Status: stopStatus(parent), Steps: steps})
			return
		}
		if steps > 0 {
			// a message sent while the agent works is seen at the next step
			l.join(ctx, a, ss, in)
		}
		steps++
		var resp llm.Response
		ref, resp, err = l.step(ctx, a, ss, ref, steps, agent.Stricter(a.Mode(), in.Mode))
		if err != nil {
			if ctx.Err() != nil {
				l.endTurn(a, ss, in, TurnEnd{Status: stopStatus(parent), Steps: steps})
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
			// a call through call_tool runs (and is recorded) as the tool it names
			name, args, _ := unwrapCall(tc.Name, tc.Args)
			exe := l.toolsReg.Run(ctx, l.c, tools.Call{
				ID: tc.ID, Name: name, Args: args, Agent: a.ID(), Session: ss,
				Mode: agent.Stricter(a.Mode(), in.Mode), Workspace: a.Workspace(),
			})
			stop = stop || exe.Result.StopTurn
			if exe.Parked {
				l.parkedNow(a.ID(), ss.ID(), exe)
			} else {
				watch.see(name, args, exe.Result)
			}
		}
		switch v := watch.verdict(); v.action {
		case stuckNudge:
			_, _ = ss.Append(session.TypeInject, a.ID(), session.InjectData{Text: v.text, Source: "watchdog"})
		case stuckStop:
			_, _ = ss.Append(session.TypeNotice, a.ID(), map[string]string{"text": v.text})
			l.endTurn(a, ss, in, TurnEnd{Status: "failed", Error: v.text, Steps: steps})
			return
		}
		_, _ = ss.Append(session.TypeStepEnd, a.ID(), map[string]int{"step": steps})
		if stop {
			break
		}
		if len(calls) > 0 {
			continue
		}
		// a message that arrived during the final reply is answered in
		// this turn rather than queued behind it
		if l.join(ctx, a, ss, in) > 0 {
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

// join records the queued messages that can join the turn of in
// (agent.Absorb) as user messages of this turn; it returns how many.
func (l *loop) join(ctx context.Context, a *agent.Agent, ss *session.Session, in agent.Input) int {
	d := kernel.For(l.c, a.ID())
	n := 0
	for _, x := range a.Absorb(in) {
		xp, err := PreStep.Run(d, ctx, &x, func(_ context.Context, y *agent.Input) (*agent.Input, error) { return y, nil })
		if err != nil || xp == nil {
			_, _ = ss.Append(session.TypeNotice, a.ID(), map[string]string{"text": fmt.Sprintf("input rejected: %v", err)})
			continue
		}
		if !xp.Logged {
			if err := ResolveInput(d, ctx, xp); err != nil {
				_, _ = ss.Append(session.TypeNotice, a.ID(), map[string]string{"text": fmt.Sprintf("input rejected: %v", err)})
				continue
			}
		}
		if xp.Logged {
			n++
		} else if _, err := ss.Append(session.TypeUserMessage, "", session.UserMessage{Text: xp.Text, From: xp.From, To: a.ID(), Attachments: xp.Attachments, Reply: xp.Reply, Use: xp.Use}); err == nil {
			n++
		}
	}
	return n
}

func (l *loop) endTurn(a *agent.Agent, ss *session.Session, in agent.Input, te TurnEnd) {
	l.toolsReg.Unpark(a.ID())
	_, _ = ss.Append(session.TypeTurnEnd, a.ID(), te)
	if te.Status == "failed" {
		a.SetStatus(agent.StatusFailed, te.Error)
	} else {
		a.SetStatus(agent.StatusIdle, "")
	}
	TurnEnded.Emit(kernel.For(l.c, a.ID()), TurnEndEvent{Agent: a.ID(), Session: ss.ID(), Input: in, Text: turnText(ss, a.ID()), TurnEnd: te})
}

// turnText is the text of the agent's last reply in the turn that just
// ended: none when the turn wrote no reply (stopped before the model
// answered, failed on its first step), never an earlier turn's.
func turnText(ss *session.Session, agentID string) string {
	evs := ss.Events()
	// back from the end: past this turn's own turn/end (just appended;
	// other agents may have written after it in a channel), up to its start
	own := false
	for i := len(evs) - 1; i >= 0; i-- {
		e := evs[i]
		if e.Agent != agentID {
			continue
		}
		switch e.Type {
		case session.TypeTurnEnd:
			if !own {
				own = true
				continue
			}
			return "" // the turn before: this one wrote nothing
		case session.TypeTurnStart:
			return ""
		case session.TypeAssistantMessage:
			var m session.AssistantMessage
			if e.Decode(&m) == nil {
				return m.Message.Text()
			}
			return ""
		}
	}
	return ""
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
	ms, err := l.llms.ModelsOf(ctx, ad) // the catalog fills windows providers do not report
	if err == nil {
		for _, m := range ms {
			if m.ID == ref.Model && m.ContextWindow > 0 {
				return m.ContextWindow
			}
		}
	}
	return l.cfg.DefaultWindow
}

// nameChat names an unnamed blank chat after its first message (a
// generated topic, when available, is shown instead later).
func nameChat(a *agent.Agent, text string) {
	if !a.IsChat() || a.Spec().Name != agent.ChatDefaultName {
		return
	}
	if t := agent.ChatNameFrom(text); t != "" {
		_ = a.Update(func(s *agent.Spec) { s.Name = t })
	}
}

// SystemSections are the prompt sections a's requests carry: all of them
// for an agent, none for a blank chat (a plain conversation with the
// model: no system prompt, no tools; compacted history still arrives as
// a summary message).
func SystemSections(p *prompt.Assembler, a *agent.Agent) []prompt.Section {
	if a.IsChat() {
		return nil
	}
	return p.Sections(a.ID())
}

// System renders a's system prompt (as prompt.Assembler.Assemble does).
func System(p *prompt.Assembler, a *agent.Agent) string {
	var parts []string
	for _, s := range SystemSections(p, a) {
		parts = append(parts, s.Text)
	}
	return strings.Join(parts, "\n\n")
}

func (l *loop) system(a *agent.Agent) string { return System(l.prompts, a) }

func (l *loop) build(a *agent.Agent, ss *session.Session, ad llm.Adapter, ref llm.ModelRef, mode string, win int) llm.Request {
	plan := l.planTools(a, ss, ad, ref.Model, win)
	l.offers.set(a.ID(), ss.ID(), plan)
	system := l.system(a)
	if plan.Section != "" {
		system = strings.TrimSpace(system + "\n\n" + plan.Section)
	}
	return llm.Request{
		Model:       ref.Model,
		Effort:      ref.Effort,
		System:      system,
		Messages:    session.DeriveMessages(ss.Events(), a.ID(), l.agents.Names()),
		Tools:       plan.Tools,
		NativeTools: a.Spec().NativeTools,
		Agent:       a.ID(),
		Session:     ss.ID(),
		Workspace:   a.Workspace(),
		Mode:        mode,
	}
}

// step performs one model request with failover; returns the model that
// finally answered.
func (l *loop) step(ctx context.Context, a *agent.Agent, ss *session.Session, ref llm.ModelRef, n int, mode string) (llm.ModelRef, llm.Response, error) {
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
		win := l.window(ctx, ad, ref)
		req := l.build(a, ss, ad, ref, mode, win)
		req.Tools = ChatTools(a, req.Tools)
		if h, ok := kernel.Lookup[Hydrator](l.c, AttachmentsKey); ok {
			h.Hydrate(ctx, a.Workspace(), ad, ref.Model, &req)
		}
		est := EstimateContext(req)
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
		if a.IsChat() {
			// a plain chat stays plain whatever a Request listener added
			req.System, req.Tools, req.NativeTools = "", nil, nil
		}
		l.record(a, ss, req)
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
			recordNativeLoads(ss, a.ID(), resp.Message)
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
	// A gateway answers a model it does not have with a generic error
	// (magpie: 503 无可用渠道); the provider's own list says what it is.
	if !l.modelListed(ctx, ref) {
		cause = &llm.Error{Kind: llm.ErrModelNotFound, Provider: ref.Provider, Message: cause.Error()}
	}
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

// modelListed reports whether ref's provider lists the model; true when
// it cannot tell (no list, an error, or the CLI-style "default").
func (l *loop) modelListed(ctx context.Context, ref llm.ModelRef) bool {
	ad, ok := l.llms.Get(ref.Provider)
	if !ok {
		return false
	}
	if ref.Model == "" || ref.Model == "default" {
		return true
	}
	c, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ms, err := l.llms.ModelsOf(c, ad)
	if err != nil || len(ms) == 0 {
		return true
	}
	for _, m := range ms {
		if m.ID == ref.Model {
			return true
		}
	}
	return false
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

// ---------------------------------------------------------------- stuck

// stuckWatch notices a turn going round in circles: the same tool call
// (name + arguments) getting the same result again and again, or a long
// run of failing calls. It first nudges the model to change approach, and
// stops the turn if that does not help, instead of burning the step budget.
type stuckWatch struct {
	lastSig  string
	repeats  int // consecutive identical call+result
	failures int // consecutive failing calls
	nudged   bool
	nudgedAt int // repeats / failures when nudged
}

// Thresholds.
const (
	stuckRepeatNudge = 3
	stuckRepeatStop  = 5
	stuckFailNudge   = 6
	stuckFailStop    = 10
)

type stuckAction int

const (
	stuckNone stuckAction = iota
	stuckNudge
	stuckStop
)

type stuckVerdict struct {
	action stuckAction
	text   string
}

func (w *stuckWatch) see(name string, args json.RawMessage, r tools.Result) {
	h := sha256.Sum256([]byte(name + "\x00" + string(args) + "\x00" + r.Content))
	sig := hex.EncodeToString(h[:8])
	if sig == w.lastSig {
		w.repeats++
	} else {
		w.lastSig, w.repeats = sig, 1
	}
	if r.IsError {
		w.failures++
	} else {
		w.failures = 0
	}
}

func (w *stuckWatch) verdict() stuckVerdict {
	switch {
	case w.repeats >= stuckRepeatStop:
		return stuckVerdict{stuckStop, fmt.Sprintf("已停止：连续 %d 次用相同参数调用同一个工具，结果都一样，Agent 在原地打转。请给它更具体的指示，或换个做法。", w.repeats)}
	case w.failures >= stuckFailStop:
		return stuckVerdict{stuckStop, fmt.Sprintf("已停止：连续 %d 次工具调用失败。请查看最近的错误，给 Agent 更具体的指示或放宽权限后再继续。", w.failures)}
	case !w.nudged && w.repeats >= stuckRepeatNudge:
		w.nudged = true
		return stuckVerdict{stuckNudge, fmt.Sprintf("[watchdog] You have made the same tool call with the same arguments %d times and got the same result each time. "+
			"Repeating it will not change the outcome. Stop, think about why, and try a different approach — or, if you are blocked, explain what you need and end your turn.", w.repeats)}
	case !w.nudged && w.failures >= stuckFailNudge:
		w.nudged = true
		return stuckVerdict{stuckNudge, fmt.Sprintf("[watchdog] Your last %d tool calls all failed. Step back: read the errors, check your assumptions (paths, permissions, run mode), "+
			"and change approach. If something outside your control is blocking you, tell the user what you need and end your turn.", w.failures)}
	}
	return stuckVerdict{}
}

// stopStatus is how a cancelled turn ends: "interrupted" when the host is
// shutting down (picked up again after the restart), "cancelled" when a
// person stopped it.
func stopStatus(parent context.Context) string {
	if parent.Err() != nil {
		return StatusInterrupted
	}
	return "cancelled"
}

// StatusInterrupted ends a turn cut off by a shutdown.
const StatusInterrupted = "interrupted"

// ResumeNote starts a turn picked up again after a restart.
const ResumeNote = "[Crewbase restarted] Your previous turn was cut off in the middle by a restart; tool calls that were running then were interrupted. " +
	"Pick up the same request where you left off: first check what is already done (files, commands' effects) instead of assuming, " +
	"do not repeat actions with side effects that already happened, then finish the work."

// RejectForReply is the rejection a pending approval gets when the person
// writes instead of answering it.
const RejectForReply = "The user sent a new message instead of approving this. Do not retry it; read their message and follow it."

// UseRequest is a message's text and / picks, for the agent it goes to.
type UseRequest struct {
	Agent string
	Text  string
	Use   []session.Invoke
}

// ResolveInput runs ResolveUse on in.
func ResolveInput(d kernel.Dispatcher, ctx context.Context, in *agent.Input) error {
	if len(in.Use) == 0 && !strings.HasPrefix(in.Text, "/") {
		return nil
	}
	r, err := ResolveUse.Run(d, ctx, &UseRequest{Agent: in.Agent, Text: in.Text, Use: in.Use}, func(_ context.Context, r *UseRequest) (*UseRequest, error) { return r, nil })
	if err != nil {
		return err
	}
	if r != nil {
		in.Text, in.Use = r.Text, r.Use
	}
	return nil
}
