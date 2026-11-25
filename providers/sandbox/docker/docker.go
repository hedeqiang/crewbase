// Package docker is sandbox-docker: the container provider of the
// `sandbox` capability. Each command runs in a fresh `docker run --rm`
// container with --security-opt no-new-privileges:true, --shm-size 1g, a
// pids limit, and the workspace bind-mounted at the same path:
//
//   - read-only: cwd mounted read-only, root filesystem read-only (+ tmpfs /tmp);
//   - workspace-write: writable roots mounted read-write;
//   - network none → --network none; open → default bridge; allowlist →
//     default bridge reported as enforcement "partial" (no egress proxy yet).
//
// The daemon is probed before use; if it is down Confine returns
// SANDBOX_UNAVAILABLE. It never falls back to running on the host.
package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/sandbox"
	"github.com/hedeqiang/crewbase/internal/netproxy"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/sandbox/internal/protect"
)

// Defaults.
const (
	DefaultImage     = "debian:stable-slim"
	DefaultShmSize   = "1g"
	DefaultPidsLimit = 512
)

// Config is the plugin config.
type Config struct {
	Binary    string `json:"binary,omitempty"`    // docker CLI; default: PATH, then ~/.local/bin/docker
	Image     string `json:"image,omitempty"`     // default debian:stable-slim
	ShmSize   string `json:"shmSize,omitempty"`   // default 1g
	PidsLimit int    `json:"pidsLimit,omitempty"` // default 512
	Memory    string `json:"memory,omitempty"`    // e.g. "2g"; empty = unlimited
	CPUs      string `json:"cpus,omitempty"`      // e.g. "2"
	// User is passed as --user; default on Linux is the host uid:gid so
	// files written to the workspace keep their owner.
	User string `json:"user,omitempty"`
	// SeccompProfile is an optional path passed as --security-opt seccomp=.
	SeccompProfile string `json:"seccompProfile,omitempty"`
}

// Docker implements sandbox.Sandbox.
type Docker struct {
	cfg Config

	mu      sync.Mutex
	probed  time.Time
	probeE  error
	probeOK bool
	// run executes a short docker CLI command (tests override it).
	run func(ctx context.Context, args ...string) ([]byte, error)
}

// New creates a provider.
func New(cfg Config) *Docker {
	if cfg.Binary == "" {
		cfg.Binary = findDocker()
	}
	if cfg.Image == "" {
		cfg.Image = DefaultImage
	}
	if cfg.ShmSize == "" {
		cfg.ShmSize = DefaultShmSize
	}
	if cfg.PidsLimit == 0 {
		cfg.PidsLimit = DefaultPidsLimit
	}
	if cfg.User == "" && runtime.GOOS == "linux" {
		cfg.User = fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	}
	d := &Docker{cfg: cfg}
	d.run = func(ctx context.Context, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, d.cfg.Binary, args...)
		cmd.Env = cliEnv()
		return cmd.CombinedOutput()
	}
	return d
}

func findDocker() string {
	if p, err := exec.LookPath("docker"); err == nil {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".local", "bin", "docker")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "docker"
}

func (d *Docker) Name() string { return "docker" }

// Image returns the configured image.
func (d *Docker) Image() string { return d.cfg.Image }

// Available probes the daemon. Success is cached for 30s, failure for 3s.
func (d *Docker) Available(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	ttl := 3 * time.Second
	if d.probeOK {
		ttl = 30 * time.Second
	}
	if !d.probed.IsZero() && time.Since(d.probed) < ttl {
		return d.probeE
	}
	pctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	out, err := d.run(pctx, "info", "--format", "{{.ServerVersion}}")
	d.probed = time.Now()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		d.probeOK = false
		d.probeE = sandbox.Unavailable("docker daemon not available (%s): %s", errString(err), firstLine(string(out)))
		return d.probeE
	}
	d.probeOK, d.probeE = true, nil
	return nil
}

