package nightshift

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/capabilities/notify"
	"github.com/hedeqiang/crewbase/capabilities/vcs"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/git"
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/features/routines"
	"github.com/hedeqiang/crewbase/features/rules"
	"github.com/hedeqiang/crewbase/features/schedule"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

// fakeGitHub is the GitHub side of the shift: a pull request whose checks
// fail and an issue opened tonight.
type fakeGitHub struct {
	mu    sync.Mutex
	looks int
}

func (f *fakeGitHub) Origin(context.Context, string) *git.Origin {
	return &git.Origin{Host: "github.com", Repo: "acme/api", GitHub: true, GH: "ok"}
}

func (f *fakeGitHub) ListPRs(context.Context, string, string) ([]git.PRSummary, error) {
	f.mu.Lock()
	f.looks++
	f.mu.Unlock()
	return []git.PRSummary{
		{Number: 12, Title: "Faster cache", URL: "https://github.com/acme/api/pull/12", Author: "ann", State: "open", Head: "cache", Base: "main", HeadSHA: "abc1234def"},
		{Number: 13, Title: "From a fork", Fork: true, HeadSHA: "fff"},
	}, nil
}

func (f *fakeGitHub) PRChecks(_ context.Context, _, sha string) (git.Checks, error) {
	return git.Checks{State: "failure", Runs: []git.CheckResult{{Name: "test", Status: "completed", Conclusion: "failure", Annotations: []string{"cache_test.go:41: want 2, got 3"}}}}, nil
}

func (f *fakeGitHub) RecentActivity(_ context.Context, _ string, since time.Time) (git.Activity, error) {
	return git.Activity{Repo: "acme/api", Issues: []git.IssueSummary{{Number: 31, Title: "Crash on empty input", Author: "bob", URL: "https://github.com/acme/api/issues/31", Created: since.Add(time.Minute)}}, Comments: []git.CommentSummary{}}, nil
}

