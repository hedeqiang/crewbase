package gitcli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/vcs"
)

// ---------------------------------------------------------------- merge preview

// MergePreview implements vcs.Provider with git merge-tree --write-tree:
// the merged tree is computed in the object store, no working tree or
// index is touched (and nothing is locked).
func (g *Git) MergePreview(ctx context.Context, repo, target, source string) (vcs.MergePreview, error) {
	p := vcs.MergePreview{Target: target, Source: source}
	var err error
	if p.TargetHead, err = g.Resolve(ctx, repo, target); err != nil {
		return p, err
	}
	if p.SourceHead, err = g.Resolve(ctx, repo, source); err != nil {
		return p, err
	}
	if mb, err := g.run(ctx, repo, opts{}, "merge-base", p.TargetHead, p.SourceHead); err == nil {
		p.Base = trim(mb.out)
	}
	if c, err := g.run(ctx, repo, opts{}, "rev-list", "--left-right", "--count", p.TargetHead+"..."+p.SourceHead); err == nil {
		f := strings.Fields(trim(c.out))
		if len(f) == 2 {
			p.Behind, _ = strconv.Atoi(f[0])
			p.Ahead, _ = strconv.Atoi(f[1])
		}
	}
	p.UpToDate = p.Ahead == 0
	p.FastForward = p.Base == p.TargetHead && p.Ahead > 0
	if wts, err := g.Worktrees(ctx, repo); err == nil {
		for _, w := range wts {
			if w.Branch == target {
				p.CheckedOutIn = w.Path
			}
		}
	}
	if p.UpToDate {
		p.Clean = true
		return p, nil
	}
	if fs, err := g.run(ctx, repo, opts{}, "diff", "--name-only", "--no-renames", p.TargetHead+"..."+p.SourceHead); err == nil {
		p.Files = lines(fs.out)
	}
	r, err := g.run(ctx, repo, opts{ok: []int{1}}, "merge-tree", "--write-tree", "--name-only", "--messages", "-z", p.TargetHead, p.SourceHead)
	if err != nil {
		return p, err
	}
	// <tree>\0<conflicted path>\0…\0\0<messages>
	parts := strings.SplitN(string(r.out), "\x00\x00", 2)
	head := strings.Split(strings.TrimRight(parts[0], "\x00"), "\x00")
	if len(head) > 0 {
		p.Tree = head[0]
	}
	seen := map[string]bool{}
	for _, c := range head[1:] {
		if c != "" && !seen[c] {
			seen[c] = true
			p.Conflicts = append(p.Conflicts, c)
		}
	}
	if len(parts) == 2 {
		for _, m := range strings.Split(parts[1], "\x00") {
			// messages: <n paths>\0<path>…\0<type>\0<message>
			if m = strings.TrimSpace(m); m != "" && strings.Contains(m, " ") {
				p.Messages = append(p.Messages, m)
			}
		}
	}
	p.Clean = r.code == 0
	return p, nil
}

