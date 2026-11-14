package rules_test

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
	"github.com/hedeqiang/crewbase/features/rules"
	"github.com/hedeqiang/crewbase/kernel"
)

func newEngine(t *testing.T, opts rules.Options) *rules.Engine {
	t.Helper()
	e, err := rules.New(nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestDefaultTable(t *testing.T) {
	e := newEngine(t, rules.Options{})
	want := map[string]tools.Verdict{
		"read": tools.Allow, "exec": tools.Allow, "install": tools.Allow, "write": tools.Allow, "none": tools.Allow,
		"push": tools.Ask, "delete": tools.Ask, "publish": tools.Ask, "payment": rules.Block,
		"mystery": tools.Ask, // unknown categories fail safe
	}
	for cat, v := range want {
		for _, agent := range []string{"", "kai"} {
			if got, reason := e.Evaluate(agent, cat); got != v || reason == "" {
				t.Errorf("%s/%s = %s (%q), want %s", agent, cat, got, reason, v)
			}
		}
	}
}

func TestPaymentNeverChangeable(t *testing.T) {
	if _, err := rules.New(nil, rules.Options{Global: map[string]tools.Verdict{"payment": tools.Allow}}); err == nil {
		t.Fatal("global payment override must fail")
	}
	e := newEngine(t, rules.Options{})
	if err := e.SetAgentRule("kai", "payment", tools.Ask); !errors.Is(err, rules.ErrLoosen) {
		t.Fatal("loosening payment must fail", err)
	}
	if ok, _ := e.Remember("kai", "payment"); ok {
		t.Fatal("payment must never be remembered")
	}
	if e.Rememberable("kai", "payment") {
		t.Fatal("payment not rememberable")
	}
	if v, _ := e.Evaluate("kai", "payment"); v != rules.Block {
		t.Fatal(v)
	}
}

func TestAgentRulesOnlyTighten(t *testing.T) {
	e := newEngine(t, rules.Options{})
	if err := e.SetAgentRule("kai", "exec", tools.Ask); err != nil {
		t.Fatal(err)
	}
	if err := e.SetAgentRule("kai", "write", rules.Block); err != nil {
		t.Fatal(err)
	}
	if err := e.SetAgentRule("kai", "push", rules.Block); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []struct {
		cat string
		v   tools.Verdict
	}{{"push", tools.Allow}, {"delete", tools.Allow}, {"publish", tools.Allow}} {
		if err := e.SetAgentRule("kai", bad.cat, bad.v); !errors.Is(err, rules.ErrLoosen) {
			t.Fatalf("%s→%s must be rejected: %v", bad.cat, bad.v, err)
		}
	}
	check := func(agent, cat string, want tools.Verdict) {
		t.Helper()
		if v, _ := e.Evaluate(agent, cat); v != want {
			t.Fatalf("%s/%s = %s want %s", agent, cat, v, want)
		}
	}
	check("kai", "exec", tools.Ask)
	check("kai", "write", rules.Block)
	check("kai", "push", rules.Block)
	check("nova", "exec", tools.Allow) // other agents unaffected
	check("", "push", tools.Ask)
	// setting back to the global verdict removes the tightening
	if err := e.SetAgentRule("kai", "exec", tools.Allow); err != nil {
		t.Fatal(err)
	}
	check("kai", "exec", tools.Allow)
}

func TestRememberOnlyAskToAllow(t *testing.T) {
	e := newEngine(t, rules.Options{})
	if ok, _ := e.Remember("kai", "push"); !ok {
		t.Fatal("push should be rememberable")
	}
	if v, reason := e.Evaluate("kai", "push"); v != tools.Allow || !strings.Contains(reason, "remembered") {
		t.Fatal(v, reason)
	}
	if v, _ := e.Evaluate("nova", "push"); v != tools.Ask {
		t.Fatal("remember is agent scoped", v)
	}
	// Block cannot be remembered
	e.SetAgentRule("kai", "delete", rules.Block)
	if ok, _ := e.Remember("kai", "delete"); ok {
		t.Fatal("block must not be remembered")
	}
	// Allow needs no remembering
	if ok, _ := e.Remember("kai", "read"); ok {
		t.Fatal("allow should not be stored")
	}
	if got := e.Remembered(); len(got) != 1 || got[0].Agent != "kai" || got[0].Category != "push" {
		t.Fatal(got)
	}
}

func TestRevokeRestoresAsk(t *testing.T) {
	e := newEngine(t, rules.Options{})
	e.Remember("kai", "delete")
	if v, _ := e.Evaluate("kai", "delete"); v != tools.Allow {
		t.Fatal(v)
	}
	if err := e.Revoke("kai", "delete"); err != nil {
		t.Fatal(err)
	}
	if v, _ := e.Evaluate("kai", "delete"); v != tools.Ask {
		t.Fatal(v)
	}
	if len(e.Remembered()) != 0 {
		t.Fatal(e.Remembered())
	}
}

func TestEnforcedLayer(t *testing.T) {
	e := newEngine(t, rules.Options{Enforced: []rules.Enforced{
		{Category: "publish", NoRemember: true},
		{Category: "exec", Verdict: tools.Ask},
		{Category: "push", Verdict: rules.Block},
	}})
	if ok, _ := e.Remember("kai", "publish"); ok || e.Rememberable("kai", "publish") {
		t.Fatal("enforced noRemember")
	}
	if v, _ := e.Evaluate("kai", "exec"); v != tools.Ask {
		t.Fatal("enforced floor", v)
	}
	if ok, _ := e.Remember("kai", "exec"); ok {
		t.Fatal("enforced verdict overrides personal choices")
	}
	if err := e.SetAgentRule("kai", "exec", tools.Allow); !errors.Is(err, rules.ErrLoosen) {
		t.Fatal(err)
	}
	if v, _ := e.Evaluate("kai", "push"); v != rules.Block {
		t.Fatal(v)
	}

	// An enforced layer added later suspends an existing remembered choice.
	dir := t.TempDir()
	e1 := newEngine(t, rules.Options{Dir: dir})
	e1.Remember("kai", "delete")
	e2 := newEngine(t, rules.Options{Dir: dir, Enforced: []rules.Enforced{{Category: "delete", NoRemember: true}}})
	if v, _ := e2.Evaluate("kai", "delete"); v != tools.Ask {
		t.Fatal("enforced must override remembered", v)
	}
	if got := e2.Remembered(); len(got) != 1 || !got[0].Overridden {
		t.Fatal(got)
	}
}

func TestPersistenceReload(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(t, rules.Options{Dir: dir})
	e.SetAgentRule("kai", "exec", rules.Block)
	e.Remember("kai", "push")
	e.Remember("nova", "delete")

	e2 := newEngine(t, rules.Options{Dir: dir})
	if v, _ := e2.Evaluate("kai", "exec"); v != rules.Block {
		t.Fatal(v)
	}
	if v, _ := e2.Evaluate("kai", "push"); v != tools.Allow {
		t.Fatal(v)
	}
	if len(e2.Remembered()) != 2 {
		t.Fatal(e2.Remembered())
	}
	e2.Revoke("kai", "push")
	e3 := newEngine(t, rules.Options{Dir: dir})
	if v, _ := e3.Evaluate("kai", "push"); v != tools.Ask {
		t.Fatal(v)
	}
	// memory-only engines do not touch disk
	m := newEngine(t, rules.Options{})
	m.Remember("kai", "push")
	if v, _ := newEngine(t, rules.Options{}).Evaluate("kai", "push"); v != tools.Ask {
		t.Fatal(v)
	}
}

// ---------------------------------------------------------------- integration

func setup(t *testing.T, cfg map[string]any) (*kernel.Host, *tools.Registry, *session.Session, *approval.Inbox, *rules.Engine) {
	t.Helper()
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	for _, s := range []kernel.Spec{session.Plugin, tools.Plugin, approval.Plugin, approval.DesktopPlugin} {
		if _, err := h.Load(s, kernel.LoadOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.Load(rules.Plugin, kernel.LoadOptions{Config: cfg}); err != nil {
		t.Fatal(err)
	}
	if info, _ := h.Plugin("@crewbase/rules"); info.State != kernel.StateActive {
		t.Fatal(info)
	}
	st := kernel.Use[*session.Store](h.Root(), session.ServiceKey)
	ss, err := st.Create(session.Meta{Kind: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	reg := kernel.Use[*tools.Registry](h.Root(), tools.ServiceKey)
	for _, cat := range []string{"read", "push", "publish", "payment"} {
		cat := cat
		reg.Register(h.Root(), &tools.Tool{Name: "do-" + cat, Category: cat, Execute: func(context.Context, tools.Call) (tools.Result, error) {
			return tools.Text("did " + cat), nil
		}})
	}
	return h, reg, ss,
		kernel.Use[*approval.Inbox](h.Root(), approval.InboxKey),
		kernel.Use[*rules.Engine](h.Root(), rules.ServiceKey)
}

func mkcall(ss *session.Session, agent, name string) tools.Call {
	return tools.Call{ID: session.NewID("call"), Name: name, Args: json.RawMessage(`{}`), Agent: agent, Session: ss, Mode: "host"}
}

func waitPending(t *testing.T, in *approval.Inbox) approval.Request {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p := in.Pending(); len(p) == 1 {
			return p[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no pending approval")
	return approval.Request{}
}

func runAsync(reg *tools.Registry, h *kernel.Host, c tools.Call) chan tools.Execution {
	ch := make(chan tools.Execution, 1)
	go func() { ch <- reg.Run(context.Background(), h.Root(), c) }()
	return ch
}

func TestRememberThroughPipeline(t *testing.T) {
	h, reg, ss, inbox, eng := setup(t, nil)
	changes := 0
	rules.Changed.Listen(h.Root(), func(struct{}) { changes++ })

	// read is allowed without asking, even in host mode
	if exe := reg.Run(context.Background(), h.Root(), mkcall(ss, "kai", "do-read")); exe.Result.Content != "did read" {
		t.Fatal(exe.Result)
	}

	c := mkcall(ss, "kai", "do-push")
	done := runAsync(reg, h, c)
	req := waitPending(t, inbox)
	if !req.Rememberable || req.ConfirmRemember || req.Category != "push" {
		t.Fatalf("%+v", req)
	}
	inbox.Decide(c.ID, approval.Decision{Outcome: approval.OutcomeAllowedOnce, Remember: true})
	if exe := <-done; exe.Result.Content != "did push" {
		t.Fatal(exe.Result)
	}
	if changes != 1 || len(eng.Remembered()) != 1 {
		t.Fatal(changes, eng.Remembered())
	}
	// next call is auto-allowed: no approval card
	exe := reg.Run(context.Background(), h.Root(), mkcall(ss, "kai", "do-push"))
	if exe.Result.Content != "did push" || exe.Outcome != "" {
		t.Fatalf("%+v", exe)
	}
	// another agent still asks
	c = mkcall(ss, "nova", "do-push")
	done = runAsync(reg, h, c)
	waitPending(t, inbox)
	inbox.Decide(c.ID, approval.Decision{Outcome: approval.OutcomeRejected})
	<-done

	// revoke → asks again
	eng.Revoke("kai", "push")
	c = mkcall(ss, "kai", "do-push")
	done = runAsync(reg, h, c)
	waitPending(t, inbox)
	inbox.Decide(c.ID, approval.Decision{Outcome: approval.OutcomeRejected})
	if exe := <-done; !exe.Denied {
		t.Fatal(exe)
	}
}

func TestPaymentBlockedInPipeline(t *testing.T) {
	h, reg, ss, inbox, _ := setup(t, nil)
	exe := reg.Run(context.Background(), h.Root(), mkcall(ss, "kai", "do-payment"))
	if !exe.Denied || !strings.Contains(exe.Result.Content, "payment") || len(inbox.Pending()) != 0 {
		t.Fatal(exe)
	}
	// a downstream hook asking for payment still cannot make it rememberable
	if _, ok := ss.Last(session.TypeApprovalAsked); ok {
		t.Fatal("payment must not reach approval")
	}
}

func TestPublishNeedsConfirm(t *testing.T) {
	h, reg, ss, inbox, eng := setup(t, nil)
	c := mkcall(ss, "kai", "do-publish")
	done := runAsync(reg, h, c)
	req := waitPending(t, inbox)
	if !req.ConfirmRemember || !req.Rememberable {
		t.Fatalf("%+v", req)
	}
	// "always allow" without the second confirmation is not remembered
	inbox.Decide(c.ID, approval.Decision{Outcome: approval.OutcomeAllowedOnce, Remember: true})
	<-done
	if v, _ := eng.Evaluate("kai", "publish"); v != tools.Ask {
		t.Fatal("publish remembered without second confirmation", v)
	}
	c = mkcall(ss, "kai", "do-publish")
	done = runAsync(reg, h, c)
	waitPending(t, inbox)
	inbox.Decide(c.ID, approval.Decision{Outcome: approval.OutcomeAllowedOnce, Remember: true, Confirmed: true})
	<-done
	if v, _ := eng.Evaluate("kai", "publish"); v != tools.Allow {
		t.Fatal(v)
	}

	// a remembered publish emitted without a confirm card is ignored
	tools.OnRemember.Emit(h, tools.Remembered{Agent: "nova", Category: "publish"})
	if v, _ := eng.Evaluate("nova", "publish"); v != tools.Ask {
		t.Fatal("publish remember without confirmation accepted", v)
	}
	// a card that was rejected does not leave a usable confirmation behind
	c = mkcall(ss, "nova", "do-publish")
	done = runAsync(reg, h, c)
	waitPending(t, inbox)
	inbox.Decide(c.ID, approval.Decision{Outcome: approval.OutcomeRejected})
	<-done
	tools.OnRemember.Emit(h, tools.Remembered{Agent: "nova", Category: "publish"})
	if v, _ := eng.Evaluate("nova", "publish"); v != tools.Ask {
		t.Fatal(v)
	}
}

func TestDownstreamCannotLoosen(t *testing.T) {
	h, reg, ss, inbox, _ := setup(t, nil)
	h.Load(kernel.Spec{Name: "loosener", Apply: func(c *kernel.Context) error {
		tools.PreExecute.Use(c, func(ctx context.Context, d *tools.Decision, next kernel.Next[*tools.Decision, *tools.Decision]) (*tools.Decision, error) {
			d.Verdict, d.Rememberable, d.ConfirmRemember = tools.Allow, true, false
			return next(ctx, d)
		})
		return nil
	}}, kernel.LoadOptions{})
	c := mkcall(ss, "kai", "do-publish")
	done := runAsync(reg, h, c)
	req := waitPending(t, inbox)
	if !req.ConfirmRemember {
		t.Fatal("confirm must be re-asserted")
	}
	inbox.Decide(c.ID, approval.Decision{Outcome: approval.OutcomeRejected})
	<-done
}

func TestPluginConfig(t *testing.T) {
	dir := t.TempDir()
	h, reg, ss, inbox, eng := setup(t, map[string]any{
		"dir":      dir,
		"global":   map[string]any{"install": "ask"},
		"agents":   map[string]any{"kai": map[string]any{"read": "block", "push": "allow"}},
		"enforced": []any{map[string]any{"category": "push", "noRemember": true}},
	})
	if v, _ := eng.Evaluate("nova", "install"); v != tools.Ask {
		t.Fatal(v)
	}
	if v, _ := eng.Evaluate("kai", "read"); v != rules.Block {
		t.Fatal(v)
	}
	if v, _ := eng.Evaluate("kai", "push"); v != tools.Ask {
		t.Fatal("loosening config ignored", v)
	}
	exe := reg.Run(context.Background(), h.Root(), mkcall(ss, "kai", "do-read"))
	if !exe.Denied {
		t.Fatal(exe)
	}
	c := mkcall(ss, "kai", "do-push")
	done := runAsync(reg, h, c)
	if req := waitPending(t, inbox); req.Rememberable {
		t.Fatal("enforced noRemember must hide the button")
	}
	inbox.Decide(c.ID, approval.Decision{Outcome: approval.OutcomeAllowedOnce, Remember: true})
	<-done
	if len(eng.Remembered()) != 0 {
		t.Fatal(eng.Remembered())
	}
	if _, err := rules.ParseVerdict("maybe"); err == nil {
		t.Fatal("bad verdict")
	}
}
