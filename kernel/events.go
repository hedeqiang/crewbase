package kernel

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Dispatcher is anything events can be dispatched through: a *Context, a
// *Host, or a scoped dispatcher from For. The dispatch scope decides which
// listeners receive the event: "" reaches every listener; an agent id reaches
// global listeners plus that agent's own listeners (scoped dispatch).
type Dispatcher interface {
	dispatch() (*Host, string)
}

func (c *Context) dispatch() (*Host, string) { return c.host, c.scope }
func (h *Host) dispatch() (*Host, string)    { return h, "" }

type scoped struct {
	h     *Host
	scope string
}

func (s scoped) dispatch() (*Host, string) { return s.h, s.scope }

// For dispatches on behalf of an agent scope (e.g. a global plugin reporting
// an agent's activity).
func For(d Dispatcher, scope string) Dispatcher {
	h, _ := d.dispatch()
	return scoped{h: h, scope: scope}
}

type listener struct {
	id      uint64
	scope   string
	prepend bool
	fn      any
	owner   string
}

var (
	eventNamesMu sync.Mutex
	eventNames   = map[string]string{}
)

func registerEventName(name, mode string) {
	eventNamesMu.Lock()
	defer eventNamesMu.Unlock()
	if prev, ok := eventNames[name]; ok {
		panic(fmt.Sprintf("kernel: event %q already defined (%s)", name, prev))
	}
	eventNames[name] = mode
}

// EventNames returns every defined event and its dispatch mode.
func EventNames() map[string]string {
	eventNamesMu.Lock()
	defer eventNamesMu.Unlock()
	out := make(map[string]string, len(eventNames))
	for k, v := range eventNames {
		out[k] = v
	}
	return out
}

func addListener(c *Context, name string, fn any, prepend bool) {
	h := c.host
	h.mu.Lock()
	h.seq++
	l := &listener{id: h.seq, scope: c.scope, prepend: prepend, fn: fn, owner: c.PluginID()}
	if prepend {
		h.listeners[name] = append([]*listener{l}, h.listeners[name]...)
	} else {
		h.listeners[name] = append(h.listeners[name], l)
	}
	h.mu.Unlock()
	c.Defer(func() {
		h.mu.Lock()
		ls := h.listeners[name]
		for i, x := range ls {
			if x == l {
				h.listeners[name] = append(ls[:i:i], ls[i+1:]...)
				break
			}
		}
		h.mu.Unlock()
	})
}

func snapshot(d Dispatcher, name string) []*listener {
	h, scope := d.dispatch()
	h.mu.Lock()
	defer h.mu.Unlock()
	all := h.listeners[name]
	out := make([]*listener, 0, len(all))
	for _, l := range all {
		if scope == "" || l.scope == "" || l.scope == scope {
			out = append(out, l)
		}
	}
	return out
}

func logPanic(d Dispatcher, name string, l *listener, r any) {
	h, _ := d.dispatch()
	h.log.Error("event listener panic", "event", name, "plugin", l.owner, "panic", r)
}

// ---------------------------------------------------------------- Emit

// EmitEvent is a fire-and-forget notification observed in registration order.
type EmitEvent[P any] struct{ name string }

// NewEmit defines an Emit event.
func NewEmit[P any](name string) *EmitEvent[P] {
	registerEventName(name, "emit")
	return &EmitEvent[P]{name: name}
}

func (e *EmitEvent[P]) Name() string { return e.name }

// Listen registers an observer bound to ctx.
func (e *EmitEvent[P]) Listen(c *Context, fn func(P)) { addListener(c, e.name, fn, false) }

// Emit notifies observers synchronously; a panicking observer is isolated.
func (e *EmitEvent[P]) Emit(d Dispatcher, p P) {
	for _, l := range snapshot(d, e.name) {
		func() {
			defer func() {
				if r := recover(); r != nil {
					logPanic(d, e.name, l, r)
				}
			}()
			l.fn.(func(P))(p)
		}()
	}
}

// ---------------------------------------------------------------- Waterfall

// Next calls the downstream middleware (or the terminal handler).
type Next[A, R any] func(ctx context.Context, a A) (R, error)

// Middleware wraps downstream. Returning without calling next short-circuits.
type Middleware[A, R any] func(ctx context.Context, a A, next Next[A, R]) (R, error)

// WaterfallEvent is an around-middleware chain. The first registered
// listener is outermost; Prepend listeners run before all normal ones.
type WaterfallEvent[A, R any] struct{ name string }

// NewWaterfall defines a Waterfall event.
func NewWaterfall[A, R any](name string) *WaterfallEvent[A, R] {
	registerEventName(name, "waterfall")
	return &WaterfallEvent[A, R]{name: name}
}

func (e *WaterfallEvent[A, R]) Name() string { return e.name }

// Use appends a middleware.
func (e *WaterfallEvent[A, R]) Use(c *Context, fn Middleware[A, R]) {
	addListener(c, e.name, fn, false)
}

