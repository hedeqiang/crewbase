// Package local is shell-local: the local-process provider of the
// `shell` capability. Every command whose policy is not "full" is wrapped by
// the `sandbox` service (Confine) first; if no sandbox is provided or it is
// unavailable the command is not run (fail closed). Commands run in their
// own process group, which is SIGKILLed on timeout, cancellation and after
// the command exits (no stray background processes). Output is capped per
// stream, keeping the head and the tail.
package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/sandbox"
	"github.com/hedeqiang/crewbase/capabilities/shell"
	"github.com/hedeqiang/crewbase/kernel"
)

// Defaults.
const (
	DefaultMaxOutput = 64 << 10
	DefaultTimeout   = 2 * time.Minute
	DefaultMaxTime   = 2 * time.Hour // background jobs; tools-basic caps foreground commands lower
	DefaultShell     = "/bin/sh"
)

// DefaultInheritEnv is the host environment passed to commands; everything
// else (API keys, tokens) stays out unless a request adds it.
var DefaultInheritEnv = []string{"PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "LC_CTYPE", "TERM", "TMPDIR", "SHELL", "TZ"}

// Config is the plugin config.
type Config struct {
	Shell          string   `json:"shell,omitempty"`          // default /bin/sh
	MaxOutput      int      `json:"maxOutput,omitempty"`      // bytes per stream, default 64KiB
	DefaultTimeout string   `json:"defaultTimeout,omitempty"` // Go duration, default 2m
	MaxTimeout     string   `json:"maxTimeout,omitempty"`     // Go duration, default 30m
	InheritEnv     []string `json:"inheritEnv,omitempty"`     // host env keys passed through
}

// Local implements shell.Shell.
type Local struct {
	shell      string
	maxOutput  int
	timeout    time.Duration
	maxTimeout time.Duration
	inherit    []string
	// sandbox resolves the current sandbox service (nil = none provided).
	sandbox func() sandbox.Sandbox
}

// New creates a provider; sb resolves the sandbox service per call.
func New(cfg Config, sb func() sandbox.Sandbox) (*Local, error) {
	l := &Local{shell: cfg.Shell, maxOutput: cfg.MaxOutput, inherit: cfg.InheritEnv, sandbox: sb,
		timeout: DefaultTimeout, maxTimeout: DefaultMaxTime}
	if l.shell == "" {
		l.shell = DefaultShell
	}
	if l.maxOutput <= 0 {
		l.maxOutput = DefaultMaxOutput
	}
	if l.inherit == nil {
		l.inherit = DefaultInheritEnv
	}
	if l.sandbox == nil {
		l.sandbox = func() sandbox.Sandbox { return nil }
	}
	var err error
	if cfg.DefaultTimeout != "" {
		if l.timeout, err = time.ParseDuration(cfg.DefaultTimeout); err != nil {
			return nil, fmt.Errorf("shell-local: defaultTimeout: %w", err)
		}
	}
	if cfg.MaxTimeout != "" {
		if l.maxTimeout, err = time.ParseDuration(cfg.MaxTimeout); err != nil {
			return nil, fmt.Errorf("shell-local: maxTimeout: %w", err)
		}
	}
	return l, nil
}

func (l *Local) baseEnv() []string {
	keep := map[string]bool{}
	for _, k := range l.inherit {
		keep[k] = true
	}
	var out []string
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok && keep[k] {
			out = append(out, kv)
		}
	}
	return out
}

// mergeEnv appends extra over base; later keys win.
func mergeEnv(base, extra []string) []string {
	idx := map[string]int{}
	var out []string
	for _, kv := range append(append([]string(nil), base...), extra...) {
		k, _, _ := strings.Cut(kv, "=")
		if i, ok := idx[k]; ok {
			out[i] = kv
			continue
		}
		idx[k] = len(out)
		out = append(out, kv)
	}
	return out
}

// Exec runs one command. See shell.Shell.
func (l *Local) Exec(ctx context.Context, req shell.ExecRequest) (shell.RunResult, error) {
	argv := req.Argv
	if len(argv) == 0 {
		if strings.TrimSpace(req.Command) == "" {
			return shell.RunResult{}, shell.ErrEmpty
		}
		argv = []string{l.shell, "-c", req.Command}
	}
	if req.Cwd == "" || !filepath.IsAbs(req.Cwd) {
		return shell.RunResult{}, fmt.Errorf("shell-local: cwd must be an absolute path, got %q", req.Cwd)
	}
	if st, err := os.Stat(req.Cwd); err != nil || !st.IsDir() {
		return shell.RunResult{}, fmt.Errorf("shell-local: cwd is not a directory: %s", req.Cwd)
	}
	env := mergeEnv(l.baseEnv(), req.Env)
	pol := req.Policy.Normalize()

	conf := sandbox.Unconfined(argv, env)
	if pol.Mode != sandbox.ModeFull {
		sb := l.sandbox()
		if sb == nil {
			return shell.RunResult{}, sandbox.Unavailable("no sandbox provider is loaded; refusing to run a %s command unconfined", pol.Mode)
		}
		var err error
		conf, err = sb.Confine(ctx, argv, req.Cwd, env, pol)
		if err != nil {
			return shell.RunResult{}, err
		}
		if conf.Release != nil {
			defer conf.Release() // run waits for the command to exit
		}
		if conf.Enforcement == "" || conf.Enforcement == sandbox.EnforcementNone {
			return shell.RunResult{}, sandbox.Unavailable("sandbox %s returned no enforcement for a %s policy", sb.Name(), pol.Mode)
		}
		if len(conf.Argv) == 0 {
			return shell.RunResult{}, errors.New("shell-local: sandbox returned an empty command")
		}
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = l.timeout
	}
	if timeout > l.maxTimeout {
		timeout = l.maxTimeout
	}
	max := req.MaxOutput
	if max <= 0 || max > l.maxOutput {
		max = l.maxOutput
	}
	return l.run(ctx, conf, req.Cwd, timeout, max, req.Output)
}

