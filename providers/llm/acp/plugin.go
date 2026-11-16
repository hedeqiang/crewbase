// Package acp bridges subscription coding CLIs (Claude Code, Codex,
// Antigravity, Cursor CLI, OpenCode, Grok CLI) into Crewbase as llm adapters by
// driving them over the Agent Client Protocol (JSON-RPC 2.0 on stdio,
// https://agentclientprotocol.com). The user's existing CLI login and
// subscription quota are reused; no API keys are involved.
//
// Model: a Crewbase step is one ACP session/prompt turn. The CLI owns its
// own tools and history; its tool calls run inside the CLI and are reported
// as text notes (never as Crewbase tool calls), so Response.StopReason is
// always "end" or "max_tokens". Crewbase tools in Request.Tools are not
// offered to the CLI as such; instead, CLIs that take http MCP servers get
// Crewbase's MCP server (features/mcpserver: computer_* and browser_*,
// run through Crewbase's own tool pipeline) with a per-session token; see
// mcp.go.
//
// Sessions: one ACP session per conversation, keyed by agent + session (or
// hash(system prompt + first message) when the request carries neither). It is reused while the request's messages extend what the
// session has already seen (including its own last reply); otherwise a new
// session starts with a rendered transcript of the earlier messages, which
// keeps model switching lossless.
//
// Sessions run in Request.Workspace (the asking agent's workspace) when set,
// else the configured cwd, and are keyed by Request.Agent + Request.Session
// when known (falling back to the prompt hash below).
//
// Permissions: session/request_permission is routed to the approval service
// (session Request.Session, agent Request.Agent, call id
// "acp/<session>/<toolCallId>", category from the ACP tool kind, summary
// from the tool title). Allowed-once selects the CLI's allow_once option;
// anything else (rejected, no approver, no session) selects reject_once.
// Config autoApprove: true is an explicit override that allows everything.
// Requests for the CLI's calls of Crewbase's own MCP tools are allowed
// without asking: Crewbase's tool pipeline asks for those itself (mcp.go).
// Client methods: fs/read_text_file and fs/write_text_file are served,
// restricted to the session cwd (symlinks resolved); terminal/* and
// everything else answer method-not-found (capability not advertised).
//
// CLI config: in an agent's own workspace (Request.OwnWorkspace) project
// config the CLIs would load (.claude/, .gemini/, opencode.json, ...) is
// moved aside before every turn, cannot be written over ACP, and Claude Code
// loads user settings only. See cliconfig.go.
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
//	antigravity  agy acp
//	cursor       cursor-agent acp
//	opencode     opencode acp
//	grok         grok agent stdio (unverified)
package acp

import (
	"context"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/features/mcpserver"
	"github.com/hedeqiang/crewbase/kernel"
)

// approver routes CLI permission requests to the approval service, looked
// up when asked (it may be provided after this plugin). Without the
// service, a session store or a known session the answer is unavailable.
func approver(c *kernel.Context) func(context.Context, approval.Request) approval.Outcome {
	return func(ctx context.Context, req approval.Request) approval.Outcome {
		svc, ok := kernel.Lookup[*approval.Service](c, approval.ServiceKey)
		if !ok || req.Session == "" {
			return approval.OutcomeUnavailable
		}
		store, ok := kernel.Lookup[*session.Store](c, session.ServiceKey)
		if !ok {
			return approval.OutcomeUnavailable
		}
		ss, err := store.Get(req.Session)
		if err != nil {
			return approval.OutcomeUnavailable
		}
		return svc.Ask(ctx, ss, req).Outcome
	}
}

// mcpGranter opens grants on Crewbase's MCP server (features/mcpserver),
// looked up when a session starts (it may load after this plugin, or not
// at all: then CLIs are not given Crewbase's tools).
func mcpGranter(c *kernel.Context) func(MCPBinding) MCPGrant {
	return func(b MCPBinding) MCPGrant {
		svc, ok := kernel.Lookup[*mcpserver.Service](c, mcpserver.ServiceKey)
		if !ok {
			return nil
		}
		g, err := svc.Open(mcpserver.Binding{Agent: b.Agent, Session: b.Session, Workspace: b.Workspace, Mode: b.Mode, Own: b.Own})
		if err != nil {
			c.Log().Debug("acp: Crewbase MCP server not offered", "agent", b.Agent, "err", err)
			return nil
		}
		return g
	}
}

// rescanEvery is how often PATH is checked for newly installed CLIs.
var rescanEvery = time.Minute

