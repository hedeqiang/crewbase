// Package runmode is the run-mode guard: hard constraints of the three run
// mode presets (docs/06 §2), enforced as a tools/guard listener, which can
// only deny or abstain and cannot be re-allowed by rules or hooks.
//
//   - read-only: only read / none categories pass. exec passes only when a
//     sandbox is loaded that fully enforces a read-only file policy; write,
//     delete, install, push, publish and payment are denied.
//   - read-only and sandbox: file path arguments must stay inside the
//     agent workspace (symlinks resolved).
//   - host: no guard constraints (rules and approval still apply).
//
// Host mode lasts until it is changed. A grant from before (they ran 24h)
// that has run out reads as sandbox in core/agent (Agent.Mode); this
// package reads it through Agent.Mode and
// offers helpers to change modes with a `mode/changed` session event.
package runmode

import (
	"context"
	"errors"
	"fmt"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"path/filepath"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/sandbox"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

// PathArgs are the argument names treated as file system paths for tools
// that do not declare tools.Tool.PathArgs; the guard checks these
// well-known names on such tools.
var PathArgs = []string{"path", "paths", "file_path", "filepath", "cwd", "dir", "directory", "workdir", "target", "source", "destination", "dest"}

// Effective is the agent's effective run mode (host expires to sandbox).
func Effective(a *agent.Agent) string { return sandbox.EffectiveRunMode(a.Mode()) }

// HostExpiry returns when host mode ends, if the agent is in host mode now.
func HostExpiry(a *agent.Agent) (time.Time, bool) {
	if a.Mode() != agent.ModeHost {
		return time.Time{}, false
	}
	return a.Spec().ModeUntil, true
}

// Expired reports whether the agent was set to host mode but the grant ran
// out (its effective mode is back to sandbox).
func Expired(a *agent.Agent) bool {
	return a.Spec().Mode == agent.ModeHost && a.Mode() != agent.ModeHost
}

// Change is the data of a `mode/changed` session event.
type Change struct {
	From   string    `json:"from"`
	To     string    `json:"to"`
	Until  time.Time `json:"until,omitzero"`
	By     string    `json:"by,omitempty"`
	Reason string    `json:"reason,omitempty"` // e.g. "user", "expired"
}

// Record appends a `mode/changed` event to ss.
func Record(ss *session.Session, agentID string, ch Change) error {
	if ss == nil {
		return errors.New("runmode: no session")
	}
	_, err := ss.Append(session.TypeModeChanged, agentID, ch)
	return err
}

// Set switches the agent's run mode and records `mode/changed` in ss (the
// agent's own session when ss is nil). Host mode is granted until the
// mode is changed again.
func Set(a *agent.Agent, ss *session.Session, mode, by, reason string) (Change, error) {
	switch mode {
	case agent.ModeReadOnly, agent.ModeSandbox, agent.ModeHost:
	default:
		return Change{}, fmt.Errorf("runmode: unknown mode %q", mode)
	}
	from := Effective(a)
	if err := a.Update(func(s *agent.Spec) {
		s.Mode = mode
		s.ModeUntil = time.Time{} // a new grant has no end
	}); err != nil {
		return Change{}, err
	}
	ch := Change{From: from, To: mode, By: by, Reason: reason}
	if mode == agent.ModeHost {
		ch.Until = a.Spec().ModeUntil
	}
	if ss == nil {
		s, err := a.Session()
		if err != nil {
			return ch, err
		}
		ss = s
	}
	return ch, Record(ss, a.ID(), ch)
}

// RecordExpiry records the automatic host → sandbox revert once, if it
// happened and the session's last mode/changed does not already say so.
func RecordExpiry(a *agent.Agent, ss *session.Session) (bool, error) {
	if !Expired(a) || ss == nil {
		return false, nil
	}
	if e, ok := ss.Last(session.TypeModeChanged); ok {
		var last Change
		if e.Decode(&last) == nil && last.To == agent.ModeSandbox && last.Reason == "expired" {
			return false, nil
		}
	}
	return true, Record(ss, a.ID(), Change{From: agent.ModeHost, To: agent.ModeSandbox, Reason: "expired"})
}

// Guard holds the guard's dependencies.
type Guard struct {
	// Sandbox resolves the current sandbox service (nil = none).
	Sandbox func() sandbox.Sandbox
}

// Check returns a denial reason, or ok=false to abstain.
func (g *Guard) Check(ctx context.Context, d *tools.Decision) (string, bool) {
	if d == nil || d.Call == nil {
		return "", false
	}
	call := d.Call
	mode := sandbox.EffectiveRunMode(call.Mode)
	if mode == agent.ModeHost {
		return "", false
	}
	if mode == agent.ModeReadOnly {
		if d.Tool != nil && d.Tool.Unconfined && d.Category != tools.CategoryRead && d.Category != tools.CategoryNone {
			return fmt.Sprintf("read-only mode: %s runs outside the sandbox (a host-trusted server), so only its read tools may run", call.Name), true
		}
		if reason, deny := g.readOnly(ctx, d.Category); deny {
			return reason, true
		}
	}
	extra := append(append([]string(nil), call.Roots...), call.ReadRoots...)
	if d.Tool != nil {
		extra = append(extra, d.Tool.AllowedRoots...)
	}
	return checkPaths(call, argNames(d.Tool), extra...)
}

// argNames is the tool's declared path arguments, or the well-known names.
func argNames(t *tools.Tool) []string {
	if t != nil && len(t.PathArgs) > 0 {
		return t.PathArgs
	}
	return PathArgs
}

func (g *Guard) readOnly(ctx context.Context, cat string) (string, bool) {
	switch cat {
	case tools.CategoryRead, tools.CategoryNone:
		return "", false
	case tools.CategoryExec:
		var sb sandbox.Sandbox
		if g.Sandbox != nil {
			sb = g.Sandbox()
		}
		if sb == nil {
			return "read-only mode: commands need a sandbox that enforces read-only, and none is loaded", true
		}
		enf, err := sb.Check(ctx, sandbox.Policy{Mode: sandbox.ModeReadOnly, Network: sandbox.NetworkNone})
		if err != nil {
			return fmt.Sprintf("read-only mode: %v", err), true
		}
		if enf != sandbox.EnforcementFull {
			return fmt.Sprintf("read-only mode: sandbox %s only enforces read-only %s", sb.Name(), enf), true
		}
		return "", false
	}
	if cat == "" {
		cat = "unknown"
	}
	return fmt.Sprintf("read-only mode does not allow %s actions; ask the user to switch to sandbox mode", cat), true
}

// checkPaths denies path arguments that leave the workspace.
func checkPaths(call *tools.Call, names []string, granted ...string) (string, bool) {
	paths := pathArgs(call, names)
	if len(paths) == 0 {
		return "", false
	}
	ws := call.Workspace
	if ws == "" || !filepath.IsAbs(ws) {
		return "no workspace is set for this agent; file paths are not allowed outside host mode", true
	}
	roots := []string{realPath(filepath.Clean(ws))}
	for _, g := range granted {
		if filepath.IsAbs(g) {
			roots = append(roots, realPath(filepath.Clean(g)))
		}
	}
	for _, p := range paths {
		abs := p
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(ws, abs)
		}
		real := realPath(filepath.Clean(abs))
		ok := false
		for _, root := range roots {
			ok = ok || within(real, root)
		}
		if !ok {
			return fmt.Sprintf("path %q is outside the workspace (%s); only host mode may touch it", p, ws), true
		}
	}
	return "", false
}

