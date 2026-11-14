// Package bundles declares the builtin bundles and profiles, and imports
// every builtin plugin so the kernel catalog is populated. Config strings may
// use ${root} (the data directory, default ~/.crewbase).
package bundles

import (
	"runtime"

	"github.com/hedeqiang/crewbase/kernel"

	_ "github.com/hedeqiang/crewbase/capabilities/approval"
	_ "github.com/hedeqiang/crewbase/capabilities/credentials"
	_ "github.com/hedeqiang/crewbase/capabilities/notify"
	_ "github.com/hedeqiang/crewbase/core/agent"
	_ "github.com/hedeqiang/crewbase/core/agentloop"
	_ "github.com/hedeqiang/crewbase/core/llm"
	_ "github.com/hedeqiang/crewbase/core/prompt"
	_ "github.com/hedeqiang/crewbase/core/session"
	_ "github.com/hedeqiang/crewbase/core/tools"
	_ "github.com/hedeqiang/crewbase/features/alerts"
	_ "github.com/hedeqiang/crewbase/features/api"
	_ "github.com/hedeqiang/crewbase/features/channel"
	_ "github.com/hedeqiang/crewbase/features/compaction"
	_ "github.com/hedeqiang/crewbase/features/gateway"
	_ "github.com/hedeqiang/crewbase/features/memory"
	_ "github.com/hedeqiang/crewbase/features/redact"
	_ "github.com/hedeqiang/crewbase/features/router"
	_ "github.com/hedeqiang/crewbase/features/routines"
	_ "github.com/hedeqiang/crewbase/features/rules"
	_ "github.com/hedeqiang/crewbase/features/runmode"
	_ "github.com/hedeqiang/crewbase/features/schedule"
	_ "github.com/hedeqiang/crewbase/features/skills"
	_ "github.com/hedeqiang/crewbase/features/toolsbasic"
	_ "github.com/hedeqiang/crewbase/features/usage"
	_ "github.com/hedeqiang/crewbase/providers/fs/local"
	_ "github.com/hedeqiang/crewbase/providers/llm/acp"
	_ "github.com/hedeqiang/crewbase/providers/llm/anthropic"
	_ "github.com/hedeqiang/crewbase/providers/llm/gemini"
	_ "github.com/hedeqiang/crewbase/providers/llm/local"
	_ "github.com/hedeqiang/crewbase/providers/llm/magpie"
	_ "github.com/hedeqiang/crewbase/providers/llm/mock"
	_ "github.com/hedeqiang/crewbase/providers/llm/openai"
	_ "github.com/hedeqiang/crewbase/providers/sandbox/docker"
	_ "github.com/hedeqiang/crewbase/providers/sandbox/none"
	_ "github.com/hedeqiang/crewbase/providers/sandbox/seatbelt"
	_ "github.com/hedeqiang/crewbase/providers/shell/local"
)

