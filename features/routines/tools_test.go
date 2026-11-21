package routines_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/capabilities/notify"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/features/routines"
	"github.com/hedeqiang/crewbase/features/schedule"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

// TestAgentSetsUpRoutines: the agent creates a routine (the person
// confirms it, seeing what it does), lists it, pauses it (no
// confirmation) and cannot go below an hour or touch another agent's.
func TestAgentSetsUpRoutines(t *testing.T) {
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	root := t.TempDir()
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, nil}, {prompt.Plugin, nil}, {tools.Plugin, nil}, {approval.Plugin, nil}, {approval.DesktopPlugin, nil},
		{llm.Plugin, nil}, {mock.Plugin, nil}, {router.Plugin, nil},
		{agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"mock/default"}}},
		{agentloop.Plugin, nil}, {notify.Plugin, nil}, {schedule.Plugin, nil},
		{routines.Plugin, map[string]any{"dir": root, "brief": "off"}},
	} {
		if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
			t.Fatal(err)
		}
	}
	r := h.Root()
	agents := kernel.Use[*agent.Registry](r, agent.ServiceKey)
	svc := kernel.Use[*routines.Service](r, routines.ServiceKey)
	inbox := kernel.Use[*approval.Inbox](r, approval.InboxKey)
	ad, _ := kernel.Use[*llm.Registry](r, llm.ServiceKey).Get("mock")
	m := ad.(*mock.Adapter)
	kai, _ := agents.Create(agent.Spec{Name: "Kai"})
	mia, _ := agents.Create(agent.Spec{Name: "Mia"})

	sid := func(a *agent.Agent) string {
		ss, err := a.Session()
		if err != nil {
			t.Fatal(err)
		}
		return ss.ID()
	}
	// the agent calls one tool, then reports the result it got
	var results []string
	call := func(a *agent.Agent, name string, args map[string]any) {
		b, _ := json.Marshal(args)
		done := false
		m.Script = func(req llm.Request) (llm.Response, error) {
			last := req.Messages[len(req.Messages)-1]
			if done || last.Role == llm.RoleTool {
				done = true
				for _, p := range last.Parts {
					if p.ToolResult != nil {
						results = append(results, p.ToolResult.Content)
					}
				}
				return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok")}, nil
			}
			return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: "tool_call", ToolCall: &llm.ToolCall{ID: "c" + name, Name: name, Args: b}}}}}, nil
		}
		if _, err := a.Enqueue(agent.Input{Session: sid(a), Text: "go"}); err != nil {
			t.Fatal(err)
		}
	}
	last := func() string {
		if len(results) == 0 {
			return ""
		}
		return results[len(results)-1]
	}
	// waits for the approval card and answers it
	approve := func(allow bool) approval.Request {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if p := inbox.Pending(); len(p) > 0 {
				o := approval.OutcomeAllowedOnce
				if !allow {
					o = approval.OutcomeRejected
				}
				inbox.Decide(p[0].CallID, approval.Decision{Outcome: o})
				return p[0]
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("no approval asked")
		return approval.Request{}
	}

	// create: confirmed, with the schedule and prompt in plain words
	call(kai, routines.ToolCreate, map[string]any{"name": "竞品定价巡检", "prompt": "看三家竞品的定价页", "schedule": "daily 08:30"})
	req := approve(true)
	agentloop.WaitIdle(kai, 3*time.Second)
	if !strings.Contains(req.Summary, "竞品定价巡检") || !strings.Contains(req.Summary, "every day at 08:30") || !strings.Contains(req.Preview, "看三家竞品的定价页") {
		t.Fatalf("card: %q / %q", req.Summary, req.Preview)
	}
	list := svc.List()
	if len(list) != 1 || list[0].Agent != kai.ID() || list[0].By != "agent" || list[0].Origin != sid(kai) || list[0].Mode != agent.ModeReadOnly {
		t.Fatalf("created %+v (result %q)", list, last())
	}
	id := list[0].ID

	// rejected: nothing created
	call(kai, routines.ToolCreate, map[string]any{"name": "x", "prompt": "y", "schedule": "daily 09:00"})
	approve(false)
	agentloop.WaitIdle(kai, 3*time.Second)
	if len(svc.List()) != 1 {
		t.Fatal("a rejected routine was created")
	}

	// too often: refused without asking
	call(kai, routines.ToolCreate, map[string]any{"name": "x", "prompt": "y", "schedule": "every 30m"})
	agentloop.WaitIdle(kai, 3*time.Second)
	if !strings.Contains(last(), "once an hour") || len(svc.List()) != 1 {
		t.Fatalf("every 30m: %q", last())
	}

	// list
	call(kai, routines.ToolList, map[string]any{})
	agentloop.WaitIdle(kai, 3*time.Second)
	if !strings.Contains(last(), id) || !strings.Contains(last(), "daily 08:30") {
		t.Fatalf("list %q", last())
	}

	// pause: no confirmation
	call(kai, routines.ToolUpdate, map[string]any{"id": id, "enabled": false})
	agentloop.WaitIdle(kai, 3*time.Second)
	if got, _ := svc.Get(id); got.Enabled || len(inbox.Pending()) != 0 {
		t.Fatalf("pause: %+v %q", got, last())
	}

	// change the schedule: confirmed
	call(kai, routines.ToolUpdate, map[string]any{"id": id, "schedule": "weekly fri 17:00", "enabled": true})
	req = approve(true)
	agentloop.WaitIdle(kai, 3*time.Second)
	if got, _ := svc.Get(id); got.Schedule != "weekly fri 17:00" || !got.Enabled || got.By != "agent" || !strings.Contains(req.Summary, "every Friday at 17:00") {
		t.Fatalf("update: %+v %q", got, req.Summary)
	}

	// another agent cannot see or touch it
	call(mia, routines.ToolCancel, map[string]any{"id": id})
	agentloop.WaitIdle(mia, 3*time.Second)
	if _, ok := svc.Get(id); !ok {
		t.Fatal("another agent deleted it")
	}

	// cancel
	call(kai, routines.ToolCancel, map[string]any{"id": id})
	agentloop.WaitIdle(kai, 3*time.Second)
	if _, ok := svc.Get(id); ok {
		t.Fatalf("not deleted: %q", last())
	}
}
