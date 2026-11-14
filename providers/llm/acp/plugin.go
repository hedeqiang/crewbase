// Package acp bridges subscription coding CLIs (Claude Code, Codex, Gemini
// CLI, Cursor CLI, OpenCode, Grok CLI) into Crewbase as llm adapters by
// driving them over the Agent Client Protocol (JSON-RPC 2.0 on stdio,
// https://agentclientprotocol.com). The user's existing CLI login and
// subscription quota are reused; no API keys are involved.
//
// Model: a Crewbase step is one ACP session/prompt turn. The CLI owns its
// own tools and history; its tool calls run inside the CLI and are reported
// as text notes (never as Crewbase tool calls), so Response.StopReason is
// always "end" or "max_tokens". Crewbase tools in Request.Tools are not
// offered to the CLI.
//
// Sessions: one ACP session per conversation, keyed by hash(system prompt +
// first message). It is reused while the request's messages extend what the
// session has already seen (including its own last reply); otherwise a new
// session starts with a rendered transcript of the earlier messages, which
// keeps model switching lossless.
//
// Permissions: session/request_permission is rejected (the agent's
// reject_once option) unless config autoApprove is true, in which case
// allow_once is chosen. Routing to Crewbase approvals is future work.
// Client methods: fs/read_text_file and fs/write_text_file are served,
// restricted to the session cwd (symlinks resolved); terminal/* and
// everything else answer method-not-found (capability not advertised).
//
// Errors: JSON-RPC errors, limit banners in replies and the process's stderr
// are classified: usage/5-hour/weekly limits and quota -> quota_exhausted,
// rate limits -> rate_limited, ACP -32000 / "run /login" -> auth, process
// exit -> unavailable.
//
// Processes: started on first use, stopped after idleTimeoutSec without a
// turn, and on dispose the whole process group is terminated (Setpgid).
//
// ACP entrypoints (built-in defaults, first one found on PATH wins):
//
//	claude-code  claude-agent-acp | claude-code-acp | npx -y @agentclientprotocol/claude-agent-acp
//	codex        codex-acp | npx -y @zed-industries/codex-acp
//	gemini-cli   gemini --experimental-acp
//	cursor       cursor-agent acp
//	opencode     opencode acp
//	grok         grok agent stdio (unverified)
package acp

import (
	"context"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/kernel"
)

// ServiceKey is the key of the detection service (*Service).
const ServiceKey = "llm-acp"

// Service exposes detection and the registered adapters to the UI.
type Service struct {
	cfg      Config
	adapters map[string]*Adapter // by agent id
}

// Detect lists subscription CLIs (installed or not) for the settings page.
func (s *Service) Detect(ctx context.Context) []CLIInfo {
	infos := Detect(ctx, s.cfg)
	for i := range infos {
		if a := s.adapters[infos[i].ID]; a != nil {
			infos[i].Running = a.Running()
		}
	}
	return infos
}

// Adapter returns the adapter for an agent id, if registered.
func (s *Service) Adapter(agentID string) (*Adapter, bool) {
	a, ok := s.adapters[agentID]
	return a, ok
}

// Plugin registers one llm adapter ("acp-<id>") per installed CLI.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/llm-acp",
	Inject:      []string{llm.ServiceKey},
	Description: "Subscription CLIs (Claude Code, Codex, Gemini CLI, ...) over ACP.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		idle := time.Duration(cfg.IdleTimeoutSec) * time.Second
		if cfg.IdleTimeoutSec == 0 {
			idle = 10 * time.Minute
		} else if cfg.IdleTimeoutSec < 0 {
			idle = -1
		}
		notes := cfg.ToolNotes == nil || *cfg.ToolNotes
		reg := kernel.Use[*llm.Registry](c, llm.ServiceKey)
		svc := &Service{cfg: cfg, adapters: map[string]*Adapter{}}
		goFn := func(fn func(context.Context)) bool {
			if c.Disposed() {
				return false
			}
			c.Go(fn)
			return true
		}
		for _, ac := range agentList(cfg) {
			r := resolve(ac)
			if !r.runnable() {
				c.Log().Debug("acp agent not available", "agent", ac.ID, "cli", r.CLI, "installed", r.cliPath != "")
				continue
			}
			a := newAdapter(providerID(r.ID), r, Options{AutoApprove: cfg.AutoApprove, IdleTimeout: idle, ToolNotes: notes, Go: goFn})
			// processes stop when the plugin context's goroutines are
			// cancelled; Close also drops sessions and the idle timer
			c.Defer(a.Close)
			if err := reg.Register(c, a); err != nil {
				return err
			}
			svc.adapters[r.ID] = a
		}
		return c.Provide(ServiceKey, svc)
	},
})
