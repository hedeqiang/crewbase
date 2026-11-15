package agentloop_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

type env struct {
	h      *kernel.Host
	agents *agent.Registry
	llms   *llm.Registry
	inbox  *approval.Inbox
	root   string
}

func newEnv(t *testing.T, root string) *env {
	t.Helper()
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	load := func(s kernel.Spec, cfg map[string]any) {
		if _, err := h.Load(s, kernel.LoadOptions{Config: cfg}); err != nil {
			t.Fatal(err)
		}
	}
	load(session.Plugin, map[string]any{"dir": root + "/sessions"})
	load(prompt.Plugin, nil)
	load(tools.Plugin, nil)
	load(approval.Plugin, nil)
	load(approval.DesktopPlugin, nil)
	load(llm.Plugin, nil)
	load(mock.Plugin, map[string]any{"ids": []string{"claude-code", "codex"}})
	load(router.Plugin, nil)
	load(agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"claude-code/default", "codex/default"}})
	load(agentloop.Plugin, nil)
	for _, p := range h.Plugins() {
		if p.State != kernel.StateActive {
			t.Fatalf("plugin not active: %+v", p)
		}
	}
	r := h.Root()
	return &env{h: h, root: root,
		agents: kernel.Use[*agent.Registry](r, agent.ServiceKey),
		llms:   kernel.Use[*llm.Registry](r, llm.ServiceKey),
		inbox:  kernel.Use[*approval.Inbox](r, approval.InboxKey),
	}
}

func lastAssistant(t *testing.T, ss *session.Session) (session.AssistantMessage, string) {
	t.Helper()
	e, ok := ss.Last(session.TypeAssistantMessage)
	if !ok {
		t.Fatal("no assistant message")
	}
	var m session.AssistantMessage
	e.Decode(&m)
	return m, m.Message.Text()
}

func send(t *testing.T, a *agent.Agent, text string) {
	t.Helper()
	if _, err := a.Enqueue(agent.Input{Text: text}); err != nil {
		t.Fatal(err)
	}
}

