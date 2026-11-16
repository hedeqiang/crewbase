package gemini

// Gemini's computer_use tool (desktop environment): the model calls
// predefined functions (click, type, drag_and_drop, …) with coordinates on
// a 0–999 grid and an intent; a safety_decision of require_confirmation
// must be confirmed by the person, and the function response then carries
// safety_acknowledgement. Every response carries a screenshot. Crewbase
// runs them as llm.NativeComputer calls (features/computertools).

import (
	"encoding/json"
	"slices"

	"google.golang.org/genai"

	"github.com/hedeqiang/crewbase/core/llm"
)

// computerTool is the computer_use tool, or nil when req does not ask for
// it or model has none.
func (a *Adapter) computerTool(req llm.Request, model string) *genai.Tool {
	if !req.Wants(llm.NativeComputer) || llm.ComputerTool(model, a.capsOf(model)) != "computer_use" {
		return nil
	}
	return &genai.Tool{ComputerUse: &genai.ComputerUse{Environment: genai.EnvironmentDesktop,
		ExcludedPredefinedFunctions: llm.GeminiComputerExcluded}}
}

// capsOf is the capability list the model listing reports (nil = unknown).
func (a *Adapter) capsOf(model string) []string {
	for _, s := range a.static {
		if s.ID == model {
			return s.Capabilities
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, m := range a.cached {
		if m.ID == model {
			return m.Capabilities
		}
	}
	return nil
}

// nativeCall turns a predefined function call into a native call (ok =
// false: a declared function, or computer_use was not declared).
func nativeCall(req llm.Request, declared bool, id string, fc *genai.FunctionCall, batch string) (llm.ToolCall, bool) {
	if !declared || !slices.Contains(llm.GeminiComputerFunctions, fc.Name) {
		return llm.ToolCall{}, false
	}
	for _, t := range req.Tools {
		if t.Name == fc.Name {
			return llm.ToolCall{}, false
		}
	}
	args := fc.Args
	if args == nil {
		args = map[string]any{}
	}
	in, _ := json.Marshal(args)
	c := llm.ComputerCall{Vendor: llm.ComputerGemini, Tool: "computer_use", Action: fc.Name, Input: in, Batch: batch}
	if sd, ok := args["safety_decision"].(map[string]any); ok && sd["decision"] == "require_confirmation" {
		msg, _ := sd["explanation"].(string)
		c.Safety = []llm.SafetyCheck{{Code: "require_confirmation", Message: msg}}
	}
	return llm.EncodeComputerCall(id, c), true
}

// nativeFunctionCall replays a native call as the predefined function.
func nativeFunctionCall(tc *llm.ToolCall) (*genai.FunctionCall, bool) {
	c, ok := llm.DecodeComputerCall(*tc)
	if !ok || c.Vendor != llm.ComputerGemini {
		return nil, false
	}
	var args map[string]any
	_ = json.Unmarshal(c.Input, &args)
	return &genai.FunctionCall{Name: c.Action, Args: args}, true
}

// nativeResponse is the function response of a native call: the outcome,
// the acknowledgement when the person confirmed, and the screenshot.
func nativeResponse(action string, r *llm.ToolResult) *genai.FunctionResponse {
	ack, text := llm.Acknowledged(r.Content)
	resp := map[string]any{"output": text}
	if r.IsError {
		resp = map[string]any{"error": text}
	}
	if ack {
		resp["safety_acknowledgement"] = true
	}
	fr := &genai.FunctionResponse{Name: action, Response: resp}
	for _, at := range r.Attachments {
		if len(at.Data) > 0 && at.IsImage() {
			fr.Parts = append(fr.Parts, &genai.FunctionResponsePart{InlineData: &genai.FunctionResponseBlob{MIMEType: at.Mime, Data: at.Data}})
		}
	}
	return fr
}
