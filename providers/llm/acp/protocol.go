package acp

import "encoding/json"

// Wire types for the subset of the Agent Client Protocol (protocol version 1)
// that the bridge uses. See https://agentclientprotocol.com/protocol.

const protocolVersion = 1

// ACP method names.
const (
	mInitialize      = "initialize"
	mSessionNew      = "session/new"
	mSessionPrompt   = "session/prompt"
	mSessionCancel   = "session/cancel"
	mSessionUpdate   = "session/update"
	mSessionSetModel = "session/set_model" // unstable; claude-code-acp
	// session/set_config_option sets a session config option (ACP v1);
	// codex-acp >= 0.9 takes the model and the reasoning effort only here
	mSessionSetConfig  = "session/set_config_option"
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

// metaToolName reads _meta.claudeCode.toolName.
func metaToolName(meta json.RawMessage) string {
	var m struct {
		ClaudeCode struct {
			ToolName string `json:"toolName"`
		} `json:"claudeCode"`
	}
	if json.Unmarshal(meta, &m) != nil {
		return ""
	}
	return m.ClaudeCode.ToolName
}

type newSessionParams struct {
	Cwd        string         `json:"cwd"`
	MCPServers []any          `json:"mcpServers"`
	Meta       map[string]any `json:"_meta,omitempty"`
}

type acpModel struct {
	ModelID     string `json:"modelId"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type newSessionResult struct {
	SessionID string `json:"sessionId"`
	// Models is an unstable extension (claude-code-acp, codex-acp < 0.9,
	// cursor-agent, agy).
	Models *struct {
		AvailableModels []acpModel `json:"availableModels"`
		CurrentModelID  string     `json:"currentModelId"`
	} `json:"models,omitempty"`
	// ConfigOptions are the session's selectors (ACP v1): the model
	// (category "model") and the reasoning level ("thought_level") among
	// them. codex-acp 0.16 reports its models only here.
	ConfigOptions []configOption `json:"configOptions,omitempty"`
}

// configOption is a session config option. Only "select" options are read.
type configOption struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Category     string          `json:"category,omitempty"`
	Type         string          `json:"type,omitempty"`
	CurrentValue json.RawMessage `json:"currentValue,omitempty"`
	Options      json.RawMessage `json:"options,omitempty"`
}

type configSelectOption struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// choices returns the option's values, flat or grouped.
func (o configOption) choices() []configSelectOption {
	var flat []configSelectOption
	if json.Unmarshal(o.Options, &flat) == nil && (len(flat) == 0 || flat[0].Value != "") {
		return flat
	}
	var groups []struct {
		Options []configSelectOption `json:"options"`
	}
	if json.Unmarshal(o.Options, &groups) != nil {
		return nil
	}
	var out []configSelectOption
	for _, g := range groups {
		out = append(out, g.Options...)
	}
	return out
}

// current returns the option's current value ("" if not a string).
func (o configOption) current() string {
	var s string
	_ = json.Unmarshal(o.CurrentValue, &s)
	return s
}

// findOption returns the option of a category (or with id when the agent
// does not categorise), nil if absent.
func findOption(opts []configOption, category, id string) *configOption {
	for i := range opts {
		if opts[i].Category == category && (opts[i].Type == "" || opts[i].Type == "select") {
			return &opts[i]
		}
	}
	for i := range opts {
		if opts[i].ID == id && (opts[i].Type == "" || opts[i].Type == "select") {
			return &opts[i]
		}
	}
	return nil
}

type setConfigParams struct {
	SessionID string `json:"sessionId"`
	ConfigID  string `json:"configId"`
	Value     string `json:"value"`
}

type setConfigResult struct {
	ConfigOptions []configOption `json:"configOptions"`
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
	// tool_call: Claude Code puts the tool's name in _meta.claudeCode.toolName
	Meta json.RawMessage `json:"_meta,omitempty"`
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
		// files the call works on (locations, diff content, raw input)
		Locations []struct {
			Path string `json:"path"`
		} `json:"locations,omitempty"`
		Content  json.RawMessage `json:"content,omitempty"`
		RawInput json.RawMessage `json:"rawInput,omitempty"`
		Meta     json.RawMessage `json:"_meta,omitempty"`
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
