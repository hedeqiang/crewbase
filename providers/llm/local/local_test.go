package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/kernel"
)

// fakeOllama is a minimal Ollama server.
type fakeOllama struct {
	up       atomic.Bool
	mu       sync.Mutex
	lastChat map[string]any
	chat     func(w http.ResponseWriter)
}

func (f *fakeOllama) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !f.up.Load() {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	switch r.URL.Path {
	case "/api/version":
		io.WriteString(w, `{"version":"0.12.0"}`)
	case "/api/tags":
		io.WriteString(w, `{"models":[{"name":"qwen3:8b","model":"qwen3:8b","size":5200000000,"digest":"d1","details":{"parameter_size":"8.2B"}},
			{"name":"llama3.2:3b","size":2019393189,"digest":"d2"}]}`)
	case "/api/show":
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		if b["model"] == "qwen3:8b" {
			io.WriteString(w, `{"capabilities":["completion","tools","thinking"],"model_info":{"general.architecture":"qwen3","qwen3.context_length":40960}}`)
		} else {
			io.WriteString(w, `{"capabilities":["completion"],"model_info":{"llama.context_length":131072}}`)
		}
	case "/api/chat":
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		f.lastChat = b
		chat := f.chat
		f.mu.Unlock()
		chat(w)
	default:
		http.NotFound(w, r)
	}
}

func ndjson(w http.ResponseWriter, lines ...string) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
}

func newFake(chat func(w http.ResponseWriter)) (*fakeOllama, *httptest.Server) {
	f := &fakeOllama{chat: chat}
	f.up.Store(true)
	return f, httptest.NewServer(f)
}

