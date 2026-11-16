package session

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/core/llm"
)

// A request stopped before any reply must not ride along with the next
// message as a live request (walkthrough: essay stopped, then "rm").
func TestDeriveMessagesStoppedRequest(t *testing.T) {
	st, _ := NewStore(nil, "")
	ss, _ := st.Create(Meta{Kind: "agent"})
	ss.Append(TypeUserMessage, "", UserMessage{Text: "write an 800-word essay about the sea", To: "kai"})
	ss.Append(TypeTurnStart, "kai", map[string]any{"input": "i1"})
	ss.Append(TypeStepStart, "kai", map[string]any{"step": 1})
	ss.Append(TypeTurnEnd, "kai", map[string]any{"status": "cancelled", "steps": 1})
	ss.Append(TypeUserMessage, "", UserMessage{Text: "delete hello.txt with rm", To: "kai"})
	ss.Append(TypeTurnStart, "kai", map[string]any{"input": "i2"})

	msgs := DeriveMessages(ss.Events(), "kai", nil)
	if len(msgs) != 1 || msgs[0].Role != llm.RoleUser || len(msgs[0].Parts) != 2 {
		t.Fatalf("%+v", msgs)
	}
	if p := msgs[0].Parts[0].Text; !strings.Contains(p, "essay") || !strings.HasSuffix(p, StoppedNote) {
		t.Fatalf("stopped request not marked: %q", p)
	}
	if p := msgs[0].Parts[1].Text; strings.Contains(p, StoppedNote) || !strings.Contains(p, "rm") {
		t.Fatalf("live request: %q", p)
	}
}

// A stopped turn that already produced output keeps its history as is;
// only a message joined after the last reply is marked.
func TestDeriveMessagesStoppedAfterOutput(t *testing.T) {
	st, _ := NewStore(nil, "")
	ss, _ := st.Create(Meta{Kind: "agent"})
	ss.Append(TypeUserMessage, "", UserMessage{Text: "list files", To: "kai"})
	call := llm.ToolCall{ID: "c1", Name: "bash", Args: json.RawMessage(`{"cmd":"ls"}`)}
	ss.Append(TypeAssistantMessage, "kai", AssistantMessage{Message: llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: "tool_call", ToolCall: &call}}}})
	ss.Append(TypeToolResult, "kai", llm.ToolResult{CallID: "c1", Content: "a.txt"})
	ss.Append(TypeTurnEnd, "kai", map[string]any{"status": "cancelled", "steps": 1})
	ss.Append(TypeUserMessage, "", UserMessage{Text: "thanks", To: "kai"})

	msgs := DeriveMessages(ss.Events(), "kai", nil)
	if len(msgs) != 4 || msgs[0].Text() != "list files" || msgs[3].Text() != "thanks" {
		t.Fatalf("%+v", msgs)
	}

	// joined after the tool step, then stopped: that one is unanswered
	ss.Append(TypeAssistantMessage, "kai", AssistantMessage{Message: llm.TextMessage(llm.RoleAssistant, "ok")})
	ss.Append(TypeTurnEnd, "kai", map[string]any{"status": "completed", "steps": 1})
	ss.Append(TypeUserMessage, "", UserMessage{Text: "now run tests", To: "kai"})
	call2 := llm.ToolCall{ID: "c2", Name: "bash", Args: json.RawMessage(`{"cmd":"go test"}`)}
	ss.Append(TypeAssistantMessage, "kai", AssistantMessage{Message: llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: "tool_call", ToolCall: &call2}}}})
	ss.Append(TypeToolResult, "kai", llm.ToolResult{CallID: "c2", Content: "ok"})
	ss.Append(TypeUserMessage, "", UserMessage{Text: "and push", To: "kai"})
	ss.Append(TypeTurnEnd, "kai", map[string]any{"status": "cancelled", "steps": 2})
	msgs = DeriveMessages(ss.Events(), "kai", nil)
	last := msgs[len(msgs)-1]
	if last.Role != llm.RoleUser || last.Text() != "and push\n"+StoppedNote {
		t.Fatalf("%+v", last)
	}
	for _, m := range msgs[:len(msgs)-1] {
		if strings.Contains(m.Text(), StoppedNote) {
			t.Fatalf("answered message marked: %+v", m)
		}
	}
}
