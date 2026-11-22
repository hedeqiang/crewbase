package session

import (
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/core/llm"
)

// The greeting a new agent opens with is for the person: the model needs
// its instruction while it writes it, and neither afterwards.
func TestGreetingLeavesTheHistoryOnceOver(t *testing.T) {
	st, _ := NewStore(nil, t.TempDir())
	ss, _ := st.Create(Meta{ID: "s1", Kind: "agent"})
	ss.Append(TypeInject, "kai", InjectData{Source: greetingTag, Text: "introduce yourself"})
	ss.Append(TypeTurnStart, "kai", map[string]any{"input": "in1", "tag": greetingTag})
	// during the greeting: the instruction is there
	if got := texts(DeriveMessages(ss.Events(), "kai", nil)); !strings.Contains(got, "introduce yourself") {
		t.Fatalf("during the greeting: %s", got)
	}
	ss.Append(TypeAssistantMessage, "kai", AssistantMessage{Message: llm.TextMessage(llm.RoleAssistant, "Hi, I am Kai. > Try: fix my build")})
	ss.Append(TypeTurnEnd, "kai", map[string]any{"status": "completed"})
	ss.Append(TypeUserMessage, "", UserMessage{Text: "help me write tests", To: "kai"})
	ss.Append(TypeTurnStart, "kai", map[string]any{"input": "in2"})

	msgs := DeriveMessages(ss.Events(), "kai", nil)
	got := texts(msgs)
	if strings.Contains(got, "introduce yourself") || strings.Contains(got, "I am Kai") {
		t.Fatalf("the greeting stayed in the history: %s", got)
	}
	if len(msgs) != 1 || msgs[0].Role != llm.RoleUser || !strings.Contains(got, "help me write tests") {
		t.Fatalf("history: %s", got)
	}
}

func texts(msgs []llm.Message) string {
	var b []string
	for _, m := range msgs {
		b = append(b, string(m.Role)+":"+m.Text())
	}
	return strings.Join(b, " | ")
}