// Phase 1 exit criteria (with mock models): quota exhaustion switches to
// the next model and continues; a push action is suspended for approval and
// resumes after approval; the session can be rebuilt from the log.
func TestPhase1ExitCriteria(t *testing.T) {
	root := t.TempDir()
	e := newEnv(t, root)

	// a push-category tool, plus a rule asking for push
	e.h.Load(kernel.Spec{Name: "push-tool", Inject: []string{"tools"}, Apply: func(c *kernel.Context) error {
		kernel.Use[*tools.Registry](c, "tools").Register(c, &tools.Tool{
			Name: "git_push", Category: tools.CategoryPush,
			Execute: func(context.Context, tools.Call) (tools.Result, error) { return tools.Text("pushed main"), nil },
		})
		tools.PreExecute.Use(c, func(ctx context.Context, d *tools.Decision, next kernel.Next[*tools.Decision, *tools.Decision]) (*tools.Decision, error) {
			if d.Category == tools.CategoryPush {
				d.RequireApproval("code push asks by default")
			}
			return next(ctx, d)
		})
		return nil
	}}, kernel.LoadOptions{})

	a, err := e.agents.Create(agent.Spec{Name: "Kai", Role: "engineer"})
	if err != nil {
		t.Fatal(err)
	}
	ss, _ := a.Session()

	send(t, a, "hello")
	if !agentloop.WaitIdle(a, 2*time.Second) {
		t.Fatal("not idle")
	}
	if m, txt := lastAssistant(t, ss); m.Model.Provider != "claude-code" || txt != "default: hello" {
		t.Fatalf("got %s %q", m.Model, txt)
	}

	// Claude Code quota exhausted → switch to Codex, keep going.
	cc, _ := e.llms.Get("claude-code")
	cc.(*mock.Adapter).SetExhausted(true)
	start := time.Now()
	send(t, a, "continue")
	agentloop.WaitIdle(a, 2*time.Second)
	if time.Since(start) > 5*time.Second {
		t.Fatal("switch must complete within 5s")
	}
	m, txt := lastAssistant(t, ss)
	if m.Model.Provider != "codex" || txt != "default: continue" {
		t.Fatalf("got %s %q", m.Model, txt)
	}
	sw, ok := ss.Last(session.TypeModelSwitched)
	if !ok {
		t.Fatal("no model/switched event")
	}
	var swd session.ModelSwitchData
	sw.Decode(&swd)
	if swd.From.Provider != "claude-code" || swd.To.Provider != "codex" || swd.Reason != "quota_exhausted" {
		t.Fatalf("%+v", swd)
	}

	// Codex's request is reconstructible from the log: it contains the
	// earlier exchange answered by Claude Code.
	cx, _ := e.llms.Get("codex")
	reqs := cx.(*mock.Adapter).Requests()
	got := reqs[len(reqs)-1].Messages
	want := session.DeriveMessages(ss.Events(), a.ID(), e.agents.Names())
	// the log now also holds the final assistant message
	if len(got) != len(want)-1 {
		t.Fatalf("request not rebuilt from log: %d vs %d", len(got), len(want))
	}
	bg, _ := json.Marshal(got)
	bw, _ := json.Marshal(want[:len(want)-1])
	if string(bg) != string(bw) {
		t.Fatalf("mismatch\n%s\n%s", bg, bw)
	}

	// git push → approval → resume from the breakpoint
	send(t, a, `/tool git_push {"branch":"main"}`)
	deadline := time.Now().Add(2 * time.Second)
	for len(e.inbox.Pending()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	p := e.inbox.Pending()
	if len(p) != 1 || p[0].Category != tools.CategoryPush || p[0].Agent != a.ID() {
		t.Fatalf("pending %+v", p)
	}
	if st, _ := a.Status(); st != agent.StatusRunning {
		t.Fatalf("status %s", st)
	}
	e.inbox.Decide(p[0].CallID, approval.Decision{Outcome: approval.OutcomeAllowedOnce})
	agentloop.WaitIdle(a, 2*time.Second)
	if _, txt := lastAssistant(t, ss); txt != "Tool git_push returned: pushed main" {
		t.Fatalf("%q", txt)
	}

	// Restart from disk: agent, session and messages come back.
	e.h.Close()
	e2 := newEnv(t, root)
	a2, ok := e2.agents.Get(a.ID())
	if !ok {
		t.Fatal("agent not restored")
	}
	ss2, _ := a2.Session()
	if len(ss2.Events()) != len(ss.Events()) {
		t.Fatalf("events %d vs %d", len(ss2.Events()), len(ss.Events()))
	}
	if !strings.Contains(a2.Instructions(), "engineer") {
		t.Fatal("instructions file")
	}
}

func TestQueuePauseResumeStopAndNoModel(t *testing.T) {
	e := newEnv(t, t.TempDir())
	a, _ := e.agents.Create(agent.Spec{Name: "Otto"})
	ss, _ := a.Session()
	a.Pause()
	send(t, a, "one")
	send(t, a, "two")
	jump, _ := a.Enqueue(agent.Input{Text: "urgent", Front: true})
	if q := a.Queue(); len(q) != 3 || q[0].ID != jump.ID {
		t.Fatalf("queue %+v", q)
	}
	a.Cancel(a.Queue()[2].ID) // cancel "two"
	a.Resume()
	agentloop.WaitIdle(a, 2*time.Second)
	var texts []string
	for _, ev := range ss.Events() {
		if ev.Type == session.TypeUserMessage {
			var m session.UserMessage
			ev.Decode(&m)
			texts = append(texts, m.Text)
		}
	}
	if strings.Join(texts, ",") != "urgent,one" {
		t.Fatalf("order %v", texts)
	}

	// all models exhausted → failed turn with a clear error
	for _, id := range []string{"claude-code", "codex"} {
		ad, _ := e.llms.Get(id)
		ad.(*mock.Adapter).SetExhausted(true)
	}
	send(t, a, "x")
	agentloop.WaitIdle(a, 2*time.Second)
	if st, detail := a.Status(); st != agent.StatusFailed || !strings.Contains(detail, "no available model") {
		t.Fatalf("%s %s", st, detail)
	}
}

func TestQueueRestoredAfterRestart(t *testing.T) {
	root := t.TempDir()
	e := newEnv(t, root)
	a, _ := e.agents.Create(agent.Spec{Name: "Iris"})
	a.Pause()
	send(t, a, "later")
	e.h.Close()
	e2 := newEnv(t, root)
	a2, _ := e2.agents.Get(a.ID())
	if q := a2.Queue(); len(q) != 1 || q[0].Text != "later" || !a2.Paused() {
		t.Fatalf("queue not restored: %+v paused=%v", q, a2.Paused())
	}
}

func TestEphemeralAgentNotPersisted(t *testing.T) {
	root := t.TempDir()
	e := newEnv(t, root)
	a, err := e.agents.Create(agent.Spec{Name: "Lead-sub-1", Ephemeral: true, Parent: "lead"})
	if err != nil {
		t.Fatal(err)
	}
	ws := a.Workspace()
	send(t, a, "hi")
	agentloop.WaitIdle(a, 2*time.Second)
	e.h.Close() // crash-like: never deleted
	e2 := newEnv(t, root)
	if _, ok := e2.agents.Get(a.ID()); ok {
		t.Fatal("ephemeral agent restored")
	}
	if _, err := os.Stat(ws); !os.IsNotExist(err) {
		t.Fatal("leftover ephemeral workspace not swept")
	}
	b, _ := e2.agents.Create(agent.Spec{Name: "Tmp", Ephemeral: true})
	ws = b.Workspace()
	e2.agents.Delete(b.ID())
	if _, err := os.Stat(ws); !os.IsNotExist(err) {
		t.Fatal("ephemeral workspace kept after delete")
	}
}

// A blank chat works like an agent (tools run) but its system prompt
// carries no persona, instructions, memory or plugin fragments.
func TestBlankChat(t *testing.T) {
	e := newEnv(t, t.TempDir())
	ran := false
	tr := kernel.Use[*tools.Registry](e.h.Root(), tools.ServiceKey)
	tr.Register(e.h.Root(), &tools.Tool{Name: "touch", Description: "x", Execute: func(context.Context, tools.Call) (tools.Result, error) {
		ran = true
		return tools.Result{Content: "done"}, nil
	}})
	pa := kernel.Use[*prompt.Assembler](e.h.Root(), prompt.ServiceKey)
	pa.Register(e.h.Root(), prompt.Static("plugin-style", 50, "ALWAYS ANSWER IN PIRATE"))
	c, err := e.agents.Create(agent.Spec{Kind: agent.KindChat})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(c.ID(), "chat") || c.Spec().Name != "新对话" || c.Mode() != agent.ModeSandbox {
		t.Fatalf("chat spec %+v", c.Spec())
	}
	if _, err := os.Stat(c.Workspace() + "/INSTRUCTIONS.md"); err == nil {
		t.Fatal("a chat must not get instructions")
	}
	c2, _ := e.agents.Create(agent.Spec{Kind: agent.KindChat})
	if c2.ID() == c.ID() {
		t.Fatal("chat ids must be unique")
	}
	ad, _ := e.llms.Get("claude-code")
	m := ad.(*mock.Adapter)
	send(t, c, "/tool touch {}")
	agentloop.WaitIdle(c, 2*time.Second)
	reqs := m.Requests()
	if len(reqs) == 0 {
		t.Fatal("no request")
	}
	r := reqs[0]
	if !strings.Contains(r.System, "Crewbase") || !strings.Contains(r.System, c.Workspace()) {
		t.Fatalf("chat lost the base rules / workspace: %q", r.System)
	}
	if strings.Contains(r.System, "PIRATE") || strings.Contains(r.System, "Your name is") {
		t.Fatalf("chat carried persona or plugin prompt: %q", r.System)
	}
	if len(r.Tools) == 0 || !ran {
		t.Fatal("a chat must still use tools")
	}
	if n := c.Spec().Name; n != "/tool touch {}" {
		t.Fatalf("chat not named after its first message: %q", n)
	}
	// an agent gets the full prompt
	a, _ := e.agents.Create(agent.Spec{Name: "Otto"})
	if !strings.Contains(agentloop.System(pa, a), "PIRATE") {
		t.Fatal("agents keep plugin fragments")
	}
	if _, err := e.agents.Create(agent.Spec{Name: "x", Kind: "robot"}); err == nil {
		t.Fatal("unknown kind accepted")
	}
}

func userTexts(ss *session.Session) []string {
	var out []string
	for _, ev := range ss.Events() {
		if ev.Type == session.TypeUserMessage {
			var m session.UserMessage
			ev.Decode(&m)
			out = append(out, m.Text)
		}
	}
	return out
}

func count(ss *session.Session, typ string) int {
	n := 0
	for _, ev := range ss.Events() {
		if ev.Type == typ {
			n++
		}
	}
	return n
}

// Messages sent in a row become one turn with one reply; tagged inputs
// (IM, workflows, routines, subagents) and other conversations never join.
func TestMessagesInARowJoinOneTurn(t *testing.T) {
	e := newEnv(t, t.TempDir())
	ad, _ := e.llms.Get("claude-code")
	m := ad.(*mock.Adapter)
	a, _ := e.agents.Create(agent.Spec{Name: "Ivy"})
	ss, _ := a.Session()
	a.Pause()
	send(t, a, "fix the login bug")
	send(t, a, "it's in auth.go")
	send(t, a, "and add a test")
	tagged, _ := a.Enqueue(agent.Input{Text: "from slack", Tag: "im:x"})
	a.Resume()
	agentloop.WaitIdle(a, 3*time.Second)
	if got := count(ss, session.TypeTurnStart); got != 2 {
		t.Fatalf("turns = %d, want 2 (three joined + the tagged one)", got)
	}
	reqs := m.Requests()
	first := reqs[0].Messages[len(reqs[0].Messages)-1].Text()
	for _, want := range []string{"fix the login bug", "it's in auth.go", "and add a test"} {
		if !strings.Contains(first, want) {
			t.Fatalf("first request misses %q: %q", want, first)
		}
	}
	if strings.Contains(first, "from slack") {
		t.Fatal("a tagged input joined")
	}
	if got := strings.Join(userTexts(ss), "|"); got != "fix the login bug|it's in auth.go|and add a test|from slack" {
		t.Fatalf("log order %q", got)
	}
	_ = tagged
}

// A message sent while the agent works is seen at its next step, in the
// same turn.
func TestMessageMidTurnIsSeenNextStep(t *testing.T) {
	e := newEnv(t, t.TempDir())
	ad, _ := e.llms.Get("claude-code")
	m := ad.(*mock.Adapter)
	a, _ := e.agents.Create(agent.Spec{Name: "Max"})
	ss, _ := a.Session()
	var calls int
	m.Script = func(req llm.Request) (llm.Response, error) {
		calls++
		last := req.Messages[len(req.Messages)-1].Text()
		switch calls {
		case 1: // first step: start work and, meanwhile, the person writes again
			send(t, a, "actually, use port 8080")
			return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: "tool_call", ToolCall: &llm.ToolCall{ID: "t1", Name: "noop", Args: json.RawMessage(`{}`)}}}}, StopReason: "tool_use"}, nil
		default:
			return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok: "+last), StopReason: "end"}, nil
		}
	}
	send(t, a, "start the server")
	agentloop.WaitIdle(a, 3*time.Second)
	if got := count(ss, session.TypeTurnStart); got != 1 {
		t.Fatalf("turns = %d, want 1", got)
	}
	_, text := lastAssistant(t, ss)
	if !strings.Contains(text, "use port 8080") {
		t.Fatalf("mid-turn message not seen at the next step: %q", text)
	}
}

