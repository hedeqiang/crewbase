package agentloop

// Deferred tools and tool search.
//
// Every tool is in a load tier (tools.Tool.Load). Core tools go out on
// every step. Deferred tools (MCP servers, connectors, heavy built-ins) go
// out on every step too while they are few and small; once they pass the
// thresholds (tools.DeferConfig: more than 30 tools, or schemas over 10% of
// the model's window) they are held back:
//
//   - adapters with native tool search (llm.ToolSearcher, e.g. Anthropic)
//     get every tool with the deferred ones marked (llm.ToolSchema.Deferred);
//     the provider searches and loads them;
//   - every other adapter gets the core tools, the tool_search and
//     call_tool meta-tools and a prompt section naming the deferred groups.
//     A tool the agent loads (tools/loaded events) is not added to the
//     request's tools right away: that would change the start of the
//     prompt and throw away the provider's prompt cache. tool_search
//     answers with the loaded tools' full definitions instead, and the
//     model calls them through call_tool. They join the request's tools,
//     after the core ones in load order, only when the cache is cold
//     anyway: after a compaction, or when the agent's last request is
//     older than the cache lifetime (cacheTTL). A call to a deferred tool
//     the model has not loaded loads it and asks for a retry; any other
//     unknown tool points at tool_search.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
)

// ToolSearchName is the generic tool-search meta-tool.
const ToolSearchName = "tool_search"

// CallToolName is the generic path's proxy for loaded tools that are not
// in the request's tools yet.
const CallToolName = "call_tool"

// cacheTTL is how long a provider keeps a prompt cache warm without use
// (Anthropic and OpenAI: about 5 minutes). Past it, adding tools costs
// nothing extra.
const cacheTTL = 5 * time.Minute

// clock is the time a request is planned at (tests move it).
var clock = time.Now

// Search result limits.
const (
	searchDefaultLimit = 5
	searchMaxLimit     = 20
)

// toolPlan is the tool set of one request.
type toolPlan struct {
	Tools []llm.ToolSchema
	// Section is the prompt section the generic path adds ("" otherwise).
	Section string
	// Mode: "all" (nothing held back), "native" or "generic".
	Mode string
	// offered names the tools sent (generic mode only).
	offered map[string]bool
}

// Plan modes.
const (
	planAll     = "all"
	planNative  = "native"
	planGeneric = "generic"
)

// offers remembers, per agent and session, what the last generic request
// offered, for the call gate.
type offers struct{ m sync.Map }

func offerKey(agent, sess string) string { return agent + "\x00" + sess }

func (o *offers) set(agent, sess string, p toolPlan) {
	if p.Mode == planGeneric {
		o.m.Store(offerKey(agent, sess), p.offered)
	} else {
		o.m.Delete(offerKey(agent, sess))
	}
}

func (o *offers) get(agent, sess string) (map[string]bool, bool) {
	v, ok := o.m.Load(offerKey(agent, sess))
	if !ok {
		return nil, false
	}
	return v.(map[string]bool), true
}

// tiers splits the tools a may be offered in ss into core (always sent,
// including the agent's AlwaysLoad) and deferred, in registration order.
func (l *loop) tiers(a *agent.Agent, kind string) (core, deferred []*tools.Tool, meta *tools.Tool) {
	core, deferred, metas := l.tiersAll(a, kind)
	return core, deferred, metas[ToolSearchName]
}

// tiersAll is tiers with every meta-tool by name.
func (l *loop) tiersAll(a *agent.Agent, kind string) (core, deferred []*tools.Tool, metas map[string]*tools.Tool) {
	always := a.Spec().AlwaysLoad
	metas = map[string]*tools.Tool{}
	for _, t := range l.toolsReg.Offerable(a.ID(), kind) {
		switch {
		case t.Load == tools.LoadMeta:
			metas[t.Name] = t
		case t.Deferred() && !slices.Contains(always, t.Name) && !slices.Contains(always, tools.GroupOf(t)):
			deferred = append(deferred, t)
		default:
			core = append(core, t)
		}
	}
	return core, deferred, metas
}

// deferConfig is the loop's thresholds with the agent's override.
func (l *loop) deferConfig(a *agent.Agent) tools.DeferConfig {
	c := l.cfg.ToolSearch
	if m := a.Spec().ToolSearch; m != "" {
		c.Mode = m
	}
	return c.WithDefaults()
}