// Prepend inserts a middleware ahead of all normally registered ones.
func (e *WaterfallEvent[A, R]) Prepend(c *Context, fn Middleware[A, R]) {
	addListener(c, e.name, fn, true)
}

// Run executes the chain ending in terminal. A panic in any middleware is
// returned as an error.
func (e *WaterfallEvent[A, R]) Run(d Dispatcher, ctx context.Context, a A, terminal Next[A, R]) (R, error) {
	ls := snapshot(d, e.name)
	var call func(i int) Next[A, R]
	call = func(i int) Next[A, R] {
		return func(ctx context.Context, a A) (r R, err error) {
			if i >= len(ls) {
				if terminal == nil {
					return r, nil
				}
				return terminal(ctx, a)
			}
			l := ls[i]
			defer func() {
				if rec := recover(); rec != nil {
					logPanic(d, e.name, l, rec)
					err = fmt.Errorf("%s: listener %s panicked: %v", e.name, l.owner, rec)
				}
			}()
			return l.fn.(Middleware[A, R])(ctx, a, call(i+1))
		}
	}
	return call(0)(ctx, a)
}

// ---------------------------------------------------------------- Parallel

// ParallelEvent fans out to every listener concurrently and waits. Errors
// and panics are isolated per listener and returned joined.
type ParallelEvent[P any] struct{ name string }

// NewParallel defines a Parallel event.
func NewParallel[P any](name string) *ParallelEvent[P] {
	registerEventName(name, "parallel")
	return &ParallelEvent[P]{name: name}
}

func (e *ParallelEvent[P]) Name() string { return e.name }

func (e *ParallelEvent[P]) Listen(c *Context, fn func(context.Context, P) error) {
	addListener(c, e.name, fn, false)
}

func (e *ParallelEvent[P]) Run(d Dispatcher, ctx context.Context, p P) error {
	ls := snapshot(d, e.name)
	errs := make([]error, len(ls))
	var wg sync.WaitGroup
	for i, l := range ls {
		wg.Add(1)
		go func(i int, l *listener) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					logPanic(d, e.name, l, r)
					errs[i] = fmt.Errorf("%s: listener %s panicked: %v", e.name, l.owner, r)
				}
			}()
			errs[i] = l.fn.(func(context.Context, P) error)(ctx, p)
		}(i, l)
	}
	wg.Wait()
	return errors.Join(errs...)
}

// ---------------------------------------------------------------- Serial

// SerialEvent awaits listeners one after another and collects their results.
// The first error stops the chain.
type SerialEvent[P, R any] struct{ name string }

// NewSerial defines a Serial event.
func NewSerial[P, R any](name string) *SerialEvent[P, R] {
	registerEventName(name, "serial")
	return &SerialEvent[P, R]{name: name}
}

func (e *SerialEvent[P, R]) Name() string { return e.name }

func (e *SerialEvent[P, R]) Listen(c *Context, fn func(context.Context, P) (R, error)) {
	addListener(c, e.name, fn, false)
}

func (e *SerialEvent[P, R]) Run(d Dispatcher, ctx context.Context, p P) (out []R, err error) {
	for _, l := range snapshot(d, e.name) {
		r, err := func() (r R, err error) {
			defer func() {
				if rec := recover(); rec != nil {
					logPanic(d, e.name, l, rec)
					err = fmt.Errorf("%s: listener %s panicked: %v", e.name, l.owner, rec)
				}
			}()
			return l.fn.(func(context.Context, P) (R, error))(ctx, p)
		}()
		if err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, nil
}

// ---------------------------------------------------------------- Bail

// BailEvent asks listeners in order until one returns ok=true ("who can
// handle this?").
type BailEvent[P, R any] struct{ name string }

// NewBail defines a Bail event.
func NewBail[P, R any](name string) *BailEvent[P, R] {
	registerEventName(name, "bail")
	return &BailEvent[P, R]{name: name}
}

func (e *BailEvent[P, R]) Name() string { return e.name }

func (e *BailEvent[P, R]) Listen(c *Context, fn func(context.Context, P) (R, bool)) {
	addListener(c, e.name, fn, false)
}

func (e *BailEvent[P, R]) Prepend(c *Context, fn func(context.Context, P) (R, bool)) {
	addListener(c, e.name, fn, true)
}

// Run returns the first handled result; ok=false when nobody handled it.
// A panicking listener counts as not handling.
func (e *BailEvent[P, R]) Run(d Dispatcher, ctx context.Context, p P) (R, bool) {
	for _, l := range snapshot(d, e.name) {
		r, ok := func() (r R, ok bool) {
			defer func() {
				if rec := recover(); rec != nil {
					logPanic(d, e.name, l, rec)
					ok = false
				}
			}()
			return l.fn.(func(context.Context, P) (R, bool))(ctx, p)
		}()
		if ok {
			return r, true
		}
	}
	var zero R
	return zero, false
}
