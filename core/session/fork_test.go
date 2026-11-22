package session

import "testing"

func TestForkKeepsWhatTheCompactionKept(t *testing.T) {
	st, _ := NewStore(nil, "")
	src, _ := st.Create(Meta{Kind: "agent", Agents: []string{"a"}})
	src.Append(TypeUserMessage, "", UserMessage{Text: "u1", To: "a"})
	_ = src.UpdateMeta(func(m *Meta) { m.Topic = "t" })
	u2, _ := src.Append(TypeUserMessage, "", UserMessage{Text: "u2", To: "a"})
	src.Append(TypeUserMessage, "", UserMessage{Text: "u3 kept", To: "a"})
	src.Append(TypeCompaction, "a", CompactionData{Summary: "S", UptoSeq: u2.Seq})
	last := src.Events()[len(src.Events())-1].Seq
	t.Logf("src: %+v", DeriveMessages(src.Events(), "a", nil))
	fk, _ := st.Fork(src, last, Meta{Kind: "subagent", Agents: []string{"a"}})
	msgs := DeriveMessages(fk.Events(), "a", nil)
	t.Logf("fork: %+v", msgs)
	found := false
	for _, m := range msgs {
		for _, p := range m.Parts {
			if p.Text == "u3 kept" || len(p.Text) > 0 && forkContains(p.Text, "u3 kept") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("the fork lost a message kept after the compaction cut")
	}
}
func forkContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
