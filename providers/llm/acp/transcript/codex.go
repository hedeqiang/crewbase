package transcript

import (
	"encoding/json"
	"os"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
)

// Codex rollout lines are {timestamp, type, payload}. The ones read here:
//
//	session_meta    payload.id (the thread id, also in the file name), cwd, cli_version;
//	                base_instructions.text = the model instructions (newer versions only);
//	                instructions = the user instructions (AGENTS.md), which are also sent
//	                as a user message, so they are not counted again
//	turn_context    payload.model of the turn
//	response_item   the history: message (user/assistant/developer), reasoning,
//	                function_call(+_output), custom_tool_call(+_output), additional_tools
//	                (dynamic tool namespaces), ...
//	compacted       payload.replacement_history replaces the history before it
//	event_msg       payload.type "token_count": info.last_token_usage is the last
//	                request's usage (input_tokens includes cached_input_tokens)
//
// The built-in tool schemas are not recorded.

type codexState struct {
	model      string
	base       string // base instructions, "" = not recorded
	compacted  *span  // the last compacted line
	items      []span // response items after it
	tools      *span  // the last additional_tools item
	usage      *llm.Usage
	at         time.Time
	idMismatch bool
}

type codexLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexPayload struct {
	Type             string `json:"type"`
	ID               string `json:"id"`
	Model            string `json:"model"`
	BaseInstructions *struct {
		Text string `json:"text"`
	} `json:"base_instructions"`
	Info *struct {
		Last *struct {
			Input      int `json:"input_tokens"`
			Cached     int `json:"cached_input_tokens"`
			CacheWrite int `json:"cache_write_input_tokens"`
			Output     int `json:"output_tokens"`
		} `json:"last_token_usage"`
	} `json:"info"`
}

func (st *codexState) line(b []byte, at span, id string) {
	var l codexLine
	if json.Unmarshal(b, &l) != nil {
		return
	}
	var p codexPayload
	switch l.Type {
	case "session_meta":
		if json.Unmarshal(l.Payload, &p) != nil {
			return
		}
		if p.ID != "" && p.ID != id {
			st.idMismatch = true // another thread's file: read nothing
		}
		if p.BaseInstructions != nil {
			st.base = p.BaseInstructions.Text
		}
	case "turn_context":
		if json.Unmarshal(l.Payload, &p) == nil && p.Model != "" {
			st.model = p.Model
		}
	case "compacted":
		c := at
		st.compacted, st.items = &c, nil
	case "response_item":
		if json.Unmarshal(l.Payload, &p) != nil {
			return
		}
		if p.Type == "additional_tools" {
			t := at
			st.tools = &t
			return
		}
		st.items = append(st.items, at)
	case "event_msg":
		if json.Unmarshal(l.Payload, &p) != nil || p.Type != "token_count" || p.Info == nil || p.Info.Last == nil {
			return
		}
		u := p.Info.Last
		st.usage = &llm.Usage{InputTokens: u.Input, OutputTokens: u.Output, CacheRead: u.Cached, CacheWrite: u.CacheWrite}
		st.at, _ = time.Parse(time.RFC3339Nano, l.Timestamp)
	}
}

// Codex reads the current context of Codex thread id from its rollout at
// path: the history since the last compaction and the last request's usage.
func (r *Reader) Codex(path, id string) (*Composition, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrNotFound
	}
	defer f.Close()
	fs := r.state("codex\x00" + path + "\x00" + id)
	fs.mu.Lock()
	defer fs.mu.Unlock()
	err = r.advance(fs, f, func() { fs.codex = &codexState{} }, func(b []byte, at span) { fs.codex.line(b, at, id) })
	if err != nil {
		return nil, err
	}
	st := fs.codex
	if st.idMismatch {
		return nil, ErrNotFound
	}
	out := &Composition{CLI: Codex, Session: id, Path: path, Model: st.model, At: st.at}
	if st.base != "" {
		out.SystemRecorded = true
		out.System = []Block{{Name: "Base instructions", Text: st.base}}
	}
	if st.usage != nil {
		out.Usage, out.Counted = *st.usage, st.usage.InputTokens > 0
	}
	if st.compacted != nil {
		if b, err := readSpan(f, *st.compacted); err == nil {
			var l codexLine
			var c struct {
				History []json.RawMessage `json:"replacement_history"`
				Message string            `json:"message"`
			}
			if json.Unmarshal(b, &l) == nil && json.Unmarshal(l.Payload, &c) == nil {
				if c.History == nil && c.Message != "" {
					out.Messages = append(out.Messages, llm.TextMessage(llm.RoleUser, c.Message))
				}
				for _, it := range c.History {
					if m, ok := codexItem(it); ok {
						out.Messages = append(out.Messages, m)
					}
				}
			}
		}
	}
	for _, sp := range st.items {
		b, err := readSpan(f, sp)
		if err != nil {
			continue
		}
		var l codexLine
		if json.Unmarshal(b, &l) != nil {
			continue
		}
		if m, ok := codexItem(l.Payload); ok {
			out.Messages = append(out.Messages, m)
		}
	}
	if st.tools != nil {
		if b, err := readSpan(f, *st.tools); err == nil {
			out.Tools = codexTools(b)
		}
	}
	return out, nil
}

