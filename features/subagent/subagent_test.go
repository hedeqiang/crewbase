package subagent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	capsub "github.com/hedeqiang/crewbase/capabilities/subagent"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/features/subagent"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

type env struct {
	h      *kernel.Host
	agents *agent.Registry
	tools  *tools.Registry
	svc    *capsub.Service
	mock   *mock.Adapter
}

func newEnv(t *testing.T, limits map[string]any, extra ...kernel.Spec) *env {
	t.Helper()
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	root := t.TempDir()
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, nil}, {prompt.Plugin, nil}, {tools.Plugin, nil},
		{llm.Plugin, nil}, {mock.Plugin, nil}, {router.Plugin, nil},
		{agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"mock/default"}}},
		{agentloop.Plugin, nil},
		{capsub.Plugin, limits}, {subagent.RunnerPlugin, nil}, {subagent.LocalPlugin, nil},
		{subagent.ForkPlugin, nil}, {subagent.DelegatePlugin, nil},
	} {
		if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range extra {
		if _, err := h.Load(s, kernel.LoadOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range h.Plugins() {
		if p.State != kernel.StateActive {
			t.Fatalf("plugin not active: %+v", p)
		}
	}
	r := h.Root()
	ad, _ := kernel.Use[*llm.Registry](r, llm.ServiceKey).Get("mock")
	return &env{h: h,
		agents: kernel.Use[*agent.Registry](r, agent.ServiceKey),
		tools:  kernel.Use[*tools.Registry](r, tools.ServiceKey),
		svc:    kernel.Use[*capsub.Service](r, capsub.ServiceKey),
		mock:   ad.(*mock.Adapter),
	}
}

func delegateCmd(args map[string]any) string {
	b, _ := json.Marshal(args)
	return "/tool delegate " + string(b)
}

func run(t *testing.T, a *agent.Agent, text string) string {
	t.Helper()
	if _, err := a.Enqueue(agent.Input{Text: text}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if !agentloop.WaitIdle(a, 5*time.Second) {
		t.Fatal("agent not idle")
	}
	ss, _ := a.Session()
	e, ok := ss.Last(session.TypeAssistantMessage)
	if !ok {
		t.Fatal("no assistant message")
	}
	var m session.AssistantMessage
	_ = e.Decode(&m)
	return m.Message.Text()
}

func spawned(t *testing.T, ss *session.Session) []capsub.SpawnedData {
	t.Helper()
	var out []capsub.SpawnedData
	for _, e := range ss.Events() {
		if e.Type == capsub.TypeSpawned {
			var d capsub.SpawnedData
			_ = e.Decode(&d)
			out = append(out, d)
		}
	}
	return out
}

func TestLocalDelegationLineageAndCleanup(t *testing.T) {
	e := newEnv(t, nil)
	kai, _ := e.agents.Create(agent.Spec{Name: "Kai"})
	ss, _ := kai.Session()

	got := run(t, kai, delegateCmd(map[string]any{"task": "summarize the README"}))
	if got != "Tool delegate returned: default: summarize the README" {
		t.Fatalf("%q", got)
	}
	sp := spawned(t, ss)
	if len(sp) != 1 || sp[0].Depth != 1 || sp[0].Parent != kai.ID() || !sp[0].Ephemeral || sp[0].Provider != "local" {
		t.Fatalf("lineage %+v", sp)
	}
	if !strings.HasPrefix(sp[0].Child, kai.ID()+"-sub-") {
		t.Fatalf("child id %s", sp[0].Child)
	}
	if _, ok := e.agents.Get(sp[0].Child); ok {
		t.Fatal("ephemeral agent must be deleted afterwards")
	}
	css, err := e.agents.Sessions().Get(sp[0].ChildSession)
	if err != nil {
		t.Fatal(err)
	}
	if m := css.Meta(); m.Parent != ss.ID() || m.Kind != subagent.SessionKind {
		t.Fatalf("child meta %+v", m)
	}
	if subagent.Depth(e.agents.Sessions(), css) != 1 || subagent.Depth(e.agents.Sessions(), ss) != 0 {
		t.Fatal("depth from lineage")
	}
	if _, ok := ss.Last(capsub.TypeFinished); !ok {
		t.Fatal("no subagent/finished")
	}

	// Delegation to a named existing agent keeps it.
	otto, _ := e.agents.Create(agent.Spec{Name: "Otto"})
	got = run(t, kai, delegateCmd(map[string]any{"task": "check prices", "agent": otto.ID()}))
	if got != "Tool delegate returned: default: check prices" {
		t.Fatalf("%q", got)
	}
	if _, ok := e.agents.Get(otto.ID()); !ok {
		t.Fatal("named agent must survive")
	}
	// Delegating to oneself is refused.
	got = run(t, kai, delegateCmd(map[string]any{"task": "x", "agent": kai.ID()}))
	if !strings.Contains(got, "cannot delegate to itself") {
		t.Fatalf("%q", got)
	}
}

func TestPrivateToolsNotInherited(t *testing.T) {
	e := newEnv(t, nil)
	kai, _ := e.agents.Create(agent.Spec{Name: "Kai"})
	e.tools.Register(kai.Ctx(), &tools.Tool{Name: "secret", Execute: func(context.Context, tools.Call) (tools.Result, error) {
		return tools.Text("s3cr3t"), nil
	}})
	if got := run(t, kai, "/tool secret {}"); got != "Tool secret returned: s3cr3t" {
		t.Fatalf("parent should see its private tool: %q", got)
	}
	got := run(t, kai, delegateCmd(map[string]any{"task": "/tool secret {}"}))
	if !strings.Contains(got, "unknown tool: secret") || strings.Contains(got, "s3cr3t") {
		t.Fatalf("child must not see the parent's private tool: %q", got)
	}
	// The child's requests never listed it.
	for _, r := range e.mock.Requests() {
		if !strings.Contains(r.System, "-sub-") {
			continue
		}
		for _, ts := range r.Tools {
			if ts.Name == "secret" {
				t.Fatal("secret tool offered to child")
			}
		}
	}
}

func TestDepthLimit(t *testing.T) {
	e := newEnv(t, map[string]any{"maxDepth": 2})
	kai, _ := e.agents.Create(agent.Spec{Name: "Kai"})
	leaf := delegateCmd(map[string]any{"task": "leaf"})
	mid := delegateCmd(map[string]any{"task": leaf})
	top := delegateCmd(map[string]any{"task": mid})
	got := run(t, kai, top)
	if !strings.Contains(got, "maximum delegation depth") {
		t.Fatalf("depth must be bounded: %q", got)
	}
	// depth 1 and 2 ran; nothing at depth 3; every ephemeral agent is gone.
	for _, a := range e.agents.List() {
		if a.ID() != kai.ID() {
			t.Fatalf("leftover agent %s", a.ID())
		}
	}
	maxDepth := 0
	for _, m := range e.agents.Sessions().List() {
		ss, _ := e.agents.Sessions().Get(m.ID)
		for _, d := range spawned(t, ss) {
			maxDepth = max(maxDepth, d.Depth)
		}
	}
	if maxDepth != 2 {
		t.Fatalf("max recorded depth %d", maxDepth)
	}
}

type blockingProvider struct {
	started chan struct{}
	release chan struct{}
}

func (p *blockingProvider) Name() string { return "block" }
func (p *blockingProvider) Spawn(ctx context.Context, req capsub.Request) (capsub.Result, error) {
	p.started <- struct{}{}
	select {
	case <-p.release:
		return capsub.Result{Output: "done " + req.Task, Diagnostic: strings.Repeat("x", 10000)}, nil
	case <-ctx.Done():
		return capsub.Result{StopReason: capsub.StopCancelled}, ctx.Err()
	}
}

func TestConcurrencyLimitAndCancel(t *testing.T) {
	e := newEnv(t, map[string]any{"maxConcurrent": 3})
	p := &blockingProvider{started: make(chan struct{}, 10), release: make(chan struct{})}
	if err := e.svc.Register(e.h.Root(), p); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make([]capsub.Result, 3)
	for i := range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], _ = e.svc.Spawn(context.Background(), "block", capsub.Request{Parent: "kai", Task: "t", Depth: 1})
		}()
	}
	for range 3 {
		<-p.started
	}
	if _, err := e.svc.Spawn(context.Background(), "block", capsub.Request{Parent: "kai", Task: "t"}); !errors.Is(err, capsub.ErrBusy) {
		t.Fatalf("4th spawn must be refused, got %v", err)
	}
	// another parent is unaffected by kai's limit (and cancellation returns at once)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan capsub.Result, 1)
	go func() {
		r, _ := e.svc.Spawn(ctx, "block", capsub.Request{Parent: "otto", Task: "t"})
		done <- r
	}()
	<-p.started
	cancel()
	select {
	case r := <-done:
		if r.StopReason != capsub.StopCancelled {
			t.Fatalf("%+v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not propagate")
	}
	close(p.release)
	wg.Wait()
	for _, r := range results {
		if r.Output != "done t" || r.StopReason != capsub.StopCompleted || len(r.Diagnostic) > capsub.MaxDiagnostic {
			t.Fatalf("%+v", r.StopReason)
		}
	}
	if e.svc.Active("kai") != 0 {
		t.Fatal("slots not released")
	}
	if _, err := e.svc.Spawn(context.Background(), "block", capsub.Request{Parent: "kai", Task: "t", Depth: 3}); !errors.Is(err, capsub.ErrDepthExceeded) {
		t.Fatalf("depth: %v", err)
	}
}

func TestParentCancelPropagatesToChild(t *testing.T) {
	e := newEnv(t, nil)
	started := make(chan string, 1)
	cancelled := make(chan struct{}, 1)
	e.tools.Register(e.h.Root(), &tools.Tool{Name: "block", Execute: func(ctx context.Context, c tools.Call) (tools.Result, error) {
		started <- c.Agent
		<-ctx.Done()
		cancelled <- struct{}{}
		return tools.Text("stopped"), nil
	}})
	kai, _ := e.agents.Create(agent.Spec{Name: "Kai"})
	ss, _ := kai.Session()
	if _, err := kai.Enqueue(agent.Input{Text: delegateCmd(map[string]any{"task": "/tool block {}"})}); err != nil {
		t.Fatal(err)
	}
	var child string
	select {
	case child = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("child never started")
	}
	if child == kai.ID() || !strings.HasPrefix(child, kai.ID()+"-sub-") {
		t.Fatalf("block ran on %s", child)
	}
	kai.Stop()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("child turn not cancelled")
	}
	agentloop.WaitIdle(kai, 5*time.Second)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := e.agents.Get(child); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("ephemeral child not cleaned up after cancel")
		}
		time.Sleep(10 * time.Millisecond)
	}
	te, _ := ss.Last(session.TypeTurnEnd)
	var end agentloop.TurnEnd
	_ = te.Decode(&end)
	if end.Status != "cancelled" {
		t.Fatalf("parent turn %+v", end)
	}
}

