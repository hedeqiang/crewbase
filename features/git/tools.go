package git

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hedeqiang/crewbase/capabilities/vcs"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

const maxDiffChars = 60000

// registerTools adds the git_* tools (a deferred family, "git").
func (s *Service) registerTools(c *kernel.Context, reg *tools.Registry) {
	str := tools.Prop
	arr := func(desc string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
	}
	wtProp := str("string", "optional worktree id (wt…); default: the folder you work in")
	actor := func(call tools.Call) string { return agentActor(call.Agent) }
	sid := func(call tools.Call) string {
		if call.Session != nil {
			return call.Session.ID()
		}
		return ""
	}
	dir := func(ctx context.Context, call tools.Call, wt string) (string, *Worktree, error) {
		return s.dirFor(ctx, call.Agent, call.Workspace, wt)
	}
	add := func(t *tools.Tool) {
		t.Load, t.Group = tools.LoadDeferred, "git"
		reg.Register(c, t)
	}
	errText := func(name string, err error) tools.Result {
		var ge *Error
		if errors.As(err, &ge) {
			return tools.Errorf("%s: %s: %s", name, ge.Code, ge.Msg)
		}
		if errors.Is(err, vcs.ErrNotRepo) {
			return tools.Errorf("%s: this folder is not in a git repository", name)
		}
		return tools.Errorf("%s: %v", name, err)
	}

	add(&tools.Tool{
		Name:        "git_status",
		Description: "Show the git state of your folder (or a worktree): branch, ahead/behind, changed files, a merge in progress, and the repository's worktrees and open reviews.",
		Params:      tools.Schema(map[string]any{"worktree": wtProp}),
		Category:    tools.CategoryRead,
		Execute: func(ctx context.Context, call tools.Call) (tools.Result, error) {
			var a struct{ Worktree string }
			_ = call.Decode(&a)
			d, w, err := dir(ctx, call, a.Worktree)
			if err != nil {
				return errText("git_status", err), nil
			}
			g, err := s.vcs()
			if err != nil {
				return errText("git_status", err), nil
			}
			info, _ := s.Repo(ctx, d)
			st, err := g.Status(ctx, d)
			if err != nil {
				return errText("git_status", err), nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "Repository %s, branch %s", info.Name, orNone(st.Branch))
			if st.Upstream != "" {
				fmt.Fprintf(&b, " (upstream %s: %d ahead, %d behind)", st.Upstream, st.Ahead, st.Behind)
			}
			if w != nil {
				ws := s.Describe(ctx, *w)
				fmt.Fprintf(&b, "\nWorktree %s from %s: %d commit(s) ahead, %d behind", w.ID, w.Base, ws.Ahead, ws.Behind)
			}
			if st.Merging {
				b.WriteString("\nA merge is in progress: resolve with git_conflict_resolve.")
			}
			if st.Clean {
				b.WriteString("\nNothing changed.")
			} else {
				b.WriteString("\nChanged:")
				for _, f := range st.Files {
					mark := ""
					if f.Staged && f.Unstaged {
						mark = " (partly staged)"
					} else if f.Staged {
						mark = " (staged)"
					}
					fmt.Fprintf(&b, "\n  %s %s%s", f.Status, f.Path, mark)
				}
			}
			if wts := s.Worktrees(info.ID, false); len(wts) > 0 {
				b.WriteString("\nWorktrees:")
				for _, x := range wts {
					fmt.Fprintf(&b, "\n  %s  %s (from %s) — %s, %s", x.ID, x.Branch, x.Base, s.name(agentActor(x.Holder)), trunc(x.Task, 80))
				}
			}
			for _, r := range s.Reviews(info.ID) {
				if r.State != RvMerged && r.State != RvClosed {
					fmt.Fprintf(&b, "\nReview %s: %s → %s, %s — %s", r.ID, r.Source, r.Target, r.State, r.Title)
				}
			}
			return tools.Result{Content: b.String(), Data: st}, nil
		},
	})

	add(&tools.Tool{
		Name: "git_diff",
		Description: "Show a diff with stable hunk ids ([h…]) for git_hunk_apply. side: work (unstaged, default), staged, head (all uncommitted), range (what the branch adds since base). " +
			"With review: the review's state, verdicts and every comment, with the change under review (comments under their lines).",
		Params: tools.Schema(map[string]any{
			"worktree": wtProp,
			"review":   str("string", "a review id (rv…): its change"),
			"side":     str("string", "work | staged | head | range"),
			"base":     str("string", "range: the branch to compare with (default: the worktree's base)"),
			"paths":    arr("only these files"),
		}),
		Category: tools.CategoryRead,
		Execute: func(ctx context.Context, call tools.Call) (tools.Result, error) {
			var a struct {
				Worktree, Review, Side, Base string
				Paths                        []string
			}
			_ = call.Decode(&a)
			var fs []vcs.FileDiff
			if a.Review != "" {
				r, ok := s.Review(a.Review)
				if !ok {
					return tools.Errorf("git_diff: no review %s", a.Review), nil
				}
				var err error
				if fs, err = s.ReviewDiff(ctx, r, a.Paths); err != nil {
					return errText("git_diff", err), nil
				}
				return tools.Result{Content: s.renderReview(r) + "\n\n" + RenderDiffWith(fs, maxDiffChars, r.Comments, s.name)}, nil
			} else {
				d, w, err := dir(ctx, call, a.Worktree)
				if err != nil {
					return errText("git_diff", err), nil
				}
				g, err := s.vcs()
				if err != nil {
					return errText("git_diff", err), nil
				}
				o := vcs.DiffOptions{Side: a.Side, Base: a.Base, Paths: a.Paths, Untracked: true}
				if o.Side == vcs.DiffRange && o.Base == "" {
					if w == nil {
						return tools.Errorf("git_diff: range needs base"), nil
					}
					o.Base = w.Base
				}
				if fs, err = g.Diff(ctx, d, o); err != nil {
					return errText("git_diff", err), nil
				}
			}
			return tools.Result{Content: RenderDiff(fs, maxDiffChars)}, nil
		},
	})

	add(&tools.Tool{
		Name: "git_worktree_spawn",
		Description: "Make an isolated git worktree (a new branch and folder outside the project) of the repository you work in, for work that must not disturb the main folder. " +
			"Hand work into it with delegate(task, worktree: id).",
		Params: tools.Schema(map[string]any{
			"base":   str("string", "branch to start from (default: your folder's branch)"),
			"branch": str("string", "new branch name (default crew/<you>-…)"),
			"task":   str("string", "what it is for (shown to people)"),
		}),
		Category: tools.CategoryWrite,
		Execute: func(ctx context.Context, call tools.Call) (tools.Result, error) {
			var a struct{ Base, Branch, Task string }
			_ = call.Decode(&a)
			w, err := s.Spawn(ctx, SpawnRequest{Dir: call.Workspace, Base: a.Base, Branch: a.Branch, Holder: call.Agent, Parent: call.Agent, Session: sid(call), Task: a.Task, Origin: "tool", Actor: actor(call)})
			if err != nil {
				return errText("git_worktree_spawn", err), nil
			}
			return tools.Result{Content: fmt.Sprintf("Worktree %s: branch %s from %s, in %s. Give work to a sub-agent there with delegate(task, worktree: %q).", w.ID, w.Branch, w.Base, w.Path, w.ID), Data: w}, nil
		},
	})

	add(&tools.Tool{
		Name:        "git_hunk_apply",
		Description: "Stage, unstage or discard one hunk (ids from git_diff: side work for stage/discard, staged for unstage). Discard throws the change away.",
		Params: tools.Schema(map[string]any{
			"worktree": wtProp,
			"path":     str("string", "the file"),
			"hunk":     str("string", "the hunk id (h…)"),
			"action":   str("string", "stage | unstage | discard"),
		}, "path", "hunk", "action"),
		Category: tools.CategoryWrite,
		Classify: func(call tools.Call) (string, string) {
			if call.String("action") == vcs.HunkDiscard {
				return tools.CategoryDelete, "Discard a change in " + call.String("path")
			}
			return "", ""
		},
		Execute: func(ctx context.Context, call tools.Call) (tools.Result, error) {
			var a struct{ Worktree, Path, Hunk, Action string }
			_ = call.Decode(&a)
			d, _, err := dir(ctx, call, a.Worktree)
			if err != nil {
				return errText("git_hunk_apply", err), nil
			}
			if l := s.frozenFor(ctx, d, a.Path, actor(call)); l != nil {
				return tools.Errorf("git_hunk_apply: %s: %s is frozen by %s", CodeLockFrozen, span(*l), s.name(l.Holder)), nil
			}
			g, err := s.vcs()
			if err != nil {
				return errText("git_hunk_apply", err), nil
			}
			if err := g.ApplyHunk(ctx, d, a.Path, a.Hunk, a.Action); err != nil {
				return errText("git_hunk_apply", err), nil
			}
			s.changed(vcs.Event{Type: "git/hunk." + a.Action, ID: a.Hunk, Actor: actor(call), Session: sid(call), Data: map[string]any{"path": a.Path, "dir": d}})
			done := map[string]string{vcs.HunkStage: "Staged", vcs.HunkUnstage: "Unstaged", vcs.HunkDiscard: "Discarded"}[a.Action]
			return tools.Text(fmt.Sprintf("%s hunk %s of %s.", done, a.Hunk, a.Path)), nil
		},
	})

	add(&tools.Tool{
		Name:        "git_commit",
		Description: "Commit in your folder or a worktree: every change (all, default) or what is staged, or the given paths. Committing on a protected branch (main, …) asks the person.",
		Params: tools.Schema(map[string]any{
			"worktree": wtProp,
			"message":  str("string", "the commit message: a short summary line, then details"),
			"all":      str("boolean", "commit every change, new files included (default true; false = only what is staged)"),
			"paths":    arr("stage and commit these files"),
		}, "message"),
		Category: tools.CategoryWrite,
		Classify: func(call tools.Call) (string, string) {
			d, _, err := s.dirFor(context.Background(), call.Agent, call.Workspace, call.String("worktree"))
			if err != nil {
				return "", ""
			}
			if info, err := s.Repo(context.Background(), d); err == nil && s.protectedBranch(info.Branch) {
				return tools.CategoryPush, "Commit on protected branch " + info.Branch + ": " + firstLine(call.String("message"))
			}
			return "", "Commit: " + firstLine(call.String("message"))
		},
		Execute: func(ctx context.Context, call tools.Call) (tools.Result, error) {
			var a struct {
				Worktree, Message string
				All               *bool
				Paths             []string
			}
			_ = call.Decode(&a)
			d, w, err := dir(ctx, call, a.Worktree)
			if err != nil {
				return errText("git_commit", err), nil
			}
			g, err := s.vcs()
			if err != nil {
				return errText("git_commit", err), nil
			}
			all := a.All == nil || *a.All
			c, err := g.Commit(ctx, d, vcs.CommitRequest{Message: a.Message, All: all && len(a.Paths) == 0, Paths: a.Paths, Author: s.signature(actor(call))})
			if err != nil {
				return errText("git_commit", err), nil
			}
			info, _ := s.Repo(ctx, d)
			ev := vcs.Event{Type: "git/commit", Repo: info.ID, ID: c.Hash, Actor: actor(call), Session: sid(call), Data: map[string]any{"commit": c, "branch": info.Branch}}
			if w != nil {
				ev.Data.(map[string]any)["worktree"] = w.ID
			}
			s.changed(ev)
			return tools.Result{Content: fmt.Sprintf("Committed %s on %s: %s", c.Short, orNone(info.Branch), c.Subject), Data: c}, nil
		},
	})

	add(&tools.Tool{
		Name: "git_review_open",
		Description: "Open a code review of a branch (a worktree's, or your folder's) against its target, naming reviewers (agent ids, or \"person\"). " +
			"With review: ask for another round after fixing. Nobody reviews their own work: reviewers must not work for the same agent as the author.",
		Params: tools.Schema(map[string]any{
			"worktree":  wtProp,
			"review":    str("string", "an open review to send back to its reviewers (after new commits)"),
			"source":    str("string", "the branch to review (default: the worktree's / your folder's)"),
			"target":    str("string", "the branch it goes into (default: where it started, or main)"),
			"title":     str("string", "a short title"),
			"body":      str("string", "what changed and why, what to look at"),
			"reviewers": arr("agent ids, or \"person\" (default: the person)"),
			"draft":     str("boolean", "open as a draft (reviewers are not asked yet)"),
		}),
		Category: tools.CategoryWrite,
		Execute: func(ctx context.Context, call tools.Call) (tools.Result, error) {
			var a struct {
				Worktree, Review, Source, Target, Title, Body string
				Reviewers                                     []string
				Draft                                         bool
			}
			_ = call.Decode(&a)
			r, err := s.Open(ctx, OpenRequest{ID: a.Review, Dir: call.Workspace, Worktree: a.Worktree, Source: a.Source, Target: a.Target, Title: a.Title, Body: a.Body,
				Reviewers: a.Reviewers, Author: actor(call), Session: sid(call), Draft: a.Draft})
			if err != nil {
				return errText("git_review_open", err), nil
			}
			who := make([]string, len(r.Reviewers))
			for i, x := range r.Reviewers {
				who[i] = s.name(x)
			}
			return tools.Result{Content: fmt.Sprintf("Review %s (%s → %s) is %s, round %d of %d, reviewers: %s. Their verdict comes back to you as a message.",
				r.ID, r.Source, r.Target, r.State, r.Round, r.MaxRounds, strings.Join(who, ", ")), Data: r}, nil
		},
	})

	add(&tools.Tool{
		Name: "git_review_submit",
		Description: "Review: leave line comments and a verdict on a review (decision approve, request_changes or comment). " +
			"Comments: [{path, line, severity: blocker|major|minor|nit, body}] — line numbers of the new file (side old for removed lines). You cannot judge work by the agent you work for.",
		Params: tools.Schema(map[string]any{
			"review":   str("string", "the review id (rv…)"),
			"decision": str("string", "approve | request_changes | comment"),
			"body":     str("string", "the overall verdict"),
			"comments": map[string]any{"type": "array", "description": "line comments", "items": map[string]any{"type": "object", "properties": map[string]any{
				"path": str("string", "file"), "line": str("integer", "line number"), "side": str("string", "new (default) | old"),
				"severity": str("string", "blocker | major | minor | nit"), "body": str("string", "the comment"),
			}, "required": []string{"body"}}},
		}, "review", "decision"),
		Category: tools.CategoryNone,
		Execute: func(ctx context.Context, call tools.Call) (tools.Result, error) {
			var a struct {
				Review, Decision, Body string
				Comments               []CommentInput
			}
			if err := call.Decode(&a); err != nil {
				return tools.Errorf("git_review_submit: %v", err), nil
			}
			r, err := s.Submit(ctx, SubmitRequest{ID: a.Review, Actor: actor(call), Decision: a.Decision, Body: a.Body, Comments: a.Comments, Session: sid(call)})
			if err != nil {
				return errText("git_review_submit", err), nil
			}
			return tools.Result{Content: fmt.Sprintf("Recorded. Review %s is now %s (round %d of %d).", r.ID, r.State, r.Round, r.MaxRounds), Data: r}, nil
		},
	})

	add(&tools.Tool{
		Name: "git_merge",
		Description: "Merge an approved review into its target (strategy merge-no-ff default, squash or ff-only), or preview: true to see whether it merges cleanly. " +
			"Only an approved review at its latest commit merges; merging into a protected branch asks the person.",
		Params: tools.Schema(map[string]any{
			"review":   str("string", "the review id"),
			"strategy": str("string", "merge-no-ff | squash | ff-only"),
			"message":  str("string", "the merge commit message (optional)"),
			"preview":  str("boolean", "only show what the merge would do"),
		}, "review"),
		Category: tools.CategoryWrite,
		Classify: func(call tools.Call) (string, string) {
			var a struct {
				Review  string
				Preview bool
			}
			_ = call.Decode(&a)
			if a.Preview {
				return tools.CategoryRead, "Preview merging " + a.Review
			}
			if r, ok := s.Review(a.Review); ok {
				sum := fmt.Sprintf("Merge %s into %s: %s", r.Source, r.Target, r.Title)
				if s.protectedBranch(r.Target) {
					return tools.CategoryPush, sum
				}
				return "", sum
			}
			return "", ""
		},
		Execute: func(ctx context.Context, call tools.Call) (tools.Result, error) {
			var a struct {
				Review, Strategy, Message string
				Preview                   bool
			}
			_ = call.Decode(&a)
			r, ok := s.Review(a.Review)
			if !ok {
				return tools.Errorf("git_merge: no review %s", a.Review), nil
			}
			if a.Preview {
				g, err := s.vcs()
				if err != nil {
					return errText("git_merge", err), nil
				}
				p, err := g.MergePreview(ctx, r.RepoRoot, r.Target, r.Source)
				if err != nil {
					return errText("git_merge", err), nil
				}
				return tools.Result{Content: renderPreview(r, p), Data: p}, nil
			}
			r, c, err := s.MergeReview(ctx, MergeReviewRequest{ID: a.Review, Actor: actor(call), Strategy: a.Strategy, Message: a.Message, Session: sid(call)})
			if err != nil {
				return errText("git_merge", err), nil
			}
			return tools.Result{Content: fmt.Sprintf("Merged %s into %s as %s. Remove the worktree with git_worktree_remove when it is no longer needed.", r.Source, r.Target, c.Short), Data: c}, nil
		},
	})

	add(&tools.Tool{
		Name: "git_conflict_resolve",
		Description: "Resolve merge conflicts in a worktree: start (bring target into the worktree's branch; conflicts come back with their blocks), status, " +
			"resolve a path (how: ours | theirs | manual after you edited the file), conclude (commit the merge), abort.",
		Params: tools.Schema(map[string]any{
			"worktree": wtProp,
			"action":   str("string", "start | status | resolve | conclude | abort"),
			"target":   str("string", "start: the branch to merge in (default: the worktree's base)"),
			"path":     str("string", "resolve: the file"),
			"how":      str("string", "resolve: ours | theirs | manual"),
			"message":  str("string", "conclude: the merge commit message"),
		}, "action"),
		Category: tools.CategoryWrite,
		Execute: func(ctx context.Context, call tools.Call) (tools.Result, error) {
			var a struct{ Worktree, Action, Target, Path, How, Message string }
			_ = call.Decode(&a)
			d, w, err := dir(ctx, call, a.Worktree)
			if err != nil {
				return errText("git_conflict_resolve", err), nil
			}
			g, err := s.vcs()
			if err != nil {
				return errText("git_conflict_resolve", err), nil
			}
			sig := s.signature(actor(call))
			switch a.Action {
			case "start":
				target := a.Target
				if target == "" && w != nil {
					target = w.Base
				}
				if target == "" {
					return tools.Errorf("git_conflict_resolve: start needs target"), nil
				}
				cs, err := g.StartMerge(ctx, d, target, sig)
				if err != nil {
					return errText("git_conflict_resolve", err), nil
				}
				if len(cs) == 0 {
					return tools.Text(fmt.Sprintf("%s merged in without conflicts; finish with action: conclude.", target)), nil
				}
				return tools.Result{Content: renderConflicts(cs), Data: cs}, nil
			case "status":
				cs, err := g.Conflicts(ctx, d)
				if err != nil {
					return errText("git_conflict_resolve", err), nil
				}
				if len(cs) == 0 {
					return tools.Text("No unresolved conflicts; conclude to commit the merge."), nil
				}
				return tools.Result{Content: renderConflicts(cs), Data: cs}, nil
			case "resolve":
				if err := g.ResolveConflict(ctx, d, a.Path, a.How); err != nil {
					return errText("git_conflict_resolve", err), nil
				}
				return tools.Text("Resolved " + a.Path + "."), nil
			case "conclude":
				c, err := g.ConcludeMerge(ctx, d, a.Message, sig)
				if err != nil {
					return errText("git_conflict_resolve", err), nil
				}
				s.changed(vcs.Event{Type: "git/commit", ID: c.Hash, Actor: actor(call), Session: sid(call), Data: map[string]any{"commit": c, "merge": true}})
				return tools.Text("Merge committed as " + c.Short + ". If a review is open, ask for another round with git_review_open(review: …)."), nil
			case "abort":
				if err := g.AbortMerge(ctx, d); err != nil {
					return errText("git_conflict_resolve", err), nil
				}
				return tools.Text("Merge aborted."), nil
			}
			return tools.Errorf("git_conflict_resolve: action is start, status, resolve, conclude or abort"), nil
		},
	})

	add(&tools.Tool{
		Name:        "git_push",
		Description: "Push a branch to its remote (asks the person every time). The credential set for the remote's host in Settings › Git is used for this one push.",
		Params: tools.Schema(map[string]any{
			"worktree": wtProp,
			"branch":   str("string", "the branch (default: the folder's)"),
			"remote":   str("string", "the remote (default origin)"),
			"force":    str("boolean", "force-with-lease (overwrite the remote branch if it is where you last saw it)"),
		}),
		Category: tools.CategoryPush,
		Classify: func(call tools.Call) (string, string) {
			b := call.String("branch")
			if b == "" {
				if d, _, err := s.dirFor(context.Background(), call.Agent, call.Workspace, call.String("worktree")); err == nil {
					if info, err := s.Repo(context.Background(), d); err == nil {
						b = info.Branch
					}
				}
			}
			r := call.String("remote")
			if r == "" {
				r = "origin"
			}
			return tools.CategoryPush, fmt.Sprintf("git push %s %s", r, b)
		},
		Execute: func(ctx context.Context, call tools.Call) (tools.Result, error) {
			var a struct {
				Worktree, Branch, Remote string
				Force                    bool
			}
			_ = call.Decode(&a)
			d, _, err := dir(ctx, call, a.Worktree)
			if err != nil {
				return errText("git_push", err), nil
			}
			out, err := s.Push(ctx, PushRequest{Dir: d, Branch: a.Branch, Remote: a.Remote, Force: a.Force, Actor: actor(call), Session: sid(call)})
			if err != nil {
				return errText("git_push", err), nil
			}
			return tools.Text("Pushed.\n" + trunc(out, 2000)), nil
		},
	})

	add(&tools.Tool{
		Name: "git_worktree_remove",
		Description: "Remove a worktree. One with uncommitted changes or unmerged commits is refused unless discard: true (its changes are archived as a patch and a bundle first). " +
			"Its branch goes too unless keep_branch.",
		Params: tools.Schema(map[string]any{
			"worktree":    str("string", "the worktree id"),
			"discard":     str("boolean", "remove it even with work in it (archived first)"),
			"keep_branch": str("boolean", "keep its branch"),
		}, "worktree"),
		Category: tools.CategoryWrite,
		Classify: func(call tools.Call) (string, string) {
			var a struct{ Discard bool }
			_ = call.Decode(&a)
			if a.Discard {
				return tools.CategoryDelete, "Remove worktree " + call.String("worktree") + " and discard its work (archived)"
			}
			return "", ""
		},
		Execute: func(ctx context.Context, call tools.Call) (tools.Result, error) {
			var a struct {
				Worktree   string
				Discard    bool
				KeepBranch bool `json:"keep_branch"`
			}
			_ = call.Decode(&a)
			if _, _, err := dir(ctx, call, a.Worktree); err != nil {
				return errText("git_worktree_remove", err), nil
			}
			w, err := s.Remove(ctx, RemoveRequest{ID: a.Worktree, Discard: a.Discard, KeepBranch: a.KeepBranch, Actor: actor(call), Session: sid(call)})
			if err != nil {
				return errText("git_worktree_remove", err), nil
			}
			msg := "Removed worktree " + w.ID + "."
			if w.Archive != nil {
				msg += " Its work was archived:"
				if w.Archive.Patch != "" {
					msg += " " + w.Archive.Patch
				}
				if w.Archive.Bundle != "" {
					msg += " " + w.Archive.Bundle
				}
			}
			return tools.Text(msg), nil
		},
	})

	add(&tools.Tool{
		Name: "git_lock",
		Description: "Freeze a file, or lines of it, so other agents cannot change them (their writes are refused with GIT_LOCK_FROZEN, and so is a merge that would change them). " +
			"Use it for code that was agreed on. release: a lock id to lift.",
		Params: tools.Schema(map[string]any{
			"worktree": wtProp,
			"path":     str("string", "the file (relative to the repository)"),
			"start":    str("integer", "first line (omit both for the whole file)"),
			"end":      str("integer", "last line"),
			"reason":   str("string", "why (shown to whoever is stopped)"),
			"release":  str("string", "lift this lock (lk…) instead"),
		}),
		Category: tools.CategoryNone,
		Execute: func(ctx context.Context, call tools.Call) (tools.Result, error) {
			var a struct {
				Worktree, Path, Reason, Release string
				Start, End                      int
			}
			_ = call.Decode(&a)
			if a.Release != "" {
				if err := s.RemoveLock(a.Release, actor(call)); err != nil {
					return errText("git_lock", err), nil
				}
				return tools.Text("Lifted " + a.Release + "."), nil
			}
			d, _, err := dir(ctx, call, a.Worktree)
			if err != nil {
				return errText("git_lock", err), nil
			}
			info, err := s.Repo(ctx, d)
			if err != nil {
				return errText("git_lock", err), nil
			}
			l, err := s.AddLock(LockRequest{Repo: info.ID, Path: a.Path, Start: a.Start, End: a.End, Holder: actor(call), Reason: a.Reason})
			if err != nil {
				return errText("git_lock", err), nil
			}
			return tools.Result{Content: fmt.Sprintf("Frozen %s (lock %s).", span(l), l.ID), Data: l}, nil
		},
	})
}

