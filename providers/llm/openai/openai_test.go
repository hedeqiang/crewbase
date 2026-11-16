package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/kernel"
)

func sse(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range events {
		fmt.Fprintf(w, "data: %s\n\n", e)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

type captured struct {
	mu   sync.Mutex
	body map[string]any
	auth string
}

func (c *captured) set(r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.body = nil
	_ = json.Unmarshal(b, &c.body)
	c.auth = r.Header.Get("Authorization")
}

func newAdapter(t *testing.T, url string, mod func(*ProviderConfig)) *Adapter {
	t.Helper()
	pc := ProviderConfig{ID: "test", BaseURL: url, APIKey: "sk-testsecretkey1234567890"}
	if mod != nil {
		mod(&pc)
	}
	a, err := New(nil, pc)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestStreamText(t *testing.T) {
	var cap captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		cap.set(r)
		sse(w,
			`{"choices":[{"delta":{"role":"assistant","reasoning_content":"hmm"}}]}`,
			`{"choices":[{"delta":{"content":"Hel"}}]}`,
			`{"choices":[{"delta":{"content":"lo"},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":6}}}`,
			`[DONE]`)
	}))
	defer srv.Close()
	a := newAdapter(t, srv.URL, nil)
	var chunks []llm.Chunk
	resp, err := a.Stream(context.Background(), llm.Request{Model: "m", System: "be nice", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}},
		func(c llm.Chunk) error { chunks = append(chunks, c); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Text() != "Hello" || resp.StopReason != "end" {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Usage.InputTokens != 11 || resp.Usage.OutputTokens != 2 || resp.Usage.CacheRead != 6 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	if len(chunks) != 3 || chunks[0].Type != "thinking" || chunks[1].Text != "Hel" {
		t.Fatalf("chunks = %+v", chunks)
	}
	if cap.auth != "Bearer sk-testsecretkey1234567890" {
		t.Fatalf("auth header = %q", cap.auth)
	}
	msgs := cap.body["messages"].([]any)
	if m0 := msgs[0].(map[string]any); m0["role"] != "system" || m0["content"] != "be nice" {
		t.Fatalf("system = %v", m0)
	}
	if cap.body["stream"] != true || cap.body["stream_options"] == nil {
		t.Fatalf("body = %v", cap.body)
	}
}

func TestStreamToolCallAssembly(t *testing.T) {
	var cap captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.set(r)
		sse(w,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"pa"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_b","function":{"name":"ls","arguments":""}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"x\"}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			`[DONE]`)
	}))
	defer srv.Close()
	a := newAdapter(t, srv.URL, nil)
	var got []llm.ToolCall
	req := llm.Request{Model: "m",
		Tools: []llm.ToolSchema{{Name: "read", Description: "read a file"}},
		Messages: []llm.Message{
			llm.TextMessage(llm.RoleUser, "go"),
			{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: "text", Text: "ok"}, {Type: "tool_call", ToolCall: &llm.ToolCall{ID: "c0", Name: "read", Args: json.RawMessage(`{"path":"a"}`)}}}},
			{Role: llm.RoleTool, Parts: []llm.Part{{Type: "tool_result", ToolResult: &llm.ToolResult{CallID: "c0", Name: "read", Content: "data"}}}},
		}}
	resp, err := a.Stream(context.Background(), req, func(c llm.Chunk) error {
		if c.Type == "tool_call" {
			got = append(got, *c.ToolCall)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "tool_use" || len(got) != 2 {
		t.Fatalf("stop=%s got=%+v", resp.StopReason, got)
	}
	if got[0].ID != "call_a" || got[0].Name != "read" || string(got[0].Args) != `{"path":"x"}` {
		t.Fatalf("call0 = %+v", got[0])
	}
	if got[1].Name != "ls" || string(got[1].Args) != "{}" {
		t.Fatalf("call1 = %+v", got[1])
	}
	msgs := cap.body["messages"].([]any)
	as := msgs[1].(map[string]any)
	tcs := as["tool_calls"].([]any)
	fn := tcs[0].(map[string]any)["function"].(map[string]any)
	if as["role"] != "assistant" || as["content"] != "ok" || fn["arguments"] != `{"path":"a"}` {
		t.Fatalf("assistant = %v", as)
	}
	tm := msgs[2].(map[string]any)
	if tm["role"] != "tool" || tm["tool_call_id"] != "c0" || tm["content"] != "data" {
		t.Fatalf("tool msg = %v", tm)
	}
	tools := cap.body["tools"].([]any)
	if tools[0].(map[string]any)["type"] != "function" {
		t.Fatalf("tools = %v", tools)
	}
}

func TestMaxTokensStop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"choices":[{"delta":{"content":"x"},"finish_reason":"length"}]}`, `[DONE]`)
	}))
	defer srv.Close()
	resp, err := newAdapter(t, srv.URL, nil).Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, func(llm.Chunk) error { return nil })
	if err != nil || resp.StopReason != "max_tokens" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	if resp.Usage.InputTokens == 0 {
		t.Fatal("expected estimated usage")
	}
}

func TestErrorClasses(t *testing.T) {
	const key = "sk-testsecretkey1234567890"
	cases := []struct {
		name   string
		status int
		header map[string]string
		body   string
		kind   llm.ErrorKind
		retry  time.Duration
	}{
		{"rate", 429, map[string]string{"Retry-After": "7"}, `{"error":{"message":"Rate limit reached for requests","type":"requests","code":"rate_limit_exceeded"}}`, llm.ErrRateLimited, 7 * time.Second},
		{"quota429", 429, nil, `{"error":{"message":"You exceeded your current quota, please check your plan and billing details.","type":"insufficient_quota","code":"insufficient_quota"}}`, llm.ErrQuotaExhausted, 0},
		{"payment", 402, nil, `{"error":{"message":"Insufficient Balance"}}`, llm.ErrQuotaExhausted, 0},
		{"auth401", 401, nil, `{"error":{"message":"Incorrect API key provided: ` + key + `"}}`, llm.ErrAuth, 0},
		{"auth403", 403, nil, `{"error":{"message":"forbidden"}}`, llm.ErrAuth, 0},
		{"server", 503, nil, `upstream overloaded`, llm.ErrUnavailable, 0},
		{"overflow", 400, nil, `{"error":{"message":"This model's maximum context length is 128000 tokens.","code":"context_length_exceeded"}}`, llm.ErrContextOverflow, 0},
		{"bad", 400, nil, `{"error":{"message":"Invalid value for 'temperature'"}}`, llm.ErrBadRequest, 0},
		{"notfound", 404, nil, `{"error":{"message":"model not found"}}`, llm.ErrModelNotFound, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			_, err := newAdapter(t, srv.URL, nil).Stream(context.Background(), llm.Request{Model: "m"}, func(llm.Chunk) error { return nil })
			var le *llm.Error
			if !errors.As(err, &le) || le.Kind != tc.kind {
				t.Fatalf("err = %v, want kind %s", err, tc.kind)
			}
			if le.RetryAfter != tc.retry {
				t.Fatalf("retry = %v", le.RetryAfter)
			}
			if strings.Contains(err.Error(), key) {
				t.Fatalf("key leaked: %v", err)
			}
		})
	}
}

func TestConnectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, err := newAdapter(t, url, nil).Stream(context.Background(), llm.Request{Model: "m"}, func(llm.Chunk) error { return nil })
	if llm.KindOf(err) != llm.ErrUnavailable {
		t.Fatalf("err = %v", err)
	}
}

func TestInStreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"choices":[{"delta":{"content":"x"}}]}`, `{"error":{"message":"Rate limit exceeded","code":429}}`)
	}))
	defer srv.Close()
	_, err := newAdapter(t, srv.URL, nil).Stream(context.Background(), llm.Request{Model: "m"}, func(llm.Chunk) error { return nil })
	if llm.KindOf(err) != llm.ErrRateLimited {
		t.Fatalf("err = %v", err)
	}
}

func TestTruncatedStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"choices":[{"delta":{"content":"x"}}]}`)
	}))
	defer srv.Close()
	_, err := newAdapter(t, srv.URL, nil).Stream(context.Background(), llm.Request{Model: "m"}, func(llm.Chunk) error { return nil })
	if llm.KindOf(err) != llm.ErrUnavailable {
		t.Fatalf("err = %v", err)
	}
}

func TestOnChunkErrorPassesThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"choices":[{"delta":{"content":"x"}}]}`, `[DONE]`)
	}))
	defer srv.Close()
	stop := errors.New("stop")
	_, err := newAdapter(t, srv.URL, nil).Stream(context.Background(), llm.Request{Model: "m"}, func(llm.Chunk) error { return stop })
	if err != stop {
		t.Fatalf("err = %v", err)
	}
}

func TestModels(t *testing.T) {
	var hits int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list","data":[{"id":"gpt-4o","object":"model"},{"id":"custom","context_length":32000}]}`)
	}))
	defer srv.Close()
	a := newAdapter(t, srv.URL, nil)
	ms, err := a.Models(context.Background())
	if err != nil || len(ms) != 2 {
		t.Fatalf("ms=%v err=%v", ms, err)
	}
	if ms[0].ID != "custom" || ms[0].ContextWindow != 32000 || ms[1].ContextWindow != 128000 || ms[0].Provider != "test" {
		t.Fatalf("ms = %+v", ms)
	}
	if _, err := a.Models(context.Background()); err != nil || hits != 1 {
		t.Fatalf("expected cached list, hits=%d", hits)
	}
	static := newAdapter(t, srv.URL, func(pc *ProviderConfig) {
		pc.Models = []ModelSpec{{ID: "a", ContextWindow: 1000}, {ID: "deepseek-chat"}}
	})
	ms, _ = static.Models(context.Background())
	if len(ms) != 2 || ms[0].ContextWindow != 1000 || ms[1].ContextWindow != 128000 || hits != 1 {
		t.Fatalf("static = %+v hits=%d", ms, hits)
	}
}

