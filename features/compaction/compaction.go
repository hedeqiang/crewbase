// Package compaction is compaction-summary: when an agent's context passes
// the threshold (C-1, default 80% of the model window), the older part of
// the conversation is replaced by a summary. Instructions and MEMORY.md live
// in system prompt fragments and are therefore always kept; the most recent
// steps are kept verbatim; the original events stay in the append-only log
// and can be recalled with history_search / history_read (C-2).
//
// A compaction is recorded as three session events:
//
//	compaction/start   {reason, model, uptoSeq, tokensBefore}
//	compaction/summary session.CompactionData (DeriveMessages applies it)
//	compaction/end     {status, method, pruned, tokensAfter, error}
//
// The cut point is always a step or turn boundary: a tool call and its
// result are never separated. Long tool results are pruned before the cut
// region is summarised. If the model fails, a deterministic extractive
// summary is used instead, so compaction never fails the turn.
package compaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

// Session event types written by this plugin (compaction/summary is
// session.TypeCompaction).
const (
	TypeStart = "compaction/start"
	TypeEnd   = "compaction/end"
	// TypeNextConversation is appended to an agent's old direct session
	// when NewConversation starts a fresh one; it links the chain so the
	// current session survives restarts.
	TypeNextConversation = "conversation/next"
)

// SummaryMarker starts the summarisation system prompt (tests and adapters
// can recognise summarisation requests by it).
const SummaryMarker = "You are compacting a conversation"

const summarySystem = SummaryMarker + ` between a user and an AI agent so the agent can continue with a smaller context.
Write a concise summary of the conversation above that preserves everything needed to continue the work:
- the user's goals and explicit requests (latest first if they changed)
- decisions made and their reasons
- key facts, names, file paths, commands, URLs, identifiers and numbers
- errors encountered and how they were resolved
- what was done, the current state, and open tasks / next steps
The agent's instructions and long-term memory (MEMORY.md) are kept separately; do not restate them.
Original messages remain retrievable with history_search / history_read, so omit verbatim detail that can be looked up.
Write plain text in the language the conversation uses. Do not call tools.`

// Config of the plugin.
type Config struct {
	// KeepRecentTokens is the minimum context (estimated tokens) kept
	// verbatim after the cut. 0 = KeepRecentRatio of the model window.
	KeepRecentTokens int     `json:"keepRecentTokens"`
	KeepRecentRatio  float64 `json:"keepRecentRatio"`
	// PruneToolResultChars: tool results longer than this are pruned to a
	// head/tail excerpt before summarisation.
	PruneToolResultChars int `json:"pruneToolResultChars"`
	// SummaryMaxTokens caps the summary (model max tokens and the
	// extractive fallback budget). 0 = 10% of the window, at most 4000.
	SummaryMaxTokens int `json:"summaryMaxTokens"`
	// Threshold is the usage ratio shown on the context popover (the
	// agent loop owns the actual trigger; keep them equal).
	Threshold     float64 `json:"threshold"`
	DefaultWindow int     `json:"defaultWindow"`
	// TimeoutSeconds bounds the summarisation request.
	TimeoutSeconds int `json:"timeoutSeconds"`
}

// Start is the compaction/start payload.
type Start struct {
	Reason       string       `json:"reason"`
	Model        llm.ModelRef `json:"model"`
	UptoSeq      int64        `json:"uptoSeq"`
	TokensBefore int          `json:"tokensBefore"`
}

// End is the compaction/end payload.
type End struct {
	Status string `json:"status"` // completed | skipped
	Method string `json:"method"` // model | extractive
	Pruned int    `json:"pruned"` // tool results pruned before summarising
	// KeptPruned counts tool/pruned events written for the kept region.
	KeptPruned  int    `json:"keptPruned,omitempty"`
	TokensAfter int    `json:"tokensAfter"`
	Error       string `json:"error,omitempty"` // model error that caused the fallback
}

// ErrNothingToCompact is returned when no valid cut point exists.
var ErrNothingToCompact = errors.New("compaction: nothing to compact")

