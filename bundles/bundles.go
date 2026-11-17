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
	_ "github.com/hedeqiang/crewbase/capabilities/subagent"
	_ "github.com/hedeqiang/crewbase/capabilities/ui"
	_ "github.com/hedeqiang/crewbase/core/agent"
	_ "github.com/hedeqiang/crewbase/core/agentloop"
	_ "github.com/hedeqiang/crewbase/core/llm"
	_ "github.com/hedeqiang/crewbase/core/prompt"
	_ "github.com/hedeqiang/crewbase/core/session"
	_ "github.com/hedeqiang/crewbase/core/tools"
	_ "github.com/hedeqiang/crewbase/features/alerts"
	_ "github.com/hedeqiang/crewbase/features/api"
	_ "github.com/hedeqiang/crewbase/features/attachments"
	_ "github.com/hedeqiang/crewbase/features/audit"
	_ "github.com/hedeqiang/crewbase/features/browsertools"
	_ "github.com/hedeqiang/crewbase/features/catalog"
	_ "github.com/hedeqiang/crewbase/features/channel"
	_ "github.com/hedeqiang/crewbase/features/compaction"
	_ "github.com/hedeqiang/crewbase/features/computertools"
	_ "github.com/hedeqiang/crewbase/features/console"
	_ "github.com/hedeqiang/crewbase/features/deepseek"
	_ "github.com/hedeqiang/crewbase/features/devices"
	_ "github.com/hedeqiang/crewbase/features/followups"
	_ "github.com/hedeqiang/crewbase/features/gateway"
	_ "github.com/hedeqiang/crewbase/features/goals"
	_ "github.com/hedeqiang/crewbase/features/google"
	_ "github.com/hedeqiang/crewbase/features/im"
	_ "github.com/hedeqiang/crewbase/features/imconfig"
	_ "github.com/hedeqiang/crewbase/features/mcp"
	_ "github.com/hedeqiang/crewbase/features/media"
	_ "github.com/hedeqiang/crewbase/features/members"
	_ "github.com/hedeqiang/crewbase/features/memory"
	_ "github.com/hedeqiang/crewbase/features/modelcatalog"
	_ "github.com/hedeqiang/crewbase/features/plugins"
	_ "github.com/hedeqiang/crewbase/features/providers"
	_ "github.com/hedeqiang/crewbase/features/redact"
	_ "github.com/hedeqiang/crewbase/features/relay"
	_ "github.com/hedeqiang/crewbase/features/roles"
	_ "github.com/hedeqiang/crewbase/features/router"
	_ "github.com/hedeqiang/crewbase/features/routines"
	_ "github.com/hedeqiang/crewbase/features/rules"
	_ "github.com/hedeqiang/crewbase/features/runmode"
	_ "github.com/hedeqiang/crewbase/features/schedule"
	_ "github.com/hedeqiang/crewbase/features/skills"
	_ "github.com/hedeqiang/crewbase/features/subagent"
	_ "github.com/hedeqiang/crewbase/features/taskboard"
	_ "github.com/hedeqiang/crewbase/features/teams"
	_ "github.com/hedeqiang/crewbase/features/telemetry"
	_ "github.com/hedeqiang/crewbase/features/toolsbasic"
	_ "github.com/hedeqiang/crewbase/features/topics"
	_ "github.com/hedeqiang/crewbase/features/usage"
	_ "github.com/hedeqiang/crewbase/features/workflows"
	_ "github.com/hedeqiang/crewbase/providers/browser/chrome"
	_ "github.com/hedeqiang/crewbase/providers/computer/macos"
	_ "github.com/hedeqiang/crewbase/providers/fs/local"
	_ "github.com/hedeqiang/crewbase/providers/im/discord"
	_ "github.com/hedeqiang/crewbase/providers/im/feishu"
	_ "github.com/hedeqiang/crewbase/providers/im/slack"
	_ "github.com/hedeqiang/crewbase/providers/im/telegram"
	_ "github.com/hedeqiang/crewbase/providers/llm/anthropic"
	_ "github.com/hedeqiang/crewbase/providers/llm/gemini"
	_ "github.com/hedeqiang/crewbase/providers/llm/local"
	_ "github.com/hedeqiang/crewbase/providers/llm/magpie"
	_ "github.com/hedeqiang/crewbase/providers/llm/mock"
	_ "github.com/hedeqiang/crewbase/providers/llm/openai"
	_ "github.com/hedeqiang/crewbase/providers/sandbox/bubblewrap"
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
	{ID: "credentials", Name: "@crewbase/credentials", Config: map[string]any{"root": "${root}"}},
	{ID: "redact", Name: "@crewbase/redact"},
	{ID: "llm", Name: "@crewbase/llm"},
	// Local models; API providers (llm-openai / llm-anthropic / llm-gemini)
	// are added by the user ("add provider"), which writes a patch entry.
	{ID: "llm-local", Name: "@crewbase/llm-local"},
	// magpie gateway, registered only while magpie runs (github.com/yetone/magpie).
	{ID: "llm-magpie", Name: "@crewbase/llm-magpie", Config: map[string]any{"stateDir": "${root}"}},
	// Model metadata (context window, prices) from OpenRouter + LiteLLM:
	// embedded snapshot, cached and refreshed daily under ${root}.
	{ID: "model-catalog", Name: "@crewbase/model-catalog", Config: map[string]any{"dir": "${root}"}},
	// API providers added from the UI (keys in the keychain), hot-applied.
	{ID: "providers", Name: "@crewbase/providers", Config: map[string]any{"dir": "${root}"}},
	// Deterministic mock for demos and tests; enable with a patch:
	//   - id: llm-mock
	//     disabled: false
	{ID: "llm-mock", Name: "@crewbase/llm-mock", Config: map[string]any{"ids": []string{"mock"}}, Disabled: true},
	// Cheap chores go to DeepSeek Flash when it is reachable (directly or
	// through magpie); otherwise the agent's own model order applies.
	{ID: "router", Name: "@crewbase/router-default", Config: map[string]any{"routes": cheapRoutes()}},
	{ID: "agents", Name: "@crewbase/agents", Config: map[string]any{"root": "${root}"}}, // default models: the best of what is connected
	{ID: "agent-loop", Name: "@crewbase/agent-loop"},
	{ID: "channel", Name: "@crewbase/channel-local"},
	{ID: "sandbox", Name: defaultSandbox()},
	{ID: "shell", Name: "@crewbase/shell-local"},
	{ID: "fs", Name: "@crewbase/fs-local"},
	{ID: "tools-basic", Name: "@crewbase/tools-basic"},
	{ID: "runmode", Name: "@crewbase/runmode"},
	{ID: "notify", Name: "@crewbase/notify"},
	{ID: "ui", Name: "@crewbase/ui"},
	{ID: "attachments", Name: "@crewbase/attachments"},
	// DeepSeek Search / Fetch / OCR for any model, once a DeepSeek key exists.
	{ID: "deepseek-tools", Name: "@crewbase/deepseek-tools"},
	// image_generate / image_edit / video_generate, once an image or video
	// model exists (OpenAI, Gemini, magpie).
	{ID: "media", Name: "@crewbase/media"},
	{ID: "schedule", Name: "@crewbase/schedule", Config: map[string]any{"dir": "${root}"}},
	{ID: "routines", Name: "@crewbase/routines", Config: map[string]any{"dir": "${root}"}},
	{ID: "followups", Name: "@crewbase/followups"},
	{ID: "alerts", Name: "@crewbase/alerts"},
	{ID: "topics", Name: "@crewbase/topics"},
	{ID: "audit", Name: "@crewbase/audit", Config: map[string]any{"dir": "${root}"}},
	{ID: "goals", Name: "@crewbase/goals"},
	{ID: "taskboard", Name: "@crewbase/taskboard"},
	// OpenTelemetry export is off by default (no telemetry leaves the machine).
	{ID: "otel", Name: "@crewbase/otel", Config: map[string]any{"endpoint": "http://127.0.0.1:4318"}, Disabled: true},
	// IM gateways: outbound connections only (no public webhook). Enable
	// with a patch once channel and allowUsers are set; tokens live in the
	// keychain under these refs.
	{ID: "im-slack", Name: "@crewbase/im-slack", Config: map[string]any{"stateDir": "${root}", "botTokenRef": "keychain:crewbase/slack-bot", "appTokenRef": "keychain:crewbase/slack-app"}, Disabled: true},
	{ID: "im-telegram", Name: "@crewbase/im-telegram", Config: map[string]any{"stateDir": "${root}", "botTokenRef": "keychain:crewbase/telegram-bot"}, Disabled: true},
	{ID: "im-discord", Name: "@crewbase/im-discord", Config: map[string]any{"stateDir": "${root}", "botTokenRef": "keychain:crewbase/discord-bot"}, Disabled: true},
	{ID: "im-feishu", Name: "@crewbase/im-feishu", Config: map[string]any{"stateDir": "${root}", "botTokenRef": "keychain:crewbase/feishu-app"}, Disabled: true},
	{ID: "subagent", Name: "@crewbase/subagent", Config: map[string]any{"maxDepth": 2, "maxConcurrent": 3}},
	{ID: "subagent-runner", Name: "@crewbase/subagent-runner"},
	{ID: "subagent-local", Name: "@crewbase/subagent-local"},
	{ID: "subagent-fork", Name: "@crewbase/subagent-fork"},
	{ID: "delegate", Name: "@crewbase/delegate"},
	{ID: "workflows", Name: "@crewbase/workflows", Config: map[string]any{"dir": "${root}"}},
	{ID: "browser", Name: "@crewbase/browser-chrome"},
	{ID: "browser-tools", Name: "@crewbase/browser-tools"},
	{ID: "mcp", Name: "@crewbase/mcp", Config: map[string]any{"dir": "${root}"}},
	{ID: "skills", Name: "@crewbase/skills", Config: map[string]any{"dirs": []string{"${root}/skills"}}},
	{ID: "catalog", Name: "@crewbase/catalog"},
	// Gmail / Calendar / Drive / Docs / Sheets / Slides with Google sign-in.
	// OAuth client: config clientId / clientSecret, else env
	// CREWBASE_GOOGLE_CLIENT_ID / _SECRET, else the build defaults.
	{ID: "google", Name: "@crewbase/google", Config: map[string]any{"dir": "${root}"}},
	{ID: "compaction", Name: "@crewbase/compaction-summary"},
	{ID: "memory", Name: "@crewbase/memory-markdown-sqlite", Config: map[string]any{"root": "${root}"}},
	// Nightly: agents curate an overgrown MEMORY.md (backed up first).
	{ID: "memory-tidy", Name: "@crewbase/memory-tidy"},
	{ID: "memory-working", Name: "@crewbase/memory-working"},
}}