// frozenFor is the lock that stops actor changing a file in dir.
func (s *Service) frozenFor(ctx context.Context, dir, rel, actor string) *Lock {
	if !s.anyLocks() {
		return nil
	}
	info, err := s.Repo(ctx, dir)
	if err != nil {
		return nil
	}
	p, err := cleanRel(rel)
	if err != nil {
		return nil
	}
	return s.frozen(info.ID, p, actor, nil, false)
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return trunc(s, 120)
}

// renderReview is a review's state, verdicts and comments not on a line.
func (s *Service) renderReview(r Review) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Review %s: “%s” — %s → %s, %s, round %d of %d, by %s.", r.ID, r.Title, r.Source, r.Target, r.State, r.Round, r.MaxRounds, s.name(r.Author))
	for _, v := range r.Verdicts {
		stale := ""
		if v.Decision == VApprove && v.Commit != r.Head {
			stale = " (at an older commit)"
		}
		fmt.Fprintf(&b, "\n- %s: %s, round %d%s", s.name(v.Reviewer), v.Decision, v.Round, stale)
		if v.Body != "" {
			fmt.Fprintf(&b, ": %s", trunc(v.Body, 3000))
		}
	}
	for _, c := range r.Comments {
		if c.Path == "" {
			fmt.Fprintf(&b, "\n- comment %s [%s] by %s%s: %s", c.ID, c.Severity, s.name(c.Author), resolvedMark(c), c.Body)
		}
	}
	return b.String()
}

