package runmode_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/capabilities/sandbox"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/runmode"
	"github.com/hedeqiang/crewbase/features/toolsbasic"
	"github.com/hedeqiang/crewbase/kernel"
	fslocal "github.com/hedeqiang/crewbase/providers/fs/local"
	sandboxnone "github.com/hedeqiang/crewbase/providers/sandbox/none"
	"github.com/hedeqiang/crewbase/providers/sandbox/seatbelt"
	shelllocal "github.com/hedeqiang/crewbase/providers/shell/local"
)

type fixed struct{ enf string }

func (f fixed) Name() string { return "fixed" }
func (f fixed) Check(context.Context, sandbox.Policy) (string, error) {
	if f.enf == "" {
		return "", sandbox.Unavailable("down")
	}
	return f.enf, nil
}
func (f fixed) Confine(context.Context, []string, string, []string, sandbox.Policy) (sandbox.Confined, error) {
	return sandbox.Confined{}, nil
}

func dec(mode, cat, ws string, args any) *tools.Decision {
	b, _ := json.Marshal(args)
	return &tools.Decision{Call: &tools.Call{Name: "t", Args: b, Mode: mode, Workspace: ws}, Category: cat}
}

func TestReadOnlyCategories(t *testing.T) {
	ws := t.TempDir()
	ctx := context.Background()
	g := &runmode.Guard{Sandbox: func() sandbox.Sandbox { return fixed{enf: sandbox.EnforcementFull} }}
	for cat, deny := range map[string]bool{
		tools.CategoryRead: false, tools.CategoryNone: false, tools.CategoryExec: false,
		tools.CategoryWrite: true, tools.CategoryDelete: true, tools.CategoryInstall: true,
		tools.CategoryPush: true, tools.CategoryPublish: true, tools.CategoryPayment: true, "": true, "custom": true,
	} {
		reason, denied := g.Check(ctx, dec("read-only", cat, ws, nil))
		if denied != deny {
			t.Errorf("%q: denied=%v (%s)", cat, denied, reason)
		}
		// sandbox and host modes do not restrict categories
		if _, d := g.Check(ctx, dec("sandbox", cat, ws, nil)); d {
			t.Errorf("sandbox mode denied %q", cat)
		}
		if _, d := g.Check(ctx, dec("host", cat, ws, nil)); d {
			t.Errorf("host mode denied %q", cat)
		}
	}
	// unknown mode fails closed to read-only
	if _, d := g.Check(ctx, dec("bogus", tools.CategoryWrite, ws, nil)); !d {
		t.Fatal("unknown mode must act read-only")
	}
	// exec in read-only needs full read-only enforcement
	for name, sb := range map[string]sandbox.Sandbox{"nil": nil, "partial": fixed{enf: sandbox.EnforcementPartial}, "down": fixed{}, "none": sandboxnone.None{}} {
		g := &runmode.Guard{Sandbox: func() sandbox.Sandbox { return sb }}
		if reason, d := g.Check(ctx, dec("read-only", tools.CategoryExec, ws, nil)); !d {
			t.Errorf("%s: exec allowed in read-only", name)
		} else if !strings.Contains(reason, "read-only") {
			t.Errorf("%s: %s", name, reason)
		}
	}
}

func TestPathsStayInWorkspace(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "ws")
	os.MkdirAll(ws, 0o755)
	os.Symlink(base, filepath.Join(ws, "up"))
	g := &runmode.Guard{}
	ctx := context.Background()
	cases := []struct {
		args any
		deny bool
	}{
		{map[string]string{"path": "a.txt"}, false},
		{map[string]string{"path": filepath.Join(ws, "sub/new.txt")}, false},
		{map[string]string{"path": "."}, false},
		{map[string]string{"path": "../x"}, true},
		{map[string]string{"path": "/etc/passwd"}, true},
		{map[string]string{"file_path": "/etc/passwd"}, true},
		{map[string]string{"cwd": "/"}, true},
		{map[string]string{"path": "up/secret"}, true}, // symlink escape
		{map[string]any{"paths": []string{"a"}}, false},
		{map[string]any{"paths": []string{"a", "/etc"}}, true},
		{map[string]string{"path": "https://example.com/a"}, false}, // URLs are not paths
		{map[string]string{"command": "cat /etc/passwd"}, false},    // commands are the sandbox's job
	}
	for _, c := range cases {
		reason, d := g.Check(ctx, dec("sandbox", tools.CategoryRead, ws, c.args))
		if d != c.deny {
			t.Errorf("%v: denied=%v %s", c.args, d, reason)
		}
		if _, d := g.Check(ctx, dec("host", tools.CategoryRead, ws, c.args)); d {
			t.Errorf("%v: host mode must not be path-confined", c.args)
		}
	}
	if _, d := g.Check(ctx, dec("sandbox", tools.CategoryRead, "", map[string]string{"path": "a"})); !d {
		t.Fatal("no workspace + path must deny")
	}
	if _, d := g.Check(ctx, dec("sandbox", tools.CategoryNone, "", map[string]string{"q": "a"})); d {
		t.Fatal("no path args: abstain")
	}
}

