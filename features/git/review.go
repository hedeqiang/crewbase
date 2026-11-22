package git

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/vcs"
	"github.com/hedeqiang/crewbase/core/agent"
)

// OpenRequest opens a review (or asks for another round of an open one).
type OpenRequest struct {
	ID        string // an existing review: new commits, review again
	Dir       string // a folder of the repository (when no worktree)
	Worktree  string // the worktree whose branch is reviewed
	Source    string // the branch ("" = the worktree's / the folder's)
	Target    string // ("" = the worktree's base / main)
	Title     string
	Body      string
	Reviewers []string // agent ids, or "person"
	Author    string   // actor
	Session   string
	Draft     bool
}

// CommentInput is a comment to add.
type CommentInput struct {
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line,omitempty"`
	Side     string `json:"side,omitempty"`
	Severity string `json:"severity,omitempty"`
	Body     string `json:"body"`
}

func (s *Service) reviewOfBranch(repo, branch string) (Review, bool) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	var best *Review
	for _, r := range s.st.st.Reviews {
		if r.Repo == repo && r.Source == branch && (best == nil || r.Created.After(best.Created)) {
			best = r
		}
	}
	if best == nil {
		return Review{}, false
	}
	return copyReview(best), true
}

func copyReview(r *Review) Review {
	x := *r
	x.Reviewers = slices.Clone(r.Reviewers)
	x.Comments = slices.Clone(r.Comments)
	x.Verdicts = slices.Clone(r.Verdicts)
	return x
}

// Review returns a review by id.
func (s *Service) Review(id string) (Review, bool) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	for _, r := range s.st.st.Reviews {
		if r.ID == id {
			return copyReview(r), true
		}
	}
	return Review{}, false
}

// Reviews of a repository ("" = all), newest first.
func (s *Service) Reviews(repo string) []Review {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	out := []Review{}
	for i := len(s.st.st.Reviews) - 1; i >= 0; i-- {
		if r := s.st.st.Reviews[i]; repo == "" || r.Repo == repo {
			out = append(out, copyReview(r))
		}
	}
	return out
}

// mutate changes a review under the lock and saves it.
func (s *Service) mutate(id string, fn func(*Review) error) (Review, error) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	for _, r := range s.st.st.Reviews {
		if r.ID != id {
			continue
		}
		if err := fn(r); err != nil {
			return Review{}, err
		}
		r.Updated = time.Now().UTC()
		return copyReview(r), s.st.save()
	}
	return Review{}, ErrNotFound
}

// reviewer turns "kai" / "person" / "agent:kai" into an actor.
func reviewerActor(x string) string {
	x = strings.TrimSpace(x)
	switch {
	case x == "":
		return ""
	case strings.HasPrefix(x, "agent:"), strings.HasPrefix(x, "person:"):
		return x
	case x == "person" || x == "me" || x == "owner" || x == "user":
		return PersonActor("")
	}
	return agentActor(x)
}

