package agentloop

// Approvals that don't hold the agent up. A turn waiting for a person's
// approval is parked when something else arrives for the agent (another
// conversation, an IM thread, a routine, a background result): the call
// ends with tools.ParkedResult, the agent says it is waiting and moves on,
// and the request stays open on the desktop and in IM. When the person
// decides, a short turn in the original conversation tells the agent: an
// approved call runs once without asking again (tools grants it), a
// declined one is re-planned. A message from the person in the same
// conversation still answers the approval (replyInsteadOfApproval).
//
// Parked calls are recorded in <root>/parked.json so a restart, which
// loses the open request, tells the agent to ask again if still needed.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

var sprintf = fmt.Sprintf

// Notes that start the turn after a parked approval is decided.
const (
	ApprovedNote = "[Approved] The user approved the action you were waiting on: %s. " +
		"Run exactly the same call again now (it goes through without asking) and continue the task."
	DeclinedNote = "[Not approved] The user declined the action you were waiting on: %s.%s " +
		"Do not retry it; re-plan without it or ask the user."
	ExpiredNote = "[Approval not given] The approval you were waiting on was not answered (%s). " +
		"Ask again only if it is still needed."
	LostNote = "[Crewbase restarted] The approval you were waiting on (%s) was lost in the restart. " +
		"If the action is still needed, run the call again to ask once more; otherwise carry on."
)

// ApprovalTag prefixes the tag of the turn that reports a parked decision.
const ApprovalTag = "approval:"

type parkedCall struct {
	Agent   string `json:"agent"`
	Session string `json:"session"`
	CallID  string `json:"callId"`
	Tool    string `json:"tool"`
}

type parkBook struct {
	mu   sync.Mutex
	path string
	m    map[string]parkedCall // call id →
}

func (b *parkBook) load() []parkedCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	raw, err := os.ReadFile(b.path)
	if err != nil {
		return nil
	}
	var xs []parkedCall
	_ = json.Unmarshal(raw, &xs)
	return xs
}

func (b *parkBook) saveLocked() {
	xs := make([]parkedCall, 0, len(b.m))
	for _, p := range b.m {
		xs = append(xs, p)
	}
	if len(xs) == 0 {
		_ = os.Remove(b.path)
		return
	}
	raw, _ := json.MarshalIndent(xs, "", "  ")
	tmp := b.path + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, b.path)
	}
}

func (b *parkBook) add(p parkedCall) {
	b.mu.Lock()
	if b.m == nil {
		b.m = map[string]parkedCall{}
	}
	b.m[p.CallID] = p
	b.saveLocked()
	b.mu.Unlock()
}

func (b *parkBook) remove(callID string) {
	b.mu.Lock()
	if _, ok := b.m[callID]; ok {
		delete(b.m, callID)
		b.saveLocked()
	}
	b.mu.Unlock()
}

// startParking wires parking up; called once the agents are loaded.
func (l *loop) startParking() {
	l.parked.path = filepath.Join(l.agents.Root(), "parked.json")
	for _, p := range l.parked.load() { // left over from before a restart
		if a, ok := l.agents.Get(p.Agent); ok {
			_, _ = a.Enqueue(agent.Input{Session: p.Session, Tag: ApprovalTag + p.CallID, Text: sprintf(LostNote, p.Tool)})
		}
	}
	l.parked.mu.Lock()
	l.parked.m = map[string]parkedCall{}
	l.parked.saveLocked()
	l.parked.mu.Unlock()

	tools.ParkedDecided.Listen(l.c, func(pd tools.ParkedDecision) {
		l.parked.remove(pd.CallID)
		a, ok := l.agents.Get(pd.Agent)
		if !ok {
			return
		}
		what := pd.Summary
		if what == "" {
			what = pd.Tool
		}
		var text string
		switch pd.Decision.Outcome {
		case approval.OutcomeAllowedOnce:
			text = sprintf(ApprovedNote, what)
		case approval.OutcomeRejected:
			why := ""
			if pd.Decision.Message != "" {
				why = " Their note: " + pd.Decision.Message
			}
			text = sprintf(DeclinedNote, what, why)
		default:
			reason := string(pd.Decision.Outcome)
			if pd.Decision.Message != "" {
				reason = pd.Decision.Message
			}
			text = sprintf(ExpiredNote, reason)
		}
		_, _ = a.Enqueue(agent.Input{Session: pd.Session, Tag: ApprovalTag + pd.CallID, Text: text})
	})
}

// parkedNow records a call that stopped waiting for its approval.
func (l *loop) parkedNow(agentID, session string, exe tools.Execution) {
	l.parked.add(parkedCall{Agent: agentID, Session: session, CallID: exe.Call.ID, Tool: exe.Call.Name})
}

// replyInsteadOfApproval runs whenever an agent's queue changes. While a
// turn waits for an approval: a message from the person in the same
// conversation answers it (declined; their message is what the agent reads
// next); anything else waiting parks the call so it is not stuck behind it.
func (l *loop) replyInsteadOfApproval(agentID string) {
	a, ok := l.agents.Get(agentID)
	if !ok || a.Paused() {
		return
	}
	v, ok := l.cur.Load(agentID)
	if !ok {
		return
	}
	cur := v.(agent.Input)
	q := a.Queue()
	if len(q) == 0 {
		return
	}
	if q[0].Tag == "" && agent.Joinable(cur, q[0]) {
		in, ok := kernel.Lookup[*approval.Inbox](l.c, approval.InboxKey)
		if !ok {
			return
		}
		for _, r := range in.Pending() {
			if r.Agent == agentID && r.Session == cur.Session {
				in.Decide(r.CallID, approval.Decision{Outcome: approval.OutcomeRejected, Message: RejectForReply, By: "reply"})
			}
		}
		return
	}
	l.toolsReg.Park(agentID)
}
