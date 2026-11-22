package git

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/hedeqiang/crewbase/capabilities/vcs"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// APIPlugin is the 代码 (Git) panel's API. Every route names the agent
// whose project it is about (?agent= or "agent" in the body): the caller
// must be able to see that agent (to read) or work with it (to change),
// and worktrees, reviews and locks are those of that agent's repository.
//
//	GET    /api/git/repo?agent=                      repository, status, worktrees, reviews, locks
//	GET    /api/git/diff?agent=&worktree=&side=&base=&path=
//	GET    /api/git/log?agent=&worktree=&rev=
//	POST   /api/git/hunk          {agent, worktree, path, hunk, action}
//	POST   /api/git/files         {agent, worktree, paths, action: stage|unstage|discard}
//	POST   /api/git/commit        {agent, worktree, message, all}
//	POST   /api/git/push          {agent, worktree, branch, remote, force}
//	POST   /api/git/worktrees     {agent, base, branch, task}
//	GET    /api/git/worktrees/{id}?agent=
//	DELETE /api/git/worktrees/{id}?agent=&discard=&keepBranch=
//	POST   /api/git/reviews       {agent, worktree, source, target, title, body, reviewers, draft}
//	GET    /api/git/reviews/{id}?agent=              {review, files, preview}
//	POST   /api/git/reviews/{id}/submit   {decision, body, comments}
//	POST   /api/git/reviews/{id}/comments/{cid}/resolve  {resolved}
//	POST   /api/git/reviews/{id}/publish | close | again
//	POST   /api/git/reviews/{id}/merge    {strategy, message}
//	POST   /api/git/locks         {agent, path, start, end, reason}
//	DELETE /api/git/locks/{id}?agent=
//	GET    /api/git/settings, PUT /api/git/settings
var APIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/git-api",
	Inject:      []string{gateway.ServiceKey, ServiceKey, agent.ServiceKey},
	Description: "The 代码 panel's API: repository status, hunks, commits, worktrees, code reviews, locks and pushes.",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		s := kernel.Use[*Service](c, ServiceKey)
		agents := kernel.Use[*agent.Registry](c, agent.ServiceKey)
		mobile := []string{"mobile"}
		type access interface {
			Agents(deviceID string, ids []string, act bool) bool
			ChatOwner(deviceID string) string
		}
		acc := func() access { a, _ := kernel.Lookup[access](c, "access"); return a }
		person := func(r *gateway.Request) string {
			if a := acc(); a != nil {
				return PersonActor(a.ChatOwner(r.DeviceID))
			}
			return PersonActor("")
		}
		// the agent a request is about, when the caller may reach it
		reach := func(r *gateway.Request, id string, act bool) (*agent.Agent, error) {
			a, ok := agents.Get(id)
			if !ok {
				return nil, gateway.NotFound()
			}
			if x := acc(); x != nil && !x.Agents(r.DeviceID, []string{id}, act) {
				return nil, gateway.NotFound()
			}
			return a, nil
		}
		repoOf := func(ctx context.Context, r *gateway.Request, id string, act bool) (*agent.Agent, vcs.RepoInfo, error) {
			a, err := reach(r, id, act)
			if err != nil {
				return nil, vcs.RepoInfo{}, err
			}
			info, err := s.Repo(ctx, a.Workspace())
			if err != nil {
				if errors.Is(err, vcs.ErrNotRepo) || errors.Is(err, vcs.ErrUnavailable) {
					return a, vcs.RepoInfo{}, &gateway.Error{Status: 404, Code: "GIT_NO_REPO", Message: err.Error()}
				}
				return nil, vcs.RepoInfo{}, err
			}
			return a, info, nil
		}
		// the folder a request works in: the agent's, or one of its repository's worktrees
		dirOf := func(ctx context.Context, r *gateway.Request, agentID, wt string, act bool) (string, *Worktree, vcs.RepoInfo, error) {
			a, info, err := repoOf(ctx, r, agentID, act)
			if err != nil {
				return "", nil, info, err
			}
			if wt == "" {
				return a.Workspace(), nil, info, nil
			}
			w, ok := s.Worktree(wt)
			if !ok || w.Repo != info.ID || w.State != WTActive {
				return "", nil, info, gateway.NotFound()
			}
			return w.Path, &w, info, nil
		}
		reviewOf := func(ctx context.Context, r *gateway.Request, agentID string, act bool) (Review, error) {
			_, info, err := repoOf(ctx, r, agentID, act)
			if err != nil {
				return Review{}, err
			}
			rv, ok := s.Review(r.Params["id"])
			if !ok || rv.Repo != info.ID {
				return Review{}, gateway.NotFound()
			}
			return rv, nil
		}
		q := func(r *gateway.Request, k string) string { return r.HTTP.URL.Query().Get(k) }
		vcsOr := func() (vcs.Provider, error) { return s.vcs() }

		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/git/repo", Devices: mobile,
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				a, info, err := repoOf(ctx, r, q(r, "agent"), false)
				if err != nil {
					var ge *gateway.Error
					if errors.As(err, &ge) && ge.Code == "GIT_NO_REPO" {
						return map[string]any{"repo": nil, "reason": ge.Message}, nil
					}
					return nil, err
				}
				p, err := vcsOr()
				if err != nil {
					return nil, err
				}
				st, err := p.Status(ctx, a.Workspace())
				if err != nil {
					return nil, err
				}
				wts := []WorktreeState{}
				for _, w := range s.Worktrees(info.ID, q(r, "all") == "1") {
					wts = append(wts, s.Describe(ctx, w))
				}
				log, _ := p.Log(ctx, a.Workspace(), "HEAD", 8)
				reviews := s.Reviews(info.ID)
				for i := range reviews {
					if reviews[i].State != RvMerged && reviews[i].State != RvClosed {
						if x, err := s.refresh(ctx, reviews[i].ID); err == nil {
							reviews[i] = x
						}
					}
				}
				return map[string]any{"repo": info, "status": st, "worktrees": wts, "reviews": reviews, "locks": s.Locks(info.ID), "log": log,
					"protected": s.protectedBranch(info.Branch), "here": a.Workspace()}, nil
			}})

		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/git/diff", Devices: mobile,
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				d, w, _, err := dirOf(ctx, r, q(r, "agent"), q(r, "worktree"), false)
				if err != nil {
					return nil, err
				}
				p, err := vcsOr()
				if err != nil {
					return nil, err
				}
				o := vcs.DiffOptions{Side: q(r, "side"), Base: q(r, "base"), Untracked: true}
				if path := q(r, "path"); path != "" {
					o.Paths = []string{path}
				}
				if o.Side == vcs.DiffRange && o.Base == "" && w != nil {
					o.Base = w.Base
				}
				fs, err := p.Diff(ctx, d, o)
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return map[string]any{"files": fs}, nil
			}})

		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/git/log", Devices: mobile,
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				d, _, _, err := dirOf(ctx, r, q(r, "agent"), q(r, "worktree"), false)
				if err != nil {
					return nil, err
				}
				p, err := vcsOr()
				if err != nil {
					return nil, err
				}
				n, _ := strconv.Atoi(q(r, "n"))
				cs, err := p.Log(ctx, d, q(r, "rev"), n)
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return map[string]any{"commits": cs}, nil
			}})

		type target struct {
			Agent    string `json:"agent"`
			Worktree string `json:"worktree"`
		}
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/git/hunk",
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				var b struct {
					target
					Path   string `json:"path"`
					Hunk   string `json:"hunk"`
					Action string `json:"action"`
				}
				if err := r.Decode(&b); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				d, _, _, err := dirOf(ctx, r, b.Agent, b.Worktree, true)
				if err != nil {
					return nil, err
				}
				p, err := vcsOr()
				if err != nil {
					return nil, err
				}
				if err := p.ApplyHunk(ctx, d, b.Path, b.Hunk, b.Action); err != nil {
					if errors.Is(err, vcs.ErrStaleHunk) {
						return nil, gateway.Conflict("%v", err)
					}
					return nil, gateway.BadRequest("%v", err)
				}
				s.changed(vcs.Event{Type: "git/hunk." + b.Action, ID: b.Hunk, Actor: person(r), Data: map[string]any{"path": b.Path}})
				return map[string]any{"ok": true}, nil
			}})

		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/git/files",
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				var b struct {
					target
					Paths  []string `json:"paths"`
					Action string   `json:"action"`
				}
				if err := r.Decode(&b); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				d, _, _, err := dirOf(ctx, r, b.Agent, b.Worktree, true)
				if err != nil {
					return nil, err
				}
				p, err := vcsOr()
				if err != nil {
					return nil, err
				}
				switch b.Action {
				case vcs.HunkStage:
					err = p.Stage(ctx, d, b.Paths)
				case vcs.HunkUnstage:
					err = p.Unstage(ctx, d, b.Paths)
				case vcs.HunkDiscard:
					err = p.Discard(ctx, d, b.Paths)
				default:
					return nil, gateway.BadRequest("action is stage, unstage or discard")
				}
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return map[string]any{"ok": true}, nil
			}})

		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/git/commit",
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				var b struct {
					target
					Message string `json:"message"`
					All     bool   `json:"all"`
				}
				if err := r.Decode(&b); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				d, w, info, err := dirOf(ctx, r, b.Agent, b.Worktree, true)
				if err != nil {
					return nil, err
				}
				p, err := vcsOr()
				if err != nil {
					return nil, err
				}
				cm, err := p.Commit(ctx, d, vcs.CommitRequest{Message: b.Message, All: b.All})
				if err != nil {
					if errors.Is(err, vcs.ErrNothing) {
						return nil, gateway.Conflict("nothing to commit")
					}
					return nil, gateway.BadRequest("%v", err)
				}
				s.forget(d)
				data := map[string]any{"commit": cm}
				if w != nil {
					data["worktree"] = w.ID
				}
				s.changed(vcs.Event{Type: "git/commit", Repo: info.ID, ID: cm.Hash, Actor: person(r), Data: data})
				return cm, nil
			}})

		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/git/push",
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				var b struct {
					target
					Branch string `json:"branch"`
					Remote string `json:"remote"`
					Force  bool   `json:"force"`
				}
				if err := r.Decode(&b); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				d, _, _, err := dirOf(ctx, r, b.Agent, b.Worktree, true)
				if err != nil {
					return nil, err
				}
				out, err := s.Push(ctx, PushRequest{Dir: d, Branch: b.Branch, Remote: b.Remote, Force: b.Force, Actor: person(r)})
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return map[string]any{"output": out}, nil
			}})

		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/git/worktrees",
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				var b struct {
					Agent  string `json:"agent"`
					Base   string `json:"base"`
					Branch string `json:"branch"`
					Task   string `json:"task"`
				}
				if err := r.Decode(&b); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				a, _, err := repoOf(ctx, r, b.Agent, true)
				if err != nil {
					return nil, err
				}
				w, err := s.Spawn(ctx, SpawnRequest{Dir: a.Workspace(), Base: b.Base, Branch: b.Branch, Holder: a.ID(), Parent: a.ID(), Task: b.Task, Origin: "person", Actor: person(r)})
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return s.Describe(ctx, w), nil
			}})

		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/git/worktrees/{id}", Devices: mobile,
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				_, info, err := repoOf(ctx, r, q(r, "agent"), false)
				if err != nil {
					return nil, err
				}
				w, ok := s.Worktree(r.Params["id"])
				if !ok || w.Repo != info.ID {
					return nil, gateway.NotFound()
				}
				return s.Describe(ctx, w), nil
			}})

		g.Handle(c, gateway.Route{Method: "DELETE", Path: "/api/git/worktrees/{id}",
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				_, info, err := repoOf(ctx, r, q(r, "agent"), true)
				if err != nil {
					return nil, err
				}
				w, ok := s.Worktree(r.Params["id"])
				if !ok || w.Repo != info.ID {
					return nil, gateway.NotFound()
				}
				out, err := s.Remove(ctx, RemoveRequest{ID: w.ID, Discard: q(r, "discard") == "1", KeepBranch: q(r, "keepBranch") == "1", Actor: person(r)})
				if err != nil {
					return nil, apiErr(err)
				}
				return out, nil
			}})

		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/git/reviews",
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				var b struct {
					target
					Source    string   `json:"source"`
					Target    string   `json:"target"`
					Title     string   `json:"title"`
					Body      string   `json:"body"`
					Reviewers []string `json:"reviewers"`
					Draft     bool     `json:"draft"`
				}
				if err := r.Decode(&b); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				a, info, err := repoOf(ctx, r, b.Agent, true)
				if err != nil {
					return nil, err
				}
				if b.Worktree != "" {
					if w, ok := s.Worktree(b.Worktree); !ok || w.Repo != info.ID {
						return nil, gateway.NotFound()
					}
				}
				// the person opens it on behalf of the agent whose work it is
				author := agentActor(a.ID())
				if w, ok := s.Worktree(b.Worktree); ok && w.Holder != "" {
					author = agentActor(w.Holder)
				}
				rv, err := s.Open(ctx, OpenRequest{Dir: a.Workspace(), Worktree: b.Worktree, Source: b.Source, Target: b.Target, Title: b.Title, Body: b.Body,
					Reviewers: b.Reviewers, Author: author, Draft: b.Draft})
				if err != nil {
					return nil, apiErr(err)
				}
				return rv, nil
			}})

		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/git/reviews/{id}", Devices: mobile,
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				rv, err := reviewOf(ctx, r, q(r, "agent"), false)
				if err != nil {
					return nil, err
				}
				if x, err := s.refresh(ctx, rv.ID); err == nil {
					rv = x
				}
				files, err := s.ReviewDiff(ctx, rv, nil)
				if err != nil {
					files = []vcs.FileDiff{}
				}
				out := map[string]any{"review": rv, "files": files}
				if rv.State != RvMerged && rv.State != RvClosed {
					if p, err := vcsOr(); err == nil {
						if pv, err := p.MergePreview(ctx, rv.RepoRoot, rv.Target, rv.Source); err == nil {
							out["preview"] = pv
						}
					}
				}
				out["me"] = person(r)
				out["selfReview"] = s.sameOrigin(person(r), rv.Author)
				return out, nil
			}})

		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/git/reviews/{id}/submit", Devices: mobile,
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				var b struct {
					Agent    string         `json:"agent"`
					Decision string         `json:"decision"`
					Body     string         `json:"body"`
					Comments []CommentInput `json:"comments"`
				}
				if err := r.Decode(&b); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				rv, err := reviewOf(ctx, r, b.Agent, true)
				if err != nil {
					return nil, err
				}
				out, err := s.Submit(ctx, SubmitRequest{ID: rv.ID, Actor: person(r), Decision: b.Decision, Body: b.Body, Comments: b.Comments})
				if err != nil {
					return nil, apiErr(err)
				}
				return out, nil
			}})

		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/git/reviews/{id}/comments/{cid}/resolve", Devices: mobile,
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				var b struct {
					Agent    string `json:"agent"`
					Resolved *bool  `json:"resolved"`
				}
				if err := r.Decode(&b); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				rv, err := reviewOf(ctx, r, b.Agent, true)
				if err != nil {
					return nil, err
				}
				out, err := s.ResolveComment(rv.ID, r.Params["cid"], person(r), b.Resolved == nil || *b.Resolved)
				if err != nil {
					return nil, apiErr(err)
				}
				return out, nil
			}})

		for _, act := range []string{"publish", "close", "again"} {
			g.Handle(c, gateway.Route{Method: "POST", Path: "/api/git/reviews/{id}/" + act,
				Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
					var b struct {
						Agent string `json:"agent"`
					}
					_ = r.Decode(&b)
					rv, err := reviewOf(ctx, r, b.Agent, true)
					if err != nil {
						return nil, err
					}
					var out Review
					switch act {
					case "close":
						out, err = s.Close(rv.ID, person(r))
					default:
						out, err = s.again(ctx, OpenRequest{ID: rv.ID, Author: person(r)})
					}
					if err != nil {
						return nil, apiErr(err)
					}
					return out, nil
				}})
		}

		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/git/reviews/{id}/merge",
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				var b struct {
					Agent    string `json:"agent"`
					Strategy string `json:"strategy"`
					Message  string `json:"message"`
				}
				if err := r.Decode(&b); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				rv, err := reviewOf(ctx, r, b.Agent, true)
				if err != nil {
					return nil, err
				}
				out, cm, err := s.MergeReview(ctx, MergeReviewRequest{ID: rv.ID, Actor: person(r), Strategy: b.Strategy, Message: b.Message, Approved: true})
				if err != nil {
					return nil, apiErr(err)
				}
				return map[string]any{"review": out, "commit": cm}, nil
			}})

		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/git/locks",
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				var b struct {
					Agent  string `json:"agent"`
					Path   string `json:"path"`
					Start  int    `json:"start"`
					End    int    `json:"end"`
					Reason string `json:"reason"`
				}
				if err := r.Decode(&b); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				_, info, err := repoOf(ctx, r, b.Agent, true)
				if err != nil {
					return nil, err
				}
				l, err := s.AddLock(LockRequest{Repo: info.ID, Path: b.Path, Start: b.Start, End: b.End, Holder: person(r), Reason: b.Reason})
				if err != nil {
					return nil, apiErr(err)
				}
				return l, nil
			}})

		g.Handle(c, gateway.Route{Method: "DELETE", Path: "/api/git/locks/{id}",
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				_, info, err := repoOf(ctx, r, q(r, "agent"), true)
				if err != nil {
					return nil, err
				}
				for _, l := range s.Locks(info.ID) {
					if l.ID == r.Params["id"] {
						if err := s.RemoveLock(l.ID, person(r)); err != nil {
							return nil, apiErr(err)
						}
						return map[string]any{"ok": true}, nil
					}
				}
				return nil, gateway.NotFound()
			}})

		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/git/settings",
			Handle: func(context.Context, *gateway.Request) (any, error) { return s.Settings(), nil }})
		g.Handle(c, gateway.Route{Method: "PUT", Path: "/api/git/settings",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b Settings
				if err := r.Decode(&b); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				out, err := s.SetSettings(b)
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return out, nil
			}})

		// live updates: the panel asks again when its repository changes
		vcs.Changed.Listen(c, func(ev vcs.Event) {
			g.Hub().Publish("git.changed", map[string]any{"type": ev.Type, "repo": ev.Repo, "id": ev.ID})
		})
		return nil
	},
})

// apiErr maps refusals to HTTP errors with their stable codes.
func apiErr(err error) error {
	var ge *Error
	switch {
	case errors.As(err, &ge):
		st := ge.Status
		if st == 0 {
			st = http.StatusConflict
		}
		return &gateway.Error{Status: st, Code: ge.Code, Message: ge.Msg}
	case errors.Is(err, ErrNotFound):
		return gateway.NotFound()
	case errors.Is(err, vcs.ErrConflict):
		return &gateway.Error{Status: 409, Code: CodeConflict, Message: err.Error()}
	}
	msg := err.Error()
	if strings.Contains(msg, "not found") {
		return gateway.NotFound()
	}
	return gateway.BadRequest("%s", msg)
}
