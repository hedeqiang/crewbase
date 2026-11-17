package compaction

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/features/redact"
	"github.com/hedeqiang/crewbase/kernel"
)

// Segment keys of the context usage breakdown (docs/03 §5.3), in display
// order.
const (
	SegSystem   = "system"   // system prompt (minus memory and skills)
	SegMemory   = "memory"   // MEMORY.md fragment ("agent-memory")
	SegTools    = "tools"    // tool definitions (schemas)
	SegSkills   = "skills"   // skill / sub-agent catalogue fragments
	SegToolIO   = "toolio"   // tool calls and results in the history
	SegMessages = "messages" // user / assistant text
	SegFree     = "free"     // remaining window
)

// MemoryFragment is the prompt fragment counted as memory.
const MemoryFragment = "agent-memory"

// Segment is one bar segment.
type Segment struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Tokens int    `json:"tokens"`
}

// Breakdown is the context usage popover model.
//
// Estimated is the sum of the estimated segments and equals what the agent
// loop estimates for the next request (agentloop.EstimateContext, before
// agent/request middleware); the loop compacts on it, so OverThreshold
// follows it. Used is the provider's count of the last request when it
// reported one (Counted), else Estimated. Used + Free = Window when not
// over.
type Breakdown struct {
	Agent     string       `json:"agent"`
	Session   string       `json:"session"`
	Model     llm.ModelRef `json:"model"`
	Window    int          `json:"window"`
	Threshold float64      `json:"threshold"`
	Used      int          `json:"used"`
	Estimated int          `json:"estimated"`
	Free      int          `json:"free"`
	Percent   float64      `json:"percent"` // Used / Window, 0..100+
	// OverThreshold is true when the next step will compact first.
	OverThreshold bool      `json:"overThreshold"`
	Segments      []Segment `json:"segments"`
	// Compactions counts compaction summaries for this agent in the session.
	Compactions int `json:"compactions"`
	// CompactedUpto is the last summarised seq (0 = none).
	CompactedUpto int64 `json:"compactedUpto,omitempty"`

	// Counted: Used comes from the provider's count of the last request
	// (plus the estimate of what was added since), not an estimate.
	Counted bool `json:"counted"`
	// Recorded: the items follow the request the loop last sent
	// (agentloop.SentRequests), not only a re-derived one.
	Recorded bool `json:"recorded,omitempty"`
	// Cache is the prompt cache of the last request, when the provider
	// reported cache reads or writes.
	Cache *Cache `json:"cache,omitempty"`
	// Headroom estimates how many more requests fit before auto-compaction
	// at the average input growth per request; 0 = unknown.
	Headroom int `json:"headroom,omitempty"`
	// Items are the blocks of the context map (see Item), at most MaxItems.
	Items []Item `json:"items"`
}

// Cache is the prompt-cache use of one request. Input counts every input
// token (llm.Usage.InputTokens: cache reads and writes included).
type Cache struct {
	Input int `json:"input"`
	Read  int `json:"read"`
	Write int `json:"write"`
	// HitPercent = Read / Input × 100.
	HitPercent float64 `json:"hitPercent"`
}

func isSkillSection(name string) bool {
	n := strings.ToLower(name)
	return strings.HasPrefix(n, "skill") || strings.HasPrefix(n, "subagent") || strings.HasPrefix(n, "sub-agent")
}

// Usage computes the context usage breakdown for agentID in sessionID
// ("" = the agent's current conversation).
func (s *Service) Usage(agentID, sessionID string) (Breakdown, error) {
	b, _, err := s.usage(agentID, sessionID)
	return b, err
}

