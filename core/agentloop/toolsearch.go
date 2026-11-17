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
//   - every other adapter gets the core tools, the tool_search meta-tool,
//     a prompt section naming the deferred groups, and the tools the agent
//     has loaded in this session (tools/loaded events), after the core
//     tools in load order. A call to a deferred tool the model has not
//     loaded loads it and asks for a retry; any other unknown tool points
//     at tool_search.

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
)

// ToolSearchName is the generic tool-search meta-tool.
const ToolSearchName = "tool_search"

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
	always := a.Spec().AlwaysLoad
	for _, t := range l.toolsReg.Offerable(a.ID(), kind) {
		switch {
		case t.Load == tools.LoadMeta:
			if t.Name == ToolSearchName {
				meta = t
			}
		case t.Deferred() && !slices.Contains(always, t.Name) && !slices.Contains(always, tools.GroupOf(t)):
			deferred = append(deferred, t)
		default:
			core = append(core, t)
		}
	}
	return core, deferred, meta
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
	core, deferred, meta := l.tiers(a, ss.Meta().Kind)
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
	}
	for _, n := range tools.Loaded(ss.Events(), a.ID()) {
		if t, ok := byName[n]; ok && !offered[n] {
			out = append(out, t.Schema())
			offered[n] = true
		}
	}
	var rest []*tools.Tool
	for _, t := range deferred {
		if !offered[t.Name] {
			rest = append(rest, t)
		}
	}
	section := ""
	if meta != nil {
		section = searchSection(rest)
	}
	return toolPlan{Tools: out, Section: section, Mode: planGeneric, offered: offered}
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
		"The matching tools are loaded and you can call them from your next step. Do not guess tool names that are not loaded.")
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
		return tools.Errorf("%s was not loaded, so you called it without seeing its parameters. It is loaded now: "+
			"from your next step its full definition is in your tools. Call it again with arguments that match that definition.", call.Name), true
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
	b.WriteString("Loaded these tools; you can call them from your next step:\n")
	for _, t := range found {
		names = append(names, t.Name)
		h := SearchHit{Name: t.Name, Group: tools.GroupOf(t), Description: tools.OneLine(t.Description)}
		hits = append(hits, h)
		fmt.Fprintf(&b, "- %s [%s]: %s\n", h.Name, h.Group, h.Description)
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
