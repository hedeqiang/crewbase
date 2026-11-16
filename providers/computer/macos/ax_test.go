package macos

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/computer"
)

func TestSparse(t *testing.T) {
	big := computer.Rect{W: 1200, H: 800}
	for _, c := range []struct {
		n     int
		frame computer.Rect
		want  bool
	}{
		{0, big, true},
		{4, big, true},
		{5, big, false},
		{40, big, false},
		{0, computer.Rect{W: 300, H: 800}, false}, // a palette or a small panel
		{0, computer.Rect{W: 1200, H: 200}, false},
		{0, computer.Rect{}, false},
	} {
		if got := sparse(c.n, c.frame); got != c.want {
			t.Errorf("sparse(%d, %+v) = %v, want %v", c.n, c.frame, got, c.want)
		}
	}
}

// electronApp is a VS Code-like app: a big window that shows only its
// traffic-light buttons until accessibility is switched on.
type electronApp struct {
	f        *fake
	app, win ref
	content  ref
}

func buildElectron(pid int) *electronApp {
	f := newFake()
	e := &electronApp{f: f}
	e.app = f.add(0, rawNode{Role: "AXApplication", Title: "Code"})
	e.win = f.add(0, rawNode{Role: "AXWindow", Title: "main.go — crewbase", X: 0, Y: 25, W: 1400, H: 850, HasFrame: true, Main: true})
	f.nodes[e.app].attrs["AXFocusedWindow"] = e.win
	for _, s := range []string{"AXCloseButton", "AXMinimizeButton", "AXZoomButton"} {
		f.add(e.win, rawNode{Role: "AXButton", Subrole: s, Enabled: true, HasEnabled: true, Actions: []string{"AXPress"}})
	}
	e.content = f.add(e.win, rawNode{Role: "AXGroup"})
	f.apps = []computer.App{{ID: "com.microsoft.VSCode", Name: "Code", PID: pid, Active: true}}
	f.appEl[pid] = e.app
	return e
}

// build adds the tree an Electron app shows once accessibility is on.
func (e *electronApp) build() {
	for _, n := range []string{"Explorer", "Search", "Source Control", "Run and Debug", "Extensions", "main.go"} {
		e.f.add(e.content, rawNode{Role: "AXButton", Description: n, Enabled: true, HasEnabled: true, Actions: []string{"AXPress"}})
	}
	e.f.add(e.content, rawNode{Role: "AXTextArea", Description: "Editor content", Value: "package main", HasValue: true, Settable: true})
}

func (f *fake) setsOf(attr string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.sets {
		if strings.Contains(s, ":"+attr+"=") {
			n++
		}
	}
	return n
}

// axProvider is a provider whose polling sleeps are counted, not slept;
// onSleep runs at each.
func axProvider(t *testing.T, f *fake, onSleep func(n int)) (*Provider, *int) {
	t.Helper()
	p := newTestProvider(t, f)
	slept := 0
	p.sleep = func(time.Duration) {
		slept++
		if onSleep != nil {
			onSleep(slept)
		}
	}
	return p, &slept
}

