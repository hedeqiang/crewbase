// Package notify defines the `notify` capability: delivering events to
// people. Channels (desktop, mobile push, Slack, Telegram, Feishu…) are
// providers registered on the service; delivery fans out in parallel and a
// failing channel never blocks the others.
package notify

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/kernel"
)

const ServiceKey = "notify"

// Kinds of notifications (settings › notifications matrix).
const (
	KindNeedsYou    = "needs-you"    // approval or takeover
	KindTaskDone    = "task-done"    // turn completed
	KindHandover    = "handover"     // channel handover
	KindModelSwitch = "model-switch" // never notified by default (quiet)
	KindQuotaLow    = "quota-low"
	KindBrief       = "brief"
	KindFailed      = "failed"
	KindRoutine     = "routine" // routine found something
)

// Kinds lists every kind, in settings order.
var Kinds = []string{KindNeedsYou, KindTaskDone, KindFailed, KindHandover, KindRoutine, KindBrief, KindQuotaLow, KindModelSwitch}

// QuietHours is a daily window (local time, may span midnight, e.g.
// 22:00–08:00) in which only urgent notifications are delivered.
type QuietHours struct {
	On    bool   `json:"on"`
	Start string `json:"start"` // "HH:MM"
	End   string `json:"end"`   // "HH:MM"
}

func parseHM(v string) (int, error) {
	t, err := time.Parse("15:04", v)
	if err != nil {
		return 0, fmt.Errorf("time %q must be HH:MM", v)
	}
	return t.Hour()*60 + t.Minute(), nil
}

// Validate checks the times.
func (q QuietHours) Validate() error {
	if !q.On && q.Start == "" && q.End == "" {
		return nil
	}
	a, err := parseHM(q.Start)
	if err != nil {
		return err
	}
	b, err := parseHM(q.End)
	if err != nil {
		return err
	}
	if a == b {
		return errors.New("quiet hours start and end must differ")
	}
	return nil
}

// Contains reports whether t (in its own location) falls in the window.
func (q QuietHours) Contains(t time.Time) bool {
	if !q.On {
		return false
	}
	a, err1 := parseHM(q.Start)
	b, err2 := parseHM(q.End)
	if err1 != nil || err2 != nil || a == b {
		return false
	}
	m := t.Hour()*60 + t.Minute()
	if a < b {
		return m >= a && m < b
	}
	return m >= a || m < b // spans midnight
}

// Prefs are the person's notification choices (settings › 通知). They
// override channel defaults set by plugin config.
type Prefs struct {
	// Matrix[kind][channel]; missing entries use the default policy.
	Matrix map[string]map[string]bool `json:"matrix,omitempty"`
	Quiet  QuietHours                 `json:"quiet"`
}

// Validate checks kinds and quiet hours.
func (p Prefs) Validate() error {
	for k := range p.Matrix {
		known := false
		for _, x := range Kinds {
			known = known || x == k
		}
		if !known {
			return fmt.Errorf("unknown notification kind %q", k)
		}
	}
	return p.Quiet.Validate()
}