// Service is the `compaction` service. It implements agentloop.Compactor.
type Service struct {
	c       *kernel.Context
	cfg     Config
	agents  *agent.Registry
	llms    *llm.Registry
	toolsR  *tools.Registry
	prompts *prompt.Assembler

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

var _ agentloop.Compactor = (*Service)(nil)

func (s *Service) lock(sessionID string) func() {
	s.mu.Lock()
	m := s.locks[sessionID]
	if m == nil {
		m = &sync.Mutex{}
		s.locks[sessionID] = m
	}
	s.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// Plugin provides the `compaction` service.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/compaction-summary",
	Inject:      []string{agent.ServiceKey, llm.ServiceKey, tools.ServiceKey, prompt.ServiceKey},
	Description: "Context compaction: summary of older turns, tool-result pruning, context usage breakdown, new conversation.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		s := New(c, cfg)
		// The agent is told what compaction keeps (C-1); history tools are
		// mentioned only when an agent can actually see them.
		s.prompts.Register(c, &prompt.Fragment{Name: "context-compaction", Order: 90, Render: func(id string) string {
			t := "Context management: when this conversation grows long, older turns are replaced by a summary. " +
				"Your instructions and long-term memory (MEMORY.md) are always kept, and so are the most recent steps."
			if _, ok := s.toolsR.Get(id, "history_search"); ok {
				t += " The original messages are not lost: use history_search to find earlier details and history_read to read them."
			}
			return t
		}})
		return c.Provide(agentloop.CompactionKey, s)
	},
})

// New builds the service from injected services on c.
func New(c *kernel.Context, cfg Config) *Service {
	if cfg.KeepRecentRatio <= 0 {
		cfg.KeepRecentRatio = 0.2
	}
	if cfg.PruneToolResultChars <= 0 {
		cfg.PruneToolResultChars = 2000
	}
	if cfg.Threshold <= 0 {
		cfg.Threshold = 0.8
	}
	if cfg.DefaultWindow <= 0 {
		cfg.DefaultWindow = 128000
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = 120
	}
	return &Service{c: c, cfg: cfg,
		agents:  kernel.Use[*agent.Registry](c, agent.ServiceKey),
		llms:    kernel.Use[*llm.Registry](c, llm.ServiceKey),
		toolsR:  kernel.Use[*tools.Registry](c, tools.ServiceKey),
		prompts: kernel.Use[*prompt.Assembler](c, prompt.ServiceKey),
		locks:   map[string]*sync.Mutex{},
	}
}

// Config returns the effective configuration.
func (s *Service) Config() Config { return s.cfg }

// ---------------------------------------------------------------- compact

// Compact implements agentloop.Compactor.
func (s *Service) Compact(ctx context.Context, a *agent.Agent, ss *session.Session, model llm.ModelRef, reason string) error {
	_, err := s.compact(ctx, a, ss, model, reason)
	return err
}

// Result describes a finished compaction.
type Result struct {
	session.CompactionData
	Method string `json:"method"`
	Pruned int    `json:"pruned"`
}