// A model repeating the same call is nudged once, then stopped, instead of
// running to the step limit.
func TestStuckLoopIsNudgedThenStopped(t *testing.T) {
	e := newEnv(t, t.TempDir())
	ad, _ := e.llms.Get("claude-code")
	m := ad.(*mock.Adapter)
	a, _ := e.agents.Create(agent.Spec{Name: "Loopy"})
	ss, _ := a.Session()
	calls := 0
	nudgeSeen := false
	m.Script = func(req llm.Request) (llm.Response, error) {
		calls++
		for _, msg := range req.Messages {
			if strings.Contains(msg.Text(), "[watchdog]") {
				nudgeSeen = true
			}
		}
		return llm.Response{StopReason: "tool_use", Message: llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{
			{Type: "tool_call", ToolCall: &llm.ToolCall{ID: fmt.Sprintf("t%d", calls), Name: "missing_tool", Args: json.RawMessage(`{"x":1}`)}}}}}, nil
	}
	send(t, a, "do it")
	agentloop.WaitIdle(a, 5*time.Second)
	if !nudgeSeen {
		t.Fatal("the model was never told it was repeating itself")
	}
	if calls > 6 {
		t.Fatalf("ran %d steps; should stop after 5 identical calls", calls)
	}
	st, detail := a.Status()
	if st != agent.StatusFailed || !strings.Contains(detail, "原地打转") {
		t.Fatalf("status %s %q", st, detail)
	}
	_ = ss
}

