package git

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hedeqiang/crewbase/capabilities/sandbox"
	"github.com/hedeqiang/crewbase/capabilities/vcs"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

// untrustedAt is the untrusted worktree p lies in.
func (s *Service) untrustedAt(p string) (Worktree, bool) {
	if p == "" {
		return Worktree{}, false
	}
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	for _, w := range s.st.st.Worktrees {
		if w.State == WTActive && w.Untrusted && (p == w.Path || strings.HasPrefix(p, w.Path+string(filepath.Separator))) {
			return *w, true
		}
	}
	return Worktree{}, false
}

// untrustedIn: the untrusted worktree a command call works in or names
// (its workspace, its cwd argument, or a path in the command text).
func (s *Service) untrustedIn(call tools.Call) (Worktree, bool) {
	if w, ok := s.untrustedAt(call.Workspace); ok {
		return w, true
	}
	var args map[string]any
	_ = json.Unmarshal(call.Args, &args)
	if cwd, _ := args["cwd"].(string); cwd != "" {
		if !filepath.IsAbs(cwd) {
			cwd = filepath.Join(call.Workspace, cwd)
		}
		if w, ok := s.untrustedAt(cwd); ok {
			return w, true
		}
	}
	text := string(call.Args)
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	for _, w := range s.st.st.Worktrees {
		if w.State != WTActive || !w.Untrusted {
			continue
		}
		// the folder named in the command, or (shorter) its id
		if strings.Contains(text, w.Path) || strings.Contains(text, w.ID) {
			return *w, true
		}
	}
	return Worktree{}, false
}

// gitWrites are the git tools that change a working tree.
var gitWrites = map[string]bool{"git_commit": true, "git_hunk_apply": true, "git_conflict_resolve": true}

// preExecute (tools/pre-execute) keeps code from an untrusted pull request
// from running, and shows the full text a pull request tool publishes.
func (s *Service) preExecute(c *kernel.Context) func(ctx context.Context, d *tools.Decision, next kernel.Next[*tools.Decision, *tools.Decision]) (*tools.Decision, error) {
	return func(ctx context.Context, d *tools.Decision, next kernel.Next[*tools.Decision, *tools.Decision]) (*tools.Decision, error) {
		if d == nil || d.Call == nil {
			return next(ctx, d)
		}
		call := d.Call
		switch {
		case d.Category == tools.CategoryExec || d.Category == tools.CategoryInstall:
			if w, ok := s.untrustedIn(*call); ok {
				sb, _ := kernel.Lookup[sandbox.Sandbox](c, sandbox.ServiceKey)
				if sb == nil || sb.Name() != "docker" {
					s.changed(vcsEvent("git/untrusted.blocked", w, call))
					return d.DenyWith(fmt.Sprintf("worktree %s holds pull request #%d from a fork: its code does not run on this computer "+
						"(the sandbox here limits writes, not reads, and the network is open). Review it by reading the files and the diff; "+
						"to run its tests, switch the sandbox to Docker (Settings › Git explains how).", w.ID, w.PR)), nil
				}
				// Docker reads only what it mounts: run, offline, if the person agrees
				call.Offline = true
				d.RequireApproval(fmt.Sprintf("runs code from pull request #%d (a fork) in a Docker sandbox without network", w.PR))
				d.Rememberable = false
			}
		case gitWrites[call.Name]:
			var a struct{ Worktree string }
			_ = json.Unmarshal(call.Args, &a)
			if w, ok := s.Worktree(a.Worktree); ok && w.Untrusted {
				return d.DenyWith(fmt.Sprintf("worktree %s holds pull request #%d from a fork and is read only; review it with git_diff and git_pr_review", w.ID, w.PR)), nil
			}
			if w, ok := s.untrustedAt(call.Workspace); ok && a.Worktree == "" {
				return d.DenyWith(fmt.Sprintf("this worktree holds pull request #%d from a fork and is read only", w.PR)), nil
			}
		case call.Name == "git_pr_create":
			var a struct{ Review, Title, Body string }
			_ = json.Unmarshal(call.Args, &a)
			if r, ok := s.Review(a.Review); ok {
				title := a.Title
				if title == "" {
					title = r.Title
				}
				d.Preview = "**" + title + "**\n\n" + s.PRBody(r, a.Body)
			}
		case call.Name == "git_pr_review":
			var a struct{ Review string }
			_ = json.Unmarshal(call.Args, &a)
			if r, ok := s.Review(a.Review); ok && r.PR != nil {
				if c, err := s.github(ctx, r.RepoRoot); err == nil {
					if in, _, err := s.prReviewInput(ctx, c, r); err == nil {
						var b strings.Builder
						fmt.Fprintf(&b, "%s on #%d\n\n%s", in.Event, r.PR.Number, in.Body)
						for _, cm := range in.Comments {
							fmt.Fprintf(&b, "\n\n`%s:%d`\n%s", cm.Path, cm.Line, cm.Body)
						}
						d.Preview = b.String()
					}
				}
			}
		}
		return next(ctx, d)
	}
}

func vcsEvent(typ string, w Worktree, call *tools.Call) vcs.Event {
	sid := ""
	if call.Session != nil {
		sid = call.Session.ID()
	}
	return vcs.Event{Type: typ, Repo: w.Repo, ID: w.ID, Actor: agentActor(call.Agent), Session: sid, Data: map[string]any{"pr": w.PR, "tool": call.Name}}
}
