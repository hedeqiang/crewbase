package seatbelt_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/capabilities/sandbox"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/sandbox/seatbelt"
)

func darwin(t *testing.T) *seatbelt.Seatbelt {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("seatbelt is macOS only")
	}
	sb := seatbelt.New(seatbelt.Config{})
	if _, err := sb.Check(context.Background(), sandbox.Policy{Mode: sandbox.ModeReadOnly}); err != nil {
		t.Fatalf("sandbox-exec should work on darwin: %v", err)
	}
	return sb
}

// run executes a confined /bin/sh -c script and returns combined output.
func run(t *testing.T, sb *seatbelt.Seatbelt, cwd, script string, p sandbox.Policy) (string, error, sandbox.Confined) {
	t.Helper()
	c, err := sb.Confine(context.Background(), []string{"/bin/sh", "-c", script}, cwd, os.Environ(), p)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(c.Argv[0], c.Argv[1:]...)
	cmd.Dir, cmd.Env = cwd, c.Env
	out, err := cmd.CombinedOutput()
	return string(out), err, c
}

// dirs creates test directories outside the temp dirs (which the profile
// deliberately leaves writable).
func dirs(t *testing.T) (ws, outside string) {
	wd, _ := os.Getwd()
	skipIfUnderTemp(t, wd)
	base, err := os.MkdirTemp(wd, ".sbtest-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	ws, outside = filepath.Join(base, "ws"), filepath.Join(base, "outside")
	os.MkdirAll(ws, 0o755)
	os.MkdirAll(outside, 0o755)
	return
}

func TestWriteOutsideWorkspaceBlocked(t *testing.T) {
	sb := darwin(t)
	ws, outside := dirs(t)
	p := sandbox.Policy{Mode: sandbox.ModeWorkspaceWrite, WritableRoots: []string{ws}, Network: sandbox.NetworkNone}

	out, err, c := run(t, sb, ws, "echo in > "+filepath.Join(ws, "a.txt"), p)
	if err != nil {
		t.Fatalf("write inside workspace failed: %v %s", err, out)
	}
	if c.Enforcement != sandbox.EnforcementFull {
		t.Fatal(c.Enforcement)
	}
	if b, _ := os.ReadFile(filepath.Join(ws, "a.txt")); string(b) != "in\n" {
		t.Fatal(string(b))
	}

	target := filepath.Join(outside, "b.txt")
	out, err, c = run(t, sb, ws, "echo out > "+target, p)
	if err == nil {
		t.Fatal("write outside the workspace must fail")
	}
	if _, serr := os.Stat(target); serr == nil {
		t.Fatal("file outside the workspace was created")
	}
	if !sandbox.MatchBlocked(c.BlockedSignatures, out) {
		t.Fatalf("output should match a blocked signature: %q", out)
	}
	// symlink inside the workspace pointing outside does not help
	os.Symlink(outside, filepath.Join(ws, "link"))
	if _, err, _ := run(t, sb, ws, "echo x > link/c.txt", p); err == nil {
		t.Fatal("write through symlink must fail")
	}
	if _, err := os.Stat(filepath.Join(outside, "c.txt")); err == nil {
		t.Fatal("escaped via symlink")
	}
	// temp dir stays writable
	if out, err, _ := run(t, sb, ws, `f=$(mktemp) && echo t > "$f" && rm "$f"`, p); err != nil {
		t.Fatalf("tmp should be writable: %v %s", err, out)
	}
}

func TestReadOnlyBlocksWorkspaceToo(t *testing.T) {
	sb := darwin(t)
	ws, _ := dirs(t)
	os.WriteFile(filepath.Join(ws, "r.txt"), []byte("data"), 0o644)
	p := sandbox.Policy{Mode: sandbox.ModeReadOnly}
	out, err, _ := run(t, sb, ws, "cat r.txt", p)
	if err != nil || out != "data" {
		t.Fatalf("read must work: %v %q", err, out)
	}
	if _, err, _ := run(t, sb, ws, "echo x > w.txt", p); err == nil {
		t.Fatal("read-only must block writes in the workspace")
	}
	if _, err := os.Stat(filepath.Join(ws, "w.txt")); err == nil {
		t.Fatal("file created in read-only mode")
	}
}

func TestNetwork(t *testing.T) {
	sb := darwin(t)
	ws, _ := dirs(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "pong") }))
	defer srv.Close()
	script := "/usr/bin/curl -sS -m 5 " + srv.URL

	none := sandbox.Policy{Mode: sandbox.ModeWorkspaceWrite, WritableRoots: []string{ws}, Network: sandbox.NetworkNone}
	out, err, c := run(t, sb, ws, script, none)
	if err == nil || strings.Contains(out, "pong") {
		t.Fatalf("network none must block: %q", out)
	}
	if !sandbox.MatchBlocked(c.BlockedSignatures, out) {
		t.Fatalf("blocked signature expected: %q", out)
	}
	open := none
	open.Network = sandbox.NetworkOpen
	if out, err, c := run(t, sb, ws, script, open); err != nil || out != "pong" || c.Enforcement != sandbox.EnforcementFull {
		t.Fatalf("open network: %v %q %s", err, out, c.Enforcement)
	}
	allow := none
	allow.Network, allow.AllowHosts = sandbox.NetworkAllowlist, []string{"example.com"}
	_, _, c = run(t, sb, ws, "true", allow)
	if c.Enforcement != sandbox.EnforcementPartial || len(c.Limits) == 0 {
		t.Fatal("allowlist is partial on seatbelt", c.Enforcement, c.Limits)
	}
}

