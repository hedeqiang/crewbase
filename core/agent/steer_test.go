package agent

import (
	"testing"

	"github.com/hedeqiang/crewbase/kernel"
)

func TestQueueAndSteer(t *testing.T) {
	cur := Input{ID: "a", Session: "s"}
	if !Joinable(cur, Input{ID: "b", Session: "s"}) {
		t.Fatal("a message mid-turn joins it by default")
	}
	if Joinable(cur, Input{ID: "c", Session: "s", Queue: true}) {
		t.Fatal("a queued (排队) message waits for the turn to end")
	}
}

func TestMoveQueued(t *testing.T) {
	a := &Agent{id: "x", ctx: kernel.NewHost(nil).Root()}
	a.queue = []Input{{ID: "1"}, {ID: "2"}, {ID: "3"}}
	order := func() string {
		s := ""
		for _, in := range a.queue {
			s += in.ID
		}
		return s
	}
	for _, c := range []struct {
		id   string
		by   int
		want string
	}{{"3", -1, "132"}, {"3", -5, "312"}, {"3", 1, "132"}, {"2", 9, "132"}, {"1", 1, "312"}} {
		if !a.Move(c.id, c.by) || order() != c.want {
			t.Fatalf("move %s by %d: %s, want %s", c.id, c.by, order(), c.want)
		}
	}
	if a.Move("nope", 1) {
		t.Fatal("moved what is not queued")
	}
}