// Base is shared by every profile: model adapters, tools, persistence,
// approval, agents and the default loop.
var Base = kernel.Bundle{Name: "base", Entries: []kernel.Entry{
	{ID: "session", Name: "@crewbase/session", Config: map[string]any{"dir": "${root}/sessions"}},
	{ID: "prompt", Name: "@crewbase/prompt"},
	{ID: "tools", Name: "@crewbase/tools", Config: map[string]any{"requirePolicy": "rules"}},
	{ID: "approval", Name: "@crewbase/approval"},
	{ID: "rules", Name: "@crewbase/rules", Config: map[string]any{"dir": "${root}"}},
	{ID: "credentials", Name: "@crewbase/credentials"},
	{ID: "redact", Name: "@crewbase/redact"},
	{ID: "llm", Name: "@crewbase/llm"},
	// Subscriptions first: logged-in CLIs over ACP, then local models.
	// API providers (llm-openai / llm-anthropic / llm-gemini) are added by
	// the user ("add provider"), which writes a patch entry.
	{ID: "llm-acp", Name: "@crewbase/llm-acp"},
	{ID: "llm-local", Name: "@crewbase/llm-local"},
	// magpie gateway, registered only while magpie runs (github.com/yetone/magpie).
	{ID: "llm-magpie", Name: "@crewbase/llm-magpie"},
	// Deterministic mock for demos and tests; enable with a patch:
	//   - id: llm-mock
	//     disabled: false
	{ID: "llm-mock", Name: "@crewbase/llm-mock", Config: map[string]any{"ids": []string{"mock"}}, Disabled: true},
	{ID: "router", Name: "@crewbase/router-default"},
	{ID: "agents", Name: "@crewbase/agents", Config: map[string]any{"root": "${root}", "defaultModels": []string{"acp-claude-code/default", "acp-codex/default", "acp-gemini-cli/default"}}},
	{ID: "agent-loop", Name: "@crewbase/agent-loop"},
	{ID: "channel", Name: "@crewbase/channel-local"},
	{ID: "sandbox", Name: defaultSandbox()},
	{ID: "shell", Name: "@crewbase/shell-local"},
	{ID: "fs", Name: "@crewbase/fs-local"},
	{ID: "tools-basic", Name: "@crewbase/tools-basic"},
	{ID: "runmode", Name: "@crewbase/runmode"},
	{ID: "notify", Name: "@crewbase/notify"},
	{ID: "schedule", Name: "@crewbase/schedule", Config: map[string]any{"dir": "${root}"}},
	{ID: "routines", Name: "@crewbase/routines", Config: map[string]any{"dir": "${root}"}},
	{ID: "alerts", Name: "@crewbase/alerts"},
	{ID: "skills", Name: "@crewbase/skills", Config: map[string]any{"dirs": []string{"${root}/skills"}}},
	{ID: "compaction", Name: "@crewbase/compaction-summary"},
	{ID: "memory", Name: "@crewbase/memory-markdown-sqlite", Config: map[string]any{"root": "${root}"}},
}}

// Desktop adds the local gateway, API and desktop approval cards.
var Desktop = kernel.Bundle{Name: "desktop", Entries: []kernel.Entry{
	{ID: "approval-desktop", Name: "@crewbase/approval-desktop"},
	{ID: "gateway", Name: "@crewbase/gateway", Config: map[string]any{"addr": "127.0.0.1:7788", "tokenFile": "${root}/token"}},
	{ID: "api", Name: "@crewbase/api"},
	{ID: "channel-api", Name: "@crewbase/channel-api"},
	{ID: "webhook", Name: "@crewbase/webhook"},
	{ID: "routines-api", Name: "@crewbase/routines-api"},
	{ID: "usage", Name: "@crewbase/usage"},
}}

// Server is headless: same API bound to localhost (remote access goes
// through outbound relays, never inbound ports).
var Server = kernel.Bundle{Name: "server", Entries: []kernel.Entry{
	{ID: "approval-desktop", Name: "@crewbase/approval-desktop"},
	{ID: "gateway", Name: "@crewbase/gateway", Config: map[string]any{"addr": "127.0.0.1:7788", "tokenFile": "${root}/token"}},
	{ID: "api", Name: "@crewbase/api"},
	{ID: "channel-api", Name: "@crewbase/channel-api"},
}}

// defaultSandbox picks the sandbox provider for this platform: Seatbelt on
// macOS, Docker elsewhere. Switch with a patch:
//   - id: sandbox
//     name: '@crewbase/sandbox-docker'
func defaultSandbox() string {
	if runtime.GOOS == "darwin" {
		return "@crewbase/sandbox-seatbelt"
	}
	return "@crewbase/sandbox-docker"
}

// extra bundles are appended to profiles by later packages (AddToProfiles).
var extraBase []kernel.Entry

// AddToBase lets feature packages append entries to the base bundle before
// Init runs (called from init functions).
func AddToBase(e ...kernel.Entry) { extraBase = append(extraBase, e...) }

// Init registers bundles and profiles. Call once at startup.
func Init() {
	b := Base
	b.Entries = append(append([]kernel.Entry(nil), Base.Entries...), extraBase...)
	kernel.RegisterBundle(b)
	kernel.RegisterBundle(Desktop)
	kernel.RegisterBundle(Server)
	kernel.RegisterProfile(kernel.Profile{Name: "desktop", Bundles: []string{"base", "desktop"}})
	kernel.RegisterProfile(kernel.Profile{Name: "server", Bundles: []string{"base", "server"}})
	kernel.RegisterProfile(kernel.Profile{Name: "sdk", Bundles: []string{"base"}})
}
