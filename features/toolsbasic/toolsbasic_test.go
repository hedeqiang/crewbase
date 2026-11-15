package toolsbasic_test

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
	"github.com/hedeqiang/crewbase/features/toolsbasic"
	"github.com/hedeqiang/crewbase/kernel"
	fslocal "github.com/hedeqiang/crewbase/providers/fs/local"
	sandboxnone "github.com/hedeqiang/crewbase/providers/sandbox/none"
	sandboxseatbelt "github.com/hedeqiang/crewbase/providers/sandbox/seatbelt"
	shelllocal "github.com/hedeqiang/crewbase/providers/shell/local"
)

func TestClassifyCommand(t *testing.T) {
	cases := map[string]string{
		"ls -la":                                tools.CategoryExec,
		"go test ./...":                         tools.CategoryExec,
		"git status && git diff":                tools.CategoryExec,
		"git push origin main":                  tools.CategoryPush,
		"git -C repo push":                      tools.CategoryPush,
		"cd x && git commit -m wip && git push": tools.CategoryPush,
		"gh pr merge 12 --squash":               tools.CategoryPush,
		"docker push img":                       tools.CategoryPush,
		"rm -rf build":                          tools.CategoryDelete,
		"sudo rm /etc/x":                        tools.CategoryDelete,
		"/bin/rm x":                             tools.CategoryDelete,
		"git clean -fdx":                        tools.CategoryDelete,
		"git branch -D old":                     tools.CategoryDelete,
		"find . -name '*.o' -delete":            tools.CategoryDelete,
		"npm install":                           tools.CategoryInstall,
		"npm i lodash":                          tools.CategoryInstall,
		"pnpm add x":                            tools.CategoryInstall,
		"pip install requests":                  tools.CategoryInstall,
		"python3 -m pip install x":              tools.CategoryInstall,
		"uv pip install x":                      tools.CategoryInstall,
		"go get example.com/x":                  tools.CategoryInstall,
		"brew install jq":                       tools.CategoryInstall,
		"FOO=1 apt-get install -y curl":         tools.CategoryInstall,
		"cargo add serde":                       tools.CategoryInstall,
		"npm run build":                         tools.CategoryExec,
		"curl https://example.com":              tools.CategoryExec,
		"curl -X POST https://api/x":            tools.CategoryPublish,
		"curl -XPOST https://api/x":             tools.CategoryPublish,
		"curl -d 'a=1' https://api/x":           tools.CategoryPublish,
		"curl --data-binary @f https://api":     tools.CategoryPublish,
		"curl -X GET https://api":               tools.CategoryExec,
		"echo hi | mail -s x a@b.c":             tools.CategoryPublish,
		"sendmail a@b.c < msg":                  tools.CategoryPublish,
		"npm publish":                           tools.CategoryPublish,
		"gh pr create --fill":                   tools.CategoryPublish,
		"sh -c 'git push'":                      tools.CategoryPush,
		"echo $(rm -rf x)":                      tools.CategoryDelete,
		"rm x; git push":                        tools.CategoryPush,
	}
	for cmd, want := range cases {
		if got := toolsbasic.ClassifyCommand(cmd); got != want {
			t.Errorf("%q → %s, want %s", cmd, got, want)
		}
	}
}

type env struct {
	h   *kernel.Host
	reg *tools.Registry
	ss  *session.Session
	ws  string
	// categories seen by pre-execute
	cats map[string]string
}

