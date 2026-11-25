// Package gitcli provides the `vcs` capability with the git binary
// (@crewbase/vcs-gitcli).
//
// Every call is argv (exec, never a shell) with settings that keep a
// repository's own content from running code in Crewbase's process:
// hooks point at an empty directory, fsmonitor, external diff and
// textconv, signing and file:// transports are off. Settings a repository
// could still use to run programs (filter and merge drivers) live in its
// .git/config, which agents may not write (agent.ProtectedPaths). Reads
// take no optional locks; writes to one repository wait for each other.
package gitcli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/vcs"
	"github.com/hedeqiang/crewbase/internal/netproxy"
	"github.com/hedeqiang/crewbase/kernel"
)

// Git implements vcs.Provider.
type Git struct {
	bin   string
	hooks string // an empty directory hooks point at
	mu    sync.Mutex
	locks map[string]*sync.Mutex // per repository (common dir)
	// Timeout bounds one command (default 2 minutes; push 5).
	Timeout time.Duration
}

// New finds git on PATH (ErrUnavailable without it).
func New() (*Git, error) {
	bin, err := exec.LookPath("git")
	if err != nil {
		return nil, vcs.ErrUnavailable
	}
	hooks, err := os.MkdirTemp("", "crewbase-nohooks-")
	if err != nil {
		return nil, err
	}
	return &Git{bin: bin, hooks: hooks, locks: map[string]*sync.Mutex{}, Timeout: 2 * time.Minute}, nil
}

// Close removes the empty hooks directory.
func (g *Git) Close() { _ = os.RemoveAll(g.hooks) }

// safe are the settings every call carries.
func (g *Git) safe() []string {
	return []string{
		"-c", "core.hooksPath=" + g.hooks,
		"-c", "core.fsmonitor=false",
		"-c", "core.untrackedCache=false",
		"-c", "core.quotepath=off",
		"-c", "core.pager=cat",
		"-c", "core.editor=true",
		"-c", "color.ui=false",
		"-c", "commit.gpgsign=false",
		"-c", "tag.gpgsign=false",
		"-c", "protocol.file.allow=never",
		"-c", "protocol.ext.allow=never",
		"-c", "diff.external=",
		"-c", "advice.detachedHead=false",
		"-c", "init.defaultBranch=main",
	}
}

type opts struct {
	env   []string
	stdin []byte
	write bool // a write: wait for the repository's other writes
	lock  string
	// ok are exit codes that are not errors (merge-tree's 1 = conflicts)
	ok      []int
	timeout time.Duration
	secrets []string // scrubbed from output and errors
}

type result struct {
	out  []byte
	err  []byte
	code int
}

// run runs git in dir.
func (g *Git) run(ctx context.Context, dir string, o opts, args ...string) (result, error) {
	if o.write && o.lock != "" {
		l := g.lockFor(o.lock)
		l.Lock()
		defer l.Unlock()
	}
	t := o.timeout
	if t == 0 {
		t = g.Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, t)
	defer cancel()
	cmd := exec.CommandContext(ctx, g.bin, append(g.safe(), args...)...)
	cmd.Dir = dir
	cmd.Env = append(baseEnv(), o.env...)
	if !o.write {
		cmd.Env = append(cmd.Env, "GIT_OPTIONAL_LOCKS=0")
	}
	if o.stdin != nil {
		cmd.Stdin = bytes.NewReader(o.stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	r := result{out: stdout.Bytes(), err: stderr.Bytes()}
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			return r, fmt.Errorf("git %s: %w", first(args), err)
		}
		r.code = ee.ExitCode()
		for _, c := range o.ok {
			if c == r.code {
				return r, nil
			}
		}
		msg := strings.TrimSpace(scrub(string(r.err), o.secrets))
		if msg == "" {
			msg = strings.TrimSpace(scrub(string(r.out), o.secrets))
		}
		if ctx.Err() != nil {
			msg = "timed out"
		}
		return r, &Error{Args: first(args), Code: r.code, Msg: msg}
	}
	return r, nil
}

