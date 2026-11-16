package gemini

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/core/llm"
)

func TestComputerUse(t *testing.T) {
	var cap captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.set(r)
		sse(w, `{"candidates":[{"content":{"role":"model","parts":[`+
			`{"functionCall":{"id":"fc1","name":"click","args":{"x":500,"y":420,"intent":"open the menu"}}},`+
			`{"functionCall":{"id":"fc2","name":"type","args":{"text":"hi","press_enter":true,"intent":"send","safety_decision":{"decision":"require_confirmation","explanation":"Sending a message"}}}},`+
			`{"functionCall":{"id":"fc3","name":"computer_apps","args":{}}}`+
			`]},"finishReason":"STOP"}]}`)
	}))
	defer srv.Close()
	a := newAdapter(srv.URL)
	req := llm.Request{Model: "gemini-4-carbon", NativeTools: []string{llm.NativeComputer},
		Tools:    []llm.ToolSchema{{Name: "computer_apps", Parameters: map[string]any{"type": "object"}}},
		Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "send hi")}}
	resp, err := a.Stream(context.Background(), req, func(llm.Chunk) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	// request: computer_use, desktop, the functions we cannot do excluded
	tl := cap.body["tools"].([]any)
	cu := tl[1].(map[string]any)["computerUse"].(map[string]any)
	if cu["environment"] != "ENVIRONMENT_DESKTOP" || !strings.Contains(cap.raw, `"excludedPredefinedFunctions":["triple_click"`) {
		t.Fatalf("tool %v", cu)
	}
	calls := resp.Message.ToolCalls()
	if len(calls) != 3 || calls[2].Name != "computer_apps" {
		t.Fatalf("calls %+v", calls)
	}
	c1, ok1 := llm.DecodeComputerCall(calls[0])
	c2, ok2 := llm.DecodeComputerCall(calls[1])
	if !ok1 || !ok2 || c1.Action != "click" || c1.Batch == "" || c1.Batch != c2.Batch || len(c1.Safety) != 0 {
		t.Fatalf("native %+v %+v", c1, c2)
	}
	if len(c2.Safety) != 1 || c2.Safety[0].Message != "Sending a message" {
		t.Fatalf("safety %+v", c2.Safety)
	}

	// replay: the predefined functions, their responses with the
	// screenshot inline, the acknowledgement only on the confirmed one
	png := []byte{0x89, 'P', 'N', 'G'}
	shot := []llm.Attachment{{Path: "generated/s.png", Mime: "image/png", Data: png}}
	req.Messages = append(req.Messages, resp.Message, llm.Message{Role: llm.RoleTool, Parts: []llm.Part{
		{Type: "tool_result", ToolResult: &llm.ToolResult{CallID: "fc1", Name: llm.NativeComputer, Content: "Did click.", Attachments: shot}},
		{Type: "tool_result", ToolResult: &llm.ToolResult{CallID: "fc2", Name: llm.NativeComputer, Content: llm.SafetyAcknowledged + "\nDid type.", Attachments: shot}},
		{Type: "tool_result", ToolResult: &llm.ToolResult{CallID: "fc3", Name: "computer_apps", Content: "apps"}},
	}})
	if _, err := a.Stream(context.Background(), req, func(llm.Chunk) error { return nil }); err != nil {
		t.Fatal(err)
	}
	cs := cap.body["contents"].([]any)
	model := cs[1].(map[string]any)["parts"].([]any)
	if fc := model[0].(map[string]any)["functionCall"].(map[string]any); fc["name"] != "click" || fc["args"].(map[string]any)["x"] != float64(500) {
		t.Fatalf("replayed call %v", fc)
	}
	user := cs[2].(map[string]any)["parts"].([]any)
	fr1 := user[0].(map[string]any)["functionResponse"].(map[string]any)
	fr2 := user[1].(map[string]any)["functionResponse"].(map[string]any)
	fr3 := user[2].(map[string]any)["functionResponse"].(map[string]any)
	if fr1["name"] != "click" || fr1["response"].(map[string]any)["safety_acknowledgement"] != nil {
		t.Fatalf("response 1 %v", fr1)
	}
	if r := fr2["response"].(map[string]any); fr2["name"] != "type" || r["safety_acknowledgement"] != true || r["output"] != "Did type." {
		t.Fatalf("response 2 %v", fr2)
	}
	if p := fr1["parts"].([]any)[0].(map[string]any)["inlineData"].(map[string]any); p["mimeType"] != "image/png" || p["data"] != "iVBORw==" {
		t.Fatalf("screenshot %v", fr1["parts"])
	}
	if fr3["name"] != "computer_apps" || fr3["parts"] != nil {
		t.Fatalf("function response %v", fr3)
	}

	// without the request (or on older models) nothing is declared and
	// a function named like a predefined one stays a function call
	for _, r := range []llm.Request{{Model: "gemini-2.5-flash", NativeTools: []string{llm.NativeComputer}}, {Model: "gemini-4-carbon"}} {
		r.Messages = []llm.Message{llm.TextMessage(llm.RoleUser, "x")}
		resp, err := a.Stream(context.Background(), r, func(llm.Chunk) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		if cap.body["tools"] != nil || resp.Message.ToolCalls()[0].Name != "click" {
			t.Fatalf("%s: tools %v calls %+v", r.Model, cap.body["tools"], resp.Message.ToolCalls())
		}
	}
}