func TestManualAccessibilityForElectron(t *testing.T) {
	e := buildElectron(500)
	on := false
	e.f.onSet = func(f *fake, r ref, attr string, v bool) {
		if r == e.app && attr == attrManualAX && v {
			on = true
		}
	}
	// the tree appears at the second poll after the switch
	p, slept := axProvider(t, e.f, func(n int) {
		if on && n == 2 {
			e.build()
		}
	})
	ctx := context.Background()
	obs, err := p.Observe(ctx, "com.microsoft.VSCode", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !on || e.f.setsOf(attrManualAX) != 1 || e.f.setsOf(attrEnhancedUI) != 0 {
		t.Fatalf("sets = %v", e.f.sets)
	}
	if *slept != 2 {
		t.Fatalf("polled %d times, want 2 (stop once the tree is there)", *slept)
	}
	if n := countIDs(obs.Root); n < sparseElems {
		t.Fatalf("outline still sparse (%d elements): %+v", n, obs.Root)
	}
	// once per pid: observing again neither sets nor waits
	if _, err := p.Observe(ctx, "com.microsoft.VSCode", 0); err != nil {
		t.Fatal(err)
	}
	if e.f.setsOf(attrManualAX) != 1 || *slept != 2 {
		t.Fatalf("second observe switched again: sets %v, slept %d", e.f.sets, *slept)
	}
	p.Close()
	e.f.checkNoLeaks(t)
}

func TestManualAccessibilityOncePerPid(t *testing.T) {
	e := buildElectron(500)
	// the tree never comes: bounded polling, then the sparse outline
	p, slept := axProvider(t, e.f, nil)
	ctx := context.Background()
	obs, err := p.Observe(ctx, "Code", 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := int(axWait / axPoll); *slept != want {
		t.Fatalf("polled %d times, want %d", *slept, want)
	}
	if countIDs(obs.Root) != 3 {
		t.Fatalf("outline = %+v", obs.Root)
	}
	for i := 0; i < 3; i++ {
		if _, err := p.Observe(ctx, "Code", 0); err != nil {
			t.Fatal(err)
		}
	}
	if e.f.setsOf(attrManualAX) != 1 || *slept != int(axWait/axPoll) {
		t.Fatalf("retried on the same pid: sets %v, slept %d", e.f.sets, *slept)
	}
	// relaunched: a new process is tried again
	e.f.apps[0].PID = 501
	e.f.appEl[501] = e.app
	if _, err := p.Observe(ctx, "Code", 0); err != nil {
		t.Fatal(err)
	}
	if e.f.setsOf(attrManualAX) != 2 {
		t.Fatalf("new pid not tried: %v", e.f.sets)
	}
	p.Close()
	e.f.checkNoLeaks(t)
}

func TestEnhancedUserInterfaceFallback(t *testing.T) {
	e := buildElectron(600)
	e.f.setErr = map[string]error{attrManualAX: errors.New("attribute unsupported")}
	p, _ := axProvider(t, e.f, func(int) {})
	if _, err := p.Observe(context.Background(), "Code", 0); err != nil {
		t.Fatal(err)
	}
	if e.f.setsOf(attrManualAX) != 1 || e.f.setsOf(attrEnhancedUI) != 1 {
		t.Fatalf("sets = %v", e.f.sets)
	}
	// neither works: no waiting at all
	e2 := buildElectron(601)
	e2.f.setErr = map[string]error{attrManualAX: errors.New("no"), attrEnhancedUI: errors.New("no")}
	p2, slept := axProvider(t, e2.f, nil)
	if _, err := p2.Observe(context.Background(), "Code", 0); err != nil {
		t.Fatal(err)
	}
	if *slept != 0 {
		t.Fatalf("waited %d times though nothing was switched on", *slept)
	}
	p.Close()
	p2.Close()
	e.f.checkNoLeaks(t)
	e2.f.checkNoLeaks(t)
}

func TestManualAccessibilityNotForNativeApps(t *testing.T) {
	// a native app with a full outline is never touched
	n := buildNotes()
	p, slept := axProvider(t, n.f, nil)
	for i := 0; i < 2; i++ {
		if _, err := p.Observe(context.Background(), "com.apple.Notes", 0); err != nil {
			t.Fatal(err)
		}
	}
	if n.f.setsOf(attrManualAX)+n.f.setsOf(attrEnhancedUI) != 0 || *slept != 0 {
		t.Fatalf("native app switched: %v", n.f.sets)
	}
	// nor is a small window with few controls (a palette, a dialog)
	e := buildElectron(700)
	raw := e.f.nodes[e.win].raw
	raw.W, raw.H = 320, 200
	e.f.nodes[e.win].raw = raw
	p2, _ := axProvider(t, e.f, nil)
	if _, err := p2.Observe(context.Background(), "Code", 0); err != nil {
		t.Fatal(err)
	}
	if e.f.setsOf(attrManualAX) != 0 {
		t.Fatalf("small window switched: %v", e.f.sets)
	}
	p.Close()
	p2.Close()
	n.f.checkNoLeaks(t)
	e.f.checkNoLeaks(t)
}

func countIDs(e computer.Element) int {
	n := 0
	if e.ID != 0 {
		n++
	}
	for _, k := range e.Children {
		n += countIDs(k)
	}
	return n
}