func (s *Service) usage(agentID, sessionID string) (Breakdown, []*Item, error) {
	a, ok := s.agents.Get(agentID)
	if !ok {
		return Breakdown{}, nil, fmt.Errorf("agent %s not found", agentID)
	}
	if sessionID == "" {
		sessionID = s.CurrentSession(a)
	}
	ss, err := s.agents.Sessions().Get(sessionID)
	if err != nil {
		return Breakdown{}, nil, err
	}
	events := ss.Events()
	ctx := context.Background()
	ref := s.currentModel(a, events)
	win := s.window(ctx, ref)

	kind := ss.Meta().Kind
	ad, _ := s.llms.Get(ref.Provider)

	segs := map[string]int{}
	var items []*Item
	// System prompt: split by section. Sections are joined with "\n\n" in
	// Assemble; the separators are attributed to the system segment so that
	// the segments add up to EstimateTokens(system) exactly.
	system := agentloop.System(s.prompts, a)
	total := llm.EstimateTokens(system)
	var sysItems []*Item
	for _, sec := range agentloop.SystemSections(s.prompts, a) {
		n := llm.EstimateTokens(sec.Text)
		it := &Item{ID: "sys:" + sec.Name, Kind: SegSystem, Label: sec.Name, Tokens: n, content: one("text", "markdown", sec.Text)}
		switch {
		case sec.Name == MemoryFragment:
			it.Kind = SegMemory
			segs[SegMemory] += n
		case isSkillSection(sec.Name):
			it.Kind = SegSkills
			segs[SegSkills] += n
		default:
			sysItems = append(sysItems, it)
		}
		items = append(items, it)
	}
	segs[SegSystem] = max(0, total-segs[SegMemory]-segs[SegSkills])
	// The system items add up to the segment: the rounding of the joined
	// text and its separators go to the largest section.
	sum := 0
	for _, it := range sysItems {
		sum += it.Tokens
	}
	if d := segs[SegSystem] - sum; d != 0 {
		if len(sysItems) == 0 {
			items = append(items, &Item{ID: "sys:", Kind: SegSystem, Label: "separators", Tokens: d})
		} else {
			big := sysItems[0]
			for _, it := range sysItems {
				if it.Tokens > big.Tokens {
					big = it
				}
			}
			big.Tokens += d
		}
	}

	// Tool definitions as the loop sends them for this session kind: what
	// the next request would carry. Deferred tools held back by tool search
	// are left out (the planner is the agent loop's own choice).
	ts := agentloop.ToolSchemas(s.toolsR, a, kind)
	if p, ok := kernel.Lookup[agentloop.ToolPlanner](s.c, agentloop.ToolPlannerKey); ok && ad != nil {
		ts = p.PlannedTools(a, ss, ad, ref.Model, win)
	}
	for _, t := range ts {
		if t.Deferred {
			continue // native tool search: outside the context until loaded
		}
		b, _ := json.Marshal(t)
		n := llm.EstimateTokens(string(b))
		segs[SegTools] += n
		items = append(items, toolItem(t, s.toolGroup(agentID, t), n))
	}
	msgs := session.DeriveMessages(events, agentID, s.agents.Names())
	countParts(segs, msgs)
	items = append(items, messageItems(msgs)...)
	b := Breakdown{Agent: agentID, Session: sessionID, Model: ref, Window: win, Threshold: s.cfg.Threshold}
	labels := map[string]string{
		SegSystem: "System prompt", SegMemory: "Memory", SegTools: "Tool definitions",
		SegSkills: "Skills & sub-agents", SegToolIO: "Tool calls & results", SegMessages: "Messages",
		SegOther: "Counted beyond the estimate", SegFree: "Free",
	}
	segKeys := []string{SegSystem, SegMemory, SegTools, SegSkills, SegToolIO, SegMessages}
	// What the loop really sent last adds what re-deriving the request
	// cannot see: loaded attachments (a document's extracted text, images),
	// and what Request listeners added to the system prompt and tools. The
	// prompt and tools themselves stay current.
	if sr, ok := kernel.Lookup[agentloop.SentRequests](s.c, agentloop.SentRequestsKey); ok {
		if q, seq, ok := sr.LastSent(agentID, sessionID); ok && !compactedSince(events, agentID, seq) {
			items, b.Recorded = s.withSent(q, items, segs, ts, msgs), true
		}
	}
	for _, k := range segKeys {
		b.Segments = append(b.Segments, Segment{Key: k, Label: labels[k], Tokens: segs[k]})
		b.Estimated += segs[k]
	}
	b.Used = b.Estimated
	{
		if c := s.counted(events, agentID, ref); c.used > 0 {
			b.Used, b.Counted, b.Cache = c.used, true, c.cache
			if extra := c.used - b.Estimated; extra > 0 {
				b.Segments = append(b.Segments, Segment{Key: SegOther, Label: labels[SegOther], Tokens: extra})
				items = append(items, &Item{ID: "other", Kind: SegOther, Label: labels[SegOther], Tokens: extra})
			}
			if c.growth > 0 && win > 0 {
				if left := int(s.cfg.Threshold*float64(win)) - b.Used; left > 0 {
					b.Headroom = int(float64(left) / c.growth)
				}
			}
		}
	}
	b.Free = max(0, win-b.Used)
	b.Segments = append(b.Segments, Segment{Key: SegFree, Label: labels[SegFree], Tokens: b.Free})
	if win > 0 {
		b.Percent = float64(b.Used) * 100 / float64(win)
		b.OverThreshold = float64(b.Estimated) > s.cfg.Threshold*float64(win)
	}
	for _, e := range events {
		if e.Type == session.TypeCompaction && (e.Agent == agentID || e.Agent == "") {
			b.Compactions++
			var c session.CompactionData
			if e.Decode(&c) == nil {
				b.CompactedUpto = c.UptoSeq
			}
		}
	}
	items = rollUp(items, MaxItems)
	b.Items = make([]Item, len(items))
	for i, it := range items {
		// labels preview messages and arguments: no secrets in them
		it.Label, it.Group = redact.Redact(it.Label), redact.Redact(it.Group)
		b.Items[i] = *it
	}
	return b, items, nil
}

