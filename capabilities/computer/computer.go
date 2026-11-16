// Package computer defines the `computer` capability: agents operating the
// person's native apps (docs/computer-use.md). Websites are not its job;
// they go to the browser capability.
//
// A provider (computer-macos; later Windows, Linux) implements Computer.
// Consumers are the model-facing computer_* tools (features/computertools),
// which own the policy: the master switch, per-agent × per-app permission,
// blocked apps, confirmation of consequential actions, auto-pause while the
// person uses the machine, the stop hotkey and the action log.
//
// How it sees: an outline of the target app's frontmost window from the
// accessibility tree, with numbered elements (like browser_snapshot), and,
// only when asked, a screenshot of that window alone. How it acts, most
// structured first: element actions (press, set value, menu items) and
// keyboard events posted to the app's process run in the background without
// moving the person's cursor or focus; coordinate actions need the app in
// front (Act reports Foreground so the caller can ask first and pause on
// user input).
//
// Coordinates are window points: (0,0) is the top-left of the target
// window as Observe reported it, in the same units as Element.Frame and the
// screenshot's Scale (screenshot pixels = points × Scale).
package computer

import (
	"context"
	"errors"
	"time"
)

// ServiceKey is the service key of the computer capability.
const ServiceKey = "computer"

// Errors shared by providers and consumers.
var (
	// ErrUnsupported: this platform or build cannot operate apps.
	ErrUnsupported = errors.New("operating apps is not supported here")
	// ErrNoAccessibility: the app lacks the Accessibility permission.
	ErrNoAccessibility = errors.New("Crewbase needs the Accessibility permission to operate apps")
	// ErrNoScreenRecording: the app lacks the Screen Recording permission
	// (screenshots only; element outlines still work).
	ErrNoScreenRecording = errors.New("Crewbase needs the Screen Recording permission to take screenshots")
	// ErrNoApp: the app is not running (or has no window to act on).
	ErrNoApp = errors.New("the app is not running or has no window")
	// ErrNoElement: the element id is not in the latest outline.
	ErrNoElement = errors.New("no such element in the latest outline; observe again")
	// ErrStale: the app's window changed since the outline was taken.
	ErrStale = errors.New("the window changed since it was observed; observe again")
)

// App is a running application.
type App struct {
	// ID is the bundle identifier ("com.apple.Notes"), the stable key for
	// permissions; Name is what the person sees ("备忘录").
	ID     string `json:"id"`
	Name   string `json:"name"`
	PID    int    `json:"pid"`
	Active bool   `json:"active"` // frontmost
	Hidden bool   `json:"hidden,omitempty"`
}

// Rect is a rectangle in window points.
type Rect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// Element is one node of a window outline.
type Element struct {
	// ID numbers the element within this outline (1, 2, …); 0 for
	// structure-only nodes the model cannot act on.
	ID       int       `json:"id,omitempty"`
	Role     string    `json:"role"`            // button, text field, menu item, row, …
	Name     string    `json:"name,omitempty"`  // title / label / description
	Value    string    `json:"value,omitempty"` // text value, checkbox state, …
	Frame    Rect      `json:"frame"`
	Enabled  bool      `json:"enabled"`
	Focused  bool      `json:"focused,omitempty"`
	Selected bool      `json:"selected,omitempty"`
	Secure   bool      `json:"secure,omitempty"` // a password field: never typed into by agents
	Actions  []string  `json:"actions,omitempty"`
	Children []Element `json:"children,omitempty"`
}

// Window is an app window as observed.
type Window struct {
	ID     int    `json:"id"` // platform window id (screenshots)
	Title  string `json:"title"`
	Frame  Rect   `json:"frame"` // on screen, in points
	Main   bool   `json:"main,omitempty"`
	Screen int    `json:"screen,omitempty"`
}

// Observation is what the model gets about an app.
type Observation struct {
	App     App       `json:"app"`
	Window  Window    `json:"window"`
	Root    Element   `json:"root"`
	Menus   []Element `json:"menus,omitempty"` // the app's menu bar, top level and items
	Version int64     `json:"version"`         // changes when the outline changes; Act checks it
	// Truncated: the outline was cut at the provider's limit.
	Truncated bool      `json:"truncated,omitempty"`
	Time      time.Time `json:"time"`
}