// Merge implements vcs.Provider. The new commit is made from the tree
// merge-tree computed and the branch moved with a compare-and-swap, so a
// branch that moved meanwhile is not overwritten.
func (g *Git) Merge(ctx context.Context, repo string, m vcs.MergeRequest) (vcs.Commit, error) {
	if err := checkRef(m.Target); err != nil {
		return vcs.Commit{}, err
	}
	key := g.key(ctx, repo)
	l := g.lockFor(key)
	l.Lock()
	defer l.Unlock()
	p, err := g.MergePreview(ctx, repo, m.Target, m.Source)
	if err != nil {
		return vcs.Commit{}, err
	}
	if m.Expect != "" && !strings.HasPrefix(p.SourceHead, m.Expect) {
		return vcs.Commit{}, fmt.Errorf("%s moved to %s since it was reviewed at %s", m.Source, short(p.SourceHead), short(m.Expect))
	}
	if p.UpToDate {
		return vcs.Commit{}, vcs.ErrUpToDate
	}
	if !p.Clean {
		return vcs.Commit{}, fmt.Errorf("%w: %s", vcs.ErrConflict, strings.Join(p.Conflicts, ", "))
	}
	// a working tree that has the target checked out comes along: it must
	// have nothing uncommitted that the merge would overwrite
	if p.CheckedOutIn != "" {
		st, err := g.Status(ctx, p.CheckedOutIn)
		if err != nil {
			return vcs.Commit{}, err
		}
		for _, f := range st.Files {
			if f.Status != vcs.StUntracked {
				return vcs.Commit{}, fmt.Errorf("%w: %s has %s checked out with uncommitted changes (%s)", vcs.ErrDirty, p.CheckedOutIn, m.Target, f.Path)
			}
		}
	}
	msg := strings.TrimSpace(m.Message)
	var next string
	switch m.Strategy {
	case vcs.MergeFFOnly:
		if !p.FastForward {
			return vcs.Commit{}, vcs.ErrNotFF
		}
		next = p.SourceHead
	case vcs.MergeSquash:
		if msg == "" {
			msg = fmt.Sprintf("Squash %s into %s", m.Source, m.Target)
		}
		r, err := g.run(ctx, repo, opts{write: true, env: sigEnv(m.Author), stdin: []byte(msg + "\n")}, "commit-tree", p.Tree, "-p", p.TargetHead, "-F", "-")
		if err != nil {
			return vcs.Commit{}, err
		}
		next = trim(r.out)
	default:
		if msg == "" {
			msg = fmt.Sprintf("Merge branch '%s' into %s", m.Source, m.Target)
		}
		r, err := g.run(ctx, repo, opts{write: true, env: sigEnv(m.Author), stdin: []byte(msg + "\n")}, "commit-tree", p.Tree, "-p", p.TargetHead, "-p", p.SourceHead, "-F", "-")
		if err != nil {
			return vcs.Commit{}, err
		}
		next = trim(r.out)
	}
	if _, err := g.run(ctx, repo, opts{write: true}, "update-ref", "-m", "crewbase: merge "+m.Source, "refs/heads/"+m.Target, next, p.TargetHead); err != nil {
		return vcs.Commit{}, fmt.Errorf("%s moved during the merge; preview again: %w", m.Target, err)
	}
	if p.CheckedOutIn != "" {
		// bring the checked-out tree from the old commit to the new one
		// (two-tree read-tree refuses to overwrite local changes)
		if _, err := g.run(ctx, p.CheckedOutIn, opts{write: true}, "read-tree", "-m", "-u", p.TargetHead, next); err != nil {
			_, _ = g.run(ctx, repo, opts{write: true}, "update-ref", "refs/heads/"+m.Target, p.TargetHead, next)
			return vcs.Commit{}, fmt.Errorf("could not update %s: %w", p.CheckedOutIn, err)
		}
	}
	cs, err := g.Log(ctx, repo, next, 1)
	if err != nil || len(cs) == 0 {
		return vcs.Commit{Hash: next, Short: short(next)}, err
	}
	return cs[0], nil
}

func short(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

// ---------------------------------------------------------------- conflicts

// StartMerge implements vcs.Provider.
func (g *Git) StartMerge(ctx context.Context, dir, target string, author vcs.Signature) ([]vcs.Conflict, error) {
	if err := checkRev(target); err != nil {
		return nil, err
	}
	st, err := g.Status(ctx, dir)
	if err != nil {
		return nil, err
	}
	if st.Merging {
		return g.Conflicts(ctx, dir)
	}
	for _, f := range st.Files {
		if f.Status != vcs.StUntracked {
			return nil, fmt.Errorf("%w: commit or discard %s first", vcs.ErrDirty, f.Path)
		}
	}
	r, err := g.run(ctx, dir, opts{write: true, lock: g.key(ctx, dir), env: sigEnv(author), ok: []int{1}},
		"merge", "--no-ff", "--no-commit", "--no-edit", "--no-verify", "--end-of-options", target)
	if err != nil {
		return nil, err
	}
	if r.code == 0 {
		// merged cleanly: nothing to resolve, the merge waits to be concluded
		return nil, nil
	}
	return g.Conflicts(ctx, dir)
}

// Conflicts implements vcs.Provider.
func (g *Git) Conflicts(ctx context.Context, dir string) ([]vcs.Conflict, error) {
	r, err := g.run(ctx, dir, opts{}, "diff", "--name-only", "--diff-filter=U", "-z")
	if err != nil {
		return nil, err
	}
	var out []vcs.Conflict
	for _, p := range strings.Split(strings.TrimRight(string(r.out), "\x00"), "\x00") {
		if p == "" {
			continue
		}
		c := vcs.Conflict{Path: p}
		if b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p))); err == nil {
			c.Blocks = ConflictBlocks(string(b))
		}
		out = append(out, c)
	}
	return out, nil
}

