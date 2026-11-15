// Package chrome is browser-chrome: the local Chrome / Chromium provider of
// the `browser` capability (docs/04 "browser pool").
//
//   - One browser per agent, started on demand, with its own profile in
//     <agent workspace>/browser (cookies and logins persist across restarts;
//     login state is never restored from logs).
//   - Idle browsers are closed after idleTimeout (default 10m) — gracefully,
//     so the profile is flushed — and restarted on the next use. A browser a
//     person has taken over is never reclaimed.
//   - Chrome runs in its own process group; the whole tree is killed when
//     the browser is released or the plugin unloads.
//   - Attached mode (config "attach": a DevTools ws/http URL) connects to a
//     browser that is already running. One agent at a time owns it, the
//     agent works in its own tab, and cleanup only disconnects.
//
// Headless browsers are taken over frame by frame: the takeover UI shows
// Screenshot frames and sends Input (State.View = "frames"); with
// headless=false the window is on the desktop (State.View = "window").
package chrome

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto"
	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"

	"github.com/hedeqiang/crewbase/capabilities/browser"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/kernel"
)

// Defaults.
const (
	DefaultIdleTimeout  = 10 * time.Minute
	DefaultStartTimeout = 30 * time.Second
	DefaultWidth        = 1280
	DefaultHeight       = 800
)

// Config is the plugin config.
type Config struct {
	// Executable is the browser binary; auto-detected when empty.
	Executable string `json:"executable,omitempty"`
	// Headless defaults to true.
	Headless *bool `json:"headless,omitempty"`
	// IdleTimeout closes unused browsers (Go duration, default 10m; "0"
	// disables reclaiming).
	IdleTimeout string `json:"idleTimeout,omitempty"`
	// StartTimeout bounds a browser start (default 30s).
	StartTimeout string `json:"startTimeout,omitempty"`
	// Attach connects to a running browser (ws://… or http://host:port)
	// instead of launching one.
	Attach string `json:"attach,omitempty"`
	// Args are extra command-line flags.
	Args []string `json:"args,omitempty"`
	// Width / Height of the window (default 1280×800).
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
	// ProfileRoot is used when the agents service is unavailable: the
	// profile of agent X is <profileRoot>/X.
	ProfileRoot string `json:"profileRoot,omitempty"`
}

// Chrome implements browser.Browser.
type Chrome struct {
	exe      string
	attach   string
	headless bool
	idle     time.Duration
	start    time.Duration
	width    int
	height   int
	args     []string
	ctl      *browser.Control
	profile  func(agentID string) (string, error)
	changed  func(browser.State)

	mu     sync.Mutex
	slots  map[string]*slot
	closed bool
	owner  string // attached mode: agent that owns the browser
	fmu    sync.Once
	fs     *filterState
	// fakeWindow (tests): a "window" launch stays headless, so no window
	// pops up, but everything else of a switch happens.
	fakeWindow bool
	// windowErr (tests): a window launch fails with it.
	windowErr error
}

type slot struct {
	mu     sync.Mutex // serializes start / stop
	inst   atomic.Pointer[instance]
	target target.ID // attached mode: the agent's tab, reused on reconnect
	// window: a takeover moved the browser to a desktop window (window.go).
	window atomic.Bool
	// carry: tabs (and session cookies) to restore at the next start: the
	// last known state of a window takeover, or of a switch that failed.
	carry *carry
}

type instance struct {
	agent    string
	dir      string
	attached bool
	headless bool
	frames   *frameConn // iframes (nil if unavailable)
	cmd      *exec.Cmd
	exited   chan struct{}
	stderr   *tailBuf
	wsURL    string

	allocCancel   context.CancelFunc
	browserCtx    context.Context
	browserCancel context.CancelFunc
	tabCtx        context.Context
	tabCancel     context.CancelFunc
	page          *cdpPage
	last          atomic.Int64
}

func (in *instance) touch() { in.last.Store(time.Now().UnixNano()) }

