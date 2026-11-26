package session

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/core/llm"
)

func msgJSON(t *testing.T, ms []llm.Message) []string {
	t.Helper()
	var out []string
	for _, m := range ms {
		b, _ := json.Marshal(m)
		out = append(out, string(b))
	}
	return out
}

func reply(text string) AssistantMessage {
	return AssistantMessage{Message: llm.TextMessage(llm.RoleAssistant, text)}
}

// Append-only: after a compaction the kept turns stay as they were and
// the summary follows them, where the compaction happened; what comes
// later follows the summary and does not merge into it.
func TestDeriveAppendOnlyCompaction(t *testing.T) {
	st, _ := NewStore(nil, "")
	ss, _ := st.Create(Meta{Kind: "agent"})
	ss.Append(TypeUserMessage, "", UserMessage{Text: "one"})
	a1, _ := ss.Append(TypeAssistantMessage, "kai", reply("1"))
	ss.Append(TypeUserMessage, "", UserMessage{Text: "two"})
	ss.Append(TypeAssistantMessage, "kai", reply("2"))
	ss.Append(TypeUserMessage, "", UserMessage{Text: "three"})
	before := msgJSON(t, DeriveMessagesWith(ss.Events(), "kai", nil, DeriveOptions{AppendOnly: true}))
	ss.Append(TypeCompaction, "kai", CompactionData{Summary: "counted to one", UptoSeq: a1.Seq})

	opt := DeriveOptions{AppendOnly: true}
	after := msgJSON(t, DeriveMessagesWith(ss.Events(), "kai", nil, opt))
	// the first turn went; the rest is as it was, then the summary
	kept := before[2:]
	if len(after) != len(kept)+1 {
		t.Fatalf("after %v", after)
	}
	for i := range kept {
		if after[i] != kept[i] {
			t.Fatalf("kept item %d changed:\n%s\n%s", i, kept[i], after[i])
		}
	}
	if !strings.Contains(after[len(after)-1], "counted to one") || !strings.Contains(after[len(after)-1], "before the messages above") {
		t.Fatalf("summary %s", after[len(after)-1])
	}
	ss.Append(TypeAssistantMessage, "kai", reply("3"))
	ss.Append(TypeUserMessage, "", UserMessage{Text: "four"})
	later := msgJSON(t, DeriveMessagesWith(ss.Events(), "kai", nil, opt))
	for i := range after {
		if later[i] != after[i] {
			t.Fatalf("item %d changed later:\n%s\n%s", i, after[i], later[i])
		}
	}
	// the default keeps the summary in front
	if m := DeriveMessages(ss.Events(), "kai", nil); !strings.HasPrefix(m[0].Text(), SummaryHeader) {
		t.Fatalf("default %v", m[0])
	}
}

// Append-only: a message that arrived while a step was being answered
// goes after that step's reply instead of merging into the message the
// step's request ended with.
func TestDeriveAppendOnlyLateMessage(t *testing.T) {
	st, _ := NewStore(nil, "")
	ss, _ := st.Create(Meta{Kind: "agent"})
	ss.Append(TypeUserMessage, "", UserMessage{Text: "list files"})
	ss.Append(TypeTurnStart, "kai", map[string]any{"input": "i1"})
	opt := DeriveOptions{AppendOnly: true}
	sent := msgJSON(t, DeriveMessagesWith(ss.Events(), "kai", nil, opt))
	ss.Append(TypeStepStart, "kai", map[string]any{"step": 1})
	ss.Append(TypeUserMessage, "", UserMessage{Text: "and sizes"}) // during the reply
	ss.Append(TypeAssistantMessage, "kai", reply("a.txt"))
	next := msgJSON(t, DeriveMessagesWith(ss.Events(), "kai", nil, opt))
	if len(next) != 3 || next[0] != sent[0] || !strings.Contains(next[1], "a.txt") || !strings.Contains(next[2], "and sizes") {
		t.Fatalf("next %v", next)
	}
	// a step that failed: the late message goes before the next step
	ss.Append(TypeStepStart, "kai", map[string]any{"step": 2})
	ss.Append(TypeUserMessage, "", UserMessage{Text: "hurry"})
	ss.Append(TypeTurnEnd, "kai", map[string]any{"status": "failed"})
	if m := DeriveMessagesWith(ss.Events(), "kai", nil, opt); !strings.Contains(m[len(m)-1].Text(), "hurry") {
		t.Fatalf("after failed step %v", msgJSON(t, m))
	}
}

// Append-only: every tool image after the compaction stays.
func TestDeriveAppendOnlyImages(t *testing.T) {
	st, _ := NewStore(nil, "")
	ss, _ := st.Create(Meta{Kind: "agent"})
	ss.Append(TypeUserMessage, "", UserMessage{Text: "look"})
	for i := 0; i < MaxToolImages+2; i++ {
		id := "c" + string(rune('0'+i))
		ss.Append(TypeAssistantMessage, "kai", AssistantMessage{Message: llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: "tool_call", ToolCall: &llm.ToolCall{ID: id, Name: "screenshot"}}}}})
		ss.Append(TypeToolResult, "kai", llm.ToolResult{CallID: id, Content: "shot", Attachments: []llm.Attachment{{Path: "s.png", Mime: "image/png"}}})
	}
	count := func(ms []llm.Message) int {
		n := 0
		for _, m := range ms {
			for _, p := range m.Parts {
				if p.Type == llm.PartAttachment {
					n++
				}
			}
		}
		return n
	}
	if n := count(DeriveMessages(ss.Events(), "kai", nil)); n != MaxToolImages {
		t.Fatalf("default %d", n)
	}
	if n := count(DeriveMessagesWith(ss.Events(), "kai", nil, DeriveOptions{AppendOnly: true})); n != MaxToolImages+2 {
		t.Fatalf("append-only %d", n)
	}
}
