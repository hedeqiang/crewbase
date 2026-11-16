package openai

// OpenAI's computer tool lives only in the Responses API: tools
// [{"type":"computer"}]; the model returns computer_call items with an
// ordered actions[] (and pending_safety_checks); each is answered with a
// computer_call_output carrying a computer_screenshot, acknowledging the
// safety checks the person confirmed. Requests that ask for
// llm.NativeComputer on a model with the tool go this way (openai preset,
// or "responses": true); everything else stays on Chat Completions.
//
// The request body is built as JSON through the SDK's extra-fields
// mechanism (option.WithJSONSet) and the stream is read from the raw
// events, so the adapter does not depend on the SDK's evolving item
// types. Requests are stateless (store: false): reasoning items are sent
// back with their encrypted content, kept in the thinking part's Meta.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"github.com/hedeqiang/crewbase/core/llm"
)

// Part.Meta keys of Responses items.
const (
	// MetaReasoning is a reasoning item (raw JSON, encrypted content).
	MetaReasoning = "openai:reasoning"
	// MetaItem is the id of a computer_call item.
	MetaItem = "openai:item"
)

// computerTool reports whether req goes through the Responses API with
// the computer tool.
func (a *Adapter) computerTool(req llm.Request) bool {
	if !a.responses || !req.Wants(llm.NativeComputer) {
		return false
	}
	var caps []string
	if s, ok := findSpec(a.static, req.Model); ok {
		caps = s.Capabilities
	}
	return llm.ComputerTool(req.Model, caps) == "computer"
}

// responsesBody is the Responses request (besides the model).
func (a *Adapter) responsesBody(req llm.Request) map[string]any {
	tl := []any{}
	for _, t := range req.Tools {
		tl = append(tl, map[string]any{"type": "function", "name": t.Name, "description": t.Description,
			"parameters": schemaOrEmpty(t.Parameters), "strict": false})
	}
	tl = append(tl, map[string]any{"type": "computer"})
	body := map[string]any{
		"input":   responsesInput(req.Messages),
		"tools":   tl,
		"store":   false,
		"include": []string{"reasoning.encrypted_content"},
	}
	if req.System != "" {
		body["instructions"] = req.System
	}
	if req.MaxTokens > 0 {
		body["max_output_tokens"] = req.MaxTokens
	}
	if e := strings.ToLower(req.Effort); e != "" && a.effortFor(req.Model) {
		if e == "max" {
			e = "xhigh"
		}
		body["reasoning"] = map[string]any{"effort": e, "summary": "auto"}
	}
	return body
}

