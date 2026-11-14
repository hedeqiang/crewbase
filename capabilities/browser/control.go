package browser

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/kernel"
)

// Control holds the takeover state of every agent's browser. Providers embed
// one and route every agent operation through Session (which uses Op).
type Control struct {
	d kernel.Dispatcher

	mu     sync.Mutex
	agents map[string]*control
}

type control struct {
	by    string
	since time.Time
	ended uint64        // finished takeovers
	wake  chan struct{} // closed and replaced on every change
	ops   map[uint64]context.CancelFunc
	next  uint64
	epoch uint64 // started takeovers
}

// NewControl creates a Control. d (may be nil) dispatches the takeover events.
func NewControl(d kernel.Dispatcher) *Control {
	return &Control{d: d, agents: map[string]*control{}}
}

func (c *Control) get(agent string) *control {
	x := c.agents[agent]
	if x == nil {
		x = &control{wake: make(chan struct{}), ops: map[uint64]context.CancelFunc{}}
		c.agents[agent] = x
	}
	return x
}

func (x *control) notify() {
	close(x.wake)
	x.wake = make(chan struct{})
}

// Holder returns who controls the agent's browser ("" = the agent).
func (c *Control) Holder(agent string) (by string, since time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if x := c.agents[agent]; x != nil {
		return x.by, x.since
	}
	return "", time.Time{}
}

// Begin starts a takeover by a person. In-flight agent operations are
// cancelled and their results discarded. started is false when the same
// person already holds the browser.
func (c *Control) Begin(agent, by string) (started bool, err error) {
	if by == "" {
		by = "user"
	}
	c.mu.Lock()
	x := c.get(agent)
	switch {
	case x.by == by:
		c.mu.Unlock()
		return false, nil
	case x.by != "":
		c.mu.Unlock()
		return false, ErrNotController
	}
	x.by, x.since = by, time.Now().UTC()
	x.epoch++
	for _, cancel := range x.ops {
		cancel()
	}
	x.notify()
	ev := TakeoverEvent{Agent: agent, By: by, At: x.since}
	c.mu.Unlock()
	if c.d != nil {
		TakeoverStarted.Emit(kernel.For(c.d, agent), ev)
	}
	return true, nil
}

// End finishes the takeover and wakes AwaitHandback callers.
func (c *Control) End(agent string) error {
	c.mu.Lock()
	x := c.get(agent)
	if x.by == "" {
		c.mu.Unlock()
		return ErrNotTakenOver
	}
	ev := TakeoverEvent{Agent: agent, By: x.by, At: time.Now().UTC()}
	x.by, x.since = "", time.Time{}
	x.ended++
	x.notify()
	c.mu.Unlock()
	if c.d != nil {
		TakeoverEnded.Emit(kernel.For(c.d, agent), ev)
	}
	return nil
}

// Busy reports whether an agent operation is in flight.
func (c *Control) Busy(agent string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	x := c.agents[agent]
	return x != nil && len(x.ops) > 0
}

// Await blocks until a takeover that is active now, or that starts later,
// has ended.
func (c *Control) Await(ctx context.Context, agent string) error {
	c.mu.Lock()
	x := c.get(agent)
	target := x.ended + 1
	c.mu.Unlock()
	for {
		c.mu.Lock()
		done := x.ended >= target
		wake := x.wake
		c.mu.Unlock()
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		}
	}
}

// Op starts an agent operation. It fails with ErrTakenOver while a person
// holds the browser. The returned context is cancelled when a takeover
// starts; end reports ErrTakenOver if one started meanwhile, in which case
// the operation's result must be discarded.
func (c *Control) Op(ctx context.Context, agent string) (opCtx context.Context, end func() error, err error) {
	c.mu.Lock()
	x := c.get(agent)
	if x.by != "" {
		c.mu.Unlock()
		return nil, nil, ErrTakenOver
	}
	opCtx, cancel := context.WithCancel(ctx)
	id := x.next
	x.next++
	x.ops[id] = cancel
	epoch := x.epoch
	c.mu.Unlock()
	return opCtx, func() error {
		c.mu.Lock()
		delete(x.ops, id)
		changed := x.epoch != epoch
		c.mu.Unlock()
		cancel()
		if changed {
			return ErrTakenOver
		}
		return nil
	}, nil
}

// Session is an agent's handle on its browser. Every method fails with
// ErrTakenOver while a person controls the browser. A Session has no
// screenshot method on purpose: the model never sees screenshots.
type Session struct {
	agent string
	page  Page
	ctl   *Control
	touch func()
}

// NewSession wraps a provider page. touch (may be nil) is called around
// every operation so providers can track idleness.
func NewSession(agent string, page Page, ctl *Control, touch func()) *Session {
	if touch == nil {
		touch = func() {}
	}
	return &Session{agent: agent, page: page, ctl: ctl, touch: touch}
}

// Agent returns the owning agent id.
func (s *Session) Agent() string { return s.agent }

func (s *Session) do(ctx context.Context, fn func(ctx context.Context) error) error {
	opCtx, end, err := s.ctl.Op(ctx, s.agent)
	if err != nil {
		return err
	}
	s.touch()
	err = fn(opCtx)
	s.touch()
	if e := end(); e != nil {
		return e
	}
	return err
}

func (s *Session) Navigate(ctx context.Context, url string) error {
	return s.do(ctx, func(ctx context.Context) error { return s.page.Navigate(ctx, url) })
}

func (s *Session) Back(ctx context.Context) error {
	return s.do(ctx, func(ctx context.Context) error { return s.page.Back(ctx) })
}

// Eval evaluates expr and decodes the result into out only when no takeover
// happened meanwhile.
func (s *Session) Eval(ctx context.Context, expr string, out any) error {
	var raw json.RawMessage
	if err := s.do(ctx, func(ctx context.Context) error { return s.page.Eval(ctx, expr, &raw) }); err != nil {
		return err
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func (s *Session) Click(ctx context.Context, x, y float64) error {
	return s.do(ctx, func(ctx context.Context) error { return s.page.Click(ctx, x, y) })
}

func (s *Session) InsertText(ctx context.Context, text string) error {
	return s.do(ctx, func(ctx context.Context) error { return s.page.InsertText(ctx, text) })
}

func (s *Session) Key(ctx context.Context, key string) error {
	return s.do(ctx, func(ctx context.Context) error { return s.page.Key(ctx, key) })
}

func (s *Session) Scroll(ctx context.Context, x, y, dy float64) error {
	return s.do(ctx, func(ctx context.Context) error { return s.page.Scroll(ctx, x, y, dy) })
}

// Location returns the page URL and title.
func (s *Session) Location(ctx context.Context) (url, title string, err error) {
	var u, t string
	if err := s.do(ctx, func(ctx context.Context) error {
		var e error
		u, t, e = s.page.Location(ctx)
		return e
	}); err != nil {
		return "", "", err
	}
	return u, t, nil
}
