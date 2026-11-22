package gitcli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/capabilities/vcs"
)

func newGit(t *testing.T) *Git {
	t.Helper()
	g, err := New()
	if err != nil {
		t.Skip("git not installed")
	}
	t.Cleanup(g.Close)
	return g
}

// repo makes a repository with one commit on main.
func repo(t *testing.T, g *Git) string {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	if _, err := g.run(ctx, dir, opts{write: true}, "init", "--quiet", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "app.go", "package app\n\nfunc A() int { return 1 }\n\nfunc B() int { return 2 }\n")
	write(t, dir, "long.go", "package app\n\n// 1\n// 2\n// 3\n// 4\n// 5\n// 6\n// 7\n// 8\nvar Z = 0\n")
	write(t, dir, "README.md", "# app\n")
	if _, err := g.Commit(ctx, dir, vcs.CommitRequest{Message: "init", All: true, Author: vcs.Signature{Name: "T", Email: "t@x"}}); err != nil {
		t.Fatal(err)
	}
	return real(dir)
}

func write(t *testing.T, dir, rel, s string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWorktreeDiffHunkCommit(t *testing.T) {
	g := newGit(t)
	ctx := context.Background()
	main := repo(t, g)
	info, err := g.Probe(ctx, main)
	if err != nil || info.Branch != "main" || info.Main != main || info.Linked {
		t.Fatalf("%v %+v", err, info)
	}
	wt := filepath.Join(t.TempDir(), "wt1")
	if err := g.AddWorktree(ctx, main, wt, "crew/a", "main"); err != nil {
		t.Fatal(err)
	}
	wi, _ := g.Probe(ctx, wt)
	if wi.ID != info.ID || !wi.Linked || wi.Branch != "crew/a" {
		t.Fatalf("%+v", wi)
	}
	// two separate changes in one file, one new file
	write(t, wt, "app.go", "package app\n\nfunc A() int { return 10 }\n\nfunc B() int { return 2 }\n")
	write(t, wt, "long.go", "package app\n\n// one\n// 2\n// 3\n// 4\n// 5\n// 6\n// 7\n// 8\nvar Z = 0\n\nfunc C() int { return 3 }\n")
	write(t, wt, "notes/new.md", "hello\n")
	fs, err := g.Diff(ctx, wt, vcs.DiffOptions{Untracked: true})
	if err != nil || len(fs) != 3 {
		t.Fatalf("%v %+v", err, fs)
	}
	app := fs[1]
	if app.Path != "long.go" || len(app.Hunks) != 2 || app.Added != 3 || app.Removed != 1 {
		t.Fatalf("%+v", app)
	}
	if fs[2].Status != vcs.StUntracked || fs[2].Added != 1 {
		t.Fatalf("%+v", fs[2])
	}
	// stage only the first hunk
	if err := g.ApplyHunk(ctx, wt, "long.go", app.Hunks[0].ID, vcs.HunkStage); err != nil {
		t.Fatal(err)
	}
	staged, _ := g.Diff(ctx, wt, vcs.DiffOptions{Side: vcs.DiffStaged})
	if len(staged) != 1 || len(staged[0].Hunks) != 1 || staged[0].Hunks[0].ID != app.Hunks[0].ID {
		t.Fatalf("%+v", staged)
	}
	// a stale id is refused
	if err := g.ApplyHunk(ctx, wt, "long.go", "hdeadbeef", vcs.HunkStage); !errors.Is(err, vcs.ErrStaleHunk) {
		t.Fatal(err)
	}
	// discard the second (unstaged) hunk
	rest, _ := g.Diff(ctx, wt, vcs.DiffOptions{Paths: []string{"long.go"}})
	if err := g.ApplyHunk(ctx, wt, "long.go", rest[0].Hunks[0].ID, vcs.HunkDiscard); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(wt, "long.go"))
	if strings.Contains(string(b), "func C") || !strings.Contains(string(b), "// one") {
		t.Fatalf("%s", b)
	}
	// unstage it again, stage again, commit
	if err := g.ApplyHunk(ctx, wt, "long.go", staged[0].Hunks[0].ID, vcs.HunkUnstage); err != nil {
		t.Fatal(err)
	}
	if st, _ := g.Status(ctx, wt); len(st.Files) != 3 || st.Files[1].Staged {
		t.Fatalf("%+v", st)
	}
	c, err := g.Commit(ctx, wt, vcs.CommitRequest{Message: "A returns 10", All: true, Author: vcs.Signature{Name: "Kai", Email: "kai@crewbase.local"}})
	if err != nil || c.Author != "Kai" {
		t.Fatalf("%v %+v", err, c)
	}
	if _, err := g.Commit(ctx, wt, vcs.CommitRequest{Message: "again", All: true}); !errors.Is(err, vcs.ErrNothing) {
		t.Fatal(err)
	}
	// what the branch adds
	rd, _ := g.Diff(ctx, wt, vcs.DiffOptions{Side: vcs.DiffRange, Base: "main"})
	if len(rd) != 3 {
		t.Fatalf("%+v", rd)
	}
	wts, _ := g.Worktrees(ctx, main)
	if len(wts) != 2 || !wts[0].Main || wts[1].Branch != "crew/a" {
		t.Fatalf("%+v", wts)
	}
	// remove with the branch
	if err := g.RemoveWorktree(ctx, main, wt, false, "crew/a"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Resolve(ctx, main, "crew/a"); err == nil {
		t.Fatal("branch kept")
	}
}

func TestMergePreviewAndMerge(t *testing.T) {
	g := newGit(t)
	ctx := context.Background()
	main := repo(t, g)
	sig := vcs.Signature{Name: "Kai", Email: "kai@x"}
	wt := filepath.Join(t.TempDir(), "wt")
	g.AddWorktree(ctx, main, wt, "feat", "main")
	write(t, wt, "app.go", "package app\n\nfunc A() int { return 1 }\n\nfunc B() int { return 20 }\n")
	g.Commit(ctx, wt, vcs.CommitRequest{Message: "B", All: true, Author: sig})
	// main moves on elsewhere in the file
	write(t, main, "README.md", "# app\n\nmore\n")
	g.Commit(ctx, main, vcs.CommitRequest{Message: "readme", All: true, Author: sig})

	p, err := g.MergePreview(ctx, main, "main", "feat")
	if err != nil || !p.Clean || p.FastForward || p.Ahead != 1 || p.Behind != 1 || p.CheckedOutIn != main || len(p.Files) != 1 {
		t.Fatalf("%v %+v", err, p)
	}
	// main has a local change: the merge is refused, nothing moves
	write(t, main, "app.go", "dirty\n")
	if _, err := g.Merge(ctx, main, vcs.MergeRequest{Target: "main", Source: "feat", Author: sig}); !errors.Is(err, vcs.ErrDirty) {
		t.Fatal(err)
	}
	g.Discard(ctx, main, []string{"app.go"})
	// reviewed at an older commit: refused
	if _, err := g.Merge(ctx, main, vcs.MergeRequest{Target: "main", Source: "feat", Expect: "0000000"}); err == nil {
		t.Fatal("stale review merged")
	}
	c, err := g.Merge(ctx, main, vcs.MergeRequest{Target: "main", Source: "feat", Author: sig, Expect: p.SourceHead[:8]})
	if err != nil || len(c.Parents) != 2 {
		t.Fatalf("%v %+v", err, c)
	}
	b, _ := os.ReadFile(filepath.Join(main, "app.go"))
	if !strings.Contains(string(b), "return 20") {
		t.Fatal("checked-out main not updated")
	}
	if st, _ := g.Status(ctx, main); !st.Clean {
		t.Fatalf("%+v", st)
	}
	if _, err := g.Merge(ctx, main, vcs.MergeRequest{Target: "main", Source: "feat"}); !errors.Is(err, vcs.ErrUpToDate) {
		t.Fatal(err)
	}
}

func TestConflictResolve(t *testing.T) {
	g := newGit(t)
	ctx := context.Background()
	main := repo(t, g)
	sig := vcs.Signature{Name: "Kai", Email: "kai@x"}
	wt := filepath.Join(t.TempDir(), "wt")
	g.AddWorktree(ctx, main, wt, "feat", "main")
	write(t, wt, "app.go", "package app\n\nfunc A() int { return 100 }\n\nfunc B() int { return 2 }\n")
	g.Commit(ctx, wt, vcs.CommitRequest{Message: "A=100", All: true, Author: sig})
	write(t, main, "app.go", "package app\n\nfunc A() int { return 7 }\n\nfunc B() int { return 2 }\n")
	g.Commit(ctx, main, vcs.CommitRequest{Message: "A=7", All: true, Author: sig})

	p, _ := g.MergePreview(ctx, main, "main", "feat")
	if p.Clean || len(p.Conflicts) != 1 || p.Conflicts[0] != "app.go" {
		t.Fatalf("%+v", p)
	}
	// main's working tree was not touched by the preview
	if st, _ := g.Status(ctx, main); !st.Clean {
		t.Fatal("preview touched main")
	}
	if _, err := g.Merge(ctx, main, vcs.MergeRequest{Target: "main", Source: "feat"}); !errors.Is(err, vcs.ErrConflict) {
		t.Fatal(err)
	}
	// resolve in the worktree: bring main in, fix, conclude
	cs, err := g.StartMerge(ctx, wt, "main", sig)
	if err != nil || len(cs) != 1 || len(cs[0].Blocks) != 1 || !strings.Contains(cs[0].Blocks[0].Ours, "100") || !strings.Contains(cs[0].Blocks[0].Theirs, "7") {
		t.Fatalf("%v %+v", err, cs)
	}
	if err := g.ResolveConflict(ctx, wt, "app.go", "manual"); err == nil {
		t.Fatal("markers accepted")
	}
	write(t, wt, "app.go", "package app\n\nfunc A() int { return 107 }\n\nfunc B() int { return 2 }\n")
	if err := g.ResolveConflict(ctx, wt, "app.go", "manual"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.ConcludeMerge(ctx, wt, "", sig); err != nil {
		t.Fatal(err)
	}
	p, _ = g.MergePreview(ctx, main, "main", "feat")
	if !p.Clean || !p.FastForward {
		t.Fatalf("%+v", p)
	}
	if _, err := g.Merge(ctx, main, vcs.MergeRequest{Target: "main", Source: "feat", Strategy: vcs.MergeFFOnly}); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveAndHooksOff(t *testing.T) {
	g := newGit(t)
	ctx := context.Background()
	main := repo(t, g)
	// a hook the repository planted must not run
	marker := filepath.Join(t.TempDir(), "ran")
	write(t, main, ".git/hooks/pre-commit", "#!/bin/sh\ntouch "+marker+"\n")
	os.Chmod(filepath.Join(main, ".git/hooks/pre-commit"), 0o755)
	write(t, main, ".git/hooks/post-commit", "#!/bin/sh\ntouch "+marker+"\n")
	os.Chmod(filepath.Join(main, ".git/hooks/post-commit"), 0o755)
	wt := filepath.Join(t.TempDir(), "wt")
	g.AddWorktree(ctx, main, wt, "feat", "main")
	write(t, wt, "x.txt", "x\n")
	g.Commit(ctx, wt, vcs.CommitRequest{Message: "x", All: true})
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a repository hook ran")
	}
	write(t, wt, "y.txt", "uncommitted\n")
	write(t, wt, "README.md", "changed\n")
	out := t.TempDir()
	patch, bundle, err := g.Archive(ctx, wt, "main", out)
	if err != nil || patch == "" || bundle == "" {
		t.Fatalf("%v %q %q", err, patch, bundle)
	}
	pb, _ := os.ReadFile(patch)
	if !strings.Contains(string(pb), "y.txt") || !strings.Contains(string(pb), "changed") {
		t.Fatalf("%s", pb)
	}
	// the index was not touched
	if st, _ := g.Status(ctx, wt); st.Files[0].Staged || st.Files[1].Staged {
		t.Fatalf("%+v", st)
	}
	if err := exec.Command("git", "bundle", "verify", bundle).Run(); err != nil {
		// verify needs the prerequisite commits: run it in the repo
		c := exec.Command("git", "bundle", "verify", bundle)
		c.Dir = main
		if b, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, b)
		}
	}
	// a dirty worktree is not removed without force
	if err := g.RemoveWorktree(ctx, main, wt, false, ""); err == nil {
		t.Fatal("dirty worktree removed")
	}
	if err := g.RemoveWorktree(ctx, main, wt, true, "feat"); err != nil {
		t.Fatal(err)
	}
}

func TestRefsAndHosts(t *testing.T) {
	for _, bad := range []string{"-x", "a b", "a..b", "x.lock", "", "a~1", "--upload-pack=x"} {
		if checkRef(bad) == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	for in, want := range map[string]string{
		"https://github.com/o/r.git": "github.com", "git@GitHub.com:o/r.git": "github.com", "ssh://git@gitlab.example.com:2222/o/r": "gitlab.example.com", "/tmp/x": "",
	} {
		if got := HostOf(in); got != want {
			t.Fatalf("%s: %q", in, got)
		}
	}
	if scrub("token abcd1234 in url", []string{"abcd1234"}) != "token *** in url" {
		t.Fatal("not scrubbed")
	}
}
