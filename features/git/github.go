package git

// A small GitHub REST v3 client (github.com and GitHub Enterprise Server),
// only what pull requests need. Requests go through netguard: public
// addresses only, unless the admin allowed a private range (an internal
// GHE) in Settings › Git.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/internal/netguard"
	"github.com/hedeqiang/crewbase/providers/gitcli"
)

type ghClient struct {
	base  string // https://api.github.com | https://<host>/api/v3
	token string
	http  *http.Client
	repo  gitcli.Remote
}

// GHError is an error answer from GitHub.
type GHError struct {
	Status int
	Msg    string
}

func (e *GHError) Error() string { return fmt.Sprintf("GitHub %d: %s", e.Status, e.Msg) }

// ghUser is a user (or bot) on GitHub.
type ghUser struct {
	Login string `json:"login"`
	Type  string `json:"type,omitempty"`
}

type ghRef struct {
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
	Repo *struct {
		FullName string `json:"full_name"`
	} `json:"repo"`
}

// ghPR is a pull request as GitHub returns it.
type ghPR struct {
	Number         int       `json:"number"`
	Title          string    `json:"title"`
	Body           string    `json:"body"`
	State          string    `json:"state"` // open | closed
	Draft          bool      `json:"draft"`
	Merged         bool      `json:"merged"`
	Mergeable      *bool     `json:"mergeable"`
	MergeableState string    `json:"mergeable_state"`
	HTMLURL        string    `json:"html_url"`
	User           ghUser    `json:"user"`
	Head           ghRef     `json:"head"`
	Base           ghRef     `json:"base"`
	Additions      int       `json:"additions"`
	Deletions      int       `json:"deletions"`
	ChangedFiles   int       `json:"changed_files"`
	Comments       int       `json:"comments"`
	ReviewComments int       `json:"review_comments"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	MergeCommitSHA string    `json:"merge_commit_sha"`
}

// Fork reports whether the PR comes from another repository.
func (p ghPR) Fork() bool {
	return p.Head.Repo == nil || p.Base.Repo == nil || !strings.EqualFold(p.Head.Repo.FullName, p.Base.Repo.FullName)
}

type ghCheckRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`     // queued | in_progress | completed
	Conclusion string `json:"conclusion"` // success | failure | timed_out | cancelled | skipped | neutral | action_required
	HTMLURL    string `json:"html_url"`
	Output     struct {
		Title       string `json:"title"`
		Summary     string `json:"summary"`
		Annotations int    `json:"annotations_count"`
	} `json:"output"`
}

type ghAnnotation struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Level     string `json:"annotation_level"` // notice | warning | failure
	Message   string `json:"message"`
	Title     string `json:"title"`
}

type ghStatus struct {
	State       string `json:"state"` // success | pending | failure | error
	Context     string `json:"context"`
	Description string `json:"description"`
	TargetURL   string `json:"target_url"`
}

type ghReview struct {
	ID          int64     `json:"id"`
	User        ghUser    `json:"user"`
	State       string    `json:"state"` // APPROVED | CHANGES_REQUESTED | COMMENTED | DISMISSED
	Body        string    `json:"body"`
	CommitID    string    `json:"commit_id"`
	SubmittedAt time.Time `json:"submitted_at"`
	HTMLURL     string    `json:"html_url"`
}

type ghReviewComment struct {
	ID       int64     `json:"id"`
	ReviewID int64     `json:"pull_request_review_id"`
	User     ghUser    `json:"user"`
	Path     string    `json:"path"`
	Line     int       `json:"line"`
	Side     string    `json:"side"` // LEFT | RIGHT
	Body     string    `json:"body"`
	Created  time.Time `json:"created_at"`
	HTMLURL  string    `json:"html_url"`
}