func (s *Service) compact(ctx context.Context, a *agent.Agent, ss *session.Session, model llm.ModelRef, reason string) (Result, error) {
	defer s.lock(ss.ID())()
	id := a.ID()
	events := ss.Events()
	win := s.window(ctx, model)
	before := s.estimate(id, events)

	keep := s.cfg.KeepRecentTokens
	if keep <= 0 {
		keep = int(float64(win) * s.cfg.KeepRecentRatio)
	}
	prev, prevEvent := latestCompaction(events, id)
	cut, ok := ChooseCut(events, id, prev.UptoSeq, keep, 2*keep)
	if !ok {
		return Result{}, ErrNothingToCompact
	}
	if _, err := ss.Append(TypeStart, id, Start{Reason: reason, Model: model, UptoSeq: cut, TokensBefore: before}); err != nil {
		return Result{}, err
	}

	// The region being summarised: everything up to the cut, with the
	// previous summary in front of it.
	region := make([]session.Event, 0, len(events))
	for _, e := range events {
		if e.Seq > cut {
			break
		}
		if e.Type == session.TypeCompaction {
			continue
		}
		region = append(region, e)
	}
	if prevEvent != nil {
		region = append(region, *prevEvent)
	}
	msgs := session.DeriveMessages(region, id, s.agents.Names())
	msgs, pruned := Prune(msgs, s.cfg.PruneToolResultChars, ss.ID(), region)

	// Oversized tool results in the kept region (older than the last
	// KeepSteps steps) are pruned in place, so the live context shrinks
	// too; the originals stay readable with history_read.
	keptPruned := 0
	for _, pe := range PruneKept(events, id, cut, s.cfg.PruneToolResultChars, ss.ID()) {
		if _, err := ss.Append(session.TypeToolPruned, id, pe); err == nil {
			keptPruned++
		}
	}
	if keptPruned > 0 {
		events = ss.Events()
	}

	maxTok := s.cfg.SummaryMaxTokens
	if maxTok <= 0 {
		maxTok = min(win/10, 4000)
	}
	method := "model"
	summary, merr := s.summarise(ctx, model, msgs, win, maxTok)
	if merr != nil || strings.TrimSpace(summary) == "" {
		if merr == nil {
			merr = errors.New("empty summary")
		}
		s.c.Log().Warn("compaction: model summary failed, using extractive summary", "agent", id, "err", merr)
		method = "extractive"
		summary = Extractive(msgs, maxTok)
	}
	summary = strings.TrimSpace(summary)

	data := session.CompactionData{Summary: summary, UptoSeq: cut, Before: before}
	// Estimate the context after compaction by projecting the summary.
	raw, _ := json.Marshal(data)
	after := s.estimate(id, append(events, session.Event{Seq: int64(len(events)) + 1, Type: session.TypeCompaction, Agent: id, Data: raw}))
	data.After = after
	if _, err := ss.Append(session.TypeCompaction, id, data); err != nil {
		return Result{}, err
	}
	end := End{Status: "completed", Method: method, Pruned: pruned, KeptPruned: keptPruned, TokensAfter: after}
	if method == "extractive" && merr != nil {
		end.Error = merr.Error()
	}
	_, _ = ss.Append(TypeEnd, id, end)
	return Result{CompactionData: data, Method: method, Pruned: pruned}, nil
}

func (s *Service) summarise(ctx context.Context, model llm.ModelRef, msgs []llm.Message, win, maxTok int) (string, error) {
	ad, ok := s.llms.Get(model.Provider)
	if !ok {
		return "", fmt.Errorf("provider %s not configured", model.Provider)
	}
	if len(msgs) == 0 {
		return "", errors.New("no messages to summarise")
	}
	req := llm.Request{
		Model:     model.Model,
		Effort:    "low",
		System:    summarySystem,
		Messages:  append(append([]llm.Message(nil), msgs...), llm.TextMessage(llm.RoleUser, "Summarise the conversation above now, following the system instructions.")),
		MaxTokens: maxTok,
	}
	// The summarisation request itself must fit: prune harder, then give up
	// (the caller falls back to the extractive summary).
	budget := int(float64(win)*0.9) - maxTok
	if llm.EstimateRequest(req) > budget {
		req.Messages, _ = Prune(req.Messages, 300, "", nil)
		req.Messages = truncateTexts(req.Messages, 1500)
	}
	if llm.EstimateRequest(req) > budget {
		return "", fmt.Errorf("summarisation request too large (%d tokens)", llm.EstimateRequest(req))
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(s.cfg.TimeoutSeconds)*time.Second)
	defer cancel()
	resp, err := ad.Stream(ctx, req, func(llm.Chunk) error { return nil })
	if err != nil {
		return "", err
	}
	return resp.Message.Text(), nil
}

func truncateTexts(msgs []llm.Message, n int) []llm.Message {
	out := make([]llm.Message, len(msgs))
	for i, m := range msgs {
		parts := make([]llm.Part, len(m.Parts))
		copy(parts, m.Parts)
		for j := range parts {
			if parts[j].Type == "text" && len(parts[j].Text) > n {
				parts[j].Text = clip(parts[j].Text, n) + " …[truncated]"
			}
		}
		out[i] = llm.Message{Role: m.Role, Parts: parts}
	}
	return out
}

