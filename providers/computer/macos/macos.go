// Package macos is computer-macos: the macOS provider of the `computer`
// capability (docs/computer-use.md).
//
// It sees through the accessibility API (AXUIElement): an outline of the
// target app's frontmost window with numbered elements, plus the menu bar.
// Screenshots capture that window alone with screencapture(1). It acts
// through element actions (AXPress, AXValue, menu items) and keyboard
// events posted to the app's process, both in the background, and through
// mouse events at global coordinates once the app is in front.
//
// The system calls go through purego (no cgo). On other systems the
// plugin registers but provides nothing.
package macos

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/computer"
	"github.com/hedeqiang/crewbase/kernel"
)

// Config is the plugin config.
type Config struct {
	// MaxNodes caps an outline (default 400).
	MaxNodes int `json:"maxNodes,omitempty"`
	// MaxEdge is the default long edge of screenshots (default 1280).
	MaxEdge int `json:"maxEdge,omitempty"`
	// MessagingTimeout bounds each accessibility call to an app (Go
	// duration, default 1s): a hung app cannot hang the agent.
	MessagingTimeout string `json:"messagingTimeout,omitempty"`
	// ObserveTimeout bounds a whole outline (default 4s); the outline is
	// truncated when it runs out.
	ObserveTimeout string `json:"observeTimeout,omitempty"`
}

// Plugin provides `computer` on macOS.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/computer-macos",
	Description: "Operates native macOS apps: accessibility outlines, window screenshots, element actions and events posted to the app (desktop only).",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		pf, err := newPlatform()
		if err != nil {
			c.Log().Debug("computer-macos: not available here", "err", err)
			return nil
		}
		p, err := New(pf, cfg)
		if err != nil {
			return err
		}
		c.Defer(p.Close)
		return c.Provide(computer.ServiceKey, p)
	},
})

// Provider implements computer.Computer.
type Provider struct {
	pf         platform
	maxNodes   int
	maxEdge    int
	msgTimeout time.Duration
	obsTimeout time.Duration
	settle     time.Duration // wait after an action before checking it

	mu     sync.Mutex
	apps   map[string]*appState // by bundle id
	closed bool
	// axTried: pids whose accessibility was switched on (or tried), so an
	// Electron app is switched once and other apps never
	axTried map[int]bool
	axWait  time.Duration
	axPoll  time.Duration
	sleep   func(time.Duration)

	// IdleFor: when the provider last posted events, and the person's
	// last input before that
	pmu              sync.Mutex
	postFrom, postTo time.Time
	humanAt          time.Time
}

// appState is the latest outline of an app and the lock serialising its
// actions.
type appState struct {
	mu      sync.Mutex
	pid     int
	appEl   ref
	window  ref
	winID   int
	frame   computer.Rect // global points
	version int64
	elems   []elemInfo // element N is elems[N-1]
}

// release drops the refs of the outline (st.mu held).
func (st *appState) release(pf platform) {
	for _, e := range st.elems {
		pf.Release(e.ref)
	}
	st.elems = nil
	if st.window != 0 {
		pf.Release(st.window)
		st.window = 0
	}
	if st.appEl != 0 {
		pf.Release(st.appEl)
		st.appEl = 0
	}
	st.version = 0
	st.winID = 0
	st.frame = computer.Rect{}
}

var _ computer.Computer = (*Provider)(nil)

// New returns a provider on pf.
func New(pf platform, cfg Config) (*Provider, error) {
	p := &Provider{pf: pf, maxNodes: cfg.MaxNodes, maxEdge: cfg.MaxEdge, msgTimeout: time.Second, obsTimeout: 4 * time.Second,
		settle: 200 * time.Millisecond, apps: map[string]*appState{},
		axTried: map[int]bool{}, axWait: axWait, axPoll: axPoll, sleep: time.Sleep}
	if p.maxNodes <= 0 {
		p.maxNodes = DefaultMaxNodes
	}
	if p.maxEdge <= 0 {
		p.maxEdge = DefaultMaxEdge
	}
	for _, d := range []struct {
		s   string
		dst *time.Duration
	}{{cfg.MessagingTimeout, &p.msgTimeout}, {cfg.ObserveTimeout, &p.obsTimeout}} {
		if d.s == "" {
			continue
		}
		v, err := time.ParseDuration(d.s)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("computer-macos: bad duration %q", d.s)
		}
		*d.dst = v
	}
	return p, nil
}

// Close releases every outline.
func (p *Provider) Close() {
	p.mu.Lock()
	p.closed = true
	apps := p.apps
	p.apps = map[string]*appState{}
	p.mu.Unlock()
	for _, st := range apps {
		st.mu.Lock()
		st.release(p.pf)
		st.mu.Unlock()
	}
}

// Permissions reports Accessibility and Screen Recording (never prompts).
func (p *Provider) Permissions() computer.Permissions {
	return computer.Permissions{Accessibility: p.pf.Trusted(), ScreenRecording: p.pf.ScreenCapture()}
}

