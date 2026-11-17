package agentloop_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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
	if got := reqs[len(reqs)-1].Tools; len(got) != 4 || got[3].Name != agentloop.ToolSearchName {
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
	if got := names(first.Tools); strings.Join(got, ",") != "core_00,core_01,core_02,tool_search" {
		t.Fatalf("first step tools = %v", got)
	}
	if !strings.Contains(first.System, "- notion (79):") || !strings.Contains(first.System, "- github (71):") ||
		!strings.Contains(first.System, "call tool_search") {
		t.Fatalf("system = %s", first.System)
	}
	// the search result names the tools with a one-line description
	res := toolResult(t, ss, "c1")
	if !strings.Contains(res.Content, "- notion__API_post_page [notion]: Notion | Create a page.") {
		t.Fatalf("search result = %s", res.Content)
	}
	second := names(reqs[1].Tools)
	if len(second) != 7 || second[3] != agentloop.ToolSearchName || !has(reqs[1].Tools[4:], "notion__API_post_page") {
		t.Fatalf("second step tools = %v", second)
	}
	if r := toolResult(t, ss, "c2"); r.IsError || r.Content != "ran notion__API_post_page" {
		t.Fatalf("call = %+v", r)
	}
	// a later load appends after the earlier ones: the prefix is stable
	fourth := names(reqs[3].Tools)
	if strings.Join(fourth[:7], ",") != strings.Join(second, ",") || fourth[7] != "github__list_pull_requests" {
		t.Fatalf("fourth step tools = %v", fourth)
	}
	// loaded tools leave the group index in the prompt
	if strings.Contains(reqs[3].System, "- notion (79):") || !strings.Contains(reqs[3].System, "- notion (76):") {
		t.Fatalf("index not updated: %s", reqs[3].System)
	}

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
	after := names(s2.requests()[0].Tools)
	if strings.Join(after, ",") != strings.Join(fourth, ",") {
		t.Fatalf("after restart = %v, want %v", after, fourth)
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
		call("u3", "github__merge_pull_request", `{"owner":"o","repo":"r","pullNumber":1}`),
	}
	s.install(ad.(*mock.Adapter))
	a, _ := e.agents.Create(agent.Spec{Name: "Guesser"})
	ss, _ := a.Session()
	send(t, a, "merge it")
	agentloop.WaitIdle(a, 5*time.Second)
	if r := toolResult(t, ss, "i1"); !strings.Contains(r.Content, "- notion (79 tools):") || !strings.Contains(r.Content, "- github (71 tools):") {
		t.Fatalf("index = %s", r.Content)
	}
	if r := toolResult(t, ss, "u1"); !r.IsError || !strings.Contains(r.Content, "It is loaded now") {
		t.Fatalf("unloaded deferred call = %+v", r)
	}
	if r := toolResult(t, ss, "u2"); !r.IsError || !strings.Contains(r.Content, "Use tool_search") {
		t.Fatalf("unknown call = %+v", r)
	}
	if r := toolResult(t, ss, "u3"); r.IsError || r.Content != "ran github__merge_pull_request" {
		t.Fatalf("retry = %+v", r)
	}
	reqs := s.requests()
	if !has(reqs[2].Tools, "github__merge_pull_request") {
		t.Fatalf("not loaded after the call: %v", names(reqs[2].Tools))
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
	if n0 != 179 || n1 != 30 || b1*5 > b0 {
		t.Fatalf("before %d/%d after %d/%d", n0, b0, n1, b1)
	}
}
