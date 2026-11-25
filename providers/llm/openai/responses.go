package openai

// The Responses API path (see routing.go for which requests take it).
//
// It serves normal agent turns: instructions, the conversation as input
// items (messages with text, images and PDFs; function_call and
// function_call_output items), function tools, reasoning effort and
// summaries, max_output_tokens, streaming text / reasoning / tool calls,
// usage with cached tokens and stop reasons. And OpenAI's computer tool,
// which lives only here: tools [{"type":"computer"}]; the model returns
// computer_call items with an ordered actions[] (and
// pending_safety_checks); each is answered with a computer_call_output
// carrying a computer_screenshot, acknowledging the safety checks the
// person confirmed.
//
// The request body is built as JSON through the SDK's extra-fields
// mechanism (option.WithJSONSet) and the stream is read from the raw
// events, so the adapter does not depend on the SDK's evolving item
// types. Requests are stateless (store: false): reasoning items are sent
// back with their encrypted content, kept in the thinking part's Meta
// together with the provider and model that wrote them (encrypted
// reasoning only verifies on the account that produced it, and a gateway
// may route each model to a different account).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
	// MetaReasoningFrom is "<provider>/<model>" of a reasoning item.
	MetaReasoningFrom = "openai:from"
	// MetaItem is the id of a computer_call item.
	MetaItem = "openai:item"
)

// computerTool reports whether req goes through the Responses API with
// the computer tool: on OpenAI's own API for the models that have it, and
// elsewhere only for a model whose capabilities list computer_use.
// Gateways and compatible servers do not all take it: magpie's Codex
// accounts answer "Unsupported tool type: computer". One that refuses it
// anyway is remembered per model (refusedComputer). Without it computer
// use goes on through the function tools.
func (a *Adapter) computerTool(req llm.Request) bool {
	if a.respMode == ResponsesNever || !req.Wants(llm.NativeComputer) || a.routes.computerRefused(req.Model) {
		return false
	}
	var caps []string
	if s, ok := findSpec(a.static, req.Model); ok {
		caps = s.Capabilities
	}
	if llm.ComputerTool(req.Model, caps) != "computer" {
		return false
	}
	return a.official || slices.Contains(caps, "computer_use")
}

// refusedComputer reports whether err says the provider does not take the
// computer tool ("Codex: Unsupported tool type: computer", "tool type
// 'computer' is not supported").
func refusedComputer(err error) bool {
	var le *llm.Error
	if !errors.As(err, &le) || le.Kind != llm.ErrBadRequest {
		return false
	}
	m := strings.ToLower(le.Message)
	if !strings.Contains(m, "computer") {
		return false
	}
	for _, w := range []string{"unsupported tool", "tool type", "not supported", "unknown tool", "invalid tool", "not available"} {
		if strings.Contains(m, w) {
			return true
		}
	}
	return false
}

// reasoningFrom tags reasoning items of this provider and model.
func (a *Adapter) reasoningFrom(model string) string { return a.id + "/" + model }

// responsesEffort maps a Crewbase effort to reasoning.effort ("" = none
// sent): max is sent as xhigh, off as none.
func responsesEffort(e string) string {
	switch e = strings.ToLower(e); e {
	case "max":
		return "xhigh"
	case "off":
		return "none"
	}
	return e
}

// responsesBody is the Responses request (besides the model).
func (a *Adapter) responsesBody(req llm.Request) map[string]any {
	tl, namespaces := a.responsesTools(req)
	if a.computerTool(req) {
		tl = append(tl, map[string]any{"type": "computer"})
	}
	body := map[string]any{
		"input": a.responsesInput(req, namespaces),
		"store": false,
	}
	if len(tl) > 0 {
		body["tools"] = tl
	}
	if req.System != "" {
		body["instructions"] = req.System
	}
	if req.MaxTokens > 0 {
		body["max_output_tokens"] = req.MaxTokens
	}
	if a.effortFor(req.Model) { // a reasoning model
		body["include"] = []string{"reasoning.encrypted_content"}
		r := map[string]any{}
		if e := responsesEffort(req.Effort); e != "" {
			r["effort"] = e
		}
		if r["effort"] != "none" {
			// "detailed": with "auto" GPT-6.x (seen through magpie, 2026-11)
			// returns its reasoning with an empty summary, so nothing of the
			// model's thinking could be shown
			r["summary"] = "detailed"
		}
		body["reasoning"] = r
	}
	return body
}

// responsesTools maps the function tools; namespaces names the tool
// search namespaces sent (see toolsearch.go).
func (a *Adapter) responsesTools(req llm.Request) ([]any, map[string]bool) {
	if a.searchTools(req) {
		return a.searchToolList(req.Tools)
	}
	var tl []any
	for _, t := range req.Tools {
		tl = append(tl, functionTool(t, false))
	}
	return tl, nil
}

func functionTool(t llm.ToolSchema, deferred bool) map[string]any {
	f := map[string]any{"type": "function", "name": t.Name, "description": t.Description,
		"parameters": schemaOrEmpty(t.Parameters), "strict": false}
	if deferred {
		f["defer_loading"] = true
	}
	return f
}

