package session

import (
	"strings"
	"testing"
)

func TestPickedSkillReachesTheModelFromTheLog(t *testing.T) {
	st, _ := NewStore(nil, t.TempDir())
	ss, _ := st.Create(Meta{ID: "s1", Kind: "agent"})
	ss.Append(TypeUserMessage, "", UserMessage{Text: "for 0.9.7", To: "kai", Use: []Invoke{
		{Kind: InvokeSkill, Name: "release-notes", Text: "Group changes by area."},
		{Kind: InvokeTools, Name: "github"},
	}})
	msgs := DeriveMessages(ss.Events(), "kai", nil)
	if len(msgs) != 1 {
		t.Fatalf("messages: %+v", msgs)
	}
	got := msgs[0].Text()
	for _, want := range []string{`<skill name="release-notes">`, "Group changes by area.", "use the github tools", "for 0.9.7"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Index(got, "for 0.9.7") < strings.Index(got, "</skill>") {
		t.Fatalf("the person's words come after the skill:\n%s", got)
	}
}
