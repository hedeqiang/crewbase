package agentloop

import (
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
)

// What a request takes of the model's context. The loop's compaction
// trigger and the context meter (features/compaction) both count it this
// way, so the meter shows what the loop acts on.

// ChatTools are the tools a's requests offer out of ts: none for a blank
// chat (a plain conversation), ts for agents.
func ChatTools(a *agent.Agent, ts []llm.ToolSchema) []llm.ToolSchema {
	if a.IsChat() {
		return nil
	}
	return ts
}

// ToolSchemas are the tool schemas a's requests in a session of kind
// carry (what the loop's build sends).
func ToolSchemas(r *tools.Registry, a *agent.Agent, kind string) []llm.ToolSchema {
	return ChatTools(a, r.SchemasFor(a.ID(), kind))
}

// EstimateContext estimates the context req takes (llm.EstimateRequest).
func EstimateContext(req llm.Request) int { return llm.EstimateRequest(req) }

// ToolPlannerKey is the service key of the ToolPlanner (the agent loop).
const ToolPlannerKey = "agentloop-tools"

// ToolPlanner reports the tool definitions the next request of an agent in
// a session would carry (core + loaded on the generic tool-search path;
// deferred ones marked on the native path, which take no context room).
type ToolPlanner interface {
	PlannedTools(a *agent.Agent, ss *session.Session, ad llm.Adapter, model string, window int) []llm.ToolSchema
}

// RecordedRequest is a model request as it was sent (see SentRequests).
type RecordedRequest struct {
	// System is the system prompt, block by block as sent.
	System []RecordedBlock `json:"system"`
	// Tools are the tool definitions sent.
	Tools    []llm.ToolSchema `json:"tools"`
	Messages []llm.Message    `json:"messages"`
}

// RecordedBlock is one system prompt block of a recorded request.
type RecordedBlock struct {
	Name string `json:"name,omitempty"`
	Text string `json:"text"`
}

// PlannedTools implements ToolPlanner.
func (l *loop) PlannedTools(a *agent.Agent, ss *session.Session, ad llm.Adapter, model string, window int) []llm.ToolSchema {
	return ChatTools(a, l.planTools(a, ss, ad, model, window).Tools)
}