// responsesInput maps the conversation to Responses input items.
func responsesInput(msgs []llm.Message) []any {
	native := map[string]llm.ComputerCall{}
	for _, m := range msgs {
		for _, tc := range m.ToolCalls() {
			if c, ok := llm.DecodeComputerCall(tc); ok && c.Vendor == llm.ComputerOpenAI {
				native[tc.ID] = c
			}
		}
	}
	var out []any
	for _, m := range msgs {
		switch m.Role {
		case llm.RoleAssistant:
			var text strings.Builder
			flush := func() {
				if text.Len() > 0 {
					out = append(out, map[string]any{"role": "assistant", "content": text.String()})
					text.Reset()
				}
			}
			for _, p := range m.Parts {
				switch p.Type {
				case "thinking":
					if raw := p.Meta[MetaReasoning]; raw != "" && json.Valid([]byte(raw)) {
						flush()
						out = append(out, json.RawMessage(raw))
					}
				case "text":
					text.WriteString(p.Text)
				case "tool_call":
					tc := p.ToolCall
					if tc == nil {
						continue
					}
					flush()
					if c, ok := native[tc.ID]; ok {
						item := map[string]any{"type": "computer_call", "call_id": tc.ID, "actions": json.RawMessage(c.Actions),
							"pending_safety_checks": safetyJSON(c.Safety), "status": "completed"}
						if id := p.Meta[MetaItem]; id != "" {
							item["id"] = id
						}
						out = append(out, item)
						continue
					}
					out = append(out, map[string]any{"type": "function_call", "call_id": tc.ID, "name": tc.Name, "arguments": string(argsObject(tc.Args))})
				}
			}
			flush()
		default:
			var content []any
			var after []any
			for _, p := range m.Parts {
				switch p.Type {
				case "text":
					if p.Text != "" {
						content = append(content, map[string]any{"type": "input_text", "text": p.Text})
					}
				case llm.PartAttachment:
					if at := p.Attachment; at != nil {
						content = append(content, map[string]any{"type": "input_text", "text": at.Fallback()})
						switch {
						case len(at.Data) > 0 && at.IsImage():
							content = append(content, map[string]any{"type": "input_image", "image_url": dataURL(at), "detail": "auto"})
						case len(at.Data) > 0 && at.IsPDF():
							content = append(content, map[string]any{"type": "input_file", "filename": at.Name, "file_data": dataURL(at)})
						case len(at.Data) > 0 && at.Alt != "":
							content = append(content, map[string]any{"type": "input_text", "text": at.Alt})
						}
					}
				case "tool_result":
					r := p.ToolResult
					if r == nil {
						continue
					}
					c, ok := native[r.CallID]
					if !ok {
						text := r.Content
						if r.IsError {
							text = "[error] " + text
						}
						out = append(out, map[string]any{"type": "function_call_output", "call_id": r.CallID, "output": text})
						continue
					}
					ack, text := llm.Acknowledged(r.Content)
					item := map[string]any{"type": "computer_call_output", "call_id": r.CallID}
					for _, at := range r.Attachments {
						if len(at.Data) > 0 && at.IsImage() {
							item["output"] = map[string]any{"type": "computer_screenshot", "image_url": dataURL(&at), "detail": "original"}
						}
					}
					if ack && len(c.Safety) > 0 {
						item["acknowledged_safety_checks"] = safetyJSON(c.Safety)
					}
					out = append(out, item)
					// what happened (and failures) in words, after the outputs
					if r.IsError || item["output"] == nil {
						after = append(after, map[string]any{"type": "input_text", "text": "[computer] " + text})
					}
				}
			}
			if len(after) > 0 {
				content = append(after, content...)
			}
			if len(content) > 0 {
				out = append(out, map[string]any{"role": "user", "content": content})
			}
		}
	}
	return out
}

func safetyJSON(cs []llm.SafetyCheck) []any {
	out := []any{}
	for _, c := range cs {
		out = append(out, map[string]any{"id": c.ID, "code": c.Code, "message": c.Message})
	}
	return out
}

func dataURL(at *llm.Attachment) string {
	return "data:" + at.Mime + ";base64," + base64.StdEncoding.EncodeToString(at.Data)
}

// rItem is a Responses output item, as much of it as Crewbase reads.
type rItem struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	CallID  string          `json:"call_id"`
	Name    string          `json:"name"`
	Args    string          `json:"arguments"`
	Action  json.RawMessage `json:"action"`
	Actions json.RawMessage `json:"actions"`
	Safety  []struct {
		ID      string `json:"id"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"pending_safety_checks"`
	Content []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
	Summary []struct {
		Text string `json:"text"`
	} `json:"summary"`
}

// rEvent is a Responses stream event.
type rEvent struct {
	Type     string `json:"type"`
	Delta    string `json:"delta"`
	Message  string `json:"message"`
	Code     string `json:"code"`
	Response *struct {
		ID     string            `json:"id"`
		Status string            `json:"status"`
		Output []json.RawMessage `json:"output"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Incomplete *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Usage *struct {
			Input   int `json:"input_tokens"`
			Output  int `json:"output_tokens"`
			Details struct {
				Cached int `json:"cached_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
	} `json:"response"`
}

