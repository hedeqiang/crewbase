// Package kernel is the Crewbase plugin host: the only non-replaceable part
// of the system. It provides a service registry keyed by stable names,
// dependency resolution with PENDING state, typed events with five dispatch
// modes, reversible effects, and agent scopes. See docs/05-plugin-system.md.
package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
)

// State is the lifecycle state of a loaded plugin.
type State string

const (
	StatePending  State = "PENDING"
	StateActive   State = "ACTIVE"
	StateFailed   State = "FAILED"
	StateDisposed State = "DISPOSED"
)

// Spec declares a plugin. Apply runs once every Inject key is available and
// registers services, listeners and effects on ctx. Everything registered
// through ctx is undone, in reverse order, when the plugin unloads.
type Spec struct {
	Name     string
	Inject   []string
	Optional []string // services used if present; never block activation
	Apply    func(ctx *Context) error
	// Description is shown by `crew plugins`.
	Description string
}

// Define is a readability helper mirroring the docs: kernel.Define(kernel.Spec{...}).
func Define(s Spec) Spec { return s }

// ErrServiceTaken is returned when a second provider registers a key that is
// already provided in the same namespace (extension rule 1).
var ErrServiceTaken = errors.New("service key already provided")

type service struct {
	value any
	owner *Plugin
}

// Host is a running plugin tree.
type Host struct {
	mu        sync.Mutex
	services  map[string]*service
	plugins   []*Plugin
	listeners map[string][]*listener
	seq       uint64
	log       *slog.Logger
	root      *Context

	reconciling bool
	dirty       bool
}

// NewHost creates an empty host. logger may be nil.
func NewHost(logger *slog.Logger) *Host {
	if logger == nil {
		logger = slog.Default()
	}
	h := &Host{
		services:  map[string]*service{},
		listeners: map[string][]*listener{},
		log:       logger,
	}
	h.root = newContext(h, nil, nil, "")
	return h
}

// Root returns the host's root context. Registrations on it live until Close.
func (h *Host) Root() *Context { return h.root }

// Log returns the host logger.
func (h *Host) Log() *slog.Logger { return h.log }

// LoadOptions configure a single plugin entry.
type LoadOptions struct {
	ID     string         // stable entry id; defaults to Spec.Name
	Config map[string]any // raw config, decoded by the plugin
	// Isolate gives this plugin a private namespace for the listed service
	// keys (an "isolation group"). Plugins sharing the same Group name see
	// each other's isolated services; others do not.
	Isolate []string
	Group   string
}

// Plugin is a loaded plugin entry.
type Plugin struct {
	host      *Host
	spec      Spec
	opts      LoadOptions
	state     State
	err       error
	ctx       *Context
	provides  []string
	origin    Entry
	fromEntry bool
}

// ID returns the stable entry id.
func (p *Plugin) ID() string { return p.opts.ID }

// Load adds a plugin to the tree. It activates immediately if its
// dependencies are available, otherwise it stays PENDING.
func (h *Host) Load(spec Spec, opts LoadOptions) (*Plugin, error) {
	if spec.Name == "" {
		return nil, errors.New("kernel: plugin spec has no name")
	}
	if opts.ID == "" {
		opts.ID = spec.Name
	}
	h.mu.Lock()
	for _, p := range h.plugins {
		if p.opts.ID == opts.ID {
			h.mu.Unlock()
			return nil, fmt.Errorf("kernel: plugin id %q already loaded", opts.ID)
		}
	}
	p := &Plugin{host: h, spec: spec, opts: opts, state: StatePending}
	h.plugins = append(h.plugins, p)
	h.dirty = true
	h.mu.Unlock()
	p.notify("")
	h.reconcile()
	return p, nil
}

// Unload disposes a plugin and removes it from the tree.
func (h *Host) Unload(id string) bool {
	h.mu.Lock()
	var target *Plugin
	for i, p := range h.plugins {
		if p.opts.ID == id {
			target = p
			h.plugins = append(h.plugins[:i:i], h.plugins[i+1:]...)
			break
		}
	}
	h.mu.Unlock()
	if target == nil {
		return false
	}
	target.deactivate(StateDisposed)
	h.markDirty()
	h.reconcile()
	return true
}