func TestShiftHandlesWaitsAndReports(t *testing.T) {
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	root := t.TempDir()
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, nil}, {prompt.Plugin, nil}, {tools.Plugin, nil}, {approval.Plugin, nil}, {approval.DesktopPlugin, nil},
		{rules.Plugin, nil}, {llm.Plugin, nil}, {mock.Plugin, nil}, {router.Plugin, nil},
		{agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"mock/default"}}},
		{agentloop.Plugin, nil}, {notify.Plugin, nil}, {schedule.Plugin, nil},
		{routines.Plugin, map[string]any{"dir": root, "brief": "off"}},
		{Plugin, map[string]any{"dir": root}},
	} {
		if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
			t.Fatal(x.s.Name, err)
		}
	}
	r := h.Root()
	var mu sync.Mutex
	var sent []notify.Notification
	notify.Sent.Listen(r, func(n notify.Notification) { mu.Lock(); sent = append(sent, n); mu.Unlock() })
	agents := kernel.Use[*agent.Registry](r, agent.ServiceKey)
	s := kernel.Use[*Service](r, ServiceKey)
	rts := kernel.Use[*routines.Service](r, routines.ServiceKey)
	inbox := kernel.Use[*approval.Inbox](r, approval.InboxKey)
	gh := &fakeGitHub{}
	s.git = func() Source { return gh }

	// the agent pushes a fix: that asks the person
	kernel.Use[*tools.Registry](r, tools.ServiceKey).Register(r, &tools.Tool{Name: "git_push", Category: tools.CategoryPush,
		Execute: func(context.Context, tools.Call) (tools.Result, error) { return tools.Text("Pushed."), nil }})
	var asked []string
	ad, _ := kernel.Use[*llm.Registry](r, llm.ServiceKey).Get("mock")
	ad.(*mock.Adapter).Script = func(req llm.Request) (llm.Response, error) {
		last := req.Messages[len(req.Messages)-1]
		if last.Role == llm.RoleTool {
			return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Fixed the cache test on cache; the push waits for your approval.")}, nil
		}
		mu.Lock()
		asked = append(asked, last.Text())
		mu.Unlock()
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: "tool_call", ToolCall: &llm.ToolCall{ID: "p1", Name: "git_push", Args: []byte(`{"branch":"cache"}`)}}}}}, nil
	}

	ada, _ := agents.Create(agent.Spec{Name: "Ada"})
	now := time.Now().UTC()
	cfg := Config{Enabled: true, Agent: ada.ID(), TZ: "UTC", Start: now.Add(-time.Minute).Format("15:04"), End: now.Add(10 * time.Minute).Format("15:04"),
		Watch: Watch{CI: true, GitHub: true}}
	saved, err := s.Save(cfg)
	if err != nil {
		t.Fatal(err)
	}
	rt, ok := rts.Get(saved.Routine)
	if !ok || rt.Mode != agent.ModeSandbox || !rt.Enabled || !strings.Contains(rt.Schedule, "webhook during") {
		t.Fatalf("routine %+v", rt)
	}
	if !strings.Contains(rt.Prompt, "Never try to get around an approval") || strings.Contains(strings.ToLower(rt.Prompt), "bypass") {
		t.Fatalf("prompt: %s", rt.Prompt)
	}
	if st := s.Status(context.Background(), true); !st.OnDuty || st.Source != "poll" || st.Repo != "acme/api" {
		t.Fatalf("status %+v", st)
	}
	// the shift opened and looked at GitHub: one run, the push waits
	deadline := time.Now().Add(5 * time.Second)
	for len(inbox.Pending()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	pend := inbox.Pending()
	if len(pend) != 1 || pend[0].Category != tools.CategoryPush || pend[0].Agent != ada.ID() {
		t.Fatalf("pending %+v", pend)
	}
	mu.Lock()
	first := strings.Join(asked, "\n")
	mu.Unlock()
	if !strings.Contains(first, "pull request #12") || !strings.Contains(first, "issue #31") || strings.Contains(first, "#13") || !strings.Contains(first, "EXTERNAL EVENT DATA") {
		t.Fatalf("handed over: %s", first)
	}
	// a second look hands nothing over again
	s.Poll(context.Background())
	if sh := s.Shift(); sh == nil || len(sh.Events) != 2 || sh.Polls != 2 {
		t.Fatalf("shift %+v", sh)
	}

	// a git change of the agent's is noted
	s.onGit(vcsCommit(ada.ID()))

	// 提前结束: the report lists what came in, the change and what waits
	rep, err := s.End()
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Early || len(rep.Handled) != 1 || len(rep.Handled[0].Events) != 2 || rep.Handled[0].Status != routines.StatusRunning {
		t.Fatalf("handled %+v", rep.Handled)
	}
	if len(rep.Waiting) != 1 || rep.Waiting[0].CallID != pend[0].CallID || rep.Waiting[0].Session != rt.Session {
		t.Fatalf("waiting %+v", rep.Waiting)
	}
	if len(rep.Changes) != 1 || rep.Changes[0].Ref != "1a2b3c4" || rep.Work != rt.Session || rep.Session != ada.Spec().Session {
		t.Fatalf("report %+v", rep)
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	var brief *notify.Notification
	for i := range sent {
		if sent[i].Kind == notify.KindBrief {
			brief = &sent[i]
		}
	}
	mu.Unlock()
	if brief == nil || !strings.Contains(brief.Body, "待你批准 1 项") || brief.Session != ada.Spec().Session {
		t.Fatalf("notification %+v", brief)
	}
	// the rest of the hours stay off: the routine is paused, nothing reopens
	if rt, _ := rts.Get(saved.Routine); rt.Enabled {
		t.Fatal("routine still on after 提前结束")
	}
	s.Ensure(context.Background())
	if s.Shift() != nil {
		t.Fatal("reopened after 提前结束")
	}
	if st := s.Status(context.Background(), false); !st.Ended || st.Waiting != 1 {
		t.Fatalf("status after %+v", st)
	}
	if rs := s.Reports(); len(rs) != 1 || !s.MarkSeen(rs[0].ID) || !s.Reports()[0].Seen {
		t.Fatal("reports")
	}
	// the approval is still the person's to decide
	inbox.Decide(pend[0].CallID, approval.Decision{Outcome: approval.OutcomeRejected})
	agentloop.WaitIdle(ada, 2*time.Second)
}

func vcsCommit(agentID string) vcs.Event {
	return vcs.Event{Type: "git/commit", ID: "1a2b3c4d5e", Actor: "agent:" + agentID, Session: "s1",
		Data: map[string]any{"commit": vcs.Commit{Hash: "1a2b3c4d5e", Short: "1a2b3c4", Subject: "Fix the cache test"}, "branch": "cache"}}
}

func TestDescribeGitHubEvents(t *testing.T) {
	b := json.RawMessage(`{"action":"opened","issue":{"number":31,"title":"Crash","html_url":"https://x/31"},"sender":{"login":"bob"}}`)
	if d := describe("issues", b); d.Kind != "issue" || d.Num != 31 || d.Who != "bob" || d.URL != "https://x/31" {
		t.Fatalf("%+v", d)
	}
	b = json.RawMessage(`{"action":"created","comment":{"body":"rename","path":"a.go","line":4,"html_url":"u"},"pull_request":{"number":7,"title":"T"}}`)
	if d := describe("pull_request_review_comment", b); d.Kind != "comment" || d.Num != 7 || d.Detail != "a.go:4" {
		t.Fatalf("%+v", d)
	}
	b = json.RawMessage(`{"action":"completed","check_run":{"name":"test","conclusion":"failure","head_sha":"abc","pull_requests":[{"number":12}]}}`)
	if d := describe("check_run", b); d.Kind != "ci" || d.Num != 12 || d.sha != "abc" {
		t.Fatalf("%+v", d)
	}
}

func TestHours(t *testing.T) {
	c := Config{Start: "22:00", End: "08:00", TZ: "Asia/Shanghai"}
	at := time.Date(2026, 11, 26, 15, 30, 0, 0, time.UTC) // 23:30 in Shanghai
	if sp, _ := c.spec(); !sp.Within(at) || sp.Within(at.Add(9*time.Hour)) {
		t.Fatal("within")
	}
	if u := c.untilOf(at); !u.Equal(time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("until %s", u)
	}
	if n := c.nextStart(at); !n.Equal(time.Date(2026, 11, 27, 14, 0, 0, 0, time.UTC)) {
		t.Fatalf("next %s", n)
	}
}