// Apps lists running regular apps, frontmost first.
func (p *Provider) Apps(ctx context.Context) ([]computer.App, error) {
	return p.pf.Apps()
}

// IdleFor is the time since the last keyboard or mouse input.
//
// The events the provider posts count as input to the system too, so input
// that happened while the provider was posting (and just after) is taken
// as its own: the person's idle time then runs from their last input
// before it.
func (p *Provider) IdleFor() time.Duration {
	now := time.Now()
	last := now.Add(-p.rawIdle())
	p.pmu.Lock()
	defer p.pmu.Unlock()
	if p.ours(last) {
		return max(0, now.Sub(p.humanAt))
	}
	return now.Sub(last)
}

func (p *Provider) rawIdle() time.Duration {
	s := p.pf.IdleSeconds()
	if s < 0 {
		return 0
	}
	return time.Duration(s * float64(time.Second))
}

// ours: input at t falls in the provider's latest posting window (p.pmu
// held).
func (p *Provider) ours(t time.Time) bool {
	return !p.postFrom.IsZero() && !t.Before(p.postFrom.Add(-10*time.Millisecond)) &&
		(p.postTo.IsZero() || !t.After(p.postTo.Add(postEcho)))
}

// postEcho is how long after posting the system may still record it.
const postEcho = 150 * time.Millisecond

// posting marks the start of posting events; the returned func marks the
// end.
func (p *Provider) posting() func() {
	now := time.Now()
	last := now.Add(-p.rawIdle())
	p.pmu.Lock()
	if !p.ours(last) {
		p.humanAt = last
	}
	p.postFrom, p.postTo = now, time.Time{}
	p.pmu.Unlock()
	return func() {
		p.pmu.Lock()
		p.postTo = time.Now()
		p.pmu.Unlock()
	}
}

// resolve finds a running app by bundle id, else by name.
func (p *Provider) resolve(id string) (computer.App, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return computer.App{}, fmt.Errorf("no app given: %w", computer.ErrNoApp)
	}
	apps, err := p.pf.Apps()
	if err != nil {
		return computer.App{}, err
	}
	for _, a := range apps {
		if a.ID == id {
			return a, nil
		}
	}
	for _, a := range apps {
		if strings.EqualFold(a.ID, id) || strings.EqualFold(a.Name, id) {
			return a, nil
		}
	}
	return computer.App{}, fmt.Errorf("%s: %w", id, computer.ErrNoApp)
}

// state returns the app's state, locked; unlock with st.mu.Unlock.
func (p *Provider) state(app computer.App) (*appState, error) {
	key := app.ID
	if key == "" {
		key = fmt.Sprintf("pid:%d", app.PID)
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, errors.New("computer-macos: closed")
	}
	st := p.apps[key]
	if st == nil {
		st = &appState{}
		p.apps[key] = st
	}
	p.mu.Unlock()
	st.mu.Lock()
	if st.pid != app.PID {
		// relaunched (or first use): the old refs belong to a dead process
		st.release(p.pf)
		st.pid = app.PID
	}
	if st.appEl == 0 {
		el, err := p.pf.AppElement(app.PID)
		if err != nil {
			st.mu.Unlock()
			return nil, mapErr(err)
		}
		st.appEl = el
		p.pf.SetTimeout(el, p.msgTimeout.Seconds())
	}
	return st, nil
}

// mapErr turns platform errors into computer errors.
func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errAPIDisabled):
		return computer.ErrNoAccessibility
	case errors.Is(err, errInvalid):
		return computer.ErrStale
	}
	return err
}

// frontWindow returns the app's focused window, else its main window, else
// the first of AXWindows (retained).
func (p *Provider) frontWindow(appEl ref) (ref, error) {
	var firstErr error
	for _, a := range []string{"AXFocusedWindow", "AXMainWindow"} {
		w, err := p.pf.Attr(appEl, a)
		if err == nil && w != 0 {
			return w, nil
		}
		if firstErr == nil && err != nil && !errors.Is(err, errNoValue) {
			firstErr = err
		}
	}
	ws, err := p.pf.Elements(appEl, "AXWindows", 1)
	if err == nil && len(ws) > 0 {
		for _, w := range ws[1:] {
			p.pf.Release(w)
		}
		return ws[0], nil
	}
	if errors.Is(err, errAPIDisabled) || errors.Is(firstErr, errAPIDisabled) {
		return 0, computer.ErrNoAccessibility
	}
	return 0, computer.ErrNoApp
}

