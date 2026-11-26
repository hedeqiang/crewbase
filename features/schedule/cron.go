package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Spec is a parsed trigger: a 5-field cron expression, "every <duration>",
// "daily HH:MM", "weekly <weekday> HH:MM", "at <RFC3339>" or "webhook",
// optionally read in a zone and limited to a daily window (window.go).
type Spec struct {
	raw     string
	every   time.Duration
	at      time.Time
	cron    *cron
	webhook bool
	loc     *time.Location
	win     *window
}

func (s Spec) String() string { return s.raw }

// MinInterval is the smallest allowed gap between two firings.
const MinInterval = 60 * time.Second

// Parse parses a trigger spec.
func Parse(raw string) (Spec, error) {
	raw = strings.TrimSpace(raw)
	s := Spec{raw: raw}
	fields, err := s.limits(strings.Fields(raw))
	if err != nil {
		return s, err
	}
	if len(fields) == 0 {
		return s, fmt.Errorf("empty schedule")
	}
	switch strings.ToLower(fields[0]) {
	case "webhook":
		if len(fields) != 1 {
			return s, fmt.Errorf("want: webhook [during HH:MM-HH:MM]")
		}
		s.webhook = true
		return s, nil
	case "every":
		if len(fields) != 2 {
			return s, fmt.Errorf("want: every <duration>")
		}
		d, err := time.ParseDuration(fields[1])
		if err != nil {
			return s, err
		}
		if d < MinInterval {
			return s, fmt.Errorf("interval %s is below the minimum of %s", d, MinInterval)
		}
		s.every = d
		return s, nil
	case "at":
		if len(fields) != 2 {
			return s, fmt.Errorf("want: at <RFC3339 time>")
		}
		t, err := time.Parse(time.RFC3339, fields[1])
		if err != nil {
			return s, err
		}
		s.at = t
		return s, nil
	case "daily":
		if len(fields) != 2 {
			return s, fmt.Errorf("want: daily HH:MM")
		}
		h, m, err := hhmm(fields[1])
		if err != nil {
			return s, err
		}
		c, _ := parseCron(fmt.Sprintf("%d %d * * *", m, h))
		s.cron = c
		return s, nil
	case "weekly":
		if len(fields) != 3 {
			return s, fmt.Errorf("want: weekly <mon|tue|...> HH:MM")
		}
		wd, ok := weekdays[strings.ToLower(fields[1])[:3]]
		if !ok {
			return s, fmt.Errorf("unknown weekday %q", fields[1])
		}
		h, m, err := hhmm(fields[2])
		if err != nil {
			return s, err
		}
		c, _ := parseCron(fmt.Sprintf("%d %d * * %d", m, h, wd))
		s.cron = c
		return s, nil
	}
	c, err := parseCron(strings.Join(fields, " "))
	if err != nil {
		return s, err
	}
	s.cron = c
	return s, nil
}

var weekdays = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}

func hhmm(v string) (int, int, error) {
	hs, ms, ok := strings.Cut(v, ":")
	h, err1 := strconv.Atoi(hs)
	m, err2 := strconv.Atoi(ms)
	if !ok || err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("bad time %q (want HH:MM)", v)
	}
	return h, m, nil
}

// Next returns the first firing strictly after t, or zero if none (a
// webhook has none), inside the window when there is one.
func (s Spec) Next(t time.Time) time.Time {
	if s.loc != nil {
		t = t.In(s.loc)
	}
	if s.win != nil {
		return s.windowed(t)
	}
	return s.next(t)
}

func (s Spec) next(t time.Time) time.Time {
	switch {
	case s.every > 0:
		return t.Add(s.every).Truncate(time.Second)
	case !s.at.IsZero():
		if s.at.After(t) {
			return s.at
		}
		return time.Time{}
	case s.cron != nil:
		return s.cron.next(t)
	}
	return time.Time{}
}

// ---------------------------------------------------------------- cron

type cron struct {
	min, hour, dom, mon, dow uint64 // bitsets
	domStar, dowStar         bool
}

func parseCron(expr string) (*cron, error) {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return nil, fmt.Errorf("cron needs 5 fields (min hour dom mon dow), got %d", len(f))
	}
	var c cron
	var err error
	if c.min, err = field(f[0], 0, 59); err != nil {
		return nil, fmt.Errorf("minute: %w", err)
	}
	if c.hour, err = field(f[1], 0, 23); err != nil {
		return nil, fmt.Errorf("hour: %w", err)
	}
	if c.dom, err = field(f[2], 1, 31); err != nil {
		return nil, fmt.Errorf("day of month: %w", err)
	}
	if c.mon, err = field(f[3], 1, 12); err != nil {
		return nil, fmt.Errorf("month: %w", err)
	}
	if c.dow, err = field(strings.ReplaceAll(f[4], "7", "0"), 0, 6); err != nil {
		return nil, fmt.Errorf("day of week: %w", err)
	}
	c.domStar, c.dowStar = f[2] == "*", f[4] == "*"
	// A minute-level cron fires at most once a minute, so MinInterval holds.
	return &c, nil
}

func field(s string, lo, hi int) (uint64, error) {
	var bits uint64
	for _, part := range strings.Split(s, ",") {
		step := 1
		if base, st, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(st)
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("bad step %q", st)
			}
			part, step = base, n
		}
		a, b := lo, hi
		if part != "*" {
			x, y, isRange := strings.Cut(part, "-")
			var err error
			if a, err = strconv.Atoi(x); err != nil {
				return 0, fmt.Errorf("bad value %q", x)
			}
			b = a
			if isRange {
				if b, err = strconv.Atoi(y); err != nil {
					return 0, fmt.Errorf("bad value %q", y)
				}
			} else if step > 1 {
				b = hi
			}
		}
		if a < lo || b > hi || a > b {
			return 0, fmt.Errorf("value out of range %d-%d", lo, hi)
		}
		for i := a; i <= b; i += step {
			bits |= 1 << uint(i)
		}
	}
	return bits, nil
}

func has(bits uint64, v int) bool { return bits&(1<<uint(v)) != 0 }

func (c *cron) dayMatches(t time.Time) bool {
	d, w := has(c.dom, t.Day()), has(c.dow, int(t.Weekday()))
	switch {
	case c.domStar && c.dowStar:
		return true
	case c.domStar:
		return w
	case c.dowStar:
		return d
	default:
		return d || w // standard cron: either matches
	}
}

func (c *cron) next(t time.Time) time.Time {
	t = t.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		if !has(c.mon, int(t.Month())) {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !c.dayMatches(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !has(c.hour, t.Hour()) {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, t.Location())
			continue
		}
		if !has(c.min, t.Minute()) {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	return time.Time{}
}
