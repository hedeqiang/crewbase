// Package git is @crewbase/git: agents working side by side on one
// repository without stepping on each other, and reviewing each other's
// work before it lands.
//
//   - A worktree pool (<root>/worktrees/<repo>/<id>, outside the project):
//     a sub-agent that may write gets a branch and a working tree of its
//     own (vcs.AllocateWorkspace), so parallel sub-agents never share a
//     folder or an index. A worktree with work in it is kept when its
//     task ends; one removed with uncommitted work is archived first (a
//     patch and a bundle).
//   - Freeze locks: a file or a range of its lines is frozen for everyone
//     but its holder (fs.WriteIntent refuses the write, GIT_LOCK_FROZEN;
//     a merge that would change it is refused too).
//   - Local code review: a branch is opened for review against its target,
//     reviewers leave line comments and a verdict, the author fixes in
//     rounds (bounded), and only an approved head merges. Nobody approves
//     their own work, nor the work of the agent they work for
//     (GIT_SELF_APPROVAL_DENIED).
//   - Merges are previewed and recorded without touching any working tree
//     (merge-tree); pushes ask the person and get their credential for
//     that one command.
//
// Every change is recorded in the audit log and pushed to the UI.
package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/vcs"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/kernel"
)

// ServiceKey is the key of the git service (*Service).
const ServiceKey = "git"

// Stable error codes (API errors and tool results carry them).
const (
	CodeLockFrozen   = "GIT_LOCK_FROZEN"
	CodeSelfApproval = "GIT_SELF_APPROVAL_DENIED"
	CodeDirty        = "GIT_WORKTREE_DIRTY"
	CodeNotApproved  = "GIT_REVIEW_NOT_APPROVED"
	CodeConflict     = "GIT_MERGE_CONFLICT"
	CodeStale        = "GIT_REVIEW_STALE"
)

// Error is a refusal with a stable code.
type Error struct {
	Code   string
	Status int
	Msg    string
}

func (e *Error) Error() string { return e.Code + ": " + e.Msg }

func fail(code string, status int, f string, a ...any) error {
	return &Error{Code: code, Status: status, Msg: fmt.Sprintf(f, a...)}
}

// ErrNotFound: no such worktree, review or lock (or not one the caller may see).
var ErrNotFound = errors.New("not found")

// Service is the git service.
type Service struct {
	c      *kernel.Context
	root   string // <root>: worktrees live in <root>/worktrees
	st     *store
	agents *agent.Registry
	vcsFn  func() vcs.Provider
	// audit records an entry (nil = no audit log)
	audit func(typ, actor, agent, session string, data any)
	probe sync.Map // dir → probed
	// enqueue hands an agent a message (reviews, fix rounds); tests swap it.
	enqueue func(a *agent.Agent, in agent.Input) error
}

type probed struct {
	info vcs.RepoInfo
	err  error
	at   time.Time
}

func (s *Service) vcs() (vcs.Provider, error) {
	if v := s.vcsFn(); v != nil {
		return v, nil
	}
	return nil, vcs.ErrUnavailable
}

// Repo is the repository dir lies in (probes are kept for a few seconds).
func (s *Service) Repo(ctx context.Context, dir string) (vcs.RepoInfo, error) {
	if dir == "" {
		return vcs.RepoInfo{}, vcs.ErrNotRepo
	}
	if v, ok := s.probe.Load(dir); ok && time.Since(v.(probed).at) < 5*time.Second {
		p := v.(probed)
		return p.info, p.err
	}
	g, err := s.vcs()
	if err != nil {
		return vcs.RepoInfo{}, err
	}
	if _, err := os.Stat(dir); err != nil {
		return vcs.RepoInfo{}, vcs.ErrNotRepo
	}
	info, err := g.Probe(ctx, dir)
	s.probe.Store(dir, probed{info: info, err: err, at: time.Now()})
	return info, err
}

func (s *Service) forget(dir string) { s.probe.Delete(dir) }

// ---------------------------------------------------------------- actors

// Actors are agent:<id> or person:<id> ("person:owner" for the host's owner).
func agentActor(id string) string { return "agent:" + id }

// PersonActor is the actor of a person ("" = the host's owner).
func PersonActor(id string) string {
	if id == "" {
		id = "owner"
	}
	return "person:" + id
}

func actorAgent(actor string) (string, bool) { return strings.CutPrefix(actor, "agent:") }

