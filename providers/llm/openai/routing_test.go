package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hedeqiang/crewbase/core/llm"
)

// Recorded shape of a normal tool turn (Responses streaming events): a
// reasoning item with a two-part summary, a little text and a function
// call whose arguments stream in deltas.
var toolTurnEvents = []string{
	`{"type":"response.created","sequence_number":0,"response":{"id":"resp_t","status":"in_progress","output":[]}}`,
	`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"reasoning","id":"rs_t","summary":[]}}`,
	`{"type":"response.reasoning_summary_part.added","sequence_number":2,"item_id":"rs_t","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`,
	`{"type":"response.reasoning_summary_text.delta","sequence_number":3,"item_id":"rs_t","output_index":0,"summary_index":0,"delta":"Look at the file."}`,
	`{"type":"response.reasoning_summary_part.added","sequence_number":4,"item_id":"rs_t","output_index":0,"summary_index":1,"part":{"type":"summary_text","text":""}}`,
	`{"type":"response.reasoning_summary_text.delta","sequence_number":5,"item_id":"rs_t","output_index":0,"summary_index":1,"delta":"Then answer."}`,
	`{"type":"response.output_item.done","sequence_number":6,"output_index":0,"item":{"type":"reasoning","id":"rs_t","summary":[{"type":"summary_text","text":"Look at the file."},{"type":"summary_text","text":"Then answer."}],"encrypted_content":"ENC_T"}}`,
	`{"type":"response.output_text.delta","sequence_number":7,"item_id":"msg_t","output_index":1,"content_index":0,"delta":"Reading."}`,
	`{"type":"response.output_item.done","sequence_number":8,"output_index":1,"item":{"type":"message","id":"msg_t","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Reading.","annotations":[]}]}}`,
	`{"type":"response.function_call_arguments.delta","sequence_number":9,"item_id":"fc_t","output_index":2,"delta":"{\"path\":"}`,
	`{"type":"response.function_call_arguments.delta","sequence_number":10,"item_id":"fc_t","output_index":2,"delta":"\"a.txt\"}"}`,
	`{"type":"response.output_item.done","sequence_number":11,"output_index":2,"item":{"type":"function_call","id":"fc_t","call_id":"call_r1","name":"read_file","arguments":"{\"path\":\"a.txt\"}","status":"completed"}}`,
}

func completed(output string, usage string) string {
	return `{"type":"response.completed","sequence_number":99,"response":{"id":"resp_t","status":"completed","output":[` + output + `],"usage":` + usage + `}}`
}

const toolTurnOutput = `{"type":"reasoning","id":"rs_t","summary":[{"type":"summary_text","text":"Look at the file."},{"type":"summary_text","text":"Then answer."}],"encrypted_content":"ENC_T"},` +
	`{"type":"message","id":"msg_t","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Reading.","annotations":[]}]},` +
	`{"type":"function_call","id":"fc_t","call_id":"call_r1","name":"read_file","arguments":"{\"path\":\"a.txt\"}","status":"completed"}`

const toolTurnUsage = `{"input_tokens":1200,"input_tokens_details":{"cached_tokens":1024},"output_tokens":80,"output_tokens_details":{"reasoning_tokens":40},"total_tokens":1280}`

// gateway answers /responses and /chat/completions with handlers set per test.
type gateway struct {
	mu        sync.Mutex
	paths     []string
	bodies    []map[string]any
	responses func(w http.ResponseWriter, body map[string]any)
	chat      func(w http.ResponseWriter, body map[string]any)
}

func (g *gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	g.mu.Lock()
	g.paths = append(g.paths, r.URL.Path)
	g.bodies = append(g.bodies, body)
	g.mu.Unlock()
	switch r.URL.Path {
	case "/responses":
		if g.responses == nil {
			http.NotFound(w, r)
			return
		}
		g.responses(w, body)
	case "/chat/completions":
		if g.chat == nil {
			sse(w, `{"id":"c","choices":[{"index":0,"delta":{"content":"chat"},"finish_reason":"stop"}]}`, "[DONE]")
			return
		}
		g.chat(w, body)
	default:
		http.NotFound(w, r)
	}
}

func (g *gateway) last() (string, map[string]any) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.paths[len(g.paths)-1], g.bodies[len(g.bodies)-1]
}

func (g *gateway) pathList() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return strings.Join(g.paths, " ")
}

