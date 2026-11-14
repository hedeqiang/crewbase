// Package sandbox defines the `sandbox` capability: wrapping a command into a
// restricted environment. Providers (sandbox-none, sandbox-seatbelt,
// sandbox-docker, ...) implement Sandbox; consumers are the providers of
// `shell` / `subprocess`. See docs/06-security-model.md §2.
//
// Rules every provider follows:
//
//   - The policy travels with each call; providers never fix it.
//   - Enforcement is reported honestly: full, partial, or none.
//   - A backend that cannot run fails closed with ErrUnavailable
//     (SANDBOX_UNAVAILABLE). It never silently runs the command unconfined.
//   - BlockedSignatures let callers tell "blocked by the sandbox" (ask for
//     more permission) from "the command itself failed" (retry / fix).
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hedeqiang/crewbase/core/agent"
)

// ServiceKey is the service key of the sandbox capability.
const ServiceKey = "sandbox"

// File-system modes (Harness sandbox modes).
const (
	ModeReadOnly       = "read-only"
	ModeWorkspaceWrite = "workspace-write"
	ModeFull           = "full" // danger-full-access: no file confinement
)

// Network modes (the independent third knob).
const (
	NetworkNone      = "none"
	NetworkAllowlist = "allowlist"
	NetworkOpen      = "open"
)

// Enforcement levels reported by providers.
const (
	EnforcementFull    = "full"
	EnforcementPartial = "partial"
	EnforcementNone    = "none"
)

// CodeUnavailable is the stable error code for an unavailable backend.
const CodeUnavailable = "SANDBOX_UNAVAILABLE"

// ErrUnavailable means the backend cannot confine this command right now
// (daemon down, binary missing, policy not enforceable). Callers must not
// run the command. Test with errors.Is.
var ErrUnavailable = errors.New(CodeUnavailable)

// Unavailable wraps ErrUnavailable with a reason.
func Unavailable(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrUnavailable, fmt.Sprintf(format, a...))
}

// Policy is the per-call confinement request.
type Policy struct {
	Mode          string   `json:"mode"`                    // read-only | workspace-write | full
	WritableRoots []string `json:"writableRoots,omitempty"` // workspace-write only
	Network       string   `json:"network"`                 // none | allowlist | open
	AllowHosts    []string `json:"allowHosts,omitempty"`    // allowlist only
}

// Normalize fills defaults and fails closed on unknown values: an unknown
// file mode becomes read-only, an unknown network mode becomes none, and an
// allowlist without hosts becomes none.
func (p Policy) Normalize() Policy {
	switch p.Mode {
	case ModeReadOnly, ModeWorkspaceWrite, ModeFull:
	default:
		p.Mode = ModeReadOnly
	}
	if p.Mode != ModeWorkspaceWrite {
		p.WritableRoots = nil
	}
	switch p.Network {
	case NetworkNone, NetworkOpen:
	case NetworkAllowlist:
		if len(p.AllowHosts) == 0 {
			p.Network = NetworkNone
		}
	case "":
		if p.Mode == ModeFull {
			p.Network = NetworkOpen
		} else {
			p.Network = NetworkNone
		}
	default:
		p.Network = NetworkNone
	}
	if p.Network != NetworkAllowlist {
		p.AllowHosts = nil
	}
	return p
}

// Confined is a command ready to run.
type Confined struct {
	Argv        []string `json:"argv"`
	Env         []string `json:"env"`
	Enforcement string   `json:"enforcement"` // full | partial | none
	// Limits explains a partial enforcement (e.g. "network allowlist is not
	// filtered by domain").
	Limits []string `json:"limits,omitempty"`
	// BlockedSignatures are substrings of stderr/stdout that indicate the
	// sandbox (not the command) refused an operation.
	BlockedSignatures []string `json:"blockedSignatures,omitempty"`
	// Kill, if set, stops out-of-process work (e.g. a container) when the
	// command is cancelled or times out. Killing the local process is the
	// caller's job.
	Kill func() `json:"-"`
}

// Sandbox is the `sandbox` service.
type Sandbox interface {
	// Name identifies the provider ("seatbelt", "docker", "none").
	Name() string
	// Check reports the enforcement level the provider would give p, or
	// ErrUnavailable if it cannot confine p right now. It runs nothing.
	Check(ctx context.Context, p Policy) (string, error)
	// Confine wraps argv for execution in cwd under p.
	Confine(ctx context.Context, argv []string, cwd string, env []string, p Policy) (Confined, error)
}

// Unconfined is the passthrough for ModeFull: argv unchanged, enforcement none.
func Unconfined(argv, env []string) Confined {
	return Confined{Argv: append([]string(nil), argv...), Env: append([]string(nil), env...), Enforcement: EnforcementNone}
}

// EffectiveRunMode normalizes a run mode preset: "" is the default
// (sandbox) and anything unknown fails closed to read-only.
func EffectiveRunMode(m string) string {
	switch m {
	case agent.ModeHost, agent.ModeSandbox, agent.ModeReadOnly:
		return m
	case "":
		return agent.ModeSandbox
	}
	return agent.ModeReadOnly
}

// PolicyFor maps an agent run mode preset to a sandbox policy (docs/06 §2):
// read-only → read-only, sandbox → workspace-write on the workspace, host →
// full. Unknown modes fail closed to read-only. allowHosts, when non-empty,
// turns on the network allowlist for the confined modes.
func PolicyFor(runMode, workspace string, allowHosts []string) Policy {
	var p Policy
	switch EffectiveRunMode(runMode) {
	case agent.ModeHost:
		p = Policy{Mode: ModeFull, Network: NetworkOpen}
	case agent.ModeSandbox:
		p = Policy{Mode: ModeWorkspaceWrite, WritableRoots: []string{workspace}, Network: NetworkNone}
	default: // read-only and anything unknown
		p = Policy{Mode: ModeReadOnly, Network: NetworkNone}
	}
	if p.Mode != ModeFull && len(allowHosts) > 0 {
		p.Network, p.AllowHosts = NetworkAllowlist, append([]string(nil), allowHosts...)
	}
	if p.Mode == ModeWorkspaceWrite && workspace == "" {
		p.WritableRoots = nil // nothing writable rather than everything
	}
	return p.Normalize()
}

// Validate checks argv and writable roots before confinement.
func Validate(argv []string, p Policy) error {
	if len(argv) == 0 || argv[0] == "" {
		return errors.New("sandbox: empty command")
	}
	for _, r := range p.WritableRoots {
		if !filepath.IsAbs(r) {
			return fmt.Errorf("sandbox: writable root must be absolute: %q", r)
		}
		if filepath.Clean(r) == "/" {
			return errors.New("sandbox: writable root may not be /")
		}
	}
	return nil
}

// MatchBlocked reports whether output contains one of the signatures
// (case-insensitive).
func MatchBlocked(signatures []string, outputs ...string) bool {
	for _, o := range outputs {
		lo := strings.ToLower(o)
		for _, s := range signatures {
			if s != "" && strings.Contains(lo, strings.ToLower(s)) {
				return true
			}
		}
	}
	return false
}
