package acp

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
)

// Crewbase's own tools for the CLI (features/mcpserver).
//
// When an ACP session is opened for an agent in a Crewbase session, the
// adapter asks Options.MCP for a grant and passes Crewbase's MCP server to
// the CLI in session/new:
//
//	{"type": "http", "name": "crewbase", "url": "http://127.0.0.1:<port>/mcp",
//	 "headers": [{"name": "Authorization", "value": "Bearer <token>"}]}
//
// only if the agent advertised agentCapabilities.mcpCapabilities.http in
// initialize (otherwise it is skipped and logged once per process). The
// token is bound to the agent and session; each prompt turn activates it
// with the turn's run mode and context (Grant.Turn), so cancelling the
// turn cancels in-flight MCP calls. The grant is revoked when the ACP
// session is dropped (new session, conversation evicted, process exit,
// adapter close). The token never appears in logs or traces.
//
// Claude Code passes mcpServers from session/new to the SDK merged after
// the _meta.claudeCode.options spread (claude-agent-acp), so the
// settingSources option of an agent's own workspace (cliconfig.go) does not
// drop it: settingSources only selects file-based settings.
//
// Double approval: CLIs ask session/request_permission before running an
// MCP tool. For our crewbase tools that request is allowed without asking
// the person (allow_once), because the call itself goes through Crewbase's
// tool pipeline, which applies the rules and asks the person when needed.
// A request is ours only when its kind is "other" (or absent) and its
// tool name (_meta.claudeCode.toolName, else the title) is exactly one of
// the forms below naming a tool the grant offers; titles containing
// anything else (e.g. a shell command mentioning the name) do not match.
//
//	mcp__crewbase__<tool>          Claude Code (claude-agent-acp: title and
//	                               _meta.claudeCode.toolName)
//	crewbase: <tool>               Cursor (cursor-agent: "<server>: <tool>")
//	crewbase_<tool>                OpenCode (title = its tool name)
//	<tool> (crewbase MCP Server)   Gemini-style (Antigravity, unverified)
//	Tool: crewbase/<tool>, crewbase/<tool>, crewbase.<tool>, crewbase__<tool>
//	Approve <tool>                 codex-acp, only with a text content line
//	                               exactly "Server: crewbase"
//
// Anything else is asked as before (both Crewbase's pipeline and the CLI's
// request then ask: safe, if repetitive).

// MCPBinding is what a grant acts as.
type MCPBinding struct {
	Agent, Session, Workspace, Mode string
	Own                             bool
}

// MCPGrant is one ACP session's access to Crewbase's MCP server
// (*mcpserver.Grant).
type MCPGrant interface {
	// Server is the name, URL and bearer token given to the CLI.
	Server() (name, url, token string)
	// Turn enables calls while ctx lives with run mode mode; end disables
	// them and cancels calls still running.
	Turn(ctx context.Context, mode string) (end func())
	// Allows reports whether tool is offered to this session now.
	Allows(tool string) bool
	// Stale reports that the offered tools changed since the CLI listed
	// them (a new ACP session lists them again).
	Stale() bool
	// Revoke invalidates the token. Idempotent.
	Revoke()
}

type httpHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// mcpServerHTTP is an ACP McpServer of type http.
type mcpServerHTTP struct {
	Type    string       `json:"type"`
	Name    string       `json:"name"`
	URL     string       `json:"url"`
	Headers []httpHeader `json:"headers"`
}

// httpMCP reports whether the agent advertised mcpCapabilities.http.
func httpMCP(caps json.RawMessage) bool {
	var c struct {
		MCPCapabilities struct {
			HTTP bool `json:"http"`
		} `json:"mcpCapabilities"`
	}
	return json.Unmarshal(caps, &c) == nil && c.MCPCapabilities.HTTP
}

// crewbaseToolName returns the crewbase tool a permission request is for,
// or "" (see the package comment above for the accepted forms).
func crewbaseToolName(kind, title, metaTool string, content json.RawMessage) string {
	if kind != "" && kind != "other" {
		return ""
	}
	for _, s := range []string{metaTool, title} {
		if t := matchCrewbase(strings.TrimSpace(s)); t != "" {
			return t
		}
	}
	// codex-acp: "Approve <tool title>", the server in the content
	if m := reCodexApprove.FindStringSubmatch(title); m != nil && contentHasLine(content, "Server: "+ServerName) {
		return m[1]
	}
	return ""
}

// ServerName is the MCP server's name in session/new.
const ServerName = "crewbase"

var reCodexApprove = regexp.MustCompile(`^Approve ([A-Za-z0-9_\-]+)$`)

// contentHasLine reports whether a text block of ACP tool call content has
// line as one of its lines.
func contentHasLine(raw json.RawMessage, line string) bool {
	var items []struct {
		Type    string `json:"type"`
		Content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &items) != nil {
		return false
	}
	for _, it := range items {
		if it.Type != "content" || it.Content.Type != "text" {
			continue
		}
		for _, l := range strings.Split(it.Content.Text, "\n") {
			if strings.TrimSpace(l) == line {
				return true
			}
		}
	}
	return false
}

var (
	reToolName  = `([A-Za-z0-9_\-]+)`
	reCrewbaseT = []*regexp.Regexp{
		regexp.MustCompile(`^mcp__crewbase__` + reToolName + `$`),
		regexp.MustCompile(`^crewbase__` + reToolName + `$`),
		regexp.MustCompile(`^crewbase: ` + reToolName + `$`),
		regexp.MustCompile(`^crewbase_` + reToolName + `$`),
		regexp.MustCompile(`^(?:Tool: )?crewbase[/.]` + reToolName + `$`),
		regexp.MustCompile(`^` + reToolName + ` \(crewbase MCP Server\)$`),
	}
)

func matchCrewbase(s string) string {
	if s == "" {
		return ""
	}
	for _, re := range reCrewbaseT {
		if m := re.FindStringSubmatch(s); m != nil {
			return m[1]
		}
	}
	return ""
}

// reBearer finds bearer tokens in traced JSON-RPC traffic.
var reBearer = regexp.MustCompile(`Bearer [^"\\]+`)

func redactBearer(msg []byte) []byte {
	return reBearer.ReplaceAll(msg, []byte("Bearer [redacted]"))
}