// Open opens a review.
func (s *Service) Open(ctx context.Context, o OpenRequest) (Review, error) {
	g, err := s.vcs()
	if err != nil {
		return Review{}, err
	}
	if o.ID != "" {
		return s.again(ctx, o)
	}
	var info vcs.RepoInfo
	source, target := strings.TrimSpace(o.Source), strings.TrimSpace(o.Target)
	wtID := o.Worktree
	if wtID != "" {
		w, ok := s.Worktree(wtID)
		if !ok || w.State != WTActive {
			return Review{}, fmt.Errorf("no worktree %s", wtID)
		}
		if info, err = s.Repo(ctx, w.Path); err != nil {
			return Review{}, err
		}
		if source == "" {
			source = w.Branch
		}
		if target == "" {
			target = w.Base
		}
	} else {
		if info, err = s.Repo(ctx, o.Dir); err != nil {
			return Review{}, err
		}
		if source == "" {
			source = info.Branch
		}
		if w, ok := s.worktreeAt(info.Root); ok {
			wtID = w.ID
			if target == "" {
				target = w.Base
			}
		}
	}
	main := info.Main
	if main == "" {
		main = info.Root
	}
	if target == "" {
		target = s.defaultBranch(ctx, main)
	}
	if source == "" || source == target {
		return Review{}, fmt.Errorf("review a branch against another (source %q, target %q)", source, target)
	}
	if r, ok := s.reviewOfBranch(info.ID, source); ok && r.State != RvMerged && r.State != RvClosed && r.Target == target {
		o.ID = r.ID
		return s.again(ctx, o)
	}
	p, err := g.MergePreview(ctx, main, target, source)
	if err != nil {
		return Review{}, err
	}
	if p.UpToDate {
		return Review{}, fmt.Errorf("%s has nothing %s lacks: commit the work first", source, target)
	}
	author := o.Author
	var reviewers []string
	for _, x := range o.Reviewers {
		a := reviewerActor(x)
		if a == "" || slices.Contains(reviewers, a) {
			continue
		}
		if s.sameOrigin(a, author) {
			return Review{}, fail(CodeSelfApproval, 403, "%s cannot review work by %s: they work for the same %s", s.name(a), s.name(author), s.name(s.origin(author)))
		}
		if id, ok := actorAgent(a); ok {
			if _, ok := s.agents.Get(id); !ok {
				return Review{}, fmt.Errorf("unknown reviewer %s", id)
			}
		}
		reviewers = append(reviewers, a)
	}
	if len(reviewers) == 0 {
		reviewers = []string{PersonActor("")}
		if s.sameOrigin(reviewers[0], author) {
			reviewers = nil
		}
	}
	title := strings.TrimSpace(o.Title)
	if title == "" {
		if cs, _ := g.Log(ctx, main, target+".."+source, 1); len(cs) > 0 {
			title = cs[0].Subject
		} else {
			title = source
		}
	}
	set := s.st.settings()
	now := time.Now().UTC()
	r := &Review{ID: newID("rv"), Repo: info.ID, RepoRoot: main, Worktree: wtID, Source: source, Target: target, Title: trunc(title, 200), Body: trunc(o.Body, 4000),
		Author: author, Origin: s.origin(author), Reviewers: reviewers, State: RvOpen, Head: p.SourceHead, Base: p.Base, Round: 1, MaxRounds: set.MaxAutoRounds,
		Comments: []Comment{}, Verdicts: []Verdict{}, Session: o.Session, Created: now, Updated: now}
	if o.Draft {
		r.State = RvDraft
	}
	s.st.mu.Lock()
	s.st.st.Reviews = append(s.st.st.Reviews, r)
	err = s.st.save()
	out := copyReview(r)
	s.st.mu.Unlock()
	if err != nil {
		return Review{}, err
	}
	s.changed(vcs.Event{Type: "git/review.opened", Repo: r.Repo, ID: r.ID, Actor: author, Session: o.Session, Data: out})
	if out.State == RvOpen {
		s.askReviewers(ctx, out)
	}
	return out, nil
}

func (s *Service) defaultBranch(ctx context.Context, main string) string {
	g, err := s.vcs()
	if err != nil {
		return "main"
	}
	for _, b := range []string{"main", "master", "trunk", "develop"} {
		if _, err := g.Resolve(ctx, main, b); err == nil {
			return b
		}
	}
	if info, err := s.Repo(ctx, main); err == nil && info.Branch != "" {
		return info.Branch
	}
	return "main"
}

