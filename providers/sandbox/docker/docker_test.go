package docker

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hedeqiang/crewbase/capabilities/sandbox"
	"github.com/hedeqiang/crewbase/kernel"
)

func argsHas(args []string, seq ...string) bool {
	for i := 0; i+len(seq) <= len(args); i++ {
		ok := true
		for j, s := range seq {
			if args[i+j] != s {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func TestArgs(t *testing.T) {
	d := New(Config{Image: "alpine:3", User: "1000:1000"})
	ws := "/w/kai"
	args, err := d.Args("n1", []string{"sh", "-c", "ls"}, ws+"/sub", []string{"PATH=/host/bin", "API=secret", "HOME=/Users/x"},
		sandbox.Policy{Mode: sandbox.ModeWorkspaceWrite, WritableRoots: []string{ws}, Network: sandbox.NetworkNone})
	if err != nil {
		t.Fatal(err)
	}
	for _, seq := range [][]string{
		{"run", "--rm"}, {"--security-opt", "no-new-privileges:true"}, {"--shm-size", "1g"},
		{"--network", "none"}, {"--mount", "type=bind,source=/w/kai,target=/w/kai"}, {"-w", "/w/kai/sub"},
		{"-e", "API"}, {"--user", "1000:1000"}, {"alpine:3", "sh", "-c", "ls"}, {"--name", "n1"},
	} {
		if !argsHas(args, seq...) {
			t.Errorf("missing %v in %v", seq, args)
		}
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "secret") {
		t.Fatal("env values must not appear in argv")
	}
	if argsHas(args, "-e", "PATH") || argsHas(args, "-e", "HOME") {
		t.Fatal("host-only env forwarded")
	}
	if strings.Contains(joined, "--read-only") {
		t.Fatal("workspace-write has a writable root fs")
	}

	// read-only: cwd mounted readonly, root fs read-only
	args, _ = d.Args("n2", []string{"ls"}, ws, nil, sandbox.Policy{Mode: sandbox.ModeReadOnly})
	if !argsHas(args, "--mount", "type=bind,source=/w/kai,target=/w/kai,readonly") || !argsHas(args, "--read-only") {
		t.Fatal(args)
	}
	// open network
	args, _ = d.Args("n3", []string{"ls"}, ws, nil, sandbox.Policy{Mode: sandbox.ModeReadOnly, Network: sandbox.NetworkOpen})
	if !argsHas(args, "--network", "bridge") {
		t.Fatal(args)
	}
	// cwd outside writable roots is mounted read-only
	args, _ = d.Args("n4", []string{"ls"}, "/elsewhere", nil, sandbox.Policy{Mode: sandbox.ModeWorkspaceWrite, WritableRoots: []string{ws}})
	if !argsHas(args, "--mount", "type=bind,source=/elsewhere,target=/elsewhere,readonly") {
		t.Fatal(args)
	}
	if _, err := d.Args("n", []string{"ls"}, "rel", nil, sandbox.Policy{}); err == nil {
		t.Fatal("relative cwd")
	}
	if _, err := d.Args("n", []string{"ls"}, "/w", nil, sandbox.Policy{Mode: sandbox.ModeWorkspaceWrite, WritableRoots: []string{"/a,b"}}); err == nil {
		t.Fatal("comma in mount path")
	}
	if _, err := New(Config{Image: "--privileged"}).Args("n", []string{"ls"}, "/w", nil, sandbox.Policy{}); err == nil {
		t.Fatal("flag as image")
	}
}

func TestDaemonDownFailsClosed(t *testing.T) {
	d := New(Config{})
	var probes atomic.Int32
	d.run = func(ctx context.Context, args ...string) ([]byte, error) {
		probes.Add(1)
		return []byte("Cannot connect to the Docker daemon"), errors.New("exit status 1")
	}
	ctx := context.Background()
	p := sandbox.Policy{Mode: sandbox.ModeWorkspaceWrite, WritableRoots: []string{"/w"}}
	_, err := d.Confine(ctx, []string{"ls"}, "/w", nil, p)
	if !errors.Is(err, sandbox.ErrUnavailable) || !strings.Contains(err.Error(), "Cannot connect") {
		t.Fatal(err)
	}
	if _, err := d.Check(ctx, p); !errors.Is(err, sandbox.ErrUnavailable) {
		t.Fatal(err)
	}
	if probes.Load() != 1 {
		t.Fatal("failure is cached briefly", probes.Load())
	}
	// full mode never needs the daemon
	if c, err := d.Confine(ctx, []string{"ls"}, "/w", nil, sandbox.Policy{Mode: sandbox.ModeFull}); err != nil || c.Enforcement != sandbox.EnforcementNone {
		t.Fatal(c, err)
	}
}

func TestDaemonUpConfines(t *testing.T) {
	d := New(Config{Binary: "/bin/docker-fake"})
	var calls [][]string
	d.run = func(ctx context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args)
		return []byte("27.0.1\n"), nil
	}
	c, err := d.Confine(context.Background(), []string{"ls"}, "/w", []string{"K=V"},
		sandbox.Policy{Mode: sandbox.ModeWorkspaceWrite, WritableRoots: []string{"/w"}, Network: sandbox.NetworkAllowlist, AllowHosts: []string{"x.org"}})
	if err != nil {
		t.Fatal(err)
	}
	if c.Argv[0] != "/bin/docker-fake" || c.Enforcement != sandbox.EnforcementPartial || len(c.Limits) == 0 || c.Kill == nil {
		t.Fatal(c)
	}
	hasKV := false
	for _, kv := range c.Env {
		hasKV = hasKV || kv == "K=V"
	}
	if !hasKV {
		t.Fatal("request env goes to the CLI env", c.Env)
	}
	c.Kill()
	if last := calls[len(calls)-1]; last[0] != "kill" || !strings.HasPrefix(last[1], "crewbase-sbx-") {
		t.Fatal(calls)
	}
}

func TestPlugin(t *testing.T) {
	h := kernel.NewHost(nil)
	defer h.Close()
	h.Load(Plugin, kernel.LoadOptions{Config: map[string]any{"image": "alpine:3"}})
	sb, ok := kernel.Lookup[sandbox.Sandbox](h.Root(), sandbox.ServiceKey)
	if !ok || sb.Name() != "docker" || sb.(*Docker).Image() != "alpine:3" {
		t.Fatal(h.Plugins())
	}
}

// TestRealDocker runs only when a daemon answers `docker info`.
func TestRealDocker(t *testing.T) {
	d := New(Config{Image: "alpine:3"})
	if err := exec.Command(d.cfg.Binary, "info").Run(); err != nil {
		t.Skip("docker daemon not available:", err)
	}
	wd, _ := os.Getwd()
	base, _ := os.MkdirTemp(wd, ".dktest-")
	defer os.RemoveAll(base)
	ws := filepath.Join(base, "ws")
	os.MkdirAll(ws, 0o755)
	p := sandbox.Policy{Mode: sandbox.ModeWorkspaceWrite, WritableRoots: []string{ws}, Network: sandbox.NetworkNone}
	c, err := d.Confine(context.Background(), []string{"sh", "-c", "echo hi > a.txt && (echo x > /etc/escape 2>&1 || true) && wget -q -T 3 -O- http://example.com || echo NONET"}, ws, nil, p)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(c.Argv[0], c.Argv[1:]...)
	cmd.Env = c.Env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if b, _ := os.ReadFile(filepath.Join(ws, "a.txt")); string(b) != "hi\n" {
		t.Fatal("workspace write", string(b), string(out))
	}
	if !strings.Contains(string(out), "NONET") {
		t.Fatal("network none", string(out))
	}
}
