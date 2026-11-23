package git

// GitHub pull requests: list and read them, check one out into a worktree
// of its own (a fork's is untrusted: read only, nothing runs in it outside
// Docker), open one from a reviewed branch, publish the local review on it,
// merge it, and read its checks.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/vcs"
)

// Codes.
const (
	CodeChecksFailed = "GIT_PR_CHECKS_FAILED"
	CodeNotMergeable = "GIT_PR_NOT_MERGEABLE"
)

// PRSummary is a pull request in a list.
type PRSummary struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	Author    string    `json:"author"`
	State     string    `json:"state"`
	Draft     bool      `json:"draft,omitempty"`
	Fork      bool      `json:"fork,omitempty"`
	Head      string    `json:"head"`
	Base      string    `json:"base"`
	HeadSHA   string    `json:"headSha"`
	Updated   time.Time `json:"updated"`
	Review    string    `json:"review,omitempty"`   // the local review it is tied to
	Worktree  string    `json:"worktree,omitempty"` // where it is checked out
	Untrusted bool      `json:"untrusted,omitempty"`
}

// CheckResult is one check (a check run or a commit status).
type CheckResult struct {
	Name        string   `json:"name"`
	Status      string   `json:"status"`     // queued | in_progress | completed
	Conclusion  string   `json:"conclusion"` // success | failure | …
	URL         string   `json:"url,omitempty"`
	Summary     string   `json:"summary,omitempty"`
	Annotations []string `json:"annotations,omitempty"` // path:line: message (failures first)
}

// Checks are a commit's checks and what they add up to.
type Checks struct {
	State string        `json:"state"` // success | failure | pending | none
	Runs  []CheckResult `json:"runs"`
}

// PRView is one pull request in full.
type PRView struct {
	PRSummary
	Body      string            `json:"body"`
	Additions int               `json:"additions"`
	Deletions int               `json:"deletions"`
	Files     []ghFile          `json:"files"`
	Mergeable *bool             `json:"mergeable"`
	MState    string            `json:"mergeableState,omitempty"`
	Checks    Checks            `json:"checks"`
	Reviews   []ghReview        `json:"reviews"`
	Comments  []ghReviewComment `json:"comments"`
	Merged    bool              `json:"merged,omitempty"`
	Me        string            `json:"me,omitempty"` // the token's user
}

func summary(p ghPR) PRSummary {
	st := p.State
	if p.Merged {
		st = "merged"
	}
	return PRSummary{Number: p.Number, Title: p.Title, URL: p.HTMLURL, Author: p.User.Login, State: st, Draft: p.Draft, Fork: p.Fork(),
		Head: p.Head.Ref, Base: p.Base.Ref, HeadSHA: p.Head.SHA, Updated: p.UpdatedAt}
}

// annotate adds what is known locally about a PR (its review, its worktree).
func (s *Service) annotate(repo string, slug string, p *PRSummary) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	for _, r := range s.st.st.Reviews {
		if r.Repo == repo && r.PR != nil && r.PR.Number == p.Number && strings.EqualFold(r.PR.Repo, slug) {
			p.Review = r.ID
		}
	}
	for _, w := range s.st.st.Worktrees {
		if w.Repo == repo && w.State == WTActive && w.PR == p.Number && strings.EqualFold(w.PRRepo, slug) {
			p.Worktree, p.Untrusted = w.ID, w.Untrusted
		}
	}
}

// ListPRs lists the pull requests of dir's repository on GitHub.
func (s *Service) ListPRs(ctx context.Context, dir, state string) ([]PRSummary, error) {
	c, err := s.github(ctx, dir)
	if err != nil {
		return nil, err
	}
	info, err := s.Repo(ctx, dir)
	if err != nil {
		return nil, err
	}
	ps, err := c.listPRs(ctx, state, 50)
	if err != nil {
		return nil, err
	}
	out := make([]PRSummary, 0, len(ps))
	for _, p := range ps {
		x := summary(p)
		s.annotate(info.ID, c.repo.Slug(), &x)
		out = append(out, x)
	}
	return out, nil
}