func (in *instance) alive() bool {
	if in == nil || in.tabCtx.Err() != nil {
		return false
	}
	if in.exited != nil {
		select {
		case <-in.exited:
			return false
		default:
		}
	}
	return true
}

// Options wire a Chrome to its host.
type Options struct {
	// Dispatcher for the takeover events (may be nil).
	Dispatcher kernel.Dispatcher
	// Profile resolves an agent's profile directory.
	Profile func(agentID string) (string, error)
	// Changed is called on every state change (may be nil).
	Changed func(browser.State)
}

var agentIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// New creates a provider. It fails when no browser executable is found
// (unless attaching).
func New(cfg Config, o Options) (*Chrome, error) {
	c := &Chrome{exe: cfg.Executable, attach: cfg.Attach, headless: true, idle: DefaultIdleTimeout,
		start: DefaultStartTimeout, width: cfg.Width, height: cfg.Height, args: cfg.Args,
		ctl: browser.NewControl(o.Dispatcher), profile: o.Profile, changed: o.Changed, slots: map[string]*slot{}}
	if cfg.Headless != nil {
		c.headless = *cfg.Headless
	}
	if c.width <= 0 {
		c.width = DefaultWidth
	}
	if c.height <= 0 {
		c.height = DefaultHeight
	}
	var err error
	if cfg.IdleTimeout != "" {
		if c.idle, err = time.ParseDuration(cfg.IdleTimeout); err != nil {
			return nil, fmt.Errorf("browser-chrome: idleTimeout: %w", err)
		}
	}
	if cfg.StartTimeout != "" {
		if c.start, err = time.ParseDuration(cfg.StartTimeout); err != nil {
			return nil, fmt.Errorf("browser-chrome: startTimeout: %w", err)
		}
	}
	if c.attach != "" {
		u, err := url.Parse(c.attach)
		if err != nil || (u.Scheme != "ws" && u.Scheme != "wss" && u.Scheme != "http" && u.Scheme != "https") {
			return nil, fmt.Errorf("browser-chrome: attach must be a ws:// or http:// DevTools URL, got %q", c.attach)
		}
		c.headless = false
	} else {
		if c.exe == "" {
			c.exe = FindExecutable()
		}
		if c.exe == "" {
			return nil, errors.New("browser-chrome: no Chrome/Chromium executable found; install Google Chrome or Chromium, or set config \"executable\" (or CREWBASE_BROWSER)")
		}
		if !isExecutable(c.exe) {
			return nil, fmt.Errorf("browser-chrome: executable %q does not exist or is not executable", c.exe)
		}
	}
	if c.profile == nil {
		dir := cfg.ProfileRoot
		if dir == "" {
			dir = filepath.Join(os.TempDir(), "crewbase-browser")
		}
		c.profile = func(id string) (string, error) { return filepath.Join(dir, id), nil }
	}
	if c.changed == nil {
		c.changed = func(browser.State) {}
	}
	return c, nil
}

// Executable returns the browser binary in use ("" in attached mode).
func (c *Chrome) Executable() string { return c.exe }

// ProfileDir returns the agent's profile directory.
func (c *Chrome) ProfileDir(agentID string) (string, error) {
	if !agentIDRe.MatchString(agentID) {
		return "", fmt.Errorf("browser-chrome: invalid agent id %q", agentID)
	}
	return c.profile(agentID)
}

func (c *Chrome) slot(agentID string) (*slot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, browser.ErrClosed
	}
	s := c.slots[agentID]
	if s == nil {
		s = &slot{}
		c.slots[agentID] = s
	}
	return s, nil
}

func (c *Chrome) peek(agentID string) *instance {
	c.mu.Lock()
	s := c.slots[agentID]
	c.mu.Unlock()
	if s == nil {
		return nil
	}
	return s.inst.Load()
}

// Session implements browser.Browser.
func (c *Chrome) Session(ctx context.Context, agentID string) (*browser.Session, error) {
	if !agentIDRe.MatchString(agentID) {
		return nil, fmt.Errorf("browser-chrome: invalid agent id %q", agentID)
	}
	if by, _ := c.ctl.Holder(agentID); by != "" {
		return nil, browser.ErrTakenOver
	}
	in, err := c.ensure(ctx, agentID)
	if err != nil {
		return nil, err
	}
	return browser.NewSession(agentID, in.page, c.ctl, in.touch), nil
}

