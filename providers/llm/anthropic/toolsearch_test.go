package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/core/llm"
)

func TestSearchesTools(t *testing.T) {
	a := New(nil, ProviderConfig{})
	for model, want := range map[string]bool{
		"claude-opus-5-5": true, "claude-sonnet-5": true, "claude-haiku-4-5-20251001": true, "claude-sonnet-4-5-20250929": true,
		"claude-opus-4-8": true, "claude-sonnet-4-6": true, "claude-fable-5-2": true, "claude-mythos-preview": true,
		"claude-opus-4-1-20250805": false, "claude-opus-4-20250514": false, "claude-sonnet-4-20250514": false,
		"claude-3-7-sonnet-latest": false, "deepseek-chat": false, "": true, // default model
	} {
		if got := a.SearchesTools(model); got != want {
			t.Errorf("%q: %v", model, got)
		}
	}
	off := false
	if New(nil, ProviderConfig{ToolSearch: &off}).SearchesTools("claude-opus-5-5") {
		t.Error("toolSearch: false is ignored")
	}
	var _ llm.ToolSearcher = a
}

// countCache counts cache_control markers anywhere in v.
func countCache(v any) int {
	n := 0
	switch x := v.(type) {
	case map[string]any:
		if _, ok := x["cache_control"]; ok {
			n++
		}
		for _, y := range x {
			n += countCache(y)
		}
	case []any:
		for _, y := range x {
			n += countCache(y)
		}
	}
	return n
}

func bodyOf(t *testing.T, a *Adapter, req llm.Request) map[string]any {
	t.Helper()
	b, err := json.Marshal(a.params(req))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestDeferredToolsAndBreakpoints(t *testing.T) {
	a := New(nil, ProviderConfig{})
	obj := map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}}
	req := llm.Request{Model: "claude-opus-5-5", System: "sys",
		Tools: []llm.ToolSchema{
			{Name: "bash", Parameters: obj}, {Name: "read_file", Parameters: obj},
			{Name: "notion__API_post_page", Description: "Create a page", Parameters: obj, Deferred: true},
			{Name: "github__list_pull_requests", Parameters: obj, Deferred: true},
		},
		Messages: []llm.Message{
			llm.TextMessage(llm.RoleUser, "go"),
			{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: "tool_call", ToolCall: &llm.ToolCall{ID: "t1", Name: "bash", Args: json.RawMessage(`{"q":"ls"}`)}}}},
			{Role: llm.RoleTool, Parts: []llm.Part{{Type: "tool_result", ToolResult: &llm.ToolResult{CallID: "t1", Content: "a.go"}}}},
		}}
	m := bodyOf(t, a, req)
	ts := m["tools"].([]any)
	if len(ts) != 5 {
		t.Fatalf("tools = %v", ts)
	}
	get := func(i int) map[string]any { return ts[i].(map[string]any) }
	if get(0)["name"] != "bash" || get(0)["defer_loading"] != nil || get(0)["cache_control"] != nil {
		t.Fatalf("core tool = %v", get(0))
	}
	if get(1)["name"] != "read_file" || get(1)["defer_loading"] != nil {
		t.Fatalf("core tool = %v", get(1))
	}
	search := get(2)
	if search["type"] != "tool_search_tool_bm25_20251119" || search["name"] != "tool_search_tool_bm25" || search["defer_loading"] != nil ||
		search["cache_control"] == nil {
		t.Fatalf("search tool = %v", search)
	}
	for _, i := range []int{3, 4} {
		if get(i)["defer_loading"] != true || get(i)["cache_control"] != nil {
			t.Fatalf("deferred tool = %v", get(i))
		}
	}
	// system + last tool + last message: three breakpoints, at most four
	if n := countCache(m); n != 3 {
		t.Fatalf("%d breakpoints in %v", n, m)
	}
	msgs := m["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)["content"].([]any)
	if last[len(last)-1].(map[string]any)["cache_control"] == nil {
		t.Fatalf("last message not marked: %v", last)
	}

	// no deferred tools: no search tool; the last core tool is marked
	req.Tools = req.Tools[:2]
	m = bodyOf(t, a, req)
	ts = m["tools"].([]any)
	if len(ts) != 2 || ts[1].(map[string]any)["cache_control"] == nil || countCache(m) != 3 {
		t.Fatalf("tools = %v", ts)
	}
	// prompt cache off: no breakpoints at all
	off := false
	if n := countCache(bodyOf(t, New(nil, ProviderConfig{PromptCache: &off}), req)); n != 0 {
		t.Fatalf("%d breakpoints with promptCache off", n)
	}
	// every request stays within the limit, whatever the history ends with
	req.Messages = append(req.Messages, llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: "thinking", Text: "x", Meta: map[string]string{MetaSignature: "S"}}}})
	if n := countCache(bodyOf(t, a, req)); n > MaxCacheBreakpoints {
		t.Fatalf("%d breakpoints", n)
	}
}

