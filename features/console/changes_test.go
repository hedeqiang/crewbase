package console_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/toolsbasic"
	"github.com/hedeqiang/crewbase/kernel"
	fslocal "github.com/hedeqiang/crewbase/providers/fs/local"
	sandboxnone "github.com/hedeqiang/crewbase/providers/sandbox/none"
	shelllocal "github.com/hedeqiang/crewbase/providers/shell/local"
)

// runTool runs a file tool as the agent would (through the pre-execute
// hook the change tracker is on).
func runTool(t *testing.T, h *kernel.Host, a *agent.Agent, name string, args map[string]string) {
	t.Helper()
	reg := kernel.Use[*tools.Registry](h.Root(), tools.ServiceKey)
	b, _ := json.Marshal(args)
	exe := reg.Run(context.Background(), h.Root(), tools.Call{ID: "c-" + name, Name: name, Args: b, Agent: a.ID(), Workspace: a.Workspace(), Mode: agent.ModeHost})
	if exe.Result.IsError {
		t.Fatalf("%s: %s", name, exe.Result.Content)
	}
}

func files(out map[string]any) map[string]map[string]any {
	m := map[string]map[string]any{}
	for _, f := range out["files"].([]any) {
		x := f.(map[string]any)
		m[x["path"].(string)] = x
	}
	return m
}