func (c *Chrome) ensure(ctx context.Context, agentID string) (*instance, error) {
	if !agentIDRe.MatchString(agentID) {
		return nil, fmt.Errorf("browser-chrome: invalid agent id %q", agentID)
	}
	s, err := c.slot(agentID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if in := s.inst.Load(); in.alive() {
		s.mu.Unlock()
		in.touch()
		return in, nil
	} else if in != nil {
		// crashed or disconnected: clean up before restarting
		c.stopLocked(s, in)
	}
	var in *instance
	if c.attach != "" {
		in, err = c.connect(ctx, agentID, s)
	} else {
		in, err = c.launch(ctx, agentID, c.headless && !s.window.Load())
		if err == nil && s.carry != nil {
			c.restore(ctx, in, &carry{tabs: pick(s.carry.tabs), cookies: s.carry.cookies})
			if !s.window.Load() {
				s.carry = nil
			}
		}
	}
	if err == nil {
		c.mu.Lock()
		if c.closed {
			err = browser.ErrClosed
		}
		c.mu.Unlock()
		if err != nil {
			c.stopLocked(s, in)
		}
	}
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	in.touch()
	s.inst.Store(in)
	s.mu.Unlock()
	c.changed(c.State(agentID))
	return in, nil
}

func (c *Chrome) launch(ctx context.Context, agentID string, headless bool) (*instance, error) {
	if !headless && c.windowErr != nil {
		return nil, c.windowErr
	}
	dir, err := c.ProfileDir(agentID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("browser-chrome: profile dir: %w", err)
	}
	portFile := filepath.Join(dir, "DevToolsActivePort")
	_ = os.Remove(portFile)
	args := []string{
		"--remote-debugging-port=0",
		"--user-data-dir=" + dir,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-sync",
		"--disable-features=Translate,MediaRouter,OptimizationHints",
		"--disable-background-networking",
		"--hide-crash-restore-bubble",
		"--password-store=basic",
		"--use-mock-keychain",
		fmt.Sprintf("--window-size=%d,%d", c.width, c.height),
	}
	if headless || c.fakeWindow {
		args = append(args, "--headless=new", "--hide-scrollbars", "--mute-audio")
	}
	if os.Geteuid() == 0 {
		args = append(args, "--no-sandbox")
	}
	args = append(args, c.args...)
	args = append(args, "about:blank")

	cmd := exec.Command(c.exe, args...)
	setGroup(cmd)
	// stderr goes through our own pipe so Wait does not block on helper
	// processes that inherited it.
	tail := &tailBuf{max: 4 << 10}
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return nil, fmt.Errorf("browser-chrome: start %s: %w", c.exe, err)
	}
	pw.Close()
	go func() { _, _ = io.Copy(tail, pr); pr.Close() }()
	in := &instance{agent: agentID, dir: dir, cmd: cmd, exited: make(chan struct{}), stderr: tail, headless: headless}
	go func() { _ = cmd.Wait(); close(in.exited) }()

	fail := func(err error) (*instance, error) {
		killTree(cmd.Process.Pid)
		<-in.exited
		if in.frames != nil {
			in.frames.close()
		}
		if in.allocCancel != nil {
			in.tabCancel()
			in.browserCancel()
			in.allocCancel()
		}
		return nil, err
	}
	wsURL, err := waitPortFile(ctx, portFile, in.exited, c.start)
	if err != nil {
		return fail(fmt.Errorf("browser-chrome: %w\n%s", err, tail.String()))
	}
	in.wsURL = wsURL
	allocCtx, allocCancel := remote.NewAllocator(context.Background(), wsURL, remote.NoModifyURL)
	in.allocCancel = allocCancel
	in.browserCtx, in.browserCancel = chromedp.NewContext(allocCtx)
	id, err := firstPage(ctx, in.browserCtx, c.start)
	if err != nil {
		in.tabCancel = func() {}
		return fail(fmt.Errorf("browser-chrome: %w", err))
	}
	in.tabCtx, in.tabCancel = chromedp.NewContext(in.browserCtx, chromedp.WithTargetID(id))
	if err := initTab(ctx, in.tabCtx, c.start); err != nil {
		return fail(fmt.Errorf("browser-chrome: attach tab: %w", err))
	}
	in.page = &cdpPage{tab: in.tabCtx}
	c.openFrames(ctx, in, wsURL)
	c.applyFilter(in)
	return in, nil
}

