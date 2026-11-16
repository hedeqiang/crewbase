package bubblewrap_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/capabilities/sandbox"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/providers/sandbox/bubblewrap"
)

// index returns the position of seq in args, or -1.
func index(args []string, seq ...string) int {
	for i := 0; i+len(seq) <= len(args); i++ {
		ok := true
		for j, s := range seq {
			if args[i+j] != s {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

func count(args []string, s string) int {
	n := 0
	for _, a := range args {
		if a == s {
			n++
		}
	}
	return n
}

func realDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestArgsProtectCLIConfig(t *testing.T) {
	ws := realDir(t)
	if err := os.MkdirAll(filepath.Join(ws, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".claude", "settings.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "opencode.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := sandbox.PolicyFor(agent.ModeSandbox, ws, nil)
	p.ProtectedRoots = append(p.ProtectedRoots, p.ProtectedRoots...) // duplicates collapse
	args := bubblewrap.New(bubblewrap.Config{}).Args(p, ws, nil)

	bind := index(args, "--bind-try", ws, ws)
	if bind < 0 {
		t.Fatalf("workspace not bound writable: %q", args)
	}
	for _, name := range []string{".claude", "opencode.json"} {
		path := filepath.Join(ws, name)
		i := index(args, "--ro-bind", path, path)
		if i < bind {
			t.Fatalf("%s: present path must be bound read-only after the workspace: %q", name, args)
		}
		if index(args, "--tmpfs", path) >= 0 {
			t.Fatalf("%s: present path got a placeholder: %q", name, args)
		}
	}
	for _, name := range agent.CLIConfigNames {
		path := filepath.Join(ws, name)
		// "--ro-bind p p" or "--tmpfs p --remount-ro p": twice, once.
		if n := count(args, path); n != 2 {
			t.Fatalf("%s appears %d times, want 2: %q", name, n, args)
		}
		if name == ".claude" || name == "opencode.json" {
			continue
		}
		i := index(args, "--tmpfs", path, "--remount-ro", path)
		if i < bind {
			t.Fatalf("%s: missing name needs a read-only tmpfs after the workspace: %q", name, args)
		}
	}
	if args[len(args)-2] != "--chdir" {
		t.Fatalf("protected mounts belong before --chdir: %q", args)
	}
	// Planning alone never touches the folder.
	if _, err := os.Lstat(filepath.Join(ws, ".mcp.json")); !os.IsNotExist(err) {
		t.Fatal("Args created a placeholder", err)
	}
}

func TestArgsProtectOnlyInsideWritable(t *testing.T) {
	ws, other := realDir(t), realDir(t)
	os.MkdirAll(filepath.Join(other, ".claude"), 0o755)
	p := sandbox.Policy{Mode: sandbox.ModeWorkspaceWrite, WritableRoots: []string{ws},
		ProtectedRoots: []string{filepath.Join(other, ".claude"), filepath.Join(other, ".mcp.json"), filepath.Join(ws, "no-parent", ".mcp.json")}}
	args := bubblewrap.New(bubblewrap.Config{}).Args(p, ws, nil)
	// Only the base mounts remain: --ro-bind / / and --tmpfs /tmp.
	if count(args, "--ro-bind") != 1 || count(args, "--tmpfs") != 1 || count(args, "--remount-ro") != 0 {
		t.Fatalf("paths outside writable roots (or without a parent) must not be mounted: %q", args)
	}
}

func TestArgsReadOnlyWorkspaceInWritableTemp(t *testing.T) {
	tmp := realDir(t)
	ws := filepath.Join(tmp, "ws")
	os.MkdirAll(ws, 0o755)
	p := sandbox.PolicyFor(agent.ModeReadOnly, ws, nil)
	args := bubblewrap.New(bubblewrap.Config{}).Args(p, ws, []string{"TMPDIR=" + tmp})
	bind, ro := index(args, "--bind-try", tmp, tmp), index(args, "--ro-bind", ws, ws)
	if bind < 0 || ro < bind {
		t.Fatalf("read-only workspace inside a writable TMPDIR must be re-bound read-only: %q", args)
	}
}

// TestLiveProtectedRoots runs inside real bubblewrap (CI installs it on
// ubuntu-latest); it skips where bwrap or user namespaces are unavailable.
func TestLiveProtectedRoots(t *testing.T) {
	b := ready(t)
	home, _ := os.UserHomeDir()
	base, err := os.MkdirTemp(home, ".bwtest-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	ws := filepath.Join(base, "ws")
	os.MkdirAll(ws, 0o755)
	p := sandbox.PolicyFor(agent.ModeSandbox, ws, nil)
	exec1 := func(script string) (string, error) {
		t.Helper()
		c, err := b.Confine(context.Background(), []string{"/bin/sh", "-c", script}, ws, os.Environ(), p)
		if err != nil {
			t.Fatal(err)
		}
		if c.Release == nil {
			t.Fatal("missing protected names need a Release")
		}
		defer c.Release()
		out, err := exec.Command(c.Argv[0], c.Argv[1:]...).CombinedOutput()
		return string(out), err
	}

	// Missing names cannot be created.
	for _, script := range []string{
		"mkdir .claude",
		"mkdir -p .claude/agents",
		"touch .mcp.json",
		"echo '{}' > opencode.json",
		"mkdir .codex",
	} {
		if out, err := exec1(script); err == nil {
			t.Errorf("%q must fail: %s", script, out)
		}
	}
	if out, err := exec1("echo ok > normal.txt && mkdir -p src && echo x > src/a.go"); err != nil {
		t.Fatalf("normal writes must still work: %v %s", err, out)
	}
	if b, _ := os.ReadFile(filepath.Join(ws, "normal.txt")); string(b) != "ok\n" {
		t.Fatalf("normal.txt = %q", b)
	}
	for _, name := range agent.CLIConfigNames {
		if _, err := os.Lstat(filepath.Join(ws, name)); !os.IsNotExist(err) {
			t.Fatalf("%s left behind in the workspace: %v", name, err)
		}
	}

	// Present config cannot be changed, removed or moved.
	settings := filepath.Join(ws, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(settings), 0o755)
	if err := os.WriteFile(settings, []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, script := range []string{
		`echo '{"hooks":{}}' > .claude/settings.json`,
		"touch .claude/settings.local.json",
		"rm -rf .claude",
		"mv .claude moved",
	} {
		if out, err := exec1(script); err == nil {
			t.Errorf("%q must fail: %s", script, out)
		}
	}
	if b, _ := os.ReadFile(settings); string(b) != `{"a":1}` {
		t.Fatalf("settings.json changed: %q", b)
	}
	if out, err := exec1("cat .claude/settings.json"); err != nil || !strings.Contains(out, `"a":1`) {
		t.Fatalf("protected config stays readable: %v %s", err, out)
	}
}
