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

// Recorded shape of a computer turn (Responses API, computer tool guide):
// a reasoning item, a little text and one computer_call with actions[]
// and a pending safety check.
var computerEvents = []string{
	`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress","output":[]}}`,
	`{"type":"response.reasoning_summary_text.delta","sequence_number":1,"item_id":"rs_1","output_index":0,"summary_index":0,"delta":"Need the menu."}`,
	`{"type":"response.output_text.delta","sequence_number":2,"item_id":"msg_1","output_index":1,"content_index":0,"delta":"Opening it."}`,
	`{"type":"response.completed","sequence_number":3,"response":{"id":"resp_1","status":"completed","output":[` +
		`{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Need the menu."}],"encrypted_content":"ENC"},` +
		`{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Opening it.","annotations":[]}]},` +
		`{"type":"computer_call","id":"cu_1","call_id":"call_002","status":"completed","pending_safety_checks":[{"id":"sc_1","code":"malicious_instructions","message":"Suspicious text on screen"}],` +
		`"actions":[{"type":"click","button":"left","x":405,"y":157},{"type":"type","text":"penguin"}]}` +
		`],"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":40},"output_tokens":20,"output_tokens_details":{"reasoning_tokens":5},"total_tokens":120}}}`,
}

func TestResponsesComputer(t *testing.T) {
	var cap captured
	paths := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		cap.set(r)
		if r.URL.Path == "/responses" {
			sse(w, computerEvents...)
			return
		}
		sse(w, `{"id":"c","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":"stop"}]}`, "[DONE]")
	}))
	defer srv.Close()
	a := newAdapter(t, srv.URL, func(pc *ProviderConfig) {
		pc.Preset = "openai"
		pc.Responses = ResponsesAuto
		pc.EffortStyle = "openai"
	})
	req := llm.Request{Model: "gpt-6.1-sol", System: "sys", Effort: "low", NativeTools: []string{llm.NativeComputer},
		Tools:    []llm.ToolSchema{{Name: "computer_apps", Description: "list", Parameters: map[string]any{"type": "object"}}},
		Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "search penguin")}}
	var chunks []llm.Chunk
	resp, err := a.Stream(context.Background(), req, func(c llm.Chunk) error { chunks = append(chunks, c); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if paths[0] != "/responses" {
		t.Fatalf("path %v", paths)
	}
	b := cap.body
	tl := b["tools"].([]any)
	if tl[len(tl)-1].(map[string]any)["type"] != "computer" || tl[0].(map[string]any)["name"] != "computer_apps" {
		t.Fatalf("tools %v", tl)
	}
	if b["store"] != false || b["instructions"] != "sys" || b["model"] != "gpt-6.1-sol" || b["reasoning"].(map[string]any)["effort"] != "low" {
		t.Fatalf("body %v", b)
	}

	if len(chunks) < 3 || chunks[0].Type != "thinking" || chunks[1].Text != "Opening it." {
		t.Fatalf("chunks %+v", chunks)
	}
	calls := resp.Message.ToolCalls()
	if len(calls) != 1 || resp.StopReason != "tool_use" || resp.Usage.CacheRead != 40 {
		t.Fatalf("resp %+v", resp)
	}
	c, ok := llm.DecodeComputerCall(calls[0])
	if !ok || c.Vendor != llm.ComputerOpenAI || calls[0].ID != "call_002" || c.Batch != "resp_1" || len(c.Safety) != 1 || c.Safety[0].ID != "sc_1" {
		t.Fatalf("native %+v", c)
	}
	var acts []map[string]any
	if json.Unmarshal(c.Actions, &acts) != nil || len(acts) != 2 || acts[1]["text"] != "penguin" {
		t.Fatalf("actions %s", c.Actions)
	}

	// next request: reasoning replayed, the call with its item id, the
	// output with the screenshot and the confirmed safety check
	png := []byte{0x89, 'P', 'N', 'G'}
	req.Messages = append(req.Messages, resp.Message, llm.Message{Role: llm.RoleTool, Parts: []llm.Part{{Type: "tool_result",
		ToolResult: &llm.ToolResult{CallID: "call_002", Name: llm.NativeComputer, Content: llm.SafetyAcknowledged + "\nDid click.",
			Attachments: []llm.Attachment{{Path: "generated/s.png", Mime: "image/png", Data: png}}}}}})
	if _, err := a.Stream(context.Background(), req, func(llm.Chunk) error { return nil }); err != nil {
		t.Fatal(err)
	}
	in := cap.body["input"].([]any)
	types := []string{}
	for _, x := range in {
		m := x.(map[string]any)
		ty, _ := m["type"].(string)
		if ty == "" {
			ty = "msg:" + m["role"].(string)
		}
		types = append(types, ty)
	}
	if want := "msg:user reasoning msg:assistant computer_call computer_call_output"; join(types) != want {
		t.Fatalf("input %v", types)
	}
	if r := in[1].(map[string]any); r["encrypted_content"] != "ENC" {
		t.Fatalf("reasoning %v", r)
	}
	call := in[3].(map[string]any)
	if call["id"] != "cu_1" || call["call_id"] != "call_002" || len(call["actions"].([]any)) != 2 {
		t.Fatalf("call %v", call)
	}
	out := in[4].(map[string]any)
	o := out["output"].(map[string]any)
	if o["type"] != "computer_screenshot" || o["image_url"] != "data:image/png;base64,iVBORw==" || o["detail"] != "original" {
		t.Fatalf("output %v", out)
	}
	if ack := out["acknowledged_safety_checks"].([]any); ack[0].(map[string]any)["id"] != "sc_1" {
		t.Fatalf("ack %v", out)
	}

	// not confirmed: no acknowledgement
	req.Messages[len(req.Messages)-1].Parts[0].ToolResult.Content = "Did click."
	a.Stream(context.Background(), req, func(llm.Chunk) error { return nil })
	if out := cap.body["input"].([]any)[4].(map[string]any); out["acknowledged_safety_checks"] != nil {
		t.Fatalf("acknowledged without confirmation: %v", out)
	}

	// a model that neither reasons nor has the tool: Chat Completions
	for _, r := range []llm.Request{{Model: "gpt-4.1"}, {Model: "gpt-4o", NativeTools: []string{llm.NativeComputer}}} {
		r.Messages = []llm.Message{llm.TextMessage(llm.RoleUser, "x")}
		if _, err := a.Stream(context.Background(), r, func(llm.Chunk) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if p := paths[len(paths)-1]; p != "/chat/completions" {
			t.Fatalf("%s went to %s", r.Model, p)
		}
	}
	// other providers keep Chat Completions unless configured
	if newAdapter(t, srv.URL, nil).computerTool(llm.Request{Model: "gpt-6.1-sol", NativeTools: []string{llm.NativeComputer}}) {
		t.Fatal("responses used for a non-openai provider")
	}
}

func join(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += " "
		}
		out += x
	}
	return out
}

