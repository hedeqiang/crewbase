// Package seatbelt is sandbox-seatbelt: the macOS provider of the
// `sandbox` capability. It wraps commands in `sandbox-exec -p <profile>`
// with a profile generated from the per-call policy:
//
//   - file writes are denied everywhere except the writable roots, the temp
//     directories and a few /dev nodes (reads are not restricted: the file
//     sandbox governs file effects, docs/06 §2);
//   - network is denied unless the policy says open. Seatbelt cannot filter
//     by domain, so an allowlist policy allows the network and reports
//     enforcement "partial".
//
// Paths are passed as -D parameters rather than spliced into the profile,
// so a path can never change the profile's meaning.
package seatbelt

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/sandbox"
	"github.com/hedeqiang/crewbase/kernel"
)

// DefaultBinary is the sandbox-exec location on macOS.
const DefaultBinary = "/usr/bin/sandbox-exec"

// Config is the plugin config.
type Config struct {
	// Binary overrides the sandbox-exec path.
	Binary string `json:"binary,omitempty"`
	// ExtraWritable are always-writable paths (e.g. a build cache).
	ExtraWritable []string `json:"extraWritable,omitempty"`
}

// Seatbelt implements sandbox.Sandbox.
type Seatbelt struct {
	binary string
	extra  []string

	mu      sync.Mutex
	probed  time.Time
	probeOK bool
	probeE  error
}

// New creates a provider. binary "" uses DefaultBinary.
func New(cfg Config) *Seatbelt {
	if cfg.Binary == "" {
		cfg.Binary = DefaultBinary
	}
	return &Seatbelt{binary: cfg.Binary, extra: cfg.ExtraWritable}
}

func (s *Seatbelt) Name() string { return "seatbelt" }

// available probes sandbox-exec once (failures are re-probed after 30s):
// a process that is itself sandboxed cannot apply a nested profile, which
// would otherwise surface as a confusing per-command failure.
func (s *Seatbelt) available(ctx context.Context) error {
	if runtime.GOOS != "darwin" {
		return sandbox.Unavailable("sandbox-seatbelt requires macOS (running on %s)", runtime.GOOS)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.probeOK {
		return nil
	}
	if !s.probed.IsZero() && time.Since(s.probed) < 30*time.Second {
		return s.probeE
	}
	s.probed = time.Now()
	if _, err := os.Stat(s.binary); err != nil {
		s.probeE = sandbox.Unavailable("sandbox-exec not found at %s", s.binary)
		return s.probeE
	}
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(pctx, s.binary, "-p", "(version 1)(allow default)", "/usr/bin/true").CombinedOutput()
	if err != nil {
		s.probeE = sandbox.Unavailable("sandbox-exec probe failed: %v %s", err, strings.TrimSpace(string(out)))
		return s.probeE
	}
	s.probeOK, s.probeE = true, nil
	return nil
}

func (s *Seatbelt) Check(ctx context.Context, p sandbox.Policy) (string, error) {
	p = p.Normalize()
	if p.Mode == sandbox.ModeFull {
		return sandbox.EnforcementNone, nil
	}
	if err := s.available(ctx); err != nil {
		return "", err
	}
	if p.Network == sandbox.NetworkAllowlist {
		return sandbox.EnforcementPartial, nil
	}
	return sandbox.EnforcementFull, nil
}

// Profile returns the seatbelt profile and its -D parameters for p.
// Exported for tests and diagnostics.
func (s *Seatbelt) Profile(p sandbox.Policy, env []string) (string, []string) {
	p = p.Normalize()
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n")
	b.WriteString("(deny file-write*)\n")
	b.WriteString(`(allow file-write*
  (literal "/dev/null") (literal "/dev/zero") (literal "/dev/tty")
  (literal "/dev/dtracehelper") (regex #"^/dev/fd/[0-9]+$") (regex #"^/dev/ttys[0-9]+$"))
`)
	var roots []string
	if p.Mode == sandbox.ModeWorkspaceWrite {
		roots = append(roots, p.WritableRoots...)
	}
	roots = append(roots, tempDirs(env)...)
	roots = append(roots, s.extra...)
	var params []string
	seen := map[string]bool{}
	n := 0
	for _, r := range roots {
		r = resolve(r)
		if r == "" || r == "/" || seen[r] {
			continue
		}
		seen[r] = true
		key := fmt.Sprintf("WRITABLE_ROOT_%d", n)
		n++
		params = append(params, "-D", key+"="+r)
		fmt.Fprintf(&b, "(allow file-write* (subpath (param %q)))\n", key)
	}
	switch p.Network {
	case sandbox.NetworkOpen, sandbox.NetworkAllowlist:
		// allowlist cannot be filtered by domain here: reported as partial.
	default:
		b.WriteString("(deny network* (remote ip))\n")
	}
	return b.String(), params
}

func (s *Seatbelt) Confine(ctx context.Context, argv []string, cwd string, env []string, p sandbox.Policy) (sandbox.Confined, error) {
	p = p.Normalize()
	if err := sandbox.Validate(argv, p); err != nil {
		return sandbox.Confined{}, err
	}
	if p.Mode == sandbox.ModeFull {
		return sandbox.Unconfined(argv, env), nil
	}
	enf, err := s.Check(ctx, p)
	if err != nil {
		return sandbox.Confined{}, err
	}
	profile, params := s.Profile(p, env)
	out := []string{s.binary, "-p", profile}
	out = append(out, params...)
	out = append(out, argv...)
	c := sandbox.Confined{
		Argv:        out,
		Env:         append([]string(nil), env...),
		Enforcement: enf,
		BlockedSignatures: []string{
			"Operation not permitted",
			"sandbox-exec:",
			"deny(1)",
		},
	}
	if p.Network == sandbox.NetworkAllowlist {
		c.Limits = append(c.Limits, "seatbelt cannot filter network by domain; allowlist is not enforced")
	}
	if p.Network == sandbox.NetworkNone {
		c.BlockedSignatures = append(c.BlockedSignatures, "Couldn't connect to server", "Failed to connect to", "Network is unreachable")
	}
	return c, nil
}

// tempDirs lists the temp directories commands may write to.
func tempDirs(env []string) []string {
	out := []string{"/private/tmp", "/private/var/tmp", os.TempDir()}
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "TMPDIR="); ok && v != "" {
			out = append(out, v)
		}
	}
	return out
}

// resolve makes p absolute and resolves symlinks (seatbelt matches real
// paths: /tmp is /private/tmp). A missing path resolves its parent.
func resolve(p string) string {
	if p == "" || !filepath.IsAbs(p) {
		return ""
	}
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	dir, base := filepath.Split(p)
	if dir == p || dir == "" {
		return p
	}
	if r := resolve(strings.TrimSuffix(dir, "/")); r != "" {
		return filepath.Join(r, base)
	}
	return p
}

var _ sandbox.Sandbox = (*Seatbelt)(nil)

// Plugin provides `sandbox` via macOS sandbox-exec.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/sandbox-seatbelt",
	Description: "macOS sandbox-exec (seatbelt) sandbox: writes confined to writable roots, network denied unless open.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		return c.Provide(sandbox.ServiceKey, New(cfg))
	},
})
