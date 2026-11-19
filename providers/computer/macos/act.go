package macos

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"regexp"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/computer"
)

// errSecure: agents never type into password fields.
var errSecure = errors.New("this is a password field: agents never type into it; ask the person to enter it themselves")

// bundleIDRe is what Open accepts as a bundle id (anything else is a name).
var bundleIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.\-]*\.[A-Za-z0-9.\-]+$`)

// Act performs one action. Actions on one app are serialised.
func (p *Provider) Act(ctx context.Context, a computer.Action) (computer.Result, error) {
	switch a.Kind {
	case computer.Open:
		return p.open(ctx, a.App)
	case computer.Press, computer.SetValue, computer.Focus, computer.Menu, computer.Type, computer.Key,
		computer.Click, computer.DoubleClick, computer.TripleClick, computer.RightClick, computer.MiddleClick, computer.MouseMove,
		computer.Scroll, computer.Drag, computer.Activate:
	default:
		return computer.Result{}, fmt.Errorf("unknown action %q", a.Kind)
	}
	if !p.pf.Trusted() {
		return computer.Result{}, computer.ErrNoAccessibility
	}
	app, err := p.resolve(a.App)
	if err != nil {
		return computer.Result{}, err
	}
	st, err := p.state(app)
	if err != nil {
		return computer.Result{}, err
	}
	defer st.mu.Unlock()
	if a.Version != 0 && a.Version != st.version {
		return computer.Result{}, computer.ErrStale
	}
	if err := ctx.Err(); err != nil {
		return computer.Result{}, err
	}
	switch a.Kind {
	case computer.Type, computer.Key, computer.SetValue, computer.Click, computer.DoubleClick, computer.TripleClick, computer.RightClick,
		computer.MiddleClick, computer.MouseMove, computer.Scroll, computer.Drag:
		defer p.posting()() // our events are not the person's input (IdleFor)
	}

	switch a.Kind {
	case computer.Press:
		return p.press(st, a)
	case computer.SetValue:
		return p.setValue(st, a)
	case computer.Focus:
		e, err := p.element(st, a.Element)
		if err != nil {
			return computer.Result{}, err
		}
		if err := p.pf.SetBool(e.ref, "AXFocused", true); err != nil {
			return computer.Result{}, fmt.Errorf("focus [%d]: %w", a.Element, mapErr(err))
		}
		time.Sleep(p.settle / 2)
		if ok, err := p.pf.Bool(e.ref, "AXFocused"); err == nil && !ok {
			return computer.Result{Done: false, Note: "focus was asked for, but the element does not report focus"}, nil
		}
		return computer.Result{Done: true}, nil
	case computer.Menu:
		return p.menu(st, a.Path)
	case computer.Type:
		return p.typeText(st, app, a)
	case computer.Key:
		keys, err := parseKeys(a.Keys)
		if err != nil {
			return computer.Result{}, err
		}
		before := p.fingerprint(st, 0)
		for _, k := range keys {
			if err := p.pf.PostKey(app.PID, k.Code, k.Flags); err != nil {
				return computer.Result{}, err
			}
			time.Sleep(15 * time.Millisecond)
		}
		return p.checked(st, 0, before, "keys sent"), nil
	case computer.Activate:
		if err := p.activate(st, app); err != nil {
			return computer.Result{}, err
		}
		return computer.Result{Done: true}, nil
	}
	return p.pointer(ctx, st, app, a)
}

// element returns numbered element n of the latest outline.
func (p *Provider) element(st *appState, n int) (elemInfo, error) {
	if n <= 0 || n > len(st.elems) {
		return elemInfo{}, computer.ErrNoElement
	}
	e := st.elems[n-1]
	if _, err := p.pf.Read(e.ref); err != nil {
		if errors.Is(err, errInvalid) {
			return elemInfo{}, computer.ErrStale
		}
		return elemInfo{}, mapErr(err)
	}
	return e, nil
}

// fingerprint summarises what an action may visibly change: the element
// (when given), the focused element, the window's title and the number of
// windows.
func (p *Provider) fingerprint(st *appState, n int) uint64 {
	h := fnv.New64a()
	if n > 0 && n <= len(st.elems) {
		if raw, err := p.pf.Read(st.elems[n-1].ref); err == nil {
			fmt.Fprintf(h, "e|%s|%s|%t|%t|%t|%s|", raw.Value, raw.Title, raw.Selected, raw.Focused, raw.Enabled, raw.Description)
		} else {
			fmt.Fprintf(h, "e|gone|")
		}
	}
	if f, err := p.pf.Attr(st.appEl, "AXFocusedUIElement"); err == nil {
		if raw, err := p.pf.Read(f); err == nil {
			fmt.Fprintf(h, "f|%s|%s|%s|%g,%g|", raw.Role, raw.Value, raw.Title, raw.X, raw.Y)
		}
		p.pf.Release(f)
	}
	if w, err := p.frontWindow(st.appEl); err == nil {
		if raw, err := p.pf.Read(w); err == nil {
			fmt.Fprintf(h, "w|%s|%g,%g,%g,%g|", raw.Title, raw.X, raw.Y, raw.W, raw.H)
		}
		p.pf.Release(w)
	}
	if ws, err := p.pf.Elements(st.appEl, "AXWindows", 50); err == nil {
		fmt.Fprintf(h, "n|%d", len(ws))
		for _, w := range ws {
			p.pf.Release(w)
		}
	}
	return h.Sum64()
}

// checked waits for the action to settle and reports whether anything
// visible changed.
func (p *Provider) checked(st *appState, n int, before uint64, what string) computer.Result {
	time.Sleep(p.settle)
	if p.fingerprint(st, n) != before {
		return computer.Result{Done: true}
	}
	return computer.Result{Done: false, Note: what + ", but no change is visible in the element or the window; observe (or take a screenshot) to check"}
}

func (p *Provider) press(st *appState, a computer.Action) (computer.Result, error) {
	e, err := p.element(st, a.Element)
	if err != nil {
		return computer.Result{}, err
	}
	before := p.fingerprint(st, a.Element)
	action := pressAction(e.raw)
	switch {
	case action != "":
		err = p.pf.Perform(e.ref, action)
	case selectable[e.raw.Role]:
		err = p.pf.SetBool(e.ref, "AXSelected", true)
	case focusable[e.raw.Role] || e.raw.secure():
		err = p.pf.SetBool(e.ref, "AXFocused", true)
	default:
		return computer.Result{}, fmt.Errorf("[%d] %s has no action to press; try click", a.Element, roleName(e.raw.Role, e.raw.Subrole))
	}
	if err != nil {
		return computer.Result{}, fmt.Errorf("press [%d]: %w", a.Element, mapErr(err))
	}
	return p.checked(st, a.Element, before, "pressed"), nil
}

// pressAction is the element's default action: AXPress, else its first
// meaningful action, else its first action.
func pressAction(n rawNode) string {
	for _, a := range n.Actions {
		if a == "AXPress" {
			return a
		}
	}
	if m := n.meaningfulActions(); len(m) > 0 {
		return m[0]
	}
	if len(n.Actions) > 0 && !selectable[n.Role] && !focusable[n.Role] {
		return n.Actions[0]
	}
	return ""
}

func (p *Provider) setValue(st *appState, a computer.Action) (computer.Result, error) {
	e, err := p.element(st, a.Element)
	if err != nil {
		return computer.Result{}, err
	}
	if e.secure {
		return computer.Result{}, errSecure
	}
	raw, _ := p.pf.Read(e.ref)
	if raw.Settable {
		if err := p.pf.SetString(e.ref, "AXValue", a.Text); err == nil {
			time.Sleep(p.settle / 2)
			if now, err := p.pf.Read(e.ref); err == nil && now.Value == a.Text {
				return computer.Result{Done: true}, nil
			}
		}
	}
	// not settable (or it did not take): focus, select all, type
	if err := p.pf.SetBool(e.ref, "AXFocused", true); err != nil {
		return computer.Result{}, fmt.Errorf("set_value [%d]: the value cannot be set and the element cannot take focus: %w", a.Element, mapErr(err))
	}
	time.Sleep(50 * time.Millisecond)
	if err := p.pf.PostKey(st.pid, keyCodes["a"], flagCommand); err != nil {
		return computer.Result{}, err
	}
	time.Sleep(30 * time.Millisecond)
	if a.Text == "" {
		if err := p.pf.PostKey(st.pid, keyCodes["delete"], 0); err != nil {
			return computer.Result{}, err
		}
	} else if err := p.postText(st.pid, a.Text); err != nil {
		return computer.Result{}, err
	}
	time.Sleep(p.settle)
	now, err := p.pf.Read(e.ref)
	switch {
	case err != nil:
		return computer.Result{Done: false, Note: "typed the value, but the element can no longer be read; observe to check"}, nil
	case now.Value == a.Text:
		return computer.Result{Done: true, Note: "the value was typed (the element does not take a value directly)"}, nil
	case !now.HasValue:
		return computer.Result{Done: false, Note: "typed the value, but the element does not expose its value; observe or take a screenshot to check"}, nil
	}
	return computer.Result{Done: false, Note: fmt.Sprintf("typed the value, but the element now reads %q", truncate(now.Value, 80))}, nil
}

// postText types s, Return and Tab as keys.
func (p *Provider) postText(pid int, s string) error {
	var run strings.Builder
	flush := func() error {
		if run.Len() == 0 {
			return nil
		}
		err := p.pf.PostText(pid, run.String())
		run.Reset()
		return err
	}
	for _, r := range s {
		var key string
		switch r {
		case '\n':
			key = "return"
		case '\r':
			continue
		case '\t':
			key = "tab"
		default:
			run.WriteRune(r)
			continue
		}
		if err := flush(); err != nil {
			return err
		}
		if err := p.pf.PostKey(pid, keyCodes[key], 0); err != nil {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return flush()
}

func (p *Provider) typeText(st *appState, app computer.App, a computer.Action) (computer.Result, error) {
	if a.Text == "" {
		return computer.Result{}, errors.New("no text to type")
	}
	if a.Element > 0 {
		e, err := p.element(st, a.Element)
		if err != nil {
			return computer.Result{}, err
		}
		if e.secure {
			return computer.Result{}, errSecure
		}
		if err := p.pf.SetBool(e.ref, "AXFocused", true); err != nil {
			return computer.Result{}, fmt.Errorf("type: cannot focus [%d]: %w", a.Element, mapErr(err))
		}
		time.Sleep(50 * time.Millisecond)
	}
	// never into a password field, whatever has the focus
	if f, err := p.pf.Attr(st.appEl, "AXFocusedUIElement"); err == nil {
		raw, rerr := p.pf.Read(f)
		p.pf.Release(f)
		if rerr == nil && raw.secure() {
			return computer.Result{}, errSecure
		}
	}
	before := p.fingerprint(st, a.Element)
	if err := p.postText(app.PID, a.Text); err != nil {
		return computer.Result{}, err
	}
	return p.checked(st, a.Element, before, "typed"), nil
}

// menu presses the menu bar item at path.
func (p *Provider) menu(st *appState, path []string) (computer.Result, error) {
	if len(path) == 0 {
		return computer.Result{}, errors.New("menu: no path given (e.g. [\"File\", \"New\"])")
	}
	bar, err := p.pf.Attr(st.appEl, "AXMenuBar")
	if err != nil {
		return computer.Result{}, fmt.Errorf("menu: the app has no menu bar: %w", mapErr(err))
	}
	defer p.pf.Release(bar)
	item, raw, err := findMenu(p.pf, bar, path)
	if err != nil {
		return computer.Result{}, err
	}
	defer p.pf.Release(item)
	if raw.HasEnabled && !raw.Enabled {
		return computer.Result{}, fmt.Errorf("menu item %q is disabled now", strings.Join(path, " › "))
	}
	before := p.fingerprint(st, 0)
	if err := p.pf.Perform(item, "AXPress"); err != nil {
		return computer.Result{}, fmt.Errorf("menu %q: %w", strings.Join(path, " › "), mapErr(err))
	}
	return p.checked(st, 0, before, "chose the menu item"), nil
}

// findMenu walks the menu bar by titles; the item is retained.
func findMenu(src nodeSource, bar ref, path []string) (ref, rawNode, error) {
	cur := ref(0)
	var curRaw rawNode
	for i, want := range path {
		var kids []ref
		if i == 0 {
			kids, _, _ = src.Children(bar, 40)
		} else {
			kids = menuChildren(src, cur, 200)
			src.Release(cur)
			cur = 0
		}
		titles := make([]string, len(kids))
		raws := make([]rawNode, len(kids))
		for j, k := range kids {
			if raw, err := src.Read(k); err == nil {
				titles[j], raws[j] = raw.Title, raw
			}
		}
		idx := matchTitle(titles, want)
		for j, k := range kids {
			if j != idx {
				src.Release(k)
			}
		}
		if idx < 0 {
			var avail []string
			for _, t := range titles {
				if strings.TrimSpace(t) != "" {
					avail = append(avail, t)
				}
			}
			where := "the menu bar"
			if i > 0 {
				where = strings.Join(path[:i], " › ")
			}
			if len(avail) == 0 {
				return 0, rawNode{}, fmt.Errorf("menu: %q has no items", where)
			}
			return 0, rawNode{}, fmt.Errorf("menu: no %q in %s (there are: %s)", want, where, strings.Join(avail, ", "))
		}
		cur, curRaw = kids[idx], raws[idx]
	}
	return cur, curRaw, nil
}

// activate brings the app and its observed window to the front, and waits
// until it is.
func (p *Provider) activate(st *appState, app computer.App) error {
	if err := p.pf.Activate(app.PID); err != nil {
		// accessibility can do it when AppKit refuses
		if err2 := p.pf.SetBool(st.appEl, "AXFrontmost", true); err2 != nil {
			return fmt.Errorf("could not bring %s to the front: %w", app.Name, err)
		}
	}
	if st.window != 0 {
		_ = p.pf.Perform(st.window, "AXRaise")
	}
	for i := 0; i < 20; i++ {
		if ok, err := p.pf.Bool(st.appEl, "AXFrontmost"); err == nil && ok {
			time.Sleep(50 * time.Millisecond)
			return nil
		}
		if i == 5 {
			_ = p.pf.SetBool(st.appEl, "AXFrontmost", true)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("could not bring %s to the front", app.Name)
}

// pointer runs the coordinate actions.
func (p *Provider) pointer(ctx context.Context, st *appState, app computer.App, a computer.Action) (computer.Result, error) {
	if st.window == 0 {
		return computer.Result{}, errors.New("observe the app first: coordinates are relative to its window")
	}
	win := st.frame
	if raw, err := p.pf.Read(st.window); err == nil && raw.HasFrame {
		win = computer.Rect{X: raw.X, Y: raw.Y, W: raw.W, H: raw.H} // it may have moved
	} else if errors.Is(err, errInvalid) {
		return computer.Result{}, computer.ErrStale
	}
	x, y := a.X, a.Y
	if a.Element > 0 && x == 0 && y == 0 {
		e, err := p.element(st, a.Element)
		if err != nil {
			return computer.Result{}, err
		}
		x, y = centre(e.frame)
	}
	screens := p.pf.Screens()
	gx, gy, err := toScreen(win, x, y, screens)
	if err != nil {
		return computer.Result{}, err
	}
	var tx, ty float64
	if a.Kind == computer.Drag {
		if tx, ty, err = toScreen(win, a.ToX, a.ToY, screens); err != nil {
			return computer.Result{}, fmt.Errorf("drag target: %w", err)
		}
	}
	if a.Kind == computer.Scroll && a.DX == 0 && a.DY == 0 {
		return computer.Result{}, errors.New("scroll: give dx and/or dy (lines; dy > 0 scrolls down)")
	}
	if err := p.activate(st, app); err != nil {
		return computer.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return computer.Result{}, err
	}
	flags, err := modFlags(a.Mods)
	if err != nil {
		return computer.Result{}, err
	}
	// held keys travel as flags on each mouse event: apps read them there
	// (cmd-click, shift-click), as they do for synthesized clicks generally
	before := p.fingerprint(st, 0)
	if err := p.pf.PostMouse(mouseMove, buttonLeft, gx, gy, 0, flags); err != nil {
		return computer.Result{}, err
	}
	time.Sleep(30 * time.Millisecond)
	click := func(b mouseButton, n int) error {
		if err := p.pf.PostMouse(mouseDown, b, gx, gy, n, flags); err != nil {
			return err
		}
		time.Sleep(20 * time.Millisecond)
		return p.pf.PostMouse(mouseUp, b, gx, gy, n, flags)
	}
	// a double or triple click is clicks numbered 1, 2, 3 in quick succession
	clicks := func(b mouseButton, n int) error {
		for i := 1; i <= n; i++ {
			if i > 1 {
				time.Sleep(40 * time.Millisecond)
			}
			if err := click(b, i); err != nil {
				return err
			}
		}
		return nil
	}
	switch a.Kind {
	case computer.Click:
		err = click(buttonLeft, 1)
	case computer.RightClick:
		err = click(buttonRight, 1)
	case computer.MiddleClick:
		err = click(buttonMiddle, 1)
	case computer.DoubleClick:
		err = clicks(buttonLeft, 2)
	case computer.TripleClick:
		err = clicks(buttonLeft, 3)
	case computer.MouseMove:
		// done above: the pointer is there
	case computer.Scroll:
		err = p.pf.PostScroll(gx, gy, a.DX, a.DY, flags)
	case computer.Drag:
		if err = p.pf.PostMouse(mouseDown, buttonLeft, gx, gy, 1, flags); err != nil {
			break
		}
		const steps = 12
		for i := 1; i <= steps && err == nil; i++ {
			time.Sleep(15 * time.Millisecond)
			f := float64(i) / steps
			err = p.pf.PostMouse(mouseDrag, buttonLeft, gx+(tx-gx)*f, gy+(ty-gy)*f, 1, flags)
		}
		if err == nil {
			time.Sleep(30 * time.Millisecond)
			err = p.pf.PostMouse(mouseUp, buttonLeft, tx, ty, 1, flags)
		}
	}
	if err != nil {
		return computer.Result{}, err
	}
	return p.checked(st, 0, before, string(a.Kind)+" sent"), nil
}

// open launches an app (by bundle id or name) in the background, or finds
// it running, and waits briefly for a window.
func (p *Provider) open(ctx context.Context, id string) (computer.Result, error) {
	id = strings.TrimSpace(id)
	if id == "" || strings.HasPrefix(id, "-") {
		return computer.Result{}, fmt.Errorf("open: bad app %q", id)
	}
	if _, err := p.resolve(id); err == nil {
		return computer.Result{Done: true, Note: "already running"}, nil
	}
	if err := p.pf.Launch(id); err != nil {
		return computer.Result{}, fmt.Errorf("open %s: %w", id, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return computer.Result{}, err
		}
		if app, err := p.resolve(id); err == nil {
			if !p.pf.Trusted() {
				return computer.Result{Done: true}, nil
			}
			if el, err := p.pf.AppElement(app.PID); err == nil {
				ws, _ := p.pf.Elements(el, "AXWindows", 1)
				for _, w := range ws {
					p.pf.Release(w)
				}
				p.pf.Release(el)
				if len(ws) > 0 {
					return computer.Result{Done: true}, nil
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if _, err := p.resolve(id); err == nil {
		return computer.Result{Done: false, Note: "launched, but it shows no window yet"}, nil
	}
	return computer.Result{Done: false, Note: "asked macOS to open it, but it is not running yet"}, nil
}