// tee forwards writes to a live-output callback.
type tee struct {
	w      io.Writer
	stream string
	fn     func(string, []byte)
}

func (t tee) Write(p []byte) (int, error) {
	t.fn(t.stream, append([]byte(nil), p...))
	return t.w.Write(p)
}

func (l *Local) run(ctx context.Context, conf sandbox.Confined, cwd string, timeout time.Duration, max int, live func(string, []byte)) (shell.RunResult, error) {
	res := shell.RunResult{Enforcement: conf.Enforcement, Limits: conf.Limits, ExitCode: -1}
	if ctx.Err() != nil {
		res.Cancelled = true
		return res, nil
	}
	cmd := exec.Command(conf.Argv[0], conf.Argv[1:]...)
	cmd.Dir = cwd
	cmd.Env = conf.Env
	setGroup(cmd)

	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return res, err
	}
	defer devnull.Close()
	cmd.Stdin = devnull
	outR, outW, err := os.Pipe()
	if err != nil {
		return res, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		outR.Close()
		outW.Close()
		return res, err
	}
	cmd.Stdout, cmd.Stderr = outW, errW

	stdout, stderr := newCapBuf(max), newCapBuf(max)
	start := time.Now()
	if err := cmd.Start(); err != nil {
		outR.Close()
		outW.Close()
		errR.Close()
		errW.Close()
		return res, fmt.Errorf("shell-local: start: %w", err)
	}
	afterStart(cmd)
	outW.Close()
	errW.Close()
	pid := cmd.Process.Pid

	var readers sync.WaitGroup
	readers.Add(2)
	var so, se io.Writer = stdout, stderr
	if live != nil {
		so, se = tee{stdout, "stdout", live}, tee{stderr, "stderr", live}
	}
	go func() { defer readers.Done(); _, _ = io.Copy(so, outR) }()
	go func() { defer readers.Done(); _, _ = io.Copy(se, errR) }()

	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var werr error
	select {
	case werr = <-waited:
	case <-timer.C:
		res.TimedOut = true
	case <-ctx.Done():
		res.Cancelled = true
	}
	if res.TimedOut || res.Cancelled {
		killGroup(pid)
		if conf.Kill != nil {
			conf.Kill()
		}
		werr = <-waited
	}
	// Reap the rest of the tree: nothing outlives the command.
	killGroup(pid)

	drained := make(chan struct{})
	go func() { readers.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(2 * time.Second):
		// a process escaped the group and still holds a pipe
	}
	outR.Close()
	errR.Close()
	<-drained

	res.Duration = time.Since(start)
	if cmd.ProcessState != nil && !res.TimedOut && !res.Cancelled {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	var ee *exec.ExitError
	if werr != nil && !errors.As(werr, &ee) && !res.TimedOut && !res.Cancelled {
		return res, fmt.Errorf("shell-local: wait: %w", werr)
	}
	res.Stdout, res.Stderr = stdout.String(), stderr.String()
	res.Truncated = stdout.truncated() || stderr.truncated()
	if res.ExitCode != 0 && conf.Enforcement != sandbox.EnforcementNone {
		res.Blocked = sandbox.MatchBlocked(conf.BlockedSignatures, res.Stderr, res.Stdout)
	}
	return res, nil
}

// capBuf keeps the first and last bytes of a stream up to max in total.
type capBuf struct {
	mu      sync.Mutex
	headMax int
	tailMax int
	head    []byte
	tail    []byte
	total   int64
}

func newCapBuf(max int) *capBuf {
	t := max / 4
	return &capBuf{headMax: max - t, tailMax: t}
}

func (c *capBuf) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)
	c.total += int64(n)
	if room := c.headMax - len(c.head); room > 0 {
		k := min(room, len(p))
		c.head = append(c.head, p[:k]...)
		p = p[k:]
	}
	if len(p) > 0 && c.tailMax > 0 {
		c.tail = append(c.tail, p...)
		if len(c.tail) > 2*c.tailMax {
			c.tail = append(c.tail[:0], c.tail[len(c.tail)-c.tailMax:]...)
		}
	}
	return n, nil
}

func (c *capBuf) kept() int64 {
	return int64(len(c.head) + min(len(c.tail), c.tailMax))
}

func (c *capBuf) truncated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total > c.kept()
}

func (c *capBuf) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	tail := c.tail
	if len(tail) > c.tailMax {
		tail = tail[len(tail)-c.tailMax:]
	}
	if c.total <= int64(len(c.head)+len(tail)) {
		return string(c.head) + string(tail)
	}
	var b bytes.Buffer
	b.Write(c.head)
	fmt.Fprintf(&b, "\n…[%d bytes truncated]…\n", c.total-int64(len(c.head)+len(tail)))
	b.Write(tail)
	return b.String()
}

var _ shell.Shell = (*Local)(nil)

// Plugin provides `shell` running local processes through `sandbox`.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/shell-local",
	Description: "Local command execution; confined by the sandbox service unless the policy is full (host mode).",
	Optional:    []string{sandbox.ServiceKey},
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		l, err := New(cfg, func() sandbox.Sandbox {
			sb, _ := kernel.Lookup[sandbox.Sandbox](c, sandbox.ServiceKey)
			return sb
		})
		if err != nil {
			return err
		}
		return c.Provide(shell.ServiceKey, l)
	},
})
