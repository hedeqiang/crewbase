package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/core/llm"
)

func TestPersistReloadAndTornLine(t *testing.T) {
	dir := t.TempDir()
	st, _ := NewStore(nil, dir)
	ss, err := st.Create(Meta{ID: "s1", Kind: "agent", Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	ss.Append(TypeUserMessage, "", UserMessage{Text: "hi"})
	st.Close()

	// simulate a crash mid-write
	f, _ := os.OpenFile(filepath.Join(dir, "s1", "session.v1.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"v":1,"seq":3,"ty`)
	f.Close()

	st2, _ := NewStore(nil, dir)
	ss2, err := st2.Get("s1")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(ss2.Events()); n != 2 || ss2.Meta().Title != "t" {
		t.Fatalf("events=%d meta=%+v", n, ss2.Meta())
	}
	if len(st2.List()) != 1 {
		t.Fatal("list")
	}
}

func TestDeriveMessagesInterruptedAndCompaction(t *testing.T) {
	st, _ := NewStore(nil, "")
	ss, _ := st.Create(Meta{Kind: "agent"})
	ss.Append(TypeUserMessage, "", UserMessage{Text: "do it"})
	call := llm.ToolCall{ID: "c1", Name: "bash", Args: json.RawMessage(`{"cmd":"ls"}`)}
	call2 := llm.ToolCall{ID: "c2", Name: "bash", Args: json.RawMessage(`{"cmd":"pwd"}`)}
	ss.Append(TypeAssistantMessage, "kai", AssistantMessage{Message: llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{
		{Type: "text", Text: "running"}, {Type: "tool_call", ToolCall: &call}, {Type: "tool_call", ToolCall: &call2},
	}}})
	ss.Append(TypeToolCall, "kai", ToolCallData{Call: call})
	ss.Append(TypeToolResult, "kai", llm.ToolResult{CallID: "c1", Content: "a.txt"})
	ss.Append(TypeToolCall, "kai", ToolCallData{Call: call2}) // crashed before result

	msgs := DeriveMessages(ss.Events(), "kai", nil)
	if len(msgs) != 4 {
		t.Fatalf("want user, assistant, 2 tool results; got %d: %+v", len(msgs), msgs)
	}
	if msgs[2].Parts[0].ToolResult.Content != "a.txt" || msgs[3].Parts[0].ToolResult.Content != InterruptedResult {
		t.Fatalf("results: %+v %+v", msgs[2], msgs[3])
	}
	if p := PendingCalls(ss.Events(), "kai"); len(p) != 1 || p[0].ID != "c2" {
		t.Fatal(p)
	}

	// another agent's reply is attributed
	ss.Append(TypeAssistantMessage, "nova", AssistantMessage{Message: llm.TextMessage(llm.RoleAssistant, "done")})
	msgs = DeriveMessages(ss.Events(), "kai", map[string]string{"nova": "Nova"})
	if last := msgs[len(msgs)-1]; last.Role != llm.RoleUser || last.Text() != "[Nova]: done" {
		t.Fatalf("%+v", last)
	}

	// compaction replaces everything up to seq
	last := ss.Events()[len(ss.Events())-1]
	ss.Append(TypeCompaction, "kai", CompactionData{Summary: "earlier stuff", UptoSeq: last.Seq})
	ss.Append(TypeUserMessage, "", UserMessage{Text: "next"})
	msgs = DeriveMessages(ss.Events(), "kai", nil)
	if len(msgs) != 1 || msgs[0].Parts[0].Text == "" || msgs[0].Parts[1].Text != "\n\nnext" {
		t.Fatalf("%+v", msgs)
	}
}

func TestFork(t *testing.T) {
	st, _ := NewStore(nil, "")
	ss, _ := st.Create(Meta{Kind: "agent", Agents: []string{"kai"}})
	e := ss.MustAppend(TypeUserMessage, "", UserMessage{Text: "one"})
	ss.MustAppend(TypeUserMessage, "", UserMessage{Text: "two"})
	f, err := st.Fork(ss, e.Seq, Meta{})
	if err != nil {
		t.Fatal(err)
	}
	msgs := DeriveMessages(f.Events(), "kai", nil)
	if len(msgs) != 1 || msgs[0].Text() != "one" || f.Meta().Parent != ss.ID() {
		t.Fatalf("%+v", msgs)
	}
}

func TestIsolatedSessionKinds(t *testing.T) {
	st, _ := NewStore(nil, "")
	ss, _ := st.Create(Meta{Kind: "workflow", Agents: []string{"a", "b"}})
	ss.Append(TypeUserMessage, "", UserMessage{Text: "task for a", To: "a"})
	ss.Append(TypeAssistantMessage, "a", AssistantMessage{Message: llm.TextMessage(llm.RoleAssistant, "a's answer")})
	ss.Append(TypeUserMessage, "", UserMessage{Text: "task for b", To: "b"})
	msgs := DeriveMessages(ss.Events(), "b", nil)
	if len(msgs) != 1 || msgs[0].Text() != "task for b" {
		t.Fatalf("%+v", msgs)
	}
	ch, _ := st.Create(Meta{Kind: "channel", Agents: []string{"a", "b"}})
	ch.Append(TypeUserMessage, "", UserMessage{Text: "task for a", To: "a"})
	ch.Append(TypeAssistantMessage, "a", AssistantMessage{Message: llm.TextMessage(llm.RoleAssistant, "a's answer")})
	if msgs := DeriveMessages(ch.Events(), "b", map[string]string{"a": "A"}); len(msgs) != 1 || !strings.Contains(msgs[0].Text(), "[A]: a's answer") {
		t.Fatalf("channels share context: %+v", msgs)
	}
}
