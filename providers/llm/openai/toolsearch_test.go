package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/core/llm"
)

var _ llm.ToolSearcher = (*Adapter)(nil)

func TestSearchesTools(t *testing.T) {
	official := newAdapter(t, "http://x", func(pc *ProviderConfig) { pc.Preset = "openai" })
	for m, want := range map[string]bool{"gpt-5.4": true, "gpt-5.4-mini": true, "gpt-5.10": true, "gpt-6.1-sol": true,
		"gpt-5.2": false, "gpt-5": false, "o3": false, "gpt-4.1": false} {
		if official.SearchesTools(m) != want {
			t.Errorf("official %s: want %v", m, want)
		}
	}
	// gateways: off unless forced (they may not pass the tool through)
	if magpieLike(t, "http://x").SearchesTools("openai/gpt-6.1-sol") {
		t.Error("magpie searches tools by default")
	}
	on, off := true, false
	forced := newAdapter(t, "http://x", func(pc *ProviderConfig) { pc.Responses = ResponsesAuto; pc.ToolSearch = &on })
	if !forced.SearchesTools("gpt-6.1-sol") || forced.SearchesTools("deepseek-chat") {
		t.Error("toolSearch: true should apply to models routed through Responses only")
	}
	if newAdapter(t, "http://x", func(pc *ProviderConfig) { pc.Preset = "openai"; pc.ToolSearch = &off }).SearchesTools("gpt-6.1-sol") {
		t.Error("toolSearch: false ignored")
	}
	never := newAdapter(t, "http://x", func(pc *ProviderConfig) { pc.Preset = "openai"; pc.Responses = ResponsesNever })
	if never.SearchesTools("gpt-6.1-sol") {
		t.Error("tool search without Responses")
	}
	official.routes.setResponsesMissing()
	if official.SearchesTools("gpt-6.1-sol") {
		t.Error("tool search after Responses went missing")
	}
}

// Recorded shape of a hosted tool search turn (Tool search guide):
// tool_search_call and tool_search_output with execution "server" and a
// null call_id, then a call to a loaded, namespaced function.
const searchOutput = `{"type":"tool_search_call","id":"ts_1","execution":"server","call_id":null,"status":"completed","arguments":{"paths":["github"]}},` +
	`{"type":"tool_search_output","id":"tso_1","execution":"server","call_id":null,"status":"completed","tools":[{"type":"namespace","name":"crew_github","description":"Tools of github","tools":[` +
	`{"type":"function","name":"github__create_issue","description":"Open an issue","parameters":{"type":"object"},"defer_loading":true}]}]},` +
	`{"type":"function_call","id":"fc_s","call_id":"call_s1","namespace":"crew_github","name":"github__create_issue","arguments":"{\"title\":\"Bug\"}","status":"completed"}`

func TestToolSearchRequestAndReplay(t *testing.T) {
	g := &gateway{responses: func(w http.ResponseWriter, _ map[string]any) {
		sse(w, completed(searchOutput, `{"input_tokens":500,"output_tokens":20}`))
	}}
	srv := httptest.NewServer(g)
	defer srv.Close()
	a := newAdapter(t, srv.URL, func(pc *ProviderConfig) { pc.Preset = "openai" })
	tools := []llm.ToolSchema{{Name: "read_file", Description: "Read a file"}}
	for i := 0; i < 10; i++ {
		tools = append(tools, llm.ToolSchema{Name: fmt.Sprintf("github__tool_%d", i), Description: "GitHub", Deferred: true, Group: "github"})
	}
	tools = append(tools, llm.ToolSchema{Name: "notion__search", Description: "Search Notion", Deferred: true}, // group from the name
		llm.ToolSchema{Name: "x", Description: "MCP server with odd id", Deferred: true, Group: "my server.io"})
	req := llm.Request{Model: "gpt-6.1-sol", Effort: "high", Tools: tools, Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "file a bug")}}
	resp, err := a.Stream(context.Background(), req, func(llm.Chunk) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	_, b := g.last()
	var shape []string
	for _, x := range b["tools"].([]any) {
		m := x.(map[string]any)
		switch m["type"] {
		case "function":
			if m["defer_loading"] != nil {
				t.Fatalf("top-level function deferred: %v", m)
			}
			shape = append(shape, "fn:"+m["name"].(string))
		case "namespace":
			fns := m["tools"].([]any)
			for _, f := range fns {
				if f.(map[string]any)["defer_loading"] != true {
					t.Fatalf("namespaced function not deferred: %v", f)
				}
			}
			shape = append(shape, fmt.Sprintf("ns:%s(%d)", m["name"], len(fns)))
		default:
			shape = append(shape, m["type"].(string))
		}
	}
	if want := "fn:read_file ns:crew_github(9) ns:crew_github_2(1) ns:crew_notion(1) ns:crew_my_server_io(1) tool_search"; strings.Join(shape, " ") != want {
		t.Fatalf("tools %v", shape)
	}

	// parsed: the two items to replay, a summary naming the loaded tools,
	// the call with its namespace
	var kinds []string
	for _, p := range resp.Message.Parts {
		kinds = append(kinds, p.Type)
	}
	if want := "provider_block provider_block server_tool tool_call"; strings.Join(kinds, " ") != want {
		t.Fatalf("parts %v", kinds)
	}
	sum := resp.Message.Parts[2]
	if sum.Meta["kind"] != llm.ServerToolSearch || sum.Meta["tools"] != "github__create_issue" || !strings.Contains(sum.Text, "github") {
		t.Fatalf("summary %+v", sum)
	}
	call := resp.Message.Parts[3]
	if call.ToolCall.Name != "github__create_issue" || call.Meta[MetaNamespace] != "crew_github" || resp.StopReason != "tool_use" {
		t.Fatalf("call %+v", call)
	}

	// next request: the search items in place, the call with its namespace
	req.Messages = append(req.Messages, resp.Message, llm.Message{Role: llm.RoleTool, Parts: []llm.Part{{Type: "tool_result",
		ToolResult: &llm.ToolResult{CallID: "call_s1", Name: "github__create_issue", Content: "#12"}}}})
	if _, err := a.Stream(context.Background(), req, func(llm.Chunk) error { return nil }); err != nil {
		t.Fatal(err)
	}
	_, b = g.last()
	in := b["input"].([]any)
	var types []string
	for _, x := range in {
		m := x.(map[string]any)
		ty, _ := m["type"].(string)
		if ty == "" {
			ty = "msg:" + m["role"].(string)
		}
		types = append(types, ty)
	}
	if want := "msg:user tool_search_call tool_search_output function_call function_call_output"; strings.Join(types, " ") != want {
		t.Fatalf("input %v", types)
	}
	if o := in[2].(map[string]any); o["execution"] != "server" || len(o["tools"].([]any)) != 1 {
		t.Fatalf("search output %v", o)
	}
	if fc := in[3].(map[string]any); fc["namespace"] != "crew_github" || fc["name"] != "github__create_issue" {
		t.Fatalf("call %v", fc)
	}

	// a later request without deferred tools (or on another model): no
	// search items, no namespace
	req.Tools = tools[:1]
	if _, err := a.Stream(context.Background(), req, func(llm.Chunk) error { return nil }); err != nil {
		t.Fatal(err)
	}
	_, b = g.last()
	s, _ := json.Marshal(b["input"])
	if strings.Contains(string(s), "tool_search") || strings.Contains(string(s), `"namespace"`) {
		t.Fatalf("search items replayed without tool search: %s", s)
	}
	for _, x := range b["tools"].([]any) {
		if x.(map[string]any)["type"] != "function" {
			t.Fatalf("tools %v", b["tools"])
		}
	}
}