func setup(t *testing.T, sb kernel.Spec) *env {
	t.Helper()
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	for _, s := range []kernel.Spec{session.Plugin, tools.Plugin, approval.Plugin, approval.DesktopPlugin, sb, fslocal.Plugin, shelllocal.Plugin, toolsbasic.Plugin} {
		if _, err := h.Load(s, kernel.LoadOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range h.Plugins() {
		if p.State != kernel.StateActive {
			t.Fatalf("not active: %+v", p)
		}
	}
	st := kernel.Use[*session.Store](h.Root(), session.ServiceKey)
	ss, _ := st.Create(session.Meta{Kind: "agent"})
	e := &env{h: h, reg: kernel.Use[*tools.Registry](h.Root(), tools.ServiceKey), ss: ss, ws: t.TempDir(), cats: map[string]string{}}
	h.Load(kernel.Spec{Name: "cat-recorder", Apply: func(c *kernel.Context) error {
		tools.PreExecute.Use(c, func(ctx context.Context, d *tools.Decision, next kernel.Next[*tools.Decision, *tools.Decision]) (*tools.Decision, error) {
			e.cats[d.Call.ID] = d.Category
			return next(ctx, d)
		})
		return nil
	}}, kernel.LoadOptions{})
	return e
}

func (e *env) run(mode, name string, args any) (tools.Execution, string) {
	b, _ := json.Marshal(args)
	id := session.NewID("call")
	exe := e.reg.Run(context.Background(), e.h.Root(), tools.Call{ID: id, Name: name, Args: b, Agent: "kai", Session: e.ss, Mode: mode, Workspace: e.ws})
	return exe, e.cats[id]
}

func confinedSandbox() kernel.Spec {
	if runtime.GOOS == "darwin" {
		return sandboxseatbelt.Plugin
	}
	return sandboxnone.Plugin
}

func TestFileTools(t *testing.T) {
	e := setup(t, sandboxnone.Plugin)
	exe, cat := e.run("sandbox", "write_file", map[string]string{"path": "notes/a.md", "content": "line1\nline2\nline3\n"})
	if exe.Result.IsError || cat != tools.CategoryWrite || !strings.Contains(exe.Result.Content, "Created") {
		t.Fatal(exe.Result, cat)
	}
	if b, _ := os.ReadFile(filepath.Join(e.ws, "notes/a.md")); string(b) != "line1\nline2\nline3\n" {
		t.Fatal("relative path must resolve against the workspace")
	}
	exe, cat = e.run("sandbox", "read_file", map[string]any{"path": "notes/a.md", "offset": 2, "limit": 1})
	if exe.Result.Content != "     2\tline2\n…[1 more lines; use offset=3 to continue]\n" || cat != tools.CategoryRead {
		t.Fatalf("%q", exe.Result.Content)
	}
	exe, _ = e.run("sandbox", "edit_file", map[string]string{"path": "notes/a.md", "old_string": "line2", "new_string": "LINE2"})
	if exe.Result.IsError {
		t.Fatal(exe.Result)
	}
	// external change → must re-read
	os.WriteFile(filepath.Join(e.ws, "notes/a.md"), []byte("changed by someone else, longer\n"), 0o644)
	exe, _ = e.run("sandbox", "edit_file", map[string]string{"path": "notes/a.md", "old_string": "changed", "new_string": "x"})
	if !exe.Result.IsError || !strings.Contains(exe.Result.Content, "read it again") {
		t.Fatal(exe.Result)
	}
	exe, _ = e.run("sandbox", "list_dir", map[string]string{})
	if exe.Result.Content != "notes/\n" {
		t.Fatalf("%q", exe.Result.Content)
	}
	exe, cat = e.run("sandbox", "delete_file", map[string]any{"path": "notes", "recursive": true})
	if exe.Result.IsError || cat != tools.CategoryDelete {
		t.Fatal(exe.Result, cat)
	}
	// escapes are refused by fs even without the run-mode guard
	exe, _ = e.run("sandbox", "read_file", map[string]string{"path": "../../etc/passwd"})
	if !exe.Result.IsError || !strings.Contains(exe.Result.Content, "outside the allowed roots") {
		t.Fatal(exe.Result)
	}
	exe, _ = e.run("read-only", "write_file", map[string]string{"path": "x", "content": "y"})
	if !exe.Result.IsError || !strings.Contains(exe.Result.Content, "read-only") {
		t.Fatal(exe.Result)
	}
	// host mode is unconfined
	other := t.TempDir()
	os.WriteFile(filepath.Join(other, "h.txt"), []byte("host"), 0o644)
	exe, _ = e.run("host", "read_file", map[string]string{"path": filepath.Join(other, "h.txt")})
	if !strings.Contains(exe.Result.Content, "host") {
		t.Fatal(exe.Result)
	}
	// binary files are not dumped
	os.WriteFile(filepath.Join(e.ws, "bin"), []byte{0, 1, 2}, 0o644)
	exe, _ = e.run("sandbox", "read_file", map[string]string{"path": "bin"})
	if !strings.Contains(exe.Result.Content, "binary file") {
		t.Fatal(exe.Result)
	}
}

func TestBashCategoriesAndHostMode(t *testing.T) {
	e := setup(t, sandboxnone.Plugin)
	exe, cat := e.run("host", "bash", map[string]string{"command": "echo hello; echo oops >&2"})
	if exe.Result.IsError || cat != tools.CategoryExec || !strings.Contains(exe.Result.Content, "Exit code: 0") ||
		!strings.Contains(exe.Result.Content, "<stdout>\nhello\n</stdout>") || !strings.Contains(exe.Result.Content, "oops") {
		t.Fatal(exe.Result, cat)
	}
	exe, _ = e.run("host", "bash", map[string]string{"command": "exit 7"})
	if !exe.Result.IsError || !strings.Contains(exe.Result.Content, "Exit code: 7") {
		t.Fatal(exe.Result)
	}
	_, cat = e.run("host", "bash", map[string]string{"command": "git push --dry-run nowhere 2>/dev/null; true"})
	if cat != tools.CategoryPush {
		t.Fatal(cat)
	}
	exe, _ = e.run("host", "bash", map[string]any{"command": "sleep 5", "timeout_seconds": 0.2})
	if !strings.Contains(exe.Result.Content, "timed out") {
		t.Fatal(exe.Result)
	}
}

func TestBashSandboxUnavailableNotRun(t *testing.T) {
	e := setup(t, sandboxnone.Plugin) // no real backend: confined modes must fail closed
	marker := filepath.Join(e.ws, "ran")
	for _, mode := range []string{"sandbox", "read-only", ""} {
		exe, _ := e.run(mode, "bash", map[string]string{"command": "touch " + marker})
		if !exe.Result.IsError || !strings.Contains(exe.Result.Content, sandbox.CodeUnavailable) || !strings.Contains(exe.Result.Content, "NOT run") {
			t.Fatalf("%q: %v", mode, exe.Result)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Fatalf("%q: command ran without a sandbox", mode)
		}
	}
}

func TestBashSeatbeltBlocked(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS only")
	}
	e := setup(t, confinedSandbox())
	wd, _ := os.Getwd()
	skipIfUnderTemp(t, wd)
	base, _ := os.MkdirTemp(wd, ".tbtest-")
	defer os.RemoveAll(base)
	e.ws = filepath.Join(base, "ws")
	os.MkdirAll(e.ws, 0o755)
	outside := filepath.Join(base, "outside.txt")

	exe, _ := e.run("sandbox", "bash", map[string]string{"command": "echo in > in.txt && cat in.txt"})
	if exe.Result.IsError || !strings.Contains(exe.Result.Content, "in") {
		t.Fatal(exe.Result)
	}
	exe, _ = e.run("sandbox", "bash", map[string]string{"command": "echo x > " + outside})
	if !exe.Result.IsError || !strings.Contains(exe.Result.Content, "sandbox blocked") {
		t.Fatal(exe.Result)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("wrote outside the workspace")
	}
	exe, _ = e.run("read-only", "bash", map[string]string{"command": "echo x > ro.txt"})
	if !exe.Result.IsError {
		t.Fatal(exe.Result)
	}
	if _, err := os.Stat(filepath.Join(e.ws, "ro.txt")); err == nil {
		t.Fatal("read-only mode wrote")
	}
}

func TestMappings(t *testing.T) {
	a, err := toolsbasic.AccessFor(tools.Call{Agent: "kai", Mode: "read-only", Workspace: "/w"})
	if err != nil || !a.ReadOnly || a.Roots[0] != "/w" {
		t.Fatal(a, err)
	}
	if a, _ := toolsbasic.AccessFor(tools.Call{Mode: "host"}); a.Roots != nil || a.ReadOnly {
		t.Fatal(a)
	}
	if _, err := toolsbasic.AccessFor(tools.Call{Mode: "sandbox"}); err == nil {
		t.Fatal("confined mode without workspace")
	}
	if a, _ := toolsbasic.AccessFor(tools.Call{Mode: "weird", Workspace: "/w"}); !a.ReadOnly {
		t.Fatal("unknown mode fails closed to read-only")
	}
	if p := toolsbasic.PolicyFor(tools.Call{Mode: "sandbox", Workspace: "/w"}, nil); p.Mode != sandbox.ModeWorkspaceWrite || p.WritableRoots[0] != "/w" {
		t.Fatal(p)
	}
	if p, _ := toolsbasic.ResolvePath(tools.Call{Workspace: "/w"}, "a/../b"); p != "/w/b" {
		t.Fatal(p)
	}
	if _, err := toolsbasic.ResolvePath(tools.Call{}, "rel"); err == nil {
		t.Fatal("relative without workspace")
	}
}

func TestPendingWithoutCapabilities(t *testing.T) {
	h := kernel.NewHost(nil)
	defer h.Close()
	h.Load(tools.Plugin, kernel.LoadOptions{})
	h.Load(toolsbasic.Plugin, kernel.LoadOptions{})
	p, _ := h.Plugin("@crewbase/tools-basic")
	if p.State != kernel.StatePending || len(p.Missing) != 2 {
		t.Fatal(p)
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

func TestPathArgsDeclared(t *testing.T) {
	e := setup(t, sandboxnone.Plugin)
	for name, want := range map[string]string{"read_file": "path", "write_file": "path", "edit_file": "path", "list_dir": "path", "delete_file": "path", "bash": "cwd"} {
		tl, ok := e.reg.Get("kai", name)
		if !ok || len(tl.PathArgs) != 1 || tl.PathArgs[0] != want {
			t.Errorf("%s: %v", name, tl)
		}
	}
}

// bash {background: true} returns at once; job_output shows live output;
// the result comes back to the agent's conversation when the job ends;
// job_stop ends a job without a report.
func TestBackgroundJobs(t *testing.T) {
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	root := t.TempDir()
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, map[string]any{"dir": root + "/sessions"}}, {prompt.Plugin, nil}, {tools.Plugin, nil},
		{approval.Plugin, nil}, {sandboxnone.Plugin, nil}, {fslocal.Plugin, nil}, {shelllocal.Plugin, nil},
		{agent.Plugin, map[string]any{"root": root}}, {toolsbasic.Plugin, nil},
	} {
		if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
			t.Fatal(err)
		}
	}
	reg := kernel.Use[*tools.Registry](h.Root(), tools.ServiceKey)
	agents := kernel.Use[*agent.Registry](h.Root(), agent.ServiceKey)
	a, err := agents.Create(agent.Spec{Name: "Builder"})
	if err != nil {
		t.Fatal(err)
	}
	ss, _ := a.Session()
	a.Pause() // keep reports in the queue
	run := func(name string, args any) tools.Result {
		b, _ := json.Marshal(args)
		return reg.Run(context.Background(), h.Root(), tools.Call{ID: session.NewID("call"), Name: name, Args: b, Agent: a.ID(), Session: ss, Mode: "host", Workspace: a.Workspace()}).Result
	}

	start := time.Now()
	r := run("bash", map[string]any{"command": "echo compiling; sleep 1; echo 'FAIL: TestLogin'; exit 3", "background": true})
	if r.IsError || !strings.Contains(r.Content, "Started job1") || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("start: %v %+v", time.Since(start), r)
	}
	time.Sleep(300 * time.Millisecond)
	if r := run("job_output", map[string]any{"id": "job1"}); !strings.Contains(r.Content, "running") || !strings.Contains(r.Content, "compiling") {
		t.Fatalf("live output: %s", r.Content)
	}
	// another agent cannot see it
	b, _ := json.Marshal(map[string]any{"id": "job1"})
	if r := reg.Run(context.Background(), h.Root(), tools.Call{ID: "x", Name: "job_output", Args: b, Agent: "someone-else", Mode: "host"}).Result; !r.IsError {
		t.Fatal("another agent read the job")
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(a.Queue()) == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	q := a.Queue()
	if len(q) != 1 || q[0].Session != ss.ID() || q[0].Tag != "job:job1" || !strings.Contains(q[0].Text, "exited 3") || !strings.Contains(q[0].Text, "FAIL: TestLogin") {
		t.Fatalf("report: %+v", q)
	}
	if r := run("job_list", map[string]any{}); !strings.Contains(r.Content, "job1  exited 3") {
		t.Fatalf("list: %s", r.Content)
	}

	// stopped jobs end at once and are not reported
	run("bash", map[string]any{"command": "sleep 30", "background": true})
	time.Sleep(200 * time.Millisecond)
	if r := run("job_stop", map[string]any{"id": "job2"}); r.IsError {
		t.Fatalf("stop: %s", r.Content)
	}
	deadline = time.Now().Add(3 * time.Second)
	for !strings.Contains(run("job_output", map[string]any{"id": "job2"}).Content, "stopped") && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if len(a.Queue()) != 1 {
		t.Fatalf("a stopped job was reported: %+v", a.Queue())
	}
}