// planTools decides the tool set of a request to ad / model with a
// context window of win tokens.
func (l *loop) planTools(a *agent.Agent, ss *session.Session, ad llm.Adapter, model string, win int) toolPlan {
	if a.IsChat() {
		return toolPlan{Mode: planAll} // a plain chat offers no tools
	}
	core, deferred, metas := l.tiersAll(a, ss.Meta().Kind)
	meta := metas[ToolSearchName]
	schemas := func(ts []*tools.Tool) []llm.ToolSchema {
		out := make([]llm.ToolSchema, 0, len(ts))
		for _, t := range ts {
			out = append(out, t.Schema())
		}
		return out
	}
	if !l.deferConfig(a).ShouldDefer(deferred, win) {
		var all []*tools.Tool
		for _, t := range l.toolsReg.Offerable(a.ID(), ss.Meta().Kind) {
			if t.Load != tools.LoadMeta {
				all = append(all, t)
			}
		}
		return toolPlan{Tools: schemas(all), Mode: planAll}
	}
	byName := map[string]*tools.Tool{}
	for _, t := range deferred {
		byName[t.Name] = t
	}
	if ts, ok := ad.(llm.ToolSearcher); ok && ts.SearchesTools(model) {
		// Tools loaded through our own search (an earlier step on another
		// provider), or by the provider's search before a compaction cut
		// the tool_reference out of the history, stay loaded; the rest
		// are the provider's to find.
		out := schemas(core)
		promoted := map[string]bool{}
		for _, n := range promotedForNative(ss.Events(), a.ID()) {
			if t, ok := byName[n]; ok {
				out = append(out, t.Schema())
				promoted[n] = true
			}
		}
		for _, t := range deferred {
			if !promoted[t.Name] {
				s := t.Schema()
				s.Deferred, s.Group = true, tools.GroupOf(t)
				out = append(out, s)
			}
		}
		return toolPlan{Tools: out, Mode: planNative}
	}
	out := schemas(core)
	offered := map[string]bool{}
	for _, t := range core {
		offered[t.Name] = true
	}
	if meta != nil {
		out = append(out, meta.Schema())
		offered[meta.Name] = true
		if p := metas[CallToolName]; p != nil {
			out = append(out, p.Schema())
			offered[p.Name] = true
		}
	}
	// loaded tools are callable (through call_tool, or by name); only those
	// loaded before the cache last went cold are in the request's tools
	events := ss.Events()
	inTools := map[string]bool{}
	for _, n := range promotedGeneric(events, a.ID(), clock()) {
		inTools[n] = true
	}
	for _, n := range tools.Loaded(events, a.ID()) {
		t, ok := byName[n]
		if !ok || offered[n] {
			continue
		}
		offered[n] = true
		if inTools[n] {
			out = append(out, t.Schema())
		}
	}
	section := ""
	if meta != nil {
		// every deferred group, loaded or not: the section stays the same
		// as tools load, so the system prompt keeps its cache
		section = searchSection(deferred)
	}
	return toolPlan{Tools: out, Section: section, Mode: planGeneric, offered: offered}
}

// promotedGeneric lists the loaded tools a generic request sends in its
// tools: those loaded before the agent's cache last went cold (its latest
// compaction, or a request more than cacheTTL after the one before), in
// load order. A request now, after more than cacheTTL, is such a point.
func promotedGeneric(events []session.Event, agentID string, now time.Time) []string {
	var cold int64
	var last time.Time
	for _, e := range events {
		if e.Agent != agentID && !(e.Type == session.TypeCompaction && e.Agent == "") {
			continue
		}
		switch e.Type {
		case session.TypeCompaction:
			cold = e.Seq
		case session.TypeStepStart:
			if !last.IsZero() && e.Time.Sub(last) > cacheTTL {
				cold = e.Seq
			}
			last = e.Time
		case session.TypeAssistantMessage:
			last = e.Time // the cache was last used when the answer came
		}
	}
	if !last.IsZero() && now.Sub(last) > cacheTTL {
		return tools.Loaded(events, agentID)
	}
	var before []session.Event
	for _, e := range events {
		if e.Seq < cold {
			before = append(before, e)
		}
	}
	return tools.Loaded(before, agentID)
}

// promotedForNative lists the loaded tools a native-search request sends
// as loaded: those loaded by our own search or a call, and those the
// provider loaded before the latest compaction of agent's history.
func promotedForNative(events []session.Event, agentID string) []string {
	var compacted int64
	for _, e := range events {
		if e.Type == session.TypeCompaction && (e.Agent == agentID || e.Agent == "") {
			compacted = e.Seq
		}
	}
	var before []session.Event
	for _, e := range events {
		if e.Seq < compacted {
			before = append(before, e)
		}
	}
	keep := map[string]bool{}
	for _, n := range tools.LoadedVia(before, agentID, nil) {
		keep[n] = true
	}
	for _, n := range tools.LoadedVia(events, agentID, func(v string) bool { return v != tools.ViaNative }) {
		keep[n] = true
	}
	var out []string
	for _, n := range tools.Loaded(events, agentID) {
		if keep[n] {
			out = append(out, n)
		}
	}
	return out
}

