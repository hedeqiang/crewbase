package docker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/hedeqiang/crewbase/capabilities/sandbox"
	"github.com/hedeqiang/crewbase/core/agent"
)

func argsIndex(args []string, seq ...string) int {
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

func TestArgsProtectCLIConfig(t *testing.T) {
	ws := t.TempDir()
	os.MkdirAll(filepath.Join(ws, ".claude"), 0o755)
	os.WriteFile(filepath.Join(ws, ".mcp.json"), []byte("{}"), 0o644)
	d := New(Config{Image: "alpine:3", User: "1000:1000"})
	p := sandbox.PolicyFor(agent.ModeSandbox, ws, nil)
	p.ProtectedRoots = append(p.ProtectedRoots, p.ProtectedRoots...) // duplicates collapse
	args, err := d.Args("n", []string{"ls"}, ws, nil, p)
	if err != nil {
		t.Fatal(err)
	}
	bind := argsIndex(args, "--mount", "type=bind,source="+ws+",target="+ws)
	image := argsIndex(args, "alpine:3")
	if bind < 0 || image < 0 {
		t.Fatalf("workspace mount: %q", args)
	}
	for _, name := range agent.CLIConfigNames {
		path := filepath.Join(ws, name)
		ro := "type=bind,source=" + path + ",target=" + path + ",readonly"
		tmpfs := path + ":ro,size=4k"
		nRO, nTmp := 0, 0
		for _, a := range args {
			if a == ro {
				nRO++
			}
			if a == tmpfs {
				nTmp++
			}
		}
		present := name == ".claude" || name == ".mcp.json"
		switch {
		case present && (nRO != 1 || nTmp != 0):
			t.Fatalf("%s: present path wants one read-only bind: %q", name, args)
		case !present && (nRO != 0 || nTmp != 1):
			t.Fatalf("%s: missing name wants one read-only tmpfs: %q", name, args)
		}
		i := argsIndex(args, "--mount", ro)
		if !present {
			i = argsIndex(args, "--tmpfs", tmpfs)
		}
		if i < bind || i > image {
			t.Fatalf("%s: protected mount must follow the workspace mount: %q", name, args)
		}
	}
	if _, err := os.Lstat(filepath.Join(ws, ".codex")); !os.IsNotExist(err) {
		t.Fatal("Args created a placeholder", err)
	}

	// Outside the writable roots nothing is added (the container doesn't
	// see those paths), and read-only mode mounts the workspace read-only.
	other := t.TempDir()
	args, _ = d.Args("n", []string{"ls"}, ws, nil, sandbox.Policy{Mode: sandbox.ModeWorkspaceWrite, WritableRoots: []string{ws},
		ProtectedRoots: []string{filepath.Join(other, ".claude")}})
	if argsIndex(args, "--tmpfs") >= 0 || len(args) != argsIndex(args, "alpine:3")+2 {
		t.Fatalf("protected root outside the writable roots was mounted: %q", args)
	}
	args, _ = d.Args("n", []string{"ls"}, ws, nil, sandbox.PolicyFor(agent.ModeReadOnly, ws, nil))
	if argsIndex(args, "--mount", "type=bind,source="+ws+",target="+ws+",readonly") < 0 {
		t.Fatalf("read-only mode: %q", args)
	}
}

func TestConfineReleasesPlaceholders(t *testing.T) {
	ws := t.TempDir()
	d := New(Config{Binary: "/bin/docker-fake"})
	d.run = func(context.Context, ...string) ([]byte, error) { return []byte("27.0.1\n"), nil }
	c, err := d.Confine(context.Background(), []string{"ls"}, ws, nil, sandbox.PolicyFor(agent.ModeSandbox, ws, nil))
	if err != nil {
		t.Fatal(err)
	}
	mcp := filepath.Join(ws, ".mcp.json")
	if c.Release == nil || placeholders.Held(mcp) != 1 {
		t.Fatal("missing names are held until Release", placeholders.Held(mcp))
	}
	os.Mkdir(mcp, 0o755) // the mount point docker would make
	c.Release()
	if _, err := os.Lstat(mcp); !os.IsNotExist(err) || placeholders.Held(mcp) != 0 {
		t.Fatal("placeholder left behind", err)
	}
}

// TestRealDockerProtectedRoots runs only when a daemon answers `docker info`.
func TestRealDockerProtectedRoots(t *testing.T) {
	d := New(Config{Image: "alpine:3"})
	if err := exec.Command(d.cfg.Binary, "info").Run(); err != nil {
		t.Skip("docker daemon not available:", err)
	}
	wd, _ := os.Getwd()
	base, _ := os.MkdirTemp(wd, ".dktest-")
	defer os.RemoveAll(base)
	ws := filepath.Join(base, "ws")
	settings := filepath.Join(ws, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(settings), 0o755)
	os.WriteFile(settings, []byte(`{"a":1}`), 0o644)
	p := sandbox.PolicyFor(agent.ModeSandbox, ws, nil)
	run := func(script string) (string, error) {
		c, err := d.Confine(context.Background(), []string{"sh", "-c", script}, ws, nil, p)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Release()
		cmd := exec.Command(c.Argv[0], c.Argv[1:]...)
		cmd.Env = c.Env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	for _, script := range []string{"touch .mcp.json", "mkdir .codex", "echo x > .claude/settings.json", "rm -rf .claude"} {
		if out, err := run(script); err == nil {
			t.Errorf("%q must fail: %s", script, out)
		}
	}
	if out, err := run("echo ok > normal.txt"); err != nil {
		t.Fatalf("normal write: %v %s", err, out)
	}
	if b, _ := os.ReadFile(settings); string(b) != `{"a":1}` {
		t.Fatalf("settings.json changed: %q", b)
	}
	for _, name := range []string{".mcp.json", ".codex"} {
		if _, err := os.Lstat(filepath.Join(ws, name)); !os.IsNotExist(err) {
			t.Fatalf("%s left behind: %v", name, err)
		}
	}
}