// magpieLike is an adapter set up as magpie's inner one: auto effort,
// Responses auto, a catalog with capabilities.
func magpieLike(t *testing.T, url string) *Adapter {
	return newAdapter(t, url, func(pc *ProviderConfig) {
		pc.ID, pc.EffortStyle, pc.Responses = "magpie", "auto", ResponsesAuto
		pc.Models = []ModelSpec{
			{ID: "openai/gpt-6.1-sol", Capabilities: []string{"tools", "reasoning", "vision"}},
			{ID: "codex/gpt-6.1-sol", Capabilities: []string{"tools", "reasoning"}},
			{ID: "openai/gpt-4.1", Capabilities: []string{"tools", "vision"}},
			{ID: "deepseek/deepseek-v4-pro", Capabilities: []string{"tools", "reasoning"}},
			{ID: "claude/claude-opus-5-5", Capabilities: []string{"tools", "reasoning", "vision"}},
		}
	})
}

func TestResponsesConfig(t *testing.T) {
	for in, want := range map[string]ResponsesMode{`true`: ResponsesAuto, `false`: ResponsesNever, `"always"`: ResponsesAlways, `"Never"`: ResponsesNever} {
		var pc ProviderConfig
		if err := json.Unmarshal([]byte(`{"id":"x","responses":`+in+`}`), &pc); err != nil || pc.Responses != want {
			t.Errorf("%s: %q %v", in, pc.Responses, err)
		}
	}
	if _, err := New(nil, ProviderConfig{ID: "x", BaseURL: "http://x", Responses: "sometimes"}); err == nil {
		t.Error("bad mode accepted")
	}
	off := newAdapter(t, "http://x", func(pc *ProviderConfig) { pc.Preset = "openai"; pc.Responses = ResponsesNever })
	if off.useResponses(llm.Request{Model: "gpt-6.1-sol"}) || off.computerTool(llm.Request{Model: "gpt-6.1-sol", NativeTools: []string{llm.NativeComputer}}) {
		t.Error("never still routes to Responses")
	}
}

func TestResponsesRouting(t *testing.T) {
	official := newAdapter(t, "http://x", func(pc *ProviderConfig) { pc.Preset = "openai" })
	mag := magpieLike(t, "http://x")
	deepseek := newAdapter(t, "http://x", func(pc *ProviderConfig) { pc.Preset = "deepseek" })
	router := newAdapter(t, "http://x", func(pc *ProviderConfig) { pc.Preset = "openrouter" })
	always := newAdapter(t, "http://x", func(pc *ProviderConfig) { pc.Responses = ResponsesAlways })
	cases := []struct {
		name  string
		a     *Adapter
		model string
		want  bool
	}{
		{"official gpt-6.1", official, "gpt-6.1-sol", true},
		{"official gpt-5", official, "gpt-5", true},
		{"official o3", official, "o3-mini", true},
		{"official gpt-4.1", official, "gpt-4.1", false},
		{"official gpt-4o", official, "gpt-4o", false},
		{"magpie openai reasoning", mag, "openai/gpt-6.1-sol", true},
		{"magpie codex", mag, "codex/gpt-6.1-sol", true},
		{"magpie openai not reasoning (catalog)", mag, "openai/gpt-4.1", false},
		{"magpie alchemypay-openai (not in catalog, by id)", mag, "alchemypay-openai/gpt-6.1-sol", true},
		{"magpie deepseek", mag, "deepseek/deepseek-v4-pro", false},
		{"magpie claude", mag, "claude/claude-opus-5-5", false},
		{"magpie routing group", mag, "opus-anywhere", false},
		{"magpie bare gpt-6", mag, "gpt-6-luna", true},
		{"deepseek preset", deepseek, "deepseek-reasoner", false},
		{"openrouter openai model", router, "openai/gpt-6.1-sol", false},
		{"always", always, "llama3", true},
	}
	for _, c := range cases {
		if got := c.a.useResponses(llm.Request{Model: c.model}); got != c.want {
			t.Errorf("%s (%s): got %v want %v", c.name, c.model, got, c.want)
		}
	}
	for m, want := range map[string]bool{"openai/x": true, "codex/x": true, "alchemypay-openai/x": true, "azure-openai/gpt-4o": true,
		"gpt-5.4": true, "o4-mini": true, "ollama": false, "gpt-4o": false, "claude/claude-opus-5-5": false, "openrouter/openai/gpt-5": true} {
		if OpenAIFamily(m) != want {
			t.Errorf("OpenAIFamily(%s) != %v", m, want)
		}
	}
}

