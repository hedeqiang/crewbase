package macos

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/computer"
)

// fake is an in-memory platform: a tree of nodes with retain counts, so
// tests can check that every ref handed out is released.
type fake struct {
	mu      sync.Mutex
	nodes   map[ref]*fnode
	counts  map[ref]int
	next    ref
	trusted bool
	screen  bool
	apps    []computer.App
	appEl   map[int]ref
	screens []computer.Rect
	wins    []cgWindow

	keys      []keyStroke
	texts     []string
	mouse     []string
	performed []string
	sets      []string
	activated int
	lastInput time.Time
	// onPerform runs when an action is performed (to change the tree).
	onPerform func(f *fake, r ref, action string)
	// setErr fails SetBool for an attribute; onSet runs after a SetBool.
	setErr map[string]error
	onSet  func(f *fake, r ref, attr string, v bool)
}

type fnode struct {
	raw   rawNode
	kids  []ref
	attrs map[string]ref
	dead  bool
}

func newFake() *fake {
	return &fake{nodes: map[ref]*fnode{}, counts: map[ref]int{}, next: 100, trusted: true, screen: true, appEl: map[int]ref{},
		screens: []computer.Rect{{X: 0, Y: 0, W: 1440, H: 900}}, lastInput: time.Now().Add(-10 * time.Second)}
}

// add creates a node under parent (0 for none) and returns its ref.
func (f *fake) add(parent ref, raw rawNode) ref {
	f.next++
	r := f.next
	f.nodes[r] = &fnode{raw: raw, attrs: map[string]ref{}}
	if parent != 0 {
		f.nodes[parent].kids = append(f.nodes[parent].kids, r)
	}
	return r
}

func (f *fake) retain(r ref) ref { f.counts[r]++; return r }

func (f *fake) Release(r ref) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.counts[r] <= 0 {
		panic(fmt.Sprintf("over-release of %d", r))
	}
	f.counts[r]--
}

func (f *fake) leaks() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.counts {
		n += c
	}
	return n
}

func (f *fake) checkNoLeaks(t *testing.T) {
	t.Helper()
	if n := f.leaks(); n != 0 {
		t.Fatalf("%d refs not released: %v", n, f.counts)
	}
}

func (f *fake) Read(r ref) (rawNode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.nodes[r]
	if n == nil || n.dead {
		return rawNode{}, errInvalid
	}
	return n.raw, nil
}

func (f *fake) Children(r ref, max int) ([]ref, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.nodes[r]
	if n == nil || n.dead {
		return nil, 0, errInvalid
	}
	var out []ref
	for i, k := range n.kids {
		if i >= max {
			break
		}
		out = append(out, f.retain(k))
	}
	return out, len(n.kids), nil
}

func (f *fake) Trusted() bool                 { return f.trusted }
func (f *fake) ScreenCapture() bool           { return f.screen }
func (f *fake) Apps() ([]computer.App, error) { return f.apps, nil }
func (f *fake) IdleSeconds() float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return time.Since(f.lastInput).Seconds()
}

// input records input to the system (posted events count too).
func (f *fake) input() {
	f.mu.Lock()
	f.lastInput = time.Now()
	f.mu.Unlock()
}

func (f *fake) AppElement(pid int) (ref, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.appEl[pid]
	if !ok {
		return 0, computer.ErrNoApp
	}
	return f.retain(r), nil
}

func (f *fake) SetTimeout(ref, float64) {}

func (f *fake) Attr(r ref, name string) (ref, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.nodes[r]
	if n == nil {
		return 0, errInvalid
	}
	a, ok := n.attrs[name]
	if !ok {
		return 0, errNoValue
	}
	return f.retain(a), nil
}

func (f *fake) Elements(r ref, name string, max int) ([]ref, error) {
	if name == "AXWindows" {
		w, err := f.Attr(r, "AXFocusedWindow")
		if err != nil {
			return nil, nil
		}
		return []ref{w}, nil
	}
	return nil, nil
}

func (f *fake) Bool(r ref, name string) (bool, error) {
	raw, err := f.Read(r)
	if err != nil {
		return false, err
	}
	switch name {
	case "AXFocused":
		return raw.Focused, nil
	case "AXFrontmost":
		return f.activated > 0, nil
	}
	return false, errNoValue
}

func (f *fake) Perform(r ref, action string) error {
	f.mu.Lock()
	f.performed = append(f.performed, fmt.Sprintf("%d:%s", r, action))
	cb := f.onPerform
	f.mu.Unlock()
	if cb != nil {
		cb(f, r, action)
	}
	return nil
}

func (f *fake) SetString(r ref, attr, v string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets = append(f.sets, fmt.Sprintf("%d:%s=%s", r, attr, v))
	if attr == "AXValue" && f.nodes[r].raw.Settable {
		f.nodes[r].raw.Value = v
	}
	return nil
}

func (f *fake) SetBool(r ref, attr string, v bool) error {
	f.mu.Lock()
	f.sets = append(f.sets, fmt.Sprintf("%d:%s=%t", r, attr, v))
	if err := f.setErr[attr]; err != nil {
		f.mu.Unlock()
		return err
	}
	if attr == "AXFocused" {
		f.nodes[r].raw.Focused = v
	}
	cb := f.onSet
	f.mu.Unlock()
	if cb != nil {
		cb(f, r, attr, v)
	}
	return nil
}

func (f *fake) WindowNumber(ref, int, computer.Rect) int { return 77 }
func (f *fake) Windows(int) ([]cgWindow, error)          { return f.wins, nil }
func (f *fake) Screens() []computer.Rect                 { return f.screens }
func (f *fake) Capture(int, string) error                { return nil }
func (f *fake) Activate(int) error                       { f.activated++; return nil }
func (f *fake) Launch(string) error                      { return nil }

func (f *fake) PostKey(pid int, code uint16, flags uint64) error {
	f.input()
	f.keys = append(f.keys, keyStroke{code, flags})
	return nil
}

func (f *fake) PostText(pid int, s string) error {
	f.input()
	f.texts = append(f.texts, s)
	return nil
}

func (f *fake) PostMouse(kind mouseKind, b mouseButton, x, y float64, clicks int) error {
	f.input()
	f.mouse = append(f.mouse, fmt.Sprintf("%d/%d@%g,%g", kind, b, x, y))
	return nil
}

func (f *fake) PostScroll(x, y float64, dx, dy int) error {
	f.input()
	f.mouse = append(f.mouse, fmt.Sprintf("scroll@%g,%g %d,%d", x, y, dx, dy))
	return nil
}

var _ platform = (*fake)(nil)