func latestCompaction(events []session.Event, agentID string) (session.CompactionData, *session.Event) {
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.Type == session.TypeCompaction && (e.Agent == agentID || e.Agent == "") {
			var c session.CompactionData
			if e.Decode(&c) == nil {
				return c, &events[i]
			}
			break
		}
	}
	return session.CompactionData{}, nil
}

// ---------------------------------------------------------------- cut

// visible reports whether e contributes to agentID's model messages.
func visible(e session.Event, agentID string) bool {
	switch e.Type {
	case session.TypeUserMessage:
		return true
	case session.TypeInject:
		return e.Agent == "" || e.Agent == agentID
	case session.TypeAssistantMessage:
		return true
	case session.TypeToolResult:
		return e.Agent == agentID
	}
	return false
}

// EventTokens estimates the model-context cost of one event for agentID,
// consistent with llm.EstimateRequest over DeriveMessages.
func EventTokens(e session.Event, agentID string) int {
	switch e.Type {
	case session.TypeUserMessage:
		var m session.UserMessage
		if e.Decode(&m) == nil {
			return llm.EstimateTokens(m.From+m.Text) + 4
		}
	case session.TypeInject:
		var m session.InjectData
		if e.Decode(&m) == nil {
			return llm.EstimateTokens(m.Text) + 4
		}
	case session.TypeAssistantMessage:
		var m session.AssistantMessage
		if e.Decode(&m) != nil {
			return 0
		}
		if e.Agent != agentID {
			return llm.EstimateTokens(m.Message.Text()) + 4
		}
		n := 0
		for _, p := range m.Message.Parts {
			if p.Type == "thinking" && len(p.Meta) == 0 {
				continue // plain thinking is not sent back (DeriveMessages)
			}
			n += llm.EstimateTokens(p.Text) + 4
			if p.ToolCall != nil {
				n += llm.EstimateTokens(string(p.ToolCall.Args)) + 8
			}
		}
		return n
	case session.TypeToolResult:
		var r llm.ToolResult
		if e.Decode(&r) == nil {
			return llm.EstimateTokens(r.Content) + 12
		}
	}
	return 0
}

// Cut is one candidate compaction boundary: every model-visible event with
// Seq <= Upto would be summarised; Kept tokens remain verbatim.
type Cut struct {
	Upto int64
	Kept int
	Turn bool // the next visible event is a user message (turn boundary)
}

// Cuts lists valid cut points after afterSeq. A cut is valid only where no
// tool call of agentID is waiting for its result (pairs are never split),
// directly before a user message (turn boundary) or before one of the
// agent's assistant messages (step boundary), and where at least one
// visible event remains after it.
func Cuts(events []session.Event, agentID string, afterSeq int64) []Cut {
	var vis []session.Event
	for _, e := range events {
		if e.Seq > afterSeq && visible(e, agentID) {
			vis = append(vis, e)
		}
	}
	// pruned tool results cost what the model sees
	pruned := map[string]string{}
	for _, e := range events {
		if e.Type == session.TypeToolPruned && e.Agent == agentID {
			var p session.PrunedData
			if e.Decode(&p) == nil {
				pruned[p.CallID] = p.Content
			}
		}
	}
	tokens := func(e session.Event) int {
		if e.Type == session.TypeToolResult && len(pruned) > 0 {
			var r llm.ToolResult
			if e.Decode(&r) == nil {
				if c, ok := pruned[r.CallID]; ok {
					return llm.EstimateTokens(c) + 12
				}
			}
		}
		return EventTokens(e, agentID)
	}
	// suffix token sums
	suffix := make([]int, len(vis)+1)
	for i := len(vis) - 1; i >= 0; i-- {
		suffix[i] = suffix[i+1] + tokens(vis[i])
	}
	open := map[string]bool{}
	var out []Cut
	for i, e := range vis {
		if i > 0 && len(open) == 0 {
			turn := e.Type == session.TypeUserMessage || e.Type == session.TypeInject
			step := e.Type == session.TypeAssistantMessage && e.Agent == agentID
			if turn || step {
				out = append(out, Cut{Upto: e.Seq - 1, Kept: suffix[i], Turn: turn})
			}
		}
		switch e.Type {
		case session.TypeAssistantMessage:
			if e.Agent != agentID {
				continue
			}
			var m session.AssistantMessage
			if e.Decode(&m) == nil {
				for _, tc := range m.Message.ToolCalls() {
					open[tc.ID] = true
				}
			}
		case session.TypeToolResult:
			var r llm.ToolResult
			if e.Decode(&r) == nil {
				delete(open, r.CallID)
			}
		}
	}
	return out
}

