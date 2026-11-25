package git

// A small GitHub REST v3 client (github.com and GitHub Enterprise Server),
// only what pull requests need. Every request goes through the local GitHub
// CLI (gh api): its login (gh auth login), its hosts and its network setup,
// so no token is kept in Crewbase for it. Tests swap the transport
// (Service.ghCallFor) for a fake server.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/internal/netproxy"
	"github.com/hedeqiang/crewbase/providers/gitcli"
)

// ghCall answers one REST request (path from the API root): the HTTP
// status and the body.
type ghCall func(ctx context.Context, method, path string, body []byte) (int, []byte, error)

type ghClient struct {
	host string
	repo gitcli.Remote
	call ghCall
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

func (c *ghClient) do(ctx context.Context, method, path string, in, out any) error {
	var body []byte
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = b
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	status, b, err := c.call(ctx, method, path, body)
	if err != nil {
		return err
	}
	if status >= 300 {
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
			msg = http.StatusText(status)
		}
		return &GHError{Status: status, Msg: msg}
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

// ErrNoGH: the GitHub CLI is not installed on this machine.
var ErrNoGH = errors.New("the GitHub CLI (gh) is not installed: install it (https://cli.github.com) and run gh auth login")

// ghLoginError: gh is not signed in to the host.
type ghLoginError struct{ host string }

func (e *ghLoginError) Error() string {
	return fmt.Sprintf("gh is not signed in to %s: run gh auth login --hostname %s", e.host, e.host)
}

// ghCLI is the transport through the local gh: gh api --include, the
// status line and headers read off the top of its output.
func ghCLI(host string) ghCall {
	return func(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
		bin, err := exec.LookPath("gh")
		if err != nil {
			return 0, nil, ErrNoGH
		}
		args := []string{"api", "--hostname", host, "--method", method, "--include",
			"-H", "Accept: application/vnd.github+json", "-H", "X-GitHub-Api-Version: 2022-11-28", strings.TrimPrefix(path, "/")}
		if body != nil {
			args = append(args, "--input", "-")
		}
		cmd := exec.CommandContext(ctx, bin, args...)
		// its own login and config; the proxy Crewbase is set to (Settings › 网络代理)
		cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "NO_COLOR=1")
		cmd.Env = append(cmd.Env, netproxy.Env()...)
		if body != nil {
			cmd.Stdin = bytes.NewReader(body)
		}
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		runErr := cmd.Run()
		status, rest, ok := parseIncluded(stdout.Bytes())
		if !ok {
			msg := strings.TrimSpace(stderr.String())
			if strings.Contains(msg, "gh auth login") || strings.Contains(msg, "not logged") || strings.Contains(msg, "authentication") {
				return 0, nil, &ghLoginError{host: host}
			}
			if ctx.Err() != nil {
				return 0, nil, ctx.Err()
			}
			if msg == "" && runErr != nil {
				msg = runErr.Error()
			}
			return 0, nil, fmt.Errorf("gh api: %s", msg)
		}
		return status, rest, nil
	}
}

// parseIncluded splits gh api --include output: "HTTP/2.0 404 Not Found",
// headers, a blank line, the body.
func parseIncluded(out []byte) (int, []byte, bool) {
	r := bufio.NewReader(bytes.NewReader(out))
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return 0, nil, false
	}
	f := strings.Fields(line)
	if len(f) < 2 || !strings.HasPrefix(f[0], "HTTP/") {
		return 0, nil, false
	}
	status, err := strconv.Atoi(f[1])
	if err != nil {
		return 0, nil, false
	}
	for {
		h, err := r.ReadString('\n')
		if strings.TrimSpace(h) == "" || err != nil {
			break
		}
	}
	rest, _ := io.ReadAll(r)
	return status, rest, true
}

// ghReach is whether the local gh can reach a host: "ok", "missing" (not
// installed) or "login" (not signed in to it).
func ghReach(ctx context.Context, host string) string {
	ghStatusCache.Lock()
	if e, ok := ghStatusCache.m[host]; ok && time.Since(e.at) < time.Minute {
		ghStatusCache.Unlock()
		return e.st
	}
	ghStatusCache.Unlock()
	st := "ok"
	if bin, err := exec.LookPath("gh"); err != nil {
		st = "missing"
	} else {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(cctx, bin, "auth", "status", "--hostname", host)
		cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "NO_COLOR=1")
		if err := cmd.Run(); err != nil {
			st = "login"
		}
	}
	ghStatusCache.Lock()
	if ghStatusCache.m == nil {
		ghStatusCache.m = map[string]ghStatusEntry{}
	}
	ghStatusCache.m[host] = ghStatusEntry{st: st, at: time.Now()}
	ghStatusCache.Unlock()
	return st
}

type ghStatusEntry struct {
	st string
	at time.Time
}

var ghStatusCache struct {
	sync.Mutex
	m map[string]ghStatusEntry
}

func (c *ghClient) repoPath() string {
	return "/repos/" + url.PathEscape(c.repo.Owner) + "/" + url.PathEscape(c.repo.Repo)
}

func (c *ghClient) me(ctx context.Context) (string, error) {
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

// Origin is a folder's origin remote, as much of it as the 代码 panel
// needs: a GitHub repository gets its pull requests followed, anything
// else only commits and pushes.
type Origin struct {
	Host   string `json:"host"`
	Repo   string `json:"repo,omitempty"` // owner/name
	GitHub bool   `json:"github"`
	// GH: whether the local GitHub CLI reaches the host: ok, missing (not
	// installed), login (not signed in to it). GitHub repositories only.
	GH string `json:"gh,omitempty"`
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
	if !ok {
		return o
	}
	o.Host, o.Repo = rm.Host, rm.Slug()
	st := s.reach(ctx, rm.Host)
	// github.com, a host named like one, or a GitHub Enterprise Server gh
	// is signed in to
	o.GitHub = rm.Host == "github.com" || strings.Contains(rm.Host, "github") || st == "ok"
	if o.GitHub {
		o.GH = st
	}
	return o
}

// reach is whether the local gh reaches host (tests swap it).
func (s *Service) reach(ctx context.Context, host string) string {
	if s.ghReachFor != nil {
		return s.ghReachFor(host)
	}
	return ghReach(ctx, host)
}

// github is the GitHub client for the repository dir lies in (its origin),
// through the local gh.
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
	c := &ghClient{host: rm.Host, repo: rm}
	if s.ghCallFor != nil {
		c.call = s.ghCallFor(rm.Host)
	} else {
		c.call = ghCLI(rm.Host)
	}
	return c, nil
}
