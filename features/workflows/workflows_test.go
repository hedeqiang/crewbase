package workflows_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/features/schedule"
	"github.com/hedeqiang/crewbase/features/workflows"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

type env struct {
	h       *kernel.Host
	root    string
	agents  *agent.Registry
	svc     *workflows.Service
	sched   *schedule.Service
	inbox   *approval.Inbox
	tools   *tools.Registry
	gw      *gateway.Gateway
	mock    *mock.Adapter
	failWes *atomic.Bool
	calls   map[string]*atomic.Int32
}

var outputs = map[string]string{"pia": "PLAN-1", "ana": "FACTS-A", "ben": "FACTS-B", "wes": "REPORT"}

func agentOf(system string) string {
	_, rest, ok := strings.Cut(system, "(agent id ")
	if !ok {
		return ""
	}
	id, _, _ := strings.Cut(rest, ")")
	return id
}

func newEnv(t *testing.T, root string) *env {
	t.Helper()
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, map[string]any{"dir": root + "/sessions"}}, {prompt.Plugin, nil}, {tools.Plugin, nil},
		{approval.Plugin, nil}, {approval.DesktopPlugin, nil},
		{llm.Plugin, nil}, {mock.Plugin, nil}, {router.Plugin, nil},
		{agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"mock/default"}}},
		{agentloop.Plugin, nil}, {schedule.Plugin, map[string]any{"dir": root}},
		{gateway.Plugin, map[string]any{"addr": "127.0.0.1:0", "token": "tok"}},
		{workflows.Plugin, map[string]any{"dir": root}}, {workflows.APIPlugin, nil},
	} {
		if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
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
	e := &env{h: h, root: root,
		agents:  kernel.Use[*agent.Registry](r, agent.ServiceKey),
		svc:     kernel.Use[*workflows.Service](r, workflows.ServiceKey),
		sched:   kernel.Use[*schedule.Service](r, schedule.ServiceKey),
		inbox:   kernel.Use[*approval.Inbox](r, approval.InboxKey),
		tools:   kernel.Use[*tools.Registry](r, tools.ServiceKey),
		gw:      kernel.Use[*gateway.Gateway](r, gateway.ServiceKey),
		mock:    ad.(*mock.Adapter),
		failWes: &atomic.Bool{},
		calls:   map[string]*atomic.Int32{},
	}
	for _, n := range []string{"Pia", "Ana", "Ben", "Wes", "Kai"} {
		id := strings.ToLower(n)
		e.calls[id] = &atomic.Int32{}
		if _, ok := e.agents.Get(id); !ok {
			if _, err := e.agents.Create(agent.Spec{Name: n}); err != nil {
				t.Fatal(err)
			}
		}
	}
	e.mock.Script = func(req llm.Request) (llm.Response, error) {
		id := agentOf(req.System)
		if c := e.calls[id]; c != nil {
			c.Add(1)
		}
		last := req.Messages[len(req.Messages)-1]
		if id == "kai" { // blocks in a tool until cancelled
			if last.Role == llm.RoleTool {
				return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "unblocked")}, nil
			}
			return llm.Response{StopReason: "tool_use", Message: llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{
				{Type: "tool_call", ToolCall: &llm.ToolCall{ID: "blk", Name: "block", Args: []byte("{}")}}}}}, nil
		}
		if id == "wes" && e.failWes.Load() {
			return llm.Response{}, errors.New("writer exploded")
		}
		return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, outputs[id])}, nil
	}
	return e
}

func pipeline() workflows.Workflow {
	return workflows.Workflow{Name: "Weekly report", Steps: []workflows.Step{
		{ID: "plan", Kind: "agent", Agent: "pia", Prompt: "Plan for: {{input}}"},
		{ID: "gate", Kind: "approval", Approval: &workflows.ApprovalGate{Summary: "Approve plan: {{prev}}"}},
		{ID: "research", Kind: "parallel", Parallel: []workflows.Step{
			{ID: "ra", Agent: "ana", Prompt: "Research A on {{prev}}"},
			{ID: "rb", Agent: "ben", Prompt: "Research B on {{steps.plan}}", Mode: agent.ModeReadOnly},
		}},
		{ID: "write", Kind: "agent", Agent: "wes", Prompt: "Write using:\n{{prev}}\nplan={{steps.plan}} a={{steps.ra}}"},
	}}
}