// Screenshot is a PNG of the target window.
type Screenshot struct {
	PNG    []byte  `json:"-"`
	Width  int     `json:"width"`  // pixels
	Height int     `json:"height"` // pixels
	Scale  float64 `json:"scale"`  // pixels per window point after any downscaling
}

// ActionKind names an action.
type ActionKind string

// Actions, most structured first.
const (
	// element actions (background)
	Press    ActionKind = "press"     // Element: its default action (AXPress)
	SetValue ActionKind = "set_value" // Element + Text: replace its value
	Focus    ActionKind = "focus"     // Element: give it keyboard focus
	Menu     ActionKind = "menu"      // Path: a menu bar item, e.g. ["文件", "新建备忘录"]
	// keyboard posted to the app (background)
	Type ActionKind = "type" // Text, into the focused element
	Key  ActionKind = "key"  // Keys: "cmd+n", "return", "down" …
	// coordinates (foreground: the app is brought to the front)
	Click       ActionKind = "click"        // Point (or Element's centre)
	DoubleClick ActionKind = "double_click" // Point
	RightClick  ActionKind = "right_click"  // Point
	Scroll      ActionKind = "scroll"       // Point + DX/DY (lines)
	Drag        ActionKind = "drag"         // Point → To
	// app lifecycle
	Open     ActionKind = "open"     // App ID or name: launch / bring up
	Activate ActionKind = "activate" // bring App to the front
)

// Action is one step on an app.
type Action struct {
	Kind    ActionKind `json:"kind"`
	App     string     `json:"app"`               // bundle id
	Element int        `json:"element,omitempty"` // from the latest Observation
	Version int64      `json:"version,omitempty"` // that Observation's Version (0 = do not check)
	Text    string     `json:"text,omitempty"`
	Keys    string     `json:"keys,omitempty"`
	Path    []string   `json:"path,omitempty"`
	X       float64    `json:"x,omitempty"`
	Y       float64    `json:"y,omitempty"`
	ToX     float64    `json:"toX,omitempty"`
	ToY     float64    `json:"toY,omitempty"`
	DX      int        `json:"dx,omitempty"`
	DY      int        `json:"dy,omitempty"`
}

// Foreground reports whether the action needs the app in front and moves
// the real cursor or keyboard focus (the caller asks first and pauses on
// user input).
func (a Action) Foreground() bool {
	switch a.Kind {
	case Click, DoubleClick, RightClick, Scroll, Drag, Activate:
		return true
	}
	return false
}

// Result is what an action did.
type Result struct {
	// Done is false when the action ran but its effect could not be seen
	// (e.g. the element's value did not change); Note says why.
	Done bool   `json:"done"`
	Note string `json:"note,omitempty"`
}

// Permissions are the system permissions the provider needs.
type Permissions struct {
	Accessibility   bool `json:"accessibility"`
	ScreenRecording bool `json:"screenRecording"`
}

// Computer operates the person's apps. Every method is safe for concurrent
// use; actions on one app are serialised by the provider.
type Computer interface {
	// Permissions reports what the system allows now.
	Permissions() Permissions
	// Apps lists running apps with windows (or menus), frontmost first.
	Apps(ctx context.Context) ([]App, error)
	// Observe outlines app's frontmost window (maxNodes caps the outline;
	// 0 = provider default).
	Observe(ctx context.Context, app string, maxNodes int) (Observation, error)
	// Screenshot captures app's frontmost window only, downscaled so its
	// long edge is at most maxEdge pixels (0 = provider default).
	Screenshot(ctx context.Context, app string, maxEdge int) (Screenshot, error)
	// Act performs one action.
	Act(ctx context.Context, a Action) (Result, error)
	// IdleFor is how long the person has not touched the keyboard or
	// mouse (for pausing foreground actions while they use the machine).
	IdleFor() time.Duration
}