// A normal tool turn through magpie: the request body, the streamed
// chunks and the parsed reply.
func TestResponsesToolTurn(t *testing.T) {
	g := &gateway{responses: func(w http.ResponseWriter, _ map[string]any) {
		sse(w, append(append([]string{}, toolTurnEvents...), completed(toolTurnOutput, toolTurnUsage))...)
	}}
	srv := httptest.NewServer(g)
	defer srv.Close()
	a := magpieLike(t, srv.URL)
	png := []byte{0x89, 'P', 'N', 'G'}
	req := llm.Request{Model: "openai/gpt-6.1-sol", System: "You are Iris.", Effort: "max", MaxTokens: 4096,
		Tools: []llm.ToolSchema{{Name: "read_file", Description: "Read a file", Parameters: map[string]any{"type": "object",
			"properties": map[string]any{"path": map[string]any{"type": "string"}}}}},
		Messages: []llm.Message{{Role: llm.RoleUser, Parts: []llm.Part{{Type: "text", Text: "What is in a.txt? See also:"},
			{Type: llm.PartAttachment, Attachment: &llm.Attachment{Path: "uploads/s.png", Name: "s.png", Mime: "image/png", Size: 4, Data: png}}}}}}
	var chunks []llm.Chunk
	resp, err := a.Stream(context.Background(), req, func(c llm.Chunk) error { chunks = append(chunks, c); return nil })
	if err != nil {
		t.Fatal(err)
	}
	path, b := g.last()
	if path != "/responses" {
		t.Fatalf("path %s", path)
	}
	if b["model"] != "openai/gpt-6.1-sol" || b["store"] != false || b["instructions"] != "You are Iris." || b["max_output_tokens"] != 4096.0 || b["stream"] != true {
		t.Fatalf("body %v", b)
	}
	if r := b["reasoning"].(map[string]any); r["effort"] != "xhigh" || r["summary"] != "detailed" {
		t.Fatalf("reasoning %v", r)
	}
	if inc := b["include"].([]any); len(inc) != 1 || inc[0] != "reasoning.encrypted_content" {
		t.Fatalf("include %v", inc)
	}
	tl := b["tools"].([]any)
	if len(tl) != 1 {
		t.Fatalf("tools %v", tl)
	}
	if f := tl[0].(map[string]any); f["type"] != "function" || f["name"] != "read_file" || f["strict"] != false || f["parameters"].(map[string]any)["type"] != "object" {
		t.Fatalf("function %v", f)
	}
	in := b["input"].([]any)
	content := in[0].(map[string]any)["content"].([]any)
	if in[0].(map[string]any)["role"] != "user" || content[0].(map[string]any)["text"] != "What is in a.txt? See also:" ||
		content[2].(map[string]any)["type"] != "input_image" || content[2].(map[string]any)["image_url"] != "data:image/png;base64,iVBORw==" {
		t.Fatalf("input %v", in)
	}
	for _, k := range []string{"messages", "reasoning_effort", "max_tokens", "max_completion_tokens", "stream_options"} {
		if _, ok := b[k]; ok {
			t.Fatalf("chat completions field %s in %v", k, b)
		}
	}

	// chunks: thinking (two summary parts), text, the call as soon as its item is done
	var kinds []string
	think := ""
	for _, c := range chunks {
		kinds = append(kinds, c.Type)
		if c.Type == "thinking" {
			think += c.Text
		}
	}
	if think != "Look at the file.\n\nThen answer." {
		t.Fatalf("thinking %q", think)
	}
	if k := strings.Join(kinds, " "); k != "thinking thinking thinking text tool_call" {
		t.Fatalf("chunk kinds %s", k)
	}
	calls := resp.Message.ToolCalls()
	if len(calls) != 1 || calls[0].ID != "call_r1" || calls[0].Name != "read_file" || string(calls[0].Args) != `{"path":"a.txt"}` {
		t.Fatalf("calls %+v", calls)
	}
	if resp.StopReason != "tool_use" || resp.Usage.InputTokens != 1200 || resp.Usage.CacheRead != 1024 || resp.Usage.OutputTokens != 80 {
		t.Fatalf("resp %+v", resp)
	}
	th := resp.Message.Parts[0]
	if th.Type != "thinking" || th.Text != "Look at the file.\n\nThen answer." || th.Meta[MetaReasoningFrom] != "magpie/openai/gpt-6.1-sol" || !strings.Contains(th.Meta[MetaReasoning], "ENC_T") {
		t.Fatalf("thinking part %+v", th)
	}
	if resp.Message.Text() != "Reading." {
		t.Fatalf("text %q", resp.Message.Text())
	}
}

