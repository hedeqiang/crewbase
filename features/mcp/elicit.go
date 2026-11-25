package mcp

// MCP elicitation (spec 2026-07-28, client/elicitation): a server asks the
// person for input in the middle of a tool call, as a form or as a link to
// open, then carries on. Supabase confirms a cost, GitHub has you type the
// repository's name before deleting it.
//
// Crewbase asks through the approval cards: the question attaches to the
// tool call it belongs to, in the conversation and in 需要你, with the
// server's name on it. A form is filled in there; a link is shown in full
// and opened in the system browser only when the person says so. Chat apps
// (IM), which have only yes and no, never answer one. Time spent waiting
// for the person does not count against the call's timeout.
//
// A question arrives with the call it belongs to in its context (a
// 2026-07-28 server returns it as input the retried call needs), or as a
// request of its own on the connection (older servers); then it belongs to
// the call in flight on that server, and is declined when calls of several
// agents are.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

// CategoryElicit is the category of a question that has no tool call to
// take one from.
const CategoryElicit = "elicitation"

// activeCall is an MCP tool call under way.
type activeCall struct {
	call     tools.Call
	category string
	clock    *callClock
	asked    atomic.Int32
	started  time.Time
}

type activeKey struct{}

func withActive(ctx context.Context, a *activeCall) context.Context {
	return context.WithValue(ctx, activeKey{}, a)
}

func activeFrom(ctx context.Context) *activeCall {
	a, _ := ctx.Value(activeKey{}).(*activeCall)
	return a
}

// inflight are the calls under way per server.
type inflight struct {
	mu    sync.Mutex
	calls map[string]map[*activeCall]struct{}
}

func (f *inflight) add(server string, a *activeCall) func() {
	f.mu.Lock()
	if f.calls == nil {
		f.calls = map[string]map[*activeCall]struct{}{}
	}
	if f.calls[server] == nil {
		f.calls[server] = map[*activeCall]struct{}{}
	}
	f.calls[server][a] = struct{}{}
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		delete(f.calls[server], a)
		f.mu.Unlock()
	}
}

// sole is the call a question on server belongs to: the latest one, when
// all calls under way there are one agent's.
func (f *inflight) sole(server string) *activeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var last *activeCall
	agent := ""
	for a := range f.calls[server] {
		if agent != "" && a.call.Agent != agent {
			return nil
		}
		agent = a.call.Agent
		if last == nil || a.started.After(last.started) {
			last = a
		}
	}
	return last
}

// callClock is a call's timeout that stops while the person is asked.
type callClock struct {
	mu      sync.Mutex
	timer   *time.Timer
	left    time.Duration
	since   time.Time
	paused  int
	expired bool
}

// errCallTimeout ends a call that ran out of time.
var errCallTimeout = fmt.Errorf("the MCP call timed out")

// newCallClock is ctx with a timeout of d that pause and resume can stop.
func newCallClock(ctx context.Context, d time.Duration) (context.Context, *callClock, func()) {
	cctx, cancel := context.WithCancelCause(ctx)
	k := &callClock{left: d, since: time.Now()}
	k.timer = time.AfterFunc(d, func() {
		k.mu.Lock()
		k.expired = true
		k.mu.Unlock()
		cancel(errCallTimeout)
	})
	return cctx, k, func() { k.timer.Stop(); cancel(context.Canceled) }
}

func (k *callClock) pause() {
	if k == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.paused++
	if k.paused == 1 && !k.expired && k.timer.Stop() {
		k.left -= time.Since(k.since)
	}
}

func (k *callClock) resume() {
	if k == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.paused--
	if k.paused == 0 && !k.expired {
		k.since = time.Now()
		k.timer.Reset(max(k.left, time.Second))
	}
}

// elicitationCaps: forms and links.
func elicitationCaps() *sdk.ElicitationCapabilities {
	return &sdk.ElicitationCapabilities{Form: &sdk.FormElicitationCapabilities{}, URL: &sdk.URLElicitationCapabilities{}}
}

// elicitHandler answers server's questions.
func (s *Service) elicitHandler(server string) func(context.Context, *sdk.ElicitRequest) (*sdk.ElicitResult, error) {
	return func(ctx context.Context, req *sdk.ElicitRequest) (*sdk.ElicitResult, error) {
		return s.elicit(ctx, server, req.Params)
	}
}

var (
	cancelled = &sdk.ElicitResult{Action: "cancel"}
	declined  = &sdk.ElicitResult{Action: "decline"}
)

func (s *Service) elicit(ctx context.Context, server string, p *sdk.ElicitParams) (*sdk.ElicitResult, error) {
	if p == nil {
		return cancelled, nil
	}
	a := activeFrom(ctx)
	if a == nil {
		a = s.flight.sole(server)
	}
	if a == nil || a.call.Session == nil {
		// no call to attach it to (a probe, or calls of several agents)
		return cancelled, nil
	}
	ap, ok := kernel.Lookup[*approval.Service](s.c, approval.ServiceKey)
	if !ok {
		return cancelled, nil
	}
	mode := p.Mode
	if mode == "" {
		mode = "form"
	}
	el := &approval.Elicitation{Server: server, Mode: mode, Message: clip(strings.TrimSpace(p.Message), 2000)}
	switch mode {
	case "form":
		if p.RequestedSchema != nil {
			b, err := json.Marshal(p.RequestedSchema)
			if err != nil {
				return cancelled, nil
			}
			el.Schema = b
		}
	case "url":
		if !openableURL(p.URL) {
			return declined, nil
		}
		el.URL = p.URL
	default:
		return declined, nil
	}
	n := a.asked.Add(1)
	cat := a.category
	if cat == "" {
		cat = CategoryElicit
	}
	req := approval.Request{
		CallID:   fmt.Sprintf("%s~ask%d", a.call.ID, n),
		Parent:   a.call.ID,
		Session:  a.call.Session.ID(),
		Agent:    a.call.Agent,
		Tool:     a.call.Name,
		Category: cat,
		Summary:  fmt.Sprintf("%s asks: %s", server, clip(firstLine(el.Message), 160)),
		Reason:   "mcp: " + server + " asked during the call",
		Elicit:   el,
	}
	a.clock.pause()
	defer a.clock.resume()
	d := ap.Ask(ctx, a.call.Session, req)
	switch d.Outcome {
	case approval.OutcomeAllowedOnce:
		if mode == "url" {
			return &sdk.ElicitResult{Action: "accept"}, nil
		}
		return &sdk.ElicitResult{Action: "accept", Content: d.Values}, nil
	case approval.OutcomeRejected:
		return declined, nil
	}
	return cancelled, nil
}

// openableURL: a link the person may be asked to open: https, or http on
// this machine (a server being developed).
func openableURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		h := u.Hostname()
		if h == "localhost" {
			return true
		}
		ip := net.ParseIP(h)
		return ip != nil && ip.IsLoopback()
	}
	return false
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}
