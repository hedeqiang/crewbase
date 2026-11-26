package schedule

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/kernel"
)

func TestWindowAndZone(t *testing.T) {
	utc := func(d, h, m int) time.Time { return time.Date(2026, 11, d, h, m, 0, 0, time.UTC) }
	cases := []struct {
		spec string
		from time.Time
		want time.Time
	}{
		// an interval inside a night window, then the next evening
		{"every 10m during 22:00-08:00", utc(14, 23, 0), utc(14, 23, 10)},
		{"every 10m during 22:00-08:00", utc(15, 7, 55), utc(15, 22, 0)},
		{"every 10m during 22:00-08:00", utc(15, 12, 0), utc(15, 22, 0)},
		// a cron inside a day window
		{"0 * * * * during 09:00-11:00", utc(14, 9, 30), utc(14, 10, 0)},
		{"0 * * * * during 09:00-11:00", utc(14, 10, 59), utc(15, 9, 0)},
		// read in another zone: 08:00 in Shanghai is 00:00 UTC
		{"TZ=Asia/Shanghai daily 08:00", utc(14, 1, 0), utc(15, 0, 0)},
		{"TZ=Asia/Shanghai every 15m during 22:00-08:00", utc(14, 5, 0), utc(14, 14, 0)},
	}
	for _, c := range cases {
		s, err := Parse(c.spec)
		if err != nil {
			t.Fatalf("%s: %v", c.spec, err)
		}
		if got := s.Next(c.from); !got.Equal(c.want) {
			t.Errorf("%s after %s: got %s want %s", c.spec, c.from, got.UTC(), c.want)
		}
	}
	s, _ := Parse("TZ=Asia/Shanghai webhook during 22:00-08:00")
	if !s.IsWebhook() || !s.Next(utc(14, 1, 0)).IsZero() {
		t.Fatal("a windowed webhook has no timed firings")
	}
	if !s.Within(utc(14, 15, 0)) || s.Within(utc(14, 3, 0)) { // 23:00 / 11:00 in Shanghai
		t.Fatal("within")
	}
	if !IsWebhookSpec("TZ=UTC webhook during 01:00-02:00") || IsWebhookSpec("every 1h") {
		t.Fatal("IsWebhookSpec")
	}
	for _, bad := range []string{"every 1h during 22:00", "every 1h during 08:00-08:00", "TZ=Nowhere/City every 1h", "webhook now", "during 01:00-02:00"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestWindowedWebhookSkipsOutside(t *testing.T) {
	h := kernel.NewHost(nil)
	defer h.Close()
	clk := &clock{t: time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)}
	s, _ := New(h.Root(), "", clk.now)
	n := 0
	s.Handle(h.Root(), "x", func(context.Context, Firing) error { n++; return nil })
	j, err := s.Add(Job{Owner: "x", Spec: "TZ=UTC webhook during 22:00-08:00"})
	if err != nil || j.Secret == "" {
		t.Fatalf("%v %+v", err, j)
	}
	if err := s.Trigger(context.Background(), j.ID, "d1", nil); !errors.Is(err, ErrOutsideWindow) || n != 0 {
		t.Fatalf("outside: %v n=%d", err, n)
	}
	// a person's run now is not held to the window
	if err := s.Trigger(context.Background(), j.ID, "", nil); err != nil || n != 1 {
		t.Fatalf("manual: %v n=%d", err, n)
	}
	clk.add(11 * time.Hour) // 23:00
	if err := s.Trigger(context.Background(), j.ID, "d2", nil); err != nil || n != 2 {
		t.Fatalf("inside: %v n=%d", err, n)
	}
}