func TestModelSpecUnmarshal(t *testing.T) {
	var pc ProviderConfig
	if err := json.Unmarshal([]byte(`{"id":"x","models":["a",{"id":"b","contextWindow":5}]}`), &pc); err != nil {
		t.Fatal(err)
	}
	if pc.Models[0].ID != "a" || pc.Models[1].ContextWindow != 5 {
		t.Fatalf("%+v", pc.Models)
	}
}

// body renders the JSON body the adapter would send.
func body(t *testing.T, a *Adapter, req llm.Request) map[string]any {
	t.Helper()
	p, extra := a.params(req)
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestEffortMapping(t *testing.T) {
	a, _ := New(nil, ProviderConfig{ID: "openai"})
	if a.BaseURL() != "https://api.openai.com/v1" {
		t.Fatal(a.BaseURL())
	}
	b := body(t, a, llm.Request{Model: "o3-mini", Effort: "high", MaxTokens: 100})
	if b["reasoning_effort"] != "high" || b["max_completion_tokens"] != float64(100) {
		t.Fatalf("body = %v", b)
	}
	if b := body(t, a, llm.Request{Model: "gpt-5.2", Effort: "max"}); b["reasoning_effort"] != "xhigh" {
		t.Fatalf("max must cap at xhigh: %v", b)
	}
	if b := body(t, a, llm.Request{Model: "gpt-4o", Effort: "high"}); b["reasoning_effort"] != nil {
		t.Fatalf("non-reasoning model got effort: %v", b)
	}
	or, _ := New(nil, ProviderConfig{ID: "openrouter"})
	if b := body(t, or, llm.Request{Model: "x/y", Effort: "low"}); b["reasoning"].(map[string]any)["effort"] != "low" {
		t.Fatalf("openrouter = %v", b)
	}
	q, _ := New(nil, ProviderConfig{ID: "qwen"})
	if b := body(t, q, llm.Request{Model: "qwen3-max", Effort: "medium"}); b["enable_thinking"] != true || b["thinking_budget"] != 8192 {
		t.Fatalf("qwen = %v", b)
	}
	d, _ := New(nil, ProviderConfig{ID: "deepseek"})
	if b := body(t, d, llm.Request{Model: "deepseek-reasoner", Effort: "high", MaxTokens: 5}); b["reasoning_effort"] != nil || b["max_tokens"] != float64(5) {
		t.Fatalf("deepseek = %v", b)
	}
	x, _ := New(nil, ProviderConfig{ID: "xai"})
	if x.BaseURL() != "https://api.x.ai/v1" || body(t, x, llm.Request{Model: "grok-3-mini", Effort: "low"})["reasoning_effort"] != "low" {
		t.Fatal("xai preset")
	}
	m, _ := New(nil, ProviderConfig{ID: "mistral"})
	if m.toolID("toolu_01abcdefghijk") == "toolu_01abcdefghijk" || len(m.toolID("toolu_01abc")) != 9 || m.toolID("abcDEF123") != "abcDEF123" {
		t.Fatal("mistral tool ids not remapped")
	}
	if b := body(t, m, llm.Request{Model: "x"}); b["stream_options"] != nil {
		t.Fatalf("mistral should not send stream_options: %v", b)
	}
	if _, err := New(nil, ProviderConfig{ID: "custom"}); err == nil {
		t.Fatal("expected error for custom without baseURL")
	}
	for id := range Presets {
		if _, err := New(nil, ProviderConfig{ID: id}); err != nil {
			t.Fatal(id, err)
		}
	}
}

func TestEnvKeyNotLeaked(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "ambient-openai-key")
	var cap captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.set(r)
		sse(w, `{"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`, `[DONE]`)
	}))
	defer srv.Close()
	a, _ := New(nil, ProviderConfig{ID: "local-thing", BaseURL: srv.URL, Kind: "local"})
	if _, err := a.Stream(context.Background(), llm.Request{Model: "m"}, func(llm.Chunk) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if cap.auth != "" {
		t.Fatalf("ambient key leaked: %q", cap.auth)
	}
}

