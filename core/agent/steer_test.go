package agent

import "testing"

func TestQueueAndSteer(t *testing.T) {
	cur := Input{ID: "a", Session: "s"}
	if !Joinable(cur, Input{ID: "b", Session: "s"}) {
		t.Fatal("a message mid-turn joins it by default")
	}
	if Joinable(cur, Input{ID: "c", Session: "s", Queue: true}) {
		t.Fatal("a queued (排队) message waits for the turn to end")
	}
}