// responsesInput maps the conversation to Responses input items.
// namespaces are the tool namespaces of this request: a recorded call's
// namespace is kept only when the request still has it.
func (a *Adapter) responsesInput(req llm.Request, namespaces map[string]bool) []any {
	msgs := req.Messages
	// recorded computer calls go as computer_call items only with the
	// computer tool; without it (another provider, or one that refused it)
	// as plain function calls, as Chat Completions sends them
	native := map[string]llm.ComputerCall{}
	if a.computerTool(req) {
		for _, m := range msgs {
			for _, tc := range m.ToolCalls() {
				if c, ok := llm.DecodeComputerCall(tc); ok && c.Vendor == llm.ComputerOpenAI {
					native[tc.ID] = c
				}
			}
		}
	}
	from := a.reasoningFrom(req.Model)
	var out []any
	for _, m := range msgs {
		switch m.Role {
		case llm.RoleAssistant:
			replaySearch := namespaces != nil && searchReplayable(m, namespaces)
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
					raw := p.Meta[MetaReasoning]
					if raw == "" || !json.Valid([]byte(raw)) {
						continue // another provider's thinking
					}
					if f := p.Meta[MetaReasoningFrom]; f != "" && f != from {
						continue // encrypted for another account / model
					}
					flush()
					out = append(out, json.RawMessage(raw))
				case llm.PartProviderBlock:
					if raw := p.Meta[MetaBlock]; raw != "" && replaySearch && json.Valid([]byte(raw)) {
						flush()
						out = append(out, json.RawMessage(raw)) // tool search items, in place
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
					item := map[string]any{"type": "function_call", "call_id": tc.ID, "name": tc.Name, "arguments": string(argsObject(tc.Args))}
					if ns := p.Meta[MetaNamespace]; ns != "" && namespaces[ns] {
						item["namespace"] = ns
					}
					out = append(out, item)
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
						content = append(content, attachmentInput(at)...)
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

// attachmentInput is a user attachment as input content: its label, then
// the image or PDF itself, or its text when it cannot be sent natively.
func attachmentInput(at *llm.Attachment) []any {
	switch {
	case len(at.Data) > 0 && at.IsImage():
		return []any{map[string]any{"type": "input_text", "text": at.Fallback()},
			map[string]any{"type": "input_image", "image_url": dataURL(at), "detail": "auto"}}
	case len(at.Data) > 0 && at.IsPDF():
		return []any{map[string]any{"type": "input_text", "text": at.Fallback()},
			map[string]any{"type": "input_file", "filename": at.Name, "file_data": dataURL(at)}}
	case at.Alt != "":
		return []any{map[string]any{"type": "input_text", "text": at.Alt}}
	}
	return []any{map[string]any{"type": "input_text", "text": at.Fallback()}}
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
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Namespace string          `json:"namespace"`
	Args      json.RawMessage `json:"arguments"` // a JSON string; an object for tool_search_call
	Action    json.RawMessage `json:"action"`
	Actions   json.RawMessage `json:"actions"`
	Safety    []struct {
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
	// tool search (see toolsearch.go)
	Execution string            `json:"execution"`
	Tools     []json.RawMessage `json:"tools"`
}

// args is a function_call's arguments string.
func (it rItem) args() string {
	var s string
	if json.Unmarshal(it.Args, &s) == nil {
		return s
	}
	return string(it.Args)
}

// rError is an error object of a stream event or failed response.
type rError struct {
	Code    any    `json:"code"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (e *rError) code() string {
	if e == nil {
		return ""
	}
	c := e.Type
	if e.Code != nil {
		c = strings.TrimSpace(c + " " + fmt.Sprint(e.Code))
	}
	return c
}

// rEvent is a Responses stream event.
type rEvent struct {
	Type         string          `json:"type"`
	Delta        string          `json:"delta"`
	Message      string          `json:"message"`
	Code         any             `json:"code"`
	Error        *rError         `json:"error"`
	SummaryIndex int             `json:"summary_index"`
	Item         json.RawMessage `json:"item"`
	Response     *struct {
		ID         string            `json:"id"`
		Status     string            `json:"status"`
		Output     []json.RawMessage `json:"output"`
		Error      *rError           `json:"error"`
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
	var items []json.RawMessage // output_item.done, for servers whose completed event has no output
	emitted := map[string]bool{}
	emitCall := func(tc *llm.ToolCall) error {
		if emitted[tc.ID] {
			return nil
		}
		emitted[tc.ID] = true
		return onChunk(llm.Chunk{Type: "tool_call", ToolCall: tc})
	}
	thinking := false
	for stream.Next() {
		var ev rEvent
		if err := json.Unmarshal([]byte(stream.Current().RawJSON()), &ev); err != nil {
			return llm.Response{}, &llm.Error{Kind: llm.ErrUnavailable, Provider: a.id, Message: "malformed stream event"}
		}
		switch ev.Type {
		case "response.output_text.delta", "response.refusal.delta":
			if err := onChunk(llm.Chunk{Type: "text", Text: ev.Delta}); err != nil {
				return llm.Response{}, err
			}
		case "response.reasoning_summary_part.added":
			if ev.SummaryIndex > 0 && thinking {
				if err := onChunk(llm.Chunk{Type: "thinking", Text: "\n\n"}); err != nil {
					return llm.Response{}, err
				}
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			thinking = true
			if err := onChunk(llm.Chunk{Type: "thinking", Text: ev.Delta}); err != nil {
				return llm.Response{}, err
			}
		case "response.output_item.done":
			if len(ev.Item) == 0 {
				continue
			}
			items = append(items, ev.Item)
			var it rItem
			// a summary that came whole, not streamed (some gateways): show it now
			if json.Unmarshal(ev.Item, &it) == nil && it.Type == "reasoning" && !thinking {
				var parts []string
				for _, x := range it.Summary {
					if x.Text != "" {
						parts = append(parts, x.Text)
					}
				}
				if len(parts) > 0 {
					thinking = true
					if err := onChunk(llm.Chunk{Type: "thinking", Text: strings.Join(parts, "\n\n")}); err != nil {
						return llm.Response{}, err
					}
				}
			}
			if it.Type == "function_call" && it.CallID != "" {
				if err := emitCall(&llm.ToolCall{ID: it.CallID, Name: it.Name, Args: normalizeArgs(it.args())}); err != nil {
					return llm.Response{}, err
				}
			}
		case "response.completed", "response.incomplete":
			e := ev
			done = &e
		case "response.failed":
			msg, code := "response failed", ""
			if ev.Response != nil && ev.Response.Error != nil {
				msg, code = ev.Response.Error.Message, ev.Response.Error.code()
			}
			return llm.Response{}, classify(a.id, 0, nil, msg, code, key)
		case "error":
			msg, code := ev.Message, ""
			if ev.Code != nil {
				code = fmt.Sprint(ev.Code)
			}
			if ev.Error != nil {
				msg, code = ev.Error.Message, ev.Error.code()
			}
			return llm.Response{}, classify(a.id, 0, nil, msg, code, key)
		}
	}
	if err := stream.Err(); err != nil {
		if missingEndpoint(err) {
			return llm.Response{}, &errNoEndpoint{a.classifyErr(ctx, err, key)}
		}
		return llm.Response{}, a.classifyErr(ctx, err, key)
	}
	if done == nil || done.Response == nil {
		if ctx.Err() != nil {
			return llm.Response{}, ctx.Err()
		}
		return llm.Response{}, &llm.Error{Kind: llm.ErrUnavailable, Provider: a.id, Message: "stream ended before completion"}
	}
	r := done.Response
	output := r.Output
	if len(output) == 0 {
		output = items
	}
	msg := llm.Message{Role: llm.RoleAssistant}
	refused := false
	from := a.reasoningFrom(req.Model)
	query := "" // of the last tool_search_call
	for _, raw := range output {
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
			if len(sum) == 0 {
				for _, c := range it.Content {
					if c.Type == "reasoning_text" && c.Text != "" {
						sum = append(sum, c.Text)
					}
				}
			}
			msg.Parts = append(msg.Parts, llm.Part{Type: "thinking", Text: strings.Join(sum, "\n\n"),
				Meta: map[string]string{MetaReasoning: string(raw), MetaReasoningFrom: from}})
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
			tc := &llm.ToolCall{ID: it.CallID, Name: it.Name, Args: normalizeArgs(it.args())}
			p := llm.Part{Type: "tool_call", ToolCall: tc}
			if it.Namespace != "" {
				p.Meta = map[string]string{MetaNamespace: it.Namespace}
			}
			msg.Parts = append(msg.Parts, p)
			if err := emitCall(tc); err != nil {
				return llm.Response{}, err
			}
		case "tool_search_call", "tool_search_output":
			msg.Parts = append(msg.Parts, searchParts(it, raw, &query)...)
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
			if err := emitCall(&tc); err != nil {
				return llm.Response{}, err
			}
		}
	}
	var usage llm.Usage
	if u := r.Usage; u != nil {
		usage = llm.Usage{InputTokens: u.Input, OutputTokens: u.Output, CacheRead: u.Details.Cached}
	}
	reason := ""
	if r.Incomplete != nil {
		reason = r.Incomplete.Reason
	}
	stop := "end"
	switch {
	case refused || (r.Status == "incomplete" && reason == "content_filter"):
		stop = llm.StopRefusal
	case len(msg.ToolCalls()) > 0:
		stop = "tool_use"
	case r.Status == "incomplete" && reason == "max_output_tokens":
		stop = "max_tokens"
	case r.Status == "incomplete":
		return llm.Response{}, &llm.Error{Kind: llm.ErrBadRequest, Provider: a.id, Message: fmt.Sprintf("response incomplete: %s", reason)}
	}
	return llm.Response{Message: msg, Usage: estimateUsage(usage, req, msg), StopReason: stop}, nil
}
