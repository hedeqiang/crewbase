package computertools_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/capabilities/computer"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/computertools"
	"github.com/hedeqiang/crewbase/kernel"
)

// native builds a native computer call as an adapter would.
func (e *env) native(a *agent.Agent, c llm.ComputerCall) tools.Call {
	tc := llm.EncodeComputerCall(session.NewID("toolu"), c)
	return tools.Call{ID: tc.ID, Name: tc.Name, Args: tc.Args, Agent: a.ID(), Session: e.ss, Mode: agent.ModeSandbox, Workspace: a.Workspace()}
}

func claude(action, input string) llm.ComputerCall {
	return llm.ComputerCall{Vendor: llm.ComputerAnthropic, Tool: "computer_toolset_20260801", Action: action, Input: json.RawMessage(input)}
}

func TestNativeToolIsNotAFunctionSchema(t *testing.T) {
	e := setup(t)
	e.on(t)
	if _, ok := e.reg.Get(e.a.ID(), computertools.ToolNative); !ok {
		t.Fatal("native tool not registered")
	}
	for _, s := range e.reg.Schemas(e.a.ID()) {
		if s.Name == computertools.ToolNative {
			t.Fatal("native tool offered as a function")
		}
	}
}

func TestNativeNeedsATargetApp(t *testing.T) {
	e := setup(t)
	e.on(t)
	allowNotes(t, e, e.a)
	refused(t, e.run(t, e.native(e.a, claude("screenshot", `{}`))), "computer_open or computer_observe")
	must(t, e.run(t, e.call(e.a, e.ss, computertools.ToolObserve, map[string]any{"app": "备忘录"})), "")
	exe := e.run(t, e.native(e.a, claude("screenshot", `{}`)))
	must(t, exe, "Screenshot of 备忘录's window (800×600 points")
	if len(exe.Result.Attachments) != 1 {
		t.Fatalf("no screenshot attached: %+v", exe.Result)
	}
	// coordinates are screenshot pixels ÷ Scale (0.01 in the fake)
	exe = e.run(t, e.native(e.a, claude("left_click", `{"coordinate":[3,4]}`)))
	must(t, exe, "Did click (300, 400) in 备忘录.")
	acts := e.fake.actions()
	if last := acts[len(acts)-1]; last.Kind != computer.Click || last.App != "com.apple.Notes" || last.X != 300 || last.Y != 400 {
		t.Fatalf("acted %+v", last)
	}
	// every step is logged
	logs := actionLogs(e.ss)
	if l := logs[len(logs)-1]; l.Action != "click" || !l.OK || !l.Foreground {
		t.Fatalf("log %+v", l)
	}
	// opening another app retargets the tool
	must(t, e.run(t, e.call(e.a, e.ss, computertools.ToolOpen, map[string]any{"app": "微信"})), "")
	refused(t, e.run(t, e.native(e.a, claude("left_click", `{"coordinate":[3,4]}`))), "screenshot first")
}

func TestNativeBlockedApp(t *testing.T) {
	e := setup(t)
	e.on(t)
	allowNotes(t, e, e.a)
	must(t, e.run(t, e.call(e.a, e.ss, computertools.ToolObserve, map[string]any{"app": "备忘录"})), "")
	if err := e.svc.SetBlocked("com.apple.Notes", true); err != nil {
		t.Fatal(err)
	}
	refused(t, e.run(t, e.native(e.a, claude("screenshot", `{}`))), "备忘录 is blocked")
	// another agent without permission is asked first; the grant for the
	// conversation covers the native tool too
	req, exe := e.ask(t, e.call(e.b, e.ss, computertools.ToolObserve, map[string]any{"app": "Shop"}), allow)
	must(t, exe, "")
	if !strings.Contains(req.Reason, "first time") || !req.Rememberable {
		t.Fatalf("first-use card %+v", req)
	}
	must(t, e.run(t, e.native(e.b, claude("screenshot", `{}`))), "Screenshot of Shop")
	if err := e.svc.SetPermission(e.b.ID(), "com.example.Shop", computertools.PermBlock); err != nil {
		t.Fatal(err)
	}
	refused(t, e.run(t, e.native(e.b, claude("screenshot", `{}`))), "not allowed to operate Shop")
	// read-only agents cannot use it at all
	c := e.native(e.a, claude("screenshot", `{}`))
	c.Mode = agent.ModeReadOnly
	refused(t, e.run(t, c), "read-only")
}