// again: the author pushed new commits (or publishes a draft); the review
// goes back to the reviewers.
func (s *Service) again(ctx context.Context, o OpenRequest) (Review, error) {
	r, ok := s.Review(o.ID)
	if !ok {
		return Review{}, ErrNotFound
	}
	if r.State == RvMerged || r.State == RvClosed {
		return Review{}, fmt.Errorf("review %s is %s", r.ID, r.State)
	}
	if !s.sameOrigin(o.Author, r.Author) && !strings.HasPrefix(o.Author, "person:") {
		return Review{}, fmt.Errorf("only %s (or a person) can ask for another round", s.name(r.Author))
	}
	head, err := s.sourceHead(ctx, r)
	if err != nil {
		return Review{}, err
	}
	wasDraft := r.State == RvDraft
	if !wasDraft && head == r.Head && r.State != RvChangesRequested {
		return r, nil // nothing new: the reviewers already have it
	}
	out, err := s.mutate(r.ID, func(x *Review) error {
		if x.State == RvChangesRequested && head != x.Head {
			x.Round++
		}
		x.Head = head
		if o.Title != "" {
			x.Title = trunc(o.Title, 200)
		}
		if o.Body != "" {
			x.Body = trunc(o.Body, 4000)
		}
		x.State = RvOpen
		s.recompute(x)
		if x.State == RvChangesRequested && head != r.Head {
			x.State = RvOpen // new commits answer the old verdicts
		}
		return nil
	})
	if err != nil {
		return Review{}, err
	}
	s.changed(vcs.Event{Type: "git/review.updated", Repo: out.Repo, ID: out.ID, Actor: o.Author, Session: o.Session, Data: out})
	if out.State == RvOpen {
		s.askReviewers(ctx, out)
	}
	return out, nil
}

func (s *Service) sourceHead(ctx context.Context, r Review) (string, error) {
	g, err := s.vcs()
	if err != nil {
		return "", err
	}
	return g.Resolve(ctx, r.RepoRoot, r.Source)
}

// refresh catches a review up with its branch: new commits make earlier
// approvals stale.
func (s *Service) refresh(ctx context.Context, id string) (Review, error) {
	r, ok := s.Review(id)
	if !ok {
		return Review{}, ErrNotFound
	}
	if r.State == RvMerged || r.State == RvClosed {
		return r, nil
	}
	head, err := s.sourceHead(ctx, r)
	if err != nil || head == r.Head {
		return r, nil
	}
	return s.mutate(id, func(x *Review) error {
		x.Head = head
		if x.State != RvDraft {
			s.recompute(x)
			if x.State == RvChangesRequested {
				x.State = RvOpen
			}
		}
		return nil
	})
}

// recompute derives the state from the comments and the verdicts at the
// current head (the caller holds the store lock).
func (s *Service) recompute(r *Review) {
	if r.State == RvMerged || r.State == RvClosed || r.State == RvDraft {
		return
	}
	for _, c := range r.Comments {
		if c.Severity == SevBlocker && !c.Resolved {
			r.State = RvChangesRequested
			return
		}
	}
	latest := map[string]Verdict{}
	for _, v := range r.Verdicts {
		if v.Decision != VComment {
			latest[v.Reviewer] = v
		}
	}
	approvals := 0
	for _, v := range latest {
		switch {
		case v.Decision == VRequestChanges && v.Round == r.Round:
			r.State = RvChangesRequested
			return
		case v.Decision == VApprove && v.Commit == r.Head:
			approvals++
		}
	}
	need := max(s.st.st.settingsOr().Approvals, 1)
	if approvals >= need {
		r.State = RvApproved
		return
	}
	r.State = RvOpen
}

func (st *state) settingsOr() Settings {
	if st.Settings == nil {
		return DefaultSettings()
	}
	return *st.Settings
}

// mayJudge: a reviewer named on it (or any person) who does not work for
// the author's origin.
func (s *Service) mayJudge(r Review, actor string) error {
	if s.sameOrigin(actor, r.Author) {
		return fail(CodeSelfApproval, 403, "%s cannot approve or reject work by %s (the same %s)", s.name(actor), s.name(r.Author), s.name(s.origin(r.Author)))
	}
	if strings.HasPrefix(actor, "person:") || slices.Contains(r.Reviewers, actor) {
		return nil
	}
	return fmt.Errorf("%s is not a reviewer of %s", s.name(actor), r.ID)
}

