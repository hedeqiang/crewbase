package acp

import (
	"os"
	"os/exec"
	"path/filepath"
)

// AgentConfig configures one ACP agent (a subscription CLI).
//
// For a built-in id (claude-code, codex, gemini-cli, cursor, opencode, grok)
// every field is optional and overrides the default. Other ids need Command.
type AgentConfig struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// Command starts the agent speaking ACP on stdio, e.g.
	// ["npx","-y","@zed-industries/claude-code-acp"].
	Command []string          `json:"command,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	// Cwd is the working directory of the process and of ACP sessions
	// (default: the Crewbase process's working directory).
	Cwd string `json:"cwd,omitempty"`
	// CLI is the underlying CLI binary (used for detection, --version and
	// "installed"); defaults per built-in, else Command[0].
	CLI string `json:"cli,omitempty"`
	// LoginCommand is shown when the CLI is not logged in.
	LoginCommand string `json:"loginCommand,omitempty"`
	// Models overrides the advertised model list (default: whatever the
	// agent reports in session/new, else a single "default" model).
	Models        []ModelConfig `json:"models,omitempty"`
	ContextWindow int           `json:"contextWindow,omitempty"`
	Disabled      bool          `json:"disabled,omitempty"`
}

// ModelConfig is a model advertised by an ACP adapter.
type ModelConfig struct {
	ID            string `json:"id"`
	Name          string `json:"name,omitempty"`
	ContextWindow int    `json:"contextWindow,omitempty"`
}

// Config is the plugin config.
//
//	{
//	  "agents": [{"id": "claude-code", "command": ["npx","-y","@zed-industries/claude-code-acp"], "cwd": "..."}],
//	  "autoApprove": false,
//	  "idleTimeoutSec": 600,
//	  "toolNotes": true,
//	  "builtins": true
//	}
type Config struct {
	Agents []AgentConfig `json:"agents"`
	// AutoApprove answers session/request_permission with the agent's
	// "allow" option. Default false: every permission request is rejected
	// (until Crewbase approvals are wired in), so CLIs run read-only-ish.
	AutoApprove bool `json:"autoApprove"`
	// IdleTimeoutSec stops an agent process after this long without a turn
	// (default 600; negative disables).
	IdleTimeoutSec int `json:"idleTimeoutSec"`
	// ToolNotes adds a short text line for every tool the CLI runs
	// (default true).
	ToolNotes *bool `json:"toolNotes,omitempty"`
	// Builtins includes the built-in agent table (default true). With false
	// only the listed agents are considered.
	Builtins *bool `json:"builtins,omitempty"`
}

// entrypoint is one way to start an agent in ACP mode.
type entrypoint struct {
	bin  string   // must be on PATH (or absolute)
	args []string // after bin
	note string
}

type builtin struct {
	id, name, cli, login string
	window               int
	entries              []entrypoint
}

// builtins lists known subscription CLIs and their ACP entrypoints, most
// preferred first. See the package doc for sources.
var builtins = []builtin{
	{id: "claude-code", name: "Claude Code", cli: "claude", login: "claude /login", window: 200000, entries: []entrypoint{
		{bin: "claude-agent-acp"},
		{bin: "claude-code-acp"},
		{bin: "npx", args: []string{"-y", "@agentclientprotocol/claude-agent-acp"}, note: "via npx (downloaded on first use)"},
	}},
	{id: "codex", name: "Codex", cli: "codex", login: "codex login", window: 272000, entries: []entrypoint{
		{bin: "codex-acp"},
		{bin: "npx", args: []string{"-y", "@zed-industries/codex-acp"}, note: "via npx (downloaded on first use)"},
	}},
	{id: "gemini-cli", name: "Gemini CLI", cli: "gemini", login: "gemini (choose \"Login with Google\")", window: 1000000, entries: []entrypoint{
		// --acp since ~0.6x; --experimental-acp is the deprecated alias
		{bin: "gemini", args: []string{"--experimental-acp"}},
	}},
	{id: "cursor", name: "Cursor CLI", cli: "cursor-agent", login: "cursor-agent login", window: 200000, entries: []entrypoint{
		{bin: "cursor-agent", args: []string{"acp"}, note: "hidden `acp` subcommand"},
	}},
	{id: "opencode", name: "OpenCode", cli: "opencode", login: "opencode auth login", window: 200000, entries: []entrypoint{
		{bin: "opencode", args: []string{"acp"}},
	}},
	{id: "grok", name: "Grok CLI", cli: "grok", login: "grok login", window: 256000, entries: []entrypoint{
		{bin: "grok", args: []string{"agent", "stdio"}, note: "unverified entrypoint"},
	}},
}

func findBuiltin(id string) (builtin, bool) {
	for _, b := range builtins {
		if b.id == id {
			return b, true
		}
	}
	return builtin{}, false
}

// lookPath is replaceable in tests.
var lookPath = exec.LookPath

// resolved is an agent config with defaults applied.
type resolved struct {
	AgentConfig
	cliPath   string // "" if the CLI is not installed
	entryNote string
}

// resolve merges cfg over the builtin defaults and resolves binaries.
func resolve(cfg AgentConfig) resolved {
	r := resolved{AgentConfig: cfg}
	b, isBuiltin := findBuiltin(cfg.ID)
	if isBuiltin {
		if r.Name == "" {
			r.Name = b.name
		}
		if r.CLI == "" {
			r.CLI = b.cli
		}
		if r.LoginCommand == "" {
			r.LoginCommand = b.login
		}
		if r.ContextWindow == 0 {
			r.ContextWindow = b.window
		}
		if len(r.Command) == 0 {
			for _, e := range b.entries {
				if p, err := lookPath(e.bin); err == nil {
					r.Command = append([]string{p}, e.args...)
					r.entryNote = e.note
					break
				}
			}
		}
	}
	if r.Name == "" {
		r.Name = r.ID
	}
	if r.CLI == "" && len(r.Command) > 0 {
		r.CLI = r.Command[0]
	}
	if r.ContextWindow == 0 {
		r.ContextWindow = 200000
	}
	if r.CLI != "" {
		if p, err := lookPath(r.CLI); err == nil {
			r.cliPath = p
		}
	}
	if len(r.Command) > 0 && !filepath.IsAbs(r.Command[0]) {
		if p, err := lookPath(r.Command[0]); err == nil {
			r.Command = append([]string{p}, r.Command[1:]...)
		} else {
			r.Command = nil // entrypoint not runnable
		}
	}
	if r.Cwd == "" {
		r.Cwd, _ = os.Getwd()
	}
	return r
}

// runnable reports whether the agent can be started.
func (r resolved) runnable() bool {
	return !r.Disabled && r.cliPath != "" && len(r.Command) > 0
}

// agentList returns configured agents merged with the builtins.
func agentList(cfg Config) []AgentConfig {
	var out []AgentConfig
	seen := map[string]bool{}
	user := map[string]AgentConfig{}
	for _, a := range cfg.Agents {
		if a.ID != "" {
			user[a.ID] = a
		}
	}
	if cfg.Builtins == nil || *cfg.Builtins {
		for _, b := range builtins {
			a, ok := user[b.id]
			if !ok {
				a = AgentConfig{ID: b.id}
			}
			out = append(out, a)
			seen[b.id] = true
		}
	}
	for _, a := range cfg.Agents {
		if a.ID != "" && !seen[a.ID] {
			out = append(out, a)
			seen[a.ID] = true
		}
	}
	return out
}
