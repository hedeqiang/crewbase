package tools

// Parking: a turn waiting for an approval can stop waiting so the agent
// handles other work (another conversation, an IM thread, a background
// result). The approval request itself is not touched — it stays open on
// every responder (desktop card, IM) until a person decides — only the
// tool call ends, with ParkedResult. The decision is emitted as
// ParkedDecided; if it allows the call, a one-time grant lets the same call
// (agent, session, tool, identical arguments) run once without asking again
// within GrantTTL, so the agent can pick the work up where it stopped.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/kernel"
)

// ParkedResult is what the model sees for a call parked while waiting.
const ParkedResult = "Waiting for the user's approval (they have not answered yet). Do not wait, retry or work around it: " +
	"tell the user in one short line that this is waiting for their approval, then end your turn. " +
	"When they decide you will get a message and can continue from here."

// GrantTTL bounds how long an approval given to a parked call stays usable.
const GrantTTL = time.Hour

// ParkedDecision is a person's answer to an approval whose call was parked.
type ParkedDecision struct {
	Agent    string
	Session  string
	CallID   string
	Tool     string
	Summary  string
	Decision approval.Decision
}

// ParkedDecided is emitted (scoped to the agent) when a parked call's
// approval is decided.
var ParkedDecided = kernel.NewEmit[ParkedDecision]("tools/parked-decided")

type parking struct {
	mu      sync.Mutex
	waiting map[string]map[chan struct{}]struct{} // agent → park signals of calls waiting for approval
	sticky  map[string]bool                       // agent parked: later waits in the same turn park at once
	grants  map[string]time.Time                  // grantKey → expiry
}

func grantKey(agent, session, tool string, args []byte) string {
	h := sha256.Sum256(args)
	return agent + "\x00" + session + "\x00" + tool + "\x00" + hex.EncodeToString(h[:])
}

func (p *parking) wait(agent string) chan struct{} {
	ch := make(chan struct{})
	p.mu.Lock()
	if p.sticky[agent] {
		p.mu.Unlock()
		close(ch)
		return ch
	}
	if p.waiting == nil {
		p.waiting = map[string]map[chan struct{}]struct{}{}
	}
	if p.waiting[agent] == nil {
		p.waiting[agent] = map[chan struct{}]struct{}{}
	}
	p.waiting[agent][ch] = struct{}{}
	p.mu.Unlock()
	return ch
}

func (p *parking) done(agent string, ch chan struct{}) {
	p.mu.Lock()
	if p.waiting[agent] == nil {
		p.mu.Unlock()
		return
	}
	delete(p.waiting[agent], ch)
	if len(p.waiting[agent]) == 0 {
		delete(p.waiting, agent)
	}
	p.mu.Unlock()
}

// Park stops agent's calls that are waiting for an approval from waiting
// (their requests stay open). It reports whether any call was waiting.
func (r *Registry) Park(agent string) bool {
	p := &r.park
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for ch := range p.waiting[agent] {
		close(ch)
		n++
	}
	delete(p.waiting, agent)
	if n > 0 {
		if p.sticky == nil {
			p.sticky = map[string]bool{}
		}
		p.sticky[agent] = true
	}
	return n > 0
}

// Unpark ends the parking Park started (the turn is over): the next turn
// waits for its approvals again.
func (r *Registry) Unpark(agent string) {
	r.park.mu.Lock()
	delete(r.park.sticky, agent)
	r.park.mu.Unlock()
}

// Waiting reports whether agent has a call waiting for an approval.
func (r *Registry) Waiting(agent string) bool {
	r.park.mu.Lock()
	defer r.park.mu.Unlock()
	return len(r.park.waiting[agent]) > 0
}

func (p *parking) grant(key string) {
	p.mu.Lock()
	if p.grants == nil {
		p.grants = map[string]time.Time{}
	}
	now := time.Now()
	for k, exp := range p.grants {
		if now.After(exp) {
			delete(p.grants, k)
		}
	}
	p.grants[key] = now.Add(GrantTTL)
	p.mu.Unlock()
}

// take consumes a live grant for key.
func (p *parking) take(key string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	exp, ok := p.grants[key]
	if !ok {
		return false
	}
	delete(p.grants, key)
	return time.Now().Before(exp)
}

// ask runs the approval and waits for it, unless the call is parked first:
// then parked is true and the decision is handled when it comes (emitted
// as ParkedDecided; an allowed call is granted one rerun).
func (r *Registry) ask(ctx context.Context, agent string, run func(context.Context) approval.Decision, onLate func(approval.Decision)) (dd approval.Decision, parked bool) {
	// The request outlives the turn if the call is parked, so it does not
	// use the turn's context; cancelling the turn still cancels it.
	actx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	res := make(chan approval.Decision, 1)
	go func() { res <- run(actx) }()
	park := r.park.wait(agent)
	defer r.park.done(agent, park)
	select {
	case dd = <-res:
		cancel()
		return dd, false
	case <-ctx.Done():
		cancel()
		return <-res, false
	case <-park:
		go func() {
			defer cancel()
			onLate(<-res)
		}()
		return approval.Decision{}, true
	}
}
