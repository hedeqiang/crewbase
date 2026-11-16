package acp

import (
	"encoding/json"
	"path/filepath"

	"github.com/hedeqiang/crewbase/core/agent"
)

// Project-level CLI config in an agent's own workspace.
//
// The CLIs read configuration from their session directory: Claude Code
// .claude/settings[.local].json (permission allow-lists, defaultMode, hooks,
// env, MCP servers) and .mcp.json, Gemini .gemini/settings.json, Cursor
// .cursor/, OpenCode opencode.json and .opencode/ (plugins run in-process),
// Grok .grok/. Hooks and MCP servers run as children of the CLI, which
// Crewbase does not sandbox, and allow-listed tools never reach
// session/request_permission. In an agent's own workspace (not a folder the
// person chose, llm.Request.OwnWorkspace) everything there was written by
// an agent, so the bridge:
//
//   - moves such entries aside before every turn (agent.QuarantineCLIConfig)
//     and starts a fresh ACP session when it moved any;
//   - starts Claude Code sessions with settingSources ["user"] (the person's
//     ~/.claude settings still apply; project and local ones do not) and
//     without the bypassPermissions mode, via the bridge's documented
//     _meta.claudeCode.options, spread over the bridge's default
//     settingSources ["user","project","local"] (checked in claude-agent-acp
//     v0.40.0 and v0.107.1; v0.10.0 ignores it, so old bridges rely on the
//     quarantine). The bridge still reads permissions.defaultMode from
//     settings.local.json itself, which the quarantine covers;
//   - refuses fs/write_text_file to those paths and rejects, without asking,
//     a CLI permission request for a tool call that would change them.
//
// In a project folder the person chose, its config is the person's and is
// honoured; agents in sandbox mode cannot write it there either (fs tools
// and the command sandbox keep agent.CLIConfigNames read-only).

// claudeCodeID is the builtin id of Claude Code.
const claudeCodeID = "claude-code"

// sessionMeta is the session/new _meta for an agent: nil unless the agent
// is Claude Code in an agent's own workspace.
func sessionMeta(agentID string, own bool) map[string]any {
	if !own || agentID != claudeCodeID {
		return nil
	}
	return map[string]any{"claudeCode": map[string]any{"options": map[string]any{
		"settingSources":                  []string{"user"},
		"allowDangerouslySkipPermissions": false,
	}}}
}

// rawInputPaths reads file paths from a tool call's raw input (Claude Code:
// file_path / notebook_path; others: path).
func rawInputPaths(raw json.RawMessage) []string {
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil {
		return nil
	}
	var out []string
	for _, k := range []string{"file_path", "notebook_path", "path", "filePath"} {
		if s, ok := in[k].(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// touchesCLIConfig returns the first of paths (absolute, or relative to
// cwd) that is CLI config in cwd, or "".
func touchesCLIConfig(cwd string, paths []string) string {
	for _, p := range paths {
		if p == "" {
			continue
		}
		abs := p
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(cwd, abs)
		}
		if agent.IsCLIConfigPath(cwd, abs) {
			return p
		}
	}
	return ""
}

// readOnlyKind reports ACP tool kinds that do not change files.
func readOnlyKind(kind string) bool {
	switch kind {
	case "read", "search", "fetch", "think":
		return true
	}
	return false
}