// Desktop adds the local gateway, API and desktop approval cards.
var Desktop = kernel.Bundle{Name: "desktop", Entries: []kernel.Entry{
	{ID: "approval-desktop", Name: "@crewbase/approval-desktop"},
	{ID: "gateway", Name: "@crewbase/gateway", Config: map[string]any{"addr": "127.0.0.1:7788", "tokenFile": "${root}/token"}},
	{ID: "api", Name: "@crewbase/api"},
	{ID: "channel-api", Name: "@crewbase/channel-api"},
	{ID: "webhook", Name: "@crewbase/webhook"},
	{ID: "routines-api", Name: "@crewbase/routines-api"},
	{ID: "followups-api", Name: "@crewbase/followups-api"},
	{ID: "memory-working-api", Name: "@crewbase/memory-working-api"},
	{ID: "usage", Name: "@crewbase/usage"},
	{ID: "workflows-api", Name: "@crewbase/workflows-api"},
	{ID: "mcp-api", Name: "@crewbase/mcp-api"},
	{ID: "catalog-api", Name: "@crewbase/catalog-api"},
	{ID: "google-api", Name: "@crewbase/google-api"},
	{ID: "skills-api", Name: "@crewbase/skills-api"},
	{ID: "browser-api", Name: "@crewbase/browser-api"},
	// Operating native apps (docs/computer-use.md): the permissions are
	// given to Crewbase.app, so only the desktop process has it. Provides
	// nothing off macOS.
	{ID: "computer", Name: "@crewbase/computer-macos"},
	// Operating native apps (docs/computer-use.md): the tools and their policy
	// wait for a computer provider (macOS only) and register nothing until
	// the switch in Settings › 电脑操作 is on; without a provider the API is
	// absent too and the page says it is unavailable.
	{ID: "computer-tools", Name: "@crewbase/computer-tools", Config: map[string]any{"dir": "${root}"}},
	{ID: "computer-api", Name: "@crewbase/computer-api"},
	{ID: "im-api", Name: "@crewbase/im-api"},
	{ID: "im-config", Name: "@crewbase/im-config", Config: map[string]any{"dir": "${root}"}},
	{ID: "audit-api", Name: "@crewbase/audit-api"},
	{ID: "goals-api", Name: "@crewbase/goals-api"},
	{ID: "plugin-manager", Name: "@crewbase/plugin-manager", Config: map[string]any{"dir": "${root}/plugins"}},
	{ID: "plugin-manager-api", Name: "@crewbase/plugin-manager-api"},
	{ID: "console", Name: "@crewbase/console", Config: map[string]any{"dir": "${root}"}},
	{ID: "providers-api", Name: "@crewbase/providers-api"},
	{ID: "model-catalog-api", Name: "@crewbase/model-catalog-api"},
	{ID: "usage-api", Name: "@crewbase/usage-api"},
	{ID: "roles", Name: "@crewbase/roles"},
	{ID: "ui-api", Name: "@crewbase/ui-api"},
	{ID: "attachments-api", Name: "@crewbase/attachments-api"},
	{ID: "voice-api", Name: "@crewbase/voice-api"},
	{ID: "devices", Name: "@crewbase/devices", Config: map[string]any{"dir": "${root}"}},
	{ID: "members", Name: "@crewbase/members", Config: map[string]any{"dir": "${root}"}},
	{ID: "teams", Name: "@crewbase/teams", Config: map[string]any{"dir": "${root}"}},
	{ID: "taskboard-api", Name: "@crewbase/taskboard-api"},
	// Outbound relay link (zero inbound ports); set config.url to enable.
	{ID: "relay", Name: "@crewbase/relay", Config: map[string]any{"dir": "${root}"}, Disabled: true},
	{ID: "audit-ui", Name: "@crewbase/audit-ui"},
}}

