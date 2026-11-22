package channel_test

import (
	"context"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
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

// 停止 in a channel stops the work done there, not what the same agent is
// doing in another conversation.
func TestStopStopsOnlyThisChannelsWork(t *testing.T) {
	s, agents, h := setupHost(t)
	a, _ := agents.Create(agent.Spec{Name: "Ada"})
	b, _ := agents.Create(agent.Spec{Name: "Bo"})
	one, _ := s.Create("one", []string{a.ID(), b.ID()}, "", "lin")
	two, _ := s.Create("two", []string{a.ID(), b.ID()}, "", "lin")
	// a slow model: Ada is busy in #one when #two is stopped
	for _, ad := range kernel.Use[*llm.Registry](h.Root(), llm.ServiceKey).List() {
		if m, ok := ad.(*mock.Adapter); ok {
			m.SetDelay(1500 * time.Millisecond)
		}
	}
	if _, err := s.Post(one.ID, "work on this", "lin"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for a.CurrentInput() == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := s.Stop(two.ID, "lin"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if a.CurrentInput() == "" {
		t.Fatal("stopping #two stopped Ada's work in #one")
	}
	if err := s.Stop(one.ID, "lin"); err != nil {
		t.Fatal(err)
	}
	if !agentloop.WaitIdle(a, 2*time.Second) {
		t.Fatal("stopping #one did not stop the work in it")
	}
}
