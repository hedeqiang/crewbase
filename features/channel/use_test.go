package channel_test

import (
	"context"
	"testing"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/kernel"
)

// A / pick in a channel reaches the log with the message: for one member
// when its turn starts, for several @mentions when the message is posted.
func TestChannelMessagesCarryTheirPicks(t *testing.T) {
	s, agents, h := setupHost(t)
	a, _ := agents.Create(agent.Spec{Name: "Ada"})
	b, _ := agents.Create(agent.Spec{Name: "Bo"})
	// stands in for the skills plugin: fills in what a pick stands for
	agentloop.ResolveUse.Use(h.Root(), func(ctx context.Context, r *agentloop.UseRequest, next kernel.Next[*agentloop.UseRequest, *agentloop.UseRequest]) (*agentloop.UseRequest, error) {
		for i := range r.Use {
			r.Use[i].Text = "instructions of " + r.Use[i].Name + " for " + r.Agent
		}
		return next(ctx, r)
	})
	ch, err := s.Create("#launch", []string{a.ID(), b.ID()}, "", "lin")
	if err != nil {
		t.Fatal(err)
	}
	use := []session.Invoke{{Kind: session.InvokeSkill, Name: "release-notes"}}

	if _, err := s.PostReply(ch.ID, "draft them", "lin", nil, use...); err != nil {
		t.Fatal(err)
	}
	waitAll(a)
	if _, err := s.PostReply(ch.ID, "@Ada @Bo split it", "lin", nil, session.Invoke{Kind: session.InvokeSkill, Name: "release-notes"}); err != nil {
		t.Fatal(err)
	}
	waitAll(a, b)

	ss, _ := agents.Sessions().Get(ch.ID)
	var got []session.UserMessage
	for _, e := range ss.Events() {
		if e.Type == session.TypeUserMessage {
			var m session.UserMessage
			_ = e.Decode(&m)
			if len(m.Use) > 0 {
				got = append(got, m)
			}
		}
	}
	if len(got) != 2 {
		t.Fatalf("messages with picks: %+v", got)
	}
	if got[0].Use[0].Text != "instructions of release-notes for ada" || got[1].Use[0].Text != "instructions of release-notes for ada" || len(got[1].Also) != 1 {
		t.Fatalf("picks: %+v", got)
	}
}
