package audit_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/audit"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/features/rules"
	"github.com/hedeqiang/crewbase/kernel"
)

const desktopTok = "tok-desktop"

func load(t *testing.T, h *kernel.Host, s kernel.Spec, cfg map[string]any) {
	t.Helper()
	p, err := h.Load(s, kernel.LoadOptions{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := h.Plugin(p.ID()); info.State != kernel.StateActive {
		t.Fatalf("%s: %+v", p.ID(), info)
	}
}

func types(recs []audit.Record) []string {
	var out []string
	for _, r := range recs {
		out = append(out, r.Type)
	}
	return out
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met")
}

func TestPluginRecordsSecurityEvents(t *testing.T) {
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	root := t.TempDir()
	load(t, h, session.Plugin, map[string]any{"dir": filepath.Join(root, "sessions")})
	load(t, h, prompt.Plugin, nil)
	load(t, h, tools.Plugin, map[string]any{"requirePolicy": "rules"})
	load(t, h, approval.Plugin, nil)
	load(t, h, approval.DesktopPlugin, nil)
	load(t, h, rules.Plugin, nil)
	load(t, h, llm.Plugin, nil)
	load(t, h, agent.Plugin, map[string]any{"root": root})
	load(t, h, audit.Plugin, map[string]any{"dir": root, "pluginPollSeconds": 1})
	load(t, h, gateway.Plugin, map[string]any{"addr": "127.0.0.1:0", "token": desktopTok})
	load(t, h, audit.APIPlugin, nil)

	r := h.Root()
	svc := kernel.Use[*audit.Service](r, audit.ServiceKey)
	reg := kernel.Use[*tools.Registry](r, tools.ServiceKey)
	agents := kernel.Use[*agent.Registry](r, agent.ServiceKey)
	inbox := kernel.Use[*approval.Inbox](r, approval.InboxKey)
	eng := kernel.Use[*rules.Engine](r, rules.ServiceKey)

	a, err := agents.Create(agent.Spec{Name: "Ada"})
	if err != nil {
		t.Fatal(err)
	}
	ss, _ := a.Session()
	ran := 0
	for _, cat := range []string{tools.CategoryPayment, tools.CategoryPush, tools.CategoryRead} {
		reg.Register(r, &tools.Tool{Name: "do_" + cat, Category: cat, Execute: func(context.Context, tools.Call) (tools.Result, error) {
			ran++
			return tools.Text("SECRET-RESULT"), nil
		}})
	}
	ctx := context.Background()
	// payment → Block
	exe := reg.Run(ctx, r, tools.Call{ID: "c1", Name: "do_payment", Args: json.RawMessage(`{"card":"4111"}`), Agent: a.ID(), Session: ss})
	if !exe.Denied {
		t.Fatal("payment must be denied")
	}
	// push → Ask → rejected
	go func() {
		waitFor(t, func() bool { return len(inbox.Pending()) == 1 })
		inbox.Decide("c2", approval.Decision{Outcome: approval.OutcomeRejected, Message: "no", By: "alice"})
	}()
	exe = reg.Run(ctx, r, tools.Call{ID: "c2", Name: "do_push", Args: json.RawMessage(`{}`), Agent: a.ID(), Session: ss})
	if !exe.Denied || exe.Outcome != approval.OutcomeRejected {
		t.Fatalf("%+v", exe)
	}
	// read → allowed, not audited
	reg.Run(ctx, r, tools.Call{ID: "c3", Name: "do_read", Args: json.RawMessage(`{}`), Agent: a.ID(), Session: ss})
	if ran != 1 {
		t.Fatalf("ran %d", ran)
	}
	// rules change, mode change, goal event, delete agent
	if err := eng.SetAgentRule(a.ID(), tools.CategoryWrite, tools.Ask); err != nil {
		t.Fatal(err)
	}
	ss.Append(session.TypeModeChanged, a.ID(), map[string]any{"from": "sandbox", "to": "read-only"})
	ss.Append("goal/status", a.ID(), map[string]any{"status": "active", "authorized": true, "by": "bob"})
	// plugin lifecycle (polled)
	load(t, h, kernel.Spec{Name: "@test/extra"}, nil)
	waitFor(t, func() bool {
		recs, _ := svc.Log().Query(time.Time{}, time.Time{}, audit.TypePluginLoaded, 0)
		return len(recs) == 1
	})
	h.Unload("@test/extra")
	waitFor(t, func() bool {
		recs, _ := svc.Log().Query(time.Time{}, time.Time{}, audit.TypePluginUnloaded, 0)
		return len(recs) == 1
	})
	if err := agents.Delete(a.ID()); err != nil {
		t.Fatal(err)
	}

	recs, err := svc.Log().Query(time.Time{}, time.Time{}, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(types(recs), ",")
	for _, want := range []string{"audit/started", "agent/created", "rule/decision", "approval/asked", "approval/decided", "rules/changed", "mode/changed", "goal/status", "plugin/loaded", "plugin/unloaded", "agent/deleted"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	var decisions, decided int
	for _, rc := range recs {
		switch rc.Type {
		case audit.TypeRuleDecision:
			decisions++
		case audit.TypeApprovalDecided:
			decided++
			if rc.Actor != "alice" {
				t.Errorf("actor %q", rc.Actor)
			}
		case audit.TypeToolDenied:
			t.Errorf("rule deny recorded twice: %+v", rc)
		case "goal/status":
			if rc.Actor != "bob" {
				t.Errorf("goal actor %q", rc.Actor)
			}
		}
		if strings.Contains(string(rc.Data), "SECRET-RESULT") {
			t.Error("tool results must not be audited")
		}
	}
	if decisions != 2 || decided != 1 {
		t.Fatalf("decisions=%d decided=%d (%s)", decisions, decided, got)
	}
	// audit never enters the session log (and so never model context)
	for _, e := range ss.Events() {
		if strings.HasPrefix(e.Type, "audit/") || e.Type == audit.TypeRuleDecision {
			t.Fatalf("audit leaked into session: %s", e.Type)
		}
	}
	res, err := svc.Verify()
	if err != nil || !res.OK || res.Records != int64(len(recs)) {
		t.Fatalf("%+v %v (records %d)", res, err, len(recs))
	}

	// API: desktop only, export + verify
	g := kernel.Use[*gateway.Gateway](r, gateway.ServiceKey)
	g.AddToken("tok-mobile", "mobile")
	get := func(path, tok string) (int, map[string]any) {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		g.ServeHTTP(w, req)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	if code, _ := get("/api/audit", "tok-mobile"); code != http.StatusForbidden {
		t.Fatalf("mobile export: %d", code)
	}
	code, out := get("/api/audit?type=rule/&from="+time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), desktopTok)
	if code != 200 || len(out["records"].([]any)) != 2 || out["head"] == "" {
		t.Fatalf("%d %v", code, out)
	}
	if code, _ := get("/api/audit?from=yesterday", desktopTok); code != 400 {
		t.Fatalf("bad from: %d", code)
	}
	code, out = get("/api/audit/verify", desktopTok)
	if code != 200 || out["ok"] != true {
		t.Fatalf("%d %v", code, out)
	}

	// tamper → verify reports it
	files, _ := filepath.Glob(filepath.Join(root, "audit", "audit-*.jsonl"))
	b, _ := os.ReadFile(files[len(files)-1])
	os.WriteFile(files[len(files)-1], []byte(strings.Replace(string(b), `"alice"`, `"eve"`, 1)), 0o600)
	_, out = get("/api/audit/verify", desktopTok)
	if out["ok"] != false {
		t.Fatalf("tampering not detected: %v", out)
	}
}

func TestRequiresDir(t *testing.T) {
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	p, _ := h.Load(audit.Plugin, kernel.LoadOptions{})
	if info, _ := h.Plugin(p.ID()); info.State != kernel.StateFailed {
		t.Fatalf("%+v", info)
	}
}
