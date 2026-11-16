//go:build !windows

package local_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/sandbox"
	"github.com/hedeqiang/crewbase/capabilities/shell"
	"github.com/hedeqiang/crewbase/kernel"
	sandboxnone "github.com/hedeqiang/crewbase/providers/sandbox/none"
	sandboxseatbelt "github.com/hedeqiang/crewbase/providers/sandbox/seatbelt"
	shelllocal "github.com/hedeqiang/crewbase/providers/shell/local"
)

var full = sandbox.Policy{Mode: sandbox.ModeFull}

func newLocal(t *testing.T, cfg shelllocal.Config, sb sandbox.Sandbox) *shelllocal.Local {
	t.Helper()
	l, err := shelllocal.New(cfg, func() sandbox.Sandbox { return sb })
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestExecBasics(t *testing.T) {
	l := newLocal(t, shelllocal.Config{}, nil)
	dir := t.TempDir()
	ctx := context.Background()
	r, err := l.Exec(ctx, shell.ExecRequest{Command: "pwd; echo err >&2; exit 3", Cwd: dir, Policy: full})
	if err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(dir)
	if strings.TrimSpace(r.Stdout) != real || r.Stderr != "err\n" || r.ExitCode != 3 || r.Enforcement != sandbox.EnforcementNone || r.Blocked {
		t.Fatalf("%+v", r)
	}
	r, err = l.Exec(ctx, shell.ExecRequest{Argv: []string{"echo", "a b"}, Cwd: dir, Policy: full})
	if err != nil || r.Stdout != "a b\n" || r.ExitCode != 0 {
		t.Fatal(r, err)
	}
	if _, err := l.Exec(ctx, shell.ExecRequest{Cwd: dir, Policy: full}); !errors.Is(err, shell.ErrEmpty) {
		t.Fatal(err)
	}
	if _, err := l.Exec(ctx, shell.ExecRequest{Command: "true", Cwd: "rel", Policy: full}); err == nil {
		t.Fatal("relative cwd")
	}
	if _, err := l.Exec(ctx, shell.ExecRequest{Command: "true", Cwd: filepath.Join(dir, "missing"), Policy: full}); err == nil {
		t.Fatal("missing cwd")
	}
	if _, err := l.Exec(ctx, shell.ExecRequest{Argv: []string{"/nonexistent/prog"}, Cwd: dir, Policy: full}); err == nil {
		t.Fatal("start failure is an error")
	}
}

func TestEnvFiltering(t *testing.T) {
	t.Setenv("CREWBASE_TEST_SECRET", "hunter2")
	l := newLocal(t, shelllocal.Config{}, nil)
	r, err := l.Exec(context.Background(), shell.ExecRequest{Command: "env", Cwd: t.TempDir(), Env: []string{"EXTRA=1", "PATH=/usr/bin:/bin"}, Policy: full})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.Stdout, "hunter2") {
		t.Fatal("non-allowlisted host env leaked")
	}
	if !strings.Contains(r.Stdout, "EXTRA=1") || !strings.Contains(r.Stdout, "PATH=/usr/bin:/bin\n") || strings.Count(r.Stdout, "PATH=") != 1 {
		t.Fatal(r.Stdout)
	}
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func TestTimeoutKillsProcessGroup(t *testing.T) {
	l := newLocal(t, shelllocal.Config{}, nil)
	dir := t.TempDir()
	pidf := filepath.Join(dir, "pid")
	start := time.Now()
	r, err := l.Exec(context.Background(), shell.ExecRequest{
		Command: "sleep 30 & echo $! > " + pidf + "; sleep 30", Cwd: dir, Timeout: 300 * time.Millisecond, Policy: full,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !r.TimedOut || r.ExitCode != -1 || time.Since(start) > 5*time.Second {
		t.Fatalf("%+v after %s", r, time.Since(start))
	}
	assertDead(t, pidf)
}

func assertDead(t *testing.T, pidf string) {
	t.Helper()
	b, err := os.ReadFile(pidf)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	deadline := time.Now().Add(2 * time.Second)
	for alive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if alive(pid) {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Fatal("background child survived")
	}
}

func TestCancelAndBackgroundCleanup(t *testing.T) {
	l := newLocal(t, shelllocal.Config{}, nil)
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	r, err := l.Exec(ctx, shell.ExecRequest{Command: "sleep 30", Cwd: dir, Policy: full})
	if err != nil || !r.Cancelled {
		t.Fatal(r, err)
	}
	// a background process left behind by a finished command is reaped
	pidf := filepath.Join(dir, "bg")
	start := time.Now()
	r, err = l.Exec(context.Background(), shell.ExecRequest{Command: "sleep 30 & echo $! > " + pidf + "; echo done", Cwd: dir, Policy: full})
	if err != nil || r.Stdout != "done\n" || r.ExitCode != 0 || time.Since(start) > 5*time.Second {
		t.Fatal(r, err, time.Since(start))
	}
	assertDead(t, pidf)
	// already-cancelled context never starts the command
	marker := filepath.Join(dir, "never")
	r, _ = l.Exec(ctx, shell.ExecRequest{Command: "touch " + marker, Cwd: dir, Policy: full})
	if _, err := os.Stat(marker); err == nil || !r.Cancelled {
		t.Fatal("ran after cancel")
	}
}

func TestOutputCap(t *testing.T) {
	l := newLocal(t, shelllocal.Config{MaxOutput: 1000}, nil)
	r, err := l.Exec(context.Background(), shell.ExecRequest{Command: "printf 'HEAD'; head -c 5000 /dev/zero | tr '\\0' x; printf 'TAIL'", Cwd: t.TempDir(), Policy: full})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Truncated || !strings.HasPrefix(r.Stdout, "HEAD") || !strings.HasSuffix(r.Stdout, "TAIL") || len(r.Stdout) > 1100 || !strings.Contains(r.Stdout, "bytes truncated") {
		t.Fatalf("truncated=%v len=%d %q…", r.Truncated, len(r.Stdout), r.Stdout[:20])
	}
	r, _ = l.Exec(context.Background(), shell.ExecRequest{Command: "echo short", Cwd: t.TempDir(), Policy: full, MaxOutput: 10})
	if r.Truncated || r.Stdout != "short\n" {
		t.Fatal(r)
	}
}

// fakeSandbox records calls; err makes it unavailable.
type fakeSandbox struct {
	err   error
	calls int
	sigs  []string
}

func (f *fakeSandbox) Name() string { return "fake" }
func (f *fakeSandbox) Check(context.Context, sandbox.Policy) (string, error) {
	return sandbox.EnforcementFull, f.err
}
func (f *fakeSandbox) Confine(_ context.Context, argv []string, _ string, env []string, _ sandbox.Policy) (sandbox.Confined, error) {
	f.calls++
	if f.err != nil {
		return sandbox.Confined{}, f.err
	}
	return sandbox.Confined{Argv: argv, Env: env, Enforcement: sandbox.EnforcementFull, BlockedSignatures: f.sigs}, nil
}

func TestSandboxUnavailableDoesNotRun(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	req := shell.ExecRequest{Command: "touch " + marker, Cwd: dir,
		Policy: sandbox.Policy{Mode: sandbox.ModeWorkspaceWrite, WritableRoots: []string{dir}}}
	for name, sb := range map[string]sandbox.Sandbox{
		"no provider":  nil,
		"unavailable":  &fakeSandbox{err: sandbox.Unavailable("docker daemon down")},
		"sandbox-none": sandboxnone.None{},
	} {
		l := newLocal(t, shelllocal.Config{}, sb)
		_, err := l.Exec(context.Background(), req)
		if !errors.Is(err, sandbox.ErrUnavailable) {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Fatalf("%s: command ran without a sandbox", name)
		}
	}
	// a provider that claims enforcement none for a confined policy is refused too
	l := newLocal(t, shelllocal.Config{}, liar{})
	if _, err := l.Exec(context.Background(), req); !errors.Is(err, sandbox.ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("ran under a lying sandbox")
	}
}

// releaser checks that Release runs once, after the command finished.
type releaser struct {
	fakeSandbox
	marker   string
	released int
	ranFirst bool
}

func (r *releaser) Confine(ctx context.Context, argv []string, cwd string, env []string, p sandbox.Policy) (sandbox.Confined, error) {
	c, err := r.fakeSandbox.Confine(ctx, argv, cwd, env, p)
	c.Release = func() {
		r.released++
		_, err := os.Stat(r.marker)
		r.ranFirst = err == nil
	}
	return c, err
}

func TestSandboxReleaseAfterExit(t *testing.T) {
	dir := t.TempDir()
	sb := &releaser{marker: filepath.Join(dir, "done")}
	l := newLocal(t, shelllocal.Config{}, sb)
	ws := sandbox.Policy{Mode: sandbox.ModeWorkspaceWrite, WritableRoots: []string{dir}}
	if _, err := l.Exec(context.Background(), shell.ExecRequest{Command: "sleep 0.1; touch " + sb.marker, Cwd: dir, Policy: ws}); err != nil {
		t.Fatal(err)
	}
	if sb.released != 1 || !sb.ranFirst {
		t.Fatalf("Release ran %d times, after the command: %v", sb.released, sb.ranFirst)
	}
}

type liar struct{}

func (liar) Name() string { return "liar" }
func (liar) Check(context.Context, sandbox.Policy) (string, error) {
	return sandbox.EnforcementNone, nil
}
func (liar) Confine(_ context.Context, argv []string, _ string, env []string, _ sandbox.Policy) (sandbox.Confined, error) {
	return sandbox.Unconfined(argv, env), nil
}

func TestBlockedSignature(t *testing.T) {
	fs := &fakeSandbox{sigs: []string{"Operation not permitted"}}
	l := newLocal(t, shelllocal.Config{}, fs)
	dir := t.TempDir()
	ws := sandbox.Policy{Mode: sandbox.ModeWorkspaceWrite, WritableRoots: []string{dir}}
	r, err := l.Exec(context.Background(), shell.ExecRequest{Command: "echo 'x: Operation not permitted' >&2; exit 1", Cwd: dir, Policy: ws})
	if err != nil || !r.Blocked || fs.calls != 1 || r.Enforcement != sandbox.EnforcementFull {
		t.Fatal(r, err)
	}
	r, _ = l.Exec(context.Background(), shell.ExecRequest{Command: "echo 'Operation not permitted'; exit 0", Cwd: dir, Policy: ws})
	if r.Blocked {
		t.Fatal("successful command is never blocked")
	}
	r, _ = l.Exec(context.Background(), shell.ExecRequest{Command: "echo 'syntax error' >&2; exit 2", Cwd: dir, Policy: ws})
	if r.Blocked {
		t.Fatal("broken command is not blocked")
	}
	// full mode bypasses the sandbox
	l.Exec(context.Background(), shell.ExecRequest{Command: "true", Cwd: dir, Policy: full})
	if fs.calls != 3 {
		t.Fatal("full mode must not call Confine", fs.calls)
	}
}

// TestSeatbeltEndToEnd runs a real sandbox-exec through shell-local.
func TestSeatbeltEndToEnd(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS only")
	}
	wd, _ := os.Getwd()
	skipIfUnderTemp(t, wd)
	base, err := os.MkdirTemp(wd, ".shtest-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	ws, outside := filepath.Join(base, "ws"), filepath.Join(base, "out")
	os.MkdirAll(ws, 0o755)
	os.MkdirAll(outside, 0o755)
	l := newLocal(t, shelllocal.Config{}, sandboxseatbelt.New(sandboxseatbelt.Config{}))
	p := sandbox.PolicyFor("sandbox", ws, nil)
	r, err := l.Exec(context.Background(), shell.ExecRequest{Command: "echo ok > in.txt && echo bad > " + filepath.Join(outside, "x.txt"), Cwd: ws, Policy: p})
	if err != nil {
		t.Fatal(err)
	}
	if r.ExitCode == 0 || !r.Blocked || r.Enforcement != sandbox.EnforcementFull {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(filepath.Join(ws, "in.txt")); err != nil {
		t.Fatal("inside write failed")
	}
	if _, err := os.Stat(filepath.Join(outside, "x.txt")); err == nil {
		t.Fatal("outside write succeeded")
	}
}

func TestPlugin(t *testing.T) {
	h := kernel.NewHost(nil)
	defer h.Close()
	if _, err := h.Load(shelllocal.Plugin, kernel.LoadOptions{Config: map[string]any{"defaultTimeout": "5s"}}); err != nil {
		t.Fatal(err)
	}
	sh := kernel.Use[shell.Shell](h.Root(), shell.ServiceKey)
	dir := t.TempDir()
	// no sandbox loaded: confined commands fail closed
	if _, err := sh.Exec(context.Background(), shell.ExecRequest{Command: "true", Cwd: dir, Policy: sandbox.PolicyFor("sandbox", dir, nil)}); !errors.Is(err, sandbox.ErrUnavailable) {
		t.Fatal(err)
	}
	// the sandbox is looked up per call
	h.Load(sandboxnone.Plugin, kernel.LoadOptions{})
	if r, err := sh.Exec(context.Background(), shell.ExecRequest{Command: "echo hi", Cwd: dir, Policy: full}); err != nil || r.Stdout != "hi\n" {
		t.Fatal(r, err)
	}
	if _, err := h.Load(kernel.Spec{Name: "bad-shell", Apply: shelllocal.Plugin.Apply}, kernel.LoadOptions{Config: map[string]any{"defaultTimeout": "soon"}}); err != nil {
		t.Fatal(err)
	}
	if p, _ := h.Plugin("bad-shell"); p.State != kernel.StateFailed {
		t.Fatal("bad config must fail", p)
	}
}

// skipIfUnderTemp: the sandbox profile deliberately leaves temp dirs
// writable, so "outside the workspace" checks need a checkout elsewhere.
func skipIfUnderTemp(t *testing.T, dir string) {
	t.Helper()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		real = dir
	}
	for _, tmp := range []string{os.TempDir(), "/tmp", "/private/tmp", "/var/folders", "/private/var/folders"} {
		if r, err := filepath.EvalSymlinks(tmp); err == nil {
			tmp = r
		}
		if strings.HasPrefix(real+"/", strings.TrimSuffix(tmp, "/")+"/") {
			t.Skip("checkout is under a temp dir, which the sandbox leaves writable")
		}
	}
}
