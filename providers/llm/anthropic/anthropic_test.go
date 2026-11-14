package anthropic

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

func sse(w http.ResponseWriter, events ...[2]string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range events {
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e[0], e[1])
	}
}

// apiKeyOf returns the credential the SDK sent (x-api-key or bearer).
func apiKeyOf(h http.Header) string {
	if k := h.Get("x-api-key"); k != "" {
		return k
	}
	return strings.TrimPrefix(h.Get("Authorization"), "Bearer ")
}

type captured struct {
	mu   sync.Mutex
	body map[string]any
	hdr  http.Header
	path string
}

func (c *captured) set(r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.body = nil
	_ = json.Unmarshal(b, &c.body)
	c.hdr = r.Header.Clone()
	c.path = r.URL.Path
}

func newAdapter(url string) *Adapter {
	return New(nil, ProviderConfig{ID: "anthropic", BaseURL: url, APIKey: "sk-testsecretkey1234567890"})
}

var textStream = [][2]string{
	{"message_start", `{"type":"message_start","message":{"id":"m1","usage":{"input_tokens":20,"cache_read_input_tokens":5,"output_tokens":1}}}`},
	{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`},
	{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"let me think"}}`},
	{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"SIG1"}}`},
	{"content_block_stop", `{"type":"content_block_stop","index":0}`},
	{"content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`},
	{"ping", `{"type":"ping"}`},
	{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hi "}}`},
	{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"there"}}`},
	{"content_block_stop", `{"type":"content_block_stop","index":1}`},
	{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}`},
	{"message_stop", `{"type":"message_stop"}`},
}

func TestStreamText(t *testing.T) {
	var cap captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.set(r)
		sse(w, textStream...)
	}))
	defer srv.Close()
	a := newAdapter(srv.URL)
	var chunks []llm.Chunk
	resp, err := a.Stream(context.Background(), llm.Request{Model: "claude-sonnet-5-5", System: "sys", Effort: "low",
		Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hello")}}, func(c llm.Chunk) error { chunks = append(chunks, c); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Text() != "Hi there" || resp.StopReason != "end" {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Usage.InputTokens != 25 || resp.Usage.OutputTokens != 9 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	if len(chunks) != 3 || chunks[0].Type != "thinking" {
		t.Fatalf("chunks = %+v", chunks)
	}
	if cap.path != "/v1/messages" || apiKeyOf(cap.hdr) != "sk-testsecretkey1234567890" || cap.hdr.Get("anthropic-version") == "" {
		t.Fatalf("path=%s headers=%v", cap.path, cap.hdr)
	}
	th := cap.body["thinking"].(map[string]any)
	if th["type"] != "adaptive" || th["budget_tokens"] != nil || cap.body["output_config"].(map[string]any)["effort"] != "low" {
		t.Fatalf("thinking = %v output_config=%v", th, cap.body["output_config"])
	}
	if cap.body["stream"] != true || cap.body["model"] != "claude-sonnet-5-5" {
		t.Fatalf("body = %v", cap.body)
	}
	sys := cap.body["system"].([]any)[0].(map[string]any)
	if sys["text"] != "sys" || sys["cache_control"] == nil {
		t.Fatalf("system = %v", sys)
	}
	if a.sigFor("let me think") != "SIG1" {
		t.Fatal("signature not remembered")
	}
}