// probeDelay is how long after start a CLI's models are probed (out of the
// way of start-up); probe is off in tests.
var (
	probeDelay = 5 * time.Second
	probe      = true
)

// FilesChange reports files a CLI changed in a workspace (its own edit
// tools or fs/write_text_file), so views of the workspace refresh.
type FilesChange struct {
	Agent   string   `json:"agent"`
	Session string   `json:"session"`
	Paths   []string `json:"paths"`
}

// FilesChanged fires after a CLI tool call changed files.
var FilesChanged = kernel.NewEmit[FilesChange]("acp/files-changed")

// ServiceKey is the key of the detection service (*Service).
const ServiceKey = "llm-acp"

// Service exposes detection and the registered adapters to the UI.
type Service struct {
	cfg      Config
	mu       sync.Mutex
	adapters map[string]*Adapter // by agent id
	// add registers a newly runnable CLI (set by the plugin).
	add func(r resolved) error
}

// Rescan registers CLIs installed since the last scan (a CLI installed
// while Crewbase runs shows up without a restart). It returns the ids added.
func (s *Service) Rescan() []string {
	var added []string
	for _, ac := range agentList(s.cfg) {
		s.mu.Lock()
		_, have := s.adapters[ac.ID]
		s.mu.Unlock()
		if have {
			continue
		}
		r := resolve(ac)
		if !r.runnable() || s.add == nil {
			continue
		}
		if err := s.add(r); err == nil {
			added = append(added, r.ID)
		}
	}
	return added
}

// Detect lists subscription CLIs (installed or not) for the settings page.
func (s *Service) Detect(ctx context.Context) []CLIInfo {
	infos := Detect(ctx, s.cfg)
	s.Rescan()
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range infos {
		if a := s.adapters[infos[i].ID]; a != nil {
			infos[i].Running = a.Running()
		}
	}
	return infos
}

// Adapter returns the adapter for an agent id, if registered.
func (s *Service) Adapter(agentID string) (*Adapter, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.adapters[agentID]
	return a, ok
}

// Plugin registers one llm adapter ("acp-<id>") per installed CLI.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/llm-acp",
	Inject:      []string{llm.ServiceKey},
	Description: "Subscription CLIs (Claude Code, Codex, Antigravity, ...) over ACP.",
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
		svc.add = func(r resolved) error {
			a := newAdapter(providerID(r.ID), r, Options{AutoApprove: cfg.AutoApprove, Approve: approver(c), MCP: mcpGranter(c), Log: c.Log(), IdleTimeout: idle, ToolNotes: notes, Go: goFn, StateDir: cfg.StateDir,
				FilesChanged: func(agent, sess string, paths []string) {
					FilesChanged.Emit(c.Host(), FilesChange{Agent: agent, Session: sess, Paths: paths})
				}})
			// processes stop when the plugin context's goroutines are
			// cancelled; Close also drops sessions and the idle timer
			c.Defer(a.Close)
			if err := reg.Register(c, a); err != nil {
				return err
			}
			svc.mu.Lock()
			svc.adapters[r.ID] = a
			svc.mu.Unlock()
			// the model picker lists the CLI's own models, not only
			// "default": from the cache at once, then learned again in the
			// background when the cache is old or was learned with another
			// CLI or bridge version
			a.loadCachedModels()
			goFn(func(ctx context.Context) {
				if !probe {
					return
				}
				a.SetVersions(versionKey(versionOf(ctx, r.cliPath), a.Bridge()))
				if a.cacheFresh() {
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(probeDelay):
				}
				pctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				defer cancel()
				if err := a.ProbeModels(pctx); err != nil {
					c.Log().Info("acp: model list not learned", "agent", r.ID, "err", err)
				}
				llm.AdaptersChanged.Emit(c.Host(), struct{}{})
			})
			return nil
		}
		for _, ac := range agentList(cfg) {
			r := resolve(ac)
			if !r.runnable() {
				c.Log().Debug("acp agent not available", "agent", ac.ID, "cli", r.CLI, "installed", r.cliPath != "")
				continue
			}
			if err := svc.add(r); err != nil {
				return err
			}
		}
		// pick up CLIs installed later
		c.Go(func(ctx context.Context) {
			t := time.NewTicker(rescanEvery)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if ids := svc.Rescan(); len(ids) > 0 {
						c.Log().Info("acp: new CLIs found", "agents", ids)
					}
				}
			}
		})
		return c.Provide(ServiceKey, svc)
	},
})