// SubmitRequest is a reviewer's round: comments and a verdict.
type SubmitRequest struct {
	ID       string
	Actor    string
	Decision string // approve | request_changes | comment
	Body     string
	Comments []CommentInput
	Session  string
}

// Submit records a reviewer's comments and verdict. Changes asked for go
// back to the author for another round (up to the review's MaxRounds).
func (s *Service) Submit(ctx context.Context, q SubmitRequest) (Review, error) {
	r, err := s.refresh(ctx, q.ID)
	if err != nil {
		return Review{}, err
	}
	if r.State == RvMerged || r.State == RvClosed {
		return Review{}, fmt.Errorf("review %s is %s", r.ID, r.State)
	}
	if r.State == RvDraft {
		return Review{}, fmt.Errorf("review %s is a draft: its author has not asked for review yet", r.ID)
	}
	switch q.Decision {
	case VApprove, VRequestChanges:
		if err := s.mayJudge(r, q.Actor); err != nil {
			return Review{}, err
		}
	case VComment, "":
		q.Decision = VComment
	default:
		return Review{}, fmt.Errorf("decision is approve, request_changes or comment, not %q", q.Decision)
	}
	if q.Decision == VApprove {
		for _, c := range q.Comments {
			if sev(c.Severity) == SevBlocker {
				return Review{}, errors.New("a blocker comment cannot come with an approval: request changes instead")
			}
		}
	}
	before := r.State
	now := time.Now().UTC()
	out, err := s.mutate(r.ID, func(x *Review) error {
		for _, c := range q.Comments {
			body := strings.TrimSpace(c.Body)
			if body == "" {
				continue
			}
			side := c.Side
			if side != "old" {
				side = "new"
			}
			x.Comments = append(x.Comments, Comment{ID: newID("cm"), Author: q.Actor, Path: c.Path, Line: max(c.Line, 0), Side: side, Severity: sev(c.Severity),
				Body: trunc(body, 4000), Commit: x.Head, Round: x.Round, Created: now})
		}
		x.Verdicts = append(x.Verdicts, Verdict{Reviewer: q.Actor, Decision: q.Decision, Body: trunc(strings.TrimSpace(q.Body), 4000), Commit: x.Head, Round: x.Round, At: now})
		s.recompute(x)
		return nil
	})
	if err != nil {
		return Review{}, err
	}
	s.changed(vcs.Event{Type: "git/review.submitted", Repo: out.Repo, ID: out.ID, Actor: q.Actor, Session: q.Session,
		Data: map[string]any{"decision": q.Decision, "state": out.State, "comments": len(q.Comments)}})
	if out.State == RvChangesRequested && (before != RvChangesRequested || q.Decision == VRequestChanges) {
		s.askFix(ctx, out)
	}
	if out.State == RvApproved && before != RvApproved {
		s.tellApproved(ctx, out)
	}
	return out, nil
}

func sev(x string) string {
	switch x {
	case SevBlocker, SevMajor, SevMinor, SevNit:
		return x
	}
	return SevNote
}

// Comment adds comments without a verdict (anyone who may see it, the
// author answering included).
func (s *Service) Comment(ctx context.Context, id, actor string, cs []CommentInput) (Review, error) {
	return s.Submit(ctx, SubmitRequest{ID: id, Actor: actor, Decision: VComment, Comments: cs})
}

