package agentloop

import (
	"strings"
	"sync"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
)

// SentRequestsKey is the service key of SentRequests (the agent loop).
const SentRequestsKey = "agentloop-sent"

// SentRequests reports the last request the agent loop actually sent for an
// agent in a session: after attachments were loaded and every Request
// listener ran. The context meter itemises it instead of re-deriving the
// request, so nothing it carries (a document's extracted text, screenshots,
// a plugin's addition) shows up as an unexplained remainder.
type SentRequests interface {
	// LastSent returns that request and the session's last event seq when
	// it was sent; false when the loop has sent nothing there since it
	// started.
	LastSent(agent, session string) (*RecordedRequest, int64, bool)
}

type sentEntry struct {
	req *RecordedRequest
	seq int64
}

// sentLog keeps the last sent request per agent and session, in memory.
type sentLog struct {
	mu sync.Mutex
	m  map[string]sentEntry
	// order is the insertion order, for the cap.
	order []string
}

// maxSent bounds the requests kept (one per agent and session).
const maxSent = 256

func (s *sentLog) set(agentID, sess string, e sentEntry) {
	k := offerKey(agentID, sess)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]sentEntry{}
	}
	if _, ok := s.m[k]; !ok {
		s.order = append(s.order, k)
		if len(s.order) > maxSent {
			delete(s.m, s.order[0])
			s.order = s.order[1:]
		}
	}
	s.m[k] = e
}

func (s *sentLog) get(agentID, sess string) (sentEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[offerKey(agentID, sess)]
	return e, ok
}

// LastSent implements SentRequests.
func (l *loop) LastSent(agentID, sess string) (*RecordedRequest, int64, bool) {
	e, ok := l.sent.get(agentID, sess)
	if !ok {
		return nil, 0, false
	}
	return e.req, e.seq, true
}

// record keeps a light copy of req as sent: attachment bytes are dropped
// (their context size is kept in Attachment.Tokens) and the system prompt
// is split into the agent's sections plus what was added to them.
func (l *loop) record(a *agent.Agent, ss *session.Session, req llm.Request) {
	rec := &RecordedRequest{Tools: append([]llm.ToolSchema(nil), req.Tools...), System: splitSystem(SystemSections(l.prompts, a), req.System)}
	for _, m := range req.Messages {
		cm := m
		cm.Parts = make([]llm.Part, len(m.Parts))
		for i, p := range m.Parts {
			if p.Attachment != nil {
				at := *p.Attachment
				at.Tokens = llm.EstimateAttachment(&at)
				at.Data = nil
				p.Attachment = &at
			}
			cm.Parts[i] = p
		}
		rec.Messages = append(rec.Messages, cm)
	}
	var seq int64
	if ev := ss.Events(); len(ev) > 0 {
		seq = ev[len(ev)-1].Seq
	}
	l.sent.set(a.ID(), ss.ID(), sentEntry{req: rec, seq: seq})
}

// splitSystem names the parts of system: the agent's prompt sections in
// order, then whatever follows them (the tool search index, additions by
// Request listeners). A system prompt that does not start with the
// sections is one block.
func splitSystem(secs []prompt.Section, system string) []RecordedBlock {
	var base []string
	for _, s := range secs {
		base = append(base, s.Text)
	}
	joined := strings.Join(base, "\n\n")
	if len(secs) == 0 || !strings.HasPrefix(system, joined) {
		return []RecordedBlock{{Name: "system", Text: system}}
	}
	var out []RecordedBlock
	for _, s := range secs {
		out = append(out, RecordedBlock{Name: s.Name, Text: s.Text})
	}
	if rest := strings.TrimSpace(system[len(joined):]); rest != "" {
		out = append(out, RecordedBlock{Name: "additions", Text: rest})
	}
	return out
}