// ViewPR reads one pull request with its files, checks and reviews.
func (s *Service) ViewPR(ctx context.Context, dir string, n int) (PRView, error) {
	c, err := s.github(ctx, dir)
	if err != nil {
		return PRView{}, err
	}
	info, err := s.Repo(ctx, dir)
	if err != nil {
		return PRView{}, err
	}
	p, err := c.getPRMergeable(ctx, n, 3, 700*time.Millisecond)
	if err != nil {
		return PRView{}, err
	}
	v := PRView{PRSummary: summary(p), Body: p.Body, Additions: p.Additions, Deletions: p.Deletions, Mergeable: p.Mergeable, MState: p.MergeableState, Merged: p.Merged}
	s.annotate(info.ID, c.repo.Slug(), &v.PRSummary)
	v.Files, _ = c.prFiles(ctx, n)
	v.Checks, _ = s.checks(ctx, c, p.Head.SHA)
	v.Reviews, _ = c.reviews(ctx, n)
	v.Comments, _ = c.reviewComments(ctx, n)
	if v.Files == nil {
		v.Files = []ghFile{}
	}
	if v.Reviews == nil {
		v.Reviews = []ghReview{}
	}
	if v.Comments == nil {
		v.Comments = []ghReviewComment{}
	}
	v.Me, _ = c.me(ctx)
	return v, nil
}

// checks adds up a commit's check runs and statuses; failed runs carry
// their annotations (a few, failures first) so an agent can act on them.
func (s *Service) checks(ctx context.Context, c *ghClient, sha string) (Checks, error) {
	out := Checks{State: "none", Runs: []CheckResult{}}
	runs, err := c.checkRuns(ctx, sha)
	if err != nil {
		return out, err
	}
	failed, pending, ok := 0, 0, 0
	for _, r := range runs {
		cr := CheckResult{Name: r.Name, Status: r.Status, Conclusion: r.Conclusion, URL: r.HTMLURL, Summary: trunc(strings.TrimSpace(r.Output.Title+"\n"+r.Output.Summary), 600)}
		switch {
		case r.Status != "completed":
			pending++
		case failedConclusion(r.Conclusion):
			failed++
			if r.Output.Annotations > 0 {
				as, _ := c.annotations(ctx, r.ID)
				slices.SortStableFunc(as, func(a, b ghAnnotation) int { return levelRank(a.Level) - levelRank(b.Level) })
				for i, a := range as {
					if i >= 15 {
						break
					}
					cr.Annotations = append(cr.Annotations, fmt.Sprintf("%s:%d: %s%s", a.Path, a.StartLine, prefix(a.Title), trunc(strings.TrimSpace(a.Message), 400)))
				}
			}
		default:
			ok++
		}
		out.Runs = append(out.Runs, cr)
	}
	// commit statuses (older CI integrations)
	if _, sts, err := c.statuses(ctx, sha); err == nil {
		for _, st := range sts {
			cr := CheckResult{Name: st.Context, Status: "completed", Conclusion: st.State, URL: st.TargetURL, Summary: st.Description}
			switch st.State {
			case "pending":
				cr.Status = "in_progress"
				pending++
			case "failure", "error":
				failed++
			default:
				ok++
			}
			out.Runs = append(out.Runs, cr)
		}
	}
	switch {
	case failed > 0:
		out.State = "failure"
	case pending > 0:
		out.State = "pending"
	case ok > 0:
		out.State = "success"
	}
	return out, nil
}

func failedConclusion(c string) bool {
	switch c {
	case "failure", "timed_out", "cancelled", "action_required", "startup_failure":
		return true
	}
	return false
}

func levelRank(l string) int {
	switch l {
	case "failure":
		return 0
	case "warning":
		return 1
	}
	return 2
}

func prefix(t string) string {
	if t = strings.TrimSpace(t); t == "" {
		return ""
	}
	return t + ": "
}