// ResolveComment marks a comment done (its author, the review's author's
// side, or a person).
func (s *Service) ResolveComment(id, comment, actor string, resolved bool) (Review, error) {
	r, ok := s.Review(id)
	if !ok {
		return Review{}, ErrNotFound
	}
	for _, c := range r.Comments {
		if c.ID == comment && c.Author != actor && !s.sameOrigin(actor, r.Author) && !strings.HasPrefix(actor, "person:") {
			return Review{}, fmt.Errorf("only its author, %s's side or a person can resolve it", s.name(r.Author))
		}
	}
	out, err := s.mutate(id, func(x *Review) error {
		for i := range x.Comments {
			c := &x.Comments[i]
			if c.ID != comment {
				continue
			}
			c.Resolved = resolved
			s.recompute(x)
			return nil
		}
		return ErrNotFound
	})
	if err == nil {
		s.changed(vcs.Event{Type: "git/review.updated", Repo: out.Repo, ID: out.ID, Actor: actor, Data: out})
	}
	return out, err
}

// Close closes a review without merging (its author's side or a person).
func (s *Service) Close(id, actor string) (Review, error) {
	r, ok := s.Review(id)
	if !ok {
		return Review{}, ErrNotFound
	}
	if !s.sameOrigin(actor, r.Author) && !strings.HasPrefix(actor, "person:") {
		return Review{}, fmt.Errorf("only %s's side or a person can close it", s.name(r.Author))
	}
	out, err := s.mutate(id, func(x *Review) error {
		if x.State == RvMerged {
			return errors.New("it is merged")
		}
		x.State = RvClosed
		return nil
	})
	if err == nil {
		s.changed(vcs.Event{Type: "git/review.closed", Repo: out.Repo, ID: out.ID, Actor: actor, Data: out})
	}
	return out, err
}

// Publish turns a draft into an open review.
func (s *Service) Publish(ctx context.Context, id, actor string) (Review, error) {
	return s.again(ctx, OpenRequest{ID: id, Author: actor})
}

// ReviewDiff is what the review's branch changes since it forked.
func (s *Service) ReviewDiff(ctx context.Context, r Review, paths []string) ([]vcs.FileDiff, error) {
	g, err := s.vcs()
	if err != nil {
		return nil, err
	}
	head := r.Source
	if r.State == RvMerged && r.Merge != nil {
		head = r.Head
	}
	base := r.Target
	if r.State == RvMerged {
		base = r.Base
	}
	return g.Diff(ctx, r.RepoRoot, vcs.DiffOptions{Side: vcs.DiffRange, Base: base, Head: head, Paths: paths})
}

// ---------------------------------------------------------------- merge

// MergeReviewRequest merges an approved review.
type MergeReviewRequest struct {
	ID       string
	Actor    string
	Strategy string
	Message  string
	Session  string
	// Approved: a person confirmed merging into a protected branch.
	Approved bool
}