func pipeline(t *testing.T, sb kernel.Spec) (*kernel.Host, *tools.Registry, *session.Session) {
	t.Helper()
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	for _, s := range []kernel.Spec{session.Plugin, tools.Plugin, approval.Plugin, approval.DesktopPlugin, sb, fslocal.Plugin, shelllocal.Plugin, toolsbasic.Plugin, runmode.Plugin} {
		if _, err := h.Load(s, kernel.LoadOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range h.Plugins() {
		if p.State != kernel.StateActive {
			t.Fatalf("not active: %+v", p)
		}
	}
	ss, _ := kernel.Use[*session.Store](h.Root(), session.ServiceKey).Create(session.Meta{Kind: "agent"})
	return h, kernel.Use[*tools.Registry](h.Root(), tools.ServiceKey), ss
}

func run(h *kernel.Host, reg *tools.Registry, ss *session.Session, mode, ws, name string, args any) tools.Execution {
	b, _ := json.Marshal(args)
	return reg.Run(context.Background(), h.Root(), tools.Call{ID: session.NewID("call"), Name: name, Args: b, Agent: "kai", Session: ss, Mode: mode, Workspace: ws})
}

func TestGuardInPipeline(t *testing.T) {
	h, reg, ss := pipeline(t, sandboxnone.Plugin)
	ws := t.TempDir()
	exe := run(h, reg, ss, "read-only", ws, "write_file", map[string]string{"path": "a", "content": "x"})
	if !exe.Denied || !strings.Contains(exe.Result.Content, "read-only mode does not allow write") {
		t.Fatal(exe.Result)
	}
	exe = run(h, reg, ss, "read-only", ws, "bash", map[string]string{"command": "ls"})
	if !exe.Denied || !strings.Contains(exe.Result.Content, sandbox.CodeUnavailable) {
		t.Fatal("sandbox-none cannot enforce read-only", exe.Result)
	}
	exe = run(h, reg, ss, "read-only", ws, "bash", map[string]string{"command": "git push"})
	if !exe.Denied || !strings.Contains(exe.Result.Content, "push") {
		t.Fatal(exe.Result)
	}
	exe = run(h, reg, ss, "sandbox", ws, "read_file", map[string]string{"path": "/etc/hosts"})
	if !exe.Denied || !strings.Contains(exe.Result.Content, "outside the workspace") {
		t.Fatal(exe.Result)
	}
	exe = run(h, reg, ss, "host", ws, "read_file", map[string]string{"path": "/etc/hosts"})
	if exe.Denied || exe.Result.IsError {
		t.Fatal(exe.Result)
	}
	exe = run(h, reg, ss, "read-only", ws, "list_dir", map[string]string{})
	if exe.Denied {
		t.Fatal(exe.Result)
	}
	// rules/hooks cannot re-allow a guard denial
	h.Load(kernel.Spec{Name: "allow-all", Apply: func(c *kernel.Context) error {
		tools.PreExecute.Prepend(c, func(ctx context.Context, d *tools.Decision, next kernel.Next[*tools.Decision, *tools.Decision]) (*tools.Decision, error) {
			r, err := next(ctx, d)
			r.Verdict = tools.Allow
			return r, err
		})
		return nil
	}}, kernel.LoadOptions{})
	if exe = run(h, reg, ss, "read-only", ws, "delete_file", map[string]string{"path": "a"}); !exe.Denied {
		t.Fatal(exe.Result)
	}
}

func TestReadOnlyExecWithSeatbelt(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS only")
	}
	h, reg, ss := pipeline(t, seatbelt.Plugin)
	ws := t.TempDir()
	os.WriteFile(filepath.Join(ws, "f"), []byte("data"), 0o644)
	exe := run(h, reg, ss, "read-only", ws, "bash", map[string]string{"command": "cat f"})
	if exe.Denied || !strings.Contains(exe.Result.Content, "data") {
		t.Fatal(exe.Result)
	}
}

func TestModeHelpers(t *testing.T) {
	h := kernel.NewHost(nil)
	defer h.Close()
	root := t.TempDir()
	h.Load(session.Plugin, kernel.LoadOptions{Config: map[string]any{"dir": root + "/sessions"}})
	h.Load(prompt.Plugin, kernel.LoadOptions{})
	h.Load(agent.Plugin, kernel.LoadOptions{Config: map[string]any{"root": root}})
	reg := kernel.Use[*agent.Registry](h.Root(), agent.ServiceKey)
	a, err := reg.Create(agent.Spec{Name: "Kai"})
	if err != nil {
		t.Fatal(err)
	}
	if runmode.Effective(a) != agent.ModeSandbox {
		t.Fatal(a.Mode())
	}
	if _, ok := runmode.HostExpiry(a); ok {
		t.Fatal("not host")
	}
	ch, err := runmode.Set(a, nil, agent.ModeHost, "ada", "user")
	if err != nil {
		t.Fatal(err)
	}
	if ch.From != agent.ModeSandbox || ch.To != agent.ModeHost || time.Until(ch.Until) < 23*time.Hour || time.Until(ch.Until) > 25*time.Hour {
		t.Fatal(ch)
	}
	if until, ok := runmode.HostExpiry(a); !ok || !until.Equal(ch.Until) {
		t.Fatal(until, ok)
	}
	ss, _ := a.Session()
	e, ok := ss.Last(session.TypeModeChanged)
	var got runmode.Change
	if !ok || e.Decode(&got) != nil || got.To != agent.ModeHost || got.By != "ada" || e.Agent != a.ID() {
		t.Fatal(e)
	}
	// expire the grant: Agent.Mode reverts, helpers notice once
	a.Update(func(s *agent.Spec) { s.ModeUntil = time.Now().Add(-time.Minute) })
	if !runmode.Expired(a) || runmode.Effective(a) != agent.ModeSandbox {
		t.Fatal("expired host mode must read as sandbox")
	}
	if rec, err := runmode.RecordExpiry(a, ss); !rec || err != nil {
		t.Fatal(rec, err)
	}
	if rec, _ := runmode.RecordExpiry(a, ss); rec {
		t.Fatal("expiry recorded twice")
	}
	if _, err := runmode.Set(a, ss, "turbo", "", ""); err == nil {
		t.Fatal("unknown mode")
	}
	ch, _ = runmode.Set(a, ss, agent.ModeReadOnly, "ada", "user")
	if ch.From != agent.ModeSandbox || !ch.Until.IsZero() || a.Spec().Mode != agent.ModeReadOnly {
		t.Fatal(ch, a.Spec())
	}
	if err := runmode.Record(nil, "x", runmode.Change{}); err == nil {
		t.Fatal("nil session")
	}
}

func TestDeclaredPathArgs(t *testing.T) {
	ws := t.TempDir()
	ctx := context.Background()
	g := &runmode.Guard{}
	outside := filepath.Join(filepath.Dir(ws), "elsewhere")
	// a declared path argument with a non-standard name is confined
	d := dec("sandbox", tools.CategoryRead, ws, map[string]any{"location": outside, "target": "x"})
	d.Tool = &tools.Tool{Name: "t", PathArgs: []string{"location"}}
	if _, denied := g.Check(ctx, d); !denied {
		t.Fatal("declared path arg not confined")
	}
	// declared args replace the well-known names: "target" is not a path here
	d = dec("sandbox", tools.CategoryRead, ws, map[string]any{"location": "a.txt", "target": outside})
	d.Tool = &tools.Tool{Name: "t", PathArgs: []string{"location"}}
	if reason, denied := g.Check(ctx, d); denied {
		t.Fatal(reason)
	}
	// without a declaration the well-known names still apply
	d = dec("sandbox", tools.CategoryRead, ws, map[string]any{"target": outside})
	d.Tool = &tools.Tool{Name: "t"}
	if _, denied := g.Check(ctx, d); !denied {
		t.Fatal("fallback names not checked")
	}
}

func TestGrantedRootsAllowed(t *testing.T) {
	ws := t.TempDir()
	granted := t.TempDir()
	other := t.TempDir()
	mk := func(p string) *tools.Decision {
		b, _ := json.Marshal(map[string]string{"path": p})
		return &tools.Decision{Call: &tools.Call{Name: "fs__read", Args: b, Mode: "sandbox", Workspace: ws},
			Tool: &tools.Tool{Name: "fs__read", PathArgs: []string{"path"}, AllowedRoots: []string{granted}}, Category: tools.CategoryRead}
	}
	g := &runmode.Guard{}
	if reason, denied := g.Check(context.Background(), mk(filepath.Join(granted, "note.txt"))); denied {
		t.Fatal("granted root denied:", reason)
	}
	if _, denied := g.Check(context.Background(), mk(filepath.Join(other, "x"))); !denied {
		t.Fatal("path outside workspace and granted roots must be denied")
	}
}