// Some gateways send response.completed without the output; the items
// from output_item.done are used then.
func TestResponsesCompletedWithoutOutput(t *testing.T) {
	g := &gateway{responses: func(w http.ResponseWriter, _ map[string]any) {
		sse(w, append(append([]string{}, toolTurnEvents...), completed("", toolTurnUsage))...)
	}}
	srv := httptest.NewServer(g)
	defer srv.Close()
	resp, err := magpieLike(t, srv.URL).Stream(context.Background(), llm.Request{Model: "codex/gpt-6.1-sol",
		Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "x")}}, func(llm.Chunk) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Message.ToolCalls()) != 1 || resp.Message.Text() != "Reading." || resp.StopReason != "tool_use" {
		t.Fatalf("resp %+v", resp)
	}
}

// The next request replays the turn: reasoning with its encrypted content
// (only this provider and model's), the text, the call, its output.
func TestResponsesReplay(t *testing.T) {
	g := &gateway{responses: func(w http.ResponseWriter, _ map[string]any) {
		sse(w, completed(`{"type":"message","id":"m2","role":"assistant","content":[{"type":"output_text","text":"It says hi."}]}`, `{"input_tokens":10,"output_tokens":3}`))
	}}
	srv := httptest.NewServer(g)
	defer srv.Close()
	a := magpieLike(t, srv.URL)
	reasoning := `{"type":"reasoning","id":"rs_t","summary":[{"type":"summary_text","text":"Look."}],"encrypted_content":"ENC_T"}`
	msgs := []llm.Message{
		llm.TextMessage(llm.RoleUser, "What is in a.txt?"),
		{Role: llm.RoleAssistant, Parts: []llm.Part{
			{Type: "thinking", Text: "Look.", Meta: map[string]string{MetaReasoning: reasoning, MetaReasoningFrom: "magpie/openai/gpt-6.1-sol"}},
			{Type: "thinking", Text: "other account", Meta: map[string]string{MetaReasoning: `{"type":"reasoning","id":"rs_x","encrypted_content":"OTHER"}`, MetaReasoningFrom: "magpie/codex/gpt-6.1-sol"}},
			{Type: "thinking", Text: "claude thinking", Meta: map[string]string{"anthropic:signature": "sig"}},
			{Type: "text", Text: "Reading."},
			{Type: "tool_call", ToolCall: &llm.ToolCall{ID: "call_r1", Name: "read_file", Args: json.RawMessage(`{"path":"a.txt"}`)}},
			{Type: "tool_call", ToolCall: &llm.ToolCall{ID: "call_r2", Name: "read_file", Args: json.RawMessage(`{"path":"b.txt"}`)}},
		}},
		{Role: llm.RoleTool, Parts: []llm.Part{
			{Type: "tool_result", ToolResult: &llm.ToolResult{CallID: "call_r1", Name: "read_file", Content: "hi"}},
			{Type: "tool_result", ToolResult: &llm.ToolResult{CallID: "call_r2", Name: "read_file", Content: "no such file", IsError: true}},
		}},
		llm.TextMessage(llm.RoleUser, "And?"),
	}
	resp, err := a.Stream(context.Background(), llm.Request{Model: "openai/gpt-6.1-sol", Messages: msgs}, func(llm.Chunk) error { return nil })
	if err != nil || resp.StopReason != "end" || resp.Message.Text() != "It says hi." {
		t.Fatalf("%+v %v", resp, err)
	}
	_, b := g.last()
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
	if want := "msg:user reasoning msg:assistant function_call function_call function_call_output function_call_output msg:user"; strings.Join(types, " ") != want {
		t.Fatalf("input %v", types)
	}
	if r := in[1].(map[string]any); r["encrypted_content"] != "ENC_T" || r["id"] != "rs_t" {
		t.Fatalf("reasoning %v", r)
	}
	if fc := in[3].(map[string]any); fc["call_id"] != "call_r1" || fc["name"] != "read_file" || fc["arguments"] != `{"path":"a.txt"}` {
		t.Fatalf("call %v", fc)
	}
	if o := in[5].(map[string]any); o["call_id"] != "call_r1" || o["output"] != "hi" {
		t.Fatalf("output %v", o)
	}
	if o := in[6].(map[string]any); o["output"] != "[error] no such file" {
		t.Fatalf("error output %v", o)
	}
	if strings.Contains(mustJSON(in), "OTHER") {
		t.Fatal("reasoning of another model replayed")
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestResponsesStopsAndErrors(t *testing.T) {
	var events []string
	g := &gateway{responses: func(w http.ResponseWriter, _ map[string]any) { sse(w, events...) }}
	srv := httptest.NewServer(g)
	defer srv.Close()
	a := magpieLike(t, srv.URL)
	run := func() (llm.Response, error) {
		return a.Stream(context.Background(), llm.Request{Model: "codex/gpt-6.1-sol", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "x")}},
			func(llm.Chunk) error { return nil })
	}
	events = []string{`{"type":"response.incomplete","response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","content":[{"type":"output_text","text":"Long"}]}]}}`}
	if resp, err := run(); err != nil || resp.StopReason != "max_tokens" {
		t.Fatalf("max tokens: %+v %v", resp, err)
	}
	events = []string{`{"type":"response.incomplete","response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[]}}`}
	if resp, err := run(); err != nil || resp.StopReason != llm.StopRefusal {
		t.Fatalf("content filter: %+v %v", resp, err)
	}
	// the Codex-account refusal, in-stream, both event shapes
	codexMsg := "The 'gpt-6.1-sol' model is not supported when using Codex with a ChatGPT account."
	for _, ev := range []string{
		`{"type":"response.failed","response":{"id":"r","status":"failed","error":{"code":"model_not_found","message":"` + codexMsg + `"}}}`,
		`{"type":"error","code":"model_not_found","message":"` + codexMsg + `"}`,
		`{"type":"error","error":{"type":"invalid_request_error","code":"model_not_found","message":"jsonrpc error -32603: ` + codexMsg + `"}}`,
	} {
		events = []string{ev}
		if _, err := run(); llm.KindOf(err) != llm.ErrModelUnavailable {
			t.Errorf("%s: %v", ev, err)
		}
	}
	events = []string{`{"type":"response.failed","response":{"status":"failed","error":{"code":"rate_limit_exceeded","message":"Rate limit reached"}}}`}
	if _, err := run(); llm.KindOf(err) != llm.ErrRateLimited {
		t.Errorf("rate limit: %v", err)
	}
	// and as an HTTP error
	g.responses = func(w http.ResponseWriter, _ map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		w.Write([]byte(`{"error":{"message":"` + codexMsg + `","type":"invalid_request_error","code":"model_not_found"}}`))
	}
	if _, err := run(); llm.KindOf(err) != llm.ErrModelUnavailable {
		t.Errorf("http: %v", err)
	}
	g.responses = func(w http.ResponseWriter, _ map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		w.Write([]byte(`{"error":{"message":"Incorrect API key provided","type":"invalid_request_error","code":"invalid_api_key"}}`))
	}
	if _, err := run(); llm.KindOf(err) != llm.ErrAuth {
		t.Errorf("auth: %v", err)
	}
	if p := g.pathList(); strings.Contains(p, "chat") {
		t.Errorf("errors fell back to chat: %s", p)
	}
}

