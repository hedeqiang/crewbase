package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/capabilities/fs"
	"github.com/hedeqiang/crewbase/capabilities/vcs"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/fs/local"
	"github.com/hedeqiang/crewbase/providers/gitcli"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

type rig struct {
	s      *Service
	h      *kernel.Host
	fs     fs.FS
	agents *agent.Registry
	repo   string // the project (main working tree)
	mu     sync.Mutex
	handed []agent.Input // what reviews handed to agents
}

func (r *rig) handedTo(tag string) []agent.Input {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []agent.Input
	for _, in := range r.handed {
		if strings.HasPrefix(in.Tag, tag) {
			out = append(out, in)
		}
	}
	return out
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@x", "-c", "init.defaultBranch=main"}, args...)...)
	c.Dir = dir
	if b, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, b)
	}
}

func setup(t *testing.T) *rig {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	proj := filepath.Join(t.TempDir(), "proj")
	os.MkdirAll(proj, 0o755)
	gitRun(t, proj, "init", "-q")
	os.WriteFile(filepath.Join(proj, "api.go"), []byte("package api\n\n// Version is agreed.\nconst Version = 1\n\nfunc Handler() {}\n\nfunc Other() {}\n"), 0o644)
	gitRun(t, proj, "add", "-A")
	gitRun(t, proj, "commit", "-q", "-m", "init")
	proj, _ = filepath.EvalSymlinks(proj)

	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, map[string]any{"dir": root + "/sessions"}},
		{prompt.Plugin, nil}, {tools.Plugin, nil}, {approval.Plugin, nil},
		{llm.Plugin, nil}, {mock.Plugin, nil}, {router.Plugin, nil},
		{agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"mock/default"}}},
		{agentloop.Plugin, nil},
		{local.Plugin, nil},
		{gitcli.Plugin, nil},
		{Plugin, map[string]any{"root": root}},
	} {
		if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
			t.Fatal(x.s.Name, err)
		}
	}
	s, _ := kernel.Lookup[*Service](h.Root(), ServiceKey)
	agents, _ := kernel.Lookup[*agent.Registry](h.Root(), agent.ServiceKey)
	f, _ := kernel.Lookup[fs.FS](h.Root(), fs.ServiceKey)
	r := &rig{s: s, h: h, fs: f, agents: agents, repo: proj}
	s.enqueue = func(a *agent.Agent, in agent.Input) error {
		r.mu.Lock()
		in.Agent = a.ID()
		r.handed = append(r.handed, in)
		r.mu.Unlock()
		return nil
	}
	for _, id := range []string{"lead", "reviewer", "security"} {
		a, err := agents.Create(agent.Spec{ID: id, Name: strings.ToUpper(id[:1]) + id[1:], Mode: agent.ModeSandbox})
		if err != nil {
			t.Fatal(err)
		}
		if id == "lead" {
			if err := a.SetProjectDir(proj); err != nil {
				t.Fatal(err)
			}
		}
	}
	return r
}