// MergeReview merges an approved review's branch into its target.
func (s *Service) MergeReview(ctx context.Context, q MergeReviewRequest) (Review, vcs.Commit, error) {
	g, err := s.vcs()
	if err != nil {
		return Review{}, vcs.Commit{}, err
	}
	r, err := s.refresh(ctx, q.ID)
	if err != nil {
		return Review{}, vcs.Commit{}, err
	}
	if r.State != RvApproved {
		return r, vcs.Commit{}, fail(CodeNotApproved, 409, "review %s is %s: it merges once approved at its latest commit", r.ID, r.State)
	}
	p, err := g.MergePreview(ctx, r.RepoRoot, r.Target, r.Source)
	if err != nil {
		return r, vcs.Commit{}, err
	}
	if !p.Clean {
		return r, vcs.Commit{}, fail(CodeConflict, 409, "%s and %s conflict in %s; resolve with git_conflict_resolve in the worktree, then ask for review again", r.Source, r.Target, strings.Join(p.Conflicts, ", "))
	}
	changed, _ := s.ReviewDiff(ctx, r, nil)
	check := &vcs.PreMergeCheck{Repo: r.Repo, Target: r.Target, Source: r.Source, Actor: q.Actor, Author: r.Author, Preview: p, Changed: changed}
	if s.c != nil {
		res, err := vcs.PreMerge.Run(s.c, ctx, check, func(_ context.Context, c *vcs.PreMergeCheck) (*vcs.PreMergeCheck, error) { return c, nil })
		if err != nil {
			return r, vcs.Commit{}, err
		}
		check = res
	}
	if check != nil && check.Deny != "" {
		code := check.Code
		if code == "" {
			code = "GIT_MERGE_DENIED"
		}
		s.changed(vcs.Event{Type: "git/merge.blocked", Repo: r.Repo, ID: r.ID, Actor: q.Actor, Data: map[string]any{"code": code, "reason": check.Deny}})
		return r, vcs.Commit{}, fail(code, 409, "%s", check.Deny)
	}
	strategy := q.Strategy
	if strategy == "" {
		strategy = vcs.MergeNoFF
	}
	msg := q.Message
	if msg == "" {
		msg = fmt.Sprintf("Merge %s: %s\n\nReviewed in Crewbase (%s), approved by %s.", r.Source, r.Title, r.ID, strings.Join(s.approvers(r), ", "))
	}
	c, err := g.Merge(ctx, r.RepoRoot, vcs.MergeRequest{Target: r.Target, Source: r.Source, Strategy: strategy, Message: msg, Author: s.signature(q.Actor), Expect: r.Head})
	if err != nil {
		if strings.Contains(err.Error(), "since it was reviewed") {
			return r, vcs.Commit{}, fail(CodeStale, 409, "%v", err)
		}
		return r, vcs.Commit{}, err
	}
	s.forget(r.RepoRoot)
	out, err := s.mutate(r.ID, func(x *Review) error {
		x.State = RvMerged
		x.Merge = &MergeInfo{Commit: c.Hash, Strategy: strategy, By: q.Actor, At: time.Now().UTC()}
		return nil
	})
	if err != nil {
		return r, c, err
	}
	s.changed(vcs.Event{Type: "git/merged", Repo: r.Repo, ID: r.ID, Actor: q.Actor, Session: q.Session, Data: map[string]any{"commit": c.Hash, "strategy": strategy, "target": r.Target, "source": r.Source}})
	return out, c, nil
}

func (s *Service) approvers(r Review) []string {
	var out []string
	for _, v := range r.Verdicts {
		if v.Decision == VApprove && v.Commit == r.Head && !slices.Contains(out, s.name(v.Reviewer)) {
			out = append(out, s.name(v.Reviewer))
		}
	}
	return out
}

// ---------------------------------------------------------------- rounds

// askReviewers hands the review to the agents named on it.
func (s *Service) askReviewers(ctx context.Context, r Review) {
	if !s.st.settings().AutoReview {
		return
	}
	stat := s.diffStat(ctx, r)
	for _, rv := range r.Reviewers {
		id, ok := actorAgent(rv)
		if !ok {
			continue
		}
		a, ok := s.agents.Get(id)
		if !ok {
			continue
		}
		text := fmt.Sprintf("[Code review request %s] %s asks you to review “%s”: %s → %s, %s (round %d).\n\n"+
			"Read it with git_diff(review: %q). Leave line comments and a verdict with one git_review_submit(review: %q, decision: approve | request_changes, comments: [{path, line, severity: blocker|major|minor|nit, body}]). "+
			"Mark what must change as blocker or major. You review only: do not change the files or merge it.",
			r.ID, s.name(r.Author), r.Title, r.Source, r.Target, stat, r.Round, r.ID, r.ID)
		if r.Body != "" {
			text += "\n\nThe author says: " + r.Body
		}
		s.hand(a, a.Spec().Session, text, "git-review:"+r.ID, s.name(r.Author))
	}
}