func resolvedMark(c Comment) string {
	if c.Resolved {
		return " (resolved)"
	}
	return ""
}

// RenderDiffWith is RenderDiff with line comments under their lines.
func RenderDiffWith(fs []vcs.FileDiff, limit int, cs []Comment, name func(string) string) string {
	if len(cs) == 0 {
		return RenderDiff(fs, limit)
	}
	at := map[string][]Comment{}
	for _, c := range cs {
		if c.Path != "" {
			side := c.Side
			if side == "" {
				side = "new"
			}
			k := fmt.Sprintf("%s:%s:%d", c.Path, side, c.Line)
			at[k] = append(at[k], c)
		}
	}
	var b strings.Builder
	shown := map[string]bool{}
	for _, f := range fs {
		fmt.Fprintf(&b, "=== %s (%s, +%d −%d)\n", f.Path, f.Status, f.Added, f.Removed)
		for _, h := range f.Hunks {
			fmt.Fprintf(&b, "%s [%s]\n", h.Header, h.ID)
			for _, l := range h.Lines {
				b.WriteString(l.Kind + l.Text + "\n")
				side, n := "new", l.New
				if l.Kind == "-" {
					side, n = "old", l.Old
				}
				k := fmt.Sprintf("%s:%s:%d", f.Path, side, n)
				for _, c := range at[k] {
					shown[c.ID] = true
					fmt.Fprintf(&b, "    ↳ comment %s [%s] %s%s: %s\n", c.ID, c.Severity, name(c.Author), resolvedMark(c), strings.ReplaceAll(trunc(c.Body, 2000), "\n", "\n      "))
				}
			}
		}
		if b.Len() > limit {
			b.WriteString("\n… (cut; ask for fewer paths)")
			break
		}
	}
	// comments on lines the diff no longer shows
	for _, c := range cs {
		if c.Path != "" && !shown[c.ID] {
			fmt.Fprintf(&b, "\n(outdated) %s:%d comment %s [%s] %s%s: %s", c.Path, c.Line, c.ID, c.Severity, name(c.Author), resolvedMark(c), trunc(c.Body, 1000))
		}
	}
	return b.String()
}

