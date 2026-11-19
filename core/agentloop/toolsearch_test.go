package agentloop_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

// fixture loads the 150 recorded-shape Notion / GitHub MCP tools.
func fixture(t testing.TB) []*tools.Tool {
	t.Helper()
	b, err := os.ReadFile("../tools/testdata/mcp_tools.json")
	if err != nil {
		t.Fatal(err)
	}
	var raw []struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	var out []*tools.Tool
	for _, r := range raw {
		name := r.Name
		out = append(out, &tools.Tool{Name: name, Description: r.Description, Params: r.Parameters, Load: tools.LoadDeferred,
			Category: tools.CategoryRead,
			Execute: func(context.Context, tools.Call) (tools.Result, error) {
				return tools.Text("ran " + name), nil
			}})
	}
	return out
}

func coreTools(n int) []*tools.Tool {
	var out []*tools.Tool
	for i := 0; i < n; i++ {
		out = append(out, &tools.Tool{Name: fmt.Sprintf("core_%02d", i), Description: "a core tool", Category: tools.CategoryRead,
			Params: tools.Schema(map[string]any{"path": tools.Prop("string", "a path")}),
			Execute: func(context.Context, tools.Call) (tools.Result, error) {
				return tools.Text("ok"), nil
			}})
	}
	return out
}

func (e *env) register(ts ...*tools.Tool) {
	r := kernel.Use[*tools.Registry](e.h.Root(), tools.ServiceKey)
	for _, t := range ts {
		r.Register(e.h.Root(), t)
	}
}

// scripted is a mock that answers each step with the next response and
// records requests.
type scripted struct {
	mu    sync.Mutex
	steps []func(req llm.Request) llm.Response
	reqs  []llm.Request
}

func (s *scripted) install(m *mock.Adapter) {
	m.Script = func(req llm.Request) (llm.Response, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.reqs = append(s.reqs, req)
		if len(s.steps) == 0 {
			return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: "end"}, nil
		}
		f := s.steps[0]
		s.steps = s.steps[1:]
		return f(req), nil
	}
}

func (s *scripted) requests() []llm.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]llm.Request(nil), s.reqs...)
}

func call(id, name, args string) func(llm.Request) llm.Response {
	return func(llm.Request) llm.Response {
		return llm.Response{StopReason: "tool_use", Message: llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{
			{Type: "tool_call", ToolCall: &llm.ToolCall{ID: id, Name: name, Args: json.RawMessage(args)}}}}}
	}
}

func names(ts []llm.ToolSchema) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Name)
	}
	return out
}

func has(ts []llm.ToolSchema, name string) bool {
	for _, t := range ts {
		if t.Name == name {
			return true
		}
	}
	return false
}

func TestFewDeferredToolsAreAllSent(t *testing.T) {
	e := newEnv(t, t.TempDir())
	e.register(coreTools(3)...)
	e.register(fixture(t)[:10]...) // 10 small deferred tools: below the thresholds
	ad, _ := e.llms.Get("claude-code")
	var s scripted
	s.install(ad.(*mock.Adapter))
	a, _ := e.agents.Create(agent.Spec{Name: "Small"})
	send(t, a, "hi")
	agentloop.WaitIdle(a, 5*time.Second)
	req := s.requests()[0]
	if len(req.Tools) != 13 || has(req.Tools, agentloop.ToolSearchName) || strings.Contains(req.System, "tool search") {
		t.Fatalf("tools = %v", names(req.Tools))
	}
	// same order as registration (as before tiers existed)
	if req.Tools[0].Name != "core_00" || req.Tools[3].Name != "notion__API_post_page" {
		t.Fatalf("order = %v", names(req.Tools))
	}
	// the agent can force deferral
	a2, _ := e.agents.Create(agent.Spec{Name: "Forced", ToolSearch: tools.DeferAlways})
	send(t, a2, "hi")
	agentloop.WaitIdle(a2, 5*time.Second)
	reqs := s.requests()
	if got := reqs[len(reqs)-1].Tools; len(got) != 5 || got[3].Name != agentloop.ToolSearchName || got[4].Name != agentloop.CallToolName {
		t.Fatalf("forced: %v", names(got))
	}
}