type fakeCreds struct{ m map[string]string }

func (f fakeCreds) Resolve(_ context.Context, ref string) (string, error) {
	if v, ok := f.m[ref]; ok {
		return v, nil
	}
	return "", errors.New("not found")
}

func TestPluginAndCredentials(t *testing.T) {
	var cap captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.set(r)
		sse(w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, `[DONE]`)
	}))
	defer srv.Close()
	h := kernel.NewHost(nil)
	defer h.Close()
	if _, err := h.Load(llm.Plugin, kernel.LoadOptions{}); err != nil {
		t.Fatal(err)
	}
	_, err := h.Load(Plugin, kernel.LoadOptions{Config: map[string]any{"providers": []any{
		map[string]any{"id": "ds", "baseURL": srv.URL, "apiKeyRef": "keychain:crewbase/ds"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	info, _ := h.Plugin("@crewbase/llm-openai")
	if info.State != kernel.StateActive {
		t.Fatalf("plugin = %+v", info)
	}
	reg := kernel.Use[*llm.Registry](h.Root(), llm.ServiceKey)
	a, ok := reg.Get("ds")
	if !ok {
		t.Fatal("adapter not registered")
	}
	// no credentials service yet → auth error, no key leaked
	_, err = a.Stream(context.Background(), llm.Request{Model: "m"}, func(llm.Chunk) error { return nil })
	if llm.KindOf(err) != llm.ErrAuth {
		t.Fatalf("err = %v", err)
	}
	if err := h.Root().Provide(CredentialsKey, Resolver(fakeCreds{m: map[string]string{"keychain:crewbase/ds": "secret-1"}})); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Stream(context.Background(), llm.Request{Model: "m"}, func(llm.Chunk) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if cap.auth != "Bearer secret-1" {
		t.Fatalf("auth = %q", cap.auth)
	}
}

func TestForeignThinkingSkipped(t *testing.T) {
	a, err := New(nil, ProviderConfig{ID: "o", BaseURL: "http://x"})
	if err != nil {
		t.Fatal(err)
	}
	msgs := a.mapMessages("", []llm.Message{
		llm.TextMessage(llm.RoleUser, "q"),
		{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: "thinking", Text: "secret", Meta: map[string]string{"anthropic:signature": "S"}}}},
		{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: "thinking", Text: "t2", Meta: map[string]string{"gemini:thoughtSignature": "x"}}, {Type: "text", Text: "a"}}},
	}, nil)
	b, _ := json.Marshal(msgs)
	if len(msgs) != 2 || strings.Contains(string(b), "secret") || strings.Contains(string(b), "t2") {
		t.Fatalf("messages = %s", b)
	}
}
