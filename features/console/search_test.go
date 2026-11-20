package console

import (
	"testing"

	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
)

func TestSearchSessions(t *testing.T) {
	st, _ := session.NewStore(nil, "")
	a, _ := st.Create(session.Meta{Kind: "agent"})
	b, _ := st.Create(session.Meta{Kind: "agent"})
	a.Append(session.TypeUserMessage, "", session.UserMessage{Text: "整理一下 Q3 季度报告的要点", To: "kai"})
	a.Append(session.TypeAssistantMessage, "kai", session.AssistantMessage{Message: llm.TextMessage(llm.RoleAssistant, "Q3 季度报告：营收增长 12%。")})
	b.Append(session.TypeUserMessage, "", session.UserMessage{Text: "secret q3 plan", To: "mia"})
	all := func(string) bool { return true }
	hits := searchSessions([]*session.Session{a, b}, "q3", all)
	if len(hits) != 3 || hits[0].Session != b.ID() {
		t.Fatalf("%+v", hits)
	}
	if hits[1].Agent != "kai" || hits[1].Snippet == "" {
		t.Fatalf("%+v", hits[1])
	}
	// not visible: not found
	if hits := searchSessions([]*session.Session{a, b}, "q3", func(id string) bool { return id == a.ID() }); len(hits) != 2 {
		t.Fatalf("%+v", hits)
	}
	if len(searchSessions([]*session.Session{a}, "q", all)) != 0 {
		t.Fatal("one letter searched")
	}
	if s := snippetAround("aaaa bbbb cccc dddd eeee ffff gggg", "dddd", 12); s != "…cccc dddd e…" && s == "" {
		t.Fatal(s)
	}
}
