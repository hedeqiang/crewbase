package agent

// An agent's input queue: enqueue, cancel, pause and resume, claim the
// next input, join or steer the running turn, reorder, and stop.

import (
	"time"

	"github.com/hedeqiang/crewbase/core/session"
)

// ---------------------------------------------------------------- queue

// Enqueue records and queues an input. Front=true jumps the queue.
func (a *Agent) Enqueue(in Input) (Input, error) {
	if in.ID == "" {
		in.ID = session.NewID("in")
	}
	if in.Session == "" {
		in.Session = a.Spec().Session
	}
	in.Queued = time.Now().UTC()
	in.Agent = a.id
	ss, err := a.reg.sessions.Get(in.Session)
	if err != nil {
		return in, err
	}
	if _, err := ss.Append(session.TypeInputQueued, a.id, in); err != nil {
		return in, err
	}
	a.mu.Lock()
	if in.Front {
		a.queue = append([]Input{in}, a.queue...)
	} else {
		a.queue = append(a.queue, in)
	}
	a.mu.Unlock()
	a.signal()
	QueueChanged.Emit(a.ctx, a.id)
	return in, nil
}

// Cancel removes a queued input.
func (a *Agent) Cancel(inputID string) bool {
	a.mu.Lock()
	var found *Input
	for i, x := range a.queue {
		if x.ID == inputID {
			found = &x
			a.queue = append(a.queue[:i:i], a.queue[i+1:]...)
			break
		}
	}
	a.mu.Unlock()
	if found == nil {
		return false
	}
	if ss, err := a.reg.sessions.Get(found.Session); err == nil {
		_, _ = ss.Append(session.TypeInputCancelled, a.id, map[string]string{"id": inputID})
	}
	QueueChanged.Emit(a.ctx, a.id)
	return true
}

// Pause stops claiming new inputs (the current turn continues).
func (a *Agent) Pause() {
	a.mu.Lock()
	a.paused = true
	a.mu.Unlock()
	if ss, err := a.Session(); err == nil {
		_, _ = ss.Append(session.TypeQueuePaused, a.id, nil)
	}
	QueueChanged.Emit(a.ctx, a.id)
}

// Resume resumes claiming inputs.
func (a *Agent) Resume() {
	a.mu.Lock()
	a.paused = false
	a.mu.Unlock()
	if ss, err := a.Session(); err == nil {
		_, _ = ss.Append(session.TypeQueueResumed, a.id, nil)
	}
	a.signal()
	QueueChanged.Emit(a.ctx, a.id)
}

// Paused reports whether the queue is paused.
func (a *Agent) Paused() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.paused
}

// Queue returns a snapshot of waiting inputs.
func (a *Agent) Queue() []Input {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Input(nil), a.queue...)
}

// Claim pops the next input unless paused. The claim is recorded.
func (a *Agent) Claim() (Input, bool) {
	a.mu.Lock()
	if a.paused || len(a.queue) == 0 {
		a.mu.Unlock()
		return Input{}, false
	}
	in := a.queue[0]
	a.queue = a.queue[1:]
	a.curInput, a.curSession, a.stopCur = in.ID, in.Session, nil
	a.mu.Unlock()
	if ss, err := a.reg.sessions.Get(in.Session); err == nil {
		_, _ = ss.Append(session.TypeInputClaimed, a.id, map[string]string{"id": in.ID})
	}
	QueueChanged.Emit(a.ctx, a.id)
	return in, true
}

// Joinable reports whether next can join the turn working on cur: both
// are plain messages (from people, or results of background work: jobs,
// delegations, follow-ups — nobody waits for the turn end of those), for
// the same conversation, run mode and model. Several results arriving
// together become one turn, and one update.
func Joinable(cur, next Input) bool {
	if next.Queue || !plain(cur) || !plain(next) || cur.Session != next.Session || cur.Mode != next.Mode {
		return false
	}
	if next.Model != nil && (cur.Model == nil || *cur.Model != *next.Model) {
		return false
	}
	return true
}

