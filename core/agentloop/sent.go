package agentloop

import (
	"encoding/json"
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

// sentFile is the session side file keeping, per agent, what the last
// sent request had beyond what the log re-derives (see sentNote), so the
// context map is right after a restart too.
const sentFile = "sent.json"

// sentNote is the compact, persisted part of a sent request: the system
// prompt additions, tools a Request listener added, and the context size
// of each attachment as it was loaded. Messages are not kept: the log has
// them.
type sentNote struct {
	Seq         int64            `json:"seq"`
	Additions   string           `json:"additions,omitempty"`
	Tools       []llm.ToolSchema `json:"tools,omitempty"`
	Attachments map[string]int   `json:"attachments,omitempty"` // workspace path → tokens
}

// LastSent implements SentRequests: the request kept in memory, else the
// note persisted with the session (after a restart).
func (l *loop) LastSent(agentID, sess string) (*RecordedRequest, int64, bool) {
	if e, ok := l.sent.get(agentID, sess); ok {
		return e.req, e.seq, true
	}
	ss, err := l.agents.Sessions().Get(sess)
	if err != nil {
		return nil, 0, false
	}
	b, err := ss.ReadState(sentFile)
	if err != nil {
		return nil, 0, false
	}
	var notes map[string]sentNote
	if json.Unmarshal(b, &notes) != nil {
		return nil, 0, false
	}
	n, ok := notes[agentID]
	if !ok {
		return nil, 0, false
	}
	a, ok := l.agents.Get(agentID)
	if !ok {
		return nil, 0, false
	}
	rec := &RecordedRequest{Tools: n.Tools}
	for _, sec := range SystemSections(l.prompts, a) {
		rec.System = append(rec.System, RecordedBlock{Name: sec.Name, Text: sec.Text})
	}
	if n.Additions != "" {
		rec.System = append(rec.System, RecordedBlock{Name: "additions", Text: n.Additions})
	}
	for _, m := range session.DeriveMessages(ss.Events(), agentID, l.agents.Names()) {
		for i, p := range m.Parts {
			if p.Attachment != nil {
				if t := n.Attachments[p.Attachment.Path]; t > 0 {
					at := *p.Attachment
					at.Tokens = t
					m.Parts[i].Attachment = &at
				}
			}
		}
		rec.Messages = append(rec.Messages, m)
	}
	return rec, n.Seq, true
}

// persistSent writes the compact note of rec for a into the session.
func (l *loop) persistSent(a *agent.Agent, ss *session.Session, rec *RecordedRequest, seq int64) {
	n := sentNote{Seq: seq}
	for _, b := range rec.System {
		if b.Name == "additions" {
			n.Additions = b.Text
		}
	}
	planned := map[string]bool{}
	for _, t := range ToolSchemas(l.toolsReg, a, ss.Meta().Kind) {
		planned[t.Name] = true
	}
	for _, t := range rec.Tools {
		if !planned[t.Name] && t.Name != ToolSearchName && !t.Deferred {
			n.Tools = append(n.Tools, t)
		}
	}
	for _, m := range rec.Messages {
		for _, p := range m.Parts {
			if at := p.Attachment; at != nil && at.Tokens > 0 {
				if n.Attachments == nil {
					n.Attachments = map[string]int{}
				}
				n.Attachments[at.Path] = at.Tokens
			}
		}
	}
	l.sentMu.Lock()
	defer l.sentMu.Unlock()
	notes := map[string]sentNote{}
	if b, err := ss.ReadState(sentFile); err == nil {
		_ = json.Unmarshal(b, &notes)
	}
	notes[a.ID()] = n
	if b, err := json.Marshal(notes); err == nil {
		if err := ss.WriteState(sentFile, b); err != nil {
			l.c.Log().Warn("keeping the sent request note failed", "session", ss.ID(), "err", err)
		}
	}
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
	l.persistSent(a, ss, rec, seq)
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