// Server is headless: same API bound to localhost (remote access goes
// through outbound relays, never inbound ports).
var Server = kernel.Bundle{Name: "server", Entries: []kernel.Entry{
	{ID: "approval-desktop", Name: "@crewbase/approval-desktop"},
	{ID: "gateway", Name: "@crewbase/gateway", Config: map[string]any{"addr": "127.0.0.1:7788", "tokenFile": "${root}/token"}},
	{ID: "api", Name: "@crewbase/api"},
	{ID: "channel-api", Name: "@crewbase/channel-api"},
	{ID: "devices", Name: "@crewbase/devices", Config: map[string]any{"dir": "${root}"}},
	{ID: "members", Name: "@crewbase/members", Config: map[string]any{"dir": "${root}"}},
	{ID: "teams", Name: "@crewbase/teams", Config: map[string]any{"dir": "${root}"}},
	{ID: "taskboard-api", Name: "@crewbase/taskboard-api"},
	{ID: "relay", Name: "@crewbase/relay", Config: map[string]any{"dir": "${root}"}, Disabled: true},
}}

func cheapRoutes() []map[string]string {
	var out []map[string]string
	for _, task := range []string{"title", "summarize"} {
		for _, m := range []string{"deepseek/deepseek-flash", "magpie/deepseek/deepseek-flash"} {
			out = append(out, map[string]string{"task": task, "model": m})
		}
	}
	return out
}

// defaultSandbox picks the sandbox provider for this platform: Seatbelt on
// macOS, bubblewrap (Linux namespaces) on Linux, Docker elsewhere. Switch with a patch:
//   - id: sandbox
//     name: '@crewbase/sandbox-docker'
func defaultSandbox() string {
	switch runtime.GOOS {
	case "darwin":
		return "@crewbase/sandbox-seatbelt"
	case "linux":
		return "@crewbase/sandbox-namespace"
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