// Notification is one message to a person.
type Notification struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`
	Title   string    `json:"title"`
	Body    string    `json:"body,omitempty"`
	Agent   string    `json:"agent,omitempty"`
	Session string    `json:"session,omitempty"`
	Time    time.Time `json:"time"`
	// Urgent ignores quiet hours (approvals are urgent by default).
	Urgent bool `json:"urgent,omitempty"`
}

// Channel delivers notifications.
type Channel interface {
	Name() string
	Send(ctx context.Context, n Notification) error
}

// Service is the `notify` service.
type Service struct {
	mu       sync.Mutex
	channels []Channel
	// enabled[kind][channel] set by channel config; missing = default policy
	enabled map[string]map[string]bool
	prefs   Prefs // the person's choices, override enabled
	now     func() time.Time
	recent  []Notification
	seq     int
	// dndUntil suppresses non-urgent notifications to every channel.
	dndUntil time.Time
}

// SetDoNotDisturb suppresses non-urgent notifications until t (zero ends
// it). Urgent ones (approvals, takeovers) are always delivered.
func (s *Service) SetDoNotDisturb(until time.Time) {
	s.mu.Lock()
	s.dndUntil = until
	s.mu.Unlock()
}

// DoNotDisturb returns the end of do-not-disturb, zero when off.
func (s *Service) DoNotDisturb() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Now().After(s.dndUntil) {
		return time.Time{}
	}
	return s.dndUntil
}

func NewService() *Service { return &Service{enabled: map[string]map[string]bool{}, now: time.Now} }

// SetPrefs replaces the person's choices.
func (s *Service) SetPrefs(p Prefs) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prefs = p
}

// Prefs returns the person's choices.
func (s *Service) Prefs() Prefs {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prefs
}

// Channels returns the names of registered channels, in order.
func (s *Service) Channels() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.channels))
	for _, ch := range s.channels {
		out = append(out, ch.Name())
	}
	return out
}

// Enabled reports whether kind is delivered to channel (choices, then
// channel config, then the default policy).
func (s *Service) Enabled(kind, channel string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabledLocked(kind, channel)
}

func (s *Service) enabledLocked(kind, channel string) bool {
	if on, ok := s.prefs.Matrix[kind][channel]; ok {
		return on
	}
	if on, ok := s.enabled[kind][channel]; ok {
		return on
	}
	return defaultOn(kind)
}

// Quiet reports whether non-urgent notifications are held right now (do
// not disturb or quiet hours).
func (s *Service) Quiet() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.quietLocked()
}

func (s *Service) quietLocked() bool {
	now := s.now()
	return now.Before(s.dndUntil) || s.prefs.Quiet.Contains(now)
}

// SendTo delivers n to one channel regardless of choices (settings › 通知
// › 发送测试).
func (s *Service) SendTo(ctx context.Context, channel string, n Notification) error {
	s.mu.Lock()
	var target Channel
	for _, ch := range s.channels {
		if ch.Name() == channel {
			target = ch
		}
	}
	s.mu.Unlock()
	if target == nil {
		return fmt.Errorf("notification channel %q is not connected", channel)
	}
	if n.Time.IsZero() {
		n.Time = time.Now().UTC()
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return target.Send(cctx, n)
}

// AddChannel registers a channel for ctx's lifetime.
func (s *Service) AddChannel(c *kernel.Context, ch Channel) {
	s.mu.Lock()
	s.channels = append(s.channels, ch)
	s.mu.Unlock()
	c.Defer(func() {
		s.mu.Lock()
		for i, x := range s.channels {
			if x == ch {
				s.channels = append(s.channels[:i:i], s.channels[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
	})
}

// SetEnabled sets a channel's default for kind (from the channel's own
// config); the person's choices in Prefs still win.
func (s *Service) SetEnabled(kind, channel string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.enabled[kind] == nil {
		s.enabled[kind] = map[string]bool{}
	}
	s.enabled[kind][channel] = on
}

// defaultOn: quiet by default — model switches never notify.
func defaultOn(kind string) bool { return kind != KindModelSwitch }

// Send delivers n to every enabled channel; errors are joined.
func (s *Service) Send(ctx context.Context, n Notification) []error {
	s.mu.Lock()
	s.seq++
	if n.ID == "" {
		n.ID = time.Now().UTC().Format("20060102150405") + "-" + itoa(s.seq)
	}
	if n.Time.IsZero() {
		n.Time = time.Now().UTC()
	}
	var targets []Channel
	quiet := !n.Urgent && s.quietLocked()
	for _, ch := range s.channels {
		if quiet {
			break
		}
		if s.enabledLocked(n.Kind, ch.Name()) {
			targets = append(targets, ch)
		}
	}
	s.recent = append(s.recent, n)
	if len(s.recent) > 200 {
		s.recent = s.recent[len(s.recent)-200:]
	}
	s.mu.Unlock()

	errs := make([]error, len(targets))
	var wg sync.WaitGroup
	for i, ch := range targets {
		wg.Add(1)
		go func(i int, ch Channel) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					errs[i] = context.Canceled
				}
			}()
			cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			errs[i] = ch.Send(cctx, n)
		}(i, ch)
	}
	wg.Wait()
	var out []error
	for _, e := range errs {
		if e != nil {
			out = append(out, e)
		}
	}
	return out
}

// Recent returns the latest notifications, newest last.
func (s *Service) Recent() []Notification {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Notification(nil), s.recent...)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// Sent is emitted for every notification (the desktop channel and the UI
// listen to it).
var Sent = kernel.NewEmit[Notification]("notify/sent")

type desktop struct{ h *kernel.Host }

func (d desktop) Name() string { return "desktop" }
func (d desktop) Send(_ context.Context, n Notification) error {
	Sent.Emit(d.h, n)
	return nil
}

// Plugin provides the notify service with the desktop channel.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/notify",
	Description: "Notification service with the desktop channel.",
	Apply: func(c *kernel.Context) error {
		s := NewService()
		s.AddChannel(c, desktop{h: c.Host()})
		return c.Provide(ServiceKey, s)
	},
})
