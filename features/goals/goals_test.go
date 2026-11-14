package goals_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
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
	"github.com/hedeqiang/crewbase/features/goals"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

const desktopTok = "tok-desktop"

type env struct {
	h      *kernel.Host
	agents *agent.Registry
	goals  *goals.Service
	mock   *mock.Adapter
	gw     *gateway.Gateway
	tools  *tools.Registry
	inbox  *approval.Inbox
}

func start(t *testing.T, root string) *env {
	t.Helper()
	h := kernel.NewHost(nil)
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, map[string]any{"dir": filepath.Join(root, "sessions")}}, {prompt.Plugin, nil}, {tools.Plugin, nil},
		{approval.Plugin, nil}, {approval.DesktopPlugin, nil}, {llm.Plugin, nil}, {mock.Plugin, nil},
		{agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"mock/default"}}},
		{agentloop.Plugin, nil}, {goals.Plugin, nil},
		{gateway.Plugin, map[string]any{"addr": "127.0.0.1:0", "token": desktopTok}}, {goals.APIPlugin, nil},
	} {
		p, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg})
		if err != nil {
			t.Fatal(err)
		}
		if info, _ := h.Plugin(p.ID()); info.State != kernel.StateActive {
			t.Fatalf("%+v", info)
		}
	}
	r := h.Root()
	ad, _ := kernel.Use[*llm.Registry](r, llm.ServiceKey).Get("mock")
	g := kernel.Use[*gateway.Gateway](r, gateway.ServiceKey)
	g.AddToken("tok-mobile", "mobile")
	return &env{h: h, agents: kernel.Use[*agent.Registry](r, agent.ServiceKey), goals: kernel.Use[*goals.Service](r, goals.ServiceKey),
		mock: ad.(*mock.Adapter), gw: g, tools: kernel.Use[*tools.Registry](r, tools.ServiceKey), inbox: kernel.Use[*approval.Inbox](r, approval.InboxKey)}
}

func (e *env) api(t *testing.T, method, path, tok string, body any) (int, map[string]any) {
	t.Helper()
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	e.gw.ServeHTTP(w, req)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func waitGoal(t *testing.T, e *env, sid string, cond func(*goals.Goal) bool) *goals.Goal {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		g, _ := e.goals.Get(sid)
		if g != nil && cond(g) {
			return g
		}
		time.Sleep(10 * time.Millisecond)
	}
	g, _ := e.goals.Get(sid)
	t.Fatalf("goal condition not met: %+v", g)
	return nil
}

func textResp(s string) (llm.Response, error) {
	return llm.Response{StopReason: "end", Message: llm.TextMessage(llm.RoleAssistant, s)}, nil
}

func callResp(id, name, args string) (llm.Response, error) {
	return llm.Response{StopReason: "tool_use", Message: llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{
		{Type: "tool_call", ToolCall: &llm.ToolCall{ID: id, Name: name, Args: json.RawMessage(args)}},
	}}}, nil
}

func lastUser(req llm.Request) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == llm.RoleUser {
			return req.Messages[i].Text()
		}
	}
	return ""
}

func count(ss *session.Session, typ string) int {
	n := 0
	for _, e := range ss.Events() {
		if e.Type == typ {
			n++
		}
	}
	return n
}

func TestContinuesUntilComplete(t *testing.T) {
	e := start(t, t.TempDir())
	t.Cleanup(e.h.Close)
	var n atomic.Int32
	e.mock.Script = func(req llm.Request) (llm.Response, error) {
		switch n.Add(1) {
		case 1, 2:
			return textResp(fmt.Sprintf("progress %d", n.Load()))
		case 3:
			if !strings.Contains(lastUser(req), "Continue working toward the goal: ship the report") {
				return textResp("missing continuation prompt")
			}
			return callResp("g1", "goal_complete", `{"summary":"report shipped"}`)
		}
		return textResp("unexpected")
	}
	a, _ := e.agents.Create(agent.Spec{Name: "Ada"})
	ss, _ := a.Session()

	if code, _ := e.api(t, "PUT", "/api/sessions/"+ss.ID()+"/goal", "tok-mobile", map[string]any{"objective": "x"}); code != 403 {
		t.Fatalf("mobile may not set goals: %d", code)
	}
	code, out := e.api(t, "PUT", "/api/sessions/"+ss.ID()+"/goal", desktopTok, map[string]any{"objective": "ship the report", "maxRounds": 5})
	if code != 200 || out["goal"].(map[string]any)["authorized"] != true {
		t.Fatalf("%d %v", code, out)
	}
	g := waitGoal(t, e, ss.ID(), func(g *goals.Goal) bool { return g.Status == goals.StatusComplete })
	if g.Rounds != 2 || g.Summary != "report shipped" {
		t.Fatalf("%+v", g)
	}
	agentloop.WaitIdle(a, 2*time.Second)
	if c := count(ss, session.TypeInject); c != 2 {
		t.Fatalf("injects %d", c)
	}
	if c := count(ss, session.TypeTurnEnd); c != 1 {
		t.Fatalf("all rounds run in one turn, got %d turns", c)
	}
	// complete goals cannot be resumed
	if code, _ := e.api(t, "POST", "/api/sessions/"+ss.ID()+"/goal/resume", desktopTok, nil); code != 409 {
		t.Fatalf("resume complete: %d", code)
	}
	if code, _ := e.api(t, "GET", "/api/sessions/nope/goal", "tok-mobile", nil); code != 404 {
		t.Fatalf("%d", code)
	}
}

