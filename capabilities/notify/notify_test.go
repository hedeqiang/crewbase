package notify

import (
	"context"
	"testing"
	"time"
)

type rec struct{ got []string }

func (r *rec) Name() string { return "rec" }
func (r *rec) Send(_ context.Context, n Notification) error {
	r.got = append(r.got, n.Title)
	return nil
}

func TestDoNotDisturbKeepsUrgent(t *testing.T) {
	s := NewService()
	r := &rec{}
	s.channels = append(s.channels, r)
	s.SetDoNotDisturb(time.Now().Add(time.Hour))
	s.Send(context.Background(), Notification{Kind: KindTaskDone, Title: "done"})
	s.Send(context.Background(), Notification{Kind: KindNeedsYou, Title: "approve", Urgent: true})
	if len(r.got) != 1 || r.got[0] != "approve" {
		t.Fatalf("%v", r.got)
	}
	s.SetDoNotDisturb(time.Time{})
	s.Send(context.Background(), Notification{Kind: KindTaskDone, Title: "done2"})
	if len(r.got) != 2 || s.DoNotDisturb() != (time.Time{}) {
		t.Fatalf("%v", r.got)
	}
}