// Close unloads every plugin in reverse load order, then disposes the root.
func (h *Host) Close() {
	h.mu.Lock()
	ps := append([]*Plugin(nil), h.plugins...)
	h.plugins = nil
	h.mu.Unlock()
	for i := len(ps) - 1; i >= 0; i-- {
		ps[i].deactivate(StateDisposed)
	}
	h.root.Dispose()
}

func (h *Host) markDirty() {
	h.mu.Lock()
	h.dirty = true
	h.mu.Unlock()
}

// reconcile activates PENDING plugins whose deps are satisfied and
// deactivates ACTIVE plugins that lost a dependency, until stable.
// It is non-reentrant: nested calls (e.g. Provide inside Apply) only mark
// the tree dirty and the outer loop picks the change up.
func (h *Host) reconcile() {
	h.mu.Lock()
	if h.reconciling {
		h.dirty = true
		h.mu.Unlock()
		return
	}
	h.reconciling = true
	defer func() {
		h.mu.Lock()
		h.reconciling = false
		h.mu.Unlock()
	}()
	for h.dirty {
		h.dirty = false
		ps := append([]*Plugin(nil), h.plugins...)
		h.mu.Unlock()

		changed := false
		for _, p := range ps {
			h.mu.Lock()
			st := p.state
			missing := p.missingLocked()
			h.mu.Unlock()
			switch {
			case st == StateActive && len(missing) > 0:
				h.log.Debug("plugin lost dependency", "plugin", p.opts.ID, "missing", missing)
				p.deactivate(StatePending)
				changed = true
			case st == StatePending && len(missing) == 0:
				p.activate()
				changed = true
			}
		}
		h.mu.Lock()
		if changed {
			h.dirty = true
		}
	}
	h.mu.Unlock()
}

func (p *Plugin) nsKey(key string) string {
	for _, k := range p.opts.Isolate {
		if k == key {
			return key + "@" + p.opts.Group
		}
	}
	return key
}

func (p *Plugin) missingLocked() []string {
	var missing []string
	for _, k := range p.spec.Inject {
		if _, ok := p.host.services[p.nsKey(k)]; !ok {
			missing = append(missing, k)
		}
	}
	return missing
}

func (p *Plugin) activate() {
	p.host.mu.Lock()
	from := p.state
	p.host.mu.Unlock()
	defer p.notify(from)
	ctx := newContext(p.host, p.host.root, p, "")
	p.host.mu.Lock()
	p.ctx = ctx
	p.provides = nil
	p.err = nil
	p.host.mu.Unlock()

	err := safeCall(func() error {
		if p.spec.Apply == nil {
			return nil
		}
		return p.spec.Apply(ctx)
	})
	if err != nil {
		p.host.log.Warn("plugin failed", "plugin", p.opts.ID, "err", err)
		ctx.Dispose()
		p.host.mu.Lock()
		p.state, p.err, p.ctx = StateFailed, err, nil
		p.host.mu.Unlock()
		return
	}
	p.host.mu.Lock()
	p.state = StateActive
	p.host.mu.Unlock()
}

func (p *Plugin) deactivate(to State) {
	p.host.mu.Lock()
	from := p.state
	ctx := p.ctx
	p.ctx = nil
	p.state = to
	p.host.mu.Unlock()
	if ctx != nil {
		ctx.Dispose()
	}
	p.notify(from)
}

func safeCall(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return fn()
}

