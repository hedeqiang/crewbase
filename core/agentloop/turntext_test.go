package agentloop_test

import (
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

func TestTurnEndCarriesOnlyItsOwnText(t *testing.T) {
	e := newEnv(t, t.TempDir())
	a, err := e.agents.Create(agent.Spec{Name: "Kai", Role: "engineer"})
	if err != nil {
		t.Fatal(err)
	}
	var got []agentloop.TurnEndEvent
	agentloop.TurnEnded.Listen(e.h.Root(), func(ev agentloop.TurnEndEvent) { got = append(got, ev) })
	send(t, a, "hello")
	if !agentloop.WaitIdle(a, 2*time.Second) {
		t.Fatal("not idle")
	}
	for _, id := range []string{"claude-code", "codex"} {
		ad, _ := e.llms.Get(id)
		ad.(*mock.Adapter).SetDelay(2 * time.Second)
	}
	send(t, a, "second")
	time.Sleep(200 * time.Millisecond)
	a.Stop()
	agentloop.WaitIdle(a, 3*time.Second)
	last := got[len(got)-1]
	t.Logf("second turn: status=%s text=%q", last.Status, last.Text)
	if last.Status == "cancelled" && last.Text != "" {
		t.Fatalf("a stopped turn with no reply reports an earlier turn's text %q", last.Text)
	}
}