// A workspace outside git: changes are what the agent's file tools did,
// compared with the files as they were before.
func TestTrackedChanges(t *testing.T) {
	base, _, h := start(t)
	loadFileTools(t, h)
	agents := kernel.Use[*agent.Registry](h.Root(), agent.ServiceKey)
	a, _ := agents.Create(agent.Spec{Name: "Kai"})
	ws := a.Workspace()
	_ = os.WriteFile(filepath.Join(ws, "notes.md"), []byte("one\ntwo\nthree\n"), 0o644)
	_ = os.WriteFile(filepath.Join(ws, "old.txt"), []byte("bye\n"), 0o644)

	_, out := do(t, base, "GET", "/api/agents/kai/changes", nil)
	if out["source"] != "tracked" || len(out["files"].([]any)) != 0 {
		t.Fatalf("before: %v", out)
	}
	runTool(t, h, a, "read_file", map[string]string{"path": "notes.md"})
	runTool(t, h, a, "edit_file", map[string]string{"path": "notes.md", "old_string": "two", "new_string": "2"})
	runTool(t, h, a, "write_file", map[string]string{"path": "src/new.go", "content": "package x\n\nfunc A() {}\n"})
	// delete_file asks for approval; keep the file as its hook does, then delete
	tryDelete(t, h, a, "old.txt")

	_, out = do(t, base, "GET", "/api/agents/kai/changes", nil)
	fs := files(out)
	if len(fs) != 3 || fs["notes.md"]["status"] != "modified" || fs["src/new.go"]["status"] != "added" || fs["old.txt"]["status"] != "deleted" {
		t.Fatalf("changes: %v", out)
	}
	if fs["notes.md"]["added"].(float64) != 1 || fs["notes.md"]["removed"].(float64) != 1 || fs["src/new.go"]["added"].(float64) != 3 {
		t.Fatalf("counts: %v", fs)
	}
	if out["added"].(float64) != 4 || out["removed"].(float64) != 2 {
		t.Fatalf("totals %v %v", out["added"], out["removed"])
	}
	// a second edit compares with the first original, not the last write
	runTool(t, h, a, "read_file", map[string]string{"path": "notes.md"})
	runTool(t, h, a, "edit_file", map[string]string{"path": "notes.md", "old_string": "three", "new_string": "3"})
	_, d := do(t, base, "GET", "/api/agents/kai/changes/diff?path=notes.md", nil)
	hunk := d["hunks"].([]any)[0].(map[string]any)
	lines := hunk["lines"].([]any)
	if len(lines) != 5 || d["added"].(float64) != 2 {
		t.Fatalf("diff %v", d)
	}
	// the copies are kept out of the workspace
	if _, err := os.Stat(filepath.Join(ws, ".crewbase")); err == nil {
		t.Fatal("kept copies in the workspace")
	}

	// 撤销: each kind goes back
	for _, p := range []string{"notes.md", "src/new.go", "old.txt"} {
		if code, _ := do(t, base, "POST", "/api/agents/kai/changes/revert", map[string]string{"path": p}); code != 200 {
			t.Fatalf("revert %s: %d", p, code)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(ws, "notes.md")); string(b) != "one\ntwo\nthree\n" {
		t.Fatalf("notes.md: %q", b)
	}
	if _, err := os.Stat(filepath.Join(ws, "src/new.go")); err == nil {
		t.Fatal("new file not removed")
	}
	if b, _ := os.ReadFile(filepath.Join(ws, "old.txt")); string(b) != "bye\n" {
		t.Fatal("deleted file not back")
	}
	_, out = do(t, base, "GET", "/api/agents/kai/changes", nil)
	if len(out["files"].([]any)) != 0 {
		t.Fatalf("after revert: %v", out)
	}

	// 全部保留: the workspace as it is becomes the starting point
	runTool(t, h, a, "write_file", map[string]string{"path": "a.txt", "content": "x\n"})
	if code, _ := do(t, base, "POST", "/api/agents/kai/changes/accept", nil); code != 200 {
		t.Fatal("accept")
	}
	_, out = do(t, base, "GET", "/api/agents/kai/changes", nil)
	if len(out["files"].([]any)) != 0 {
		t.Fatalf("after accept: %v", out)
	}
	// paths outside the workspace are refused
	if code, _ := do(t, base, "GET", "/api/agents/kai/changes/diff?path=../../token", nil); code != 400 {
		t.Fatalf("escape: %d", code)
	}
}

// A workspace in a git repository: every change counts, compared with HEAD.
func TestGitChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	base, _, h := start(t)
	agents := kernel.Use[*agent.Registry](h.Root(), agent.ServiceKey)
	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	_ = os.WriteFile(filepath.Join(repo, "app.js"), []byte("const a = 1;\nconst b = 2;\n"), 0o644)
	_ = os.WriteFile(filepath.Join(repo, "gone.txt"), []byte("x\n"), 0o644)
	_ = os.WriteFile(filepath.Join(repo, "logo.png"), []byte{0x89, 'P', 'N', 'G', 0, 1, 2}, 0o644)
	run("add", ".")
	run("commit", "-q", "-m", "init")
	a, err := agents.Create(agent.Spec{Name: "Git", SharedWorkspace: repo})
	if err != nil {
		t.Fatal(err)
	}
	// changes made any way (here: directly, as bash or the person would)
	_ = os.WriteFile(filepath.Join(repo, "app.js"), []byte("const a = 1;\nconst b = 3;\nconst c = 4;\n"), 0o644)
	_ = os.Remove(filepath.Join(repo, "gone.txt"))
	_ = os.WriteFile(filepath.Join(repo, "new.md"), []byte("# hi\n"), 0o644)
	_ = os.WriteFile(filepath.Join(repo, "logo.png"), []byte{0x89, 'P', 'N', 'G', 0, 9, 9}, 0o644)

	_, out := do(t, base, "GET", "/api/agents/"+a.ID()+"/changes", nil)
	fs := files(out)
	if out["source"] != "git" || out["branch"] != "main" || len(fs) != 4 {
		t.Fatalf("%v", out)
	}
	if fs["app.js"]["status"] != "modified" || fs["app.js"]["added"].(float64) != 2 || fs["app.js"]["removed"].(float64) != 1 {
		t.Fatalf("app.js %v", fs["app.js"])
	}
	if fs["gone.txt"]["status"] != "deleted" || fs["new.md"]["status"] != "added" || fs["new.md"]["added"].(float64) != 1 || fs["logo.png"]["binary"] != true {
		t.Fatalf("%v", fs)
	}
	_, d := do(t, base, "GET", "/api/agents/"+a.ID()+"/changes/diff?path=app.js", nil)
	if len(d["hunks"].([]any)) != 1 {
		t.Fatalf("diff %v", d)
	}
	// 撤销 restores HEAD; a new file goes away
	do(t, base, "POST", "/api/agents/"+a.ID()+"/changes/revert", map[string]string{"path": "app.js"})
	do(t, base, "POST", "/api/agents/"+a.ID()+"/changes/revert", map[string]string{"path": "new.md"})
	do(t, base, "POST", "/api/agents/"+a.ID()+"/changes/revert", map[string]string{"path": "gone.txt"})
	if b, _ := os.ReadFile(filepath.Join(repo, "app.js")); string(b) != "const a = 1;\nconst b = 2;\n" {
		t.Fatalf("app.js %q", b)
	}
	if _, err := os.Stat(filepath.Join(repo, "new.md")); err == nil {
		t.Fatal("new.md kept")
	}
	if _, err := os.Stat(filepath.Join(repo, "gone.txt")); err != nil {
		t.Fatal("gone.txt not back")
	}
	// 全部保留 is for tracked workspaces only
	if code, _ := do(t, base, "POST", "/api/agents/"+a.ID()+"/changes/accept", nil); code != 400 {
		t.Fatalf("accept in git: %d", code)
	}
}

// loadFileTools adds the file tools (and a sandbox for them) to a console
// test host.
func loadFileTools(t *testing.T, h *kernel.Host) {
	t.Helper()
	for _, s := range []kernel.Spec{sandboxnone.Plugin, fslocal.Plugin, shelllocal.Plugin, toolsbasic.Plugin} {
		if _, err := h.Load(s, kernel.LoadOptions{}); err != nil {
			t.Fatal(err)
		}
	}
}

// tryDelete runs delete_file through the hooks; when the approval is not
// granted in the test host, it removes the file itself after the hook ran.
func tryDelete(t *testing.T, h *kernel.Host, a *agent.Agent, rel string) {
	t.Helper()
	reg := kernel.Use[*tools.Registry](h.Root(), tools.ServiceKey)
	b, _ := json.Marshal(map[string]string{"path": rel})
	exe := reg.Run(context.Background(), h.Root(), tools.Call{ID: "c-del", Name: "delete_file", Args: b, Agent: a.ID(), Workspace: a.Workspace(), Mode: agent.ModeHost})
	if exe.Result.IsError {
		_ = os.Remove(filepath.Join(a.Workspace(), rel))
	}
}