// countMessages adds the estimated message segments of agentID's history
// in events to segs (as llm.EstimateRequest counts them).
func (s *Service) countMessages(segs map[string]int, events []session.Event, agentID string) {
	countParts(segs, session.DeriveMessages(events, agentID, s.agents.Names()))
}

func countParts(segs map[string]int, msgs []llm.Message) {
	for _, m := range msgs {
		for _, p := range m.Parts {
			n := llm.EstimateTokens(p.Text) + 4
			if p.Attachment != nil {
				n += llm.EstimateAttachment(p.Attachment)
			}
			key := SegMessages
			if p.ToolCall != nil {
				n += llm.EstimateTokens(string(p.ToolCall.Args)) + 8
				key = SegToolIO
			}
			if p.ToolResult != nil {
				n += llm.EstimateTokens(p.ToolResult.Content) + 8
				key = SegToolIO
			}
			segs[key] += n
		}
	}
}

// growthSamples is how many recent requests the headroom growth averages.
const growthSamples = 10

type countedUsage struct {
	used   int
	cache  *Cache
	growth float64 // average input growth per request; 0 = unknown
}

// counted is the provider's count of agentID's last request in events
// (llm.Usage.InputTokens of the latest reply, cache reads and writes
// included) plus the estimate of what was added since: that reply and any
// later messages. Zero when the latest reply came from another provider,
// carries no count or one the adapter estimated, or a compaction followed
// it. growth averages the input growth over the counted requests since the
// last compaction (at least three, at most growthSamples).
func (s *Service) counted(events []session.Event, agentID string, ref llm.ModelRef) countedUsage {
	var out countedUsage
	var inputs []int // newest first
	last := -1
	var u llm.Usage
	for i := len(events) - 1; i >= 0 && len(inputs) < growthSamples; i-- {
		e := events[i]
		if e.Type == session.TypeCompaction && (e.Agent == agentID || e.Agent == "") {
			break
		}
		if e.Type != session.TypeAssistantMessage || e.Agent != agentID {
			continue
		}
		var m session.AssistantMessage
		if e.Decode(&m) != nil || m.Model.Provider != ref.Provider || m.Usage.Estimated || m.Usage.InputTokens <= 0 {
			break
		}
		if last < 0 {
			last, u = i, m.Usage
		}
		inputs = append(inputs, m.Usage.InputTokens)
	}
	if last < 0 {
		return out
	}
	before, all := map[string]int{}, map[string]int{}
	s.countMessages(before, events[:last], agentID)
	s.countMessages(all, events, agentID)
	added := all[SegMessages] + all[SegToolIO] - before[SegMessages] - before[SegToolIO]
	out.used = u.InputTokens + max(0, added)
	out.cache = cacheOf(u)
	if n := len(inputs); n >= 3 {
		if g := float64(inputs[0]-inputs[n-1]) / float64(n-1); g > 0 {
			out.growth = g
		}
	}
	return out
}

// cacheOf is the prompt-cache use of a request, nil when the provider
// reported no cache reads or writes.
func cacheOf(u llm.Usage) *Cache {
	if u.CacheRead+u.CacheWrite <= 0 || u.InputTokens <= 0 {
		return nil
	}
	return &Cache{Input: u.InputTokens, Read: u.CacheRead, Write: u.CacheWrite, HitPercent: float64(u.CacheRead) * 100 / float64(u.InputTokens)}
}

// CompactNow compacts agentID's session ("" = current) with the agent's
// current model ("compact now" on the popover). It should be called while
// the agent is idle; concurrent compactions of one session are serialised.
func (s *Service) CompactNow(ctx context.Context, agentID, sessionID string) (Result, error) {
	a, ok := s.agents.Get(agentID)
	if !ok {
		return Result{}, fmt.Errorf("agent %s not found", agentID)
	}
	if sessionID == "" {
		sessionID = s.CurrentSession(a)
	}
	ss, err := s.agents.Sessions().Get(sessionID)
	if err != nil {
		return Result{}, err
	}
	return s.compact(ctx, a, ss, s.currentModel(a, ss.Events()), "manual")
}