func TestForkProvider(t *testing.T) {
	e := newEnv(t, nil)
	kai, _ := e.agents.Create(agent.Spec{Name: "Kai"})
	ss, _ := kai.Session()
	run(t, kai, "remember the codeword pineapple")
	got := run(t, kai, delegateCmd(map[string]any{"task": "what was the codeword?", "provider": "fork", "mode": "read-only"}))
	if got != "Tool delegate returned: default: what was the codeword?" {
		t.Fatalf("%q", got)
	}
	sp := spawned(t, ss)
	if len(sp) != 1 || sp[0].Provider != "fork" {
		t.Fatalf("%+v", sp)
	}
	css, _ := e.agents.Sessions().Get(sp[0].ChildSession)
	if css.Meta().Parent != ss.ID() {
		t.Fatal("fork lineage")
	}
	// The fork carries the parent's context up to the last turn boundary,
	// and the child's request saw it.
	var sawContext bool
	for _, r := range e.mock.Requests() {
		if !strings.Contains(r.System, "-sub-") {
			continue
		}
		for _, m := range r.Messages {
			if strings.Contains(m.Text(), "pineapple") {
				sawContext = true
			}
		}
	}
	if !sawContext {
		t.Fatal("forked child did not get the parent's context")
	}
}

func TestEphemeralChildSpec(t *testing.T) {
	e := newEnv(t, nil)
	kai, _ := e.agents.Create(agent.Spec{Name: "Kai"})
	var spec agent.Spec
	e.mock.Script = func(req llm.Request) (llm.Response, error) {
		if a, ok := e.agents.Get(req.Agent); ok && req.Agent != kai.ID() {
			spec = a.Spec()
		}
		return llm.Response{StopReason: "end", Message: llm.TextMessage(llm.RoleAssistant, "ok")}, nil
	}
	res, err := e.svc.Spawn(context.Background(), "local", capsub.Request{Parent: kai.ID(), Task: "hi"})
	if err != nil || res.StopReason != capsub.StopCompleted {
		t.Fatal(res, err)
	}
	if !spec.Ephemeral || spec.Parent != kai.ID() || strings.HasPrefix(spec.Role, "ephemeral") {
		t.Fatalf("child spec %+v", spec)
	}
}