// RenderChecks is checks for a model: failed ones with their annotations.
func RenderChecks(c Checks) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Checks: %s", c.State)
	for _, r := range c.Runs {
		if !failedConclusion(r.Conclusion) && r.Conclusion != "failure" && r.Conclusion != "error" {
			continue
		}
		fmt.Fprintf(&b, "\n✗ %s (%s) %s", r.Name, r.Conclusion, r.URL)
		if r.Summary != "" {
			fmt.Fprintf(&b, "\n  %s", strings.ReplaceAll(trunc(r.Summary, 500), "\n", "\n  "))
		}
		for _, a := range r.Annotations {
			fmt.Fprintf(&b, "\n  - %s", a)
		}
	}
	pending := 0
	for _, r := range c.Runs {
		if r.Status != "completed" {
			pending++
		}
	}
	if pending > 0 {
		fmt.Fprintf(&b, "\n(%d still running)", pending)
	}
	return b.String()
}

// ---------------------------------------------------------------- checkout

// CheckoutPR fetches pull request n into a worktree of its own. A fork's
// worktree is untrusted. Checking out again refreshes it to the PR's head.
func (s *Service) CheckoutPR(ctx context.Context, dir string, n int, holder, actor, session string) (Worktree, error) {
	g, err := s.vcs()
	if err != nil {
		return Worktree{}, err
	}
	c, err := s.github(ctx, dir)
	if err != nil {
		return Worktree{}, err
	}
	info, err := s.Repo(ctx, dir)
	if err != nil {
		return Worktree{}, err
	}
	p, err := c.getPR(ctx, n)
	if err != nil {
		return Worktree{}, err
	}
	main := info.Main
	if main == "" {
		main = info.Root
	}
	ref := fmt.Sprintf("refs/crewbase/pr/%d", n)
	if _, err := g.Fetch(ctx, main, vcs.FetchRequest{RefSpecs: []string{fmt.Sprintf("+refs/pull/%d/head:%s", n, ref)}, Token: c.token}); err != nil {
		return Worktree{}, fmt.Errorf("fetch pull request #%d: %w", n, err)
	}
	s.changed(vcs.Event{Type: "git/fetch", Repo: info.ID, Actor: actor, Session: session, Data: map[string]any{"pr": n, "ref": ref}})
	// already checked out: the same head stays, a new one replaces a
	// worktree with nothing of its own (an untrusted one never has any)
	for _, w := range s.Worktrees(info.ID, false) {
		if w.PR != n || !strings.EqualFold(w.PRRepo, c.repo.Slug()) {
			continue
		}
		head, _ := g.Resolve(ctx, w.Path, "HEAD")
		if head == p.Head.SHA {
			return w, nil
		}
		dirty, _, _ := s.hasWork(ctx, w)
		if dirty && !w.Untrusted {
			return w, fmt.Errorf("pull request #%d moved to %s, but worktree %s has uncommitted changes; commit or discard them first", n, short(p.Head.SHA), w.ID)
		}
		if _, err := s.Remove(ctx, RemoveRequest{ID: w.ID, Discard: true, Actor: actor}); err != nil {
			return w, err
		}
	}
	if holder == "" {
		holder = s.agentOfDir(dir)
	}
	task := fmt.Sprintf("GitHub #%d: %s (by %s)", n, p.Title, p.User.Login)
	w, err := s.Spawn(ctx, SpawnRequest{Dir: dir, Base: ref, Branch: fmt.Sprintf("pr/%d-%s", n, newID("")[:6]), Holder: holder, Parent: holder, Session: session,
		Task: task, Origin: "pr", Actor: actor})
	if err != nil {
		return Worktree{}, err
	}
	untrusted := p.Fork()
	w, err = s.updateWorktree(w.ID, func(x *Worktree) {
		x.PR, x.PRRepo, x.PRAuthor, x.Untrusted = n, c.repo.Slug(), p.User.Login, untrusted
		x.Base = p.Base.Ref
	})
	if err != nil {
		return Worktree{}, err
	}
	s.changed(vcs.Event{Type: "git/pr.checked-out", Repo: info.ID, ID: w.ID, Actor: actor, Session: session, Data: map[string]any{"pr": n, "untrusted": untrusted, "head": p.Head.SHA}})
	return w, nil
}