// A coordinate click on 发送 is confirmed every time, like pressing it.
func TestNativeClickOnSendIsConfirmed(t *testing.T) {
	e := setup(t)
	e.on(t)
	allowNotes(t, e, e.a)
	e.fake.mu.Lock()
	o := e.fake.obs["com.tencent.xinWeChat"]
	o.Window.Frame = computer.Rect{W: 800, H: 600}
	o.Root.Children[1].Frame = computer.Rect{X: 600, Y: 500, W: 60, H: 30}
	e.fake.obs["com.tencent.xinWeChat"] = o
	e.fake.mu.Unlock()
	must(t, e.run(t, e.call(e.a, e.ss, computertools.ToolObserve, map[string]any{"app": "微信"})), "")
	must(t, e.run(t, e.native(e.a, claude("screenshot", `{}`))), "")
	n := len(e.fake.actions())

	click := claude("left_click", `{"coordinate":[6.3,5.15]}`) // (630, 515) points: on 发送
	req, exe := e.ask(t, e.native(e.a, click), approval.Decision{Outcome: approval.OutcomeRejected})
	if req.Category != tools.CategoryPublish || req.Rememberable || req.Summary != "在 微信 点击「发送」" {
		t.Fatalf("card %+v", req)
	}
	if !exe.Denied || len(e.fake.actions()) != n {
		t.Fatalf("ran without approval: %+v", exe.Result)
	}
	for i := 0; i < 2; i++ { // "always" is not remembered
		_, exe = e.ask(t, e.native(e.a, click), allowAlways)
		must(t, exe, "Did click (630, 515) in 微信.")
	}
	// Gemini coordinates hit the same button: 0–999 of the window
	gem := llm.ComputerCall{Vendor: llm.ComputerGemini, Action: "click", Input: json.RawMessage(`{"x":790,"y":858,"intent":"send"}`)}
	req, _ = e.ask(t, e.native(e.a, gem), approval.Decision{Outcome: approval.OutcomeRejected})
	if req.Category != tools.CategoryPublish {
		t.Fatalf("gemini card %+v", req)
	}
	// clicking elsewhere does not ask
	must(t, e.run(t, e.native(e.a, claude("left_click", `{"coordinate":[1,1]}`))), "")
}

func TestNativeStop(t *testing.T) {
	e := setup(t)
	e.on(t)
	allowNotes(t, e, e.a)
	must(t, e.run(t, e.call(e.a, e.ss, computertools.ToolObserve, map[string]any{"app": "备忘录"})), "")
	e.svc.Stop(e.a.ID())
	refused(t, e.run(t, e.native(e.a, claude("screenshot", `{}`))), "stopped computer use")
	e.svc.Resume(e.a.ID())
	must(t, e.run(t, e.native(e.a, claude("screenshot", `{}`))), "")

	// stopping while a batch runs: the action is cancelled, the rest skipped
	e.fake.mu.Lock()
	e.fake.hook = func(ctx context.Context, a computer.Action) error {
		if a.Kind == computer.Type {
			e.svc.Stop(e.a.ID())
			return ctx.Err()
		}
		return nil
	}
	e.fake.mu.Unlock()
	exe := e.run(t, e.native(e.a, llm.ComputerCall{Vendor: llm.ComputerOpenAI, Actions: json.RawMessage(`[{"type":"type","text":"x"},{"type":"keypress","keys":["ENTER"]}]`)}))
	refused(t, exe, "stopped computer use")
	if acts := e.fake.actions(); acts[len(acts)-1].Kind != computer.Type {
		t.Fatalf("ran past the stop: %+v", acts[len(acts)-1])
	}
}