func TestNamespaceNotReserved(t *testing.T) {
	for in, want := range map[string]string{"browser": "crew_browser", "history": "crew_history", "Python": "crew_Python",
		"git": "crew_git", "my server!": "crew_my_server", "": "crew_tools"} {
		if got := namespaceName(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	if n := namespaceName(strings.Repeat("x", 80)); len(n) != 48 || !strings.HasPrefix(n, namespacePrefix) {
		t.Fatalf("long: %q", n)
	}
}

// A conversation recorded before namespaces had the prefix (or one whose
// group was renamed): its search items are left out, its calls stay
// without the old namespace, and no reserved name reaches the request.
func TestOldNamespacesAreNotReplayed(t *testing.T) {
	g := &gateway{responses: func(w http.ResponseWriter, _ map[string]any) {
		sse(w, completed(`{"type":"message","id":"m1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}`, `{"input_tokens":5,"output_tokens":1}`))
	}}
	srv := httptest.NewServer(g)
	defer srv.Close()
	a := newAdapter(t, srv.URL, func(pc *ProviderConfig) { pc.Preset = "openai" })
	old := `{"type":"tool_search_output","id":"tso_0","execution":"server","call_id":null,"status":"completed","tools":[{"type":"namespace","name":"history","tools":[{"type":"function","name":"history_search","parameters":{"type":"object"},"defer_loading":true}]}]}`
	call := `{"type":"tool_search_call","id":"ts_0","execution":"server","call_id":null,"status":"completed","arguments":{"paths":["history"]}}`
	asst := llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{
		{Type: llm.PartProviderBlock, Meta: map[string]string{MetaBlock: call}},
		{Type: llm.PartProviderBlock, Meta: map[string]string{MetaBlock: old}},
		{Type: "tool_call", ToolCall: &llm.ToolCall{ID: "call_h", Name: "history_search", Args: json.RawMessage(`{"q":"x"}`)}, Meta: map[string]string{MetaNamespace: "history"}},
	}}
	req := llm.Request{Model: "gpt-6.2-sol", Tools: []llm.ToolSchema{
		{Name: "read_file", Description: "Read"},
		{Name: "history_search", Description: "Search history", Deferred: true, Group: "history"},
		{Name: "browser_click", Description: "Click", Deferred: true, Group: "browser"},
	}, Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "find it"), asst,
		{Role: llm.RoleTool, Parts: []llm.Part{{Type: "tool_result", ToolResult: &llm.ToolResult{CallID: "call_h", Name: "history_search", Content: "found"}}}}}}
	if _, err := a.Stream(context.Background(), req, func(llm.Chunk) error { return nil }); err != nil {
		t.Fatal(err)
	}
	_, b := g.last()
	s, _ := json.Marshal(b)
	for _, bad := range []string{`"name":"history"`, `"name":"browser"`, `"namespace":"history"`, "tool_search_output", "tool_search_call"} {
		if strings.Contains(string(s), bad) {
			t.Errorf("request carries %s:\n%s", bad, s)
		}
	}
	if !strings.Contains(string(s), `"name":"crew_history"`) || !strings.Contains(string(s), `"call_id":"call_h"`) {
		t.Fatalf("namespaces or the call missing: %s", s)
	}
}
