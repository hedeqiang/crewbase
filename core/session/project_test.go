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

// Regenerate: the earlier answer is superseded, not a second answer the
// model also sees; the log keeps it.
func TestDeriveMessagesRewindRegenerate(t *testing.T) {
	st, _ := NewStore(nil, "")
	ss, _ := st.Create(Meta{Kind: "agent"})
	ss.Append(TypeUserMessage, "", UserMessage{Text: "hi", To: "kai"})
	ss.Append(TypeTurnStart, "kai", map[string]any{"input": "i1"})
	ss.Append(TypeAssistantMessage, "kai", AssistantMessage{Message: llm.TextMessage(llm.RoleAssistant, "hello")})
	ss.Append(TypeTurnEnd, "kai", map[string]any{"status": "completed", "steps": 1})
	q := ss.MustAppend(TypeUserMessage, "", UserMessage{Text: "a poem", To: "kai"})
	ss.Append(TypeTurnStart, "kai", map[string]any{"input": "i2"})
	call := llm.ToolCall{ID: "c1", Name: "bash", Args: json.RawMessage(`{"cmd":"ls"}`)}
	ss.Append(TypeAssistantMessage, "kai", AssistantMessage{Message: llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: "tool_call", ToolCall: &call}}}})
	ss.Append(TypeToolResult, "kai", llm.ToolResult{CallID: "c1", Content: "a.txt"})
	ss.Append(TypeAssistantMessage, "kai", AssistantMessage{Message: llm.TextMessage(llm.RoleAssistant, "roses are red")})
	ss.Append(TypeTurnEnd, "kai", map[string]any{"status": "completed", "steps": 2})
	n := len(ss.Events())

	ss.Append(TypeRewind, "kai", RewindData{From: q.Seq, Reason: RewindRegenerate})
	// another agent's view is untouched
	if m := DeriveMessages(ss.Events(), "ada", nil); len(m) != 1 || !strings.Contains(m[0].Text(), "roses") {
		t.Fatalf("other agent: %+v", m)
	}
	msgs := DeriveMessages(ss.Events(), "kai", nil)
	if len(msgs) != 2 || msgs[0].Text() != "hi" || msgs[1].Text() != "hello" {
		t.Fatalf("after rewind: %+v", msgs)
	}
	ss.Append(TypeUserMessage, "", UserMessage{Text: "a poem", To: "kai"})
	ss.Append(TypeAssistantMessage, "kai", AssistantMessage{Message: llm.TextMessage(llm.RoleAssistant, "violets are blue")})
	msgs = DeriveMessages(ss.Events(), "kai", nil)
	if len(msgs) != 4 || msgs[2].Text() != "a poem" || msgs[3].Text() != "violets are blue" {
		t.Fatalf("resent: %+v", msgs)
	}
	for _, m := range msgs {
		if strings.Contains(m.Text(), "roses") || len(m.ToolCalls()) > 0 {
			t.Fatalf("superseded answer still visible: %+v", msgs)
		}
	}
	if len(ss.Events()) != n+3 {
		t.Fatal("the log must keep superseded events")
	}
}

// Edit and resend an earlier message: it and everything after it are gone
// for the model; the compaction summary before it is kept.
func TestDeriveMessagesRewindEdit(t *testing.T) {
	st, _ := NewStore(nil, "")
	ss, _ := st.Create(Meta{Kind: "agent"})
	ss.Append(TypeUserMessage, "", UserMessage{Text: "one", To: "kai"})
	a1 := ss.MustAppend(TypeAssistantMessage, "kai", AssistantMessage{Message: llm.TextMessage(llm.RoleAssistant, "1")})
	ss.Append(TypeCompaction, "kai", CompactionData{Summary: "counted to one", UptoSeq: a1.Seq})
	two := ss.MustAppend(TypeUserMessage, "", UserMessage{Text: "two", To: "kai"})
	ss.Append(TypeAssistantMessage, "kai", AssistantMessage{Message: llm.TextMessage(llm.RoleAssistant, "2")})
	ss.Append(TypeUserMessage, "", UserMessage{Text: "three", To: "kai"})
	ss.Append(TypeAssistantMessage, "kai", AssistantMessage{Message: llm.TextMessage(llm.RoleAssistant, "3")})
	if got := CompactedUpto(ss.Events(), "kai"); got != a1.Seq {
		t.Fatalf("CompactedUpto = %d", got)
	}

	rw := ss.MustAppend(TypeRewind, "kai", RewindData{From: two.Seq, Reason: RewindEdit})
	ss.Append(TypeUserMessage, "", UserMessage{Text: "TWO", To: "kai"})
	ss.Append(TypeAssistantMessage, "kai", AssistantMessage{Message: llm.TextMessage(llm.RoleAssistant, "II")})
	msgs := DeriveMessages(ss.Events(), "kai", nil)
	// the summary and the edited message are adjacent user text: one message
	if len(msgs) != 2 || !strings.Contains(msgs[0].Text(), "counted to one") || !strings.HasSuffix(msgs[0].Text(), "\n\nTWO") || strings.Contains(msgs[0].Text(), "two") || msgs[1].Text() != "II" {
		t.Fatalf("after edit: %+v", msgs)
	}
	for _, e := range Visible(ss.Events(), "kai") {
		if e.Seq >= two.Seq && e.Seq < rw.Seq {
			t.Fatalf("superseded event visible: %+v", e)
		}
	}
	if len(Visible(ss.Events(), "ada")) != len(ss.Events()) {
		t.Fatal("rewind leaked to another agent")
	}
}

// A compaction recorded inside a rewound range (a threshold compaction
// during the superseded turn) still applies: its summary covers only
// history before the range.
func TestVisibleKeepsCompaction(t *testing.T) {
	st, _ := NewStore(nil, "")
	ss, _ := st.Create(Meta{Kind: "agent"})
	ss.Append(TypeUserMessage, "", UserMessage{Text: "one", To: "kai"})
	a1 := ss.MustAppend(TypeAssistantMessage, "kai", AssistantMessage{Message: llm.TextMessage(llm.RoleAssistant, "1")})
	q := ss.MustAppend(TypeUserMessage, "", UserMessage{Text: "two", To: "kai"})
	ss.Append(TypeCompaction, "kai", CompactionData{Summary: "counted to one", UptoSeq: a1.Seq})
	ss.Append(TypeAssistantMessage, "kai", AssistantMessage{Message: llm.TextMessage(llm.RoleAssistant, "2")})
	ss.Append(TypeRewind, "kai", RewindData{From: q.Seq, Reason: RewindRegenerate})
	msgs := DeriveMessages(ss.Events(), "kai", nil)
	if len(msgs) != 1 || !strings.Contains(msgs[0].Text(), "counted to one") {
		t.Fatalf("%+v", msgs)
	}
}
