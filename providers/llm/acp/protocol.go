package acp

import "encoding/json"

// Wire types for the subset of the Agent Client Protocol (protocol version 1)
// that the bridge uses. See https://agentclientprotocol.com/protocol.

const protocolVersion = 1

// ACP method names.
const (
	mInitialize        = "initialize"
	mSessionNew        = "session/new"
	mSessionPrompt     = "session/prompt"
	mSessionCancel     = "session/cancel"
	mSessionUpdate     = "session/update"
	mSessionSetModel   = "session/set_model" // unstable; claude-code-acp
	mRequestPermission = "session/request_permission"
	mFSRead            = "fs/read_text_file"
	mFSWrite           = "fs/write_text_file"
)

// codeAuthRequired is ACP's "Authentication required" error code.
const codeAuthRequired = -32000

type implInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

type initializeParams struct {
	ProtocolVersion    int            `json:"protocolVersion"`
	ClientCapabilities map[string]any `json:"clientCapabilities"`
	ClientInfo         implInfo       `json:"clientInfo"`
}

type authMethod struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type initializeResult struct {
	ProtocolVersion   int             `json:"protocolVersion"`
	AgentCapabilities json.RawMessage `json:"agentCapabilities,omitempty"`
	AgentInfo         *implInfo       `json:"agentInfo,omitempty"`
	AuthMethods       []authMethod    `json:"authMethods,omitempty"`
}

type newSessionParams struct {
	Cwd        string `json:"cwd"`
	MCPServers []any  `json:"mcpServers"`
}

type acpModel struct {
	ModelID     string `json:"modelId"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type newSessionResult struct {
	SessionID string `json:"sessionId"`
	// Models is an unstable extension (claude-code-acp, codex-acp).
	Models *struct {
		AvailableModels []acpModel `json:"availableModels"`
		CurrentModelID  string     `json:"currentModelId"`
	} `json:"models,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type promptParams struct {
	SessionID string         `json:"sessionId"`
	Prompt    []contentBlock `json:"prompt"`
}

type promptResult struct {
	StopReason string `json:"stopReason"`
}

type sessionNotification struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update"`
}

// sessionUpdate is the union of the session/update variants we read.
type sessionUpdate struct {
	SessionUpdate string          `json:"sessionUpdate"`
	Content       json.RawMessage `json:"content,omitempty"` // ContentBlock for chunks; array for tool_call(_update)
	ToolCallID    string          `json:"toolCallId,omitempty"`
	Title         string          `json:"title,omitempty"`
	Kind          string          `json:"kind,omitempty"`
	Status        string          `json:"status,omitempty"`
	Entries       []struct {
		Content  string `json:"content"`
		Priority string `json:"priority,omitempty"`
		Status   string `json:"status,omitempty"`
	} `json:"entries,omitempty"`
	// tool_call(_update): files the call works on
	Locations []struct {
		Path string `json:"path"`
		Line int    `json:"line,omitempty"`
	} `json:"locations,omitempty"`
	// usage_update
	Used int `json:"used,omitempty"`
	Size int `json:"size,omitempty"`
}

type permissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"` // allow_once | allow_always | reject_once | reject_always
}

type permissionParams struct {
	SessionID string `json:"sessionId"`
	ToolCall  struct {
		ToolCallID string `json:"toolCallId"`
		Title      string `json:"title,omitempty"`
		Kind       string `json:"kind,omitempty"`
	} `json:"toolCall"`
	Options []permissionOption `json:"options"`
}

type permissionOutcome struct {
	Outcome  string `json:"outcome"` // selected | cancelled
	OptionID string `json:"optionId,omitempty"`
}

type permissionResult struct {
	Outcome permissionOutcome `json:"outcome"`
}

type readParams struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Line      int    `json:"line,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type writeParams struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Content   string `json:"content"`
}