// PluginInfo is a snapshot of one plugin for diagnostics.
type PluginInfo struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	State    State    `json:"state"`
	Missing  []string `json:"missing,omitempty"`
	Provides []string `json:"provides,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// Fail marks the plugin owning c as FAILED with err and disposes it
// asynchronously (it is not reactivated until reloaded). Use it from a
// running plugin, e.g. a supervisor giving up after repeated crashes; in
// Apply, return the error instead.
func (c *Context) Fail(err error) {
	p := c.ownerPlugin()
	if p == nil || c.Disposed() {
		return
	}
	root := c
	for root.plugin == nil && root.parent != nil {
		root = root.parent
	}
	p.host.mu.Lock()
	current := p.ctx == root
	p.host.mu.Unlock()
	if !current {
		return // a previous activation: never tear down the new one
	}
	go func() {
		p.host.mu.Lock()
		p.err = err
		p.host.mu.Unlock()
		p.deactivate(StateFailed)
	}()
}

// PluginEvent reports a plugin lifecycle change.
type PluginEvent struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	From  State  `json:"from,omitempty"` // "" when loaded
	To    State  `json:"to"`             // DISPOSED when unloaded
	Error string `json:"error,omitempty"`
}

// PluginChanged is emitted on every plugin state transition (load,
// activate, pending, failed, unload). Listeners must not block.
var PluginChanged = NewEmit[PluginEvent]("kernel/plugin-changed")

func (p *Plugin) notify(from State) {
	p.host.mu.Lock()
	ev := PluginEvent{ID: p.opts.ID, Name: p.spec.Name, From: from, To: p.state}
	if p.err != nil {
		ev.Error = p.err.Error()
	}
	p.host.mu.Unlock()
	if ev.From != ev.To {
		PluginChanged.Emit(p.host, ev)
	}
}

// Plugins lists loaded plugins in load order.
func (h *Host) Plugins() []PluginInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]PluginInfo, 0, len(h.plugins))
	for _, p := range h.plugins {
		info := PluginInfo{ID: p.opts.ID, Name: p.spec.Name, State: p.state, Provides: append([]string(nil), p.provides...)}
		if p.state == StatePending {
			info.Missing = p.missingLocked()
		}
		if p.err != nil {
			info.Error = p.err.Error()
		}
		out = append(out, info)
	}
	return out
}

// Plugin returns a loaded plugin's info by id.
func (h *Host) Plugin(id string) (PluginInfo, bool) {
	for _, p := range h.Plugins() {
		if p.ID == id {
			return p, true
		}
	}
	return PluginInfo{}, false
}

// Services lists provided service keys, sorted.
func (h *Host) Services() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	keys := make([]string, 0, len(h.services))
	for k := range h.services {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Context is a service container handed to plugins. Every registration made
// through it is recorded and undone on Dispose.
type Context struct {
	host   *Host
	parent *Context
	plugin *Plugin
	scope  string

	mu        sync.Mutex
	disposers []func()
	children  map[*Context]struct{}
	disposed  bool

	goctx  context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newContext(h *Host, parent *Context, p *Plugin, scope string) *Context {
	base := context.Background()
	if parent != nil {
		base = parent.goctx
	}
	gctx, cancel := context.WithCancel(base)
	c := &Context{host: h, parent: parent, plugin: p, scope: scope, goctx: gctx, cancel: cancel}
	if parent != nil {
		parent.mu.Lock()
		if parent.children == nil {
			parent.children = map[*Context]struct{}{}
		}
		parent.children[c] = struct{}{}
		parent.mu.Unlock()
	}
	return c
}

// Host returns the owning host.
func (c *Context) Host() *Host { return c.host }

// Scope returns "" for global contexts or the agent id for agent scopes.
func (c *Context) Scope() string { return c.scope }

// PluginID returns the id of the plugin that owns this context, if any.
func (c *Context) PluginID() string {
	if p := c.ownerPlugin(); p != nil {
		return p.opts.ID
	}
	return ""
}

func (c *Context) ownerPlugin() *Plugin {
	for x := c; x != nil; x = x.parent {
		if x.plugin != nil {
			return x.plugin
		}
	}
	return nil
}

// Log returns a logger tagged with the plugin id.
func (c *Context) Log() *slog.Logger {
	if id := c.PluginID(); id != "" {
		return c.host.log.With("plugin", id)
	}
	return c.host.log
}

// Context returns a context.Context cancelled when this Context disposes.
func (c *Context) Context() context.Context { return c.goctx }

// Config returns the raw plugin config (never nil).
func (c *Context) Config() map[string]any {
	if p := c.ownerPlugin(); p != nil && p.opts.Config != nil {
		return p.opts.Config
	}
	return map[string]any{}
}

// Defer records a disposer. If the context is already disposed it runs now.
func (c *Context) Defer(dispose func()) {
	if dispose == nil {
		return
	}
	c.mu.Lock()
	if c.disposed {
		c.mu.Unlock()
		dispose()
		return
	}
	c.disposers = append(c.disposers, dispose)
	c.mu.Unlock()
}

// Effect runs setup now and records the returned disposer.
func (c *Context) Effect(setup func() (dispose func())) {
	c.Defer(setup())
}

// Go runs fn in a goroutine bound to this context: on dispose its
// context.Context is cancelled and Dispose waits for it to return.
func (c *Context) Go(fn func(ctx context.Context)) {
	c.mu.Lock()
	if c.disposed {
		c.mu.Unlock()
		return
	}
	c.wg.Add(1)
	c.mu.Unlock()
	go func() {
		defer c.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				c.Log().Error("goroutine panic", "panic", r)
			}
		}()
		fn(c.goctx)
	}()
}

// Fork creates a child context. scope "" keeps the parent's scope. The child
// is disposed with its parent and can be disposed earlier on its own.
func (c *Context) Fork(scope string) *Context {
	if scope == "" {
		scope = c.scope
	}
	child := newContext(c.host, c, nil, scope)
	return child
}

// Disposed reports whether Dispose has run.
func (c *Context) Disposed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.disposed
}

// Dispose cancels goroutines, disposes children, then runs disposers in
// reverse registration order. Safe to call more than once.
func (c *Context) Dispose() {
	c.mu.Lock()
	if c.disposed {
		c.mu.Unlock()
		return
	}
	c.disposed = true
	children := make([]*Context, 0, len(c.children))
	for ch := range c.children {
		children = append(children, ch)
	}
	ds := c.disposers
	c.disposers = nil
	c.mu.Unlock()

	c.cancel()
	c.wg.Wait()
	for _, ch := range children {
		ch.Dispose()
	}
	for i := len(ds) - 1; i >= 0; i-- {
		func() {
			defer func() {
				if r := recover(); r != nil {
					c.Log().Error("disposer panic", "panic", r)
				}
			}()
			ds[i]()
		}()
	}
	if c.parent != nil {
		c.parent.mu.Lock()
		delete(c.parent.children, c)
		c.parent.mu.Unlock()
	}
}

func (c *Context) resolveKey(key string) string {
	if p := c.ownerPlugin(); p != nil {
		return p.nsKey(key)
	}
	return key
}

// Provide registers a service under key. Only one provider per key may exist;
// a second registration fails with ErrServiceTaken. The service is removed
// when this context disposes, which deactivates dependents (they go PENDING).
func (c *Context) Provide(key string, value any) error {
	if c.Disposed() {
		return errors.New("kernel: provide on disposed context")
	}
	nk := c.resolveKey(key)
	h := c.host
	h.mu.Lock()
	if _, ok := h.services[nk]; ok {
		h.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrServiceTaken, key)
	}
	s := &service{value: value, owner: c.ownerPlugin()}
	h.services[nk] = s
	if s.owner != nil {
		s.owner.provides = append(s.owner.provides, key)
	}
	h.dirty = true
	h.mu.Unlock()

	c.Defer(func() {
		h.mu.Lock()
		if cur, ok := h.services[nk]; ok && cur == s {
			delete(h.services, nk)
			h.dirty = true
		}
		h.mu.Unlock()
		h.reconcile()
	})
	h.reconcile()
	return nil
}

// MustProvide is Provide that panics on error (used inside Apply, where the
// panic is converted into a FAILED state).
func (c *Context) MustProvide(key string, value any) {
	if err := c.Provide(key, value); err != nil {
		panic(err)
	}
}

func (c *Context) lookup(key string) (any, bool) {
	nk := c.resolveKey(key)
	c.host.mu.Lock()
	defer c.host.mu.Unlock()
	s, ok := c.host.services[nk]
	if !ok {
		return nil, false
	}
	return s.value, true
}

// Lookup finds a service by key and asserts its type.
func Lookup[T any](c *Context, key string) (T, bool) {
	var zero T
	v, ok := c.lookup(key)
	if !ok {
		return zero, false
	}
	t, ok := v.(T)
	return t, ok
}

// Use finds a service that the plugin declared in Inject. It panics when the
// service is missing or of the wrong type: that is a programming error.
func Use[T any](c *Context, key string) T {
	v, ok := c.lookup(key)
	if !ok {
		panic(fmt.Sprintf("kernel: service %q not available (declare it in Inject)", key))
	}
	t, ok := v.(T)
	if !ok {
		panic(fmt.Sprintf("kernel: service %q has type %T", key, v))
	}
	return t
}

// String helps debugging.
func (c *Context) String() string {
	var b strings.Builder
	b.WriteString("Context(")
	if id := c.PluginID(); id != "" {
		b.WriteString(id)
	}
	if c.scope != "" {
		b.WriteString(" scope=" + c.scope)
	}
	b.WriteString(")")
	return b.String()
}
