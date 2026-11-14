package chrome

// Host filtering for every request a page makes (documents, frames,
// scripts, images, XHR / fetch, websockets), through the Fetch domain:
// requests are paused, checked against the agent's filter, and continued
// or failed with BlockedByClient. data:, blob: and about: URLs are local
// and always allowed.

import (
	"context"
	"net/url"
	"strings"
	"sync"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"github.com/hedeqiang/crewbase/capabilities/browser"
)

var _ browser.HostFilter = (*Chrome)(nil)

type filterState struct {
	mu    sync.Mutex
	allow map[string]func(host string) bool // agent → filter (nil = none)
	on    map[*instance]context.CancelFunc  // instances with interception enabled
}

func (c *Chrome) filters() *filterState {
	c.fmu.Do(func() {
		c.fs = &filterState{allow: map[string]func(string) bool{}, on: map[*instance]context.CancelFunc{}}
	})
	return c.fs
}

// SetHostFilter restricts the hosts the agent's browser may load (nil
// lifts the restriction). It applies to the running browser at once and to
// browsers started later.
func (c *Chrome) SetHostFilter(agentID string, allow func(host string) bool) {
	fs := c.filters()
	fs.mu.Lock()
	if allow == nil {
		delete(fs.allow, agentID)
	} else {
		fs.allow[agentID] = allow
	}
	fs.mu.Unlock()
	if in := c.peek(agentID); in != nil {
		c.applyFilter(in)
	}
}

func (c *Chrome) filterFor(agentID string) func(string) bool {
	fs := c.filters()
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.allow[agentID]
}

// applyFilter turns interception on or off for in to match its agent's filter.
func (c *Chrome) applyFilter(in *instance) {
	fs := c.filters()
	want := c.filterFor(in.agent) != nil
	fs.mu.Lock()
	cancel, have := fs.on[in]
	fs.mu.Unlock()
	switch {
	case want && !have:
		lctx, lcancel := context.WithCancel(in.tabCtx)
		paused := make(chan *fetch.EventRequestPaused, 256)
		if err := chromedp.Listen(lctx, func(ev any) {
			if e, ok := ev.(*fetch.EventRequestPaused); ok {
				select {
				case paused <- e:
				default:
					// never block the tab's message loop; an unanswered
					// request stays paused (fails closed)
				}
			}
		}); err != nil {
			lcancel()
			return
		}
		go func() {
			for {
				select {
				case <-lctx.Done():
					return
				case e := <-paused:
					c.decide(lctx, in, e)
				}
			}
		}()
		err := chromedp.Do(in.tabCtx, chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
			_, err := cdp.Call(ctx, t, fetch.Enable, fetch.EnableParams{Patterns: []*fetch.RequestPattern{{URLPattern: "*"}}})
			return err
		}))
		if err != nil {
			lcancel()
			return
		}
		fs.mu.Lock()
		fs.on[in] = lcancel
		fs.mu.Unlock()
	case !want && have:
		_ = chromedp.Do(in.tabCtx, chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
			_, err := cdp.Call(ctx, t, fetch.Disable, cdp.Empty{})
			return err
		}))
		cancel()
		fs.mu.Lock()
		delete(fs.on, in)
		fs.mu.Unlock()
	}
}

// forget drops interception state of a stopped instance.
func (c *Chrome) forgetFilter(in *instance) {
	fs := c.filters()
	fs.mu.Lock()
	if cancel, ok := fs.on[in]; ok {
		cancel()
		delete(fs.on, in)
	}
	fs.mu.Unlock()
}

func localURL(u string) bool {
	return strings.HasPrefix(u, "data:") || strings.HasPrefix(u, "blob:") || strings.HasPrefix(u, "about:") ||
		strings.HasPrefix(u, "chrome-error:") || strings.HasPrefix(u, "devtools:")
}

func (c *Chrome) decide(ctx context.Context, in *instance, e *fetch.EventRequestPaused) {
	allow := c.filterFor(in.agent)
	ok := allow == nil || e.Request == nil || localURL(e.Request.URL)
	if !ok {
		if u, err := url.Parse(e.Request.URL); err == nil {
			ok = allow(u.Hostname())
		}
	}
	_ = chromedp.Do(in.tabCtx, chromedp.Func(func(cctx context.Context, t *chromedp.Target) error {
		if ok {
			_, err := cdp.Call(cctx, t, fetch.ContinueRequest, fetch.ContinueRequestParams{RequestID: e.RequestID})
			return err
		}
		_, err := cdp.Call(cctx, t, fetch.FailRequest, fetch.FailRequestParams{RequestID: e.RequestID, ErrorReason: network.ErrorReasonBlockedByClient})
		return err
	}))
}
