package usage

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
)

func ev(typ, agent string, at time.Time, data any) session.Event {
	b, _ := json.Marshal(data)
	return session.Event{Type: typ, Agent: agent, Time: at, Data: b}
}

func TestWorkKind(t *testing.T) {
	for tool, want := range map[string]string{"git_pr_merge": "code", "edit_file": "code", "web_search": "research", "memory_add": "memory",
		"task_progress": "planning", "read_file": "files", "github__issue": "connectors", "echo": ""} {
		if got := workKind(tool); got != want {
			t.Errorf("workKind(%s) = %q, want %q", tool, got, want)
		}
	}
}

func TestStreaks(t *testing.T) {
	today := time.Date(2026, 11, 26, 15, 0, 0, 0, time.Local)
	days := map[string]bool{}
	for i := 1; i <= 8; i++ { // yesterday and the 7 days before
		days[today.AddDate(0, 0, -i).Format("2006-01-02")] = true
	}
	days["2026-10-01"] = true
	cur, best := streaks(days, today)
	if cur != 8 || best != 8 {
		t.Fatalf("streaks = %d, %d; want 8, 8", cur, best)
	}
}

func TestProfileCounts(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.Local)
	tr := &tracker{now: func() time.Time { return now }, cursor: map[string]int64{}, stats: map[string]*agentStats{}}
	add := func(e session.Event) { tr.count(e) }
	add(ev(session.TypeTurnEnd, "kai", now, agentloop.TurnEnd{Status: "completed"}))
	add(ev(session.TypeTurnEnd, "kai", now, agentloop.TurnEnd{Status: "failed"}))
	add(ev(session.TypeToolResult, "kai", now, llm.ToolResult{Name: "memory_add"}))
	add(ev(session.TypeToolResult, "kai", now, llm.ToolResult{Name: "git_pr_merge"}))
	add(ev(session.TypeToolResult, "kai", now, llm.ToolResult{Name: "web_search", IsError: true}))
	for i := 0; i < 3; i++ {
		add(ev(session.TypeApprovalAsked, "kai", now, map[string]any{"callId": "c"}))
		add(ev(session.TypeApprovalDecided, "kai", now, map[string]any{"outcome": "allowed-once"}))
	}
	add(ev(session.TypeApprovalAsked, "bo", now, map[string]any{}))
	add(ev(session.TypeApprovalDecided, "bo", now, map[string]any{"outcome": "rejected"}))
	p := tr.Profile("kai")
	if p.Turns != 1 || p.Failed != 1 || p.ToolCalls != 3 || p.ToolErrors != 1 || p.Asked != 3 || p.Approved != 3 || p.Rejected != 0 || p.ActiveDays != 1 {
		t.Fatalf("counts: %+v", p)
	}
	earned := map[string]bool{}
	for _, b := range p.Badges {
		earned[b.ID] = b.Earned
	}
	if !earned["first-pr"] || !earned["first-memory"] || !earned["clean-week"] || earned["tools-100"] || earned["streak-7"] {
		t.Fatalf("badges: %+v", p.Badges)
	}
	if p.Recent[27] != 1 {
		t.Fatalf("recent: %v", p.Recent)
	}
	q := tr.Profile("bo")
	if q.Rejected != 1 || q.Badges[3].Earned {
		t.Fatalf("bo: %+v", q)
	}
}
