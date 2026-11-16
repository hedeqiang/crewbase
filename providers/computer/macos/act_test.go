package macos

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/capabilities/computer"
)

func newTestProvider(t *testing.T, f *fake) *Provider {
	t.Helper()
	p, err := New(f, Config{})
	if err != nil {
		t.Fatal(err)
	}
	p.settle = 0
	return p
}

func TestObserveAndClose(t *testing.T) {
	n := buildNotes()
	p := newTestProvider(t, n.f)
	ctx := context.Background()
	obs, err := p.Observe(ctx, "com.apple.Notes", 0)
	if err != nil {
		t.Fatal(err)
	}
	if obs.Window.Title != "购物清单" || obs.Window.Frame.X != 100 || obs.Window.ID != 77 || obs.Version == 0 || len(obs.Menus) != 2 {
		t.Fatalf("observation = %+v", obs)
	}
	// by name too, and a second outline replaces the first without leaking
	obs2, err := p.Observe(ctx, "备忘录", 0)
	if err != nil || obs2.Version != obs.Version {
		t.Fatalf("second observe: %v (version %d vs %d)", err, obs2.Version, obs.Version)
	}
	p.Close()
	n.f.checkNoLeaks(t)
}

func TestObserveErrors(t *testing.T) {
	n := buildNotes()
	p := newTestProvider(t, n.f)
	if _, err := p.Observe(context.Background(), "com.example.None", 0); !errors.Is(err, computer.ErrNoApp) {
		t.Fatalf("unknown app: %v", err)
	}
	n.f.trusted = false
	if _, err := p.Observe(context.Background(), "com.apple.Notes", 0); !errors.Is(err, computer.ErrNoAccessibility) {
		t.Fatalf("untrusted: %v", err)
	}
	n.f.screen = false
	if _, err := p.Screenshot(context.Background(), "com.apple.Notes", 0); !errors.Is(err, computer.ErrNoScreenRecording) {
		t.Fatalf("no screen recording: %v", err)
	}
	if got := p.Permissions(); got.Accessibility || got.ScreenRecording {
		t.Fatalf("permissions = %+v", got)
	}
	p.Close()
	n.f.checkNoLeaks(t)
}

func TestActElements(t *testing.T) {
	n := buildNotes()
	f := n.f
	p := newTestProvider(t, f)
	ctx := context.Background()
	obs, err := p.Observe(ctx, "com.apple.Notes", 0)
	if err != nil {
		t.Fatal(err)
	}
	act := func(a computer.Action) (computer.Result, error) {
		a.App = "com.apple.Notes"
		return p.Act(ctx, a)
	}

	// stale version
	if _, err := act(computer.Action{Kind: computer.Press, Element: 1, Version: obs.Version + 1}); !errors.Is(err, computer.ErrStale) {
		t.Fatalf("stale: %v", err)
	}
	// no such element
	if _, err := act(computer.Action{Kind: computer.Press, Element: 99}); !errors.Is(err, computer.ErrNoElement) {
		t.Fatalf("no element: %v", err)
	}

	// press with no visible effect: Done false, honest note
	res, err := act(computer.Action{Kind: computer.Press, Element: 1, Version: obs.Version})
	if err != nil || res.Done || !strings.Contains(res.Note, "no change") {
		t.Fatalf("press without effect = %+v, %v", res, err)
	}
	if got := f.performed[len(f.performed)-1]; !strings.HasSuffix(got, ":AXPress") {
		t.Fatalf("performed %q", got)
	}
	// press that changes the window title: Done
	f.onPerform = func(f *fake, r ref, a string) {
		if r == n.newBtn {
			f.mu.Lock()
			f.nodes[n.win].raw.Title = "新备忘录"
			f.mu.Unlock()
		}
	}
	if res, err := act(computer.Action{Kind: computer.Press, Element: 1}); err != nil || !res.Done {
		t.Fatalf("press with effect = %+v, %v", res, err)
	}

	// a row is pressed by selecting it
	if _, err := act(computer.Action{Kind: computer.Press, Element: 3}); err != nil {
		t.Fatal(err)
	}
	if !contains(f.sets, ":AXSelected=true") {
		t.Fatalf("row press sets = %v", f.sets)
	}

	// set_value on a settable field
	if res, err := act(computer.Action{Kind: computer.SetValue, Element: 2, Text: "牛奶"}); err != nil || !res.Done {
		t.Fatalf("set_value = %+v, %v", res, err)
	}

	// password fields are never typed into or set
	if _, err := act(computer.Action{Kind: computer.SetValue, Element: 5, Text: "hunter2"}); !errors.Is(err, errSecure) {
		t.Fatalf("set secure: %v", err)
	}
	if _, err := act(computer.Action{Kind: computer.Type, Element: 5, Text: "hunter2"}); !errors.Is(err, errSecure) {
		t.Fatalf("type secure: %v", err)
	}
	f.nodes[n.app].attrs["AXFocusedUIElement"] = n.pass
	if _, err := act(computer.Action{Kind: computer.Type, Text: "hunter2"}); !errors.Is(err, errSecure) {
		t.Fatalf("type into focused secure: %v", err)
	}
	for _, s := range f.texts {
		if strings.Contains(s, "hunter2") {
			t.Fatal("typed into a password field")
		}
	}
	delete(f.nodes[n.app].attrs, "AXFocusedUIElement")

	// type: text runs and newlines as Return
	f.texts, f.keys = nil, nil
	if _, err := act(computer.Action{Kind: computer.Type, Element: 4, Text: "a\nb"}); err != nil {
		t.Fatal(err)
	}
	if len(f.texts) != 2 || f.texts[0] != "a" || f.texts[1] != "b" || len(f.keys) != 1 || f.keys[0].Code != 0x24 {
		t.Fatalf("typed %q, keys %v", f.texts, f.keys)
	}

	// key
	f.keys = nil
	if _, err := act(computer.Action{Kind: computer.Key, Keys: "cmd+shift+n"}); err != nil {
		t.Fatal(err)
	}
	if len(f.keys) != 1 || f.keys[0] != (keyStroke{0x2D, flagCommand | flagShift}) {
		t.Fatalf("keys = %v", f.keys)
	}
	if _, err := act(computer.Action{Kind: computer.Key, Keys: "hyper+q"}); err == nil {
		t.Fatal("bad keys: want error")
	}

	// focus
	if _, err := act(computer.Action{Kind: computer.Focus, Element: 4}); err != nil {
		t.Fatal(err)
	}

	p.Close()
	f.checkNoLeaks(t)
}