func TestProfileUsesParams(t *testing.T) {
	sb := seatbelt.New(seatbelt.Config{})
	evil := `/tmp/x") (allow file-write* (subpath "/`
	prof, params := sb.Profile(sandbox.Policy{Mode: sandbox.ModeWorkspaceWrite, WritableRoots: []string{evil}}, nil)
	if strings.Contains(prof, evil) {
		t.Fatal("paths must not be spliced into the profile")
	}
	found := false
	for _, p := range params {
		if strings.Contains(p, `x") (allow`) {
			found = true
		}
	}
	if !found {
		t.Fatal("path passed as -D param", params)
	}
	if !strings.Contains(prof, "(deny network* (remote ip))") {
		t.Fatal("network none denies ip")
	}
	prof, _ = sb.Profile(sandbox.Policy{Mode: sandbox.ModeWorkspaceWrite, Network: sandbox.NetworkOpen}, nil)
	if strings.Contains(prof, "deny network") {
		t.Fatal("open network")
	}
}

func TestFullAndUnavailable(t *testing.T) {
	sb := seatbelt.New(seatbelt.Config{Binary: "/nonexistent/sandbox-exec"})
	c, err := sb.Confine(context.Background(), []string{"ls"}, "/", nil, sandbox.Policy{Mode: sandbox.ModeFull})
	if err != nil || c.Enforcement != sandbox.EnforcementNone || c.Argv[0] != "ls" {
		t.Fatal(c, err)
	}
	_, err = sb.Confine(context.Background(), []string{"ls"}, "/", nil, sandbox.Policy{Mode: sandbox.ModeReadOnly})
	if !errors.Is(err, sandbox.ErrUnavailable) {
		t.Fatal("missing binary must be SANDBOX_UNAVAILABLE", err)
	}
}

func TestPlugin(t *testing.T) {
	h := kernel.NewHost(nil)
	defer h.Close()
	h.Load(seatbelt.Plugin, kernel.LoadOptions{Config: map[string]any{"extraWritable": []string{"/tmp/cache"}}})
	sb, ok := kernel.Lookup[sandbox.Sandbox](h.Root(), sandbox.ServiceKey)
	if !ok || sb.Name() != "seatbelt" {
		t.Fatal(h.Plugins())
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
