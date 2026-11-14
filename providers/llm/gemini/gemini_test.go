package gemini

import (
	"context"
	"encoding/base64"
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

type captured struct {
	mu   sync.Mutex
	body map[string]any
	hdr  http.Header
	path string
	raw  string
}

func (c *captured) set(r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.body = nil
	_ = json.Unmarshal(b, &c.body)
	c.hdr = r.Header.Clone()
	c.path = r.URL.Path + "?" + r.URL.RawQuery
	c.raw = string(b)
}

func sse(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range events {
		fmt.Fprintf(w, "data: %s\r\n\r\n", e)
	}
}

func newAdapter(url string) *Adapter {
	return New(nil, ProviderConfig{ID: "gemini", BaseURL: url, APIKey: "sk-testsecretkey1234567890"})
}

func TestStreamText(t *testing.T) {
	var cap captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.set(r)
		sse(w,
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"thinking...","thought":true}]}}]}`,
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"Hel"}]}}]}`,
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"lo"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":2,"thoughtsTokenCount":3}}`)
	}))
	defer srv.Close()
	var chunks []llm.Chunk
	resp, err := newAdapter(srv.URL).Stream(context.Background(), llm.Request{Model: "gemini-2.5-pro", System: "sys", Effort: "medium",
		Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, func(c llm.Chunk) error { chunks = append(chunks, c); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Text() != "Hello" || resp.StopReason != "end" || resp.Usage.InputTokens != 7 || resp.Usage.OutputTokens != 5 {
		t.Fatalf("resp = %+v", resp)
	}
	if len(chunks) != 3 || chunks[0].Type != "thinking" {
		t.Fatalf("chunks = %+v", chunks)
	}
	if !strings.Contains(cap.path, "/models/gemini-2.5-pro:streamGenerateContent") || !strings.Contains(cap.path, "alt=sse") {
		t.Fatalf("path = %s", cap.path)
	}
	if cap.hdr.Get("x-goog-api-key") != "sk-testsecretkey1234567890" || strings.Contains(cap.path, "sk-testsecretkey1234567890") {
		t.Fatalf("key header missing or key in URL: %s", cap.path)
	}
	si := cap.body["systemInstruction"].(map[string]any)["parts"].([]any)[0].(map[string]any)
	tc := cap.body["generationConfig"].(map[string]any)["thinkingConfig"].(map[string]any)
	if si["text"] != "sys" || tc["thinkingBudget"] != float64(8192) || tc["includeThoughts"] != true {
		t.Fatalf("body = %s", cap.raw)
	}
}

func TestStreamFunctionCall(t *testing.T) {
	var cap captured
	sig := base64.StdEncoding.EncodeToString([]byte("SIG"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.set(r)
		sse(w,
			`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"a.go"}},"thoughtSignature":"`+sig+`"}]},"finishReason":"STOP"}]}`)
	}))
	defer srv.Close()
	a := newAdapter(srv.URL)
	req := llm.Request{Model: "gemini-3-pro",
		Tools: []llm.ToolSchema{{Name: "read", Description: "read", Parameters: map[string]any{"type": "object", "additionalProperties": false}}},
		Messages: []llm.Message{
			llm.TextMessage(llm.RoleUser, "go"),
			{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: "tool_call", ToolCall: &llm.ToolCall{ID: "toolu_x", Name: "ls", Args: json.RawMessage(`{"d":"."}`)}}}},
			{Role: llm.RoleTool, Parts: []llm.Part{{Type: "tool_result", ToolResult: &llm.ToolResult{CallID: "toolu_x", Content: "a.go"}}}},
		}}
	var got []llm.ToolCall
	resp, err := a.Stream(context.Background(), req, func(c llm.Chunk) error {
		if c.Type == "tool_call" {
			got = append(got, *c.ToolCall)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "tool_use" || len(got) != 1 || got[0].Name != "read" || string(got[0].Args) != `{"path":"a.go"}` {
		t.Fatalf("resp=%+v got=%+v", resp, got)
	}
	if string(a.sigFor(got[0].ID)) != "SIG" {
		t.Fatal("signature not remembered")
	}
	contents := cap.body["contents"].([]any)
	model := contents[1].(map[string]any)
	mp := model["parts"].([]any)[0].(map[string]any)
	if model["role"] != "model" || mp["functionCall"].(map[string]any)["name"] != "ls" || mp["thoughtSignature"] == nil {
		t.Fatalf("model turn = %v", model)
	}
	user := contents[2].(map[string]any)
	fr := user["parts"].([]any)[0].(map[string]any)["functionResponse"].(map[string]any)
	if user["role"] != "user" || fr["name"] != "ls" || fr["response"].(map[string]any)["output"] != "a.go" {
		t.Fatalf("function response = %v", user)
	}
	decl := cap.body["tools"].([]any)[0].(map[string]any)["functionDeclarations"].([]any)[0].(map[string]any)
	if decl["name"] != "read" || decl["parametersJsonSchema"] == nil {
		t.Fatalf("tools = %v", cap.body["tools"])
	}

	// the remembered signature is sent back with our generated id
	req.Messages = append(req.Messages, resp.Message)
	if _, err := a.Stream(context.Background(), req, func(llm.Chunk) error { return nil }); err != nil {
		t.Fatal(err)
	}
	contents = cap.body["contents"].([]any)
	last := contents[len(contents)-1].(map[string]any)["parts"].([]any)[0].(map[string]any)
	if last["thoughtSignature"] != sig || last["functionCall"].(map[string]any)["id"] != nil {
		t.Fatalf("resent call = %v", last)
	}
}

func TestErrorClasses(t *testing.T) {
	const key = "sk-testsecretkey1234567890"
	cases := []struct {
		name   string
		status int
		body   string
		kind   llm.ErrorKind
		retry  time.Duration
	}{
		{"rate", 429, `{"error":{"code":429,"message":"You exceeded your current quota, please check your plan and billing details.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"GenerateRequestsPerMinutePerProjectPerModel-FreeTier"}]},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"31s"}]}}`, llm.ErrRateLimited, 31 * time.Second},
		{"daily", 429, `{"error":{"code":429,"message":"You exceeded your current quota","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier"}]}]}}`, llm.ErrQuotaExhausted, 0},
		{"badkey", 400, `{"error":{"code":400,"message":"API key not valid. Please pass a valid API key.","status":"INVALID_ARGUMENT","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"API_KEY_INVALID"}]}}`, llm.ErrAuth, 0},
		{"perm", 403, `{"error":{"code":403,"message":"Permission denied","status":"PERMISSION_DENIED"}}`, llm.ErrAuth, 0},
		{"overflow", 400, `{"error":{"code":400,"message":"The input token count (1200000) exceeds the maximum number of tokens allowed (1048576).","status":"INVALID_ARGUMENT"}}`, llm.ErrContextOverflow, 0},
		{"bad", 400, `{"error":{"code":400,"message":"Invalid JSON payload","status":"INVALID_ARGUMENT"}}`, llm.ErrBadRequest, 0},
		{"server", 503, `{"error":{"code":503,"message":"The model is overloaded.","status":"UNAVAILABLE"}}`, llm.ErrUnavailable, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			_, err := newAdapter(srv.URL).Stream(context.Background(), llm.Request{Model: "m"}, func(llm.Chunk) error { return nil })
			var le *llm.Error
			if !errors.As(err, &le) || le.Kind != tc.kind || le.RetryAfter != tc.retry {
				t.Fatalf("err = %#v want %s", err, tc.kind)
			}
			if strings.Contains(err.Error(), key) {
				t.Fatal("key leaked")
			}
		})
	}
}

func TestInStreamErrorRefusedAndNoKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"candidates":[{"content":{"parts":[{"text":"x"}]}}]}`, `{"error":{"code":503,"message":"overloaded","status":"UNAVAILABLE"}}`)
	}))
	_, err := newAdapter(srv.URL).Stream(context.Background(), llm.Request{Model: "m"}, func(llm.Chunk) error { return nil })
	if llm.KindOf(err) != llm.ErrUnavailable {
		t.Fatalf("in-stream err = %v", err)
	}
	srv.Close()
	_, err = newAdapter(srv.URL).Stream(context.Background(), llm.Request{Model: "m"}, func(llm.Chunk) error { return nil })
	if llm.KindOf(err) != llm.ErrUnavailable {
		t.Fatalf("refused err = %v", err)
	}
	t.Setenv("GEMINI_API_KEY", "")
	_, err = New(nil, ProviderConfig{BaseURL: srv.URL}).Stream(context.Background(), llm.Request{Model: "m"}, func(llm.Chunk) error { return nil })
	if llm.KindOf(err) != llm.ErrAuth {
		t.Fatalf("no key err = %v", err)
	}
}

func TestBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"candidates":[{"finishReason":"SAFETY"}]}`)
	}))
	defer srv.Close()
	_, err := newAdapter(srv.URL).Stream(context.Background(), llm.Request{Model: "m"}, func(llm.Chunk) error { return nil })
	if llm.KindOf(err) != llm.ErrBadRequest {
		t.Fatalf("err = %v", err)
	}
}

func TestModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("pageToken") == "" {
			io.WriteString(w, `{"models":[{"name":"models/gemini-2.5-pro","displayName":"Gemini 2.5 Pro","inputTokenLimit":1048576,"supportedGenerationMethods":["generateContent","countTokens"],"thinking":true}],"nextPageToken":"p2"}`)
			return
		}
		io.WriteString(w, `{"models":[{"name":"models/aqa","supportedGenerationMethods":["generateAnswer"]},{"name":"models/text-embedding-004","inputTokenLimit":2048,"supportedGenerationMethods":["embedContent"]}]}`)
	}))
	defer srv.Close()
	ms, err := newAdapter(srv.URL).Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0].ID != "gemini-2.5-pro" || ms[0].ContextWindow != 1048576 || ms[0].Name != "Gemini 2.5 Pro" {
		t.Fatalf("ms = %+v", ms)
	}
	if ms[1].ID != "text-embedding-004" || ms[1].Capabilities[0] != "embedding" {
		t.Fatalf("embedding = %+v", ms[1])
	}
}

func TestPlugin(t *testing.T) {
	h := kernel.NewHost(nil)
	defer h.Close()
	h.Load(llm.Plugin, kernel.LoadOptions{})
	if _, err := h.Load(Plugin, kernel.LoadOptions{Config: map[string]any{"providers": []any{
		map[string]any{"id": "gemini"}, map[string]any{"id": "gemini-work", "apiKeyRef": "keychain:crewbase/gemini-work"},
	}}}); err != nil {
		t.Fatal(err)
	}
	reg := kernel.Use[*llm.Registry](h.Root(), llm.ServiceKey)
	if _, ok := reg.Get("gemini-work"); !ok {
		t.Fatalf("plugins = %+v", h.Plugins())
	}
}