// Anthropic sends one tool_use per action: they run in order and the
// rest of the turn is skipped after a failure.
func TestNativeBatchStopsAtFirstFailure(t *testing.T) {
	e := setup(t)
	e.on(t)
	allowNotes(t, e, e.a)
	must(t, e.run(t, e.call(e.a, e.ss, computertools.ToolObserve, map[string]any{"app": "备忘录"})), "")
	must(t, e.run(t, e.native(e.a, claude("screenshot", `{}`))), "")
	e.fake.mu.Lock()
	e.fake.hook = func(_ context.Context, a computer.Action) error {
		if a.Kind == computer.Type {
			return errors.New("typing failed")
		}
		return nil
	}
	e.fake.mu.Unlock()
	batch := []llm.ComputerCall{claude("left_click", `{"coordinate":[1,1]}`), claude("type", `{"text":"x"}`), claude("key", `{"text":"Return"}`)}
	var got []tools.Execution
	for i, c := range batch {
		c.Batch, c.Index, c.Count = "msg_1", i, len(batch)
		got = append(got, e.run(t, e.native(e.a, c)))
	}
	must(t, got[0], "Did click")
	refused(t, got[1], "typing failed")
	refused(t, got[2], computertools.SkippedMessage)
	for _, a := range e.fake.actions() {
		if a.Kind == computer.Key {
			t.Fatal("ran an action after the failure")
		}
	}
	// a new turn runs again
	c := claude("key", `{"text":"Return"}`)
	c.Batch = "msg_2"
	must(t, e.run(t, e.native(e.a, c)), "Did key return")
}

// The vendors' safety handshakes become a card every time; only an
// approved call carries the acknowledgement, and OpenAI / Gemini always
// get a screenshot back.
func TestNativeSafetyHandshake(t *testing.T) {
	e := setup(t)
	e.on(t)
	allowNotes(t, e, e.a)
	must(t, e.run(t, e.call(e.a, e.ss, computertools.ToolObserve, map[string]any{"app": "备忘录"})), "")
	c := llm.ComputerCall{Vendor: llm.ComputerGemini, Action: "type", Input: json.RawMessage(`{"text":"hi"}`),
		Safety: []llm.SafetyCheck{{Code: "require_confirmation", Message: "Typing into a form"}}}
	req, exe := e.ask(t, e.native(e.a, c), allowAlways)
	if req.Rememberable || !strings.Contains(req.Summary, "Typing into a form") {
		t.Fatalf("card %+v", req)
	}
	ack, _ := llm.Acknowledged(exe.Result.Content)
	if exe.Result.IsError || !ack || len(exe.Result.Attachments) != 1 {
		t.Fatalf("result %+v", exe.Result)
	}
	// asked again: never remembered
	_, exe = e.ask(t, e.native(e.a, c), approval.Decision{Outcome: approval.OutcomeRejected})
	if ack, _ := llm.Acknowledged(exe.Result.Content); ack || !exe.Denied {
		t.Fatalf("rejected call acknowledged: %+v", exe.Result)
	}
	// without a check: no card, no acknowledgement, still a screenshot
	exe = e.run(t, e.native(e.a, llm.ComputerCall{Vendor: llm.ComputerOpenAI, Actions: json.RawMessage(`[{"type":"wait"}]`)}))
	if ack, _ := llm.Acknowledged(exe.Result.Content); ack || len(exe.Result.Attachments) != 1 {
		t.Fatalf("plain call %+v", exe.Result)
	}
}

func TestOfferNative(t *testing.T) {
	e := setup(t)
	run := func(model string) *llm.Request {
		t.Helper()
		req, err := agentloop.Request.Run(kernel.For(e.h.Root(), e.a.ID()), context.Background(),
			agentloop.RequestArgs{Agent: e.a, Session: e.ss, Request: &llm.Request{Model: model, System: "base"}},
			func(_ context.Context, x agentloop.RequestArgs) (*llm.Request, error) { return x.Request, nil })
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	if slices.Contains(run("claude-opus-5-5").NativeTools, llm.NativeComputer) {
		t.Fatal("offered while computer use is off")
	}
	e.on(t)
	req := run("claude-opus-5-5")
	if !slices.Contains(req.NativeTools, llm.NativeComputer) || !strings.Contains(req.System, "computer_open") {
		t.Fatalf("not offered: %+v", req)
	}
	for _, m := range []string{"gpt-6.1-sol", "gemini-4-carbon"} {
		if !slices.Contains(run(m).NativeTools, llm.NativeComputer) {
			t.Fatalf("%s not offered", m)
		}
	}
	if slices.Contains(run("deepseek-chat").NativeTools, llm.NativeComputer) {
		t.Fatal("offered to a model without the tool")
	}
	if err := e.a.Update(func(s *agent.Spec) { s.Mode = agent.ModeReadOnly }); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(run("claude-opus-5-5").NativeTools, llm.NativeComputer) {
		t.Fatal("offered to a read-only agent")
	}
}