func TestStreamToolUse(t *testing.T) {
	var cap captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.set(r)
		sse(w,
			[2]string{"message_start", `{"type":"message_start","message":{"usage":{"input_tokens":3}}}`},
			[2]string{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"read","input":{}}}`},
			[2]string{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`},
			[2]string{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"a.go\"}"}}`},
			[2]string{"content_block_stop", `{"type":"content_block_stop","index":0}`},
			[2]string{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":4}}`},
			[2]string{"message_stop", `{"type":"message_stop"}`},
		)
	}))
	defer srv.Close()
	a := newAdapter(srv.URL)
	req := llm.Request{Model: "", Tools: []llm.ToolSchema{{Name: "read", Parameters: map[string]any{"type": "object"}}},
		Messages: []llm.Message{
			llm.TextMessage(llm.RoleUser, "go"),
			{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: "text", Text: "ok"}, {Type: "tool_call", ToolCall: &llm.ToolCall{ID: "t0", Name: "ls", Args: json.RawMessage(`{"d":"."}`)}}}},
			{Role: llm.RoleTool, Parts: []llm.Part{{Type: "tool_result", ToolResult: &llm.ToolResult{CallID: "t0", Content: "boom", IsError: true}}}},
			llm.TextMessage(llm.RoleUser, "also this"),
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
	if resp.StopReason != "tool_use" || len(got) != 1 || got[0].ID != "toolu_1" || string(got[0].Args) != `{"path":"a.go"}` {
		t.Fatalf("resp=%+v got=%+v", resp, got)
	}
	msgs := cap.body["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages = %v", msgs)
	}
	asst := msgs[1].(map[string]any)["content"].([]any)
	if asst[1].(map[string]any)["type"] != "tool_use" || asst[1].(map[string]any)["input"].(map[string]any)["d"] != "." {
		t.Fatalf("assistant = %v", asst)
	}
	user := msgs[2].(map[string]any)
	uc := user["content"].([]any)
	tr := uc[0].(map[string]any)
	if user["role"] != "user" || tr["type"] != "tool_result" || tr["tool_use_id"] != "t0" || tr["is_error"] != true || uc[1].(map[string]any)["text"] != "also this" {
		t.Fatalf("tool result turn = %v", user)
	}
}

func TestThinkingSignaturePassBack(t *testing.T) {
	a := New(nil, ProviderConfig{})
	msgs := []llm.Message{
		llm.TextMessage(llm.RoleUser, "go"),
		{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: "thinking", Text: "unknown"}, {Type: "tool_call", ToolCall: &llm.ToolCall{ID: "t", Name: "x"}}}},
		{Role: llm.RoleTool, Parts: []llm.Part{{Type: "tool_result", ToolResult: &llm.ToolResult{CallID: "t", Content: "r"}}}},
	}
	asstJSON := func() []any {
		b, err := json.Marshal(a.params(llm.Request{Effort: "xhigh", Messages: msgs}))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		json.Unmarshal(b, &m)
		if m["model"] != DefaultModel || m["output_config"].(map[string]any)["effort"] != "xhigh" || m["thinking"].(map[string]any)["type"] != "adaptive" {
			t.Fatalf("params = %s", b)
		}
		return m["messages"].([]any)[1].(map[string]any)["content"].([]any)
	}
	if c := asstJSON(); len(c) != 1 || c[0].(map[string]any)["type"] != "tool_use" {
		t.Fatalf("unsigned thinking should be dropped: %v", c)
	}
	a.rememberSig("unknown", "S")
	c := asstJSON()
	if len(c) != 2 || c[0].(map[string]any)["type"] != "thinking" || c[0].(map[string]any)["signature"] != "S" {
		t.Fatalf("signed thinking = %v", c)
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
	}{
		{"rate", 429, map[string]string{"retry-after": "12"}, `{"type":"error","error":{"type":"rate_limit_error","message":"Number of request tokens has exceeded your per-minute rate limit"}}`, llm.ErrRateLimited},
		{"credit", 400, nil, `{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API."}}`, llm.ErrQuotaExhausted},
		{"auth", 401, nil, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`, llm.ErrAuth},
		{"perm", 403, nil, `{"type":"error","error":{"type":"permission_error","message":"no"}}`, llm.ErrAuth},
		{"overloaded", 529, nil, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, llm.ErrUnavailable},
		{"overflow", 400, nil, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"}}`, llm.ErrContextOverflow},
		{"bad", 400, nil, `{"type":"error","error":{"type":"invalid_request_error","message":"messages: field required"}}`, llm.ErrBadRequest},
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
			_, err := newAdapter(srv.URL).Stream(context.Background(), llm.Request{Model: "m"}, func(llm.Chunk) error { return nil })
			var le *llm.Error
			if !errors.As(err, &le) || le.Kind != tc.kind {
				t.Fatalf("err = %v want %s", err, tc.kind)
			}
			if tc.name == "rate" && le.RetryAfter != 12*time.Second {
				t.Fatalf("retry = %v", le.RetryAfter)
			}
			if strings.Contains(err.Error(), key) {
				t.Fatal("key leaked")
			}
		})
	}
}

func TestStreamErrorEventAndRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, [2]string{"message_start", `{"type":"message_start","message":{"usage":{}}}`},
			[2]string{"error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`})
	}))
	_, err := newAdapter(srv.URL).Stream(context.Background(), llm.Request{Model: "m"}, func(llm.Chunk) error { return nil })
	if llm.KindOf(err) != llm.ErrUnavailable {
		t.Fatalf("err = %v", err)
	}
	srv.Close()
	_, err = newAdapter(srv.URL).Stream(context.Background(), llm.Request{Model: "m"}, func(llm.Chunk) error { return nil })
	if llm.KindOf(err) != llm.ErrUnavailable {
		t.Fatalf("refused err = %v", err)
	}
}

func TestModels(t *testing.T) {
	up := true
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ok := up
		mu.Unlock()
		if !ok {
			w.WriteHeader(503)
			return
		}
		if r.URL.Path != "/v1/models" || apiKeyOf(r.Header) == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"type":"model","id":"claude-opus-5-5","display_name":"Claude Opus 5.5","max_input_tokens":1000000,"max_tokens":128000,
		  "capabilities":{"image_input":{"supported":true},"thinking":{"supported":true,"types":{"adaptive":{"supported":true}}}}},
		  {"type":"model","id":"claude-haiku-5-5","display_name":"Claude Haiku 5.5","max_input_tokens":200000,"max_tokens":64000,"capabilities":{}}],
		  "has_more":false,"first_id":"claude-opus-5-5","last_id":"claude-haiku-5-5"}`)
	}))
	defer srv.Close()
	a := newAdapter(srv.URL)
	ms, err := a.Models(context.Background())
	if err != nil || len(ms) != 2 {
		t.Fatalf("ms=%v err=%v", ms, err)
	}
	if ms[1].ID != "claude-opus-5-5" || ms[1].ContextWindow != 1000000 || ms[1].Name != "Claude Opus 5.5" || !hasCap(ms[1].Capabilities, "reasoning") {
		t.Fatalf("ms = %+v", ms)
	}
	// model metadata drives max_tokens clamping and thinking support
	p := a.params(llm.Request{Model: "claude-haiku-5-5", MaxTokens: 100000})
	if p.MaxTokens != 64000 || p.Thinking.OfAdaptive != nil {
		t.Fatalf("haiku params max=%d thinking=%v", p.MaxTokens, p.Thinking.OfAdaptive)
	}
	// unreachable API, nothing cached → built-in default list
	mu.Lock()
	up = false
	mu.Unlock()
	b := newAdapter(srv.URL)
	ms, err = b.Models(context.Background())
	if err != nil || len(ms) != len(DefaultModels) || ms[0].ID != "claude-opus-5-5" {
		t.Fatalf("fallback ms=%v err=%v", ms, err)
	}
}

type fakeCreds struct{}

func (fakeCreds) Resolve(_ context.Context, ref string) (string, error) {
	if ref == "keychain:crewbase/anthropic" {
		return "from-keychain", nil
	}
	return "", errors.New("nope")
}

func TestPluginDefaultInstanceUsesKeychainRef(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	var cap captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.set(r)
		sse(w, textStream...)
	}))
	defer srv.Close()
	h := kernel.NewHost(nil)
	defer h.Close()
	h.Load(llm.Plugin, kernel.LoadOptions{})
	h.Root().Provide(CredentialsKey, Resolver(fakeCreds{}))
	if _, err := h.Load(Plugin, kernel.LoadOptions{Config: map[string]any{"baseURL": srv.URL}}); err != nil {
		t.Fatal(err)
	}
	a, ok := kernel.Use[*llm.Registry](h.Root(), llm.ServiceKey).Get("anthropic")
	if !ok {
		t.Fatalf("not registered: %+v", h.Plugins())
	}
	if _, err := a.Stream(context.Background(), llm.Request{Model: "m"}, func(llm.Chunk) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if apiKeyOf(cap.hdr) != "from-keychain" {
		t.Fatalf("key = %q", apiKeyOf(cap.hdr))
	}
}