// origin is who an actor works for: a sub-agent works for the agent that
// delegated to it (all the way up), everyone else for themselves.
func (s *Service) origin(actor string) string {
	id, ok := actorAgent(actor)
	if !ok {
		return actor
	}
	for range 16 {
		if a, ok := s.agents.Get(id); ok {
			sp := a.Spec()
			if !sp.Ephemeral || sp.Parent == "" {
				break
			}
			id = sp.Parent
			continue
		}
		// a sub-agent that is gone: its worktree remembers its parent
		if p := s.parentOfChild(id); p != "" && p != id {
			id = p
			continue
		}
		break
	}
	return agentActor(id)
}

func (s *Service) parentOfChild(id string) string {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	for _, w := range s.st.st.Worktrees {
		if w.Child == id || slices.Contains(w.Children, id) {
			return w.Parent
		}
	}
	return ""
}

// sameOrigin reports whether two actors work for the same agent or person.
func (s *Service) sameOrigin(a, b string) bool { return a == b || s.origin(a) == s.origin(b) }

// name is how an actor reads in messages.
func (s *Service) name(actor string) string {
	if id, ok := actorAgent(actor); ok {
		if a, ok := s.agents.Get(id); ok {
			return a.Spec().Name
		}
		return id
	}
	if id := strings.TrimPrefix(actor, "person:"); id != "owner" {
		return id
	}
	return "you"
}

func (s *Service) signature(actor string) vcs.Signature {
	if id, ok := actorAgent(actor); ok {
		return vcs.Signature{Name: s.name(actor) + " (Crewbase)", Email: id + "@agents.crewbase.local"}
	}
	return vcs.Signature{} // a person: their own git identity
}

// ---------------------------------------------------------------- events

func (s *Service) changed(ev vcs.Event) {
	if s.audit != nil {
		ag, _ := actorAgent(ev.Actor)
		if ev.Agent == "" {
			ev.Agent = ag
		}
		s.audit(ev.Type, ev.Actor, ev.Agent, ev.Session, map[string]any{"repo": ev.Repo, "id": ev.ID, "data": ev.Data})
	}
	if s.c != nil {
		vcs.Changed.Emit(s.c, ev)
	}
}

// ---------------------------------------------------------------- worktrees

// SpawnRequest makes a worktree.
type SpawnRequest struct {
	Dir     string // a folder of the repository
	Base    string // the branch to start from ("" = the folder's branch)
	Branch  string // the new branch ("" = crew/<holder>-<id>)
	Holder  string // the agent that will work in it
	Child   string // a sub-agent that will work in it (its holder for now)
	Parent  string // the agent it is made for
	Session string
	Task    string
	Origin  string // subagent | tool | person
	Actor   string
}

func (s *Service) poolDir(repo string) string { return filepath.Join(s.root, "worktrees", repo) }

// Spawn makes a worktree on a new branch.
func (s *Service) Spawn(ctx context.Context, r SpawnRequest) (Worktree, error) {
	g, err := s.vcs()
	if err != nil {
		return Worktree{}, err
	}
	info, err := s.Repo(ctx, r.Dir)
	if err != nil {
		return Worktree{}, err
	}
	main := info.Main
	if main == "" {
		main = info.Root
	}
	base := strings.TrimSpace(r.Base)
	if base == "" {
		base = info.Branch
		if base == "" {
			base = "HEAD"
		}
	}
	head, err := g.Resolve(ctx, info.Root, base)
	if err != nil {
		return Worktree{}, fmt.Errorf("base %s: %w", base, err)
	}
	id := newID("wt")
	branch := strings.TrimSpace(r.Branch)
	if branch == "" {
		who := r.Child
		if who == "" {
			who = r.Holder
		}
		if who == "" {
			who = "person"
		}
		branch = "crew/" + slug(who) + "-" + id[2:8]
	}
	dir := filepath.Join(s.poolDir(info.ID), id)
	if err := g.AddWorktree(ctx, main, dir, branch, head); err != nil {
		return Worktree{}, err
	}
	if rp, err := filepath.EvalSymlinks(dir); err == nil {
		dir = rp
	}
	now := time.Now().UTC()
	var kids []string
	if r.Child != "" {
		kids = []string{r.Child}
	}
	w := &Worktree{ID: id, Repo: info.ID, RepoRoot: main, Path: dir, Branch: branch, Base: base, BaseHead: head, Holder: r.Holder, Child: r.Child, Children: kids,
		Parent: r.Parent, Session: r.Session, Task: trunc(r.Task, 300), Origin: r.Origin, State: WTActive, Created: now, Updated: now}
	s.st.mu.Lock()
	s.st.st.Worktrees = append(s.st.st.Worktrees, w)
	err = s.st.save()
	out := *w
	s.st.mu.Unlock()
	if err != nil {
		return Worktree{}, err
	}
	s.changed(vcs.Event{Type: "git/worktree.created", Repo: info.ID, ID: id, Actor: r.Actor, Session: r.Session, Data: out})
	return out, nil
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "agent"
	}
	return trunc(out, 32)
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// Worktree returns a worktree by id.
func (s *Service) Worktree(id string) (Worktree, bool) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	for _, w := range s.st.st.Worktrees {
		if w.ID == id {
			return *w, true
		}
	}
	return Worktree{}, false
}

