// Package bubblewrap is sandbox-namespace: the Linux provider of the
// `sandbox` capability, wrapping commands in bubblewrap (bwrap) with Linux
// namespaces, generated per call from the policy:
//
//   - the whole filesystem is mounted read-only; writable roots (workspace)
//     are bound writable (read-only mode binds none); protected roots in
//     them stay read-only and missing ones cannot be created (see Args);
//     /tmp is a private, empty tmpfs (the host's /tmp is not visible); /dev
//     and /proc are new;
//   - network: none → a new network namespace (no interfaces but lo);
//     open → the host network; allowlist cannot filter by domain and is
//     reported as enforcement "partial";
//   - new PID / IPC / UTS namespaces, --die-with-parent, --new-session
//     (no TIOCSTI), capabilities dropped.
//
// It needs unprivileged user namespaces (most distributions) or setuid
// bwrap. If bwrap is missing or cannot create namespaces (e.g. inside a
// container whose seccomp forbids it), the provider reports
// SANDBOX_UNAVAILABLE and nothing runs unconfined.
package bubblewrap

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/sandbox"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/sandbox/internal/protect"
)

// Config is the plugin config.
type Config struct {
	// Binary overrides the bwrap path (default: looked up on PATH).
	Binary string `json:"binary,omitempty"`
	// ExtraWritable are always-writable paths (e.g. a build cache).
	ExtraWritable []string `json:"extraWritable,omitempty"`
}

// Bubblewrap implements sandbox.Sandbox.
type Bubblewrap struct {
	binary string
	extra  []string

	mu      sync.Mutex
	probed  time.Time
	probeOK bool
	probeE  error
}

// New creates a provider.
func New(cfg Config) *Bubblewrap { return &Bubblewrap{binary: cfg.Binary, extra: cfg.ExtraWritable} }

func (b *Bubblewrap) Name() string { return "bubblewrap" }

// available probes bwrap once (failures re-probed after 30s) by running
// /bin/true inside the namespaces it will use.
func (b *Bubblewrap) available(ctx context.Context) error {
	if runtime.GOOS != "linux" {
		return sandbox.Unavailable("sandbox-namespace requires Linux (running on %s)", runtime.GOOS)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.probeOK {
		return nil
	}
	if !b.probed.IsZero() && time.Since(b.probed) < 30*time.Second {
		return b.probeE
	}
	b.probed = time.Now()
	bin := b.binary
	if bin == "" {
		p, err := exec.LookPath("bwrap")
		if err != nil {
			b.probeE = sandbox.Unavailable("bubblewrap (bwrap) is not installed; install it (apt install bubblewrap / dnf install bubblewrap)")
			return b.probeE
		}
		bin = p
	}
	b.binary = bin
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	args := append(b.base(sandbox.Policy{Mode: sandbox.ModeReadOnly, Network: sandbox.NetworkNone}), "--", "/bin/true")
	out, err := exec.CommandContext(pctx, bin, args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		b.probeE = sandbox.Unavailable("bubblewrap cannot create namespaces here (%v: %s); enable unprivileged user namespaces or run crewd on the host", err, msg)
		return b.probeE
	}
	b.probeOK, b.probeE = true, nil
	return nil
}

func (b *Bubblewrap) Check(ctx context.Context, p sandbox.Policy) (string, error) {
	p = p.Normalize()
	if p.Mode == sandbox.ModeFull {
		return sandbox.EnforcementNone, nil
	}
	if err := b.available(ctx); err != nil {
		return "", err
	}
	if p.Network == sandbox.NetworkAllowlist {
		return sandbox.EnforcementPartial, nil
	}
	return sandbox.EnforcementFull, nil
}

// base returns the bwrap arguments shared by every confined command.
func (b *Bubblewrap) base(p sandbox.Policy) []string {
	args := []string{
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
		"--unshare-pid", "--unshare-ipc", "--unshare-uts", "--unshare-cgroup-try",
		"--die-with-parent", "--new-session",
		"--cap-drop", "ALL",
	}
	if p.Network != sandbox.NetworkOpen && p.Network != sandbox.NetworkAllowlist {
		args = append(args, "--unshare-net")
	}
	return args
}

// Args returns the full bwrap argument list for p (exported for tests and
// diagnostics). Writable roots are bound after the read-only root so they
// take precedence, and protected roots in them come last so they stay
// read-only (in bwrap a later mount covers an earlier one):
//
//   - a protected path that exists: --ro-bind p p;
//   - one that does not: --tmpfs p --remount-ro p, an empty read-only
//     directory, so the command cannot create the name either. The mount
//     point shows in the host folder as an empty directory while the
//     command runs; Confine's Release removes it again (package
//     internal/protect explains the trade-off).
func (b *Bubblewrap) Args(p sandbox.Policy, cwd string, env []string) []string {
	args, _ := b.args(p, cwd, env, false)
	return args
}

// placeholders tracks the mount points made for missing protected names.
var placeholders protect.Registry

func (b *Bubblewrap) args(p sandbox.Policy, cwd string, env []string, acquire bool) ([]string, func()) {
	p = p.Normalize()
	args := b.base(p)
	var roots []string
	if p.Mode == sandbox.ModeWorkspaceWrite {
		roots = append(roots, p.WritableRoots...)
	}
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "TMPDIR="); ok && v != "" && v != "/tmp" {
			roots = append(roots, v)
		}
	}
	roots = append(roots, b.extra...)
	seen := map[string]bool{}
	var bound []string
	for _, r := range roots {
		r = resolve(r)
		if r == "" || r == "/" || seen[r] {
			continue
		}
		seen[r] = true
		// --bind-try: a root that does not exist yet is skipped, not fatal.
		args = append(args, "--bind-try", r, r)
		if _, err := os.Stat(r); err == nil {
			bound = append(bound, r)
		}
	}
	var protected []string
	for _, r := range p.ProtectedRoots {
		protected = append(protected, resolve(r))
	}
	plan, release := placeholders.Plan(protected, bound, acquire)
	for _, r := range plan.Present {
		args = append(args, "--ro-bind", r, r)
	}
	for _, r := range plan.Missing {
		args = append(args, "--tmpfs", r, "--remount-ro", r)
	}
	if cwd != "" {
		args = append(args, "--chdir", cwd)
	}
	return args, release
}

