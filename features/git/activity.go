package git

// What happened on a GitHub repository since a moment (new issues, new
// review comments on its pull requests) and a commit's checks, through the
// same local gh as the pull request code. 夜间值班 polls with these when no
// GitHub webhook brings the events.

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// IssueSummary is an issue (never a pull request).
type IssueSummary struct {
	Number  int       `json:"number"`
	Title   string    `json:"title"`
	Body    string    `json:"body,omitempty"`
	URL     string    `json:"url"`
	Author  string    `json:"author"`
	Created time.Time `json:"created"`
}

// CommentSummary is a review comment on a pull request's code.
type CommentSummary struct {
	ID      int64     `json:"id"`
	PR      int       `json:"pr"`
	Author  string    `json:"author"`
	Path    string    `json:"path"`
	Line    int       `json:"line"`
	Body    string    `json:"body"`
	URL     string    `json:"url"`
	Created time.Time `json:"created"`
}

// Activity is what was opened or said on a repository since a moment.
type Activity struct {
	Repo     string           `json:"repo"` // owner/name
	Me       string           `json:"me,omitempty"`
	Issues   []IssueSummary   `json:"issues"`
	Comments []CommentSummary `json:"comments"`
}

type ghIssue struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	HTMLURL     string    `json:"html_url"`
	User        ghUser    `json:"user"`
	CreatedAt   time.Time `json:"created_at"`
	PullRequest *struct{} `json:"pull_request"`
}

type ghRepoComment struct {
	ghReviewComment
	PRURL string `json:"pull_request_url"`
}

// RecentActivity lists the issues opened and the review comments written
// on dir's GitHub repository after since (the gh user's own comments left
// out).
func (s *Service) RecentActivity(ctx context.Context, dir string, since time.Time) (Activity, error) {
	c, err := s.github(ctx, dir)
	if err != nil {
		return Activity{}, err
	}
	out := Activity{Repo: c.repo.Slug(), Issues: []IssueSummary{}, Comments: []CommentSummary{}}
	out.Me, _ = c.me(ctx)
	q := url.QueryEscape(since.UTC().Format(time.RFC3339))
	var is []ghIssue
	if err := c.do(ctx, "GET", fmt.Sprintf("%s/issues?state=open&sort=created&direction=desc&per_page=30&since=%s", c.repoPath(), q), nil, &is); err != nil {
		return out, err
	}
	for _, i := range is {
		// since= is about updates: keep what was opened after it
		if i.PullRequest != nil || !i.CreatedAt.After(since) {
			continue
		}
		out.Issues = append(out.Issues, IssueSummary{Number: i.Number, Title: i.Title, Body: i.Body, URL: i.HTMLURL, Author: i.User.Login, Created: i.CreatedAt})
	}
	var cs []ghRepoComment
	if err := c.do(ctx, "GET", fmt.Sprintf("%s/pulls/comments?sort=created&direction=desc&per_page=50&since=%s", c.repoPath(), q), nil, &cs); err != nil {
		return out, err
	}
	for _, x := range cs {
		if !x.Created.After(since) || (out.Me != "" && strings.EqualFold(x.User.Login, out.Me)) {
			continue
		}
		n := 0
		if i := strings.LastIndex(x.PRURL, "/"); i >= 0 {
			_, _ = fmt.Sscan(x.PRURL[i+1:], &n)
		}
		out.Comments = append(out.Comments, CommentSummary{ID: x.ID, PR: n, Author: x.User.Login, Path: x.Path, Line: x.Line, Body: x.Body, URL: x.HTMLURL, Created: x.Created})
	}
	return out, nil
}

// PRChecks are the checks of a commit on dir's GitHub repository.
func (s *Service) PRChecks(ctx context.Context, dir, sha string) (Checks, error) {
	c, err := s.github(ctx, dir)
	if err != nil {
		return Checks{}, err
	}
	return s.checks(ctx, c, sha)
}

// Origin is dir's origin remote as the 代码 panel sees it (nil: none).
func (s *Service) Origin(ctx context.Context, dir string) *Origin { return s.originOf(ctx, dir) }