func TestMaxRoundsAndNoProgress(t *testing.T) {
	e := start(t, t.TempDir())
	t.Cleanup(e.h.Close)
	var n atomic.Int32
	same := atomic.Bool{}
	e.mock.Script = func(req llm.Request) (llm.Response, error) {
		if same.Load() {
			return textResp("still thinking")
		}
		return textResp(fmt.Sprintf("reply %d", n.Add(1)))
	}
	a, _ := e.agents.Create(agent.Spec{Name: "Bo"})
	ss, _ := a.Session()
	if _, err := e.goals.Set(ss.ID(), goals.SetOptions{Objective: "loop", MaxRounds: 3, Authorize: true}); err != nil {
		t.Fatal(err)
	}
	g := waitGoal(t, e, ss.ID(), func(g *goals.Goal) bool { return g.Status == goals.StatusBlocked })
	if g.BlockedReason != goals.CodeMaxRounds || g.Rounds != 3 {
		t.Fatalf("%+v", g)
	}
	agentloop.WaitIdle(a, 2*time.Second)

	// no progress: identical replies
	same.Store(true)
	ch, _ := e.agents.Sessions().Create(session.Meta{Kind: "agent", Agents: []string{a.ID()}})
	if _, err := e.goals.Set(ch.ID(), goals.SetOptions{Objective: "stall", MaxRounds: 10, Authorize: true}); err != nil {
		t.Fatal(err)
	}
	g = waitGoal(t, e, ch.ID(), func(g *goals.Goal) bool { return g.Status == goals.StatusBlocked })
	if g.BlockedReason != goals.CodeNoProgress || g.Rounds != 1 {
		t.Fatalf("%+v", g)
	}
	agentloop.WaitIdle(a, 2*time.Second)
}

func TestBlockedToolAndApprovalRejected(t *testing.T) {
	e := start(t, t.TempDir())
	t.Cleanup(e.h.Close)
	r := e.h.Root()
	// every "push" call needs approval
	tools.PreExecute.Use(r, func(ctx context.Context, d *tools.Decision, next kernel.Next[*tools.Decision, *tools.Decision]) (*tools.Decision, error) {
		if d.Category == tools.CategoryPush {
			d.RequireApproval("test")
		}
		return next(ctx, d)
	})
	e.tools.Register(r, &tools.Tool{Name: "push", Category: tools.CategoryPush, Execute: func(context.Context, tools.Call) (tools.Result, error) {
		return tools.Text("pushed"), nil
	}})
	var mode atomic.Value
	mode.Store("blocked")
	e.mock.Script = func(req llm.Request) (llm.Response, error) {
		last := req.Messages[len(req.Messages)-1]
		if last.Role == llm.RoleTool {
			return textResp("ok after tool")
		}
		if mode.Load() == "blocked" {
			return callResp("b1", "goal_blocked", `{"reason":"need the API key","code":"needs_input"}`)
		}
		return callResp("p1", "push", `{}`)
	}
	a, _ := e.agents.Create(agent.Spec{Name: "Cy"})
	ss, _ := a.Session()
	e.goals.Set(ss.ID(), goals.SetOptions{Objective: "deploy", Authorize: true})
	g := waitGoal(t, e, ss.ID(), func(g *goals.Goal) bool { return g.Status == goals.StatusBlocked })
	if g.BlockedReason != goals.CodeNeedsInput || g.BlockedDetail != "need the API key" {
		t.Fatalf("%+v", g)
	}
	agentloop.WaitIdle(a, 2*time.Second)

	mode.Store("push")
	go func() {
		for i := 0; i < 500 && len(e.inbox.Pending()) == 0; i++ {
			time.Sleep(10 * time.Millisecond)
		}
		e.inbox.Decide("p1", approval.Decision{Outcome: approval.OutcomeRejected, Message: "not today"})
	}()
	if code, _ := e.api(t, "POST", "/api/sessions/"+ss.ID()+"/goal/resume", desktopTok, nil); code != 200 {
		t.Fatalf("resume %d", code)
	}
	g = waitGoal(t, e, ss.ID(), func(g *goals.Goal) bool {
		return g.Status == goals.StatusBlocked && g.BlockedReason == goals.CodeApprovalRejected
	})
	if !strings.Contains(g.BlockedDetail, "not today") {
		t.Fatalf("%+v", g)
	}
	agentloop.WaitIdle(a, 2*time.Second)
	// pause is allowed from mobile
	if code, out := e.api(t, "POST", "/api/sessions/"+ss.ID()+"/goal/pause", "tok-mobile", nil); code != 200 || out["goal"].(map[string]any)["status"] != "paused" {
		t.Fatalf("%d %v", code, out)
	}
	if code, _ := e.api(t, "POST", "/api/sessions/"+ss.ID()+"/goal/authorize", "tok-mobile", nil); code != 403 {
		t.Fatalf("mobile authorize: %d", code)
	}
}