// A background delegation returns at once; the results of parallel tasks
// come back to the parent's conversation as messages, and the parent knows
// its teammates.
func TestBackgroundDelegation(t *testing.T) {
	e := newEnv(t, nil)
	lead, _ := e.agents.Create(agent.Spec{Name: "Lead", Role: "receives requests and hands them out"})
	otto, _ := e.agents.Create(agent.Spec{Name: "Otto", Role: "pricing research"})
	ss, _ := lead.Session()
	if team := subagent.Team(e.agents, lead.ID()); !strings.Contains(team, "Otto (id "+otto.ID()+"): pricing research") || strings.Contains(team, "Lead") {
		t.Fatalf("team: %q", team)
	}
	e.mock.SetDelay(150 * time.Millisecond) // every model call takes a while
	start := time.Now()
	lead.Enqueue(agent.Input{Text: delegateCmd(map[string]any{"task": "check prices", "agent": otto.ID(), "background": true})})
	var toolRes string
	for time.Now().Before(start.Add(5*time.Second)) && toolRes == "" {
		for _, ev := range ss.Events() {
			if ev.Type == session.TypeToolResult {
				var r llm.ToolResult
				ev.Decode(&r)
				toolRes = r.Content
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(toolRes, "running in the background") {
		t.Fatalf("%q", toolRes)
	}
	if d := time.Since(start); d > 900*time.Millisecond {
		t.Fatalf("background delegation blocked the turn: %v", d)
	}
	// the result arrives as a message and gets its own reply
	deadline := time.Now().Add(5 * time.Second)
	var res string
	for time.Now().Before(deadline) && res == "" {
		for _, ev := range ss.Events() {
			if ev.Type == session.TypeUserMessage {
				var m session.UserMessage
				ev.Decode(&m)
				if m.From == "delegation" {
					res = m.Text
				}
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !strings.Contains(res, "[委派结果] d1") || !strings.Contains(res, "check prices") {
		t.Fatalf("result message: %q", res)
	}
}