// ConflictBlocks finds the <<<<<<< ||||||| ======= >>>>>>> regions.
func ConflictBlocks(text string) []vcs.ConflictBlock {
	var out []vcs.ConflictBlock
	ls := strings.Split(text, "\n")
	for i := 0; i < len(ls); i++ {
		if !strings.HasPrefix(ls[i], "<<<<<<<") {
			continue
		}
		b := vcs.ConflictBlock{Start: i + 1}
		var ours, base, theirs []string
		part := &ours
		j := i + 1
		for ; j < len(ls); j++ {
			l := ls[j]
			switch {
			case strings.HasPrefix(l, "|||||||"):
				part = &base
				continue
			case strings.HasPrefix(l, "======="):
				part = &theirs
				continue
			case strings.HasPrefix(l, ">>>>>>>"):
			default:
				*part = append(*part, l)
				continue
			}
			break
		}
		if j >= len(ls) {
			break // unterminated
		}
		b.End = j + 1
		b.Ours, b.Base, b.Theirs = strings.Join(ours, "\n"), strings.Join(base, "\n"), strings.Join(theirs, "\n")
		out = append(out, b)
		i = j
	}
	return out
}

// HasMarkers reports whether text still holds conflict markers.
func HasMarkers(text string) bool {
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "<<<<<<< ") || strings.HasPrefix(l, ">>>>>>> ") || l == "<<<<<<<" || l == ">>>>>>>" {
			return true
		}
	}
	return false
}

// ResolveConflict implements vcs.Provider.
func (g *Git) ResolveConflict(ctx context.Context, dir, path, how string) error {
	key := g.key(ctx, dir)
	switch how {
	case "ours", "theirs":
		if _, err := g.run(ctx, dir, opts{write: true, lock: key}, "checkout", "--"+how, "--", path); err != nil {
			return err
		}
	case "manual", "":
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path)))
		if err == nil && HasMarkers(string(b)) {
			return fmt.Errorf("%s still has conflict markers", path)
		}
	default:
		return fmt.Errorf("resolve with ours, theirs or manual, not %q", how)
	}
	_, err := g.run(ctx, dir, opts{write: true, lock: key}, "add", "--", path)
	return err
}

// ConcludeMerge implements vcs.Provider.
func (g *Git) ConcludeMerge(ctx context.Context, dir, message string, author vcs.Signature) (vcs.Commit, error) {
	cs, err := g.Conflicts(ctx, dir)
	if err != nil {
		return vcs.Commit{}, err
	}
	if len(cs) > 0 {
		names := make([]string, len(cs))
		for i, c := range cs {
			names[i] = c.Path
		}
		return vcs.Commit{}, fmt.Errorf("%w: still unresolved: %s", vcs.ErrConflict, strings.Join(names, ", "))
	}
	args := []string{"commit", "--quiet", "--no-verify"}
	o := opts{write: true, lock: g.key(ctx, dir), env: sigEnv(author)}
	if m := strings.TrimSpace(message); m != "" {
		args = append(args, "-F", "-")
		o.stdin = []byte(m + "\n")
	} else {
		args = append(args, "--no-edit")
	}
	if _, err := g.run(ctx, dir, o, args...); err != nil {
		return vcs.Commit{}, err
	}
	out, err := g.Log(ctx, dir, "HEAD", 1)
	if err != nil || len(out) == 0 {
		return vcs.Commit{}, err
	}
	return out[0], nil
}

// AbortMerge implements vcs.Provider.
func (g *Git) AbortMerge(ctx context.Context, dir string) error {
	_, err := g.run(ctx, dir, opts{write: true, lock: g.key(ctx, dir)}, "merge", "--abort")
	return err
}