func TestAuthorizationIsNotReplayed(t *testing.T) {
	root := t.TempDir()
	e := start(t, root)
	var replies atomic.Int32
	e.mock.Script = func(req llm.Request) (llm.Response, error) {
		return textResp(fmt.Sprintf("r%d", replies.Add(1)))
	}
	a, _ := e.agents.Create(agent.Spec{Name: "Di"})
	ss, _ := a.Session()
	a.Pause() // hold the kickoff input in the queue
	if _, err := e.goals.Set(ss.ID(), goals.SetOptions{Objective: "long job", MaxRounds: 20, Authorize: true}); err != nil {
		t.Fatal(err)
	}
	g, _ := e.goals.Get(ss.ID())
	if !g.Authorized || g.Status != goals.StatusActive || len(a.Queue()) != 1 {
		t.Fatalf("%+v queue=%d", g, len(a.Queue()))
	}
	// fork: the copy has the goal but not the authorisation
	// (up to the goal events: forking copies queued inputs too)
	st, _ := ss.Last(goals.TypeStatus)
	fk, err := e.agents.Sessions().Fork(ss, st.Seq, session.Meta{})
	if err != nil {
		t.Fatal(err)
	}
	if fg, _ := e.goals.Get(fk.ID()); fg == nil || fg.Authorized || fg.Objective != "long job" {
		t.Fatalf("fork: %+v", fg)
	}
	sid := ss.ID()
	e.h.Close()

	// restart
	e = start(t, root)
	t.Cleanup(e.h.Close)
	replies.Store(0)
	e.mock.Script = func(req llm.Request) (llm.Response, error) {
		return textResp(fmt.Sprintf("r%d", replies.Add(1)))
	}
	g, _ = e.goals.Get(sid)
	if g == nil || g.Authorized || g.Status != goals.StatusActive {
		t.Fatalf("after restart: %+v", g)
	}
	a, _ = e.agents.Get(a.ID())
	ss, _ = e.agents.Sessions().Get(sid)
	a.Resume() // the restored kickoff input runs, but nothing continues
	time.Sleep(50 * time.Millisecond)
	agentloop.WaitIdle(a, 3*time.Second)
	if replies.Load() != 1 || count(ss, session.TypeInject) != 0 {
		t.Fatalf("unauthorised goal continued: replies=%d injects=%d", replies.Load(), count(ss, session.TypeInject))
	}
	// a person re-authorises → continuation resumes (until max rounds)
	if code, out := e.api(t, "POST", "/api/sessions/"+sid+"/goal/authorize", desktopTok, nil); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	g = waitGoal(t, e, sid, func(g *goals.Goal) bool { return g.Status == goals.StatusBlocked })
	if g.BlockedReason != goals.CodeMaxRounds || g.Rounds != 20 {
		t.Fatalf("%+v", g)
	}
	agentloop.WaitIdle(a, 3*time.Second)
}

func TestValidation(t *testing.T) {
	e := start(t, t.TempDir())
	t.Cleanup(e.h.Close)
	a, _ := e.agents.Create(agent.Spec{Name: "Ed"})
	ss, _ := a.Session()
	if _, err := e.goals.Set(ss.ID(), goals.SetOptions{Objective: " "}); err == nil {
		t.Fatal("empty objective")
	}
	if _, err := e.goals.Set(ss.ID(), goals.SetOptions{Objective: "x", MaxRounds: 1000}); err == nil {
		t.Fatal("too many rounds")
	}
	if _, err := e.goals.Set(ss.ID(), goals.SetOptions{Objective: "x", Agent: "stranger"}); err == nil {
		t.Fatal("non-member agent")
	}
	if _, err := e.goals.Authorize(ss.ID(), "t"); err == nil {
		t.Fatal("authorize without goal")
	}
	g, err := e.goals.Set(ss.ID(), goals.SetOptions{Objective: "x"})
	if err != nil || g.Authorized || g.MaxRounds != 10 {
		t.Fatalf("%+v %v", g, err)
	}
	if code, _ := e.api(t, "DELETE", "/api/sessions/"+ss.ID()+"/goal", desktopTok, nil); code != 200 {
		t.Fatal(code)
	}
	if g, _ := e.goals.Get(ss.ID()); g != nil {
		t.Fatalf("cleared: %+v", g)
	}
}