func TestGenericToolSearchLoadsTools(t *testing.T) {
	root := t.TempDir()
	e := newEnv(t, root)
	e.register(coreTools(3)...)
	e.register(fixture(t)...)
	ad, _ := e.llms.Get("claude-code")
	var s scripted
	s.steps = []func(llm.Request) llm.Response{
		call("c1", agentloop.ToolSearchName, `{"query":"create a notion page","limit":3}`),
		call("c2", "notion__API_post_page", `{"parent":{},"properties":{}}`),
		call("c3", agentloop.ToolSearchName, `{"query":"list github pull requests","limit":1}`),
	}
	s.install(ad.(*mock.Adapter))
	a, _ := e.agents.Create(agent.Spec{Name: "Searcher"})
	ss, _ := a.Session()
	send(t, a, "make a page")
	agentloop.WaitIdle(a, 5*time.Second)
	reqs := s.requests()
	if len(reqs) != 4 {
		t.Fatalf("%d requests", len(reqs))
	}
	first := reqs[0]
	if got := names(first.Tools); strings.Join(got, ",") != "core_00,core_01,core_02,tool_search,call_tool" {
		t.Fatalf("first step tools = %v", got)
	}
	if !strings.Contains(first.System, "- notion (79):") || !strings.Contains(first.System, "- github (71):") ||
		!strings.Contains(first.System, "call tool_search") {
		t.Fatalf("system = %s", first.System)
	}
	// the search result gives each tool's full definition, to call through call_tool
	res := toolResult(t, ss, "c1")
	if !strings.Contains(res.Content, "- notion__API_post_page [notion]: Notion | Create a page.") ||
		!strings.Contains(res.Content, "parameters: {") || !strings.Contains(res.Content, "call_tool") {
		t.Fatalf("search result = %s", res.Content)
	}
	// a loaded tool is callable by name too
	if r := toolResult(t, ss, "c2"); r.IsError || r.Content != "ran notion__API_post_page" {
		t.Fatalf("call = %+v", r)
	}
	// loading changes neither the tools nor the system prompt: every request
	// of the warm stretch has the same prefix
	for i, r := range reqs {
		if strings.Join(names(r.Tools), ",") != strings.Join(names(first.Tools), ",") || r.System != first.System {
			t.Fatalf("request %d prefix changed: %v", i, names(r.Tools))
		}
	}
	fourth := names(reqs[3].Tools)

	// compaction summarises the history away; the loaded set stays
	_, _ = ss.Append(session.TypeCompaction, a.ID(), map[string]any{"summary": "earlier work", "uptoSeq": ss.Events()[len(ss.Events())-1].Seq})
	if got := tools.Loaded(ss.Events(), a.ID()); len(got) != 4 {
		t.Fatalf("loaded after compaction = %v", got)
	}

	// a restart keeps it too
	e.h.Close()
	e2 := newEnv(t, root)
	e2.register(coreTools(3)...)
	e2.register(fixture(t)...)
	ad2, _ := e2.llms.Get("claude-code")
	var s2 scripted
	s2.install(ad2.(*mock.Adapter))
	a2, _ := e2.agents.Get(a.ID())
	send(t, a2, "again")
	agentloop.WaitIdle(a2, 5*time.Second)
	// the compaction made the cache cold: the tools loaded before it are
	// in the request's tools now, after the core ones in load order
	after := names(s2.requests()[0].Tools)
	if strings.Join(after[:5], ",") != strings.Join(fourth, ",") || len(after) != 9 || !slices.Contains(after[5:], "notion__API_post_page") || after[8] != "github__list_pull_requests" {
		t.Fatalf("after compaction = %v", after)
	}
}

func TestLoadedToolsJoinWhenTheCacheIsCold(t *testing.T) {
	e := newEnv(t, t.TempDir())
	e.register(coreTools(2)...)
	e.register(fixture(t)...)
	ad, _ := e.llms.Get("claude-code")
	var s scripted
	s.steps = []func(llm.Request) llm.Response{
		call("c1", agentloop.ToolSearchName, `{"query":"select:github__merge_pull_request"}`),
	}
	s.install(ad.(*mock.Adapter))
	a, _ := e.agents.Create(agent.Spec{Name: "Idle"})
	send(t, a, "load it")
	agentloop.WaitIdle(a, 5*time.Second)
	// soon after: still not in the tools
	send(t, a, "again")
	agentloop.WaitIdle(a, 5*time.Second)
	reqs := s.requests()
	if has(reqs[len(reqs)-1].Tools, "github__merge_pull_request") {
		t.Fatalf("added while the cache is warm")
	}
	// past the cache lifetime: added (it costs nothing now)
	restore := agentloop.SetClock(func() time.Time { return time.Now().Add(agentloop.CacheTTL + time.Minute) })
	defer restore()
	send(t, a, "later")
	agentloop.WaitIdle(a, 5*time.Second)
	reqs = s.requests()
	if !has(reqs[len(reqs)-1].Tools, "github__merge_pull_request") {
		t.Fatalf("not added after the cache went cold: %v", names(reqs[len(reqs)-1].Tools))
	}
}