// openFrames gives the instance its frame connection (frames.go). Without
// one the agent still works in the main frame.
func (c *Chrome) openFrames(ctx context.Context, in *instance, wsURL string) {
	t := chromedp.FromContext(in.tabCtx).Target
	if t == nil {
		return
	}
	fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ws, err := browserWS(fctx, wsURL)
	if err != nil {
		return
	}
	agentID := in.agent
	f, err := openFrames(fctx, ws, t.TargetID, func() func(string) bool { return c.filterFor(agentID) })
	if err != nil {
		return
	}
	in.frames = f
	in.page.frames = f
}

func (c *Chrome) connect(ctx context.Context, agentID string, s *slot) (*instance, error) {
	c.mu.Lock()
	if c.owner != "" && c.owner != agentID {
		owner := c.owner
		c.mu.Unlock()
		return nil, fmt.Errorf("browser-chrome: the attached browser is in use by agent %s", owner)
	}
	c.owner = agentID
	c.mu.Unlock()
	in := &instance{agent: agentID, attached: true}
	fail := func(err error) (*instance, error) {
		c.mu.Lock()
		if c.owner == agentID {
			c.owner = ""
		}
		c.mu.Unlock()
		if in.frames != nil {
			in.frames.close()
		}
		if in.tabCancel != nil {
			in.tabCancel()
		}
		if in.browserCancel != nil {
			in.browserCancel()
		}
		if in.allocCancel != nil {
			in.allocCancel()
		}
		return nil, err
	}
	allocCtx, allocCancel := remote.NewAllocator(context.Background(), c.attach)
	in.allocCancel = allocCancel
	in.browserCtx, in.browserCancel = chromedp.NewContext(allocCtx)
	ts, err := targets(ctx, in.browserCtx, c.start)
	if err != nil {
		return fail(fmt.Errorf("browser-chrome: attach %s: %w", c.attach, err))
	}
	opts := []chromedp.ContextOption{chromedp.WithDetachOnCancel()}
	for _, t := range ts {
		if s.target != "" && t.TargetID == s.target && t.Type == "page" {
			opts = append(opts, chromedp.WithTargetID(t.TargetID))
		}
	}
	in.tabCtx, in.tabCancel = chromedp.NewContext(in.browserCtx, opts...)
	if err := initTab(ctx, in.tabCtx, c.start); err != nil {
		return fail(fmt.Errorf("browser-chrome: attach tab: %w", err))
	}
	if t := chromedp.FromContext(in.tabCtx).Target; t != nil {
		s.target = t.TargetID
	}
	in.page = &cdpPage{tab: in.tabCtx}
	c.openFrames(ctx, in, c.attach)
	c.applyFilter(in)
	return in, nil
}

// bounded runs fn (which may block on a chromedp context that must not carry
// a timeout: the first call on it binds the browser connection to it) and
// waits at most timeout or until ctx is done.
func bounded(ctx context.Context, timeout time.Duration, fn func() error) error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case err := <-done:
		return err
	case <-t.C:
		return errors.New("timed out")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func initTab(ctx, tab context.Context, timeout time.Duration) error {
	return bounded(ctx, timeout, func() error {
		return chromedp.Do(tab, chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
			run := true
			_, err := cdp.Call(ctx, t, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: sameTabJS, RunImmediately: &run})
			return err
		}))
	})
}

