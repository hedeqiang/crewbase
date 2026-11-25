// Package approval defines the `approval` capability: one-shot approval
// requests with a closed outcome set. Only OutcomeAllowedOnce lets a call
// proceed. No responder, a responder error or an invalid answer all mean
// OutcomeUnavailable (fail closed). See docs/06-security-model.md §4.
package approval

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/kernel"
)

const ServiceKey = "approval"

type Outcome string

const (
	OutcomeAllowedOnce Outcome = "allowed-once"
	OutcomeRejected    Outcome = "rejected"
	OutcomeCancelled   Outcome = "cancelled"
	OutcomeUnavailable Outcome = "unavailable"
)

// Valid reports whether o is in the closed set.
func (o Outcome) Valid() bool {
	switch o {
	case OutcomeAllowedOnce, OutcomeRejected, OutcomeCancelled, OutcomeUnavailable:
		return true
	}
	return false
}

// Request references an already-recorded tool call by id; it does not copy
// the arguments (the card attaches to the streamed call).
type Request struct {
	CallID   string `json:"callId"`
	Session  string `json:"session"`
	Agent    string `json:"agent"`
	Tool     string `json:"tool"`
	Category string `json:"category"`
	Summary  string `json:"summary"` // human-readable "what it wants to do"
	Reason   string `json:"reason"`  // matched rule
	// Preview is longer optional context shown on demand (e.g. the output
	// a workflow gate is about to pass on); Summary stays one line.
	Preview string    `json:"preview,omitempty"`
	Asked   time.Time `json:"asked"`
	// Rememberable is false for categories that may never be remembered
	// (external payment); ConfirmRemember asks for a second confirmation.
	Rememberable    bool `json:"rememberable"`
	ConfirmRemember bool `json:"confirmRemember"`
	// Elicit is set when the request is a question for the person rather
	// than a yes or no on the call: an MCP server asking for input mid-call
	// (MCP elicitation). CallID is then its own id, and Parent the tool
	// call it belongs to.
	Elicit *Elicitation `json:"elicit,omitempty"`
	Parent string       `json:"parent,omitempty"`
}

// Elicitation is what an MCP server asks the person (MCP elicitation):
// a form (Schema: a flat JSON Schema of strings, numbers, booleans and
// enums) or a link to open (URL), with why it asks.
type Elicitation struct {
	Server  string          `json:"server"` // the MCP server, as configured here
	Mode    string          `json:"mode"`   // form | url
	Message string          `json:"message"`
	Schema  json.RawMessage `json:"schema,omitempty"`
	URL     string          `json:"url,omitempty"`
}

// Decision is a responder's answer.
type Decision struct {
	Outcome  Outcome `json:"outcome"`
	Message  string  `json:"message,omitempty"`  // rejection reason passed to the agent
	Remember bool    `json:"remember,omitempty"` // "always allow" for this agent+category
	// Confirmed is the second confirmation required when the request has
	// ConfirmRemember (e.g. external publishing). Without it Remember is
	// dropped by the service.
	Confirmed bool   `json:"confirmed,omitempty"`
	By        string `json:"by,omitempty"` // responder / person
	// Values are what the person filled in, for an elicitation form.
	Values map[string]any `json:"values,omitempty"`
}

// Responder answers approval requests. ok=false means "not mine".
type Responder interface {
	Name() string
	Respond(ctx context.Context, req Request) (Decision, bool)
}

// Policy values recorded as approval/policy session events.
const (
	PolicyAsk   = "ask"
	PolicyNever = "never"
)

type PolicyData struct {
	Policy string `json:"policy"`
}

type DecidedData struct {
	CallID string `json:"callId"`
	Decision
}

// Asked/Decided are emitted (scoped to the agent) for UI and notifications.
var (
	Asked   = kernel.NewEmit[Request]("approval/asked")
	Decided = kernel.NewEmit[DecidedData]("approval/decided")
)

// Service is the `approval` service.
type Service struct {
	host       *kernel.Host
	timeout    time.Duration
	mu         sync.Mutex
	responders []Responder
}

func NewService(h *kernel.Host, timeout time.Duration) *Service {
	return &Service{host: h, timeout: timeout}
}

