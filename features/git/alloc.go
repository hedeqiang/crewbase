package git

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/hedeqiang/crewbase/capabilities/vcs"
	"github.com/hedeqiang/crewbase/core/agent"
)

// allocate is the vcs.AllocateWorkspace listener: where a sub-agent
// works when its parent's folder is a repository.
func (s *Service) allocate(ctx context.Context, req vcs.AllocateRequest) (vcs.Allocation, bool) {
	if req.Worktree != "" {
		return s.continueIn(ctx, req)
	}
	info, err := s.Repo(ctx, req.Workspace)
	if err != nil {
		return vcs.Allocation{}, false // not a repository: the sub-agent's own folder
	}
	shared := vcs.Allocation{Dir: req.Workspace, Note: "[Workspace] You work in the same folder as " + s.name(agentActor(req.Parent)) + " (a git repository, branch " + info.Branch + ")."}
	switch s.st.settings().Isolation {
	case vcs.IsolateShared:
		return shared, true
	case vcs.IsolateWorktree:
	default: // worktree-on-write
		if req.Mode == agent.ModeReadOnly {
			return shared, true // reading only: nothing to keep apart
		}
	}
	w, err := s.Spawn(ctx, SpawnRequest{Dir: req.Workspace, Holder: req.Child, Child: req.Child, Parent: req.Parent, Session: req.ParentSession,
		Task: req.Task, Origin: "subagent", Actor: agentActor(req.Parent)})
	if err != nil {
		if s.c != nil {
			s.c.Log().Warn("git: no worktree for a sub-agent; it works in its own folder", "parent", req.Parent, "err", err)
		}
		return vcs.Allocation{}, false
	}
	return vcs.Allocation{Dir: w.Path, Worktree: w.ID, Branch: w.Branch, Note: workNote(w), Release: s.releaser(w.ID, req.Parent)}, true
}

func workNote(w Worktree) string {
	return fmt.Sprintf("[Workspace] You work in a git worktree of your own: branch %s (from %s), folder %s. Other agents work elsewhere, so change files freely. "+
		"For git use the git tools (git_status, git_diff, git_commit; bash cannot write to the repository's .git). Commit your work with git_commit before you finish; "+
		"do not merge or push, the agent who asked will review it.", w.Branch, w.Base, w.Path)
}

// continueIn gives a sub-agent an existing worktree (a fix round).
func (s *Service) continueIn(ctx context.Context, req vcs.AllocateRequest) (vcs.Allocation, bool) {
	w, ok := s.Worktree(req.Worktree)
	if !ok || w.State != WTActive || !s.mayUse(ctx, req.Parent, req.Workspace, w) {
		return vcs.Allocation{}, false
	}
	prev := w.Holder
	if _, err := s.updateWorktree(w.ID, func(x *Worktree) {
		x.Holder, x.Child = req.Child, req.Child
		if !slices.Contains(x.Children, req.Child) {
			x.Children = append(x.Children, req.Child)
		}
	}); err != nil {
		return vcs.Allocation{}, false
	}
	rel := s.releaser(w.ID, req.Parent)
	return vcs.Allocation{Dir: w.Path, Worktree: w.ID, Branch: w.Branch, Note: workNote(w), Release: func(ctx context.Context) string {
		note := rel(ctx)
		if prev != "" && prev != req.Child {
			if _, ok := s.agents.Get(prev); ok {
				_, _ = s.updateWorktree(w.ID, func(x *Worktree) {
					if x.State == WTActive {
						x.Holder = prev
					}
				})
			}
		}
		return note
	}}, true
}

// releaser: when the sub-agent is done, a worktree with nothing in it is
// removed; one with work goes to the parent, which is told what is there.
func (s *Service) releaser(id, parent string) func(context.Context) string {
	return func(ctx context.Context) string {
		w, ok := s.Worktree(id)
		if !ok || w.State != WTActive {
			return ""
		}
		dirty, ahead, err := s.hasWork(ctx, w)
		if err != nil {
			return ""
		}
		if !dirty && ahead == 0 {
			if _, rv := s.reviewOfBranch(w.Repo, w.Branch); !rv {
				_, _ = s.Remove(ctx, RemoveRequest{ID: id, Actor: agentActor(parent)})
				return ""
			}
		}
		w, _ = s.updateWorktree(id, func(x *Worktree) { x.Holder = parent })
		var what []string
		if ahead > 0 {
			what = append(what, fmt.Sprintf("%d commit(s)", ahead))
		}
		if dirty {
			what = append(what, "uncommitted changes")
		}
		return fmt.Sprintf("[Worktree %s] The sub-agent's work is on branch %s (%s), in %s. Look at it with git_diff(worktree: %q); "+
			"commit what is left with git_commit(worktree: %q); open a review with git_review_open(worktree: %q, reviewers: [...]) — or remove it with git_worktree_remove. "+
			"A fix round continues there with delegate(task, worktree: %q).",
			w.ID, w.Branch, strings.Join(what, ", "), w.Path, w.ID, w.ID, w.ID, w.ID)
	}
}
