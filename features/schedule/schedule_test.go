package schedule

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/kernel"
)

func TestParseAndNext(t *testing.T) {
	base := time.Date(2026, 11, 14, 8, 59, 30, 0, time.UTC) // Saturday
	cases := []struct {
		spec string
		want time.Time
	}{
		{"*/15 * * * *", time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)},
		{"daily 09:30", time.Date(2026, 11, 14, 9, 30, 0, 0, time.UTC)},
		{"daily 08:00", time.Date(2026, 11, 15, 8, 0, 0, 0, time.UTC)},
		{"weekly mon 09:00", time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)},
		{"0 9 1 * *", time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)},
		{"30 8-10/2 * * 1-5", time.Date(2026, 11, 16, 8, 30, 0, 0, time.UTC)},
		{"every 5m", base.Add(5 * time.Minute).Truncate(time.Second)},
	}
	for _, c := range cases {
		s, err := Parse(c.spec)
		if err != nil {
			t.Fatalf("%s: %v", c.spec, err)
		}
		if got := s.Next(base); !got.Equal(c.want) {
			t.Errorf("%s: got %s want %s", c.spec, got, c.want)
		}
	}
	for _, bad := range []string{"every 10s", "61 * * * *", "* * *", "daily 25:00", "weekly xyz 09:00", ""} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func TestFiringPersistenceAndIdempotency(t *testing.T) {
	dir := t.TempDir()
	h := kernel.NewHost(nil)
	defer h.Close()
	clk := &clock{t: time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)}
	s, err := New(h.Root(), dir, clk.now)
	if err != nil {
		t.Fatal(err)
	}
	var fired []string
	s.Handle(h.Root(), "routine", func(_ context.Context, f Firing) error {
		fired = append(fired, f.Key)
		return nil
	})
	j, err := s.Add(Job{Owner: "routine", Name: "watch", Spec: "every 1m"})
	if err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background())
	if len(fired) != 0 {
		t.Fatal("fired too early")
	}
	clk.add(61 * time.Second)
	s.Tick(context.Background())
	s.Tick(context.Background()) // same instant: no double fire
	if len(fired) != 1 {
		t.Fatalf("fired %d", len(fired))
	}
	clk.add(61 * time.Second)
	s.Tick(context.Background())
	if len(fired) != 2 || fired[0] == fired[1] {
		t.Fatalf("%v", fired)
	}

	// reload: the job and its last key survive; nothing replays
	s2, err := New(h.Root(), dir, clk.now)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s2.Get(j.ID)
	if !ok || got.Fires != 2 || got.LastKey != fired[1] {
		t.Fatalf("%+v", got)
	}

	// manual trigger with an idempotency key is delivered once
	s.Trigger(context.Background(), j.ID, "abc", nil)
	s.Trigger(context.Background(), j.ID, "abc", nil)
	if len(fired) != 3 {
		t.Fatalf("idempotent trigger fired %d", len(fired))
	}
	// disabled jobs do not fire
	s.Update(j.ID, func(x *Job) { x.Enabled = false })
	clk.add(10 * time.Minute)
	s.Tick(context.Background())
	if len(fired) != 3 {
		t.Fatal("disabled job fired")
	}
}

func TestWebhookSecret(t *testing.T) {
	h := kernel.NewHost(nil)
	defer h.Close()
	s, _ := New(h.Root(), "", nil)
	j, err := s.Add(Job{Owner: "routine", Spec: "webhook"})
	if err != nil || j.Secret == "" {
		t.Fatal(j, err)
	}
	if !s.CheckSecret(j.ID, j.Secret) || s.CheckSecret(j.ID, "nope") || s.CheckSecret("other", j.Secret) {
		t.Fatal("secret check")
	}
}
