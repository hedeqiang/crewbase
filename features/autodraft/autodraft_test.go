package autodraft

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/features/workflows"
)

func ev(seq int64, typ, agent string, data any) session.Event {
	b, _ := json.Marshal(data)
	return session.Event{Seq: seq, Time: time.Now(), Type: typ, Agent: agent, Data: b}
}

func TestTranscriptScopes(t *testing.T) {
	call := llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: "tool_call", ToolCall: &llm.ToolCall{ID: "c1", Name: "bash", Args: json.RawMessage(`{"command":"ls"}`)}}}}
	events := []session.Event{
		ev(1, session.TypeUserMessage, "", session.UserMessage{Text: "hello"}),
		ev(2, session.TypeAssistantMessage, "a", session.AssistantMessage{Message: llm.TextMessage(llm.RoleAssistant, "hi")}),
		ev(3, session.TypeUserMessage, "", session.UserMessage{Text: "list the files every morning"}),
		ev(4, session.TypeAssistantMessage, "a", session.AssistantMessage{Message: call}),
		ev(5, session.TypeToolResult, "a", llm.ToolResult{CallID: "c1", Name: "bash", Content: "main.go"}),
		ev(6, session.TypeAssistantMessage, "a", session.AssistantMessage{Message: llm.TextMessage(llm.RoleAssistant, "one file")}),
		ev(7, session.TypeUserMessage, "", session.UserMessage{Text: "later"}),
	}
	all, from, upto := transcript(events, "a", 6, ScopeAll)
	if from != 1 || upto != 6 || !strings.Contains(all, "PERSON: hello") || strings.Contains(all, "later") {
		t.Fatalf("all: from %d upto %d\n%s", from, upto, all)
	}
	turn, from, _ := transcript(events, "a", 6, ScopeTurn)
	if from != 3 || strings.Contains(turn, "hello") || !strings.Contains(turn, "TOOL CALL bash") || !strings.Contains(turn, "TOOL RESULT bash: main.go") {
		t.Fatalf("turn: from %d\n%s", from, turn)
	}
}

func TestParseAndBuild(t *testing.T) {
	a, err := parse("Here you go:\n```json\n{\"kind\":\"workflow\",\"both\":true,\"name\":\"周报\",\"task\":\"summarise issues\",\"quiet\":\"no new issues\",\"schedule\":\"Weekly FRI 17:00\",\"steps\":[{\"name\":\"Research\",\"prompt\":\"collect\"},{\"name\":\"Review\",\"prompt\":\"ok?\",\"approval\":true},{\"name\":\"Draft\",\"prompt\":\"write\"}]}\n```")
	if err != nil {
		t.Fatal(err)
	}
	d := build(a, "ag", "s1")
	if d.Kind != KindWorkflow || !d.Both || d.Routine.Schedule != "weekly fri 17:00" || d.ScheduleGuessed {
		t.Fatalf("draft %+v", d)
	}
	if d.Routine.Agent != "ag" || d.Routine.Origin != "s1" || d.Routine.Mode != "read-only" || d.Quiet != "no new issues" {
		t.Fatalf("routine %+v", d.Routine)
	}
	st := d.Workflow.Steps
	if len(st) != 3 || st[0].ID != "research" || !strings.Contains(st[0].Prompt, "{{input}}") || st[1].Kind != workflows.KindApproval || !strings.Contains(st[2].Prompt, "{{prev}}") || st[2].Mode != "sandbox" {
		t.Fatalf("steps %+v", st)
	}

	// no time named, an unusable schedule: the default, flagged
	a, err = parse(`{"kind":"routine","name":"CI","task":"check CI","schedule":"at 2026-01-01 09:00"}`)
	if err != nil {
		t.Fatal(err)
	}
	d = build(a, "ag", "s1")
	if d.Routine.Schedule != DefaultSchedule || !d.ScheduleGuessed || len(d.Workflow.Steps) != 1 || d.Workflow.Steps[0].Agent != "ag" {
		t.Fatalf("draft %+v", d)
	}
	if _, err := parse("no json here"); err == nil {
		t.Fatal("want an error")
	}
}
