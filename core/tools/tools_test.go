package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

// greet-tool and tool-logger: the Phase 0 equivalent of Harness tutorial ch.7.
var greetTool = kernel.Spec{
	Name:   "greet-tool",
	Inject: []string{"tools"},
	Apply: func(ctx *kernel.Context) error {
		reg := kernel.Use[*tools.Registry](ctx, "tools")
		reg.Register(ctx, &tools.Tool{
			Name:        "greet",
			Description: "Greet the named person.",
			Params:      tools.Schema(map[string]any{"name": tools.Prop("string", "who")}, "name"),
			Execute: func(_ context.Context, call tools.Call) (tools.Result, error) {
				return tools.Text("Hello, " + call.String("name") + "!"), nil
			},
		})
		return nil
	},
}

func setup(t *testing.T) (*kernel.Host, *tools.Registry, *session.Session) {
	t.Helper()
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	for _, s := range []kernel.Spec{session.Plugin, tools.Plugin, approval.Plugin, approval.DesktopPlugin} {
		if _, err := h.Load(s, kernel.LoadOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	st := kernel.Use[*session.Store](h.Root(), session.ServiceKey)
	ss, err := st.Create(session.Meta{Kind: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	return h, kernel.Use[*tools.Registry](h.Root(), tools.ServiceKey), ss
}

func call(ss *session.Session, agent, name string, args any) tools.Call {
	b, _ := json.Marshal(args)
	return tools.Call{ID: session.NewID("call"), Name: name, Args: b, Agent: agent, Session: ss}
}

func TestGreetAndLogger(t *testing.T) {
	h, reg, ss := setup(t)
	var logged []string
	h.Load(kernel.Spec{Name: "tool-logger", Inject: []string{"tools"}, Apply: func(ctx *kernel.Context) error {
		tools.OnResult.Listen(ctx, func(e tools.Execution) { logged = append(logged, e.Call.Name+":"+e.Result.Content) })
		return nil
	}}, kernel.LoadOptions{})
	h.Load(greetTool, kernel.LoadOptions{})

	exe := reg.Run(context.Background(), h.Root(), call(ss, "", "greet", map[string]string{"name": "Ada"}))
	if exe.Result.Content != "Hello, Ada!" || len(logged) != 1 {
		t.Fatalf("%+v %v", exe, logged)
	}
	// logged first, then result
	evs := ss.Events()
	if evs[len(evs)-2].Type != session.TypeToolCall || evs[len(evs)-1].Type != session.TypeToolResult {
		t.Fatalf("log order: %v", evs)
	}
	// missing arg
	exe = reg.Run(context.Background(), h.Root(), call(ss, "", "greet", map[string]string{}))
	if !exe.Result.IsError || !strings.Contains(exe.Result.Content, "missing required: name") {
		t.Fatal(exe.Result)
	}
	// unloading the plugin removes the tool
	h.Unload("greet-tool")
	exe = reg.Run(context.Background(), h.Root(), call(ss, "", "greet", map[string]string{"name": "x"}))
	if !strings.Contains(exe.Result.Content, "unknown tool") {
		t.Fatal(exe.Result)
	}
}

func TestShadowingAndRestriction(t *testing.T) {
	h, reg, ss := setup(t)
	h.Load(greetTool, kernel.LoadOptions{})
	agentCtx := h.Root().Fork("kai")
	reg.Register(agentCtx, &tools.Tool{Name: "greet", Execute: func(context.Context, tools.Call) (tools.Result, error) {
		return tools.Text("yo"), nil
	}})
	if r := reg.Run(context.Background(), h.Root(), call(ss, "kai", "greet", map[string]string{"name": "a"})); r.Result.Content != "yo" {
		t.Fatal("agent tool should shadow global", r.Result)
	}
	if r := reg.Run(context.Background(), h.Root(), call(ss, "nova", "greet", map[string]string{"name": "a"})); r.Result.Content != "Hello, a!" {
		t.Fatal("other agents see global", r.Result)
	}
	reg.Restrict(agentCtx, func(name string) bool { return name != "greet" })
	if len(reg.Schemas("kai")) != 0 {
		t.Fatal("restricted tool must not appear in schemas")
	}
	if r := reg.Run(context.Background(), h.Root(), call(ss, "kai", "greet", nil)); !strings.Contains(r.Result.Content, "unknown tool") {
		t.Fatal("restricted tool indistinguishable from absent", r.Result)
	}
	agentCtx.Dispose()
	if len(reg.Schemas("kai")) != 1 {
		t.Fatal("agent registrations removed on dispose")
	}
}

func askAll(h *kernel.Host) {
	h.Load(kernel.Spec{Name: "ask-all", Apply: func(c *kernel.Context) error {
		tools.PreExecute.Use(c, func(ctx context.Context, d *tools.Decision, next kernel.Next[*tools.Decision, *tools.Decision]) (*tools.Decision, error) {
			d.RequireApproval("test rule")
			return next(ctx, d)
		})
		return nil
	}}, kernel.LoadOptions{})
}

func TestApprovalSuspendAndResume(t *testing.T) {
	h, reg, ss := setup(t)
	h.Load(greetTool, kernel.LoadOptions{})
	askAll(h)
	inbox := kernel.Use[*approval.Inbox](h.Root(), approval.InboxKey)

	var remembered []tools.Remembered
	tools.OnRemember.Listen(h.Root(), func(r tools.Remembered) { remembered = append(remembered, r) })

	done := make(chan tools.Execution)
	c := call(ss, "kai", "greet", map[string]string{"name": "Bo"})
	go func() { done <- reg.Run(context.Background(), h.Root(), c) }()
	waitPending(t, inbox, 1)
	if !inbox.Decide(c.ID, approval.Decision{Outcome: approval.OutcomeAllowedOnce, Remember: true}) {
		t.Fatal("decide")
	}
	exe := <-done
	if exe.Result.Content != "Hello, Bo!" || exe.Outcome != approval.OutcomeAllowedOnce || len(remembered) != 1 {
		t.Fatalf("%+v %v", exe, remembered)
	}
	if _, ok := ss.Last(session.TypeApprovalAsked); !ok {
		t.Fatal("approval/asked not logged")
	}

	// rejection
	c = call(ss, "kai", "greet", map[string]string{"name": "Bo"})
	go func() { done <- reg.Run(context.Background(), h.Root(), c) }()
	waitPending(t, inbox, 1)
	inbox.Decide(c.ID, approval.Decision{Outcome: approval.OutcomeRejected, Message: "not now"})
	if exe = <-done; !exe.Denied || !strings.Contains(exe.Result.Content, "not now") {
		t.Fatal(exe.Result)
	}

	// cancellation of the tool call cancels the approval
	ctx, cancel := context.WithCancel(context.Background())
	c = call(ss, "kai", "greet", map[string]string{"name": "Bo"})
	go func() { done <- reg.Run(ctx, h.Root(), c) }()
	waitPending(t, inbox, 1)
	cancel()
	if exe = <-done; exe.Outcome != approval.OutcomeCancelled {
		t.Fatal(exe.Outcome, exe.Result)
	}
}

func TestFailClosed(t *testing.T) {
	h, reg, ss := setup(t)
	h.Load(greetTool, kernel.LoadOptions{})
	askAll(h)

	// no responder → unavailable
	h.Unload("@crewbase/approval-desktop")
	exe := reg.Run(context.Background(), h.Root(), call(ss, "kai", "greet", map[string]string{"name": "x"}))
	if exe.Outcome != approval.OutcomeUnavailable || !exe.Denied {
		t.Fatal(exe)
	}
	h.Load(approval.DesktopPlugin, kernel.LoadOptions{})

	// policy never is enforced before dispatch
	ss.Append(session.TypeApprovalPolicy, "", approval.PolicyData{Policy: approval.PolicyNever})
	exe = reg.Run(context.Background(), h.Root(), call(ss, "kai", "greet", map[string]string{"name": "x"}))
	if exe.Outcome != approval.OutcomeRejected {
		t.Fatal(exe)
	}
	ss.Append(session.TypeApprovalPolicy, "", approval.PolicyData{Policy: approval.PolicyAsk})

	// audit write failure → approval fails
	svc := kernel.Use[*approval.Service](h.Root(), approval.ServiceKey)
	ss.SetFailAppend(errors.New("disk full"))
	d := svc.Ask(context.Background(), ss, approval.Request{CallID: "x"})
	ss.SetFailAppend(nil)
	if d.Outcome != approval.OutcomeUnavailable {
		t.Fatal(d)
	}
}

func TestGuardCannotBeOverridden(t *testing.T) {
	h, reg, ss := setup(t)
	h.Load(greetTool, kernel.LoadOptions{})
	h.Load(kernel.Spec{Name: "guard", Apply: func(c *kernel.Context) error {
		tools.Guard.Listen(c, func(_ context.Context, d *tools.Decision) (string, bool) {
			return "path outside workspace", true
		})
		return nil
	}}, kernel.LoadOptions{})
	exe := reg.Run(context.Background(), h.Root(), call(ss, "", "greet", map[string]string{"name": "x"}))
	if !exe.Denied || !strings.Contains(exe.Result.Content, "outside workspace") {
		t.Fatal(exe.Result)
	}
}

func TestPostExecuteRedaction(t *testing.T) {
	h, reg, ss := setup(t)
	h.Load(greetTool, kernel.LoadOptions{})
	h.Load(kernel.Spec{Name: "redact", Apply: func(c *kernel.Context) error {
		tools.PostExecute.Use(c, func(ctx context.Context, e tools.Execution, next kernel.Next[tools.Execution, tools.Result]) (tools.Result, error) {
			r, err := next(ctx, e)
			r.Content = strings.ReplaceAll(r.Content, "Secret", "[redacted]")
			return r, err
		})
		return nil
	}}, kernel.LoadOptions{})
	reg.Run(context.Background(), h.Root(), call(ss, "", "greet", map[string]string{"name": "Secret"}))
	last, _ := ss.Last(session.TypeToolResult)
	if strings.Contains(string(last.Data), "Secret") {
		t.Fatal("secret reached the log", string(last.Data))
	}
}

func waitPending(t *testing.T, in *approval.Inbox, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(in.Pending()) == n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("pending=%d want %d", len(in.Pending()), n)
}

func TestFailClosedWithoutPolicyAndOnPostExecuteError(t *testing.T) {
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	h.Load(session.Plugin, kernel.LoadOptions{})
	h.Load(tools.Plugin, kernel.LoadOptions{Config: map[string]any{"requirePolicy": "rules"}})
	reg := kernel.Use[*tools.Registry](h.Root(), tools.ServiceKey)
	ss, _ := kernel.Use[*session.Store](h.Root(), session.ServiceKey).Create(session.Meta{})
	reg.Register(h.Root(), &tools.Tool{Name: "rm", Category: tools.CategoryDelete,
		Execute: func(context.Context, tools.Call) (tools.Result, error) { return tools.Text("deleted"), nil }})
	reg.Register(h.Root(), &tools.Tool{Name: "cat", Category: tools.CategoryRead,
		Execute: func(context.Context, tools.Call) (tools.Result, error) { return tools.Text("secret-ish"), nil }})
	if exe := reg.Run(context.Background(), h.Root(), call(ss, "", "rm", nil)); !exe.Denied {
		t.Fatal("must deny when policy engine is missing", exe.Result)
	}
	if exe := reg.Run(context.Background(), h.Root(), call(ss, "", "cat", nil)); exe.Denied {
		t.Fatal("read tools still run", exe.Result)
	}
	h.Load(kernel.Spec{Name: "broken-post", Apply: func(c *kernel.Context) error {
		tools.PostExecute.Use(c, func(context.Context, tools.Execution, kernel.Next[tools.Execution, tools.Result]) (tools.Result, error) {
			return tools.Result{}, errors.New("boom")
		})
		return nil
	}}, kernel.LoadOptions{})
	exe := reg.Run(context.Background(), h.Root(), call(ss, "", "cat", nil))
	if strings.Contains(exe.Result.Content, "secret-ish") {
		t.Fatal("raw result leaked after post-execute failure")
	}
}

func TestSessionKindScopedTool(t *testing.T) {
	h, reg, ss := setup(t) // ss is an "agent" session
	reg.Register(h.Root(), &tools.Tool{Name: "handover", SessionKinds: []string{"channel"},
		Execute: func(context.Context, tools.Call) (tools.Result, error) { return tools.Text("ok"), nil }})
	for _, s := range reg.SchemasFor("", "agent") {
		if s.Name == "handover" {
			t.Fatal("channel-only tool offered in an agent session")
		}
	}
	if len(reg.SchemasFor("", "channel")) != 1 {
		t.Fatal("channel tool missing in channel session")
	}
	if exe := reg.Run(context.Background(), h.Root(), call(ss, "", "handover", nil)); !strings.Contains(exe.Result.Content, "unknown tool") {
		t.Fatal(exe.Result)
	}
}