// NewConversation starts a fresh direct session for the same agent
// (workspace, memory and queue are unchanged) and makes it current.
func (s *Service) NewConversation(a *agent.Agent) (*session.Session, error) {
	old := s.CurrentSession(a)
	sp := a.Spec()
	ns, err := s.agents.Sessions().Create(session.Meta{Kind: "agent", Title: sp.Name, Agents: []string{sp.ID}, Folder: sp.Folder, Parent: ""})
	if err != nil {
		return nil, err
	}
	// Link the old conversation to the new one (history navigation).
	if prev, err := s.agents.Sessions().Get(old); err == nil {
		if _, err := prev.Append(TypeNextConversation, sp.ID, map[string]string{"session": ns.ID()}); err != nil {
			return nil, err
		}
	}
	if err := a.SetSession(ns.ID()); err != nil {
		return nil, err
	}
	return ns, nil
}

// CurrentSession returns the agent's current direct conversation: the spec
// session, followed along conversation/next links written by
// NewConversation.
func (s *Service) CurrentSession(a *agent.Agent) string {
	id := a.Spec().Session
	seen := map[string]bool{}
	for !seen[id] {
		seen[id] = true
		ss, err := s.agents.Sessions().Get(id)
		if err != nil {
			break
		}
		e, ok := ss.Last(TypeNextConversation)
		if !ok {
			break
		}
		var m map[string]string
		if e.Decode(&m) != nil || m["session"] == "" {
			break
		}
		id = m["session"]
	}
	return id
}

// currentModel is the model of the latest step in the session, else the
// router's (or first configured) model.
func (s *Service) currentModel(a *agent.Agent, events []session.Event) llm.ModelRef {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == session.TypeStepStart && events[i].Agent == a.ID() {
			var st agentloop.StepStart
			if events[i].Decode(&st) == nil && st.Model.Provider != "" {
				return st.Model
			}
		}
	}
	if r, ok := kernel.Lookup[agentloop.Router](s.c, agentloop.RouterKey); ok {
		if ref, ok := r.Select(a); ok {
			return ref
		}
	}
	if ms := a.Spec().Models; len(ms) > 0 {
		return ms[0]
	}
	return llm.ModelRef{}
}

func (s *Service) window(ctx context.Context, ref llm.ModelRef) int {
	if ad, ok := s.llms.Get(ref.Provider); ok {
		if ms, err := s.llms.ModelsOf(ctx, ad); err == nil {
			for _, m := range ms {
				if m.ID == ref.Model && m.ContextWindow > 0 {
					return m.ContextWindow
				}
			}
		}
	}
	return s.cfg.DefaultWindow
}

// estimate mirrors the agent loop's request build and estimate
// (agentloop.EstimateContext) for a in a session of kind on model.
func (s *Service) estimate(a *agent.Agent, kind string, _ llm.ModelRef, events []session.Event) int {
	return agentloop.EstimateContext(llm.Request{
		System:   agentloop.System(s.prompts, a),
		Messages: session.DeriveMessages(events, a.ID(), s.agents.Names()),
		Tools:    agentloop.ToolSchemas(s.toolsR, a, kind),
	})
}

// compactedSince reports whether agentID's history was compacted after seq.
func compactedSince(events []session.Event, agentID string, seq int64) bool {
	for i := len(events) - 1; i >= 0 && events[i].Seq > seq; i-- {
		if e := events[i]; e.Type == session.TypeCompaction && (e.Agent == agentID || e.Agent == "") {
			return true
		}
	}
	return false
}

// withSent merges the last sent request q into the derived breakdown: its
// system additions and extra tools are added, and its messages (with the
// attachments as loaded) replace the derived ones they cover.
func (s *Service) withSent(q *agentloop.RecordedRequest, items []*Item, segs map[string]int, current []llm.ToolSchema, msgs []llm.Message) []*Item {
	for _, blk := range q.System {
		if blk.Name != "additions" {
			continue
		}
		n := llm.EstimateTokens(blk.Text) + 1
		segs[SegSystem] += n
		items = append(items, &Item{ID: "sys:additions", Kind: SegSystem, Label: "additions", Tokens: n, content: one("text", "markdown", blk.Text)})
	}
	have := map[string]bool{}
	for _, t := range current {
		have[t.Name] = true
	}
	for _, t := range q.Tools {
		if t.Deferred || have[t.Name] {
			continue
		}
		raw, _ := json.Marshal(t)
		n := llm.EstimateTokens(string(raw))
		segs[SegTools] += n
		items = append(items, toolItem(t, sentGroup(t), n))
	}
	if len(msgs) < len(q.Messages) {
		return items // the history changed under it; keep the derived view
	}
	kept := items[:0]
	for _, it := range items {
		if it.Kind != SegMessages && it.Kind != SegToolIO {
			kept = append(kept, it)
		}
	}
	segs[SegMessages], segs[SegToolIO] = 0, 0
	all := append(append([]llm.Message(nil), q.Messages...), msgs[len(q.Messages):]...)
	countParts(segs, all)
	return append(kept, messageItems(all)...)
}