// searchSection is the prompt section about the deferred tools not loaded
// yet: one line per group.
func searchSection(rest []*tools.Tool) string {
	if len(rest) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## More tools (tool search)\n")
	b.WriteString("You have more tools than are loaded. They are grouped as follows:\n")
	for _, g := range tools.Groups(rest) {
		fmt.Fprintf(&b, "- %s (%d): %s\n", g.Name, g.Count, g.Summary)
	}
	b.WriteString("To use one, first call " + ToolSearchName + " with what you want to do (e.g. \"create a page\", \"list pull requests\"). " +
		"It answers with the matching tools' definitions; call one with " + CallToolName + " (its name and arguments that match its parameters), " +
		"or directly when it is in your tools. Do not guess tool names that are not loaded.")
	return b.String()
}

// gate handles calls to tools the model was not offered (generic mode).
func (l *loop) gate(call tools.Call, t *tools.Tool, found bool) (tools.Result, bool) {
	ss := call.Session
	if ss == nil {
		return tools.Result{}, false
	}
	offered, ok := l.offers.get(call.Agent, ss.ID())
	if !ok || offered[call.Name] {
		return tools.Result{}, false
	}
	if found && t.Deferred() {
		if _, err := tools.MarkLoaded(ss, call.Agent, []string{call.Name}, tools.ViaCall); err != nil {
			return tools.Errorf("could not load %s: %v", call.Name, err), true
		}
		return tools.Errorf("%s was not loaded, so you called it without seeing its parameters. It is loaded now; its definition:\n%s\n"+
			"Call it again through %s with arguments that match these parameters.", call.Name, definition(t), CallToolName), true
	}
	if !found {
		return tools.Errorf("unknown tool: %s. Use %s to find the tool you need (an empty query lists the tool groups).", call.Name, ToolSearchName), true
	}
	return tools.Result{}, false
}

// searchTool is the generic tool_search meta-tool.
func (l *loop) searchTool() *tools.Tool {
	return &tools.Tool{
		Name: ToolSearchName,
		Description: "Find and load more tools. Describe what you want to do in a few words (e.g. \"create a notion page\", " +
			"\"list github pull requests\"); the best matching tools are loaded and callable from your next step. " +
			"An empty query lists the tool groups. \"select:name1,name2\" loads tools by exact name.",
		Params: tools.Schema(map[string]any{
			"query": tools.Prop("string", "What you want to do, in a few words; empty for the list of tool groups."),
			"limit": tools.Prop("integer", fmt.Sprintf("How many tools to load (default %d, at most %d).", searchDefaultLimit, searchMaxLimit)),
		}),
		Category: tools.CategoryNone,
		Load:     tools.LoadMeta,
		Execute:  l.runSearch,
	}
}

// SearchHit is one tool_search result (Result.Data).
type SearchHit struct {
	Name        string `json:"name"`
	Group       string `json:"group"`
	Description string `json:"description"`
}