// worktreeAt is the active worktree path p lies in.
func (s *Service) worktreeAt(p string) (Worktree, bool) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	for _, w := range s.st.st.Worktrees {
		if w.State == WTActive && (p == w.Path || strings.HasPrefix(p, w.Path+string(filepath.Separator))) {
			return *w, true
		}
	}
	return Worktree{}, false
}

// Worktrees are the pool's worktrees of a repository ("" = all), newest first.
func (s *Service) Worktrees(repo string, all bool) []Worktree {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	var out []Worktree
	for i := len(s.st.st.Worktrees) - 1; i >= 0; i-- {
		w := s.st.st.Worktrees[i]
		if (repo == "" || w.Repo == repo) && (all || w.State == WTActive) {
			out = append(out, *w)
		}
	}
	return out
}

func (s *Service) updateWorktree(id string, fn func(*Worktree)) (Worktree, error) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	for _, w := range s.st.st.Worktrees {
		if w.ID == id {
			fn(w)
			w.Updated = time.Now().UTC()
			return *w, s.st.save()
		}
	}
	return Worktree{}, ErrNotFound
}

// WorktreeState is a worktree with how it stands now.
type WorktreeState struct {
	Worktree
	Status  *vcs.Status  `json:"status,omitempty"`
	Ahead   int          `json:"ahead"`  // commits since the base
	Behind  int          `json:"behind"` // commits the base got since
	Commits []vcs.Commit `json:"commits,omitempty"`
	Review  string       `json:"review,omitempty"`  // its open review
	Missing bool         `json:"missing,omitempty"` // the folder is gone
}

// Describe is w with its status, commits and review.
func (s *Service) Describe(ctx context.Context, w Worktree) WorktreeState {
	out := WorktreeState{Worktree: w}
	if r, ok := s.reviewOfBranch(w.Repo, w.Branch); ok {
		out.Review = r.ID
	}
	if w.State != WTActive {
		return out
	}
	g, err := s.vcs()
	if err != nil {
		return out
	}
	if _, err := os.Stat(w.Path); err != nil {
		out.Missing = true
		return out
	}
	if st, err := g.Status(ctx, w.Path); err == nil {
		out.Status = &st
	}
	if p, err := g.MergePreview(ctx, w.RepoRoot, w.Base, w.Branch); err == nil {
		out.Ahead, out.Behind = p.Ahead, p.Behind
	}
	if out.Ahead > 0 {
		out.Commits, _ = g.Log(ctx, w.Path, w.Base+".."+w.Branch, 20)
	}
	return out
}

// hasWork reports whether a worktree holds anything: uncommitted changes
// or commits its base lacks.
func (s *Service) hasWork(ctx context.Context, w Worktree) (dirty bool, ahead int, err error) {
	g, err := s.vcs()
	if err != nil {
		return false, 0, err
	}
	st, err := g.Status(ctx, w.Path)
	if err != nil {
		return false, 0, err
	}
	p, err := g.MergePreview(ctx, w.RepoRoot, w.Base, w.Branch)
	if err != nil {
		// the base is gone: count the commits since it forked
		p.Ahead = 1
	}
	return !st.Clean, p.Ahead, nil
}

// RemoveRequest removes a worktree.
type RemoveRequest struct {
	ID string
	// Discard: remove it even with work in it (archived first).
	Discard bool
	// KeepBranch keeps its branch (by default a branch with nothing in it,
	// or one that was merged, goes too).
	KeepBranch bool
	Actor      string
	Session    string
}

