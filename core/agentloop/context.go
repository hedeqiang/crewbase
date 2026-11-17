package agentloop

import (
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

// What a request takes of the model's context. The loop's compaction
// trigger and the context meter (features/compaction) both count it this
// way, so the meter shows what the loop acts on.

// ToolBridgeKey is the service key of the ToolBridge (features/mcpserver).
const ToolBridgeKey = "mcp-server"

// ToolBridge offers Crewbase tools to models that run their own tools
// (llm.OwnTools), e.g. Crewbase's MCP server for coding CLIs over ACP.
type ToolBridge interface {
	// BridgedSchemas are the tools agent is offered through the bridge in
	// a session of kind.
	BridgedSchemas(agent, kind string) []llm.ToolSchema
}

// chatHiddenTools are tools a blank chat is not offered. A chat's prompt
// has no plugin fragments by design (chatFragments), so it has no skills
// catalog and skill_load would name skills it was never told about.
var chatHiddenTools = map[string]bool{"skill_load": true}

// ChatTools drops the tools a blank chat is not offered; other agents get
// ts unchanged.
func ChatTools(a *agent.Agent, ts []llm.ToolSchema) []llm.ToolSchema {
	if !a.IsChat() {
		return ts
	}
	out := ts[:0:0]
	for _, t := range ts {
		if !chatHiddenTools[t.Name] {
			out = append(out, t)
		}
	}
	return out
}

// ToolSchemas are the tool schemas a's requests in a session of kind
// carry (what the loop's build sends).
func ToolSchemas(r *tools.Registry, a *agent.Agent, kind string) []llm.ToolSchema {
	return ChatTools(a, r.SchemasFor(a.ID(), kind))
}

// ContextTools are the tool definitions that take room in the model's
// context when ad serves a request carrying ts: ts itself, except for an
// adapter whose model runs its own tools (llm.OwnTools). That model never
// sees ts; it is offered only the tools bridged to it (ToolBridge, none
// without one). bridged reports which case applies.
func ContextTools(c *kernel.Context, ad llm.Adapter, a *agent.Agent, kind string, ts []llm.ToolSchema) (out []llm.ToolSchema, bridged bool) {
	if ad == nil || !llm.RunsOwnTools(ad) {
		return ts, false
	}
	if c != nil {
		if b, ok := kernel.Lookup[ToolBridge](c, ToolBridgeKey); ok {
			return ChatTools(a, b.BridgedSchemas(a.ID(), kind)), true
		}
	}
	return nil, true
}

// EstimateContext estimates the context req takes when ad serves it
// (llm.EstimateRequest with ContextTools in place of req.Tools).
func EstimateContext(c *kernel.Context, ad llm.Adapter, a *agent.Agent, kind string, req llm.Request) int {
	req.Tools, _ = ContextTools(c, ad, a, kind, req.Tools)
	return llm.EstimateRequest(req)
}

// ToolPlannerKey is the service key of the ToolPlanner (the agent loop).
const ToolPlannerKey = "agentloop-tools"

// ToolPlanner reports the tool definitions the next request of an agent in
// a session would carry (core + loaded on the generic tool-search path;
// deferred ones marked on the native path, which take no context room).
type ToolPlanner interface {
	PlannedTools(a *agent.Agent, ss *session.Session, ad llm.Adapter, model string, window int) []llm.ToolSchema
}

// PlannedTools implements ToolPlanner.
func (l *loop) PlannedTools(a *agent.Agent, ss *session.Session, ad llm.Adapter, model string, window int) []llm.ToolSchema {
	return ChatTools(a, l.planTools(a, ss, ad, model, window).Tools)
}