func (l *loop) runSearch(_ context.Context, call tools.Call) (tools.Result, error) {
	var args struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := call.Decode(&args); err != nil {
		return tools.Errorf("bad arguments: %v", err), nil
	}
	a, ok := l.agents.Get(call.Agent)
	if !ok || call.Session == nil {
		return tools.Errorf("tool search needs an agent session"), nil
	}
	ss := call.Session
	_, deferred, _ := l.tiers(a, ss.Meta().Kind)
	if len(deferred) == 0 {
		return tools.Text("There are no more tools to load: every tool you have is already loaded."), nil
	}
	q := strings.TrimSpace(args.Query)
	if q == "" {
		var b strings.Builder
		b.WriteString("Tool groups (call " + ToolSearchName + " with what you want to do to load tools):\n")
		for _, g := range tools.Groups(deferred) {
			fmt.Fprintf(&b, "- %s (%d tools): %s\n", g.Name, g.Count, g.Summary)
		}
		return tools.Result{Content: strings.TrimRight(b.String(), "\n"), Data: tools.Groups(deferred)}, nil
	}
	limit := args.Limit
	if limit <= 0 {
		limit = searchDefaultLimit
	}
	limit = min(limit, searchMaxLimit)
	var found []*tools.Tool
	if sel, ok := strings.CutPrefix(q, "select:"); ok {
		want := map[string]bool{}
		for _, n := range strings.Split(sel, ",") {
			want[strings.TrimSpace(n)] = true
		}
		for _, t := range deferred {
			if want[t.Name] {
				found = append(found, t)
			}
		}
	} else {
		for _, h := range tools.NewIndex(deferred).Search(q, limit) {
			found = append(found, h.Tool)
		}
	}
	if len(found) == 0 {
		return tools.Text(fmt.Sprintf("No tools matched %q. Try other words, or an empty query for the list of tool groups.", q)), nil
	}
	names := make([]string, 0, len(found))
	hits := make([]SearchHit, 0, len(found))
	var b strings.Builder
	b.WriteString("Loaded these tools. Call one with " + CallToolName + " {\"name\": \"<tool>\", \"arguments\": {...}} " +
		"(or directly if it is in your tools); its arguments must match the parameters below.\n")
	for _, t := range found {
		names = append(names, t.Name)
		h := SearchHit{Name: t.Name, Group: tools.GroupOf(t), Description: tools.OneLine(t.Description)}
		hits = append(hits, h)
		fmt.Fprintf(&b, "\n- %s [%s]: %s\n%s\n", h.Name, h.Group, h.Description, definition(t))
	}
	if _, err := tools.MarkLoaded(ss, call.Agent, names, tools.ViaSearch); err != nil {
		return tools.Errorf("could not load the tools: %v", err), nil
	}
	return tools.Result{Content: strings.TrimRight(b.String(), "\n"), Data: hits}, nil
}

// recordNativeLoads records the tools a provider's own search loaded
// (llm.PartServerTool parts with kind "tool_search", Meta["tools"]), so
// they stay known after compaction and across providers.
func recordNativeLoads(ss *session.Session, agentID string, m llm.Message) {
	var names []string
	for _, p := range m.Parts {
		if p.Type == llm.PartServerTool && p.Meta["kind"] == llm.ServerToolSearch && p.Meta["tools"] != "" {
			names = append(names, strings.Split(p.Meta["tools"], ",")...)
		}
	}
	if len(names) > 0 {
		_, _ = tools.MarkLoaded(ss, agentID, names, tools.ViaNative)
	}
}

// definition is a tool's full description and parameters, as tool_search
// and the gate show them (the model calls it through call_tool).
func definition(t *tools.Tool) string {
	p, _ := json.Marshal(t.Schema().Parameters)
	d := strings.TrimSpace(t.Description)
	return "  description: " + strings.ReplaceAll(d, "\n", "\n  ") + "\n  parameters: " + string(p)
}

// callTool is the generic path's proxy: the loop runs a call to it as a
// call to the tool it names (unwrapCall), so recording, approvals and the
// person's view all see the real tool. It runs itself only when the call
// cannot be unwrapped.
func (l *loop) callTool() *tools.Tool {
	return &tools.Tool{
		Name: CallToolName,
		Description: "Call a tool that " + ToolSearchName + " loaded but that is not in your tools yet. " +
			"name: the tool's name; arguments: an object matching that tool's parameters, as " + ToolSearchName + " showed them.",
		Params: tools.Schema(map[string]any{
			"name":      tools.Prop("string", "The loaded tool's exact name."),
			"arguments": map[string]any{"type": "object", "description": "The tool's arguments, matching its parameters."},
		}, "name"),
		Category: tools.CategoryNone,
		Load:     tools.LoadMeta,
		Execute: func(_ context.Context, call tools.Call) (tools.Result, error) {
			return tools.Errorf("%s needs {\"name\": \"<a loaded tool>\", \"arguments\": {...}}; use %s first to load the tool you need.", CallToolName, ToolSearchName), nil
		},
	}
}

// unwrapCall turns a call_tool call into the call it stands for. It
// accepts arguments given as a JSON string too (some models send that).
func unwrapCall(name string, args json.RawMessage) (string, json.RawMessage, bool) {
	if name != CallToolName {
		return name, args, false
	}
	var w struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(args, &w) != nil {
		return name, args, false
	}
	w.Name = strings.TrimSpace(w.Name)
	if w.Name == "" || w.Name == CallToolName || w.Name == ToolSearchName {
		return name, args, false
	}
	inner := w.Arguments
	var str string
	if json.Unmarshal(inner, &str) == nil {
		inner = json.RawMessage(str)
	}
	if len(inner) == 0 || string(inner) == "null" {
		inner = json.RawMessage("{}")
	}
	if !json.Valid(inner) {
		return name, args, false
	}
	return w.Name, inner, true
}