// askFix hands the requested changes back to the author's side for another
// round, until the review's rounds are used up.
func (s *Service) askFix(ctx context.Context, r Review) {
	if r.Round >= r.MaxRounds {
		s.changed(vcs.Event{Type: "git/review.rounds-exhausted", Repo: r.Repo, ID: r.ID, Data: map[string]any{"round": r.Round, "max": r.MaxRounds}})
		return
	}
	who, sid := s.fixer(r)
	if who == nil {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[Code review %s] Changes were requested on “%s” (%s → %s), round %d of %d:\n", r.ID, r.Title, r.Source, r.Target, r.Round, r.MaxRounds)
	for _, v := range r.Verdicts {
		if v.Round == r.Round && v.Decision == VRequestChanges && v.Body != "" {
			fmt.Fprintf(&b, "\n%s: %s\n", s.name(v.Reviewer), v.Body)
		}
	}
	for _, c := range r.Comments {
		if c.Resolved || c.Severity == SevNote && c.Round != r.Round {
			continue
		}
		at := ""
		if c.Path != "" {
			at = c.Path
			if c.Line > 0 {
				at += fmt.Sprintf(":%d", c.Line)
			}
			at += " — "
		}
		fmt.Fprintf(&b, "- [%s] %s%s (%s)\n", c.Severity, at, c.Body, s.name(c.Author))
	}
	where := ""
	if r.Worktree != "" {
		if w, ok := s.Worktree(r.Worktree); ok && w.State == WTActive {
			where = fmt.Sprintf(" The branch is checked out in worktree %s (%s): give the fix to a sub-agent with delegate(task, worktree: %q), or use the git tools with worktree: %q.", w.ID, w.Path, w.ID, w.ID)
		}
	}
	fmt.Fprintf(&b, "\nFix them on %s.%s Commit with git_commit, then call git_review_open(review: %q) so the reviewers look again.", r.Source, where, r.ID)
	s.hand(who, sid, b.String(), "git-fix:"+r.ID, "code review")
}

// fixer is the agent that fixes a review: its author, or (a sub-agent
// that is gone) the agent it worked for.
func (s *Service) fixer(r Review) (*agent.Agent, string) {
	for _, actor := range []string{r.Author, r.Origin} {
		id, ok := actorAgent(actor)
		if !ok {
			continue
		}
		a, ok := s.agents.Get(id)
		if !ok || a.Spec().Ephemeral {
			continue
		}
		sid := a.Spec().Session
		if r.Session != "" {
			if ss, err := s.agents.Sessions().Get(r.Session); err == nil && slices.Contains(ss.Meta().Agents, id) {
				sid = r.Session
			}
		}
		return a, sid
	}
	return nil, ""
}

func (s *Service) tellApproved(ctx context.Context, r Review) {
	who, sid := s.fixer(r)
	if who == nil {
		return
	}
	text := fmt.Sprintf("[Code review %s] “%s” was approved by %s. Merge it with git_merge(review: %q) when you are ready (merging into %s may ask the person).",
		r.ID, r.Title, strings.Join(s.approvers(r), ", "), r.ID, r.Target)
	s.hand(who, sid, text, "git-approved:"+r.ID, "code review")
}

func (s *Service) hand(a *agent.Agent, session, text, tag, from string) {
	if session == "" {
		return
	}
	in := agent.Input{Session: session, Text: text, From: from, Tag: tag, Queue: true}
	enq := s.enqueue
	if enq == nil {
		enq = func(a *agent.Agent, in agent.Input) error { _, err := a.Enqueue(in); return err }
	}
	if err := enq(a, in); err != nil && s.c != nil {
		s.c.Log().Warn("git: could not hand over", "agent", a.ID(), "tag", tag, "err", err)
	}
}

func (s *Service) diffStat(ctx context.Context, r Review) string {
	fs, err := s.ReviewDiff(ctx, r, nil)
	if err != nil {
		return "?"
	}
	add, del := 0, 0
	for _, f := range fs {
		add += f.Added
		del += f.Removed
	}
	return fmt.Sprintf("%d file(s), +%d −%d", len(fs), add, del)
}