func toolResult(t *testing.T, ss *session.Session, id string) llm.ToolResult {
	t.Helper()
	for _, e := range ss.Events() {
		if e.Type != session.TypeToolResult {
			continue
		}
		var r llm.ToolResult
		if e.Decode(&r) == nil && r.CallID == id {
			return r
		}
	}
	t.Fatalf("no result for %s", id)
	return llm.ToolResult{}
}

func TestToolSearchIndexAndUnknownTools(t *testing.T) {
	e := newEnv(t, t.TempDir())
	e.register(coreTools(2)...)
	e.register(fixture(t)...)
	ad, _ := e.llms.Get("claude-code")
	var s scripted
	s.steps = []func(llm.Request) llm.Response{
		call("i1", agentloop.ToolSearchName, `{}`),
		call("u1", "github__merge_pull_request", `{"owner":"o","repo":"r","pullNumber":1}`),
		call("u2", "made_up_tool", `{}`),
		call("u3", agentloop.CallToolName, `{"name":"github__merge_pull_request","arguments":{"owner":"o","repo":"r","pullNumber":1}}`),
	}
	s.install(ad.(*mock.Adapter))
	a, _ := e.agents.Create(agent.Spec{Name: "Guesser"})
	ss, _ := a.Session()
	send(t, a, "merge it")
	agentloop.WaitIdle(a, 5*time.Second)
	if r := toolResult(t, ss, "i1"); !strings.Contains(r.Content, "- notion (79 tools):") || !strings.Contains(r.Content, "- github (71 tools):") {
		t.Fatalf("index = %s", r.Content)
	}
	if r := toolResult(t, ss, "u1"); !r.IsError || !strings.Contains(r.Content, "It is loaded now") || !strings.Contains(r.Content, `"pullNumber"`) {
		t.Fatalf("unloaded deferred call = %+v", r)
	}
	if r := toolResult(t, ss, "u2"); !r.IsError || !strings.Contains(r.Content, "Use tool_search") {
		t.Fatalf("unknown call = %+v", r)
	}
	if r := toolResult(t, ss, "u3"); r.IsError || r.Content != "ran github__merge_pull_request" {
		t.Fatalf("retry = %+v", r)
	}
	// loaded, but not added to the request's tools: the prefix (and its
	// cache) stays the same
	reqs := s.requests()
	if has(reqs[2].Tools, "github__merge_pull_request") || len(reqs[2].Tools) != len(reqs[0].Tools) {
		t.Fatalf("tools changed after a load: %v", names(reqs[2].Tools))
	}
	// the call through call_tool is recorded as the real tool
	for _, ev := range ss.Events() {
		var d session.ToolCallData
		if ev.Type == session.TypeToolCall && ev.Decode(&d) == nil && d.Call.ID == "u3" && d.Call.Name != "github__merge_pull_request" {
			t.Fatalf("recorded as %q", d.Call.Name)
		}
	}
}

// nativeSearcher is a mock whose provider searches tools itself.
type nativeSearcher struct{ *mock.Adapter }

func (nativeSearcher) SearchesTools(string) bool { return true }

func TestNativeToolSearchSendsDeferredTools(t *testing.T) {
	e := newEnv(t, t.TempDir())
	e.register(coreTools(2)...)
	e.register(fixture(t)...)
	m := mock.New("native", 200000)
	if err := e.llms.Register(e.h.Root(), nativeSearcher{m}); err != nil {
		t.Fatal(err)
	}
	var s scripted
	s.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			return llm.Response{StopReason: "tool_use", Message: llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{
				{Type: llm.PartServerTool, Text: "searched tools", Meta: map[string]string{"kind": llm.ServerToolSearch, "tools": "notion__API_post_page"}},
				{Type: "tool_call", ToolCall: &llm.ToolCall{ID: "n1", Name: "notion__API_post_page", Args: json.RawMessage(`{"parent":{},"properties":{}}`)}},
			}}}
		},
	}
	s.install(m)
	a, _ := e.agents.Create(agent.Spec{Name: "Native", Models: []llm.ModelRef{{Provider: "native", Model: "default"}}})
	ss, _ := a.Session()
	send(t, a, "page")
	agentloop.WaitIdle(a, 5*time.Second)
	reqs := s.requests()
	if len(reqs) != 2 {
		t.Fatalf("%d requests", len(reqs))
	}
	for i, req := range reqs {
		if len(req.Tools) != 152 || has(req.Tools, agentloop.ToolSearchName) || strings.Contains(req.System, "tool search") {
			t.Fatalf("req %d: %d tools", i, len(req.Tools))
		}
		for j, ts := range req.Tools {
			if ts.Deferred != (j >= 2) {
				t.Fatalf("req %d tool %s deferred=%v", i, ts.Name, ts.Deferred)
			}
		}
	}
	if r := toolResult(t, ss, "n1"); r.IsError {
		t.Fatalf("native call = %+v", r)
	}
	if got := tools.LoadedVia(ss.Events(), a.ID(), nil); len(got) != 1 || got[0] != "notion__API_post_page" {
		t.Fatalf("native loads not recorded: %v", got)
	}
	// deferred tools are outside the context until loaded
	if est := llm.EstimateRequest(reqs[0]); est > 2000 {
		t.Fatalf("estimate counts deferred tools: %d", est)
	}
	// after a compaction cut the search out of the history, the tool the
	// provider found is sent loaded (right after the core tools)
	_, _ = ss.Append(session.TypeCompaction, a.ID(), map[string]any{"summary": "s"})
	send(t, a, "again")
	agentloop.WaitIdle(a, 5*time.Second)
	reqs = s.requests()
	last := reqs[len(reqs)-1].Tools
	if len(last) != 152 || last[2].Name != "notion__API_post_page" || last[2].Deferred || !last[3].Deferred {
		t.Fatalf("after compaction: %v", names(last)[:4])
	}
}