func TestOllamaStreamText(t *testing.T) {
	f, srv := newFake(func(w http.ResponseWriter) {
		ndjson(w,
			`{"model":"qwen3:8b","message":{"role":"assistant","content":"","thinking":"hmm"},"done":false}`,
			`{"model":"qwen3:8b","message":{"role":"assistant","content":"Hel"},"done":false}`,
			`{"model":"qwen3:8b","message":{"role":"assistant","content":"lo"},"done":false}`,
			`{"model":"qwen3:8b","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":12,"eval_count":3}`)
	})
	defer srv.Close()
	o := NewOllama(nil, ServerConfig{ID: "ollama", BaseURL: srv.URL, NumCtx: 8192})
	var chunks []llm.Chunk
	resp, err := o.Stream(context.Background(), llm.Request{Model: "qwen3:8b", System: "sys", Effort: "high", MaxTokens: 50,
		Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, func(c llm.Chunk) error { chunks = append(chunks, c); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Text() != "Hello" || resp.StopReason != "end" || resp.Usage.InputTokens != 12 || resp.Usage.OutputTokens != 3 {
		t.Fatalf("resp = %+v", resp)
	}
	if len(chunks) != 3 || chunks[0].Type != "thinking" {
		t.Fatalf("chunks = %+v", chunks)
	}
	b := f.lastChat
	msgs := b["messages"].([]any)
	opts := b["options"].(map[string]any)
	if msgs[0].(map[string]any)["role"] != "system" || b["think"] != true || opts["num_ctx"] != float64(8192) || opts["num_predict"] != float64(50) || b["stream"] != true {
		t.Fatalf("body = %v", b)
	}
}

func TestOllamaToolCall(t *testing.T) {
	f, srv := newFake(func(w http.ResponseWriter) {
		ndjson(w,
			`{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"read","arguments":{"path":"a.go"}}}]},"done":false}`,
			`{"message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}`)
	})
	defer srv.Close()
	o := NewOllama(nil, ServerConfig{BaseURL: srv.URL})
	req := llm.Request{Model: "qwen3:8b", Tools: []llm.ToolSchema{{Name: "read"}}, Messages: []llm.Message{
		llm.TextMessage(llm.RoleUser, "go"),
		{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: "tool_call", ToolCall: &llm.ToolCall{ID: "c1", Name: "ls", Args: json.RawMessage(`{"d":"."}`)}}}},
		{Role: llm.RoleTool, Parts: []llm.Part{{Type: "tool_result", ToolResult: &llm.ToolResult{CallID: "c1", Content: "x"}}}},
	}}
	var got []llm.ToolCall
	resp, err := o.Stream(context.Background(), req, func(c llm.Chunk) error {
		if c.Type == "tool_call" {
			got = append(got, *c.ToolCall)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "tool_use" || len(got) != 1 || got[0].Name != "read" || string(got[0].Args) != `{"path":"a.go"}` || got[0].ID == "" {
		t.Fatalf("resp=%+v got=%+v", resp, got)
	}
	msgs := f.lastChat["messages"].([]any)
	asst := msgs[1].(map[string]any)
	fn := asst["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "ls" || fn["arguments"].(map[string]any)["d"] != "." {
		t.Fatalf("assistant = %v", asst)
	}
	tool := msgs[2].(map[string]any)
	if tool["role"] != "tool" || tool["tool_name"] != "ls" || tool["content"] != "x" {
		t.Fatalf("tool = %v", tool)
	}
}

func TestOllamaErrors(t *testing.T) {
	cases := []struct {
		status int
		body   string
		kind   llm.ErrorKind
	}{
		{404, `{"error":"model 'nope' not found"}`, llm.ErrModelNotFound},
		{500, `{"error":"model requires more system memory (12 GiB) than is available"}`, llm.ErrUnavailable},
		{429, `{"error":"server busy, please try again. maximum pending requests exceeded"}`, llm.ErrRateLimited},
		{400, `{"error":"input length exceeds the context length"}`, llm.ErrContextOverflow},
		{401, `{"error":"unauthorized"}`, llm.ErrAuth},
	}
	for _, tc := range cases {
		_, srv := newFake(func(w http.ResponseWriter) {
			w.WriteHeader(tc.status)
			io.WriteString(w, tc.body)
		})
		_, err := NewOllama(nil, ServerConfig{BaseURL: srv.URL}).Stream(context.Background(), llm.Request{Model: "nope"}, func(llm.Chunk) error { return nil })
		srv.Close()
		var le *llm.Error
		if !errors.As(err, &le) || le.Kind != tc.kind {
			t.Fatalf("%d: err = %v want %s", tc.status, err, tc.kind)
		}
	}
	// in-stream error
	_, srv := newFake(func(w http.ResponseWriter) {
		ndjson(w, `{"message":{"content":"x"}}`, `{"error":"an error was encountered while running the model: unexpected EOF"}`)
	})
	_, err := NewOllama(nil, ServerConfig{BaseURL: srv.URL}).Stream(context.Background(), llm.Request{Model: "m"}, func(llm.Chunk) error { return nil })
	if llm.KindOf(err) != llm.ErrUnavailable {
		t.Fatalf("in-stream err = %v", err)
	}
	srv.Close()
	_, err = NewOllama(nil, ServerConfig{BaseURL: srv.URL}).Stream(context.Background(), llm.Request{Model: "m"}, func(llm.Chunk) error { return nil })
	if llm.KindOf(err) != llm.ErrUnavailable {
		t.Fatalf("refused err = %v", err)
	}
}

func TestOllamaModels(t *testing.T) {
	_, srv := newFake(nil)
	defer srv.Close()
	ms, err := NewOllama(nil, ServerConfig{BaseURL: srv.URL}).Models(context.Background())
	if err != nil || len(ms) != 2 {
		t.Fatalf("ms=%v err=%v", ms, err)
	}
	q := ms[1]
	if q.ID != "qwen3:8b" || q.Name != "qwen3:8b (5.2 GB)" || q.ContextWindow != 40960 || len(q.Capabilities) != 2 || q.Capabilities[1] != "reasoning" {
		t.Fatalf("qwen = %+v", q)
	}
	if ms[0].ContextWindow != 131072 || ms[0].Name != "llama3.2:3b (2.0 GB)" {
		t.Fatalf("llama = %+v", ms[0])
	}
}

// fakeOpenAI is a minimal OpenAI-compatible server (LM Studio / vLLM).
func fakeOpenAI(up *atomic.Bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"object":"list","data":[{"id":"qwen2.5-7b-instruct","object":"model","max_model_len":32768}]}`)
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi from lmstudio\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
}

func newHost(t *testing.T) (*kernel.Host, *llm.Registry) {
	t.Helper()
	h := kernel.NewHost(nil)
	if _, err := h.Load(llm.Plugin, kernel.LoadOptions{}); err != nil {
		t.Fatal(err)
	}
	return h, kernel.Use[*llm.Registry](h.Root(), llm.ServiceKey)
}

func TestDiscoveryAppearDisappear(t *testing.T) {
	ol, olSrv := newFake(nil)
	defer olSrv.Close()
	var lmUp atomic.Bool
	lmUp.Store(true)
	lmSrv := fakeOpenAI(&lmUp)
	defer lmSrv.Close()
	junk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"hello":"not a model server"}`)
	}))
	defer junk.Close()

	h, reg := newHost(t)
	defer h.Close()
	_, err := h.Load(Plugin, kernel.LoadOptions{Config: map[string]any{
		"discover":       false,
		"rescanInterval": "0",
		"dropAfter":      1,
		"servers": []any{
			map[string]any{"id": "ollama", "type": "ollama", "baseURL": olSrv.URL},
			map[string]any{"id": "lmstudio", "type": "openai", "baseURL": lmSrv.URL + "/v1"},
			map[string]any{"id": "vllm", "type": "openai", "baseURL": junk.URL + "/v1"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	d := kernel.Use[*Discovery](h.Root(), ServiceKey)
	if _, ok := reg.Get("ollama"); !ok {
		t.Fatalf("ollama not registered; servers = %+v", d.Servers())
	}
	lm, ok := reg.Get("lmstudio")
	if !ok || lm.Kind() != "local" {
		t.Fatalf("lmstudio not registered as local")
	}
	if _, ok := reg.Get("vllm"); ok {
		t.Fatal("junk server registered as vllm")
	}
	resp, err := lm.Stream(context.Background(), llm.Request{Model: "qwen2.5-7b-instruct"}, func(llm.Chunk) error { return nil })
	if err != nil || resp.Message.Text() != "hi from lmstudio" {
		t.Fatalf("lmstudio resp=%+v err=%v", resp, err)
	}
	ms, _ := lm.Models(context.Background())
	if len(ms) != 1 || ms[0].ContextWindow != 32768 {
		t.Fatalf("lmstudio models = %+v", ms)
	}

	// disappear
	ol.up.Store(false)
	lmUp.Store(false)
	d.Rescan(context.Background())
	if _, ok := reg.Get("ollama"); ok {
		t.Fatal("ollama still registered after disappearing")
	}
	if _, ok := reg.Get("lmstudio"); ok {
		t.Fatal("lmstudio still registered after disappearing")
	}
	for _, s := range d.Servers() {
		if s.Up || s.LastError == "" {
			t.Fatalf("status = %+v", s)
		}
	}
	// reappear
	ol.up.Store(true)
	d.Rescan(context.Background())
	if a, ok := reg.Get("ollama"); !ok || a.Kind() != "local" {
		t.Fatal("ollama not re-registered")
	}
	// unloading the plugin unregisters discovered adapters
	h.Unload("@crewbase/llm-local")
	if _, ok := reg.Get("ollama"); ok {
		t.Fatal("adapter survived plugin unload")
	}
}

func TestPeriodicRescanAndDropAfter(t *testing.T) {
	ol, olSrv := newFake(nil)
	ol.up.Store(false)
	defer olSrv.Close()
	h, reg := newHost(t)
	defer h.Close()
	_, err := h.Load(Plugin, kernel.LoadOptions{Config: map[string]any{
		"discover":       false,
		"rescanInterval": "10ms",
		"servers":        []any{map[string]any{"id": "remote-ollama", "type": "ollama", "baseURL": olSrv.URL}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Get("remote-ollama"); ok {
		t.Fatal("registered while down")
	}
	ol.up.Store(true)
	waitFor(t, func() bool { _, ok := reg.Get("remote-ollama"); return ok })

	d := kernel.Use[*Discovery](h.Root(), ServiceKey)
	olSrv.Close() // connection refused from now on
	waitFor(t, func() bool { _, ok := reg.Get("remote-ollama"); return !ok })
	for _, s := range d.Servers() {
		if s.ID == "remote-ollama" && s.LastError != "not running" {
			t.Fatalf("status = %+v", s)
		}
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met")
}

func TestMergeServersAndDefaults(t *testing.T) {
	t.Setenv("OLLAMA_HOST", "10.0.0.5:11434")
	got := mergeServers(DefaultServers(), []ServerConfig{{ID: "vllm", BaseURL: "http://gpu:8000/v1"}, {ID: "extra", Type: "ollama", BaseURL: "http://x"}})
	if len(got) != 4 || got[0].BaseURL != "http://10.0.0.5:11434" || got[2].BaseURL != "http://gpu:8000/v1" || got[2].Type != "openai" || got[3].ID != "extra" {
		t.Fatalf("merged = %+v", got)
	}
	if parseDuration("", time.Minute) != time.Minute || parseDuration("0", time.Minute) != 0 || parseDuration("5s", 0) != 5*time.Second {
		t.Fatal("parseDuration")
	}
	if HumanSize(4_700_000_000) != "4.7 GB" {
		t.Fatal(HumanSize(4_700_000_000))
	}
}