func errString(err error) string {
	if err == nil {
		return "no server version"
	}
	return err.Error()
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func (d *Docker) Check(ctx context.Context, p sandbox.Policy) (string, error) {
	p = p.Normalize()
	if p.Mode == sandbox.ModeFull {
		return sandbox.EnforcementNone, nil
	}
	if err := d.Available(ctx); err != nil {
		return "", err
	}
	if p.Network == sandbox.NetworkAllowlist {
		return sandbox.EnforcementPartial, nil
	}
	return sandbox.EnforcementFull, nil
}

// Args builds the `docker run` arguments (without the binary) and the env
// keys forwarded into the container. Exported for tests.
func (d *Docker) Args(name string, argv []string, cwd string, env []string, p sandbox.Policy) ([]string, error) {
	args, _, err := d.args(name, argv, cwd, env, p, false)
	return args, err
}

// placeholders tracks the mount points made for missing protected names.
var placeholders protect.Registry

// args builds the arguments; with acquire, release frees the placeholders
// (see Confined.Release).
func (d *Docker) args(name string, argv []string, cwd string, env []string, p sandbox.Policy, acquire bool) (args []string, release func(), err error) {
	defer func() {
		if err != nil && release != nil {
			release()
			release = nil
		}
	}()
	p = p.Normalize()
	if !filepath.IsAbs(cwd) {
		return nil, nil, fmt.Errorf("sandbox-docker: cwd must be absolute: %q", cwd)
	}
	if strings.HasPrefix(d.cfg.Image, "-") || d.cfg.Image == "" {
		return nil, nil, fmt.Errorf("sandbox-docker: invalid image %q", d.cfg.Image)
	}
	cwd = filepath.Clean(cwd)
	args = []string{"run", "--rm", "--init", "--name", name,
		"--security-opt", "no-new-privileges:true",
		"--shm-size", d.cfg.ShmSize,
		"--pids-limit", strconv.Itoa(d.cfg.PidsLimit),
	}
	if d.cfg.SeccompProfile != "" {
		args = append(args, "--security-opt", "seccomp="+d.cfg.SeccompProfile)
	}
	if d.cfg.Memory != "" {
		args = append(args, "--memory", d.cfg.Memory)
	}
	if d.cfg.CPUs != "" {
		args = append(args, "--cpus", d.cfg.CPUs)
	}
	if d.cfg.User != "" {
		args = append(args, "--user", d.cfg.User)
	}
	switch p.Network {
	case sandbox.NetworkNone:
		args = append(args, "--network", "none")
	default:
		args = append(args, "--network", "bridge")
	}
	cwdMounted := false
	if p.Mode == sandbox.ModeWorkspaceWrite {
		var bound []string
		for _, r := range p.WritableRoots {
			r = filepath.Clean(r)
			if strings.Contains(r, ":") || strings.Contains(r, ",") {
				return nil, nil, fmt.Errorf("sandbox-docker: unsupported character in path %q", r)
			}
			args = append(args, "--mount", "type=bind,source="+r+",target="+r)
			bound = append(bound, r)
			if within(cwd, r) {
				cwdMounted = true
			}
		}
		// Protected roots inside a writable root stay read-only: a present
		// one is bound read-only over itself, a missing one gets an empty
		// read-only tmpfs so it cannot be created (the mount point shows in
		// the host folder as an empty directory until Release). Docker
		// mounts parents before children whatever the argument order, but
		// they follow the workspace here anyway.
		var protected []string
		for _, r := range p.ProtectedRoots {
			r = filepath.Clean(r)
			if !filepath.IsAbs(r) {
				continue
			}
			if strings.Contains(r, ":") || strings.Contains(r, ",") {
				return nil, nil, fmt.Errorf("sandbox-docker: unsupported character in path %q", r)
			}
			protected = append(protected, r)
		}
		plan, rel := placeholders.Plan(protected, bound, acquire)
		release = rel
		for _, r := range plan.Present {
			args = append(args, "--mount", "type=bind,source="+r+",target="+r+",readonly")
		}
		for _, r := range plan.Missing {
			if acquire {
				// the empty placeholder is made on the host first and bound
				// read-only: a --tmpfs on a mount point Docker has to create
				// was sometimes silently not mounted (Docker Desktop /
				// OrbStack file sharing, right after the last placeholder was
				// removed), and the command could then create the name
				if err := os.Mkdir(r, 0o755); err != nil && !os.IsExist(err) {
					return nil, nil, fmt.Errorf("sandbox-docker: placeholder %s: %w", r, err)
				}
				args = append(args, "--mount", "type=bind,source="+r+",target="+r+",readonly")
				continue
			}
			args = append(args, "--tmpfs", r+":ro,size=4k")
		}
	} else {
		args = append(args, "--read-only", "--tmpfs", "/tmp:rw,size=256m")
	}
	if !cwdMounted {
		if strings.Contains(cwd, ",") {
			return nil, nil, fmt.Errorf("sandbox-docker: unsupported character in path %q", cwd)
		}
		args = append(args, "--mount", "type=bind,source="+cwd+",target="+cwd+",readonly")
	}
	args = append(args, "-w", cwd)
	for _, k := range forwardKeys(env) {
		args = append(args, "-e", k) // value comes from the CLI env, not argv
	}
	args = append(args, d.cfg.Image)
	args = append(args, argv...)
	return args, release, nil
}

func within(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// hostOnly env keys describe the host and are not forwarded into the container.
var hostOnly = map[string]bool{"PATH": true, "HOME": true, "TMPDIR": true, "SHELL": true, "USER": true, "LOGNAME": true, "PWD": true, "OLDPWD": true, "SHLVL": true, "_": true}

func forwardKeys(env []string) []string {
	var keys []string
	seen := map[string]bool{}
	for _, kv := range env {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || k == "" || hostOnly[k] || strings.HasPrefix(k, "DOCKER_") || seen[k] {
			continue
		}
		seen[k] = true
		keys = append(keys, k)
	}
	return keys
}

// cliEnv is the environment the docker CLI itself needs.
func cliEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "PATH" || k == "HOME" || k == "USER" || k == "TMPDIR" || strings.HasPrefix(k, "DOCKER_") || k == "XDG_RUNTIME_DIR" {
			out = append(out, kv)
		}
	}
	return out
}

