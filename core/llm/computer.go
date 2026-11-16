package llm

import (
	"encoding/json"
	"strings"
)

// NativeComputer asks for the provider's own computer-use tool
// (Request.NativeTools). Unlike web search it runs locally: the adapter
// turns each native action the model sends into a ToolCall named
// NativeComputer whose Args are a ComputerCall, so it passes the tool
// pipeline (policy, approval, logging) like any other call, and turns the
// ToolResult back into the provider's own result shape.
const NativeComputer = "computer"

// Vendors of native computer-use tools (ComputerCall.Vendor).
const (
	ComputerAnthropic = "anthropic"
	ComputerOpenAI    = "openai"
	ComputerGemini    = "gemini"
)

// ComputerCall is the Args of a NativeComputer tool call: one native
// action (Anthropic, Gemini) or one ordered list of actions (OpenAI) as
// the model sent it. features/computertools translates and runs it;
// adapters read it back to replay the call.
type ComputerCall struct {
	Vendor string `json:"vendor"`
	// Tool is the provider tool version ("computer_toolset_20260801",
	// "computer_20251124", "computer", "computer_use").
	Tool string `json:"tool,omitempty"`
	// Action is the member (Anthropic toolset), the input's action
	// (Anthropic computer_20251124) or the function name (Gemini).
	Action string `json:"action,omitempty"`
	// Input is the action's input (Anthropic) or arguments (Gemini).
	Input json.RawMessage `json:"input,omitempty"`
	// Actions is OpenAI's actions[] (a single legacy action is wrapped).
	Actions json.RawMessage `json:"actions,omitempty"`
	// Safety are the provider's pending safety checks: the person must
	// confirm them (every time) before the call runs and the
	// acknowledgement is sent back.
	Safety []SafetyCheck `json:"safety,omitempty"`
	// Batch groups the native calls of one model reply (Index of Count):
	// they run in order and stop at the first failure.
	Batch string `json:"batch,omitempty"`
	Index int    `json:"index,omitempty"`
	Count int    `json:"count,omitempty"`
}

// SafetyCheck is a provider safety handshake on a native computer call
// (OpenAI pending_safety_checks, Gemini safety_decision).
type SafetyCheck struct {
	ID      string `json:"id,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// SafetyAcknowledged starts the result of a native computer call whose
// safety checks the person confirmed; adapters send the provider's
// acknowledgement only then (and strip the line).
const SafetyAcknowledged = "[safety checks confirmed by the user]"

// Acknowledged reports whether a native computer result carries the
// person's confirmation, and returns the content without the marker.
func Acknowledged(content string) (bool, string) {
	if rest, ok := strings.CutPrefix(content, SafetyAcknowledged); ok {
		return true, strings.TrimPrefix(rest, "\n")
	}
	return false, content
}

// DecodeComputerCall reads tc as a native computer call.
func DecodeComputerCall(tc ToolCall) (ComputerCall, bool) {
	if tc.Name != NativeComputer {
		return ComputerCall{}, false
	}
	var c ComputerCall
	if json.Unmarshal(tc.Args, &c) != nil || c.Vendor == "" {
		return ComputerCall{}, false
	}
	return c, true
}

// EncodeComputerCall builds the ToolCall of a native computer call.
func EncodeComputerCall(id string, c ComputerCall) ToolCall {
	b, _ := json.Marshal(c)
	return ToolCall{ID: id, Name: NativeComputer, Args: b}
}

// Wants reports whether the request asks for native tool name.
func (r Request) Wants(name string) bool {
	for _, n := range r.NativeTools {
		if n == name {
			return true
		}
	}
	return false
}

// ComputerTool reports the native computer-use tool version a model
// takes ("" = none), from its id: a small table by prefix, overridden by
// a "computer_use" capability the model lists (then the vendor's current
// version). Model ids behind gateways ("anthropic/claude-…") are matched
// on their last segment.
func ComputerTool(model string, caps []string) string {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	has := false
	for _, c := range caps {
		if c == "computer_use" {
			has = true
		}
	}
	switch {
	case strings.HasPrefix(m, "claude-"):
		for _, p := range []string{"claude-opus-4-5", "claude-opus-4-6", "claude-opus-4-7", "claude-sonnet-4-5", "claude-sonnet-4-6", "claude-haiku-4-5"} {
			if strings.HasPrefix(m, p) {
				return "computer_20251124"
			}
		}
		for _, p := range []string{"claude-opus-4-8", "claude-opus-5", "claude-sonnet-5", "claude-haiku-5", "claude-fable-", "claude-mythos-"} {
			if strings.HasPrefix(m, p) {
				return "computer_toolset_20260801"
			}
		}
		if has {
			return "computer_toolset_20260801"
		}
	case strings.HasPrefix(m, "gpt-"):
		// the computer tool (Responses API): GPT-5.4 and later
		if has || openAIComputer(m) {
			return "computer"
		}
	case strings.HasPrefix(m, "gemini-"):
		if has || strings.HasPrefix(m, "gemini-3") || strings.HasPrefix(m, "gemini-4") || strings.Contains(m, "computer-use") {
			return "computer_use"
		}
	}
	return ""
}

// openAIComputer: gpt-5.4+, gpt-6*, later majors.
func openAIComputer(m string) bool {
	rest := strings.TrimPrefix(m, "gpt-")
	major, minor := 0, 0
	i := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		major = major*10 + int(rest[i]-'0')
		i++
	}
	if i < len(rest) && rest[i] == '.' {
		i++
		for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
			minor = minor*10 + int(rest[i]-'0')
			i++
		}
	}
	return major > 5 || major == 5 && minor >= 4
}

// GeminiComputerFunctions are Gemini's predefined computer_use functions
// (desktop environment); a function call with one of these names that is
// not a declared function is a native computer action.
var GeminiComputerFunctions = []string{"click", "double_click", "triple_click", "middle_click", "right_click", "mouse_down", "mouse_up", "move",
	"type", "drag_and_drop", "wait", "press_key", "key_down", "key_up", "hotkey", "take_screenshot", "scroll"}

// GeminiComputerExcluded are the predefined functions Crewbase's computer
// cannot do (sent as excluded_predefined_functions).
var GeminiComputerExcluded = []string{"triple_click", "middle_click", "mouse_down", "mouse_up", "key_down", "key_up"}
