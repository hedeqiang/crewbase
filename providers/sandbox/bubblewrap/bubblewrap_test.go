package bubblewrap_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/capabilities/sandbox"
	"github.com/hedeqiang/crewbase/providers/sandbox/bubblewrap"
)

func ready(t *testing.T) *bubblewrap.Bubblewrap {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}
	b := bubblewrap.New(bubblewrap.Config{})
	if _, err := b.Check(context.Background(), sandbox.Policy{Mode: sandbox.ModeReadOnly}); err != nil {
		t.Skipf("bubblewrap unavailable: %v", err)
	}
	return b
}

func run(t *testing.T, b *bubblewrap.Bubblewrap, cwd string, p sandbox.Policy, script string) (string, error) {
	t.Helper()
	c, err := b.Confine(context.Background(), []string{"/bin/sh", "-c", script}, cwd, os.Environ(), p)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(c.Argv[0], c.Argv[1:]...).CombinedOutput()
	return string(out), err
}

func TestWorkspaceWriteConfinesWrites(t *testing.T) {
	b := ready(t)
	// Inside the sandbox /tmp is a private tmpfs, so host dirs under /tmp
	// are invisible; use the home directory to test real confinement.
	home, _ := os.UserHomeDir()
	base, err := os.MkdirTemp(home, ".bwtest-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	ws, outside := filepath.Join(base, "ws"), filepath.Join(base, "outside")
	os.MkdirAll(ws, 0o755)
	os.MkdirAll(outside, 0o755)
	p := sandbox.Policy{Mode: sandbox.ModeWorkspaceWrite, WritableRoots: []string{ws}, Network: sandbox.NetworkNone}
	out, err := run(t, b, ws, p, "echo ok > in.txt && cat in.txt")
	if err != nil || strings.TrimSpace(out) != "ok" {
		t.Fatalf("write inside workspace: %q %v", out, err)
	}
	out, err = run(t, b, ws, p, "echo x > "+filepath.Join(outside, "x.txt"))
	if err == nil {
		t.Fatal("write outside the workspace must fail")
	}
	if _, serr := os.Stat(filepath.Join(outside, "x.txt")); serr == nil {
		t.Fatal("file created outside the workspace")
	}
	if !sandbox.MatchBlocked([]string{"Read-only file system", "Permission denied"}, out) {
		t.Fatalf("expected blocked signature, got %q", out)
	}
}

func TestReadOnlyAndNetwork(t *testing.T) {
	b := ready(t)
	ws := t.TempDir()
	if _, err := run(t, b, ws, sandbox.Policy{Mode: sandbox.ModeReadOnly, Network: sandbox.NetworkNone}, "touch x"); err == nil {
		t.Fatal("read-only must block writes in the workspace")
	}
	// no network namespace interfaces except lo
	out, _ := run(t, b, ws, sandbox.Policy{Mode: sandbox.ModeReadOnly, Network: sandbox.NetworkNone}, "cat /proc/net/dev")
	if strings.Contains(out, "eth0") {
		t.Fatalf("network none must isolate interfaces: %s", out)
	}
	if enf, _ := b.Check(context.Background(), sandbox.Policy{Mode: sandbox.ModeReadOnly, Network: sandbox.NetworkAllowlist, AllowHosts: []string{"x.org"}}); enf != sandbox.EnforcementPartial {
		t.Fatal(enf)
	}
}

func TestUnavailableOffLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("non-linux behaviour")
	}
	_, err := bubblewrap.New(bubblewrap.Config{}).Confine(context.Background(), []string{"true"}, "", nil, sandbox.Policy{Mode: sandbox.ModeWorkspaceWrite})
	if err == nil || !strings.Contains(err.Error(), "SANDBOX_UNAVAILABLE") {
		t.Fatal(err)
	}
}