// sameTabJS keeps the agent in one tab: links with a target and
// window.open load in the current tab, so the agent sees where it went
// (new tabs would be invisible to it). Forms with a target likewise.
const sameTabJS = `(() => {
  if (window.top !== window || window.__crewSameTab) return;
  window.__crewSameTab = true;
  const fix = (e) => {
    const el = e.target && e.target.closest && e.target.closest("a[target], area[target], form[target]");
    if (el && el.target && el.target !== "_self" && el.target !== "_top" && el.target !== "_parent") el.target = "_self";
  };
  document.addEventListener("click", fix, true);
  document.addEventListener("submit", fix, true);
  const open = window.open;
  window.open = function (url) {
    if (url) { location.href = new URL(url, location.href).href; return window; }
    return open.apply(window, arguments);
  };
})();`

func targets(ctx, browserCtx context.Context, timeout time.Duration) ([]*target.Info, error) {
	var ts []*target.Info
	err := bounded(ctx, timeout, func() error {
		var err error
		ts, err = chromedp.Targets(browserCtx)
		return err
	})
	return ts, err
}

func firstPage(ctx, browserCtx context.Context, timeout time.Duration) (target.ID, error) {
	deadline := time.Now().Add(timeout)
	for {
		ts, err := targets(ctx, browserCtx, time.Until(deadline))
		if err == nil {
			for _, t := range ts {
				if t.Type == "page" {
					return t.TargetID, nil
				}
			}
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			if err == nil {
				err = errors.New("no page target")
			}
			return "", fmt.Errorf("find page: %w", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitPortFile waits for Chrome to write DevToolsActivePort ("port\npath").
func waitPortFile(ctx context.Context, path string, exited <-chan struct{}, timeout time.Duration) (string, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		if b, err := os.ReadFile(path); err == nil {
			lines := strings.Split(strings.TrimSpace(string(b)), "\n")
			if len(lines) >= 2 && lines[0] != "" {
				return "ws://127.0.0.1:" + strings.TrimSpace(lines[0]) + strings.TrimSpace(lines[1]), nil
			}
		}
		select {
		case <-exited:
			return "", errors.New("browser exited during start")
		case <-deadline.C:
			return "", errors.New("timed out waiting for the browser to start")
		case <-ctx.Done():
			return "", ctx.Err()
		case <-tick.C:
		}
	}
}

// stopLocked closes an instance; s.mu must be held.
func (c *Chrome) stopLocked(s *slot, in *instance) {
	if in == nil {
		return
	}
	s.inst.CompareAndSwap(in, nil)
	c.forgetFilter(in)
	if in.frames != nil {
		in.frames.close() // the iframe sessions go with the connection
	}
	if in.attached {
		// Attached: only disconnect; the browser and the agent's tab stay.
		in.tabCancel()
		in.browserCancel()
		in.allocCancel()
		c.mu.Lock()
		if c.owner == in.agent {
			c.owner = ""
		}
		c.mu.Unlock()
		return
	}
	// Close gracefully so the profile (cookies, logins) is flushed.
	// chromedp reserves Browser.close for its own allocator, so it is sent
	// on a separate DevTools connection.
	if in.wsURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if conn, err := remote.DialContext(ctx, in.wsURL); err == nil {
			_ = conn.Write(ctx, &cdproto.Message{ID: 1, Method: cdpbrowser.CommandClose})
			var m cdproto.Message
			_ = conn.Read(ctx, &m)
			_ = conn.Close()
		}
		cancel()
	}
	select {
	case <-in.exited:
	case <-time.After(5 * time.Second):
	}
	// Reap the whole tree (helpers may linger after the main process).
	killTree(in.cmd.Process.Pid)
	select {
	case <-in.exited:
	case <-time.After(2 * time.Second):
		_ = in.cmd.Process.Kill()
		<-in.exited
	}
	in.allocCancel()
	in.tabCancel()
	in.browserCancel()
	_ = os.Remove(filepath.Join(in.dir, "DevToolsActivePort"))
}

func (c *Chrome) stop(agentID string) bool {
	c.mu.Lock()
	s := c.slots[agentID]
	c.mu.Unlock()
	if s == nil {
		return false
	}
	s.mu.Lock()
	in := s.inst.Load()
	c.stopLocked(s, in)
	s.window.Store(false)
	s.carry = nil
	s.mu.Unlock()
	return in != nil
}

// Release implements browser.Browser.
func (c *Chrome) Release(agentID string) {
	ended := c.ctl.End(agentID) == nil
	if c.stop(agentID) || ended {
		c.changed(c.State(agentID))
	}
}

// Takeover implements browser.Browser.
func (c *Chrome) Takeover(agentID, by string) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.start)
	defer cancel()
	if _, err := c.ensure(ctx, agentID); err != nil {
		return err
	}
	started, err := c.ctl.Begin(agentID, by)
	if err != nil {
		return err
	}
	if started {
		// the person may need hosts the agent may not (an SSO page); the
		// agent's next action sets its filter again
		c.SetHostFilter(agentID, nil)
		c.changed(c.State(agentID))
	}
	return nil
}