// RenderDiff is a diff for a model: files, hunks with their ids, lines.
func RenderDiff(fs []vcs.FileDiff, limit int) string {
	if len(fs) == 0 {
		return "No changes."
	}
	var b strings.Builder
	for i, f := range fs {
		if b.Len() > limit {
			fmt.Fprintf(&b, "\n… %d more file(s) not shown; ask for them with paths.", len(fs)-i)
			break
		}
		fmt.Fprintf(&b, "=== %s (%s, +%d −%d)\n", f.Path, f.Status, f.Added, f.Removed)
		if f.Binary {
			b.WriteString("(binary)\n")
			continue
		}
		for _, h := range f.Hunks {
			fmt.Fprintf(&b, "%s [%s]\n", h.Header, h.ID)
			for _, l := range h.Lines {
				b.WriteString(l.Kind + l.Text + "\n")
			}
		}
	}
	out := b.String()
	if len(out) > limit+2000 {
		out = out[:limit] + "\n… (cut; ask for fewer paths)"
	}
	return out
}

func renderPreview(r Review, p vcs.MergePreview) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s → %s: %d commit(s) to merge, %s is %d ahead.", r.Source, r.Target, p.Ahead, r.Target, p.Behind)
	switch {
	case p.UpToDate:
		b.WriteString(" Nothing to merge.")
	case p.Clean && p.FastForward:
		b.WriteString(" Merges cleanly (fast-forward possible).")
	case p.Clean:
		b.WriteString(" Merges cleanly.")
	default:
		b.WriteString(" Conflicts in: " + strings.Join(p.Conflicts, ", ") + ". Resolve in the worktree with git_conflict_resolve(action: start).")
	}
	fmt.Fprintf(&b, " Review state: %s.", r.State)
	return b.String()
}

func renderConflicts(cs []vcs.Conflict) string {
	var b strings.Builder
	b.WriteString("Conflicts to resolve (edit each file to the right content, then resolve with how: manual; or pick ours / theirs):\n")
	for _, c := range cs {
		fmt.Fprintf(&b, "\n=== %s\n", c.Path)
		for _, k := range c.Blocks {
			fmt.Fprintf(&b, "lines %d-%d\n<<< ours (this branch)\n%s\n=== theirs\n%s\n>>>\n", k.Start, k.End, trunc(k.Ours, 2000), trunc(k.Theirs, 2000))
		}
	}
	return b.String()
}
