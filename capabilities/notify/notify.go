// Package notify defines the `notify` capability: delivering events to
// people. Channels (desktop, mobile push, Slack, Telegram, Feishu…) are
// providers registered on the service; delivery fans out in parallel and a
// failing channel never blocks the others.
package notify

import (
	"context"
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
	// enabled[kind][channel]; missing = default policy
	enabled map[string]map[string]bool
	recent  []Notification
	seq     int
}

func NewService() *Service { return &Service{enabled: map[string]map[string]bool{}} }

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

// SetEnabled toggles a kind on a channel.
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
	for _, ch := range s.channels {
		on, set := s.enabled[n.Kind][ch.Name()]
		if (set && on) || (!set && defaultOn(n.Kind)) {
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