type ghFile struct {
	Filename  string `json:"filename"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// apiBase is the REST root for a host (an explicit API URL wins).
func apiBase(host, explicit string) string {
	if explicit != "" {
		return strings.TrimRight(explicit, "/")
	}
	if host == "github.com" {
		return "https://api.github.com"
	}
	return "https://" + host + "/api/v3"
}

func (c *ghClient) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "Crewbase")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
			Errors  []struct {
				Message string `json:"message"`
				Field   string `json:"field"`
				Code    string `json:"code"`
			} `json:"errors"`
		}
		_ = json.Unmarshal(b, &e)
		msg := e.Message
		for _, x := range e.Errors {
			if x.Message != "" {
				msg += "; " + x.Message
			} else if x.Field != "" {
				msg += "; " + x.Field + " " + x.Code
			}
		}
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		if c.token != "" {
			msg = strings.ReplaceAll(msg, c.token, "***")
		}
		return &GHError{Status: resp.StatusCode, Msg: msg}
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

func (c *ghClient) repoPath() string {
	return "/repos/" + url.PathEscape(c.repo.Owner) + "/" + url.PathEscape(c.repo.Repo)
}

func (c *ghClient) me(ctx context.Context) (string, error) {
	if c.token == "" {
		return "", errors.New("no token")
	}
	var u ghUser
	err := c.do(ctx, "GET", "/user", nil, &u)
	return u.Login, err
}

func (c *ghClient) listPRs(ctx context.Context, state string, n int) ([]ghPR, error) {
	if state == "" {
		state = "open"
	}
	if n <= 0 || n > 100 {
		n = 30
	}
	var out []ghPR
	err := c.do(ctx, "GET", fmt.Sprintf("%s/pulls?state=%s&per_page=%d&sort=updated&direction=desc", c.repoPath(), url.QueryEscape(state), n), nil, &out)
	return out, err
}

func (c *ghClient) findPR(ctx context.Context, headBranch string) (*ghPR, error) {
	var out []ghPR
	err := c.do(ctx, "GET", fmt.Sprintf("%s/pulls?state=open&head=%s", c.repoPath(), url.QueryEscape(c.repo.Owner+":"+headBranch)), nil, &out)
	if err != nil || len(out) == 0 {
		return nil, err
	}
	return &out[0], nil
}

func (c *ghClient) getPR(ctx context.Context, n int) (ghPR, error) {
	var p ghPR
	err := c.do(ctx, "GET", fmt.Sprintf("%s/pulls/%d", c.repoPath(), n), nil, &p)
	return p, err
}

// getPRMergeable reads a PR until GitHub has worked out whether it merges
// (mergeable is null at first), for at most a few seconds.
func (c *ghClient) getPRMergeable(ctx context.Context, n int, tries int, wait time.Duration) (ghPR, error) {
	var p ghPR
	var err error
	for i := 0; i < max(tries, 1); i++ {
		if p, err = c.getPR(ctx, n); err != nil || p.Mergeable != nil || p.State != "open" {
			return p, err
		}
		select {
		case <-ctx.Done():
			return p, ctx.Err()
		case <-time.After(wait):
		}
	}
	return p, nil
}

func (c *ghClient) prFiles(ctx context.Context, n int) ([]ghFile, error) {
	var out []ghFile
	err := c.do(ctx, "GET", fmt.Sprintf("%s/pulls/%d/files?per_page=100", c.repoPath(), n), nil, &out)
	return out, err
}

func (c *ghClient) createPR(ctx context.Context, title, head, base, body string, draft bool) (ghPR, error) {
	var p ghPR
	err := c.do(ctx, "POST", c.repoPath()+"/pulls", map[string]any{"title": title, "head": head, "base": base, "body": body, "draft": draft}, &p)
	return p, err
}

func (c *ghClient) updatePR(ctx context.Context, n int, title, body string) (ghPR, error) {
	var p ghPR
	in := map[string]any{"body": body}
	if title != "" {
		in["title"] = title
	}
	err := c.do(ctx, "PATCH", fmt.Sprintf("%s/pulls/%d", c.repoPath(), n), in, &p)
	return p, err
}

func (c *ghClient) reviews(ctx context.Context, n int) ([]ghReview, error) {
	var out []ghReview
	err := c.do(ctx, "GET", fmt.Sprintf("%s/pulls/%d/reviews?per_page=100", c.repoPath(), n), nil, &out)
	return out, err
}

func (c *ghClient) reviewComments(ctx context.Context, n int) ([]ghReviewComment, error) {
	var out []ghReviewComment
	err := c.do(ctx, "GET", fmt.Sprintf("%s/pulls/%d/comments?per_page=100", c.repoPath(), n), nil, &out)
	return out, err
}

type ghReviewIn struct {
	CommitID string              `json:"commit_id,omitempty"`
	Event    string              `json:"event"` // APPROVE | REQUEST_CHANGES | COMMENT
	Body     string              `json:"body,omitempty"`
	Comments []ghReviewCommentIn `json:"comments,omitempty"`
}

type ghReviewCommentIn struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Side string `json:"side"` // RIGHT (new) | LEFT (old)
	Body string `json:"body"`
}

func (c *ghClient) createReview(ctx context.Context, n int, in ghReviewIn) (ghReview, error) {
	var r ghReview
	err := c.do(ctx, "POST", fmt.Sprintf("%s/pulls/%d/reviews", c.repoPath(), n), in, &r)
	return r, err
}

func (c *ghClient) checkRuns(ctx context.Context, sha string) ([]ghCheckRun, error) {
	var out struct {
		CheckRuns []ghCheckRun `json:"check_runs"`
	}
	err := c.do(ctx, "GET", fmt.Sprintf("%s/commits/%s/check-runs?per_page=100", c.repoPath(), url.PathEscape(sha)), nil, &out)
	return out.CheckRuns, err
}

func (c *ghClient) annotations(ctx context.Context, id int64) ([]ghAnnotation, error) {
	var out []ghAnnotation
	err := c.do(ctx, "GET", c.repoPath()+"/check-runs/"+strconv.FormatInt(id, 10)+"/annotations?per_page=50", nil, &out)
	return out, err
}

func (c *ghClient) statuses(ctx context.Context, sha string) (string, []ghStatus, error) {
	var out struct {
		State    string     `json:"state"`
		Statuses []ghStatus `json:"statuses"`
	}
	err := c.do(ctx, "GET", fmt.Sprintf("%s/commits/%s/status", c.repoPath(), url.PathEscape(sha)), nil, &out)
	return out.State, out.Statuses, err
}

func (c *ghClient) merge(ctx context.Context, n int, method, sha, title, msg string) (string, error) {
	in := map[string]any{"merge_method": method, "sha": sha}
	if title != "" {
		in["commit_title"] = title
	}
	if msg != "" {
		in["commit_message"] = msg
	}
	var out struct {
		SHA    string `json:"sha"`
		Merged bool   `json:"merged"`
	}
	if err := c.do(ctx, "PUT", fmt.Sprintf("%s/pulls/%d/merge", c.repoPath(), n), in, &out); err != nil {
		return "", err
	}
	return out.SHA, nil
}

// ---------------------------------------------------------------- client for a folder

// Origin is where a repository's origin remote points, as the 代码 panel
// needs it: a GitHub repository gets pull requests, anything else only
// commits and pushes.
type Origin struct {
	Host   string `json:"host"`
	Repo   string `json:"repo,omitempty"` // owner/name
	GitHub bool   `json:"github"`
	// Token: a credential for the host is set up (Settings › Git).
	Token bool `json:"token"`
}

// originOf is dir's origin remote (nil when there is none).
func (s *Service) originOf(ctx context.Context, dir string) *Origin {
	g, err := s.vcs()
	if err != nil {
		return nil
	}
	u, err := g.RemoteURL(ctx, dir, "origin")
	if err != nil || strings.TrimSpace(u) == "" {
		return nil
	}
	o := &Origin{Host: gitcli.HostOf(u)}
	rm, ok := gitcli.ParseRemote(u)
	if ok {
		o.Host, o.Repo = rm.Host, rm.Slug()
	}
	for _, r := range s.st.settings().Remotes {
		if o.Host != "" && strings.EqualFold(r.Host, o.Host) {
			o.Token = r.Credential != ""
			// a host with an API root set is a GitHub Enterprise Server
			o.GitHub = ok && r.API != ""
		}
	}
	if ok && (o.Host == "github.com" || strings.Contains(o.Host, "github")) {
		o.GitHub = true
	}
	return o
}

// github is the GitHub client for the repository dir lies in (its origin).
func (s *Service) github(ctx context.Context, dir string) (*ghClient, error) {
	g, err := s.vcs()
	if err != nil {
		return nil, err
	}
	u, err := g.RemoteURL(ctx, dir, "origin")
	if err != nil {
		return nil, fmt.Errorf("the repository has no origin remote: %w", err)
	}
	rm, ok := gitcli.ParseRemote(u)
	if !ok {
		return nil, fmt.Errorf("origin (%s) is not a GitHub repository address", u)
	}
	c := &ghClient{repo: rm, base: apiBase(rm.Host, "")}
	set := s.st.settings()
	for _, r := range set.Remotes {
		if !strings.EqualFold(r.Host, rm.Host) {
			continue
		}
		c.base = apiBase(rm.Host, r.API)
		if r.Credential != "" {
			cs, ok := s.creds()
			if !ok {
				return nil, errors.New("the vault is not available")
			}
			tok, err := cs.Resolve(ctx, r.Credential)
			if err != nil {
				return nil, fmt.Errorf("credential %s for %s: %w", r.Credential, rm.Host, err)
			}
			c.token = tok
		}
		break
	}
	allow, _ := netguard.ParsePrefixes(set.AllowPrivate)
	if s.httpFor != nil {
		c.http = s.httpFor(allow)
	} else {
		c.http = (&netguard.Guard{Allow: allow}).Client(20*time.Second, 3)
	}
	return c, nil
}