// Handback implements browser.Browser.
func (c *Chrome) Handback(agentID string) error {
	if by, _ := c.ctl.Holder(agentID); by == "" {
		return browser.ErrNotTakenOver
	}
	// A window takeover returns to headless first, while the agent is
	// still paused. If that fails the tabs are kept for the next start.
	c.mu.Lock()
	s := c.slots[agentID]
	c.mu.Unlock()
	if s != nil {
		s.mu.Lock()
		if s.window.Load() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*c.start+30*time.Second)
			_ = c.switchLocked(ctx, s, agentID, false)
			cancel()
		}
		s.mu.Unlock()
	}
	if err := c.ctl.End(agentID); err != nil {
		return err
	}
	if in := c.peek(agentID); in != nil {
		in.touch()
	}
	c.changed(c.State(agentID))
	return nil
}

// State implements browser.Browser.
func (c *Chrome) State(agentID string) browser.State {
	st := browser.State{Agent: agentID, Headless: c.headless, Attached: c.attach != "", View: "window"}
	st.TakenOverBy, st.Since = c.ctl.Holder(agentID)
	c.mu.Lock()
	s := c.slots[agentID]
	c.mu.Unlock()
	if s != nil {
		st.Window = s.window.Load()
	}
	if c.headless && !st.Window {
		st.View = "frames"
	}
	st.Headless = c.headless && !st.Window
	st.CanWindow = c.canWindow()
	in := c.peek(agentID)
	if !in.alive() {
		return st
	}
	st.Running = true
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	st.URL, st.Title, _ = in.page.Location(ctx)
	return st
}