// AddResponder registers a responder for ctx's lifetime.
func (s *Service) AddResponder(c *kernel.Context, r Responder) {
	s.mu.Lock()
	s.responders = append(s.responders, r)
	s.mu.Unlock()
	c.Defer(func() {
		s.mu.Lock()
		for i, x := range s.responders {
			if x == r {
				s.responders = append(s.responders[:i:i], s.responders[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
	})
}

// Policy returns the session's current approval policy.
func Policy(ss *session.Session) string {
	if e, ok := ss.Last(session.TypeApprovalPolicy); ok {
		var p PolicyData
		if e.Decode(&p) == nil && p.Policy != "" {
			return p.Policy
		}
	}
	return PolicyAsk
}

var errAudit = errors.New("approval: audit write failed")

// Ask dispatches req and blocks until a decision. The `never` policy is
// enforced here, before dispatch, so no responder can bypass it. Both the
// question and the answer are written to the session log; if either write
// fails the result is unavailable.
func (s *Service) Ask(ctx context.Context, ss *session.Session, req Request) Decision {
	if req.Asked.IsZero() {
		req.Asked = time.Now().UTC()
	}
	if Policy(ss) == PolicyNever {
		d := Decision{Outcome: OutcomeRejected, Message: "approval policy is never", By: "policy"}
		_, _ = ss.Append(session.TypeApprovalDecided, req.Agent, DecidedData{CallID: req.CallID, Decision: d})
		return d
	}
	if _, err := ss.Append(session.TypeApprovalAsked, req.Agent, req); err != nil {
		return Decision{Outcome: OutcomeUnavailable, Message: errAudit.Error()}
	}
	Asked.Emit(kernel.For(s.host, req.Agent), req)

	d := s.dispatch(ctx, req)
	if !d.Outcome.Valid() {
		d = Decision{Outcome: OutcomeUnavailable, Message: "invalid responder answer"}
	}
	if d.Outcome != OutcomeAllowedOnce {
		d.Remember = false
	}
	if !req.Rememberable || (req.ConfirmRemember && !d.Confirmed) || req.Elicit != nil {
		d.Remember = false
	}
	if req.Elicit == nil || d.Outcome != OutcomeAllowedOnce {
		d.Values = nil
	}
	dd := DecidedData{CallID: req.CallID, Decision: d}
	if _, err := ss.Append(session.TypeApprovalDecided, req.Agent, dd); err != nil {
		return Decision{Outcome: OutcomeUnavailable, Message: errAudit.Error()}
	}
	Decided.Emit(kernel.For(s.host, req.Agent), dd)
	return d
}

func (s *Service) dispatch(ctx context.Context, req Request) Decision {
	s.mu.Lock()
	rs := append([]Responder(nil), s.responders...)
	s.mu.Unlock()
	if len(rs) == 0 {
		return Decision{Outcome: OutcomeUnavailable, Message: "no approver available"}
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if s.timeout > 0 {
		var c2 context.CancelFunc
		cctx, c2 = context.WithTimeout(cctx, s.timeout)
		defer c2()
	}
	type res struct {
		d  Decision
		ok bool
	}
	ch := make(chan res, len(rs))
	for _, r := range rs {
		go func(r Responder) {
			defer func() {
				if rec := recover(); rec != nil {
					ch <- res{}
				}
			}()
			d, ok := r.Respond(cctx, req)
			if ok && d.By == "" {
				d.By = r.Name()
			}
			ch <- res{d, ok}
		}(r)
	}
	for range rs {
		select {
		case x := <-ch:
			if x.ok {
				return x.d // first responder to answer claims it
			}
		case <-cctx.Done():
			if ctx.Err() != nil {
				return Decision{Outcome: OutcomeCancelled, Message: "tool call cancelled"}
			}
			return Decision{Outcome: OutcomeUnavailable, Message: "approval timed out"}
		}
	}
	return Decision{Outcome: OutcomeUnavailable, Message: "no approver answered"}
}

// Inbox is the desktop responder: requests wait here until a person decides
// through the UI/API. It is also the source for the "needs you" list.
type Inbox struct {
	mu      sync.Mutex
	pending map[string]*pendingItem
	changed func()
}

type pendingItem struct {
	req Request
	ch  chan Decision
}

func NewInbox(onChange func()) *Inbox {
	return &Inbox{pending: map[string]*pendingItem{}, changed: onChange}
}

func (b *Inbox) Name() string { return "desktop" }

func (b *Inbox) notify() {
	if b.changed != nil {
		b.changed()
	}
}

func (b *Inbox) Respond(ctx context.Context, req Request) (Decision, bool) {
	it := &pendingItem{req: req, ch: make(chan Decision, 1)}
	b.mu.Lock()
	b.pending[req.CallID] = it
	b.mu.Unlock()
	b.notify()
	defer func() {
		b.mu.Lock()
		delete(b.pending, req.CallID)
		b.mu.Unlock()
		b.notify()
	}()
	select {
	case d := <-it.ch:
		return d, true
	case <-ctx.Done():
		return Decision{}, false
	}
}

// Pending lists waiting requests, oldest first.
func (b *Inbox) Pending() []Request {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Request, 0, len(b.pending))
	for _, it := range b.pending {
		out = append(out, it.req)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Asked.Before(out[j-1].Asked); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Decide answers a pending request. Returns false if it is not pending.
func (b *Inbox) Decide(callID string, d Decision) bool {
	b.mu.Lock()
	it, ok := b.pending[callID]
	b.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case it.ch <- d:
		return true
	default:
		return false
	}
}

// InboxKey is the service key of the desktop inbox.
const InboxKey = "approval.inbox"

// Changed is emitted when the inbox's pending set changes (no delta).
var Changed = kernel.NewEmit[struct{}]("approval/changed")

// Plugin provides the approval service. Config: {"timeoutSeconds": N}
// (0 = wait indefinitely; the paused call does not block other agents).
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/approval",
	Description: "Approval service: closed outcome set, fail-closed.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[struct {
			TimeoutSeconds int `json:"timeoutSeconds"`
		}](c)
		if err != nil {
			return err
		}
		return c.Provide(ServiceKey, NewService(c.Host(), time.Duration(cfg.TimeoutSeconds)*time.Second))
	},
})

// DesktopPlugin registers the desktop inbox responder.
var DesktopPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/approval-desktop",
	Inject:      []string{ServiceKey},
	Description: "Desktop approval cards (inbox responder).",
	Apply: func(c *kernel.Context) error {
		h := c.Host()
		in := NewInbox(func() { Changed.Emit(h, struct{}{}) })
		kernel.Use[*Service](c, ServiceKey).AddResponder(c, in)
		return c.Provide(InboxKey, in)
	},
})
