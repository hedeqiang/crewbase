package anthropic

// Claude's computer toolset (computer_toolset_20260801, GA, no beta
// header): one tools entry without a name or display size; the model's
// calls are tool_use blocks named after the member, carrying
// "toolset_name": "computer", often several per turn; every tool_result
// echoes the toolset name. Crewbase runs them as llm.NativeComputer calls
// (features/computertools). Members this computer cannot do are turned
// off with configs. The older computer_20251124 (beta, display size) is
// not sent: models that only take it keep the computer_* function tools.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/packages/param"

	"github.com/hedeqiang/crewbase/core/llm"
)

// Toolset is the computer toolset version declared.
const Toolset = "computer_toolset_20260801"

// toolsetName is the toolset family echoed on calls and results.
const toolsetName = "computer"

// computerTool is the toolset entry, or nil when req does not ask for it
// or model does not take the toolset.
func (a *Adapter) computerTool(req llm.Request, model string) *sdk.ToolUnionParam {
	if !req.Wants(llm.NativeComputer) || llm.ComputerTool(model, a.capsOf(model)) != Toolset {
		return nil
	}
	off := param.NewOpt(false)
	return &sdk.ToolUnionParam{OfComputerToolset20260801: &sdk.ComputerToolset20260801Param{
		Configs: sdk.ComputerToolsetConfigsParam{
			Zoom:           sdk.ComputerZoomConfigParam{Enabled: off},
			TripleClick:    sdk.ComputerTripleClickConfigParam{Enabled: off},
			MiddleClick:    sdk.ComputerMiddleClickConfigParam{Enabled: off},
			MouseMove:      sdk.ComputerMouseMoveConfigParam{Enabled: off},
			LeftMouseDown:  sdk.ComputerLeftMouseDownConfigParam{Enabled: off},
			LeftMouseUp:    sdk.ComputerLeftMouseUpConfigParam{Enabled: off},
			HoldKey:        sdk.ComputerHoldKeyConfigParam{Enabled: off},
			CursorPosition: sdk.ComputerCursorPositionConfigParam{Enabled: off},
		},
	}}
}

// capsOf is the capability list the model listing reports (nil = unknown).
func (a *Adapter) capsOf(model string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, m := range a.cached {
		if m.ID == model {
			return m.Capabilities
		}
	}
	for _, s := range a.static {
		if s.ID == model {
			return s.Capabilities
		}
	}
	return nil
}

// nativeCall turns a toolset member's tool_use into a native call.
func nativeCall(id, member string, input json.RawMessage, batch string) llm.ToolCall {
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	return llm.EncodeComputerCall(id, llm.ComputerCall{Vendor: llm.ComputerAnthropic, Tool: Toolset,
		Action: member, Input: input, Batch: batch})
}

// nativeUse replays a native call as the member tool_use it came from
// (ok=false: not ours).
func nativeUse(tc *llm.ToolCall) (sdk.ContentBlockParamUnion, bool) {
	c, ok := llm.DecodeComputerCall(*tc)
	if !ok || c.Vendor != llm.ComputerAnthropic {
		return sdk.ContentBlockParamUnion{}, false
	}
	in := c.Input
	if len(in) == 0 {
		in = json.RawMessage("{}")
	}
	return sdk.ContentBlockParamUnion{OfToolUse: &sdk.ToolUseBlockParam{ID: tc.ID, Name: c.Action, Input: in,
		ToolsetName: param.NewOpt(toolsetName)}}, true
}

// nativeResult is the toolset tool_result: the text, and the screenshot
// as an image block inside the result.
func nativeResult(r *llm.ToolResult) sdk.ContentBlockParamUnion {
	_, text := llm.Acknowledged(r.Content)
	tr := sdk.ToolResultBlockParam{ToolUseID: r.CallID, ToolsetName: param.NewOpt(toolsetName)}
	if r.IsError {
		tr.IsError = param.NewOpt(true)
	}
	if text == "" {
		text = "OK"
	}
	tr.Content = append(tr.Content, sdk.ToolResultBlockParamContentUnion{OfText: &sdk.TextBlockParam{Text: text}})
	for _, at := range r.Attachments {
		switch {
		case len(at.Data) > 0 && at.IsImage():
			tr.Content = append(tr.Content, sdk.ToolResultBlockParamContentUnion{OfImage: &sdk.ImageBlockParam{
				Source: sdk.ImageBlockParamSourceUnion{OfBase64: &sdk.Base64ImageSourceParam{
					Data: base64.StdEncoding.EncodeToString(at.Data), MediaType: sdk.Base64ImageSourceMediaType(at.Mime)}}}})
		default:
			tr.Content = append(tr.Content, sdk.ToolResultBlockParamContentUnion{OfText: &sdk.TextBlockParam{
				Text: fmt.Sprintf("[screenshot %s is not available to this model]", at.Path)}})
		}
	}
	return sdk.ContentBlockParamUnion{OfToolResult: &tr}
}
