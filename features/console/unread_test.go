package console

import (
	"testing"

	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
)

func TestUnread(t *testing.T) {
	st, _ := session.NewStore(nil, t.TempDir())
	ss, _ := st.Create(session.Meta{Kind: "agent"})
	say := func(agent, text string) {
		ss.Append(session.TypeAssistantMessage, agent, session.AssistantMessage{Message: llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: "text", Text: text}}}})
	}
	ss.Append(session.TypeUserMessage, "", session.UserMessage{Text: "hi", To: "kai"})
	ss.Append(session.TypeTurnStart, "kai", map[string]any{})
	say("kai", "hello")
	// a conversation from before read positions: all read
	r := readSeq(ss)
	if r != int64(len(ss.Events())) || unread(ss, r) != 0 {
		t.Fatalf("read %d unread %d", r, unread(ss, r))
	}
	// two turns, the second with narration and an answer: two replies
	ss.Append(session.TypeTurnStart, "kai", map[string]any{})
	say("kai", "one")
	ss.Append(session.TypeTurnStart, "kai", map[string]any{})
	say("kai", "working on it")
	say("kai", "two")
	if n := unread(ss, readSeq(ss)); n != 2 {
		t.Fatalf("unread %d", n)
	}
	if markRead(ss, 999) != int64(len(ss.Events())) || unread(ss, readSeq(ss)) != 0 {
		t.Fatal("not read")
	}
	// never back
	if markRead(ss, 1) != int64(len(ss.Events())) {
		t.Fatal("moved back")
	}
}
