package chrome

// Window takeover: a person who took over a headless browser can move it to
// a real window on the desktop (to sign in with a saved login, pass a
// captcha, use a security key) and back, without restarting anything.
//
// Chrome fixes headless / headed at launch and locks its profile, so a
// switch is: capture the open tabs (URL, scroll position, which one is in
// front) and the session cookies → close the browser gracefully (the
// profile is flushed and unlocked) → launch it again in the other mode on
// the same profile → set the session cookies back → reopen the tabs.
// Persistent cookies, local storage and IndexedDB live in the profile and
// survive by themselves; session cookies would not (Chrome drops them on
// exit), which is why they are carried over in memory — never logged.
//
// The takeover is held for the whole switch, so the agent stays paused.
// If the window cannot be opened, the browser is started headless again
// with the same tabs and the frame view goes on. On hand-back the browser
// returns to headless with the tabs as the person left them; the agent
// continues in the tab the person was looking at.

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"runtime"
	"time"

	"github.com/chromedp/cdproto/target"

	"github.com/hedeqiang/crewbase/capabilities/browser"
)

var _ browser.Windower = (*Chrome)(nil)

// tabState is one open tab, captured before a switch.
type tabState struct {
	URL     string  `json:"url"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Front   bool    `json:"front"` // visible (the tab in front of its window)
	Agent   bool    `json:"agent"` // the agent's tab
	Present bool    `json:"-"`
}

// carry is what a switch takes from one browser to the next.
type carry struct {
	tabs    []tabState
	cookies []map[string]any // session cookies only
}

// canWindow reports whether a window can be shown on this machine.
func (c *Chrome) canWindow() bool {
	if c.attach != "" || !c.headless {
		return false
	}
	if c.fakeWindow {
		return true
	}
	if runtime.GOOS == "linux" || runtime.GOOS == "freebsd" || runtime.GOOS == "openbsd" {
		return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
	}
	return true
}

func restorable(u string) bool {
	p, err := url.Parse(u)
	return err == nil && (p.Scheme == "http" || p.Scheme == "https")
}

// capture reads the tabs (and, with cookies, the session cookies) of a
// running browser.
func (c *Chrome) capture(ctx context.Context, in *instance, cookies bool) (*carry, error) {
	f := in.frames
	if f == nil {
		u, _, err := in.page.Location(ctx)
		if err != nil {
			return nil, err
		}
		return &carry{tabs: []tabState{{URL: u, Agent: true, Front: true}}}, nil
	}
	var ts struct {
		Infos []*target.Info `json:"targetInfos"`
	}
	if err := f.call(ctx, "", "Target.getTargets", map[string]any{}, &ts); err != nil {
		return nil, err
	}
	out := &carry{}
	for _, t := range ts.Infos {
		if t.Type != "page" {
			continue
		}
		tab := tabState{URL: t.URL, Agent: t.TargetID == f.pageT}
		if !restorable(tab.URL) && !tab.Agent {
			continue
		}
		var v struct {
			X, Y  float64
			Front bool
		}
		if err := f.inTab(ctx, t.TargetID, `({X: scrollX, Y: scrollY, Front: document.visibilityState === "visible"})`, &v); err == nil {
			tab.X, tab.Y, tab.Front = v.X, v.Y, v.Front
		}
		if tab.Agent {
			out.tabs = append([]tabState{tab}, out.tabs...)
		} else {
			out.tabs = append(out.tabs, tab)
		}
	}
	if cookies {
		var r struct {
			Cookies []map[string]any `json:"cookies"`
		}
		if err := f.call(ctx, "", "Storage.getCookies", map[string]any{}, &r); err == nil {
			for _, ck := range r.Cookies {
				if s, _ := ck["session"].(bool); s {
					out.cookies = append(out.cookies, cookieParam(ck))
				}
			}
		}
	}
	return out, nil
}

// cookieParam turns a Network.Cookie into a Network.CookieParam.
func cookieParam(ck map[string]any) map[string]any {
	p := map[string]any{}
	for _, k := range []string{"name", "value", "domain", "path", "secure", "httpOnly", "sameSite", "priority", "sourceScheme", "sourcePort", "partitionKey"} {
		if v, ok := ck[k]; ok {
			p[k] = v
		}
	}
	return p
}

// inTab evaluates expr in the main world of tab tid on a short-lived session.
func (f *frameConn) inTab(ctx context.Context, tid target.ID, expr string, out any) error {
	var at struct {
		SessionID target.SessionID `json:"sessionId"`
	}
	if err := f.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": tid, "flatten": true}, &at); err != nil {
		return err
	}
	defer func() {
		_ = f.call(context.WithoutCancel(ctx), "", "Target.detachFromTarget", map[string]any{"sessionId": at.SessionID}, nil)
	}()
	var r struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		Exception json.RawMessage `json:"exceptionDetails"`
	}
	if err := f.call(ctx, at.SessionID, "Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true}, &r); err != nil {
		return err
	}
	if len(r.Exception) > 0 {
		return errors.New("script error")
	}
	if out == nil || len(r.Result.Value) == 0 {
		return nil
	}
	return json.Unmarshal(r.Result.Value, out)
}

// pick returns the tab the agent continues in after a switch: the one the
// person was looking at (a single tab in front), else the agent's own tab,
// else the first. It is moved to the front of the list.
func pick(tabs []tabState) []tabState {
	if len(tabs) == 0 {
		return nil
	}
	at := -1
	front := 0
	for i, t := range tabs {
		if t.Front {
			front++
			if at < 0 || t.Agent {
				at = i
			}
		}
	}
	if at < 0 || front > 1 && !tabs[at].Agent {
		at = 0
		for i, t := range tabs {
			if t.Agent {
				at = i
			}
		}
	}
	out := append([]tabState{tabs[at]}, tabs[:at]...)
	out = append(out, tabs[at+1:]...)
	for i := range out {
		out[i].Agent = i == 0
	}
	return out
}

// restore brings the carried state into a freshly launched browser: session
// cookies first (so the pages open signed in), then the agent's tab, then
// the other tabs in the background. Failures are not fatal: a page that
// does not load is left as it is.
func (c *Chrome) restore(ctx context.Context, in *instance, cr *carry) {
	if cr == nil {
		return
	}
	f := in.frames
	if f != nil && len(cr.cookies) > 0 {
		_ = f.call(ctx, "", "Storage.setCookies", map[string]any{"cookies": cr.cookies}, nil)
	}
	for i, t := range cr.tabs {
		if !restorable(t.URL) {
			continue
		}
		if i == 0 {
			nctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			_ = in.page.Navigate(nctx, t.URL)
			cancel()
			if t.X != 0 || t.Y != 0 {
				_ = in.page.Eval(ctx, scrollJS(t.X, t.Y), nil)
			}
			continue
		}
		if f == nil {
			continue
		}
		var r struct {
			TargetID target.ID `json:"targetId"`
		}
		if err := f.call(ctx, "", "Target.createTarget", map[string]any{"url": t.URL, "background": true}, &r); err != nil {
			continue
		}
		if t.X != 0 || t.Y != 0 {
			go func(tid target.ID, x, y float64) {
				sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				for sctx.Err() == nil {
					var st string
					if err := f.inTab(sctx, tid, "document.readyState", &st); err == nil && st == "complete" {
						_ = f.inTab(sctx, tid, scrollJS(x, y), nil)
						return
					}
					time.Sleep(200 * time.Millisecond)
				}
			}(r.TargetID, t.X, t.Y)
		}
	}
	if f != nil {
		// the agent's tab in front
		_ = f.call(ctx, "", "Target.activateTarget", map[string]any{"targetId": f.pageT}, nil)
	}
}

func scrollJS(x, y float64) string {
	b, _ := json.Marshal([]float64{x, y})
	return "(([x, y]) => scrollTo(x, y))(" + string(b) + ")"
}

// SetWindow implements browser.Windower: during a takeover by `by`, move
// the agent's browser to a desktop window (open) or back to headless.
func (c *Chrome) SetWindow(agentID, by string, open bool) error {
	holder, _ := c.ctl.Holder(agentID)
	switch {
	case holder == "":
		return browser.ErrNotTakenOver
	case holder != by:
		return browser.ErrNotController
	case open && !c.canWindow():
		return browser.ErrNoWindow
	}
	s, err := c.slot(agentID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*c.start+30*time.Second)
	defer cancel()
	s.mu.Lock()
	err = c.switchLocked(ctx, s, agentID, open)
	s.mu.Unlock()
	c.changed(c.State(agentID))
	if err == nil && open {
		go c.watch(agentID, s)
	}
	return err
}

// switchLocked relaunches the agent's browser windowed (open) or headless,
// carrying the tabs over; s.mu must be held.
func (c *Chrome) switchLocked(ctx context.Context, s *slot, agentID string, open bool) error {
	old := s.inst.Load()
	if s.window.Load() == open && old.alive() {
		return nil
	}
	cr := s.carry
	if old.alive() {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if got, err := c.capture(cctx, old, true); err == nil {
			cr = got
		}
		cancel()
	}
	if cr != nil && s.window.Load() {
		// leaving a window: the agent goes on where the person was looking
		// (into a window, the agent's tab stays first: the frame view shows
		// only that one)
		cr.tabs = pick(cr.tabs)
	}
	c.stopLocked(s, old)
	in, err := c.launch(ctx, agentID, !open)
	if err != nil && open {
		// no window: back to the frame view with the same tabs
		werr := err
		s.window.Store(false)
		if in, err = c.launch(ctx, agentID, true); err != nil {
			s.carry = cr
			return errors.Join(werr, err)
		}
		c.restore(ctx, in, cr)
		in.touch()
		s.inst.Store(in)
		s.carry = nil
		return errors.Join(browser.ErrNoWindow, werr)
	}
	if err != nil {
		s.carry = cr // the next start (or a retry) restores the tabs
		s.window.Store(false)
		return err
	}
	c.restore(ctx, in, cr)
	in.touch()
	s.inst.Store(in)
	s.window.Store(open)
	s.carry = nil
	if open {
		// keep the tabs in case the person closes the window by hand
		s.carry = cr
	}
	return nil
}

// watch follows a window takeover: it keeps the last known tabs (so they
// survive the person closing the window) and reports the window closing.
func (c *Chrome) watch(agentID string, s *slot) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for range t.C {
		s.mu.Lock()
		in, window := s.inst.Load(), s.window.Load()
		s.mu.Unlock()
		if !window || in == nil {
			return
		}
		if !in.alive() {
			c.changed(c.State(agentID)) // the person closed the window
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		cr, err := c.capture(ctx, in, true)
		cancel()
		s.mu.Lock()
		if err == nil && s.window.Load() && s.inst.Load() == in && in.alive() {
			s.carry = cr
		}
		s.mu.Unlock()
	}
}