// streamResponses performs a streaming Responses request.
func (a *Adapter) streamResponses(ctx context.Context, req llm.Request, onChunk func(llm.Chunk) error) (llm.Response, error) {
	opts, key, err := a.options(ctx)
	if err != nil {
		return llm.Response{}, err
	}
	svc := responses.NewResponseService(opts...)
	var extra []option.RequestOption
	for k, v := range a.responsesBody(req) {
		extra = append(extra, option.WithJSONSet(k, v))
	}
	stream := svc.NewStreaming(ctx, responses.ResponseNewParams{Model: shared.ResponsesModel(req.Model)}, extra...)
	defer stream.Close()

	var done *rEvent
	for stream.Next() {
		var ev rEvent
		if err := json.Unmarshal([]byte(stream.Current().RawJSON()), &ev); err != nil {
			return llm.Response{}, &llm.Error{Kind: llm.ErrUnavailable, Provider: a.id, Message: "malformed stream event"}
		}
		switch ev.Type {
		case "response.output_text.delta":
			if err := onChunk(llm.Chunk{Type: "text", Text: ev.Delta}); err != nil {
				return llm.Response{}, err
			}
		case "response.reasoning_summary_text.delta":
			if err := onChunk(llm.Chunk{Type: "thinking", Text: ev.Delta}); err != nil {
				return llm.Response{}, err
			}
		case "response.completed", "response.incomplete":
			e := ev
			done = &e
		case "response.failed":
			msg, code := "response failed", ""
			if ev.Response != nil && ev.Response.Error != nil {
				msg, code = ev.Response.Error.Message, ev.Response.Error.Code
			}
			return llm.Response{}, classify(a.id, 0, nil, msg, code, key)
		case "error":
			return llm.Response{}, classify(a.id, 0, nil, ev.Message, ev.Code, key)
		}
	}
	if err := stream.Err(); err != nil {
		return llm.Response{}, a.classifyErr(ctx, err, key)
	}
	if done == nil || done.Response == nil {
		if ctx.Err() != nil {
			return llm.Response{}, ctx.Err()
		}
		return llm.Response{}, &llm.Error{Kind: llm.ErrUnavailable, Provider: a.id, Message: "stream ended before completion"}
	}
	r := done.Response
	msg := llm.Message{Role: llm.RoleAssistant}
	refused := false
	for _, raw := range r.Output {
		var it rItem
		if json.Unmarshal(raw, &it) != nil {
			continue
		}
		switch it.Type {
		case "reasoning":
			var sum []string
			for _, s := range it.Summary {
				sum = append(sum, s.Text)
			}
			msg.Parts = append(msg.Parts, llm.Part{Type: "thinking", Text: strings.Join(sum, "\n\n"), Meta: map[string]string{MetaReasoning: string(raw)}})
		case "message":
			for _, c := range it.Content {
				switch {
				case c.Type == "output_text" && c.Text != "":
					msg.Parts = append(msg.Parts, llm.Part{Type: "text", Text: c.Text})
				case c.Type == "refusal":
					refused = true
					msg.Parts = append(msg.Parts, llm.Part{Type: "text", Text: c.Refusal})
				}
			}
		case "function_call":
			tc := &llm.ToolCall{ID: it.CallID, Name: it.Name, Args: normalizeArgs(it.Args)}
			msg.Parts = append(msg.Parts, llm.Part{Type: "tool_call", ToolCall: tc})
			if err := onChunk(llm.Chunk{Type: "tool_call", ToolCall: tc}); err != nil {
				return llm.Response{}, err
			}
		case "computer_call":
			actions := it.Actions
			if len(actions) == 0 || string(actions) == "null" {
				actions = json.RawMessage("[" + string(it.Action) + "]") // a single legacy action
			}
			c := llm.ComputerCall{Vendor: llm.ComputerOpenAI, Tool: "computer", Actions: actions, Batch: r.ID}
			for _, s := range it.Safety {
				c.Safety = append(c.Safety, llm.SafetyCheck{ID: s.ID, Code: s.Code, Message: s.Message})
			}
			tc := llm.EncodeComputerCall(it.CallID, c)
			msg.Parts = append(msg.Parts, llm.Part{Type: "tool_call", ToolCall: &tc, Meta: map[string]string{MetaItem: it.ID}})
			if err := onChunk(llm.Chunk{Type: "tool_call", ToolCall: &tc}); err != nil {
				return llm.Response{}, err
			}
		}
	}
	var usage llm.Usage
	if u := r.Usage; u != nil {
		usage = llm.Usage{InputTokens: u.Input, OutputTokens: u.Output, CacheRead: u.Details.Cached}
	}
	stop := "end"
	switch {
	case refused:
		stop = llm.StopRefusal
	case len(msg.ToolCalls()) > 0:
		stop = "tool_use"
	case r.Status == "incomplete" && r.Incomplete != nil && r.Incomplete.Reason == "max_output_tokens":
		stop = "max_tokens"
	case r.Status == "incomplete":
		return llm.Response{}, &llm.Error{Kind: llm.ErrBadRequest, Provider: a.id, Message: fmt.Sprintf("response incomplete: %+v", r.Incomplete)}
	}
	return llm.Response{Message: msg, Usage: estimateUsage(usage, req, msg), StopReason: stop}, nil
}