// A gateway without /responses: one fallback to Chat Completions, then
// Chat Completions only. A 404 about the model is not a missing endpoint.
func TestResponsesMissingEndpointFallback(t *testing.T) {
	g := &gateway{} // /responses → plain 404
	srv := httptest.NewServer(g)
	defer srv.Close()
	a := magpieLike(t, srv.URL)
	req := llm.Request{Model: "openai/gpt-6.1-sol", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "x")}}
	resp, err := a.Stream(context.Background(), req, func(llm.Chunk) error { return nil })
	if err != nil || resp.Message.Text() != "chat" {
		t.Fatalf("%+v %v", resp, err)
	}
	if _, err := a.Stream(context.Background(), req, func(llm.Chunk) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if p := g.pathList(); p != "/responses /chat/completions /chat/completions" {
		t.Fatalf("paths %s", p)
	}
	// magpie's rebuilt inner adapter keeps what was learned
	b := magpieLike(t, srv.URL)
	b.InheritRoutes(a)
	if b.useResponses(req) {
		t.Fatal("rebuilt adapter forgot the missing endpoint")
	}

	// 405 counts too
	g2 := &gateway{responses: func(w http.ResponseWriter, _ map[string]any) { w.WriteHeader(405) }}
	srv2 := httptest.NewServer(g2)
	defer srv2.Close()
	if _, err := magpieLike(t, srv2.URL).Stream(context.Background(), req, func(llm.Chunk) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if p := g2.pathList(); p != "/responses /chat/completions" {
		t.Fatalf("405 paths %s", p)
	}

	// magpie's own unknown-model 404 (probed 2026-11-17) is a model error
	g3 := &gateway{responses: func(w http.ResponseWriter, _ map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(404)
		w.Write([]byte(`{"error":{"code":null,"message":"magpie knows no model \"openai/gpt-6.1-sol\" Name a model as provider/model","param":null,"type":"not_found_error"}}`))
	}}
	srv3 := httptest.NewServer(g3)
	defer srv3.Close()
	c := magpieLike(t, srv3.URL)
	if _, err := c.Stream(context.Background(), req, func(llm.Chunk) error { return nil }); llm.KindOf(err) != llm.ErrModelNotFound {
		t.Fatalf("model 404: %v", err)
	}
	if p := g3.pathList(); p != "/responses" || !c.useResponses(req) {
		t.Fatalf("model 404 treated as a missing endpoint: %s", p)
	}
}

const chat400 = `{"error":{"message":"Function tools with reasoning_effort are not supported for gpt-6.1-sol in /v1/chat/completions. To use function tools, use /v1/responses or set reasoning_effort to 'none'.","type":"invalid_request_error","param":null,"code":null}}`

func refuseTools(w http.ResponseWriter, _ map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(400)
	w.Write([]byte(chat400))
}

// Chat Completions refusing tools with an effort: retried through
// Responses, and the model stays there.
func TestChatSelfHealsToResponses(t *testing.T) {
	g := &gateway{chat: refuseTools, responses: func(w http.ResponseWriter, _ map[string]any) {
		sse(w, append(append([]string{}, toolTurnEvents...), completed(toolTurnOutput, toolTurnUsage))...)
	}}
	srv := httptest.NewServer(g)
	defer srv.Close()
	// a gateway with an OpenAI model under a name the routing does not know
	a := newAdapter(t, srv.URL, func(pc *ProviderConfig) { pc.EffortStyle = "openai" })
	req := llm.Request{Model: "sol-latest", Effort: "medium", Tools: []llm.ToolSchema{{Name: "read_file"}},
		Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "x")}}
	var chunks int
	resp, err := a.Stream(context.Background(), req, func(llm.Chunk) error { chunks++; return nil })
	if err != nil || len(resp.Message.ToolCalls()) != 1 {
		t.Fatalf("%+v %v", resp, err)
	}
	if _, err := a.Stream(context.Background(), req, func(llm.Chunk) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if p := g.pathList(); p != "/chat/completions /responses /responses" {
		t.Fatalf("paths %s", p)
	}
	_, b := g.last()
	if b["reasoning"].(map[string]any)["effort"] != "medium" {
		t.Fatalf("effort lost: %v", b["reasoning"])
	}

	// no Responses endpoint either: the original error stands, once
	g2 := &gateway{chat: refuseTools}
	srv2 := httptest.NewServer(g2)
	defer srv2.Close()
	a2 := newAdapter(t, srv2.URL, func(pc *ProviderConfig) { pc.EffortStyle = "openai" })
	_, err = a2.Stream(context.Background(), req, func(llm.Chunk) error { return nil })
	if llm.KindOf(err) != llm.ErrBadRequest || !strings.Contains(err.Error(), "/v1/responses") {
		t.Fatalf("err %v", err)
	}
	a2.Stream(context.Background(), req, func(llm.Chunk) error { return nil })
	if p := g2.pathList(); p != "/chat/completions /responses /chat/completions" {
		t.Fatalf("paths %s", p)
	}

	// "responses": "never" is respected
	g3 := &gateway{chat: refuseTools}
	srv3 := httptest.NewServer(g3)
	defer srv3.Close()
	a3 := newAdapter(t, srv3.URL, func(pc *ProviderConfig) { pc.EffortStyle = "openai"; pc.Responses = ResponsesNever })
	if _, err := a3.Stream(context.Background(), req, func(llm.Chunk) error { return nil }); llm.KindOf(err) != llm.ErrBadRequest {
		t.Fatalf("never: %v", err)
	}
	if p := g3.pathList(); p != "/chat/completions" {
		t.Fatalf("never paths %s", p)
	}
}