// ChooseCut picks the cut: the latest turn boundary that keeps at least
// keep tokens, unless that keeps more than maxKeep (one long turn), in
// which case the latest step boundary keeping at least keep tokens. If no
// cut keeps enough, the latest valid cut is used (the context is over the
// threshold, so something must go).
func ChooseCut(events []session.Event, agentID string, afterSeq int64, keep, maxKeep int) (int64, bool) {
	cuts := Cuts(events, agentID, afterSeq)
	if len(cuts) == 0 {
		return 0, false
	}
	var turn, step *Cut
	for i := range cuts {
		c := &cuts[i]
		if c.Kept < keep {
			continue
		}
		step = c
		if c.Turn {
			turn = c
		}
	}
	switch {
	case turn != nil && (turn.Kept <= maxKeep || step == turn):
		return turn.Upto, true
	case step != nil:
		return step.Upto, true
	}
	return cuts[len(cuts)-1].Upto, true
}

// ---------------------------------------------------------------- prune

// Prune shortens tool results longer than maxChars to a head/tail excerpt
// with a pointer to the original event. It returns the new messages and
// the number of pruned results.
func Prune(msgs []llm.Message, maxChars int, sessionID string, events []session.Event) ([]llm.Message, int) {
	seqOf := map[string]int64{}
	for _, e := range events {
		if e.Type == session.TypeToolResult {
			var r llm.ToolResult
			if e.Decode(&r) == nil {
				seqOf[r.CallID] = e.Seq
			}
		}
	}
	n := 0
	out := make([]llm.Message, len(msgs))
	for i, m := range msgs {
		out[i] = m
		if m.Role != llm.RoleTool {
			continue
		}
		parts := make([]llm.Part, len(m.Parts))
		copy(parts, m.Parts)
		for j, p := range parts {
			if p.ToolResult == nil || len(p.ToolResult.Content) <= maxChars {
				continue
			}
			r := *p.ToolResult
			r.Content = excerpt(r.Content, maxChars, sessionID, seqOf[r.CallID])
			parts[j].ToolResult = &r
			n++
		}
		out[i] = llm.Message{Role: m.Role, Parts: parts}
	}
	return out, n
}

// excerpt cuts content to a head/tail excerpt of about maxChars with a
// history_read pointer to the original event (when seq and sessionID are
// known).
func excerpt(content string, maxChars int, sessionID string, seq int64) string {
	head := clip(content, maxChars*3/4)
	tail := clipTail(content, maxChars/4)
	ref := ""
	if seq > 0 && sessionID != "" {
		ref = fmt.Sprintf("; history_read session=%s from=%d", sessionID, seq)
	}
	return fmt.Sprintf("%s\n…[%d chars pruned%s]…\n%s", head, len(content)-len(head)-len(tail), ref, tail)
}

// KeepSteps is the number of most recent steps whose tool results are
// never pruned from the kept region.
const KeepSteps = 2

