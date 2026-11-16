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

func at(h, m int) time.Time { return time.Date(2026, 11, 14, h, m, 0, 0, time.Local) }

func TestQuietHoursSpanMidnight(t *testing.T) {
	q := QuietHours{On: true, Start: "22:00", End: "08:00"}
	for _, c := range []struct {
		h, m int
		in   bool
	}{{21, 59, false}, {22, 0, true}, {23, 30, true}, {3, 0, true}, {7, 59, true}, {8, 0, false}, {12, 0, false}} {
		if q.Contains(at(c.h, c.m)) != c.in {
			t.Fatalf("%02d:%02d want %v", c.h, c.m, c.in)
		}
	}
	day := QuietHours{On: true, Start: "12:00", End: "13:30"}
	if !day.Contains(at(12, 45)) || day.Contains(at(13, 30)) || day.Contains(at(11, 0)) {
		t.Fatal("same-day window")
	}
	if (QuietHours{Start: "22:00", End: "08:00"}).Contains(at(23, 0)) {
		t.Fatal("off window must not hold")
	}
	for _, bad := range []QuietHours{{On: true, Start: "25:00", End: "08:00"}, {On: true, Start: "08:00", End: "08:00"}, {On: true, Start: "8", End: "9"}} {
		if bad.Validate() == nil {
			t.Fatalf("%+v should be invalid", bad)
		}
	}
	if (Prefs{Matrix: map[string]map[string]bool{"nope": {"desktop": true}}}).Validate() == nil {
		t.Fatal("unknown kind accepted")
	}
}

func TestPrefsOverrideChannelConfigAndQuietKeepsUrgent(t *testing.T) {
	s := NewService()
	r := &rec{}
	s.channels = append(s.channels, r)
	// channel config says: only failures
	for _, k := range Kinds {
		s.SetEnabled(k, "rec", k == KindFailed)
	}
	s.Send(context.Background(), Notification{Kind: KindTaskDone, Title: "done"})
	if len(r.got) != 0 {
		t.Fatalf("%v", r.got)
	}
	// the person turns task-done on; their choice wins even though the
	// channel config was applied (e.g. remounted) afterwards
	s.SetPrefs(Prefs{Matrix: map[string]map[string]bool{KindTaskDone: {"rec": true}, KindNeedsYou: {"rec": true}, KindFailed: {"rec": false}}})
	s.SetEnabled(KindTaskDone, "rec", false)
	s.Send(context.Background(), Notification{Kind: KindTaskDone, Title: "done"})
	s.Send(context.Background(), Notification{Kind: KindFailed, Title: "failed"})
	if len(r.got) != 1 || r.got[0] != "done" || s.Enabled(KindFailed, "rec") {
		t.Fatalf("%v", r.got)
	}
	// quiet hours hold non-urgent ones, urgent still goes through
	s.now = func() time.Time { return at(23, 0) }
	p := s.Prefs()
	p.Quiet = QuietHours{On: true, Start: "22:00", End: "08:00"}
	s.SetPrefs(p)
	if !s.Quiet() {
		t.Fatal("should be quiet")
	}
	s.Send(context.Background(), Notification{Kind: KindTaskDone, Title: "late"})
	s.Send(context.Background(), Notification{Kind: KindNeedsYou, Title: "approve", Urgent: true})
	if len(r.got) != 2 || r.got[1] != "approve" {
		t.Fatalf("%v", r.got)
	}
	// held notifications are still listed in Recent
	if rec := s.Recent(); rec[len(rec)-2].Title != "late" {
		t.Fatalf("%+v", rec)
	}
	if err := s.SendTo(context.Background(), "rec", Notification{Title: "test"}); err != nil || r.got[2] != "test" {
		t.Fatalf("%v %v", err, r.got)
	}
	if s.SendTo(context.Background(), "nope", Notification{Title: "x"}) == nil {
		t.Fatal("unknown channel")
	}
}

type follower struct{ rec }

func (f *follower) Name() string    { return "rec-native" }
func (f *follower) Follows() string { return "rec" }

// A follower channel is not listed, follows its channel's choices and is
// part of its test.
func TestFollowerChannel(t *testing.T) {
	s := NewService()
	r, f := &rec{}, &follower{}
	s.channels = append(s.channels, r, f)
	if ch := s.Channels(); len(ch) != 1 || ch[0] != "rec" {
		t.Fatalf("channels %v", ch)
	}
	s.SetPrefs(Prefs{Matrix: map[string]map[string]bool{KindTaskDone: {"rec": false}}})
	s.Send(context.Background(), Notification{Kind: KindTaskDone, Title: "done"})
	s.Send(context.Background(), Notification{Kind: KindFailed, Title: "failed"})
	if len(r.got) != 1 || len(f.got) != 1 || f.got[0] != "failed" {
		t.Fatalf("rec %v follower %v", r.got, f.got)
	}
	if err := s.SendTo(context.Background(), "rec", Notification{Title: "test"}); err != nil || len(f.got) != 2 {
		t.Fatalf("test: %v %v", err, f.got)
	}
}