func newName() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "crewbase-sbx-" + hex.EncodeToString(b[:])
}

func (d *Docker) Confine(ctx context.Context, argv []string, cwd string, env []string, p sandbox.Policy) (sandbox.Confined, error) {
	p = p.Normalize()
	if err := sandbox.Validate(argv, p); err != nil {
		return sandbox.Confined{}, err
	}
	if p.Mode == sandbox.ModeFull {
		return sandbox.Unconfined(argv, env), nil
	}
	enf, err := d.Check(ctx, p)
	if err != nil {
		return sandbox.Confined{}, err
	}
	name := newName()
	args, release, err := d.args(name, argv, cwd, env, p, true)
	if err != nil {
		return sandbox.Confined{}, err
	}
	cenv := cliEnv()
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if !hostOnly[k] && !strings.HasPrefix(k, "DOCKER_") {
			// a proxy on 127.0.0.1 is this machine, not the container
			cenv = append(cenv, netproxy.ForContainer(kv))
		}
	}
	c := sandbox.Confined{
		Argv:        append([]string{d.cfg.Binary}, args...),
		Env:         cenv,
		Enforcement: enf,
		Release:     release,
		BlockedSignatures: []string{
			"Read-only file system",
		},
		Kill: func() {
			kctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, _ = d.run(kctx, "kill", name)
		},
	}
	if p.Network == sandbox.NetworkNone {
		c.BlockedSignatures = append(c.BlockedSignatures,
			"Could not resolve host", "Temporary failure in name resolution",
			"Network is unreachable", "network is unreachable")
	}
	if p.Network == sandbox.NetworkAllowlist {
		c.Limits = append(c.Limits, "no egress proxy: allowlist is not filtered by domain")
	}
	return c, nil
}

// ErrBadConfig is returned for invalid plugin config.
var ErrBadConfig = errors.New("sandbox-docker: invalid config")

var _ sandbox.Sandbox = (*Docker)(nil)

// Plugin provides `sandbox` via Docker containers.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/sandbox-docker",
	Description: "Docker container sandbox (no-new-privileges, workspace bind mount, --network none); fails closed when the daemon is down.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrBadConfig, err)
		}
		return c.Provide(sandbox.ServiceKey, New(cfg))
	},
})