// Screenshot implements browser.Browser (for the person's takeover view).
func (c *Chrome) Screenshot(agentID string) ([]byte, error) {
	in := c.peek(agentID)
	if !in.alive() {
		return nil, browser.ErrNotRunning
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return in.page.Screenshot(ctx)
}

// Input implements browser.Browser. Inputs are never logged.
func (c *Chrome) Input(agentID, by string, x browser.Input) error {
	holder, _ := c.ctl.Holder(agentID)
	switch {
	case holder == "":
		return browser.ErrNotTakenOver
	case holder != by:
		return browser.ErrNotController
	}
	in := c.peek(agentID)
	if !in.alive() {
		return browser.ErrNotRunning
	}
	in.touch()
	timeout := 10 * time.Second
	if x.Type == browser.InputNavigate {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	switch x.Type {
	case browser.InputClick:
		return in.page.Click(ctx, x.X, x.Y)
	case browser.InputKey:
		if x.Text == "" {
			return errors.New("key: text is required")
		}
		if IsKeyName(x.Text) {
			return in.page.Key(ctx, x.Text)
		}
		return in.page.InsertText(ctx, x.Text)
	case browser.InputNavigate:
		u, err := url.Parse(x.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return errors.New("navigate: only http(s) URLs are allowed")
		}
		return in.page.Navigate(ctx, u.String())
	case browser.InputScroll:
		px, py := x.X, x.Y
		if px == 0 && py == 0 {
			px, py = float64(c.width)/2, float64(c.height)/2
		}
		return in.page.Scroll(ctx, px, py, x.DY)
	default:
		return fmt.Errorf("unknown input type %q", x.Type)
	}
}

// AwaitHandback implements browser.Browser.
func (c *Chrome) AwaitHandback(ctx context.Context, agentID string) (browser.State, error) {
	if err := c.ctl.Await(ctx, agentID); err != nil {
		return browser.State{}, err
	}
	return c.State(agentID), nil
}

// Reap closes browsers idle for longer than the idle timeout (and cleans up
// crashed ones). It runs periodically; exported for tests.
func (c *Chrome) Reap() {
	c.mu.Lock()
	ids := make([]string, 0, len(c.slots))
	for id := range c.slots {
		ids = append(ids, id)
	}
	c.mu.Unlock()
	for _, id := range ids {
		in := c.peek(id)
		if in == nil {
			continue
		}
		if in.alive() {
			if c.idle <= 0 {
				continue
			}
			if by, _ := c.ctl.Holder(id); by != "" || c.ctl.Busy(id) {
				continue
			}
			if time.Since(time.Unix(0, in.last.Load())) < c.idle {
				continue
			}
		}
		if c.stop(id) {
			c.changed(c.State(id))
		}
	}
}

// Running lists agents with a running browser.
func (c *Chrome) Running() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for id, s := range c.slots {
		if s.inst.Load().alive() {
			out = append(out, id)
		}
	}
	return out
}

// Close releases every browser; the provider refuses new sessions after.
func (c *Chrome) Close() {
	c.mu.Lock()
	c.closed = true
	ids := make([]string, 0, len(c.slots))
	for id := range c.slots {
		ids = append(ids, id)
	}
	c.mu.Unlock()
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func() { defer wg.Done(); c.Release(id) }()
	}
	wg.Wait()
}

var _ browser.Browser = (*Chrome)(nil)

// tailBuf keeps the last max bytes written (Chrome's stderr, for errors).
type tailBuf struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tailBuf) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = append([]byte(nil), t.b[len(t.b)-t.max:]...)
	}
	return len(p), nil
}

func (t *tailBuf) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(bytes.TrimSpace(t.b))
}

// Plugin provides `browser` with local Chrome / Chromium.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/browser-chrome",
	Description: "Per-agent Chrome/Chromium with its own profile (CDP), started on demand, idle-reclaimed, with human takeover.",
	Optional:    []string{agent.ServiceKey},
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		ch, err := New(cfg, Options{
			Dispatcher: c,
			Profile: func(id string) (string, error) {
				if agents, ok := kernel.Lookup[*agent.Registry](c, agent.ServiceKey); ok {
					if a, ok := agents.Get(id); ok {
						return filepath.Join(a.Workspace(), "browser"), nil
					}
					if cfg.ProfileRoot == "" {
						return "", fmt.Errorf("browser-chrome: unknown agent %s", id)
					}
				}
				root := cfg.ProfileRoot
				if root == "" {
					root = filepath.Join(os.TempDir(), "crewbase-browser")
				}
				return filepath.Join(root, id), nil
			},
			Changed: func(st browser.State) { browser.Changed.Emit(kernel.For(c, st.Agent), st) },
		})
		if err != nil {
			return err
		}
		c.Defer(ch.Close)
		agent.Deleted.Listen(c, func(id string) { ch.Release(id) })
		if ch.idle > 0 {
			every := min(max(ch.idle/4, 10*time.Millisecond), 30*time.Second)
			c.Go(func(ctx context.Context) {
				t := time.NewTicker(every)
				defer t.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-t.C:
						ch.Reap()
					}
				}
			})
		}
		if ch.exe != "" {
			c.Log().Debug("browser-chrome ready", "executable", ch.exe, "headless", ch.headless)
		}
		return c.Provide(browser.ServiceKey, ch)
	},
})