func (e *env) waitPending(t *testing.T, callID string) approval.Request {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, p := range e.inbox.Pending() {
			if p.CallID == callID {
				return p
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("approval %s never asked; pending %+v", callID, e.inbox.Pending())
	return approval.Request{}
}

func (e *env) wait(t *testing.T, run string) workflows.RunView {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rv, err := e.svc.Wait(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	return rv
}

func userTexts(t *testing.T, e *env, run, to string) []string {
	t.Helper()
	ss, err := e.agents.Sessions().Get(run)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, ev := range ss.Events() {
		if ev.Type == session.TypeUserMessage {
			var m session.UserMessage
			_ = ev.Decode(&m)
			if m.To == to {
				out = append(out, m.Text)
			}
		}
	}
	return out
}

func TestPipelineApproved(t *testing.T) {
	e := newEnv(t, t.TempDir())
	modes := map[string]string{}
	var modeMu sync.Mutex
	agentloop.TurnStarted.Listen(e.h.Root(), func(ev agentloop.TurnStartEvent) {
		modeMu.Lock()
		modes[ev.Agent.ID()] = ev.Input.Mode
		modeMu.Unlock()
	})
	wf, err := e.svc.Create(pipeline())
	if err != nil {
		t.Fatal(err)
	}
	if wf.ID != "weekly-report" || wf.Trigger != workflows.TriggerManual || wf.Job != "" {
		t.Fatalf("%+v", wf)
	}
	if _, err := os.Stat(filepath.Join(e.root, "workflows", wf.ID+".json")); err != nil {
		t.Fatal("definition not persisted:", err)
	}
	rv, err := e.svc.Start(wf.ID, "Q3 churn")
	if err != nil {
		t.Fatal(err)
	}
	if rv.Status != workflows.StatusRunning || rv.Workflow != wf.ID {
		t.Fatalf("%+v", rv)
	}
	ss, _ := e.agents.Sessions().Get(rv.ID)
	if m := ss.Meta(); m.Kind != workflows.SessionKind || strings.Join(m.Agents, ",") != "pia,ana,ben,wes" {
		t.Fatalf("run session meta %+v", m)
	}
	req := e.waitPending(t, rv.ID+"/gate")
	if req.Summary != "Approve plan: PLAN-1" || req.Session != rv.ID {
		t.Fatalf("approval card %+v", req)
	}
	if cur, _ := e.svc.Run(rv.ID); cur.Status != workflows.StatusRunning {
		t.Fatalf("run should wait at the gate: %+v", cur)
	}
	e.inbox.Decide(req.CallID, approval.Decision{Outcome: approval.OutcomeAllowedOnce})
	done := e.wait(t, rv.ID)
	if done.Status != workflows.StatusCompleted || done.Output != "REPORT" {
		t.Fatalf("%+v", done)
	}
	research, _ := done.Step("research")
	if research.Output != "## ra\nFACTS-A\n\n## rb\nFACTS-B" {
		t.Fatalf("joined output %q", research.Output)
	}
	if ra, _ := done.Step("ra"); ra.Group != "research" || ra.Agent != "ana" || ra.Status != workflows.StatusCompleted {
		t.Fatalf("%+v", ra)
	}
	if got := userTexts(t, e, rv.ID, "pia"); len(got) != 1 || got[0] != "Plan for: Q3 churn" {
		t.Fatalf("pia got %q", got)
	}
	if got := userTexts(t, e, rv.ID, "ana"); len(got) != 1 || got[0] != "Research A on PLAN-1" {
		t.Fatalf("ana got %q (gate passes prev through)", got)
	}
	if got := userTexts(t, e, rv.ID, "ben"); len(got) != 1 || got[0] != "Research B on PLAN-1" {
		t.Fatalf("ben got %q", got)
	}
	want := "Write using:\n## ra\nFACTS-A\n\n## rb\nFACTS-B\nplan=PLAN-1 a=FACTS-A"
	if got := userTexts(t, e, rv.ID, "wes"); len(got) != 1 || got[0] != want {
		t.Fatalf("wes got %q", got)
	}
	modeMu.Lock()
	if modes["pia"] != agent.ModeSandbox || modes["ben"] != agent.ModeReadOnly {
		t.Fatalf("modes %v", modes)
	}
	modeMu.Unlock()
	runs, _ := e.svc.Runs(wf.ID)
	if len(runs) != 1 || runs[0].ID != rv.ID {
		t.Fatalf("runs %+v", runs)
	}

	// The run is rebuilt from session events after a restart.
	e.h.Close()
	e2 := newEnv(t, e.root)
	if _, ok := e2.svc.Get(wf.ID); !ok {
		t.Fatal("definition not reloaded")
	}
	runs, err = e2.svc.Runs(wf.ID)
	if err != nil || len(runs) != 1 || runs[0].Status != workflows.StatusCompleted || runs[0].Output != "REPORT" || len(runs[0].Steps) != 6 {
		t.Fatalf("rebuilt %+v %v", runs, err)
	}
}

func TestPipelineRejected(t *testing.T) {
	e := newEnv(t, t.TempDir())
	wf, _ := e.svc.Create(pipeline())
	rv, _ := e.svc.Start(wf.ID, "x")
	req := e.waitPending(t, rv.ID+"/gate")
	e.inbox.Decide(req.CallID, approval.Decision{Outcome: approval.OutcomeRejected, Message: "not now"})
	done := e.wait(t, rv.ID)
	if done.Status != workflows.StatusRejected || !strings.Contains(done.Error, "not now") {
		t.Fatalf("%+v", done)
	}
	if g, _ := done.Step("gate"); g.Status != workflows.StatusRejected {
		t.Fatalf("%+v", g)
	}
	if _, ok := done.Step("research"); ok {
		t.Fatal("steps after a rejected gate must not run")
	}
	if e.calls["ana"].Load() != 0 || e.calls["wes"].Load() != 0 {
		t.Fatal("agents after the gate ran")
	}
}

func TestFailThenRerunFromFailedStep(t *testing.T) {
	e := newEnv(t, t.TempDir())
	wf, _ := e.svc.Create(pipeline())
	e.failWes.Store(true)
	rv, _ := e.svc.Start(wf.ID, "topic")
	e.inbox.Decide(e.waitPending(t, rv.ID+"/gate").CallID, approval.Decision{Outcome: approval.OutcomeAllowedOnce})
	failed := e.wait(t, rv.ID)
	if failed.Status != workflows.StatusFailed || !strings.Contains(failed.Error, "writer exploded") {
		t.Fatalf("%+v", failed)
	}
	if w, _ := failed.Step("write"); w.Status != workflows.StatusFailed {
		t.Fatalf("%+v", w)
	}
	if err := e.svc.Cancel(rv.ID); err == nil {
		t.Fatal("cancelling a finished run must fail")
	}
	piaBefore, anaBefore := e.calls["pia"].Load(), e.calls["ana"].Load()

	e.failWes.Store(false)
	re, err := e.svc.Rerun(rv.ID, "") // defaults to the failed step
	if err != nil {
		t.Fatal(err)
	}
	if re.ID == rv.ID || re.RerunOf != rv.ID || re.FromStep != "write" || re.Trigger != "rerun" {
		t.Fatalf("%+v", re)
	}
	done := e.wait(t, re.ID)
	if done.Status != workflows.StatusCompleted || done.Output != "REPORT" {
		t.Fatalf("%+v", done)
	}
	for _, id := range []string{"plan", "gate", "ra", "rb", "research"} {
		if s, _ := done.Step(id); !s.Reused || s.Status != workflows.StatusCompleted {
			t.Fatalf("step %s not reused: %+v", id, s)
		}
	}
	if e.calls["pia"].Load() != piaBefore || e.calls["ana"].Load() != anaBefore {
		t.Fatal("earlier steps must not run again")
	}
	want := "Write using:\n## ra\nFACTS-A\n\n## rb\nFACTS-B\nplan=PLAN-1 a=FACTS-A"
	if got := userTexts(t, e, re.ID, "wes"); len(got) != 1 || got[0] != want {
		t.Fatalf("rerun prompt %q", got)
	}
	// explicit rerun from an earlier step asks the gate again
	re2, err := e.svc.Rerun(re.ID, "gate")
	if err != nil {
		t.Fatal(err)
	}
	e.inbox.Decide(e.waitPending(t, re2.ID+"/gate").CallID, approval.Decision{Outcome: approval.OutcomeAllowedOnce})
	if d := e.wait(t, re2.ID); d.Status != workflows.StatusCompleted {
		t.Fatalf("%+v", d)
	}
	if _, err := e.svc.Rerun(re.ID, "nope"); err == nil {
		t.Fatal("unknown step")
	}
	if runs, _ := e.svc.Runs(wf.ID); len(runs) != 3 || runs[0].ID != re2.ID {
		t.Fatalf("runs newest first: %d", len(runs))
	}
}

func TestScheduledAndWebhookTriggers(t *testing.T) {
	e := newEnv(t, t.TempDir())
	wf, err := e.svc.Create(workflows.Workflow{Name: "Morning plan", Trigger: "daily 09:00",
		Steps: []workflows.Step{{ID: "p", Agent: "pia", Prompt: "Plan {{input}}"}}})
	if err != nil {
		t.Fatal(err)
	}
	if wf.Job == "" || wf.NextRun.IsZero() || wf.Secret != "" {
		t.Fatalf("%+v", wf)
	}
	j, ok := e.sched.Get(wf.Job)
	if !ok || j.Owner != workflows.Owner {
		t.Fatalf("job %+v", j)
	}
	if err := e.sched.Trigger(context.Background(), wf.Job, "k1", json.RawMessage(`{"input":"today"}`)); err != nil {
		t.Fatal(err)
	}
	runs, _ := e.svc.Runs(wf.ID)
	if len(runs) != 1 || runs[0].Trigger != "schedule" || runs[0].Input != "today" {
		t.Fatalf("%+v", runs)
	}
	done := e.wait(t, runs[0].ID)
	if done.Status != workflows.StatusCompleted || done.Output != "PLAN-1" {
		t.Fatalf("%+v", done)
	}
	if got := userTexts(t, e, done.ID, "pia"); got[0] != "Plan today" {
		t.Fatalf("%q", got)
	}

	// switching to a webhook replaces the job and returns the secret once
	wh := wf.Workflow
	wh.Trigger = workflows.TriggerWebhook
	v, err := e.svc.Update(wf.ID, wh)
	if err != nil {
		t.Fatal(err)
	}
	if v.Secret == "" || v.Hook != "/api/hooks/"+v.Job || !e.sched.CheckSecret(v.Job, v.Secret) {
		t.Fatalf("%+v", v)
	}
	// back to manual removes the job
	wh.Trigger = ""
	if v, _ = e.svc.Update(wf.ID, wh); v.Job != "" || len(e.sched.List(workflows.Owner)) != 0 {
		t.Fatalf("%+v", v)
	}
	if err := e.svc.Delete(wf.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.root, "workflows", wf.ID+".json")); !os.IsNotExist(err) {
		t.Fatal("definition file not removed")
	}
}

func TestCancelRun(t *testing.T) {
	e := newEnv(t, t.TempDir())
	started := make(chan struct{}, 1)
	stopped := make(chan struct{}, 1)
	e.tools.Register(e.h.Root(), &tools.Tool{Name: "block", Execute: func(ctx context.Context, _ tools.Call) (tools.Result, error) {
		started <- struct{}{}
		<-ctx.Done()
		stopped <- struct{}{}
		return tools.Text("stopped"), nil
	}})
	wf, err := e.svc.Create(workflows.Workflow{Name: "Long", Steps: []workflows.Step{
		{ID: "work", Agent: "kai", Prompt: "do it"},
		{ID: "after", Agent: "pia", Prompt: "next"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	rv, _ := e.svc.Start(wf.ID, "")
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("step never started")
	}
	if err := e.svc.Cancel(rv.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("agent turn not stopped")
	}
	done := e.wait(t, rv.ID)
	if done.Status != workflows.StatusCancelled {
		t.Fatalf("%+v", done)
	}
	if w, _ := done.Step("work"); w.Status != workflows.StatusCancelled {
		t.Fatalf("%+v", w)
	}
	if _, ok := done.Step("after"); ok || e.calls["pia"].Load() != 0 {
		t.Fatal("later steps ran after cancel")
	}
	// cancel while waiting at an approval gate
	wf2, _ := e.svc.Create(workflows.Workflow{Name: "Gated", Steps: []workflows.Step{{ID: "g", Kind: "approval"}}})
	rv2, _ := e.svc.Start(wf2.ID, "")
	e.waitPending(t, rv2.ID+"/g")
	_ = e.svc.Cancel(rv2.ID)
	if d := e.wait(t, rv2.ID); d.Status != workflows.StatusCancelled {
		t.Fatalf("%+v", d)
	}
}

func TestValidation(t *testing.T) {
	e := newEnv(t, t.TempDir())
	bad := map[string]workflows.Workflow{
		"no name":     {Steps: []workflows.Step{{ID: "a", Agent: "pia", Prompt: "x"}}},
		"no steps":    {Name: "x"},
		"dup id":      {Name: "x", Steps: []workflows.Step{{ID: "a", Agent: "pia", Prompt: "x"}, {ID: "b", Parallel: []workflows.Step{{ID: "a", Agent: "ana", Prompt: "y"}}}}},
		"agent":       {Name: "x", Steps: []workflows.Step{{ID: "a", Agent: "ghost", Prompt: "x"}}},
		"host":        {Name: "x", Steps: []workflows.Step{{ID: "a", Agent: "pia", Prompt: "x", Mode: agent.ModeHost}}},
		"host sub":    {Name: "x", Steps: []workflows.Step{{ID: "p", Parallel: []workflows.Step{{ID: "a", Agent: "pia", Prompt: "x", Mode: agent.ModeHost}}}}},
		"forward ref": {Name: "x", Steps: []workflows.Step{{ID: "a", Agent: "pia", Prompt: "{{steps.b}}"}, {ID: "b", Agent: "ana", Prompt: "y"}}},
		"self ref":    {Name: "x", Steps: []workflows.Step{{ID: "a", Agent: "pia", Prompt: "{{steps.a}}"}}},
		"sibling ref": {Name: "x", Steps: []workflows.Step{{ID: "p", Parallel: []workflows.Step{{ID: "a", Agent: "pia", Prompt: "x"}, {ID: "b", Agent: "ana", Prompt: "{{steps.a}}"}}}}},
		"unknown ref": {Name: "x", Steps: []workflows.Step{{ID: "a", Agent: "pia", Prompt: "{{steps.zz}}"}}},
		"trigger":     {Name: "x", Trigger: "every 5s", Steps: []workflows.Step{{ID: "a", Agent: "pia", Prompt: "x"}}},
		"kind":        {Name: "x", Steps: []workflows.Step{{ID: "a", Kind: "dance"}}},
		"nested":      {Name: "x", Steps: []workflows.Step{{ID: "p", Parallel: []workflows.Step{{ID: "q", Kind: "approval"}}}}},
	}
	for name, wf := range bad {
		if _, err := e.svc.Create(wf); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
	if len(e.svc.List()) != 0 {
		t.Fatal("invalid workflows were stored")
	}
}

func call(t *testing.T, g *gateway.Gateway, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestAPI(t *testing.T) {
	e := newEnv(t, t.TempDir())
	def := workflows.Workflow{Name: "API flow", Steps: []workflows.Step{{ID: "p", Agent: "pia", Prompt: "go {{input}}"}}}
	if code, _ := call(t, e.gw, "POST", "/api/workflows", workflows.Workflow{Name: "bad"}); code != http.StatusBadRequest {
		t.Fatalf("invalid definition: %d", code)
	}
	code, out := call(t, e.gw, "POST", "/api/workflows", def)
	if code != 200 || out["id"] != "api-flow" {
		t.Fatalf("%d %v", code, out)
	}
	if code, out = call(t, e.gw, "GET", "/api/workflows", nil); code != 200 || len(out["workflows"].([]any)) != 1 {
		t.Fatalf("%d %v", code, out)
	}
	def.Name = "API flow v2"
	if code, out = call(t, e.gw, "PUT", "/api/workflows/api-flow", def); code != 200 || out["name"] != "API flow v2" || out["id"] != "api-flow" {
		t.Fatalf("%d %v", code, out)
	}
	if code, _ = call(t, e.gw, "PUT", "/api/workflows/missing", def); code != 404 {
		t.Fatalf("put missing %d", code)
	}
	code, out = call(t, e.gw, "POST", "/api/workflows/api-flow/run", map[string]string{"input": "now"})
	if code != 200 || out["status"] != "running" {
		t.Fatalf("%d %v", code, out)
	}
	run := out["id"].(string)
	e.wait(t, run)
	if code, out = call(t, e.gw, "GET", "/api/workflow-runs/"+run, nil); code != 200 || out["status"] != "completed" || out["output"] != "PLAN-1" {
		t.Fatalf("%d %v", code, out)
	}
	if code, out = call(t, e.gw, "GET", "/api/workflows/api-flow/runs", nil); code != 200 || len(out["runs"].([]any)) != 1 {
		t.Fatalf("%d %v", code, out)
	}
	if code, out = call(t, e.gw, "POST", "/api/workflow-runs/"+run+"/rerun", map[string]string{"from": "p"}); code != 200 || out["rerunOf"] != run {
		t.Fatalf("%d %v", code, out)
	}
	e.wait(t, out["id"].(string))
	if code, _ = call(t, e.gw, "POST", "/api/workflow-runs/"+run+"/cancel", nil); code != http.StatusConflict {
		t.Fatalf("cancel finished %d", code)
	}
	for _, p := range []string{"/api/workflow-runs/nope", "/api/workflows/nope", "/api/workflows/nope/runs"} {
		if code, _ = call(t, e.gw, "GET", p, nil); code != 404 {
			t.Fatalf("%s: %d", p, code)
		}
	}
	if code, _ = call(t, e.gw, "DELETE", "/api/workflows/api-flow", nil); code != 200 {
		t.Fatalf("delete %d", code)
	}
	if code, _ = call(t, e.gw, "DELETE", "/api/workflows/api-flow", nil); code != 404 {
		t.Fatalf("delete twice %d", code)
	}
}
