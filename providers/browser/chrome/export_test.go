package chrome

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// FakeWindow makes window launches stay headless (no window pops up in
// tests); the switch is otherwise the same.
func FakeWindow(c *Chrome) { c.fakeWindow = true }

// FailWindow makes window launches fail with err.
func FailWindow(c *Chrome, err error) { c.windowErr = err }

// Headed reports whether the agent's running browser was launched for a
// window and its pid.
func Headed(c *Chrome, agent string) (bool, int) {
	in := c.peek(agent)
	if !in.alive() {
		return false, 0
	}
	return !in.headless, in.cmd.Process.Pid
}

// OpenTab opens another tab (as a person would in the window).
func OpenTab(c *Chrome, agent, url string) error {
	in := c.peek(agent)
	if !in.alive() || in.frames == nil {
		return errors.New("not running")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return in.frames.call(ctx, "", "Target.createTarget", map[string]any{"url": url}, nil)
}

// Tabs returns the open tabs as "url@scrollY", the agent's first.
func Tabs(c *Chrome, agent string) ([]string, error) {
	in := c.peek(agent)
	if !in.alive() {
		return nil, errors.New("not running")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cr, err := c.capture(ctx, in, false)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, t := range cr.tabs {
		out = append(out, fmt.Sprintf("%s@%d", t.URL, int(t.Y)))
	}
	return out, nil
}

// KillBrowser kills the agent's browser (a crash, or the window closed).
func KillBrowser(c *Chrome, agent string) {
	if in := c.peek(agent); in.alive() {
		killTree(in.cmd.Process.Pid)
		<-in.exited
	}
}

// DropFrames forgets the agent's frame connection, as when it failed to
// open (attach mode, a timeout).
func DropFrames(c *Chrome, agent string) {
	in := c.peek(agent)
	if in.alive() {
		in.page.frames = nil
	}
}