// Observe outlines the app's frontmost window.
func (p *Provider) Observe(ctx context.Context, appID string, maxNodes int) (computer.Observation, error) {
	if !p.pf.Trusted() {
		return computer.Observation{}, computer.ErrNoAccessibility
	}
	app, err := p.resolve(appID)
	if err != nil {
		return computer.Observation{}, err
	}
	st, err := p.state(app)
	if err != nil {
		return computer.Observation{}, err
	}
	defer st.mu.Unlock()
	if maxNodes <= 0 {
		maxNodes = p.maxNodes
	}
	deadline := time.Now().Add(p.obsTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}

	win, err := p.frontWindow(st.appEl)
	if err != nil {
		return computer.Observation{}, fmt.Errorf("%s: %w", app.Name, err)
	}
	raw, err := p.pf.Read(win)
	if err != nil {
		p.pf.Release(win)
		return computer.Observation{}, mapErr(err)
	}
	frame := computer.Rect{X: raw.X, Y: raw.Y, W: raw.W, H: raw.H}
	root, elems, truncated := buildOutline(p.pf, win, raw, frame, maxNodes, deadline)
	if p.claimManualAX(app.PID, len(elems), frame) && p.enableAX(st.appEl) != "" {
		// give the tree a moment to build, then read it again
		for waited := time.Duration(0); waited < p.axWait && ctx.Err() == nil && time.Now().Before(deadline); waited += p.axPoll {
			p.sleep(p.axPoll)
			if r, err := p.pf.Read(win); err == nil {
				raw = r
				frame = computer.Rect{X: raw.X, Y: raw.Y, W: raw.W, H: raw.H}
			}
			for _, e := range elems {
				p.pf.Release(e.ref)
			}
			root, elems, truncated = buildOutline(p.pf, win, raw, frame, maxNodes, deadline)
			if !sparse(len(elems), frame) {
				break
			}
		}
	}
	if err := ctx.Err(); err != nil {
		for _, e := range elems {
			p.pf.Release(e.ref)
		}
		p.pf.Release(win)
		return computer.Observation{}, err
	}
	var menus []computer.Element
	if bar, err := p.pf.Attr(st.appEl, "AXMenuBar"); err == nil {
		menus = buildMenus(p.pf, bar, deadline.Add(time.Second))
		p.pf.Release(bar)
	}

	// replace the previous outline
	for _, e := range st.elems {
		p.pf.Release(e.ref)
	}
	if st.window != 0 {
		p.pf.Release(st.window)
	}
	st.window, st.elems, st.frame = win, elems, frame
	st.winID = p.pf.WindowNumber(win, app.PID, frame)
	st.version = hashOutline(raw.Title, root, menus)

	return computer.Observation{
		App: app,
		Window: computer.Window{
			ID:     st.winID,
			Title:  raw.Title,
			Frame:  frame,
			Main:   raw.Main,
			Screen: screenOf(p.pf.Screens(), frame),
		},
		Root:      root,
		Menus:     menus,
		Version:   st.version,
		Truncated: truncated,
		Time:      time.Now(),
	}, nil
}

// Screenshot captures the app's frontmost window.
func (p *Provider) Screenshot(ctx context.Context, appID string, maxEdge int) (computer.Screenshot, error) {
	if !p.pf.ScreenCapture() {
		return computer.Screenshot{}, computer.ErrNoScreenRecording
	}
	if maxEdge <= 0 {
		maxEdge = p.maxEdge
	}
	app, err := p.resolve(appID)
	if err != nil {
		return computer.Screenshot{}, err
	}
	wins, err := p.pf.Windows(app.PID)
	if err != nil {
		return computer.Screenshot{}, err
	}
	// the window of the latest outline, or the app's frontmost window
	target := computer.Rect{}
	if p.pf.Trusted() {
		if st, err := p.state(app); err == nil {
			if st.window == 0 {
				if w, err := p.frontWindow(st.appEl); err == nil {
					st.window = w // outline-less: kept for the next screenshot only
					st.version = 0
				}
			}
			if st.window != 0 {
				if raw, err := p.pf.Read(st.window); err == nil {
					target = computer.Rect{X: raw.X, Y: raw.Y, W: raw.W, H: raw.H}
				}
			}
			st.mu.Unlock()
		}
	}
	w, ok := matchWindow(wins, target)
	if !ok {
		return computer.Screenshot{}, fmt.Errorf("%s has no window on screen (minimized, hidden or on another Space): %w", app.Name, computer.ErrNoApp)
	}
	f, err := os.CreateTemp("", "crewbase-window-*.png")
	if err != nil {
		return computer.Screenshot{}, err
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)
	if err := p.pf.Capture(w.ID, path); err != nil {
		return computer.Screenshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return computer.Screenshot{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return computer.Screenshot{}, err
	}
	if len(data) == 0 {
		return computer.Screenshot{}, computer.ErrNoScreenRecording
	}
	out, pw, ph, scale, err := fitPNG(data, w.Frame.W, maxEdge)
	if err != nil {
		return computer.Screenshot{}, fmt.Errorf("computer-macos: screenshot: %w", err)
	}
	return computer.Screenshot{PNG: out, Width: pw, Height: ph, Scale: scale}, nil
}