func (b *Bubblewrap) Confine(ctx context.Context, argv []string, cwd string, env []string, p sandbox.Policy) (sandbox.Confined, error) {
	p = p.Normalize()
	if err := sandbox.Validate(argv, p); err != nil {
		return sandbox.Confined{}, err
	}
	if p.Mode == sandbox.ModeFull {
		return sandbox.Unconfined(argv, env), nil
	}
	enf, err := b.Check(ctx, p)
	if err != nil {
		return sandbox.Confined{}, err
	}
	args, release := b.args(p, cwd, env, true)
	out := append([]string{b.binary}, args...)
	out = append(out, "--")
	out = append(out, argv...)
	c := sandbox.Confined{
		Argv:        out,
		Env:         append([]string(nil), env...),
		Enforcement: enf,
		Release:     release,
		BlockedSignatures: []string{
			"Read-only file system",
			"Permission denied",
			"bwrap:",
		},
	}
	if p.Network == sandbox.NetworkAllowlist {
		c.Limits = append(c.Limits, "bubblewrap cannot filter network by domain; allowlist is not enforced")
	}
	if p.Network == sandbox.NetworkNone {
		c.BlockedSignatures = append(c.BlockedSignatures, "Network is unreachable", "Could not resolve host", "Temporary failure in name resolution")
	}
	return c, nil
}

// resolve makes p absolute and resolves symlinks; a missing path keeps its
// cleaned form (bound with --bind-try).
func resolve(p string) string {
	if p == "" || !filepath.IsAbs(p) {
		return ""
	}
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	if _, err := os.Stat(filepath.Dir(p)); err == nil {
		if d, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
			return filepath.Join(d, filepath.Base(p))
		}
	}
	return p
}

var _ sandbox.Sandbox = (*Bubblewrap)(nil)

// Plugin provides `sandbox` via bubblewrap.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/sandbox-namespace",
	Description: "Linux bubblewrap sandbox: read-only filesystem except writable roots, network namespace unless open.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		return c.Provide(sandbox.ServiceKey, New(cfg))
	},
})
