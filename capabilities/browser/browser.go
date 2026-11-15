// Package browser defines the `browser` capability: one browser per agent,
// with its own persistent profile, that a person can take over (docs/01 A-1,
// S-5; docs/06 §6).
//
// Providers (browser-chrome, later a remote browser) implement Browser and
// drive pages through the Page interface. Consumers are the model-facing
// browser_* tools and the takeover API. The takeover rules live here, in
// Control, so that every provider enforces them the same way:
//
//   - While a person controls an agent's browser, every agent operation fails
//     with ErrTakenOver and nothing about the page is returned. An agent
//     operation that was in flight when the takeover started is cancelled and
//     its result is discarded.
//   - Screenshot and Input are for the person only; the model never receives
//     a screenshot.
//   - Login state lives in the browser profile, never in logs.
package browser

import (
	"context"
	"errors"
	"time"

	"github.com/hedeqiang/crewbase/kernel"
)

// ServiceKey is the service key of the browser capability.
const ServiceKey = "browser"

// Errors shared by providers and consumers.
var (
	// ErrTakenOver means a person controls the browser; the agent must
	// wait until control is handed back.
	ErrTakenOver = errors.New("the user is controlling the browser")
	// ErrNotTakenOver is returned by Handback / Input when nobody holds
	// the browser.
	ErrNotTakenOver = errors.New("the browser is not taken over")
	// ErrNotController is returned when someone other than the person who
	// took over tries to act on the browser.
	ErrNotController = errors.New("the browser is controlled by someone else")
	// ErrNotRunning is returned by Screenshot / Input when the agent has no
	// running browser.
	ErrNotRunning = errors.New("the browser is not running")
	// ErrClosed is returned after the provider has been unloaded.
	ErrClosed = errors.New("the browser provider is closed")
	// ErrNoFrames is returned by frame operations on a page that cannot
	// reach into iframes.
	ErrNoFrames = errors.New("this browser cannot reach into frames")
	// ErrNoWindow is returned by Windower.SetWindow when no desktop
	// window can be shown (attached or already windowed browser, no
	// display, or the window failed to open).
	ErrNoWindow = errors.New("the browser cannot open a window here")
)

// State is a snapshot of one agent's browser.
type State struct {
	Agent   string `json:"agent"`
	Running bool   `json:"running"`
	URL     string `json:"url,omitempty"`
	Title   string `json:"title,omitempty"`
	// TakenOverBy is the person (caller identity) controlling the browser,
	// "" when the agent controls it.
	TakenOverBy string    `json:"takenOverBy,omitempty"`
	Since       time.Time `json:"since,omitzero"` // takeover start
	Headless    bool      `json:"headless"`
	Attached    bool      `json:"attached,omitempty"`
	// View tells the takeover UI how to show the page: "frames" means
	// frame-by-frame screenshots plus Input (headless browsers), "window"
	// means a real browser window is visible on the desktop.
	View string `json:"view,omitempty"`
	// Window is true while a takeover runs in a desktop window that a
	// headless browser was switched to (Windower); View is then "window".
	Window bool `json:"window,omitempty"`
	// CanWindow is true when a takeover can switch to a desktop window.
	CanWindow bool `json:"canWindow,omitempty"`
}

// Windower is implemented by browsers whose takeover can move from the
// frame view to a real window on the desktop and back, keeping the profile
// and the open tabs. The agent stays paused throughout; hand-back returns
// the browser to headless.
type Windower interface {
	// SetWindow moves a takeover held by `by` to a desktop window (open)
	// or back to the frame view.
	SetWindow(agentID, by string, open bool) error
}

// Input is one human input during a takeover.
type Input struct {
	Type string  `json:"type"` // click | key | navigate | scroll
	X    float64 `json:"x,omitempty"`
	Y    float64 `json:"y,omitempty"`
	// Text is the text to insert (key) or a key name: Enter, Tab,
	// Backspace, Delete, Escape, ArrowUp, ArrowDown, ArrowLeft, ArrowRight.
	Text string  `json:"text,omitempty"`
	URL  string  `json:"url,omitempty"`
	DY   float64 `json:"dy,omitempty"`
}