// Error is a git command that failed.
type Error struct {
	Args string
	Code int
	Msg  string
}

func (e *Error) Error() string { return fmt.Sprintf("git %s: %s", e.Args, e.Msg) }

func first(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// baseEnv is the process environment without what would change git's
// behaviour behind our back, plus a fixed locale and no prompts.
func baseEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "GIT_") || k == "LANG" || k == "LC_ALL" || k == "LANGUAGE" {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "LC_ALL=C", "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=", "GCM_INTERACTIVE=never")
	// Settings › 网络代理: fetch and push go the same way (last wins)
	return append(out, netproxy.Env()...)
}

func scrub(s string, secrets []string) string {
	for _, x := range secrets {
		if len(x) >= 4 {
			s = strings.ReplaceAll(s, x, "***")
		}
	}
	return s
}

func (g *Git) lockFor(key string) *sync.Mutex {
	g.mu.Lock()
	defer g.mu.Unlock()
	l := g.locks[key]
	if l == nil {
		l = &sync.Mutex{}
		g.locks[key] = l
	}
	return l
}

func lines(b []byte) []string {
	s := strings.TrimRight(string(b), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func trim(b []byte) string { return strings.TrimSpace(string(b)) }

// ---------------------------------------------------------------- repository

// Probe implements vcs.Provider.
func (g *Git) Probe(ctx context.Context, dir string) (vcs.RepoInfo, error) {
	r, err := g.run(ctx, dir, opts{}, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir", "--git-dir")
	if err != nil {
		var ge *Error
		if errors.As(err, &ge) && strings.Contains(ge.Msg, "not a git repository") {
			return vcs.RepoInfo{}, vcs.ErrNotRepo
		}
		if os.IsNotExist(errors.Unwrap(err)) {
			return vcs.RepoInfo{}, vcs.ErrNotRepo
		}
		return vcs.RepoInfo{}, err
	}
	ls := lines(r.out)
	if len(ls) < 3 {
		return vcs.RepoInfo{}, vcs.ErrNotRepo
	}
	info := vcs.RepoInfo{Root: real(ls[0]), CommonDir: real(ls[1]), GitDir: real(ls[2])}
	info.Linked = info.CommonDir != info.GitDir
	info.Main = filepath.Dir(info.CommonDir)
	if filepath.Base(info.CommonDir) != ".git" {
		info.Main = "" // a bare repository or a separate git dir
		if !info.Linked {
			info.Main = info.Root
		}
	}
	name := info.Main
	if name == "" {
		name = info.Root
	}
	info.Name = filepath.Base(name)
	h := sha256.Sum256([]byte(info.CommonDir))
	info.ID = info.Name + "-" + hex.EncodeToString(h[:])[:8]
	if b, err := g.run(ctx, dir, opts{}, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		info.Branch = trim(b.out)
	}
	if h, err := g.run(ctx, dir, opts{}, "rev-parse", "--verify", "--quiet", "HEAD"); err == nil {
		info.Head = trim(h.out)
	}
	return info, nil
}

func real(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// key is the lock of the repository dir belongs to.
func (g *Git) key(ctx context.Context, dir string) string {
	r, err := g.run(ctx, dir, opts{}, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return dir
	}
	return real(trim(r.out))
}

// Worktrees implements vcs.Provider.
func (g *Git) Worktrees(ctx context.Context, repo string) ([]vcs.Worktree, error) {
	r, err := g.run(ctx, repo, opts{}, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var out []vcs.Worktree
	var cur *vcs.Worktree
	for _, l := range lines(r.out) {
		k, v, _ := strings.Cut(l, " ")
		switch k {
		case "worktree":
			out = append(out, vcs.Worktree{Path: real(v), Main: len(out) == 0})
			cur = &out[len(out)-1]
		case "HEAD":
			if cur != nil {
				cur.Head = v
			}
		case "branch":
			if cur != nil {
				cur.Branch = strings.TrimPrefix(v, "refs/heads/")
			}
		case "detached":
			if cur != nil {
				cur.Detached = true
			}
		case "locked":
			if cur != nil {
				cur.Locked = true
			}
		case "prunable":
			if cur != nil {
				cur.Prunable = true
			}
		case "bare":
			if cur != nil {
				cur.Main = false
			}
		}
	}
	return out, nil
}

// AddWorktree implements vcs.Provider.
func (g *Git) AddWorktree(ctx context.Context, repo, path, branch, base string) error {
	if err := checkRef(branch); err != nil {
		return err
	}
	if base == "" {
		base = "HEAD"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	_, err := g.run(ctx, repo, opts{write: true, lock: g.key(ctx, repo)}, "worktree", "add", "--quiet", "-b", branch, "--", path, base)
	return err
}

// RemoveWorktree implements vcs.Provider.
func (g *Git) RemoveWorktree(ctx context.Context, repo, path string, force bool, deleteBranch string) error {
	key := g.key(ctx, repo)
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force", "--force") // also a locked one
	}
	if _, err := g.run(ctx, repo, opts{write: true, lock: key}, append(args, "--", path)...); err != nil {
		if _, serr := os.Stat(path); !os.IsNotExist(serr) {
			return err
		}
		// already gone from disk: forget it
		_, _ = g.run(ctx, repo, opts{write: true, lock: key}, "worktree", "prune")
	}
	if deleteBranch != "" {
		if err := checkRef(deleteBranch); err != nil {
			return err
		}
		if _, err := g.run(ctx, repo, opts{write: true, lock: key}, "branch", "-D", "--", deleteBranch); err != nil {
			var ge *Error
			if !errors.As(err, &ge) || !strings.Contains(ge.Msg, "not found") {
				return err
			}
		}
	}
	return nil
}

// checkRef refuses names git would read as options or that are not valid
// branch names.
func checkRef(name string) error {
	if name == "" || strings.HasPrefix(name, "-") || strings.ContainsAny(name, " ~^:?*[\\\x7f") || strings.Contains(name, "..") ||
		strings.HasSuffix(name, ".lock") || strings.HasSuffix(name, "/") || strings.HasPrefix(name, "/") || strings.Contains(name, "@{") {
		return fmt.Errorf("%q is not a valid branch name", name)
	}
	for _, r := range name {
		if r < 0x20 {
			return fmt.Errorf("%q is not a valid branch name", name)
		}
	}
	return nil
}

// checkRev allows branch names, commit ids and HEAD-relative revisions,
// never an option.
func checkRev(rev string) error {
	if rev == "" || strings.HasPrefix(rev, "-") || strings.ContainsAny(rev, " \n\t\x00") {
		return fmt.Errorf("%q is not a revision", rev)
	}
	return nil
}

// Resolve implements vcs.Provider.
func (g *Git) Resolve(ctx context.Context, dir, rev string) (string, error) {
	if err := checkRev(rev); err != nil {
		return "", err
	}
	r, err := g.run(ctx, dir, opts{}, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("unknown revision %q", rev)
	}
	return trim(r.out), nil
}

// ---------------------------------------------------------------- status

// Status implements vcs.Provider.
func (g *Git) Status(ctx context.Context, dir string) (vcs.Status, error) {
	r, err := g.run(ctx, dir, opts{}, "status", "--porcelain=v2", "--branch", "-z", "--untracked-files=all", "--no-renames")
	if err != nil {
		return vcs.Status{}, err
	}
	st := vcs.Status{Files: []vcs.FileStatus{}}
	recs := strings.Split(strings.TrimRight(string(r.out), "\x00"), "\x00")
	for _, rec := range recs {
		if rec == "" {
			continue
		}
		switch rec[0] {
		case '#':
			f := strings.Fields(rec)
			if len(f) < 3 {
				continue
			}
			switch f[1] {
			case "branch.oid":
				if f[2] != "(initial)" {
					st.Head = f[2]
				}
			case "branch.head":
				if f[2] != "(detached)" {
					st.Branch = f[2]
				}
			case "branch.upstream":
				st.Upstream = f[2]
			case "branch.ab":
				if len(f) >= 4 {
					st.Ahead, _ = strconv.Atoi(strings.TrimPrefix(f[2], "+"))
					st.Behind, _ = strconv.Atoi(strings.TrimPrefix(f[3], "-"))
				}
			}
		case '1':
			// 1 XY sub mH mI mW hH hI path
			f := strings.SplitN(rec, " ", 9)
			if len(f) == 9 {
				st.Files = append(st.Files, fileStatus(f[1], f[8], ""))
			}
		case 'u':
			// u XY sub m1 m2 m3 mW h1 h2 h3 path
			f := strings.SplitN(rec, " ", 11)
			if len(f) == 11 {
				fs := fileStatus(f[1], f[10], "")
				fs.Status = vcs.StConflicted
				st.Files = append(st.Files, fs)
			}
		case '?':
			st.Files = append(st.Files, vcs.FileStatus{Path: rec[2:], Index: "?", Work: "?", Status: vcs.StUntracked, Unstaged: true})
		}
	}
	st.Clean = len(st.Files) == 0
	if gd, err := g.run(ctx, dir, opts{}, "rev-parse", "--path-format=absolute", "--git-dir"); err == nil {
		if _, err := os.Stat(filepath.Join(trim(gd.out), "MERGE_HEAD")); err == nil {
			st.Merging = true
		}
	}
	return st, nil
}

func fileStatus(xy, path, orig string) vcs.FileStatus {
	x, y := string(xy[0]), string(xy[1])
	fs := vcs.FileStatus{Path: path, Orig: orig, Index: x, Work: y, Staged: x != ".", Unstaged: y != "."}
	c := y
	if c == "." {
		c = x
	}
	switch c {
	case "A":
		fs.Status = vcs.StAdded
	case "D":
		fs.Status = vcs.StDeleted
	case "R", "C":
		fs.Status = vcs.StRenamed
	case "T":
		fs.Status = vcs.StTypeChange
	default:
		fs.Status = vcs.StModified
	}
	if x == "A" {
		fs.Status = vcs.StAdded
	}
	return fs
}

// ---------------------------------------------------------------- staging

// Stage implements vcs.Provider.
func (g *Git) Stage(ctx context.Context, dir string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	_, err := g.run(ctx, dir, opts{write: true, lock: g.key(ctx, dir)}, append([]string{"add", "--all", "--"}, paths...)...)
	return err
}

// Unstage implements vcs.Provider.
func (g *Git) Unstage(ctx context.Context, dir string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	if _, err := g.run(ctx, dir, opts{}, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		// no commit yet: take them out of the index
		_, err := g.run(ctx, dir, opts{write: true, lock: g.key(ctx, dir)}, append([]string{"rm", "--cached", "--quiet", "-r", "--"}, paths...)...)
		return err
	}
	_, err := g.run(ctx, dir, opts{write: true, lock: g.key(ctx, dir)}, append([]string{"restore", "--staged", "--"}, paths...)...)
	return err
}

// Discard implements vcs.Provider: tracked files back as in the index,
// untracked ones removed.
func (g *Git) Discard(ctx context.Context, dir string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	st, err := g.Status(ctx, dir)
	if err != nil {
		return err
	}
	untracked := map[string]bool{}
	for _, f := range st.Files {
		if f.Status == vcs.StUntracked {
			untracked[f.Path] = true
		}
	}
	var tracked []string
	for _, p := range paths {
		if untracked[p] {
			if err := removeIn(dir, p); err != nil {
				return err
			}
			continue
		}
		tracked = append(tracked, p)
	}
	if len(tracked) == 0 {
		return nil
	}
	_, err = g.run(ctx, dir, opts{write: true, lock: g.key(ctx, dir)}, append([]string{"restore", "--worktree", "--"}, tracked...)...)
	return err
}

// removeIn deletes rel inside dir, never outside it.
func removeIn(dir, rel string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.Remove(filepath.FromSlash(rel))
}

// ---------------------------------------------------------------- commits

// Commit implements vcs.Provider.
func (g *Git) Commit(ctx context.Context, dir string, r vcs.CommitRequest) (vcs.Commit, error) {
	msg := strings.TrimSpace(r.Message)
	if msg == "" {
		return vcs.Commit{}, errors.New("a commit needs a message")
	}
	key := g.key(ctx, dir)
	if len(r.Paths) > 0 {
		if err := g.Stage(ctx, dir, r.Paths); err != nil {
			return vcs.Commit{}, err
		}
	}
	if r.All {
		if _, err := g.run(ctx, dir, opts{write: true, lock: key}, "add", "--all"); err != nil {
			return vcs.Commit{}, err
		}
	}
	if d, err := g.run(ctx, dir, opts{ok: []int{1}}, "diff", "--cached", "--quiet"); err == nil && d.code == 0 {
		if _, herr := g.run(ctx, dir, opts{}, "rev-parse", "--verify", "--quiet", "HEAD"); herr == nil {
			return vcs.Commit{}, vcs.ErrNothing
		}
	}
	if _, err := g.run(ctx, dir, opts{write: true, lock: key, env: sigEnv(r.Author), stdin: []byte(msg + "\n")}, "commit", "--quiet", "--no-verify", "-F", "-"); err != nil {
		return vcs.Commit{}, err
	}
	cs, err := g.Log(ctx, dir, "HEAD", 1)
	if err != nil || len(cs) == 0 {
		return vcs.Commit{}, err
	}
	return cs[0], nil
}

func sigEnv(s vcs.Signature) []string {
	name, email := s.Name, s.Email
	if name == "" {
		name = "Crewbase"
	}
	if email == "" {
		email = "agent@crewbase.local"
	}
	return []string{"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email, "GIT_COMMITTER_NAME=" + name, "GIT_COMMITTER_EMAIL=" + email}
}

// Log implements vcs.Provider.
func (g *Git) Log(ctx context.Context, dir, rev string, n int) ([]vcs.Commit, error) {
	if rev == "" {
		rev = "HEAD"
	}
	for _, part := range strings.Split(rev, "..") {
		if part != "" {
			if err := checkRev(strings.TrimPrefix(part, ".")); err != nil {
				return nil, err
			}
		}
	}
	if n <= 0 || n > 500 {
		n = 50
	}
	r, err := g.run(ctx, dir, opts{}, "log", "-n", strconv.Itoa(n), "--format=%H%x1f%h%x1f%s%x1f%an%x1f%ae%x1f%aI%x1f%P%x1e", "--end-of-options", rev, "--")
	if err != nil {
		var ge *Error
		if errors.As(err, &ge) && (strings.Contains(ge.Msg, "does not have any commits") || strings.Contains(ge.Msg, "unknown revision")) {
			return nil, nil
		}
		return nil, err
	}
	var out []vcs.Commit
	for _, rec := range strings.Split(string(r.out), "\x1e") {
		rec = strings.TrimLeft(rec, "\n")
		f := strings.Split(rec, "\x1f")
		if len(f) < 7 {
			continue
		}
		when, _ := time.Parse(time.RFC3339, f[5])
		c := vcs.Commit{Hash: f[0], Short: f[1], Subject: f[2], Author: f[3], Email: f[4], When: when}
		if p := strings.TrimSpace(f[6]); p != "" {
			c.Parents = strings.Fields(p)
		}
		out = append(out, c)
	}
	return out, nil
}

// ---------------------------------------------------------------- plugin

// Plugin provides `vcs` with the git binary. Without git it provides
// nothing (the git feature stays idle).
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/vcs-gitcli",
	Description: "Version control through the git binary: worktrees, hunks, commits, merge previews, merges and pushes, with repository hooks and drivers switched off.",
	Apply: func(c *kernel.Context) error {
		g, err := New()
		if err != nil {
			c.Log().Info("vcs: git not found; the git features are off")
			return nil
		}
		c.Defer(g.Close)
		return c.Provide(vcs.ServiceKey, vcs.Provider(g))
	},
})
