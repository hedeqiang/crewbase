package session

import (
	"github.com/hedeqiang/crewbase/core/llm"
)

// Payloads of well-known events.

type UserMessage struct {
	Text string `json:"text"`
	From string `json:"from,omitempty"` // human member name
	// To addresses a specific agent in a channel (@mention); "" = owner.
	To string `json:"to,omitempty"`
}

type InjectData struct {
	Text   string `json:"text"`
	Source string `json:"source,omitempty"` // plugin id that injected it
}

type AssistantMessage struct {
	Message llm.Message  `json:"message"`
	Model   llm.ModelRef `json:"model"`
	Usage   llm.Usage    `json:"usage"`
	Stop    string       `json:"stop,omitempty"`
}

type ToolCallData struct {
	Call llm.ToolCall `json:"call"`
}

// PrunedData replaces an earlier tool result's model-visible content
// (e.g. a huge log cut down to head/tail); the original stays in the log
// and can be read back with history_read.
type PrunedData struct {
	CallID  string `json:"callId"`
	Content string `json:"content"`
}

type CompactionData struct {
	Summary string `json:"summary"`
	// UptoSeq: every model-visible event with Seq <= UptoSeq is replaced by
	// the summary.
	UptoSeq int64 `json:"uptoSeq"`
	Before  int   `json:"tokensBefore"`
	After   int   `json:"tokensAfter"`
}

type ModelSwitchData struct {
	From   llm.ModelRef `json:"from"`
	To     llm.ModelRef `json:"to"`
	Reason string       `json:"reason"`
	Detail string       `json:"detail,omitempty"`
}

// IsolatedKinds are session kinds whose agents do not share history.
var IsolatedKinds = map[string]bool{"workflow": true}

// InterruptedResult is the content synthesised for a tool call that has no
// recorded result (e.g. the process crashed mid-execution).
const InterruptedResult = "interrupted: the tool call did not complete (process stopped). Re-run it if still needed."

// DeriveMessages projects the model history for agent from events. Other
// agents' replies in a shared channel appear as attributed user messages.
// Tool calls without results are closed with InterruptedResult, so callers
// must only build requests once every call of the last step has a result.
func DeriveMessages(events []Event, agent string, names map[string]string) []llm.Message {
	// find the latest compaction for this agent
	var comp *CompactionData
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == TypeCompaction && (events[i].Agent == agent || events[i].Agent == "") {
			var c CompactionData
			if events[i].Decode(&c) == nil {
				comp = &c
			}
			break
		}
	}
	// Isolated sessions (workflow runs, subagent tasks): each agent sees
	// only what was addressed to it and its own turns; data flows between
	// steps explicitly (templates), not through shared history.
	isolated := false
	if len(events) > 0 && events[0].Type == TypeCreated {
		var m Meta
		if events[0].Decode(&m) == nil {
			isolated = IsolatedKinds[m.Kind]
		}
	}
	var msgs []llm.Message
	if comp != nil {
		msgs = append(msgs, llm.TextMessage(llm.RoleUser, "[Summary of earlier conversation]\n"+comp.Summary))
	}
	results := map[string]llm.ToolResult{}
	for _, e := range events {
		if e.Type == TypeToolResult && e.Agent == agent {
			var r llm.ToolResult
			if e.Decode(&r) == nil {
				results[r.CallID] = r
			}
		}
	}
	for _, e := range events {
		if e.Type == TypeToolPruned && e.Agent == agent {
			var p PrunedData
			if e.Decode(&p) == nil {
				if r, ok := results[p.CallID]; ok {
					r.Content = p.Content
					results[p.CallID] = r
				}
			}
		}
	}
	appendUser := func(text string) {
		if n := len(msgs); n > 0 && msgs[n-1].Role == llm.RoleUser && len(msgs[n-1].Parts) > 0 && msgs[n-1].Parts[0].Type == "text" {
			msgs[n-1].Parts = append(msgs[n-1].Parts, llm.Part{Type: "text", Text: "\n\n" + text})
			return
		}
		msgs = append(msgs, llm.TextMessage(llm.RoleUser, text))
	}
	for _, e := range events {
		if comp != nil && e.Seq <= comp.UptoSeq {
			continue
		}
		switch e.Type {
		case TypeUserMessage:
			var m UserMessage
			if e.Decode(&m) != nil {
				continue
			}
			if isolated && m.To != "" && m.To != agent {
				continue
			}
			if m.From != "" {
				appendUser("[" + m.From + "]: " + m.Text)
			} else {
				appendUser(m.Text)
			}
		case TypeInject:
			if e.Agent != "" && e.Agent != agent {
				continue
			}
			var m InjectData
			if e.Decode(&m) == nil {
				appendUser(m.Text)
			}
		case TypeAssistantMessage:
			var m AssistantMessage
			if e.Decode(&m) != nil {
				continue
			}
			if e.Agent != agent {
				if isolated {
					continue
				}
				if t := m.Message.Text(); t != "" {
					name := names[e.Agent]
					if name == "" {
						name = e.Agent
					}
					appendUser("[" + name + "]: " + t)
				}
				continue
			}
			msg := llm.Message{Role: llm.RoleAssistant}
			for _, p := range m.Message.Parts {
				// Plain thinking text is not sent back; thinking that carries
				// provider data (signatures) must round-trip to that provider,
				// which skips parts it does not recognise.
				if p.Type == "thinking" && len(p.Meta) == 0 {
					continue
				}
				msg.Parts = append(msg.Parts, p)
			}
			if len(msg.Parts) == 0 {
				continue
			}
			msgs = append(msgs, msg)
			// Results follow in call order; calls that never got a result
			// are closed as interrupted.
			for _, tc := range msg.ToolCalls() {
				r, ok := results[tc.ID]
				if !ok {
					r = llm.ToolResult{CallID: tc.ID, Name: tc.Name, Content: InterruptedResult, IsError: true}
				}
				msgs = append(msgs, toolMsg(r))
			}
		}
	}
	return msgs
}

func toolMsg(r llm.ToolResult) llm.Message {
	return llm.Message{Role: llm.RoleTool, Parts: []llm.Part{{Type: "tool_result", ToolResult: &r}}}
}

// PendingCalls returns tool calls recorded (tool/call) without a result.
func PendingCalls(events []Event, agent string) []llm.ToolCall {
	done := map[string]bool{}
	for _, e := range events {
		if e.Type == TypeToolResult {
			var r llm.ToolResult
			if e.Decode(&r) == nil {
				done[r.CallID] = true
			}
		}
	}
	var out []llm.ToolCall
	for _, e := range events {
		if e.Type == TypeToolCall && (agent == "" || e.Agent == agent) {
			var c ToolCallData
			if e.Decode(&c) == nil && !done[c.Call.ID] {
				out = append(out, c.Call)
			}
		}
	}
	return out
}