// Remove takes a worktree out of the pool. Work in it is never lost: with
// uncommitted changes or unmerged commits it is refused (GIT_WORKTREE_DIRTY)
// unless Discard, and then archived first.
func (s *Service) Remove(ctx context.Context, r RemoveRequest) (Worktree, error) {
	g, err := s.vcs()
	if err != nil {
		return Worktree{}, err
	}
	w, ok := s.Worktree(r.ID)
	if !ok || w.State != WTActive {
		return Worktree{}, ErrNotFound
	}
	var arch *Archive
	dirty, ahead, herr := s.hasWork(ctx, w)
	merged := false
	if rv, ok := s.reviewOfBranch(w.Repo, w.Branch); ok && rv.State == RvMerged {
		merged = true
	}
	if _, serr := os.Stat(w.Path); serr == nil && herr == nil && (dirty || (ahead > 0 && !merged)) {
		if !r.Discard {
			what := []string{}
			if dirty {
				what = append(what, "uncommitted changes")
			}
			if ahead > 0 && !merged {
				what = append(what, fmt.Sprintf("%d unmerged commit(s)", ahead))
			}
			return Worktree{}, fail(CodeDirty, 409, "worktree %s (%s) has %s; commit and merge them, or remove it with discard (they are archived first)", w.ID, w.Branch, strings.Join(what, " and "))
		}
		patch, bundle, err := g.Archive(ctx, w.Path, w.BaseHead, filepath.Join(s.root, "worktrees", ".archive", w.ID))
		if err != nil {
			return Worktree{}, fmt.Errorf("archive before removing: %w", err)
		}
		arch = &Archive{Patch: patch, Bundle: bundle, At: time.Now().UTC()}
	}
	branch := ""
	if !r.KeepBranch && (merged || ahead == 0) {
		branch = w.Branch
	}
	if arch != nil && !r.KeepBranch {
		branch = w.Branch // its commits are in the bundle
	}
	if rv, ok := s.reviewOfBranch(w.Repo, w.Branch); ok && rv.State != RvMerged && rv.State != RvClosed {
		branch = "" // an open review still shows it
	}
	if err := g.RemoveWorktree(ctx, w.RepoRoot, w.Path, true, branch); err != nil {
		return Worktree{}, err
	}
	s.forget(w.Path)
	state := WTRemoved
	if arch != nil {
		state = WTArchived
	} else if merged {
		state = WTMerged
	}
	out, err := s.updateWorktree(w.ID, func(x *Worktree) { x.State, x.Archive = state, arch })
	if err != nil {
		return Worktree{}, err
	}
	s.changed(vcs.Event{Type: "git/worktree.removed", Repo: w.Repo, ID: w.ID, Actor: r.Actor, Session: r.Session, Data: out})
	return out, nil
}

// ---------------------------------------------------------------- access

// dirFor is the folder an agent's call works in: a worktree it may use,
// or its own workspace.
func (s *Service) dirFor(ctx context.Context, agentID, workspace, worktree string) (string, *Worktree, error) {
	if worktree == "" {
		if w, ok := s.worktreeAt(workspace); ok {
			return workspace, &w, nil
		}
		if _, err := s.Repo(ctx, workspace); err != nil {
			return "", nil, err
		}
		return workspace, nil, nil
	}
	w, ok := s.Worktree(worktree)
	if !ok || w.State != WTActive || !s.mayUse(ctx, agentID, workspace, w) {
		return "", nil, fmt.Errorf("no worktree %s", worktree)
	}
	return w.Path, &w, nil
}

// mayUse: an agent uses a worktree it holds or was made for, one of a
// sub-agent working for it, or any worktree of the repository it works in.
func (s *Service) mayUse(ctx context.Context, agentID, workspace string, w Worktree) bool {
	me := agentActor(agentID)
	if w.Holder == agentID || w.Parent == agentID || w.Child == agentID || s.sameOrigin(me, agentActor(w.Holder)) {
		return true
	}
	if info, err := s.Repo(ctx, workspace); err == nil && info.ID == w.Repo {
		return true
	}
	return false
}

// protectedBranch reports whether branch matches a protected pattern.
func (s *Service) protectedBranch(branch string) bool {
	for _, p := range s.st.settings().Protected {
		if ok, _ := path.Match(p, branch); ok || p == branch {
			return true
		}
	}
	return false
}

// teamOf is an agent's team ("" for one that is gone).
func (s *Service) teamOf(id string) string {
	if a, ok := s.agents.Get(id); ok {
		return a.Spec().TeamID()
	}
	return ""
}
