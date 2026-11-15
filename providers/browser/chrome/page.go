package chrome

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"

	"github.com/hedeqiang/crewbase/capabilities/browser"
)

// keys maps key names accepted by Page.Key / Input to chromedp key codes.
var keys = map[string]string{
	"Enter": kb.Enter, "Tab": kb.Tab, "Backspace": kb.Backspace, "Delete": kb.Delete,
	"Escape": kb.Escape, "ArrowUp": kb.ArrowUp, "ArrowDown": kb.ArrowDown,
	"ArrowLeft": kb.ArrowLeft, "ArrowRight": kb.ArrowRight, "Home": kb.Home, "End": kb.End,
	"PageUp": kb.PageUp, "PageDown": kb.PageDown,
}

var wheel = cdp.Command[map[string]any, cdp.Empty]{Method: input.CommandDispatchMouseEvent}

// IsKeyName reports whether s is a named key (Enter, Tab, …).
func IsKeyName(s string) bool { _, ok := keys[s]; return ok }

// cdpPage drives one tab through chromedp. Calls take the caller's context
// for cancellation, but run on the tab context (the tab outlives the call).
type cdpPage struct {
	tab    context.Context
	frames *frameConn // nil when frames are out of reach
}

func (p *cdpPage) run(ctx context.Context, fn func(rctx context.Context) error) error {
	rctx, cancel := context.WithCancel(p.tab)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	err := fn(rctx)
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil && p.tab.Err() != nil {
		return fmt.Errorf("the browser was closed: %w", err)
	}
	return err
}

func (p *cdpPage) do(ctx context.Context, a chromedp.Action[chromedp.Void]) error {
	return p.run(ctx, func(rctx context.Context) error { return chromedp.Do(rctx, a) })
}

func (p *cdpPage) Navigate(ctx context.Context, url string) error {
	return p.do(ctx, chromedp.Navigate(url))
}

// Back goes one history entry back without waiting for a load event (a page
// restored from the back/forward cache fires none); callers wait for
// document.readyState themselves.
func (p *cdpPage) Back(ctx context.Context) error {
	return p.do(ctx, chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
		h, err := cdp.Call(ctx, t, page.GetNavigationHistory, cdp.Empty{})
		if err != nil {
			return err
		}
		if h.CurrentIndex <= 0 || int(h.CurrentIndex) >= len(h.Entries) {
			return errors.New("there is no previous page")
		}
		_, err = cdp.Call(ctx, t, page.NavigateToHistoryEntry, page.NavigateToHistoryEntryParams{EntryID: h.Entries[h.CurrentIndex-1].ID})
		return err
	}))
}

func (p *cdpPage) Eval(ctx context.Context, expr string, out any) error {
	return p.run(ctx, func(rctx context.Context) error {
		raw, err := chromedp.Run(rctx, chromedp.Evaluate[json.RawMessage](expr, chromedp.EvalAwaitPromise))
		if err != nil || out == nil {
			return err
		}
		if len(raw) == 0 {
			raw = json.RawMessage("null")
		}
		return json.Unmarshal(raw, out)
	})
}

func (p *cdpPage) Click(ctx context.Context, x, y float64) error {
	return p.do(ctx, chromedp.Steps(
		chromedp.MouseEvent(chromedp.MouseMoved, x, y),
		chromedp.MouseClickXY(x, y),
	))
}

func (p *cdpPage) InsertText(ctx context.Context, text string) error {
	return p.do(ctx, chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
		_, err := cdp.Call(ctx, t, input.InsertText, input.InsertTextParams{Text: text})
		return err
	}))
}

func (p *cdpPage) Key(ctx context.Context, key string) error {
	k, ok := keys[key]
	if !ok {
		return fmt.Errorf("unknown key %q", key)
	}
	return p.do(ctx, chromedp.KeyEvent(k))
}

func (p *cdpPage) Scroll(ctx context.Context, x, y, dy float64) error {
	return p.do(ctx, chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
		// The typed params drop a zero deltaX, which Chrome requires.
		_, err := cdp.Call(ctx, t, wheel, map[string]any{
			"type": input.DispatchMouseEventTypeMouseWheel, "x": x, "y": y, "deltaX": 0, "deltaY": dy,
		})
		return err
	}))
}

func (p *cdpPage) Screenshot(ctx context.Context) ([]byte, error) {
	var png []byte
	err := p.run(ctx, func(rctx context.Context) error {
		var err error
		png, err = chromedp.Run(rctx, chromedp.CaptureScreenshot())
		return err
	})
	return png, err
}

func (p *cdpPage) Location(ctx context.Context) (string, string, error) {
	var v []string
	if err := p.Eval(ctx, `[location.href, document.title]`, &v); err != nil {
		return "", "", err
	}
	if len(v) != 2 {
		return "", "", fmt.Errorf("unexpected location result")
	}
	return v[0], v[1], nil
}

// EvalFrame implements browser.FramePage.
func (p *cdpPage) EvalFrame(ctx context.Context, frame, expr string, out any) error {
	if p.frames == nil {
		return browser.ErrNoFrames
	}
	return p.run(ctx, func(rctx context.Context) error { return p.frames.eval(rctx, frame, expr, out) })
}

// ChildFrame implements browser.FramePage.
func (p *cdpPage) ChildFrame(ctx context.Context, frame string, index int) (string, error) {
	if p.frames == nil {
		return "", browser.ErrNoFrames
	}
	var id string
	err := p.run(ctx, func(rctx context.Context) error {
		var err error
		id, err = p.frames.child(rctx, frame, index)
		return err
	})
	return id, err
}

var _ browser.FramePage = (*cdpPage)(nil)

var _ browser.Page = (*cdpPage)(nil)