// A turn cut off by a shutdown is picked up again after the restart, once,
// without repeating the person's message; a turn a person stopped is not.
func TestInterruptedTurnResumesAfterRestart(t *testing.T) {
	root := t.TempDir()
	e := newEnv(t, root)
	ad, _ := e.llms.Get("claude-code")
	m := ad.(*mock.Adapter)
	m.SetDelay(10 * time.Second) // still working when the host shuts down
	a, _ := e.agents.Create(agent.Spec{Name: "Rex"})
	ss, _ := a.Session()
	send(t, a, "migrate the database")
	deadline := time.Now().Add(3 * time.Second)
	for len(m.Requests()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	e.h.Close()

	e2 := newEnv(t, root)
	ad2, _ := e2.llms.Get("claude-code")
	m2 := ad2.(*mock.Adapter)
	var sawNote bool
	m2.Script = func(req llm.Request) (llm.Response, error) {
		for _, msg := range req.Messages {
			if strings.Contains(msg.Text(), "Crewbase restarted") {
				sawNote = true
			}
		}
		return llm.Response{StopReason: "end", Message: llm.TextMessage(llm.RoleAssistant, "migration finished")}, nil
	}
	a2, _ := e2.agents.Get(a.ID())
	agentloop.WaitIdle(a2, 3*time.Second)
	ss2, _ := e2.agents.Sessions().Get(ss.ID())
	if _, text := lastAssistant(t, ss2); text != "migration finished" || !sawNote {
		t.Fatalf("not resumed: %q note=%v", text, sawNote)
	}
	if got := strings.Join(userTexts(ss2), "|"); got != "migrate the database" {
		t.Fatalf("the message was repeated: %q", got)
	}
	e2.h.Close()

	// a third start does not run it again
	e3 := newEnv(t, root)
	a3, _ := e3.agents.Get(a.ID())
	if q := a3.Queue(); len(q) != 0 {
		t.Fatalf("resumed twice: %+v", q)
	}
}

func TestStoppedTurnIsNotResumed(t *testing.T) {
	root := t.TempDir()
	e := newEnv(t, root)
	ad, _ := e.llms.Get("claude-code")
	started := make(chan struct{}, 1)
	ad.(*mock.Adapter).Script = func(req llm.Request) (llm.Response, error) {
		started <- struct{}{}
		time.Sleep(3 * time.Second)
		return llm.Response{StopReason: "end", Message: llm.TextMessage(llm.RoleAssistant, "x")}, nil
	}
	a, _ := e.agents.Create(agent.Spec{Name: "Sid"})
	send(t, a, "long task")
	<-started
	a.Stop()
	agentloop.WaitIdle(a, 5*time.Second)
	e.h.Close()
	e2 := newEnv(t, root)
	a2, _ := e2.agents.Get(a.ID())
	if q := a2.Queue(); len(q) != 0 {
		t.Fatalf("a stopped turn was resumed: %+v", q)
	}
}

// Writing to an agent that waits for approval declines the request, and
// the message is what the agent reads next, in the same turn.
func TestReplyInsteadOfApproval(t *testing.T) {
	e := newEnv(t, t.TempDir())
	ad, _ := e.llms.Get("claude-code")
	m := ad.(*mock.Adapter)
	reg := kernel.Use[*tools.Registry](e.h.Root(), tools.ServiceKey)
	ran := false
	reg.Register(e.h.Root(), &tools.Tool{Name: "deploy", Category: tools.CategoryPublish, Description: "deploy",
		Execute: func(context.Context, tools.Call) (tools.Result, error) {
			ran = true
			return tools.Text("deployed"), nil
		}})
	tools.PreExecute.Use(e.h.Root(), func(ctx context.Context, d *tools.Decision, next kernel.Next[*tools.Decision, *tools.Decision]) (*tools.Decision, error) {
		if d.Call.Name == "deploy" {
			d.RequireApproval("deploys need approval")
		}
		return next(ctx, d)
	})
	a, _ := e.agents.Create(agent.Spec{Name: "Dee"})
	ss, _ := a.Session()
	step := 0
	m.Script = func(req llm.Request) (llm.Response, error) {
		step++
		if step == 1 {
			return llm.Response{StopReason: "tool_use", Message: llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{
				{Type: "tool_call", ToolCall: &llm.ToolCall{ID: "d1", Name: "deploy", Args: json.RawMessage(`{}`)}}}}}, nil
		}
		return llm.Response{StopReason: "end", Message: llm.TextMessage(llm.RoleAssistant, "ok: "+req.Messages[len(req.Messages)-1].Text())}, nil
	}
	send(t, a, "deploy to prod")
	deadline := time.Now().Add(3 * time.Second)
	for len(e.inbox.Pending()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(e.inbox.Pending()) != 1 {
		t.Fatal("no approval asked")
	}
	send(t, a, "wait, deploy to staging first")
	agentloop.WaitIdle(a, 3*time.Second)
	if ran {
		t.Fatal("the declined deploy ran")
	}
	if len(e.inbox.Pending()) != 0 {
		t.Fatal("approval still pending")
	}
	if got := count(ss, session.TypeTurnStart); got != 1 {
		t.Fatalf("turns = %d, want 1", got)
	}
	if _, text := lastAssistant(t, ss); !strings.Contains(text, "staging first") {
		t.Fatalf("reply not read next: %q", text)
	}
}

// A turn waiting for an approval does not hold the agent up: a background
// result arriving parks it (the request stays open), the agent moves on,
// and once the person approves, the same call runs without asking again.
func TestParkedApproval(t *testing.T) {
	root := t.TempDir()
	e := newEnv(t, root)
	ad, _ := e.llms.Get("claude-code")
	m := ad.(*mock.Adapter)
	reg := kernel.Use[*tools.Registry](e.h.Root(), tools.ServiceKey)
	var mu sync.Mutex
	var ran []string
	got := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), ran...)
	}
	reg.Register(e.h.Root(), &tools.Tool{Name: "deploy", Category: tools.CategoryPublish, Description: "deploy",
		Execute: func(_ context.Context, c tools.Call) (tools.Result, error) {
			mu.Lock()
			ran = append(ran, string(c.Args))
			mu.Unlock()
			return tools.Text("deployed"), nil
		}})
	tools.PreExecute.Use(e.h.Root(), func(ctx context.Context, d *tools.Decision, next kernel.Next[*tools.Decision, *tools.Decision]) (*tools.Decision, error) {
		if d.Call.Name == "deploy" {
			d.RequireApproval("deploys need approval")
		}
		return next(ctx, d)
	})
	a, _ := e.agents.Create(agent.Spec{Name: "Dee"})
	ss, _ := a.Session()
	calls := 0
	m.Script = func(req llm.Request) (llm.Response, error) {
		last := req.Messages[len(req.Messages)-1]
		text := last.Text()
		if last.Role != llm.RoleTool && (strings.Contains(text, "deploy to prod") || strings.Contains(text, "[Approved]")) {
			calls++
			return llm.Response{StopReason: "tool_use", Message: llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{
				{Type: "tool_call", ToolCall: &llm.ToolCall{ID: fmt.Sprintf("d%d", calls), Name: "deploy", Args: json.RawMessage(`{"env":"prod"}`)}}}}}, nil
		}
		return llm.Response{StopReason: "end", Message: llm.TextMessage(llm.RoleAssistant, "noted")}, nil
	}
	waitPending := func(n int) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for len(e.inbox.Pending()) != n && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if got := len(e.inbox.Pending()); got != n {
			t.Fatalf("pending approvals = %d, want %d", got, n)
		}
	}

	send(t, a, "deploy to prod")
	waitPending(1)
	// a background result is not the person answering: it parks the call
	if _, err := a.Enqueue(agent.Input{Session: ss.ID(), Tag: "job:build", Text: "build finished"}); err != nil {
		t.Fatal(err)
	}
	if !agentloop.WaitIdle(a, 3*time.Second) {
		t.Fatal("agent still held up by the approval")
	}
	waitPending(1) // the request is still open
	if len(got()) != 0 {
		t.Fatal("ran before approval")
	}
	if r := lastToolResult(t, ss); r.Content != tools.ParkedResult {
		t.Fatalf("parked call result: %q", r.Content)
	}
	if _, err := os.Stat(root + "/parked.json"); err != nil {
		t.Fatal("parked call not recorded")
	}

	e.inbox.Decide(e.inbox.Pending()[0].CallID, approval.Decision{Outcome: approval.OutcomeAllowedOnce})
	deadline := time.Now().Add(3 * time.Second)
	for len(got()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	agentloop.WaitIdle(a, 3*time.Second)
	if r := got(); len(r) != 1 || r[0] != `{"env":"prod"}` {
		t.Fatalf("approved call ran %v", r)
	}
	if len(e.inbox.Pending()) != 0 {
		t.Fatal("asked again after approval")
	}
	if _, err := os.Stat(root + "/parked.json"); !os.IsNotExist(err) {
		t.Fatal("parked record not cleared")
	}
	// the grant was used up: the same call asks again
	send(t, a, "deploy to prod")
	waitPending(1)
}