// ---------------------------------------------------------------- archive

// Archive implements vcs.Provider.
func (g *Git) Archive(ctx context.Context, dir, base, out string) (string, string, error) {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return "", "", err
	}
	stamp := time.Now().UTC().Format("20060102-150405")
	var patch, bundle string
	// uncommitted changes, untracked files included (intent-to-add on a
	// throwaway index copy, so the real index is untouched)
	idx, err := os.CreateTemp("", "crewbase-idx-")
	if err != nil {
		return "", "", err
	}
	idx.Close()
	defer os.Remove(idx.Name())
	gd, err := g.run(ctx, dir, opts{}, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return "", "", err
	}
	if b, err := os.ReadFile(filepath.Join(trim(gd.out), "index")); err == nil {
		_ = os.WriteFile(idx.Name(), b, 0o600)
	} else {
		_ = os.Remove(idx.Name())
	}
	env := []string{"GIT_INDEX_FILE=" + idx.Name()}
	_, _ = g.run(ctx, dir, opts{env: env, write: true}, "add", "--intent-to-add", "--all")
	if g.hasHead(ctx, dir) {
		if d, err := g.run(ctx, dir, opts{env: env}, "diff", "--binary", "--no-ext-diff", "--no-textconv", "HEAD"); err == nil && len(d.out) > 0 {
			patch = filepath.Join(out, "uncommitted-"+stamp+".patch")
			if err := os.WriteFile(patch, d.out, 0o600); err != nil {
				return "", "", err
			}
		}
	}
	if base != "" && g.hasHead(ctx, dir) {
		if c, err := g.run(ctx, dir, opts{}, "rev-list", "--count", base+"..HEAD"); err == nil && trim(c.out) != "0" {
			bundle = filepath.Join(out, "commits-"+stamp+".bundle")
			if _, err := g.run(ctx, dir, opts{}, "bundle", "create", "--quiet", bundle, base+"..HEAD"); err != nil {
				return patch, "", err
			}
		}
	}
	return patch, bundle, nil
}

// ---------------------------------------------------------------- remote

// RemoteURL implements vcs.Provider.
func (g *Git) RemoteURL(ctx context.Context, dir, remote string) (string, error) {
	if remote == "" {
		remote = "origin"
	}
	if err := checkRef(remote); err != nil {
		return "", err
	}
	r, err := g.run(ctx, dir, opts{}, "remote", "get-url", "--push", "--", remote)
	if err != nil {
		return "", err
	}
	return trim(r.out), nil
}

// Remote is a parsed remote URL.
type Remote struct {
	Host  string // github.com
	Owner string
	Repo  string // without .git
}

// Slug is owner/repo.
func (r Remote) Slug() string { return r.Owner + "/" + r.Repo }

// ParseRemote reads https://host/owner/repo(.git), ssh://git@host[:port]/owner/repo
// and git@host:owner/repo forms.
func ParseRemote(raw string) (Remote, bool) {
	raw = strings.TrimSpace(raw)
	var host, path string
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		host, path = u.Hostname(), u.Path
	} else if at := strings.Index(raw, "@"); at >= 0 && !strings.Contains(raw, "://") {
		rest := raw[at+1:]
		i := strings.Index(rest, ":")
		if i <= 0 {
			return Remote{}, false
		}
		host, path = rest[:i], rest[i+1:]
	} else {
		return Remote{}, false
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if host == "" || len(parts) < 2 {
		return Remote{}, false
	}
	owner, repo := parts[len(parts)-2], strings.TrimSuffix(parts[len(parts)-1], ".git")
	if owner == "" || repo == "" {
		return Remote{}, false
	}
	return Remote{Host: strings.ToLower(host), Owner: owner, Repo: repo}, true
}

// HostOf is the host a remote URL points at ("" when it has none).
func HostOf(remote string) string {
	if u, err := url.Parse(remote); err == nil && u.Host != "" {
		return strings.ToLower(u.Hostname())
	}
	// scp-like: git@github.com:owner/repo.git
	if at := strings.Index(remote, "@"); at >= 0 {
		rest := remote[at+1:]
		if i := strings.Index(rest, ":"); i > 0 {
			return strings.ToLower(rest[:i])
		}
	}
	return ""
}