// codexItem converts one response item.
func codexItem(raw json.RawMessage) (llm.Message, bool) {
	var it struct {
		Type      string          `json:"type"`
		Role      string          `json:"role"`
		Content   json.RawMessage `json:"content"`
		Name      string          `json:"name"`
		Arguments string          `json:"arguments"`
		Input     string          `json:"input"`
		CallID    string          `json:"call_id"`
		Output    json.RawMessage `json:"output"`
		Summary   []struct {
			Text string `json:"text"`
		} `json:"summary"`
		Action json.RawMessage `json:"action"`
	}
	if json.Unmarshal(raw, &it) != nil {
		return llm.Message{}, false
	}
	switch it.Type {
	case "message":
		role := llm.RoleUser
		if it.Role == "assistant" {
			role = llm.RoleAssistant
		}
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		_ = json.Unmarshal(it.Content, &parts)
		m := llm.Message{Role: role}
		for _, p := range parts {
			if p.Text != "" {
				m.Parts = append(m.Parts, llm.Part{Type: "text", Text: p.Text})
			} else if p.Type != "" {
				m.Parts = append(m.Parts, llm.Part{Type: "text", Text: "[" + p.Type + "]"})
			}
		}
		return m, len(m.Parts) > 0
	case "reasoning":
		m := llm.Message{Role: llm.RoleAssistant}
		for _, s := range it.Summary {
			m.Parts = append(m.Parts, llm.Part{Type: "thinking", Text: s.Text})
		}
		return m, len(m.Parts) > 0
	case "function_call", "custom_tool_call":
		args := it.Arguments
		if it.Type == "custom_tool_call" {
			b, _ := json.Marshal(it.Input)
			args = string(b)
		}
		if !json.Valid([]byte(args)) {
			b, _ := json.Marshal(args)
			args = string(b)
		}
		return llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: "tool_call", ToolCall: &llm.ToolCall{ID: it.CallID, Name: it.Name, Args: json.RawMessage(args)}}}}, true
	case "function_call_output", "custom_tool_call_output":
		return llm.Message{Role: llm.RoleTool, Parts: []llm.Part{{Type: "tool_result", ToolResult: &llm.ToolResult{CallID: it.CallID, Content: codexOutput(it.Output)}}}}, true
	case "":
		return llm.Message{}, false
	default:
		// local_shell_call, web_search_call, ...: the item as sent
		return llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: "text", Text: "[" + it.Type + "] " + string(it.Action)}}}, true
	}
}

// codexOutput flattens a tool output: a string, {content: string} or
// content items.
func codexOutput(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &o) == nil && len(o.Content) > 0 {
		return codexOutput(o.Content)
	}
	var items []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &items) == nil {
		out := ""
		for _, it := range items {
			if it.Text != "" {
				out += it.Text
			} else {
				out += "[" + it.Type + "]"
			}
		}
		return out
	}
	return string(raw)
}

// codexTools reads an additional_tools item: namespaces (or plain tools)
// of dynamic tools the thread was given.
func codexTools(b []byte) []Tool {
	var l codexLine
	var p struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if json.Unmarshal(b, &l) != nil || json.Unmarshal(l.Payload, &p) != nil {
		return nil
	}
	type def struct {
		Type        string            `json:"type"`
		Name        string            `json:"name"`
		Description string            `json:"description"`
		Parameters  map[string]any    `json:"parameters"`
		Tools       []json.RawMessage `json:"tools"`
	}
	var out []Tool
	for _, raw := range p.Tools {
		var d def
		if json.Unmarshal(raw, &d) != nil {
			continue
		}
		if len(d.Tools) == 0 {
			out = append(out, Tool{Name: d.Name, Group: ToolGroup(d.Name), Description: d.Description, Schema: d.Parameters, SchemaChars: len(raw)})
			continue
		}
		for _, traw := range d.Tools {
			var t def
			if json.Unmarshal(traw, &t) != nil {
				continue
			}
			out = append(out, Tool{Name: t.Name, Group: d.Name, Description: t.Description, Schema: t.Parameters, SchemaChars: len(traw)})
		}
	}
	return out
}