// Some gateways send the reasoning summary only inside the finished item,
// never as deltas: it is shown as soon as that item arrives, once.
func TestResponsesWholeSummary(t *testing.T) {
	var cap captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.set(r)
		sse(w,
			`{"type":"response.created","sequence_number":0,"response":{"id":"r","status":"in_progress","output":[]}}`,
			`{"type":"response.output_item.done","sequence_number":1,"output_index":0,"item":{"type":"reasoning","id":"rs","summary":[{"type":"summary_text","text":"**Plan**"},{"type":"summary_text","text":"Count legs."}],"encrypted_content":"E"}}`,
			`{"type":"response.output_text.delta","sequence_number":2,"item_id":"m","output_index":1,"content_index":0,"delta":"23 and 12."}`,
			`{"type":"response.completed","sequence_number":3,"response":{"id":"r","status":"completed","output":[`+
				`{"type":"reasoning","id":"rs","summary":[{"type":"summary_text","text":"**Plan**"},{"type":"summary_text","text":"Count legs."}],"encrypted_content":"E"},`+
				`{"type":"message","id":"m","role":"assistant","status":"completed","content":[{"type":"output_text","text":"23 and 12.","annotations":[]}]}`+
				`],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`)
	}))
	defer srv.Close()
	a := newAdapter(t, srv.URL, func(pc *ProviderConfig) { pc.Responses = ResponsesAlways; pc.EffortStyle = "openai" })
	var thinking []string
	resp, err := a.Stream(context.Background(), llm.Request{Model: "gpt-6.2-sol", Effort: "high", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "q")}},
		func(c llm.Chunk) error {
			if c.Type == "thinking" {
				thinking = append(thinking, c.Text)
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if r := cap.body["reasoning"].(map[string]any); r["summary"] != "detailed" {
		t.Fatalf("asked for %v", r)
	}
	if len(thinking) != 1 || thinking[0] != "**Plan**\n\nCount legs." {
		t.Fatalf("live thinking %q", thinking)
	}
	if resp.Message.Parts[0].Type != "thinking" || resp.Message.Parts[0].Text != "**Plan**\n\nCount legs." {
		t.Fatalf("saved thinking %+v", resp.Message.Parts[0])
	}
}

// Gateways and compatible servers get the computer tool only for a model
// that lists computer_use; one that refuses it anyway (magpie's Codex
// accounts: "Unsupported tool type: computer") is answered once more
// without it, and not offered it again for that model.
func TestComputerToolOnlyWhereTaken(t *testing.T) {
	want := []string{llm.NativeComputer}
	ask := func(model string) llm.Request {
		return llm.Request{Model: model, NativeTools: want, Tools: []llm.ToolSchema{{Name: "computer_apps", Parameters: map[string]any{"type": "object"}}},
			Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "open notes")}}
	}
	// magpie: gpt-6 by name, no computer_use listed
	if magpieLike(t, "http://x").computerTool(ask("openai/gpt-6.1-sol")) {
		t.Fatal("magpie offered the computer tool")
	}
	// a compatible server whose model lists it
	listed := newAdapter(t, "http://x", func(pc *ProviderConfig) {
		pc.Responses = ResponsesAuto
		pc.Models = []ModelSpec{{ID: "gpt-6.1-sol", Capabilities: []string{"tools", "computer_use"}}}
	})
	if !listed.computerTool(ask("gpt-6.1-sol")) {
		t.Fatal("computer_use listed but not offered")
	}
	// OpenAI itself
	if !newAdapter(t, "http://x", func(pc *ProviderConfig) { pc.Preset = "openai" }).computerTool(ask("gpt-6.1-sol")) {
		t.Fatal("OpenAI not offered the computer tool")
	}

	// refused anyway: once more without it, then never again for that model
	g := &gateway{responses: func(w http.ResponseWriter, body map[string]any) {
		for _, x := range body["tools"].([]any) {
			if x.(map[string]any)["type"] == "computer" {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":{"message":"Codex: Unsupported tool type: computer","type":"invalid_request_error"}}`))
				return
			}
		}
		sse(w, completed(`{"type":"message","id":"m1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"done"}]}`, `{"input_tokens":5,"output_tokens":1}`))
	}}
	srv := httptest.NewServer(g)
	defer srv.Close()
	a := newAdapter(t, srv.URL, func(pc *ProviderConfig) {
		pc.Responses = ResponsesAuto
		pc.Models = []ModelSpec{{ID: "gpt-6.1-sol", Capabilities: []string{"tools", "computer_use"}}}
	})
	resp, err := a.Stream(context.Background(), ask("gpt-6.1-sol"), func(llm.Chunk) error { return nil })
	// without the computer tool the model goes its usual way (Chat
	// Completions on a compatible server)
	if err != nil || resp.Message.Text() != "chat" {
		t.Fatalf("not recovered: %+v %v", resp, err)
	}
	if p := g.pathList(); p != "/responses /chat/completions" {
		t.Fatalf("requests: %s", p)
	}
	if _, b := g.last(); strings.Contains(fmt.Sprint(b["tools"]), "computer]") || strings.Contains(fmt.Sprint(b["tools"]), "type:computer") {
		t.Fatalf("retry still had the computer tool: %v", b["tools"])
	}
	if _, err := a.Stream(context.Background(), ask("gpt-6.1-sol"), func(llm.Chunk) error { return nil }); err != nil || g.pathList() != "/responses /chat/completions /chat/completions" {
		t.Fatalf("offered again: %v, %s", err, g.pathList())
	}

	// another kind of 400 is not retried
	g.responses = func(w http.ResponseWriter, _ map[string]any) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"Invalid value for 'input'","type":"invalid_request_error"}}`))
	}
	b := newAdapter(t, srv.URL, func(pc *ProviderConfig) {
		pc.Responses = ResponsesAuto
		pc.Models = []ModelSpec{{ID: "gpt-6.1-sol", Capabilities: []string{"tools", "computer_use"}}}
	})
	before := len(g.paths)
	if _, err := b.Stream(context.Background(), ask("gpt-6.1-sol"), func(llm.Chunk) error { return nil }); err == nil || len(g.paths) != before+1 {
		t.Fatalf("other error: %v, %d requests", err, len(g.paths)-before)
	}
}

// A conversation with OpenAI computer calls, continued where the computer
// tool is not offered (another provider), replays them as function calls,
// not computer_call items the provider would refuse.
func TestComputerHistoryWithoutTheTool(t *testing.T) {
	call := llm.EncodeComputerCall("call_c1", llm.ComputerCall{Vendor: llm.ComputerOpenAI, Tool: "computer", Actions: json.RawMessage(`[{"type":"screenshot"}]`)})
	msgs := []llm.Message{
		llm.TextMessage(llm.RoleUser, "look"),
		{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: "tool_call", ToolCall: &call}}},
		{Role: llm.RoleTool, Parts: []llm.Part{{Type: "tool_result", ToolResult: &llm.ToolResult{CallID: "call_c1", Name: llm.NativeComputer, Content: "took a screenshot"}}}},
		llm.TextMessage(llm.RoleUser, "go on"),
	}
	a := magpieLike(t, "http://x")
	in, _ := json.Marshal(a.responsesInput(llm.Request{Model: "openai/gpt-6.1-sol", NativeTools: []string{llm.NativeComputer}, Messages: msgs}, nil))
	if strings.Contains(string(in), "computer_call") || !strings.Contains(string(in), `"type":"function_call"`) || !strings.Contains(string(in), `"call_id":"call_c1"`) {
		t.Fatalf("input %s", in)
	}
	off := newAdapter(t, "http://x", func(pc *ProviderConfig) { pc.Preset = "openai" })
	in, _ = json.Marshal(off.responsesInput(llm.Request{Model: "gpt-6.1-sol", NativeTools: []string{llm.NativeComputer}, Messages: msgs}, nil))
	if !strings.Contains(string(in), `"type":"computer_call"`) || !strings.Contains(string(in), `"type":"computer_call_output"`) {
		t.Fatalf("OpenAI input lost its computer items: %s", in)
	}
}