// askpass makes a one-off askpass for token; the script holds no secret
// (it reads this command's environment) and is removed by cleanup.
func askpass(user, token string) (env []string, args []string, cleanup func(), err error) {
	tmp, err := os.MkdirTemp("", "crewbase-askpass-")
	if err != nil {
		return nil, nil, nil, err
	}
	script := filepath.Join(tmp, "askpass")
	body := "#!/bin/sh\ncase \"$1\" in\n  Username*|username*) printf '%s\\n' \"$CREWBASE_GIT_USER\" ;;\n  *) printf '%s\\n' \"$CREWBASE_GIT_TOKEN\" ;;\nesac\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		os.RemoveAll(tmp)
		return nil, nil, nil, err
	}
	if user == "" {
		user = "x-access-token"
	}
	return []string{"GIT_ASKPASS=" + script, "CREWBASE_GIT_USER=" + user, "CREWBASE_GIT_TOKEN=" + token},
		[]string{"-c", "credential.helper="}, func() { os.RemoveAll(tmp) }, nil
}

// Fetch implements vcs.Provider.
func (g *Git) Fetch(ctx context.Context, dir string, r vcs.FetchRequest) (string, error) {
	if r.Remote == "" {
		r.Remote = "origin"
	}
	if err := checkRef(r.Remote); err != nil {
		return "", err
	}
	for _, rs := range r.RefSpecs {
		if strings.HasPrefix(rs, "-") || strings.ContainsAny(rs, " \n\t\x00") {
			return "", fmt.Errorf("%q is not a refspec", rs)
		}
	}
	args := []string{"fetch", "--no-tags", "--quiet", "--no-recurse-submodules"}
	if r.Depth > 0 {
		args = append(args, "--depth", strconv.Itoa(r.Depth))
	}
	args = append(append(args, "--", r.Remote), r.RefSpecs...)
	o := opts{write: true, lock: g.key(ctx, dir), timeout: 5 * time.Minute}
	if r.Token != "" {
		env, pre, cleanup, err := askpass(r.Username, r.Token)
		if err != nil {
			return "", err
		}
		defer cleanup()
		o.env, o.secrets = env, []string{r.Token}
		args = append(pre, args...)
	}
	res, err := g.run(ctx, dir, o, args...)
	text := scrub(strings.TrimSpace(string(res.out)+"\n"+string(res.err)), o.secrets)
	return text, err
}

// Push implements vcs.Provider. A token goes to git through an askpass
// script that reads it from the environment of this one command; the
// script holds no secret and is removed afterwards. Credential helpers
// are switched off for the command, so nothing is stored.
func (g *Git) Push(ctx context.Context, dir string, r vcs.PushRequest) (string, error) {
	if r.Remote == "" {
		r.Remote = "origin"
	}
	if err := checkRef(r.Remote); err != nil {
		return "", err
	}
	if err := checkRef(r.Branch); err != nil {
		return "", err
	}
	args := []string{"push", "--porcelain"}
	if r.Force {
		args = append(args, "--force-with-lease")
	}
	if r.SetUp {
		args = append(args, "--set-upstream")
	}
	args = append(args, "--", r.Remote, "refs/heads/"+r.Branch+":refs/heads/"+r.Branch)
	o := opts{write: true, lock: g.key(ctx, dir), timeout: 5 * time.Minute}
	if r.Token != "" {
		env, pre, cleanup, err := askpass(r.Username, r.Token)
		if err != nil {
			return "", err
		}
		defer cleanup()
		o.env, o.secrets = env, []string{r.Token}
		args = append(pre, args...)
	}
	res, err := g.run(ctx, dir, o, args...)
	text := scrub(strings.TrimSpace(string(res.out)+"\n"+string(res.err)), o.secrets)
	if err != nil {
		var ge *Error
		if errors.As(err, &ge) {
			ge.Msg = scrub(ge.Msg, o.secrets)
		}
		return text, err
	}
	return text, nil
}
