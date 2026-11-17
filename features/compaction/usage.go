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
	// SegCLI is what a CLI that runs its own tools reported using beyond
	// the estimated segments: its own system prompt and tool definitions.
	// Present only with a reported total.
	SegCLI  = "cli"
	SegFree = "free" // remaining window
)

// MemoryFragment is the prompt fragment counted as memory.
const MemoryFragment = "agent-memory"

// Segment is one bar segment.
type Segment struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Tokens int    `json:"tokens"`
	// Managed marks the tools segment of a model that runs its own tools
	// (a coding CLI over ACP, llm.OwnTools): the CLI manages its tools, and
	// the segment counts only the Crewbase tools bridged to it (MCP).
	Managed bool `json:"managed,omitempty"`
}

// Breakdown is the context usage popover model.
//
// Estimated is the sum of the estimated segments and equals what the agent
// loop estimates for the next request (agentloop.EstimateContext, before
// agent/request middleware); the loop compacts on it, so OverThreshold
// follows it. Reported is the context size a CLI that runs its own tools
// last reported (llm.Usage.Context) plus the estimate of what was added
// since; 0 when there is none. Used is Reported when set, else Estimated;
// with a reported total the segments are best-effort estimates and SegCLI
// holds the rest. Used + Free = Window when not over.
type Breakdown struct {
	Agent     string       `json:"agent"`
	Session   string       `json:"session"`
	Model     llm.ModelRef `json:"model"`
	Window    int          `json:"window"`
	Threshold float64      `json:"threshold"`
	Used      int          `json:"used"`
	Estimated int          `json:"estimated"`
	Reported  int          `json:"reported,omitempty"`
	// ToolsManaged: the model runs its own tools (see Segment.Managed).
	ToolsManaged bool    `json:"toolsManaged,omitempty"`
	Free         int     `json:"free"`
	Percent      float64 `json:"percent"` // Used / Window, 0..100+
	// OverThreshold is true when the next step will compact first.
	OverThreshold bool      `json:"overThreshold"`
	Segments      []Segment `json:"segments"`
	// Compactions counts compaction summaries for this agent in the session.
	Compactions int `json:"compactions"`
	// CompactedUpto is the last summarised seq (0 = none).
	CompactedUpto int64 `json:"compactedUpto,omitempty"`
}

func isSkillSection(name string) bool {
	n := strings.ToLower(name)
	return strings.HasPrefix(n, "skill") || strings.HasPrefix(n, "subagent") || strings.HasPrefix(n, "sub-agent")
}

// Usage computes the context usage breakdown for agentID in sessionID
// ("" = the agent's current conversation).
func (s *Service) Usage(agentID, sessionID string) (Breakdown, error) {
	a, ok := s.agents.Get(agentID)
	if !ok {
		return Breakdown{}, fmt.Errorf("agent %s not found", agentID)
	}
	if sessionID == "" {
		sessionID = s.CurrentSession(a)
	}
	ss, err := s.agents.Sessions().Get(sessionID)
	if err != nil {
		return Breakdown{}, err
	}
	events := ss.Events()
	ctx := context.Background()
	ref := s.currentModel(a, events)
	win := s.window(ctx, ref)

	kind := ss.Meta().Kind
	ad, _ := s.llms.Get(ref.Provider)

	segs := map[string]int{}
	// System prompt: split by section. Sections are joined with "\n\n" in
	// Assemble; the separators are attributed to the system segment so that
	// the segments add up to EstimateTokens(system) exactly.
	system := agentloop.System(s.prompts, a)
	total := llm.EstimateTokens(system)
	for _, sec := range agentloop.SystemSections(s.prompts, a) {
		switch {
		case sec.Name == MemoryFragment:
			segs[SegMemory] += llm.EstimateTokens(sec.Text)
		case isSkillSection(sec.Name):
			segs[SegSkills] += llm.EstimateTokens(sec.Text)
		}
	}
	segs[SegSystem] = max(0, total-segs[SegMemory]-segs[SegSkills])

	// Tool definitions as the loop sends them for this session kind, and
	// only those the model sees (a CLI gets just the bridged ones).
	ts, managed := agentloop.ContextTools(s.c, ad, a, kind, agentloop.ToolSchemas(s.toolsR, a, kind))
	for _, t := range ts {
		b, _ := json.Marshal(t)
		segs[SegTools] += llm.EstimateTokens(string(b))
	}
	s.countMessages(segs, events, agentID)
	b := Breakdown{Agent: agentID, Session: sessionID, Model: ref, Window: win, Threshold: s.cfg.Threshold, ToolsManaged: managed}
	labels := map[string]string{
		SegSystem: "System prompt", SegMemory: "Memory", SegTools: "Tool definitions",
		SegSkills: "Skills & sub-agents", SegToolIO: "Tool calls & results", SegMessages: "Messages",
		SegCLI: "Used by the CLI itself", SegFree: "Free",
	}
	if managed {
		labels[SegTools] = "Crewbase tools (the CLI manages its own)"
	}
	for _, k := range []string{SegSystem, SegMemory, SegTools, SegSkills, SegToolIO, SegMessages} {
		b.Segments = append(b.Segments, Segment{Key: k, Label: labels[k], Tokens: segs[k], Managed: managed && k == SegTools})
		b.Estimated += segs[k]
	}
	b.Used = b.Estimated
	if managed {
		if r := s.reported(events, agentID, ref); r > 0 {
			b.Reported, b.Used = r, r
			if extra := r - b.Estimated; extra > 0 {
				b.Segments = append(b.Segments, Segment{Key: SegCLI, Label: labels[SegCLI], Tokens: extra})
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
	return b, nil
}

// countMessages adds the estimated message segments of agentID's history
// in events to segs (as llm.EstimateRequest counts them).
func (s *Service) countMessages(segs map[string]int, events []session.Event, agentID string) {
	for _, m := range session.DeriveMessages(events, agentID, s.agents.Names()) {
		for _, p := range m.Parts {
			n := llm.EstimateTokens(p.Text) + 4
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

// reported is the context size the model last reported for agentID in
// events (llm.Usage.Context of its latest reply) plus the estimate of the
// messages added since; 0 when the latest reply came from another
// provider, reported nothing, or a compaction followed it (the CLI then
// starts a new session).
func (s *Service) reported(events []session.Event, agentID string, ref llm.ModelRef) int {
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.Type == session.TypeCompaction && (e.Agent == agentID || e.Agent == "") {
			return 0
		}
		if e.Type != session.TypeAssistantMessage || e.Agent != agentID {
			continue
		}
		var m session.AssistantMessage
		if e.Decode(&m) != nil || m.Usage.Context <= 0 || m.Model.Provider != ref.Provider {
			return 0
		}
		before, all := map[string]int{}, map[string]int{}
		s.countMessages(before, events[:i+1], agentID)
		s.countMessages(all, events, agentID)
		added := all[SegMessages] + all[SegToolIO] - before[SegMessages] - before[SegToolIO]
		return m.Usage.Context + max(0, added)
	}
	return 0
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
func (s *Service) estimate(a *agent.Agent, kind string, model llm.ModelRef, events []session.Event) int {
	ad, _ := s.llms.Get(model.Provider)
	return agentloop.EstimateContext(s.c, ad, a, kind, llm.Request{
		System:   agentloop.System(s.prompts, a),
		Messages: session.DeriveMessages(events, a.ID(), s.agents.Names()),
		Tools:    agentloop.ToolSchemas(s.toolsR, a, kind),
	})
}