// A parked approval lost in a restart: the agent is told to ask again.
func TestParkedApprovalLostInRestart(t *testing.T) {
	root := t.TempDir()
	e := newEnv(t, root)
	a, _ := e.agents.Create(agent.Spec{Name: "Dee"})
	ss, _ := a.Session()
	id, sid := a.ID(), ss.ID()
	e.h.Close()
	if err := os.WriteFile(root+"/parked.json", []byte(`[{"agent":"`+id+`","session":"`+sid+`","callId":"d1","tool":"deploy"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	e2 := newEnv(t, root)
	a2, _ := e2.agents.Get(id)
	agentloop.WaitIdle(a2, 3*time.Second)
	ss2, _ := a2.Session()
	found := false
	for _, ev := range ss2.Events() {
		if ev.Type == session.TypeUserMessage && strings.Contains(string(ev.Data), "lost in the restart") {
			found = true
		}
	}
	if !found {
		t.Fatal("agent not told the approval was lost")
	}
	if _, err := os.Stat(root + "/parked.json"); !os.IsNotExist(err) {
		t.Fatal("record not cleared")
	}
}

func lastToolResult(t *testing.T, ss *session.Session) llm.ToolResult {
	t.Helper()
	e, ok := ss.Last(session.TypeToolResult)
	if !ok {
		t.Fatal("no tool result")
	}
	var r llm.ToolResult
	e.Decode(&r)
	return r
}