var searchStream = [][2]string{
	{"message_start", `{"type":"message_start","message":{"id":"m2","usage":{"input_tokens":50,"output_tokens":1}}}`},
	{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"tool_search_tool_bm25","input":{}}}`},
	{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"create notion page\"}"}}`},
	{"content_block_stop", `{"type":"content_block_stop","index":0}`},
	{"content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_search_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"tool_search_tool_search_result","tool_references":[{"type":"tool_reference","tool_name":"notion__API_post_page"}]}}}`},
	{"content_block_stop", `{"type":"content_block_stop","index":1}`},
	{"content_block_start", `{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_9","name":"notion__API_post_page","input":{}}}`},
	{"content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"parent\":{}}"}}`},
	{"content_block_stop", `{"type":"content_block_stop","index":2}`},
	{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":20}}`},
	{"message_stop", `{"type":"message_stop"}`},
}

func TestToolSearchBlocksRoundTrip(t *testing.T) {
	var cap captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.set(r)
		sse(w, searchStream...)
	}))
	defer srv.Close()
	a := newAdapter(srv.URL)
	req := llm.Request{Model: "claude-opus-5-5", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "make a page")},
		Tools: []llm.ToolSchema{{Name: "bash"}, {Name: "notion__API_post_page", Deferred: true}}}
	resp, err := a.Stream(context.Background(), req, func(llm.Chunk) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	ps := resp.Message.Parts
	if len(ps) != 4 || ps[0].Type != llm.PartProviderBlock || ps[1].Type != llm.PartProviderBlock || ps[2].Type != llm.PartServerTool || ps[3].Type != "tool_call" {
		t.Fatalf("parts = %+v", ps)
	}
	if ps[2].Meta["kind"] != llm.ServerToolSearch || ps[2].Meta["tools"] != "notion__API_post_page" || !strings.Contains(ps[2].Text, "create notion page") {
		t.Fatalf("summary = %+v", ps[2])
	}
	if tc := resp.Message.ToolCalls(); len(tc) != 1 || tc[0].Name != "notion__API_post_page" || resp.StopReason != "tool_use" {
		t.Fatalf("calls = %+v", tc)
	}

	// next request: the blocks go back unchanged, before the tool_use; no
	// tool_result for the server tool; the summary is not sent
	hist := []llm.Message{req.Messages[0], {Role: llm.RoleAssistant, Parts: ps},
		{Role: llm.RoleTool, Parts: []llm.Part{{Type: "tool_result", ToolResult: &llm.ToolResult{CallID: "toolu_9", Content: "created"}}}}}
	m := bodyOf(t, a, llm.Request{Model: "claude-opus-5-5", Messages: hist, Tools: req.Tools})
	asst := m["messages"].([]any)[1].(map[string]any)["content"].([]any)
	if len(asst) != 3 {
		t.Fatalf("assistant = %v", asst)
	}
	stu, res, use := asst[0].(map[string]any), asst[1].(map[string]any), asst[2].(map[string]any)
	if stu["type"] != "server_tool_use" || stu["id"] != "srvtoolu_1" || stu["name"] != "tool_search_tool_bm25" || stu["input"].(map[string]any)["query"] != "create notion page" {
		t.Fatalf("server_tool_use = %v", stu)
	}
	refs := res["content"].(map[string]any)["tool_references"].([]any)
	if res["type"] != "tool_search_tool_result" || res["tool_use_id"] != "srvtoolu_1" || refs[0].(map[string]any)["tool_name"] != "notion__API_post_page" {
		t.Fatalf("tool_search_tool_result = %v", res)
	}
	if use["type"] != "tool_use" || use["id"] != "toolu_9" {
		t.Fatalf("tool_use = %v", use)
	}
	user := m["messages"].([]any)[2].(map[string]any)["content"].([]any)
	if len(user) != 1 || user[0].(map[string]any)["tool_use_id"] != "toolu_9" {
		t.Fatalf("results = %v", user)
	}
	// another provider's blocks are not ours to replay
	hist[1].Parts = append([]llm.Part{{Type: llm.PartProviderBlock, Meta: map[string]string{"openai:item": "{}"}}}, hist[1].Parts...)
	if c := bodyOf(t, a, llm.Request{Messages: hist}); len(c["messages"].([]any)[1].(map[string]any)["content"].([]any)) != 3 {
		t.Fatal("foreign provider block replayed")
	}
}