// PruneKept returns tool/pruned payloads for agentID's tool results after
// cut that are longer than maxChars, not already pruned, and older than
// the last KeepSteps steps (assistant messages of agentID).
func PruneKept(events []session.Event, agentID string, cut int64, maxChars int, sessionID string) []session.PrunedData {
	var steps []int64
	done := map[string]bool{}
	for _, e := range events {
		switch {
		case e.Type == session.TypeAssistantMessage && e.Agent == agentID:
			steps = append(steps, e.Seq)
		case e.Type == session.TypeToolPruned && e.Agent == agentID:
			var p session.PrunedData
			if e.Decode(&p) == nil {
				done[p.CallID] = true
			}
		}
	}
	if len(steps) <= KeepSteps {
		return nil
	}
	boundary := steps[len(steps)-KeepSteps]
	var out []session.PrunedData
	for _, e := range events {
		if e.Seq <= cut || e.Seq >= boundary || e.Type != session.TypeToolResult || e.Agent != agentID {
			continue
		}
		var r llm.ToolResult
		if e.Decode(&r) != nil || done[r.CallID] || len(r.Content) <= maxChars {
			continue
		}
		done[r.CallID] = true
		out = append(out, session.PrunedData{CallID: r.CallID, Content: excerpt(r.Content, maxChars, sessionID, e.Seq)})
	}
	return out
}

// clip returns at most n bytes of s, cut on a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !isRuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func clipTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := len(s) - n
	for i < len(s) && !isRuneStart(s[i]) {
		i++
	}
	return s[i:]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// ---------------------------------------------------------------- extractive

// Extractive builds a deterministic summary from messages: the previous
// summary, user requests, assistant replies and tool calls with the first
// line of their results, within maxTokens. When over budget, the first
// items and the most recent items are kept.
func Extractive(msgs []llm.Message, maxTokens int) string {
	if maxTokens <= 0 {
		maxTokens = 2000
	}
	var lines []string
	toolName := map[string]string{}
	for _, m := range msgs {
		for _, p := range m.Parts {
			switch {
			case p.Type == "text" && m.Role == llm.RoleUser:
				t := strings.TrimSpace(p.Text)
				if rest, ok := strings.CutPrefix(t, "[Summary of earlier conversation]"); ok {
					lines = append(lines, "Earlier summary: "+oneLine(rest, 1500))
					continue
				}
				if t != "" {
					lines = append(lines, "User: "+oneLine(t, 300))
				}
			case p.Type == "text" && m.Role == llm.RoleAssistant:
				if t := strings.TrimSpace(p.Text); t != "" {
					lines = append(lines, "Assistant: "+oneLine(t, 300))
				}
			case p.ToolCall != nil:
				toolName[p.ToolCall.ID] = p.ToolCall.Name
				lines = append(lines, fmt.Sprintf("Tool call %s(%s)", p.ToolCall.Name, oneLine(string(p.ToolCall.Args), 120)))
			case p.ToolResult != nil:
				st := "→"
				if p.ToolResult.IsError {
					st = "→ error:"
				}
				first, _, _ := strings.Cut(strings.TrimSpace(p.ToolResult.Content), "\n")
				lines = append(lines, fmt.Sprintf("  %s %s", st, oneLine(first, 200)))
			}
		}
	}
	header := "[Extractive summary: the model summary was unavailable. Earlier messages, abbreviated; use history_search / history_read for details.]"
	budget := maxTokens - llm.EstimateTokens(header)
	cost := func(ls []string) int {
		n := 0
		for _, l := range ls {
			n += llm.EstimateTokens(l) + 1
		}
		return n
	}
	if cost(lines) > budget {
		headN := min(3, len(lines))
		head := lines[:headN]
		rest := budget - cost(head) - 20
		var tail []string
		for i := len(lines) - 1; i >= headN; i-- {
			c := llm.EstimateTokens(lines[i]) + 1
			if rest-c < 0 {
				break
			}
			rest -= c
			tail = append([]string{lines[i]}, tail...)
		}
		omitted := len(lines) - headN - len(tail)
		lines = append(append(append([]string(nil), head...), fmt.Sprintf("… (%d items omitted)", omitted)), tail...)
	}
	return header + "\n" + strings.Join(lines, "\n")
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return clip(s, n) + "…"
	}
	return s
}
