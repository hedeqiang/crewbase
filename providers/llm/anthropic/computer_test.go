package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hedeqiang/crewbase/core/llm"
)

// Recorded shape of a toolset turn (platform docs, computer use tool):
// two members in one reply, each carrying toolset_name.
var toolsetStream = [][2]string{
	{"message_start", `{"type":"message_start","message":{"id":"msg_cu1","usage":{"input_tokens":10,"output_tokens":1}}}`},
	{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_a","name":"left_click","toolset_name":"computer","input":{}}}`},
	{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"coordinate\": [512, 742]}"}}`},
	{"content_block_stop", `{"type":"content_block_stop","index":0}`},
	{"content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_b","name":"screenshot","toolset_name":"computer","input":{}}}`},
	{"content_block_stop", `{"type":"content_block_stop","index":1}`},
	{"content_block_start", `{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_c","name":"computer_apps","input":{}}}`},
	{"content_block_stop", `{"type":"content_block_stop","index":2}`},
	{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":30}}`},
	{"message_stop", `{"type":"message_stop"}`},
}

func TestComputerToolset(t *testing.T) {
	var cap captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.set(r)
		sse(w, toolsetStream...)
	}))
	defer srv.Close()
	a := newAdapter(srv.URL)
	req := llm.Request{Model: "claude-opus-5-5", NativeTools: []string{llm.NativeComputer},
		Tools:    []llm.ToolSchema{{Name: "computer_apps", Parameters: map[string]any{"type": "object"}}},
		Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "click it")}}
	resp, err := a.Stream(context.Background(), req, func(llm.Chunk) error { return nil })
	if err != nil {
		t.Fatal(err)
	}

	// request: the toolset entry, no name, no display size, no beta header
	tl := cap.body["tools"].([]any)
	if len(tl) != 2 {
		t.Fatalf("tools %v", tl)
	}
	ts := tl[1].(map[string]any)
	if ts["type"] != Toolset || ts["name"] != nil || ts["display_width_px"] != nil {
		t.Fatalf("toolset entry %v", ts)
	}
	cfg := ts["configs"].(map[string]any)
	if cfg["zoom"].(map[string]any)["enabled"] != false || cfg["left_click"] != nil {
		t.Fatalf("configs %v", cfg)
	}
	if b := cap.hdr.Get("anthropic-beta"); b != "" {
		t.Fatalf("beta header %q", b)
	}

	// response: members become native calls of one batch, in order
	calls := resp.Message.ToolCalls()
	if len(calls) != 3 || resp.StopReason != "tool_use" {
		t.Fatalf("calls %+v", calls)
	}
	for i, want := range []string{"left_click", "screenshot"} {
		c, ok := llm.DecodeComputerCall(calls[i])
		if !ok || c.Vendor != llm.ComputerAnthropic || c.Action != want || c.Batch != "msg_cu1" {
			t.Fatalf("call %d: %+v", i, c)
		}
	}
	c, _ := llm.DecodeComputerCall(calls[0])
	var in struct{ Coordinate []int }
	if json.Unmarshal(c.Input, &in) != nil || len(in.Coordinate) != 2 || in.Coordinate[0] != 512 {
		t.Fatalf("input %s", c.Input)
	}
	if calls[2].Name != "computer_apps" {
		t.Fatalf("function call %+v", calls[2])
	}

	// next request: tool_use replayed under the member name with
	// toolset_name; every result echoes it, the screenshot inside
	png := []byte{0x89, 'P', 'N', 'G'}
	req.Messages = append(req.Messages, resp.Message,
		llm.Message{Role: llm.RoleTool, Parts: []llm.Part{{Type: "tool_result", ToolResult: &llm.ToolResult{CallID: "toolu_a", Name: llm.NativeComputer, Content: "Did click (320, 464) in 备忘录."}}}},
		llm.Message{Role: llm.RoleTool, Parts: []llm.Part{{Type: "tool_result", ToolResult: &llm.ToolResult{CallID: "toolu_b", Name: llm.NativeComputer, Content: "Screenshot",
			Attachments: []llm.Attachment{{Path: "generated/s.png", Mime: "image/png", Data: png}}}}}},
		llm.Message{Role: llm.RoleTool, Parts: []llm.Part{{Type: "tool_result", ToolResult: &llm.ToolResult{CallID: "toolu_c", Name: "computer_apps", Content: "apps"}}}},
	)
	if _, err := a.Stream(context.Background(), req, func(llm.Chunk) error { return nil }); err != nil {
		t.Fatal(err)
	}
	msgs := cap.body["messages"].([]any)
	asst := msgs[1].(map[string]any)["content"].([]any)
	u0 := asst[0].(map[string]any)
	if u0["type"] != "tool_use" || u0["name"] != "left_click" || u0["toolset_name"] != "computer" || u0["id"] != "toolu_a" {
		t.Fatalf("tool_use %v", u0)
	}
	if co := u0["input"].(map[string]any)["coordinate"].([]any); co[1] != float64(742) {
		t.Fatalf("input %v", u0["input"])
	}
	if f := asst[2].(map[string]any); f["name"] != "computer_apps" || f["toolset_name"] != nil {
		t.Fatalf("function tool_use %v", f)
	}
	user := msgs[2].(map[string]any)["content"].([]any)
	if len(user) != 3 {
		t.Fatalf("results %v", user)
	}
	r0, r1, r2 := user[0].(map[string]any), user[1].(map[string]any), user[2].(map[string]any)
	if r0["toolset_name"] != "computer" || r1["toolset_name"] != "computer" || r2["toolset_name"] != nil {
		t.Fatalf("toolset_name echo: %v / %v / %v", r0, r1, r2)
	}
	img := r1["content"].([]any)[1].(map[string]any)
	if img["type"] != "image" || img["source"].(map[string]any)["data"] != "iVBORw==" {
		t.Fatalf("screenshot block %v", img)
	}

	// models without the toolset, or requests without it: nothing declared
	for _, r := range []llm.Request{{Model: "claude-opus-4-7", NativeTools: []string{llm.NativeComputer}}, {Model: "claude-opus-5-5"}} {
		r.Messages = []llm.Message{llm.TextMessage(llm.RoleUser, "x")}
		if _, err := a.Stream(context.Background(), r, func(llm.Chunk) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if cap.body["tools"] != nil {
			t.Fatalf("%s: tools %v", r.Model, cap.body["tools"])
		}
	}
}