// Input types.
const (
	InputClick    = "click"
	InputKey      = "key"
	InputNavigate = "navigate"
	InputScroll   = "scroll"
)

// Browser is the `browser` service.
// HostFilter is implemented by browsers that can restrict every request a
// page makes (not only top-level navigation) to allowed hosts.
type HostFilter interface {
	// SetHostFilter restricts the agent's browser to hosts allow accepts;
	// nil lifts the restriction.
	SetHostFilter(agentID string, allow func(host string) bool)
}

type Browser interface {
	// Session returns the agent's browser session, starting the browser on
	// demand. It fails with ErrTakenOver while a person controls it.
	Session(ctx context.Context, agentID string) (*Session, error)
	// Release closes the agent's browser (attached browsers are only
	// disconnected). An active takeover ends.
	Release(agentID string)
	// Takeover gives control to a person (by identifies the caller). The
	// browser is started if it is not running. Taking over again by the
	// same person is a no-op; by someone else fails with ErrNotController.
	Takeover(agentID, by string) error
	// Handback returns control to the agent.
	Handback(agentID string) error
	// State reports the agent's browser state.
	State(agentID string) State
	// Screenshot captures the visible page as PNG. It is for the person's
	// takeover view only and never goes to the model.
	Screenshot(agentID string) ([]byte, error)
	// Input applies a person's input; only the person who took over may.
	// The input is never logged.
	Input(agentID, by string, in Input) error
	// AwaitHandback blocks until a takeover of the agent's browser that is
	// active now, or starts later, has ended, or ctx is done.
	AwaitHandback(ctx context.Context, agentID string) (State, error)
}

// Page drives one browser tab. Providers implement it; consumers reach it
// only through a Session, which enforces the takeover rules.
type Page interface {
	Navigate(ctx context.Context, url string) error
	Back(ctx context.Context) error
	// Eval evaluates a JavaScript expression (promises are awaited) and
	// decodes its JSON value into out (which may be nil).
	Eval(ctx context.Context, expr string, out any) error
	Click(ctx context.Context, x, y float64) error
	InsertText(ctx context.Context, text string) error
	// Key presses a named key (see Input.Text).
	Key(ctx context.Context, key string) error
	Scroll(ctx context.Context, x, y, dy float64) error
	Screenshot(ctx context.Context) ([]byte, error)
	Location(ctx context.Context) (url, title string, err error)
}

// FramePage is implemented by pages that reach into iframes, same-origin
// and cross-origin (out-of-process) alike. A frame is an opaque id; "" is
// the main frame. Scripts run in an isolated world of the frame, so state a
// script leaves on window (element refs) is invisible to the page and kept
// between calls until the frame navigates.
type FramePage interface {
	// EvalFrame evaluates expr in frame like Page.Eval.
	EvalFrame(ctx context.Context, frame, expr string, out any) error
	// ChildFrame returns the frame shown by the element
	// window.__crewbaseFrames[index] that a script left in frame.
	ChildFrame(ctx context.Context, frame string, index int) (string, error)
}

// TakeoverEvent is the payload of the takeover events.
type TakeoverEvent struct {
	Agent string    `json:"agent"`
	By    string    `json:"by"`
	At    time.Time `json:"at"`
}

var (
	// TakeoverStarted / TakeoverEnded are dispatched in the agent's scope.
	TakeoverStarted = kernel.NewEmit[TakeoverEvent]("browser/takeover-started")
	TakeoverEnded   = kernel.NewEmit[TakeoverEvent]("browser/takeover-ended")
	// Changed reports a state change (started, stopped, taken over, handed
	// back) for live UIs.
	Changed = kernel.NewEmit[State]("browser/changed")
)