func short(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

// agentOfDir is the (non-ephemeral) agent whose folder dir is.
func (s *Service) agentOfDir(dir string) string {
	for _, a := range s.agents.List() {
		if !a.Spec().Ephemeral && a.Workspace() == dir {
			return a.ID()
		}
	}
	return ""
}

// ---------------------------------------------------------------- create

// CreatePRRequest opens (or updates) a pull request for a review's branch.
type CreatePRRequest struct {
	Review  string
	Title   string
	Body    string // extra words above the generated part
	Draft   bool
	Actor   string
	Session string
}

// PRBody is the description a pull request gets from its review: what the
// author wrote, then who wrote and reviewed it, and the verdicts.
func (s *Service) PRBody(r Review, extra string) string {
	var b strings.Builder
	if extra = strings.TrimSpace(extra); extra != "" {
		b.WriteString(extra + "\n\n")
	}
	if strings.TrimSpace(r.Body) != "" {
		b.WriteString(strings.TrimSpace(r.Body) + "\n\n")
	}
	b.WriteString("---\n")
	fmt.Fprintf(&b, "Written by **%s** in Crewbase", s.name(r.Author))
	if r.Origin != "" && r.Origin != r.Author {
		fmt.Fprintf(&b, " (for %s)", s.name(r.Origin))
	}
	b.WriteString(".")
	var vs []string
	for _, v := range r.Verdicts {
		if v.Decision != VComment && v.Round == r.Round {
			vs = append(vs, fmt.Sprintf("%s: %s", s.name(v.Reviewer), verdictWord(v.Decision)))
		}
	}
	if len(vs) > 0 {
		fmt.Fprintf(&b, " Local review %s (round %d): %s.", r.ID, r.Round, strings.Join(vs, "; "))
	}
	return b.String()
}

func verdictWord(d string) string {
	switch d {
	case VApprove:
		return "approved"
	case VRequestChanges:
		return "changes requested"
	}
	return "commented"
}

// CreatePR pushes a review's branch and opens a pull request for it (or
// updates the one it already has).
func (s *Service) CreatePR(ctx context.Context, q CreatePRRequest) (Review, error) {
	r, ok := s.Review(q.Review)
	if !ok {
		return Review{}, ErrNotFound
	}
	if r.State == RvMerged || r.State == RvClosed {
		return Review{}, fmt.Errorf("review %s is %s", r.ID, r.State)
	}
	if strings.HasPrefix(r.Author, "person:github:") {
		return Review{}, fmt.Errorf("review %s is of someone else's pull request (#%d): publish it with git_pr_review instead", r.ID, prNumber(r))
	}
	c, err := s.github(ctx, r.RepoRoot)
	if err != nil {
		return Review{}, err
	}
	if c.token == "" {
		return Review{}, fmt.Errorf("no credential for %s: add one in Settings › Git (push credentials)", c.repo.Host)
	}
	if _, err := s.Push(ctx, PushRequest{Dir: r.RepoRoot, Branch: r.Source, Actor: q.Actor, Session: q.Session}); err != nil {
		return Review{}, fmt.Errorf("push %s: %w", r.Source, err)
	}
	title := strings.TrimSpace(q.Title)
	if title == "" {
		title = r.Title
	}
	body := s.PRBody(r, q.Body)
	var p ghPR
	switch {
	case r.PR != nil && r.PR.State == "open":
		p, err = c.updatePR(ctx, r.PR.Number, title, body)
	default:
		var found *ghPR
		if found, err = c.findPR(ctx, r.Source); err == nil && found != nil {
			p, err = c.updatePR(ctx, found.Number, title, body)
		} else if err == nil {
			p, err = c.createPR(ctx, title, r.Source, r.Target, body, q.Draft)
		}
	}
	if err != nil {
		return Review{}, err
	}
	out, err := s.mutate(r.ID, func(x *Review) error {
		link := x.PR
		if link == nil || link.Number != p.Number {
			link = &PRLink{Number: p.Number}
		}
		link.URL, link.Host, link.Repo, link.State, link.Draft = p.HTMLURL, c.repo.Host, c.repo.Slug(), p.State, p.Draft
		link.HeadSHA, link.Base, link.Updated = p.Head.SHA, p.Base.Ref, time.Now().UTC()
		x.PR = link
		return nil
	})
	if err != nil {
		return Review{}, err
	}
	s.changed(vcs.Event{Type: "git/pr.opened", Repo: r.Repo, ID: r.ID, Actor: q.Actor, Session: q.Session, Data: map[string]any{"pr": p.Number, "url": p.HTMLURL, "repo": c.repo.Slug()}})
	return out, nil
}

func prNumber(r Review) int {
	if r.PR != nil {
		return r.PR.Number
	}
	return 0
}

// ---------------------------------------------------------------- review

// SyncPRReview publishes the review's comments not yet on GitHub, with its
// verdict, as a GitHub review. On a pull request the token's own user
// opened (one shared token), approving or requesting changes is refused by
// GitHub: the verdict goes in the text of a comment instead.
func (s *Service) SyncPRReview(ctx context.Context, id, actor, session string) (Review, string, error) {
	r, ok := s.Review(id)
	if !ok {
		return Review{}, "", ErrNotFound
	}
	if r.PR == nil {
		return Review{}, "", errors.New("the review has no pull request yet: open one with git_pr_create")
	}
	c, err := s.github(ctx, r.RepoRoot)
	if err != nil {
		return Review{}, "", err
	}
	in, posted, err := s.prReviewInput(ctx, c, r)
	if err != nil {
		return Review{}, "", err
	}
	gr, err := c.createReview(ctx, r.PR.Number, in)
	if err != nil {
		return Review{}, "", err
	}
	out, err := s.mutate(r.ID, func(x *Review) error {
		if x.PR != nil {
			x.PR.Posted = append(x.PR.Posted, posted...)
			x.PR.Updated = time.Now().UTC()
		}
		return nil
	})
	s.changed(vcs.Event{Type: "git/pr.reviewed", Repo: r.Repo, ID: r.ID, Actor: actor, Session: session,
		Data: map[string]any{"pr": r.PR.Number, "event": in.Event, "comments": len(in.Comments), "url": gr.HTMLURL}})
	return out, in.Event, err
}

// prReviewInput builds the GitHub review: line comments on lines the pull
// request's diff shows, the others in the body; the event from the local
// state, a comment when the token's user is the PR's author.
func (s *Service) prReviewInput(ctx context.Context, c *ghClient, r Review) (ghReviewIn, []string, error) {
	p, err := c.getPR(ctx, r.PR.Number)
	if err != nil {
		return ghReviewIn{}, nil, err
	}
	diff, _ := s.ReviewDiff(ctx, r, nil)
	onDiff := map[string]bool{}
	for _, f := range diff {
		for _, h := range f.Hunks {
			for _, l := range h.Lines {
				if l.New > 0 {
					onDiff[fmt.Sprintf("%s:new:%d", f.Path, l.New)] = true
				}
				if l.Old > 0 && l.Kind == "-" {
					onDiff[fmt.Sprintf("%s:old:%d", f.Path, l.Old)] = true
				}
			}
		}
	}
	in := ghReviewIn{CommitID: p.Head.SHA, Event: "COMMENT"}
	var body []string
	var posted []string
	for _, cm := range r.Comments {
		if slices.Contains(r.PR.Posted, cm.ID) || cm.Resolved || cm.Severity == SevNote && cm.Path == "" {
			continue
		}
		text := fmt.Sprintf("**[%s]** %s\n\n— %s", cm.Severity, cm.Body, s.name(cm.Author))
		side := cm.Side
		if side == "" {
			side = "new"
		}
		if cm.Path != "" && cm.Line > 0 && onDiff[fmt.Sprintf("%s:%s:%d", cm.Path, side, cm.Line)] {
			ghSide := "RIGHT"
			if side == "old" {
				ghSide = "LEFT"
			}
			in.Comments = append(in.Comments, ghReviewCommentIn{Path: cm.Path, Line: cm.Line, Side: ghSide, Body: text})
		} else {
			at := ""
			if cm.Path != "" {
				at = fmt.Sprintf("`%s:%d` ", cm.Path, cm.Line)
			}
			body = append(body, "- "+at+text)
		}
		posted = append(posted, cm.ID)
	}
	verdict := "COMMENT"
	switch r.State {
	case RvApproved:
		verdict = "APPROVE"
	case RvChangesRequested:
		verdict = "REQUEST_CHANGES"
	}
	head := []string{fmt.Sprintf("**Crewbase review** (%s → %s, round %d): **%s**", r.Source, r.Target, r.Round, stateWord(r.State))}
	for _, v := range r.Verdicts {
		if v.Round == r.Round && v.Decision != VComment {
			line := fmt.Sprintf("- %s: %s", s.name(v.Reviewer), verdictWord(v.Decision))
			if v.Body != "" {
				line += " — " + trunc(strings.ReplaceAll(v.Body, "\n", " "), 600)
			}
			head = append(head, line)
		}
	}
	if r.Head != "" && !strings.HasPrefix(p.Head.SHA, r.Head) && !strings.HasPrefix(r.Head, p.Head.SHA) {
		head = append(head, fmt.Sprintf("_Reviewed at %s; the pull request is now at %s._", short(r.Head), short(p.Head.SHA)))
	}
	if me, err := c.me(ctx); err == nil && strings.EqualFold(me, p.User.Login) && verdict != "COMMENT" {
		// GitHub refuses approving or requesting changes on your own PR
		head = append(head, fmt.Sprintf("_Posted as a comment: this token's user (%s) opened the pull request, and GitHub does not let an author %s their own pull request._", me, map[string]string{"APPROVE": "approve", "REQUEST_CHANGES": "request changes on"}[verdict]))
		verdict = "COMMENT"
	}
	in.Event = verdict
	in.Body = strings.Join(head, "\n")
	if len(body) > 0 {
		in.Body += "\n\n" + strings.Join(body, "\n")
	}
	return in, posted, nil
}

func stateWord(st string) string {
	switch st {
	case RvApproved:
		return "approved"
	case RvChangesRequested:
		return "changes requested"
	case RvMerged:
		return "merged"
	}
	return "in review"
}

// ---------------------------------------------------------------- merge

// MergePRRequest merges a review's pull request on GitHub.
type MergePRRequest struct {
	Review  string
	Method  string // merge | squash | rebase
	Actor   string
	Session string
}

// MergePR merges the review's pull request on GitHub: the local review must
// be approved, nothing frozen may change, GitHub must say it merges and its
// checks must not have failed; it merges at the head that was checked.
func (s *Service) MergePR(ctx context.Context, q MergePRRequest) (Review, string, error) {
	r, err := s.refresh(ctx, q.Review)
	if err != nil {
		return Review{}, "", err
	}
	if r.PR == nil {
		return r, "", errors.New("the review has no pull request")
	}
	if r.State != RvApproved {
		return r, "", fail(CodeNotApproved, 409, "review %s is %s: it merges once approved", r.ID, r.State)
	}
	method := q.Method
	switch method {
	case "", "merge":
		method = "merge"
	case "squash", "rebase":
	default:
		return r, "", fmt.Errorf("method is merge, squash or rebase")
	}
	changed, _ := s.ReviewDiff(ctx, r, nil)
	check := &vcs.PreMergeCheck{Repo: r.Repo, Target: r.Target, Source: r.Source, Actor: q.Actor, Author: r.Author, Changed: changed}
	if s.c != nil {
		if res, err := vcs.PreMerge.Run(s.c, ctx, check, func(_ context.Context, c *vcs.PreMergeCheck) (*vcs.PreMergeCheck, error) { return c, nil }); err == nil && res != nil {
			check = res
		}
	}
	if check.Deny != "" {
		code := check.Code
		if code == "" {
			code = "GIT_MERGE_DENIED"
		}
		return r, "", fail(code, 409, "%s", check.Deny)
	}
	c, err := s.github(ctx, r.RepoRoot)
	if err != nil {
		return r, "", err
	}
	p, err := c.getPRMergeable(ctx, r.PR.Number, 5, time.Second)
	if err != nil {
		return r, "", err
	}
	if p.Merged {
		return r, "", fmt.Errorf("pull request #%d is already merged", p.Number)
	}
	if p.State != "open" {
		return r, "", fmt.Errorf("pull request #%d is %s", p.Number, p.State)
	}
	if p.Mergeable == nil {
		return r, "", fail(CodeNotMergeable, 409, "GitHub is still working out whether #%d merges; try again in a moment", p.Number)
	}
	if !*p.Mergeable {
		return r, "", fail(CodeNotMergeable, 409, "#%d does not merge cleanly into %s (%s): resolve the conflicts on the branch first", p.Number, p.Base.Ref, p.MergeableState)
	}
	ch, _ := s.checks(ctx, c, p.Head.SHA)
	if ch.State == "failure" {
		return r, "", fail(CodeChecksFailed, 409, "checks failed on #%d:\n%s", p.Number, RenderChecks(ch))
	}
	if r.Head != "" && !strings.HasPrefix(p.Head.SHA, r.Head) && !strings.HasPrefix(r.Head, p.Head.SHA) {
		return r, "", fail(CodeStale, 409, "#%d is at %s but the review approved %s: ask for review again", p.Number, short(p.Head.SHA), short(r.Head))
	}
	sha, err := c.merge(ctx, p.Number, method, p.Head.SHA, fmt.Sprintf("%s (#%d)", p.Title, p.Number), "")
	if err != nil {
		return r, "", err
	}
	out, err := s.mutate(r.ID, func(x *Review) error {
		x.State = RvMerged
		x.Merge = &MergeInfo{Commit: sha, Strategy: "github-" + method, By: q.Actor, At: time.Now().UTC()}
		if x.PR != nil {
			x.PR.State, x.PR.Updated = "merged", time.Now().UTC()
		}
		return nil
	})
	s.changed(vcs.Event{Type: "git/pr.merged", Repo: r.Repo, ID: r.ID, Actor: q.Actor, Session: q.Session, Data: map[string]any{"pr": p.Number, "commit": sha, "method": method}})
	s.changed(vcs.Event{Type: "git/merged", Repo: r.Repo, ID: r.ID, Actor: q.Actor, Session: q.Session, Data: map[string]any{"commit": sha, "strategy": "github-" + method, "target": r.Target, "source": r.Source}})
	return out, sha, err
}

// RefreshPR reads a review's pull request again (state, head, whether it
// merges, its checks) into its link.
func (s *Service) RefreshPR(ctx context.Context, id string) (Review, error) {
	r, ok := s.Review(id)
	if !ok || r.PR == nil {
		return Review{}, ErrNotFound
	}
	c, err := s.github(ctx, r.RepoRoot)
	if err != nil {
		return Review{}, err
	}
	p, err := c.getPRMergeable(ctx, r.PR.Number, 3, 700*time.Millisecond)
	if err != nil {
		return Review{}, err
	}
	ch, _ := s.checks(ctx, c, p.Head.SHA)
	return s.mutate(id, func(x *Review) error {
		if x.PR == nil {
			return nil
		}
		st := p.State
		if p.Merged {
			st = "merged"
			if x.State != RvMerged {
				x.State = RvMerged
				x.Merge = &MergeInfo{Commit: p.MergeCommitSHA, Strategy: "github", By: "person:github", At: time.Now().UTC()}
			}
		}
		x.PR.State, x.PR.Draft, x.PR.HeadSHA, x.PR.Mergeable, x.PR.URL = st, p.Draft, p.Head.SHA, p.Mergeable, p.HTMLURL
		if ch.State != "none" {
			x.PR.Checks = ch.State
		}
		x.PR.Updated = time.Now().UTC()
		return nil
	})
}
