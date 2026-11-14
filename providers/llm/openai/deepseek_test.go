package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/core/llm"
)

func TestDeepSeekDialect(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = map[string]any{}
		json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, s := range []string{
			`{"id":"x","object":"chat.completion.chunk","created":1,"model":"deepseek-flash","choices":[{"index":0,"delta":{"role":"assistant","server_tool_calls":[{"index":0,"reasoning_content":"need to search","calls":[{"index":0,"id":"s1","type":"deepseek_web_search","kind":"deep_search","action":{"type":"search","queries":["deepseek news"]},"status":"completed"}]}]}}]}`,
			`{"id":"x","object":"chat.completion.chunk","created":1,"model":"deepseek-flash","choices":[{"index":0,"delta":{"server_tool_calls":[{"index":1,"calls":[{"index":0,"id":"s2","type":"deepseek_web_search","action":{"type":"open_page","url":"https://example.com/a"}}]}]}}]}`,
			`{"id":"x","object":"chat.completion.chunk","created":1,"model":"deepseek-flash","choices":[{"index":0,"delta":{"content":"They shipped an API."}}]}`,
			`{"id":"x","object":"chat.completion.chunk","created":1,"model":"deepseek-flash","choices":[{"index":0,"delta":{"annotations":[{"type":"url_citation","url_citation":{"url":"https://example.com/a","title":"Example A","start_index":0,"end_index":5}},{"type":"url_citation","url_citation":{"url":"https://example.com/a","title":"dup"}}]},"finish_reason":"stop"}]}`,
			`{"id":"x","object":"chat.completion.chunk","created":1,"model":"deepseek-flash","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110,"prompt_tokens_details":{"cached_tokens":64},"server_tool_use":{"tool_search":1,"tool_fetch":0}}}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", s)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	a, err := New(nil, ProviderConfig{ID: "deepseek", BaseURL: srv.URL, APIKey: "k", SafetyCheck: "high"})
	if err != nil {
		t.Fatal(err)
	}
	var live []string
	resp, err := a.Stream(context.Background(), llm.Request{
		Model: "deepseek-flash", Effort: "high", NativeTools: []string{llm.NativeWebSearch, llm.NativeReadURL},
		Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "news?")},
		Tools:    []llm.ToolSchema{{Name: "bash", Description: "run", Parameters: map[string]any{"type": "object"}}},
	}, func(c llm.Chunk) error { live = append(live, c.Type); return nil })
	if err != nil {
		t.Fatal(err)
	}
	// request shape
	if th, _ := body["thinking"].(map[string]any); th["type"] != "enabled" {
		t.Fatalf("thinking %v", body["thinking"])
	}
	if sc, _ := body["safety_check"].(map[string]any); sc["level"] != "high" {
		t.Fatalf("safety %v", body["safety_check"])
	}
	tools, _ := body["tools"].([]any)
	var types []string
	for _, x := range tools {
		types = append(types, x.(map[string]any)["type"].(string))
	}
	if strings.Join(types, ",") != "function,deepseek_web_search,deepseek_read_url" {
		t.Fatalf("tools %v", types)
	}
	if _, has := body["reasoning_effort"]; has {
		t.Fatal("deepseek must not get reasoning_effort")
	}
	// response
	var cites, searches []string
	for _, p := range resp.Message.Parts {
		switch p.Type {
		case llm.PartCitation:
			cites = append(cites, p.Text+"|"+p.Meta["url"])
		case llm.PartServerTool:
			searches = append(searches, p.Text)
		}
	}
	if strings.Join(cites, ";") != "Example A|https://example.com/a" {
		t.Fatalf("citations %v", cites)
	}
	if strings.Join(searches, ";") != "已搜索：deepseek news;已阅读：https://example.com/a" {
		t.Fatalf("server tools %v", searches)
	}
	if resp.Message.Text() != "They shipped an API." || resp.Usage.ServerTools["tool_search"] != 1 || resp.Usage.CacheRead != 64 {
		t.Fatalf("%q %+v", resp.Message.Text(), resp.Usage)
	}
	if !strings.Contains(strings.Join(live, ","), llm.PartServerTool) {
		t.Fatalf("server tool steps should stream live: %v", live)
	}

	// effort off → thinking disabled; no native tools → plain function tools
	a.Stream(context.Background(), llm.Request{Model: "deepseek-flash", Effort: "minimal", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "x")}}, func(llm.Chunk) error { return nil })
	if th, _ := body["thinking"].(map[string]any); th["type"] != "disabled" {
		t.Fatalf("thinking %v", body["thinking"])
	}

	// "auto" (gateway): deepseek models get the dialect, others do not
	g, _ := New(nil, ProviderConfig{ID: "gw", BaseURL: srv.URL, APIKey: "k", EffortStyle: "auto"})
	g.Stream(context.Background(), llm.Request{Model: "openai/gpt-6-luna", Effort: "high", NativeTools: []string{llm.NativeWebSearch}, Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "x")}}, func(llm.Chunk) error { return nil })
	if body["reasoning_effort"] != "high" || body["thinking"] != nil || body["tools"] != nil {
		t.Fatalf("non-deepseek via gateway: %v", body)
	}
	g.Stream(context.Background(), llm.Request{Model: "deepseek/deepseek-flash", Effort: "high", NativeTools: []string{llm.NativeWebSearch}, Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "x")}}, func(llm.Chunk) error { return nil })
	if body["thinking"] == nil || body["tools"] == nil {
		t.Fatalf("deepseek via gateway: %v", body)
	}
}