func TestActMenu(t *testing.T) {
	n := buildNotes()
	f := n.f
	p := newTestProvider(t, f)
	ctx := context.Background()
	if _, err := p.Observe(ctx, "com.apple.Notes", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Act(ctx, computer.Action{Kind: computer.Menu, App: "com.apple.Notes", Path: []string{"文件", "新建"}}); err != nil {
		t.Fatal(err)
	}
	if got := f.performed[len(f.performed)-1]; got != itoa(int(n.newItem))+":AXPress" {
		t.Fatalf("pressed %q, want the new-note item", got)
	}
	_, err := p.Act(ctx, computer.Action{Kind: computer.Menu, App: "com.apple.Notes", Path: []string{"文件", "Print"}})
	if err == nil || !strings.Contains(err.Error(), "新建备忘录") {
		t.Fatalf("missing item: %v (want the items listed)", err)
	}
	if _, err := p.Act(ctx, computer.Action{Kind: computer.Menu, App: "com.apple.Notes", Path: []string{"File", "Export"}}); err == nil {
		t.Fatal("no File menu: want error")
	}
	if _, err := p.Act(ctx, computer.Action{Kind: computer.Menu, App: "com.apple.Notes", Path: []string{"文件", "export as pdf"}}); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled item: %v", err)
	}
	p.Close()
	f.checkNoLeaks(t)
}

func TestActPointer(t *testing.T) {
	n := buildNotes()
	f := n.f
	p := newTestProvider(t, f)
	ctx := context.Background()
	if _, err := p.Act(ctx, computer.Action{Kind: computer.Click, App: "com.apple.Notes", X: 5, Y: 5}); err == nil {
		t.Fatal("click before observe: want error")
	}
	if _, err := p.Observe(ctx, "com.apple.Notes", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Act(ctx, computer.Action{Kind: computer.Click, App: "com.apple.Notes", X: 20, Y: 30}); err != nil {
		t.Fatal(err)
	}
	// window at (100, 50): window point (20, 30) is (120, 80) on screen
	if f.activated == 0 || len(f.mouse) != 3 || f.mouse[1] != "1/0@120,80" || f.mouse[2] != "2/0@120,80" {
		t.Fatalf("activated %d, mouse %v", f.activated, f.mouse)
	}
	// element centre: [1] at (10,10) 30×20 → (25, 20) → (125, 70)
	f.mouse = nil
	if _, err := p.Act(ctx, computer.Action{Kind: computer.RightClick, App: "com.apple.Notes", Element: 1}); err != nil {
		t.Fatal(err)
	}
	if f.mouse[1] != "1/1@125,70" {
		t.Fatalf("right click %v", f.mouse)
	}
	f.mouse = nil
	if _, err := p.Act(ctx, computer.Action{Kind: computer.Scroll, App: "com.apple.Notes", X: 400, Y: 300, DY: 3}); err != nil {
		t.Fatal(err)
	}
	if f.mouse[1] != "scroll@500,350 0,3" {
		t.Fatalf("scroll %v", f.mouse)
	}
	f.mouse = nil
	if _, err := p.Act(ctx, computer.Action{Kind: computer.Drag, App: "com.apple.Notes", X: 10, Y: 10, ToX: 110, ToY: 10}); err != nil {
		t.Fatal(err)
	}
	if f.mouse[1] != "1/0@110,60" || f.mouse[len(f.mouse)-1] != "2/0@210,60" {
		t.Fatalf("drag %v", f.mouse)
	}
	if _, err := p.Act(ctx, computer.Action{Kind: computer.Click, App: "com.apple.Notes", X: 900, Y: 10}); err == nil {
		t.Fatal("click outside the window: want error")
	}
	p.Close()
	f.checkNoLeaks(t)
}

func TestIdleFor(t *testing.T) {
	p := newTestProvider(t, newFake())
	if got := p.IdleFor().Seconds(); got != 2.5 {
		t.Fatalf("IdleFor = %v", got)
	}
}

func contains(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