// TestSchemaSizeOf180Tools measures what one step carries for an agent
// with 180 tools (150 fixture MCP tools, 30 core).
func TestSchemaSizeOf180Tools(t *testing.T) {
	e := newEnv(t, t.TempDir())
	e.register(coreTools(29)...)
	e.register(fixture(t)...)
	ad, _ := e.llms.Get("claude-code")
	var s scripted
	s.install(ad.(*mock.Adapter))
	never, _ := e.agents.Create(agent.Spec{Name: "Never", ToolSearch: tools.DeferNever})
	auto, _ := e.agents.Create(agent.Spec{Name: "Auto"})
	send(t, never, "hi")
	agentloop.WaitIdle(never, 5*time.Second)
	send(t, auto, "hi")
	agentloop.WaitIdle(auto, 5*time.Second)
	reqs := s.requests()
	size := func(r llm.Request) (int, int) {
		b, _ := json.Marshal(r.Tools)
		return len(r.Tools), len(b) + len(r.System)
	}
	n0, b0 := size(reqs[0])
	n1, b1 := size(reqs[1])
	t.Logf("before: %d tools, %d chars (≈%d tokens); after: %d tools + index, %d chars (≈%d tokens)",
		n0, b0, llm.EstimateRequest(reqs[0]), n1, b1, llm.EstimateRequest(reqs[1]))
	if n0 != 179 || n1 != 31 || b1*5 > b0 {
		t.Fatalf("before %d/%d after %d/%d", n0, b0, n1, b1)
	}
}

// A greeting input is an instruction to the agent: recorded as an inject,
// not as something the person said, and the answer is a normal message.
func TestGreetingIsNotAUserMessage(t *testing.T) {
	e := newEnv(t, t.TempDir())
	ad, _ := e.llms.Get("claude-code")
	var s scripted
	s.install(ad.(*mock.Adapter))
	a, _ := e.agents.Create(agent.Spec{Name: "Greeter"})
	ss, _ := a.Session()
	if _, err := a.Enqueue(agent.Input{Text: agentloop.GreetingPrompt, Tag: agentloop.GreetingTag}); err != nil {
		t.Fatal(err)
	}
	agentloop.WaitIdle(a, 5*time.Second)
	var users, injects, answers int
	tagged := false
	for _, ev := range ss.Events() {
		switch ev.Type {
		case session.TypeUserMessage:
			users++
		case session.TypeInject:
			var d session.InjectData
			if ev.Decode(&d) == nil && d.Source == agentloop.GreetingTag {
				injects++
			}
		case session.TypeAssistantMessage:
			answers++
		case session.TypeTurnStart:
			var d agentloop.TurnStart
			tagged = tagged || (ev.Decode(&d) == nil && d.Tag == agentloop.GreetingTag)
		}
	}
	if users != 0 || injects != 1 || answers == 0 || !tagged {
		t.Fatalf("users=%d injects=%d answers=%d tagged=%v", users, injects, answers, tagged)
	}
	// the model saw the instruction
	if r := s.requests(); len(r) == 0 || !strings.Contains(r[0].Messages[len(r[0].Messages)-1].Text(), "introduce yourself") {
		t.Fatalf("request = %+v", r)
	}
}
