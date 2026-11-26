package schedule

// Limits on a spec: the zone its times are read in, and a daily window it
// fires in.
//
//	TZ=Area/City <spec>        times are read in that zone (default: local)
//	<spec> during HH:MM-HH:MM  it fires only inside that daily window; one
//	                           may cross midnight (22:00-08:00)
//
// e.g. "TZ=Asia/Shanghai every 10m during 22:00-08:00". A windowed webhook
// ("webhook during 22:00-08:00") takes deliveries only inside it.

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrOutsideWindow: a delivery came outside its job's window (skipped).
var ErrOutsideWindow = errors.New("outside its hours")

// window is a daily span in minutes from midnight, [from, to).
type window struct{ from, to int }

func (w window) contains(t time.Time) bool {
	m := t.Hour()*60 + t.Minute()
	if w.from < w.to {
		return m >= w.from && m < w.to
	}
	return m >= w.from || m < w.to // crosses midnight
}

// nextOpen is the first time at or after t the window opens.
func (w window) nextOpen(t time.Time) time.Time {
	o := time.Date(t.Year(), t.Month(), t.Day(), w.from/60, w.from%60, 0, 0, t.Location())
	if o.Before(t) {
		o = time.Date(t.Year(), t.Month(), t.Day()+1, w.from/60, w.from%60, 0, 0, t.Location())
	}
	return o
}

// limits strips the zone prefix and the window suffix off fields.
func (s *Spec) limits(fields []string) ([]string, error) {
	if len(fields) > 0 && strings.HasPrefix(fields[0], "TZ=") {
		loc, err := time.LoadLocation(strings.TrimPrefix(fields[0], "TZ="))
		if err != nil {
			return nil, fmt.Errorf("time zone: %w", err)
		}
		s.loc, fields = loc, fields[1:]
	}
	if n := len(fields); n >= 2 && strings.EqualFold(fields[n-2], "during") {
		a, b, ok := strings.Cut(fields[n-1], "-")
		if !ok {
			return nil, fmt.Errorf("want: during HH:MM-HH:MM")
		}
		ah, am, err := hhmm(a)
		if err != nil {
			return nil, err
		}
		bh, bm, err := hhmm(b)
		if err != nil {
			return nil, err
		}
		if ah*60+am == bh*60+bm {
			return nil, fmt.Errorf("the window %s is empty", fields[n-1])
		}
		s.win, fields = &window{from: ah*60 + am, to: bh*60 + bm}, fields[:n-2]
	}
	return fields, nil
}

// IsWebhookSpec reports whether raw is a webhook trigger ("webhook", maybe
// with a zone and a window).
func IsWebhookSpec(raw string) bool {
	f := strings.Fields(raw)
	if len(f) > 0 && strings.HasPrefix(f[0], "TZ=") {
		f = f[1:]
	}
	return len(f) > 0 && strings.EqualFold(f[0], "webhook")
}

// IsWebhook reports whether the spec is a webhook trigger.
func (s Spec) IsWebhook() bool { return s.webhook }

// Within reports whether t is inside the spec's window (always, without one).
func (s Spec) Within(t time.Time) bool {
	if s.win == nil {
		return true
	}
	if s.loc != nil {
		t = t.In(s.loc)
	}
	return s.win.contains(t)
}

// Location is the zone the spec's times are read in.
func (s Spec) Location() *time.Location {
	if s.loc != nil {
		return s.loc
	}
	return time.Local
}

// windowed returns the first firing of next after t inside the window.
// An interval starts again when the window opens.
func (s Spec) windowed(t time.Time) time.Time {
	n := s.next(t)
	for i := 0; i < 1000 && !n.IsZero(); i++ {
		if s.win.contains(n) {
			return n
		}
		open := s.win.nextOpen(n)
		if s.every > 0 {
			return open
		}
		n = s.next(open.Add(-time.Nanosecond))
	}
	return time.Time{}
}
