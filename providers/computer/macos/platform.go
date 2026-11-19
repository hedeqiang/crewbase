package macos

import (
	"errors"

	"github.com/hedeqiang/crewbase/capabilities/computer"
)

// ref is a retained platform object (an AXUIElementRef on macOS). Every ref
// a platform method returns is owned by the caller, who releases it.
type ref uintptr

// rawNode is what the platform reads of one accessibility element.
// Positions are global screen points (top-left origin, y down), as the
// accessibility API and CGEvent use them.
type rawNode struct {
	Role, Subrole                                string
	Title, Description, Label, Help, Placeholder string
	Value                                        string
	HasValue                                     bool
	X, Y, W, H                                   float64
	HasFrame                                     bool
	Enabled                                      bool
	HasEnabled                                   bool
	Focused, Selected, Main                      bool
	Settable                                     bool // AXValue can be set
	Actions                                      []string
	// menu items
	CmdChar string
	CmdMods int
}

// cgWindow is an on-screen window as the window server lists it.
type cgWindow struct {
	ID    int
	PID   int
	Layer int
	Frame computer.Rect
}

// mouseKind is a posted mouse event.
type mouseKind int

const (
	mouseMove mouseKind = iota
	mouseDown
	mouseUp
	mouseDrag
)

type mouseButton int

const (
	buttonLeft mouseButton = iota
	buttonRight
	buttonMiddle
)

// Platform errors, mapped to computer errors by the provider.
var (
	errInvalid     = errors.New("element no longer exists")
	errAPIDisabled = errors.New("accessibility is not allowed")
	errNoValue     = errors.New("no value")
)

// nodeSource reads the accessibility tree (the part the outline builder
// needs, so it runs with fakes).
type nodeSource interface {
	Read(r ref) (rawNode, error)
	// Children returns up to max children (retained) and how many there are.
	Children(r ref, max int) ([]ref, int, error)
	Release(r ref)
}

// platform is everything the provider asks of the operating system.
type platform interface {
	nodeSource

	Trusted() bool       // Accessibility
	ScreenCapture() bool // Screen Recording (preflight only, never prompts)
	Apps() ([]computer.App, error)
	IdleSeconds() float64

	AppElement(pid int) (ref, error)
	SetTimeout(r ref, seconds float64)
	// Attr returns an element-valued attribute (AXFocusedWindow, AXMenuBar…).
	Attr(r ref, name string) (ref, error)
	// Elements returns an array-of-elements attribute (AXWindows…).
	Elements(r ref, name string, max int) ([]ref, error)
	Bool(r ref, name string) (bool, error)
	Perform(r ref, action string) error
	SetString(r ref, attr, v string) error
	SetBool(r ref, attr string, v bool) error

	// WindowNumber is the window server's id of an AX window, or 0.
	WindowNumber(win ref, pid int, frame computer.Rect) int
	Windows(pid int) ([]cgWindow, error) // on screen, front to back
	Screens() []computer.Rect
	Capture(windowID int, path string) error

	Activate(pid int) error
	Launch(app string) error

	PostKey(pid int, code uint16, flags uint64) error
	PostText(pid int, s string) error
	PostMouse(kind mouseKind, b mouseButton, x, y float64, clicks int, flags uint64) error
	PostScroll(x, y float64, dx, dy int, flags uint64) error
}