// Absorb claims the inputs at the head of the queue that can join the
// turn working on cur (Joinable), in order: several messages sent in a row
// become one turn and one reply, and a message sent mid-turn is seen at
// the next step instead of waiting for the turn to end. Nothing is taken
// while the queue is paused. The claims are recorded.
func (a *Agent) Absorb(cur Input) []Input {
	a.mu.Lock()
	if a.paused {
		a.mu.Unlock()
		return nil
	}
	n := 0
	for n < len(a.queue) && Joinable(cur, a.queue[n]) {
		n++
	}
	if n == 0 {
		a.mu.Unlock()
		return nil
	}
	got := append([]Input(nil), a.queue[:n]...)
	a.queue = a.queue[n:]
	a.mu.Unlock()
	if ss, err := a.reg.sessions.Get(cur.Session); err == nil {
		for _, in := range got {
			_, _ = ss.Append(session.TypeInputClaimed, a.id, map[string]string{"id": in.ID, "joined": cur.ID})
		}
	}
	QueueChanged.Emit(a.ctx, a.id)
	return got
}

// Steer makes queued input id join the turn running now (at its next
// step) instead of waiting for it to end; false when it is not queued.
func (a *Agent) Steer(id string) bool {
	a.mu.Lock()
	idx := -1
	for i, in := range a.queue {
		if in.ID == id {
			idx = i
		}
	}
	if idx < 0 {
		a.mu.Unlock()
		return false
	}
	in := a.queue[idx]
	in.Queue = false
	// to the head of the queue, where the running turn takes it from
	a.queue = append(a.queue[:idx], a.queue[idx+1:]...)
	a.queue = append([]Input{in}, a.queue...)
	a.mu.Unlock()
	QueueChanged.Emit(a.ctx, a.id)
	a.signal()
	return true
}

// Move moves queued input id by places in the queue (negative = sooner),
// within its ends; false when it is not queued.
func (a *Agent) Move(id string, by int) bool {
	a.mu.Lock()
	idx := -1
	for i, in := range a.queue {
		if in.ID == id {
			idx = i
		}
	}
	if idx < 0 {
		a.mu.Unlock()
		return false
	}
	to := min(max(idx+by, 0), len(a.queue)-1)
	if to != idx {
		in := a.queue[idx]
		a.queue = append(a.queue[:idx], a.queue[idx+1:]...)
		a.queue = append(a.queue[:to], append([]Input{in}, a.queue[to:]...)...)
	}
	a.mu.Unlock()
	if to != idx {
		QueueChanged.Emit(a.ctx, a.id)
	}
	return true
}

// Wake returns a channel signalled when inputs arrive or the queue resumes.
func (a *Agent) Wake() <-chan struct{} { return a.wake }

func (a *Agent) signal() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// SetStopper installs the cancel func of the running turn.
func (a *Agent) SetStopper(stop func()) { a.SetCurrent("", stop) }

// SetCurrent records the input being worked on and its cancel func. A
// stop requested for the input between Claim and SetCurrent fires now.
func (a *Agent) SetCurrent(inputID string, stop func()) {
	a.mu.Lock()
	a.stopCur, a.curInput = stop, inputID
	if inputID == "" {
		a.curSession = ""
	}
	fire := stop != nil && inputID != "" && a.stopReq == inputID
	if fire || inputID == "" {
		a.stopReq = ""
	}
	a.mu.Unlock()
	if fire {
		stop()
	}
}

// CurrentInput returns the id of the input being worked on ("" = idle).
func (a *Agent) CurrentInput() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.curInput
}

// StopInput cancels the turn working on inputID, or removes it from the
// queue if it has not started. It never touches another input's turn.
func (a *Agent) StopInput(inputID string) bool {
	a.mu.Lock()
	stop := a.stopCur
	cur := a.curInput == inputID && inputID != ""
	if cur && stop == nil {
		// Claimed but the turn has not installed its stopper yet.
		a.stopReq = inputID
		a.mu.Unlock()
		return true
	}
	a.mu.Unlock()
	if cur {
		stop()
		return true
	}
	return a.Cancel(inputID)
}

// StopIn cancels the running turn only when it works in session id (an
// agent working elsewhere is left alone). A turn claimed but not started
// yet is stopped as soon as it starts.
func (a *Agent) StopIn(id string) bool {
	a.mu.Lock()
	if a.curInput == "" || a.curSession != id {
		a.mu.Unlock()
		return false
	}
	in := a.curInput
	a.mu.Unlock()
	return a.StopInput(in)
}

// Stop cancels the running turn (the queue and workspace are kept).
func (a *Agent) Stop() bool {
	a.mu.Lock()
	stop := a.stopCur
	a.mu.Unlock()
	if stop == nil {
		return false
	}
	stop()
	return true
}