func pathArgs(call *tools.Call, names []string) []string {
	var m map[string]any
	if call.Decode(&m) != nil {
		return nil
	}
	var out []string
	for _, k := range names {
		switch v := m[k].(type) {
		case string:
			if v != "" && !strings.Contains(v, "://") {
				out = append(out, v)
			}
		case []any:
			for _, x := range v {
				if s, ok := x.(string); ok && s != "" && !strings.Contains(s, "://") {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

// realPath resolves symlinks; a missing tail is appended to its resolved
// ancestor (same rule as fs-local).
func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p
	}
	return filepath.Join(realPath(parent), filepath.Base(p))
}

func within(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Plugin installs the run-mode guard.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/runmode",
	Description: "Run-mode guard: read-only denies writes; confined modes keep paths inside the workspace.",
	Inject:      []string{tools.ServiceKey},
	Optional:    []string{sandbox.ServiceKey},
	Apply: func(c *kernel.Context) error {
		g := &Guard{Sandbox: func() sandbox.Sandbox {
			sb, _ := kernel.Lookup[sandbox.Sandbox](c, sandbox.ServiceKey)
			return sb
		}}
		tools.Guard.Listen(c, g.Check)
		agentloop.TurnStarted.Listen(c, func(e agentloop.TurnStartEvent) {
			_, _ = RecordExpiry(e.Agent, e.Session)
		})
		return nil
	},
})