// child makes an ephemeral sub-agent of lead, as the subagent runner does.
func (r *rig) child(t *testing.T, id string) *agent.Agent {
	t.Helper()
	a, err := r.agents.Create(agent.Spec{ID: id, Name: id, Ephemeral: true, Parent: "lead", Mode: agent.ModeSandbox})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (r *rig) write(t *testing.T, who, path, content string) error {
	t.Helper()
	acc := fs.Access{Agent: who, Roots: []string{filepath.Dir(path)}}
	if _, err := os.Stat(path); err == nil {
		if _, err := r.fs.Read(context.Background(), acc, path); err != nil {
			t.Fatal(err)
		}
	}
	_, err := r.fs.Write(context.Background(), acc, path, []byte(content))
	return err
}

func TestSubAgentsGetWorktreesAndReviewFlow(t *testing.T) {
	r := setup(t)
	ctx := context.Background()
	lead, _ := r.agents.Get("lead")

	// two sub-agents that may write: a worktree each, in parallel
	c1, c2 := r.child(t, "lead-sub-1"), r.child(t, "lead-sub-2")
	a1, ok := vcs.AllocateWorkspace.Run(r.h.Root(), ctx, vcs.AllocateRequest{Parent: "lead", Child: c1.ID(), Workspace: lead.Workspace(), Mode: agent.ModeSandbox, Task: "add Handler docs"})
	if !ok || a1.Worktree == "" || !strings.Contains(a1.Dir, filepath.Join("worktrees")) {
		t.Fatalf("%v %+v", ok, a1)
	}
	a2, _ := vcs.AllocateWorkspace.Run(r.h.Root(), ctx, vcs.AllocateRequest{Parent: "lead", Child: c2.ID(), Workspace: lead.Workspace(), Mode: agent.ModeSandbox, Task: "nothing"})
	if a2.Dir == a1.Dir || a2.Branch == a1.Branch {
		t.Fatal("sub-agents share a worktree")
	}
	// a read-only sub-agent stays in the parent's folder (worktree-on-write)
	ro, _ := vcs.AllocateWorkspace.Run(r.h.Root(), ctx, vcs.AllocateRequest{Parent: "lead", Child: "x", Workspace: lead.Workspace(), Mode: agent.ModeReadOnly})
	if ro.Dir != lead.Workspace() || ro.Worktree != "" {
		t.Fatalf("%+v", ro)
	}
	// a non-repository folder: no answer (the sub-agent's own folder)
	if _, ok := vcs.AllocateWorkspace.Run(r.h.Root(), ctx, vcs.AllocateRequest{Parent: "reviewer", Child: "y", Workspace: t.TempDir(), Mode: agent.ModeSandbox}); ok {
		t.Fatal("allocated outside a repository")
	}

	// sub-agent 1 works and commits; sub-agent 2 does nothing
	if err := r.write(t, c1.ID(), filepath.Join(a1.Dir, "api.go"), "package api\n\n// Version is agreed.\nconst Version = 1\n\n// Handler serves.\nfunc Handler() {}\n\nfunc Other() {}\n"); err != nil {
		t.Fatal(err)
	}
	g, _ := r.s.vcs()
	if _, err := g.Commit(ctx, a1.Dir, vcs.CommitRequest{Message: "docs", All: true, Author: r.s.signature(agentActor(c1.ID()))}); err != nil {
		t.Fatal(err)
	}
	note1 := a1.Release(ctx)
	if note2 := a2.Release(ctx); note2 != "" {
		t.Fatal("an empty worktree was kept:", note2)
	}
	if w, _ := r.s.Worktree(a2.Worktree); w.State != WTRemoved {
		t.Fatalf("%+v", w)
	}
	if _, err := g.Resolve(ctx, r.repo, a2.Branch); err == nil {
		t.Fatal("the empty branch was kept")
	}
	if !strings.Contains(note1, a1.Worktree) {
		t.Fatal(note1)
	}
	w1, _ := r.s.Worktree(a1.Worktree)
	if w1.Holder != "lead" {
		t.Fatalf("%+v", w1)
	}
	// the sub-agent is gone now; it still counts as lead's
	r.agents.Delete(c1.ID())

	// the author's side cannot review it: lead (the origin) is refused
	if _, err := r.s.Open(ctx, OpenRequest{Worktree: a1.Worktree, Reviewers: []string{"lead"}, Author: agentActor(c1.ID())}); !isCode(err, CodeSelfApproval) {
		t.Fatal(err)
	}
	rv, err := r.s.Open(ctx, OpenRequest{Worktree: a1.Worktree, Reviewers: []string{"reviewer"}, Author: agentActor(c1.ID()), Title: "Document Handler"})
	if err != nil || rv.State != RvOpen || rv.Target != "main" || rv.Origin != "agent:lead" {
		t.Fatalf("%v %+v", err, rv)
	}
	if got := r.handedTo("git-review:" + rv.ID); len(got) != 1 || got[0].Agent != "reviewer" {
		t.Fatalf("%+v", got)
	}
	// lead approving its own sub-agent's work: refused
	if _, err := r.s.Submit(ctx, SubmitRequest{ID: rv.ID, Actor: "agent:lead", Decision: VApprove}); !isCode(err, CodeSelfApproval) {
		t.Fatal(err)
	}
	// not approved yet: no merge
	if _, _, err := r.s.MergeReview(ctx, MergeReviewRequest{ID: rv.ID, Actor: "agent:lead"}); !isCode(err, CodeNotApproved) {
		t.Fatal(err)
	}
	// the reviewer asks for changes with a blocker: lead is asked to fix (round 1 of 3)
	rv, err = r.s.Submit(ctx, SubmitRequest{ID: rv.ID, Actor: "agent:reviewer", Decision: VRequestChanges, Body: "say what it serves",
		Comments: []CommentInput{{Path: "api.go", Line: 6, Severity: SevBlocker, Body: "Handler serves what?"}}})
	if err != nil || rv.State != RvChangesRequested {
		t.Fatalf("%v %+v", err, rv)
	}
	fix := r.handedTo("git-fix:" + rv.ID)
	if len(fix) != 1 || fix[0].Agent != "lead" || !strings.Contains(fix[0].Text, "Handler serves what?") || !strings.Contains(fix[0].Text, a1.Worktree) {
		t.Fatalf("%+v", fix)
	}
	// a fix round: a new sub-agent continues in the same worktree
	c3 := r.child(t, "lead-sub-3")
	a3, ok := vcs.AllocateWorkspace.Run(r.h.Root(), ctx, vcs.AllocateRequest{Parent: "lead", Child: c3.ID(), Workspace: lead.Workspace(), Mode: agent.ModeSandbox, Worktree: a1.Worktree})
	if !ok || a3.Dir != a1.Dir {
		t.Fatalf("%+v", a3)
	}
	if err := r.write(t, c3.ID(), filepath.Join(a3.Dir, "api.go"), "package api\n\n// Version is agreed.\nconst Version = 1\n\n// Handler serves the API.\nfunc Handler() {}\n\nfunc Other() {}\n"); err != nil {
		t.Fatal(err)
	}
	g.Commit(ctx, a3.Dir, vcs.CommitRequest{Message: "say what", All: true})
	a3.Release(ctx)
	// the blocker is answered; another round
	r.s.ResolveComment(rv.ID, rv.Comments[0].ID, "agent:lead", true)
	rv, err = r.s.Open(ctx, OpenRequest{ID: rv.ID, Author: "agent:lead"})
	if err != nil || rv.State != RvOpen || rv.Round != 2 {
		t.Fatalf("%v %+v", err, rv)
	}
	if len(r.handedTo("git-review:"+rv.ID)) != 2 {
		t.Fatal("reviewer not asked again")
	}
	rv, err = r.s.Submit(ctx, SubmitRequest{ID: rv.ID, Actor: "agent:reviewer", Decision: VApprove, Body: "good"})
	if err != nil || rv.State != RvApproved {
		t.Fatalf("%v %+v", err, rv)
	}
	if len(r.handedTo("git-approved:"+rv.ID)) != 1 {
		t.Fatal("author not told")
	}

	// new commits after the approval: it is stale, back to open
	os.WriteFile(filepath.Join(a1.Dir, "extra.txt"), []byte("x\n"), 0o644)
	g.Commit(ctx, a1.Dir, vcs.CommitRequest{Message: "extra", All: true})
	if _, _, err := r.s.MergeReview(ctx, MergeReviewRequest{ID: rv.ID, Actor: "agent:lead"}); !isCode(err, CodeNotApproved) {
		t.Fatal("a stale approval merged:", err)
	}
	r.s.Open(ctx, OpenRequest{ID: rv.ID, Author: "agent:lead"})
	r.s.Submit(ctx, SubmitRequest{ID: rv.ID, Actor: "agent:reviewer", Decision: VApprove})

	// someone froze the line the change touches: the merge is refused
	info, _ := r.s.Repo(ctx, r.repo)
	lk, err := r.s.AddLock(LockRequest{Repo: info.ID, Path: "api.go", Start: 5, End: 7, Holder: "agent:security", Reason: "audited"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.s.MergeReview(ctx, MergeReviewRequest{ID: rv.ID, Actor: "agent:lead"}); !isCode(err, CodeLockFrozen) {
		t.Fatal(err)
	}
	r.s.RemoveLock(lk.ID, "agent:security")
	rv, c, err := r.s.MergeReview(ctx, MergeReviewRequest{ID: rv.ID, Actor: "agent:lead"})
	if err != nil || rv.State != RvMerged || len(c.Parents) != 2 {
		t.Fatalf("%v %+v %+v", err, rv, c)
	}
	b, _ := os.ReadFile(filepath.Join(r.repo, "api.go"))
	if !strings.Contains(string(b), "serves the API") {
		t.Fatal("lead's folder (main checked out) did not get the merge")
	}
	// merged: the worktree goes without discard, its branch too
	if w, err := r.s.Remove(ctx, RemoveRequest{ID: a1.Worktree, Actor: "agent:lead"}); err != nil || w.State != WTMerged {
		t.Fatalf("%v %+v", err, w)
	}
}

func TestFreezeLocks(t *testing.T) {
	r := setup(t)
	ctx := context.Background()
	info, _ := r.s.Repo(ctx, r.repo)
	if _, err := r.s.AddLock(LockRequest{Repo: info.ID, Path: "api.go", Start: 3, End: 4, Holder: "agent:lead", Reason: "agreed API"}); err != nil {
		t.Fatal(err)
	}
	// an overlapping lock by someone else is refused
	if _, err := r.s.AddLock(LockRequest{Repo: info.ID, Path: "api.go", Start: 4, End: 6, Holder: "agent:security"}); !isCode(err, CodeLockFrozen) {
		t.Fatal(err)
	}
	p := filepath.Join(r.repo, "api.go")
	orig, _ := os.ReadFile(p)
	// another agent changing the frozen lines: refused
	err := r.write(t, "reviewer", p, strings.Replace(string(orig), "Version = 1", "Version = 2", 1))
	if err == nil || !strings.Contains(err.Error(), CodeLockFrozen) {
		t.Fatal(err)
	}
	// the same agent changing other lines: fine
	if err := r.write(t, "reviewer", p, strings.Replace(string(orig), "func Other() {}", "func Other() { _ = 1 }", 1)); err != nil {
		t.Fatal(err)
	}
	// the holder's own sub-agent may change them
	c := r.child(t, "lead-sub-9")
	cur, _ := os.ReadFile(p)
	if err := r.write(t, c.ID(), p, strings.Replace(string(cur), "Version = 1", "Version = 2", 1)); err != nil {
		t.Fatal(err)
	}
	// an edit inside the range by another agent: refused
	acc := fs.Access{Agent: "security", Roots: []string{r.repo}}
	r.fs.Read(ctx, acc, p)
	if _, err := r.fs.Edit(ctx, acc, p, "// Version is agreed.", "// Version changed."); err == nil || !strings.Contains(err.Error(), CodeLockFrozen) {
		t.Fatal(err)
	}
	// nor delete the file
	if err := r.fs.Delete(ctx, acc, p, false); err == nil {
		t.Fatal("deleted a frozen file")
	}
	// only the holder's side or a person lifts it
	l := r.s.Locks(info.ID)[0]
	if err := r.s.RemoveLock(l.ID, "agent:security"); !isCode(err, CodeLockFrozen) {
		t.Fatal(err)
	}
	if err := r.s.RemoveLock(l.ID, PersonActor("")); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveKeepsWork(t *testing.T) {
	r := setup(t)
	ctx := context.Background()
	w, err := r.s.Spawn(ctx, SpawnRequest{Dir: r.repo, Holder: "lead", Parent: "lead", Origin: "tool"})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(w.Path, "wip.txt"), []byte("half done\n"), 0o644)
	if _, err := r.s.Remove(ctx, RemoveRequest{ID: w.ID}); !isCode(err, CodeDirty) {
		t.Fatal(err)
	}
	out, err := r.s.Remove(ctx, RemoveRequest{ID: w.ID, Discard: true})
	if err != nil || out.State != WTArchived || out.Archive == nil || out.Archive.Patch == "" {
		t.Fatalf("%v %+v", err, out)
	}
	b, _ := os.ReadFile(out.Archive.Patch)
	if !strings.Contains(string(b), "half done") {
		t.Fatal("work not archived")
	}
	if _, err := os.Stat(w.Path); !os.IsNotExist(err) {
		t.Fatal("worktree folder left")
	}
}

func TestToolsClassify(t *testing.T) {
	r := setup(t)
	reg, _ := kernel.Lookup[*tools.Registry](r.h.Root(), tools.ServiceKey)
	lead, _ := r.agents.Get("lead")
	commit, ok := reg.Get("lead", "git_commit")
	if !ok {
		t.Fatal("no git_commit")
	}
	// lead's folder is on main: committing there asks (push category)
	cat, _ := commit.Classify(tools.Call{Agent: "lead", Workspace: lead.Workspace(), Args: []byte(`{"message":"x"}`)})
	if cat != tools.CategoryPush {
		t.Fatal(cat)
	}
	push, _ := reg.Get("lead", "git_push")
	if cat, _ := push.Classify(tools.Call{Agent: "lead", Workspace: lead.Workspace()}); cat != tools.CategoryPush {
		t.Fatal(cat)
	}
	hunk, _ := reg.Get("lead", "git_hunk_apply")
	if cat, _ := hunk.Classify(tools.Call{Args: []byte(`{"action":"discard","path":"a"}`)}); cat != tools.CategoryDelete {
		t.Fatal(cat)
	}
	names := 0
	for _, n := range []string{"git_status", "git_diff", "git_worktree_spawn", "git_hunk_apply", "git_commit", "git_review_open", "git_review_submit", "git_merge", "git_conflict_resolve", "git_push", "git_worktree_remove", "git_lock"} {
		if _, ok := reg.Get("lead", n); ok {
			names++
		}
	}
	if names != 12 {
		t.Fatal(names)
	}
}

func isCode(err error, code string) bool {
	var ge *Error
	return errors.As(err, &ge) && ge.Code == code
}

// The panel refreshes reviews as it shows them; asking for another round
// afterwards must still go to the reviewers (and be a new round).
func TestAgainAfterRefreshAsksReviewers(t *testing.T) {
	r := setup(t)
	ctx := context.Background()
	w, err := r.s.Spawn(ctx, SpawnRequest{Dir: r.repo, Holder: "lead", Parent: "lead", Origin: "tool"})
	if err != nil {
		t.Fatal(err)
	}
	g, _ := r.s.vcs()
	os.WriteFile(filepath.Join(w.Path, "a.txt"), []byte("a\n"), 0o644)
	g.Commit(ctx, w.Path, vcs.CommitRequest{Message: "a", All: true})
	rv, err := r.s.Open(ctx, OpenRequest{Worktree: w.ID, Reviewers: []string{"reviewer"}, Author: "agent:lead"})
	if err != nil {
		t.Fatal(err)
	}
	r.s.Submit(ctx, SubmitRequest{ID: rv.ID, Actor: "agent:reviewer", Decision: VApprove, Comments: []CommentInput{{Path: "a.txt", Line: 1, Severity: SevMinor, Body: "name it better"}}})
	if got := r.handedTo("git-approved:" + rv.ID); len(got) != 1 || !strings.Contains(got[0].Text, "name it better") {
		t.Fatalf("the approval does not carry its comments: %+v", got)
	}
	os.WriteFile(filepath.Join(w.Path, "b.txt"), []byte("b\n"), 0o644)
	g.Commit(ctx, w.Path, vcs.CommitRequest{Message: "b", All: true})
	if x, _ := r.s.refresh(ctx, rv.ID); x.State != RvOpen {
		t.Fatalf("a stale approval still counts: %s", x.State)
	}
	rv, err = r.s.Open(ctx, OpenRequest{ID: rv.ID, Author: "agent:lead"})
	if err != nil || rv.Round != 2 {
		t.Fatalf("%v round %d", err, rv.Round)
	}
	if n := len(r.handedTo("git-review:" + rv.ID)); n != 2 {
		t.Fatalf("reviewer asked %d time(s), want 2", n)
	}
	// asking again with nothing new: not again
	r.s.Open(ctx, OpenRequest{ID: rv.ID, Author: "agent:lead"})
	if n := len(r.handedTo("git-review:" + rv.ID)); n != 2 {
		t.Fatalf("reviewer asked %d time(s) for nothing new", n)
	}
	// the review as a model reads it: verdicts and the comment under its line
	text := r.s.renderReview(rv) + RenderDiffWith(must(r.s.ReviewDiff(ctx, rv, nil)), 10000, rv.Comments, r.s.name)
	if !strings.Contains(text, "↳ comment") || !strings.Contains(text, "name it better") || !strings.Contains(text, "approve, round 1 (at an older commit)") {
		t.Fatal(text)
	}
	// the holder works in its worktree like in its folder
	lead, _ := r.agents.Get("lead")
	if roots := lead.ExtraRoots(); len(roots) != 1 || roots[0] != w.Path {
		t.Fatal(roots)
	}
}

func TestRoundsBound(t *testing.T) {
	r := setup(t)
	ctx := context.Background()
	w, _ := r.s.Spawn(ctx, SpawnRequest{Dir: r.repo, Holder: "lead", Parent: "lead", Origin: "tool"})
	g, _ := r.s.vcs()
	commit := func(n string) {
		os.WriteFile(filepath.Join(w.Path, n), []byte(n), 0o644)
		g.Commit(ctx, w.Path, vcs.CommitRequest{Message: n, All: true})
	}
	commit("a")
	rv, _ := r.s.Open(ctx, OpenRequest{Worktree: w.ID, Reviewers: []string{"reviewer"}, Author: "agent:lead"})
	var err error
	for i := range 4 {
		r.s.Submit(ctx, SubmitRequest{ID: rv.ID, Actor: "agent:reviewer", Decision: VApprove})
		commit(fmt.Sprint("n", i))
		if x, e := r.s.Open(ctx, OpenRequest{ID: rv.ID, Author: "agent:lead"}); e != nil {
			err = e
		} else {
			rv = x
		}
		rv, _ = r.s.Review(rv.ID)
	}
	if !isCode(err, CodeRounds) || rv.Round < 4 {
		t.Fatalf("%v round %d", err, rv.Round)
	}
	if n := len(r.handedTo("git-review:" + rv.ID)); n != 3 {
		t.Fatalf("reviewer asked %d times, want 3 (the rounds)", n)
	}
	// a person may still ask
	if _, err := r.s.Open(ctx, OpenRequest{ID: rv.ID, Author: PersonActor("")}); err != nil {
		t.Fatal(err)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
