package console

// 改动: what changed in an agent's workspace, for the 文件 › 改动 view.
//
//   - A workspace inside a git repository is compared with HEAD: every
//     change counts, whoever made it (file tools, bash, the person).
//   - Any other workspace is compared with what the agent's file tools
//     found before they first wrote a file: before write_file, edit_file
//     or delete_file touch a file, its content (or its absence) is kept in
//     <root>/changes/<agent>/<workspace hash>/, never in the workspace.
//     Changes made by commands (bash) are not seen there.
//
// 撤销 puts a file back as it was (git: as in HEAD); 全部保留 (tracked
// workspaces only) forgets the kept copies, so later changes start a new
// comparison.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

// Change sources (ChangesView.Source).
const (
	SourceGit     = "git"
	SourceTracked = "tracked"
)

// File states (FileChange.Status).
const (
	StatusAdded    = "added"
	StatusModified = "modified"
	StatusDeleted  = "deleted"
	StatusRenamed  = "renamed"
)

// limits of the view
const (
	maxChangedFiles  = 300
	maxDiffFileBytes = 1 << 20
)

// FileChange is one changed file.
type FileChange struct {
	Path    string    `json:"path"`             // relative to the workspace
	From    string    `json:"from,omitempty"`   // a renamed file's old path
	Status  string    `json:"status"`           // added | modified | deleted | renamed
	Added   int       `json:"added"`            // lines
	Removed int       `json:"removed"`          // lines
	Binary  bool      `json:"binary,omitempty"` // no line diff
	Large   bool      `json:"large,omitempty"`  // too large to diff
	Updated time.Time `json:"updated,omitzero"` // the file's mtime (deleted: zero)
}

// ChangesView is GET /api/agents/{id}/changes.
type ChangesView struct {
	Source  string       `json:"source"`           // git | tracked
	Branch  string       `json:"branch,omitempty"` // git
	Files   []FileChange `json:"files"`
	Added   int          `json:"added"`
	Removed int          `json:"removed"`
	// More: there were more changed files than are listed.
	More bool `json:"more,omitempty"`
}

// FileDiff is GET /api/agents/{id}/changes/diff.
type FileDiff struct {
	FileChange
	Hunks []Hunk `json:"hunks"`
}

// changes keeps the tracked workspaces' original contents.
type changes struct {
	root string // <data root>/changes
	mu   sync.Mutex
}

type keptFile struct {
	Existed bool      `json:"existed"`
	Blob    string    `json:"blob,omitempty"` // sha256 of the content, in blobs/
	Large   bool      `json:"large,omitempty"`
	At      time.Time `json:"at"`
}

type keptIndex struct {
	Workspace string              `json:"workspace"`
	Files     map[string]keptFile `json:"files"`
}

func (c *changes) dir(agentID, ws string) string {
	sum := sha256.Sum256([]byte(ws))
	return filepath.Join(c.root, agentID, hex.EncodeToString(sum[:6]))
}

func (c *changes) load(agentID, ws string) keptIndex {
	idx := keptIndex{Workspace: ws, Files: map[string]keptFile{}}
	b, err := os.ReadFile(filepath.Join(c.dir(agentID, ws), "index.json"))
	if err == nil {
		_ = json.Unmarshal(b, &idx)
		if idx.Files == nil {
			idx.Files = map[string]keptFile{}
		}
	}
	return idx
}

func (c *changes) save(agentID, ws string, idx keptIndex) error {
	d := c.dir(agentID, ws)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(idx, "", "  ")
	tmp := filepath.Join(d, ".index.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(d, "index.json"))
}

// keep records rel's content in ws before an agent's file tool first
// changes it (once per file until 全部保留).
func (c *changes) keep(agentID, ws, rel string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	idx := c.load(agentID, ws)
	if _, ok := idx.Files[rel]; ok {
		return
	}
	kf := keptFile{At: time.Now().UTC()}
	p := filepath.Join(ws, rel)
	if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
		kf.Existed = true
		if st.Size() > maxDiffFileBytes {
			kf.Large = true
		} else if b, err := os.ReadFile(p); err == nil {
			sum := sha256.Sum256(b)
			kf.Blob = hex.EncodeToString(sum[:])
			bd := filepath.Join(c.dir(agentID, ws), "blobs")
			if err := os.MkdirAll(bd, 0o700); err != nil {
				return
			}
			if err := os.WriteFile(filepath.Join(bd, kf.Blob), b, 0o600); err != nil {
				return
			}
		}
	} else if err == nil {
		return // a folder or something else: not a file change
	}
	idx.Files[rel] = kf
	_ = c.save(agentID, ws, idx)
}

func (c *changes) blob(agentID, ws, sum string) ([]byte, error) {
	if sum == "" {
		return nil, nil
	}
	return os.ReadFile(filepath.Join(c.dir(agentID, ws), "blobs", sum))
}

// hook is the tools/pre-execute middleware that keeps a file before an
// agent's file tool changes it.
func (c *changes) hook(ctx context.Context, d *tools.Decision, next kernel.Next[*tools.Decision, *tools.Decision]) (*tools.Decision, error) {
	if d != nil && d.Call != nil && d.Call.Agent != "" && d.Call.Workspace != "" {
		switch d.Call.Name {
		case "write_file", "edit_file", "delete_file":
			if rel, ok := inWorkspace(d.Call.Workspace, d.Call.String("path")); ok && !inGit(d.Call.Workspace) {
				c.keep(d.Call.Agent, d.Call.Workspace, rel)
			}
		}
	}
	return next(ctx, d)
}

// inWorkspace turns a tool's path argument into a path relative to ws.
func inWorkspace(ws, p string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", false
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(ws, p)
	}
	rel, err := filepath.Rel(ws, filepath.Clean(p))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// ---------------------------------------------------------------- git

type gitRepo struct {
	ws, top, prefix string // prefix: ws inside top ("" or "sub/dir/")
	head            bool   // HEAD exists (a first commit was made)
}

func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.quotepath=off", "-c", "color.ui=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.Bytes(), errors.New(msg)
	}
	return out.Bytes(), nil
}

var gitCache sync.Map // ws → gitProbe

type gitProbe struct {
	repo *gitRepo
	at   time.Time
}

// repoOf is the git repository ws lies in, nil when there is none (or no
// git). Probes are kept for a few seconds.
func repoOf(ws string) *gitRepo {
	if v, ok := gitCache.Load(ws); ok && time.Since(v.(gitProbe).at) < 5*time.Second {
		return v.(gitProbe).repo
	}
	var r *gitRepo
	if out, err := git(context.Background(), ws, "rev-parse", "--show-toplevel", "--show-prefix"); err == nil {
		lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
		if len(lines) >= 1 && lines[0] != "" {
			r = &gitRepo{ws: ws, top: lines[0]}
			if len(lines) >= 2 {
				r.prefix = lines[1]
			}
			_, err := git(context.Background(), ws, "rev-parse", "--verify", "--quiet", "HEAD")
			r.head = err == nil
		}
	}
	gitCache.Store(ws, gitProbe{repo: r, at: time.Now()})
	return r
}

func inGit(ws string) bool { return repoOf(ws) != nil }

// rel turns a repository path into one relative to the workspace.
func (r *gitRepo) rel(p string) (string, bool) {
	if r.prefix == "" {
		return p, true
	}
	return strings.CutPrefix(p, r.prefix)
}

func (r *gitRepo) list(ctx context.Context) (ChangesView, error) {
	v := ChangesView{Source: SourceGit, Files: []FileChange{}}
	if b, err := git(ctx, r.ws, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		v.Branch = strings.TrimSpace(string(b))
	}
	out, err := git(ctx, r.ws, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--", ".")
	if err != nil {
		return v, err
	}
	counts := map[string][2]int{}
	binary := map[string]bool{}
	if r.head {
		if ns, err := git(ctx, r.ws, "diff", "--numstat", "-z", "--find-renames", "HEAD", "--", "."); err == nil {
			parts := strings.Split(string(ns), "\x00")
			for i := 0; i < len(parts); i++ {
				f := strings.SplitN(parts[i], "\t", 3)
				if len(f) < 3 {
					continue
				}
				path := f[2]
				if path == "" && i+2 < len(parts) { // a rename: "a\tb\t\x00old\x00new"
					path = parts[i+2]
					i += 2
				}
				if f[0] == "-" {
					binary[path] = true
					continue
				}
				a, _ := strconv.Atoi(f[0])
				d, _ := strconv.Atoi(f[1])
				counts[path] = [2]int{a, d}
			}
		}
	}
	entries := strings.Split(string(out), "\x00")
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		x, y, path := e[0], e[1], e[3:]
		fc := FileChange{}
		var from string
		switch {
		case x == '?' || x == 'A':
			fc.Status = StatusAdded
		case x == 'R' || y == 'R':
			fc.Status = StatusRenamed
			if i+1 < len(entries) {
				from = entries[i+1]
				i++
			}
		case x == 'D' || y == 'D':
			fc.Status = StatusDeleted
		default:
			fc.Status = StatusModified
		}
		rel, ok := r.rel(path)
		if !ok {
			continue
		}
		fc.Path = rel
		if from != "" {
			fc.From, _ = r.rel(from)
		}
		if strings.HasSuffix(rel, "/") {
			continue // a submodule or an empty folder
		}
		if c, ok := counts[path]; ok {
			fc.Added, fc.Removed = c[0], c[1]
		} else if binary[path] {
			fc.Binary = true
		} else if fc.Status == StatusAdded {
			fc.Added, fc.Binary, fc.Large = countFile(filepath.Join(r.ws, rel))
		}
		if st, err := os.Stat(filepath.Join(r.ws, rel)); err == nil {
			fc.Updated = st.ModTime()
		}
		if len(v.Files) >= maxChangedFiles {
			v.More = true
			break
		}
		v.Files = append(v.Files, fc)
	}
	return finish(v), nil
}

// old is a file's content in HEAD ("" when it is not there).
func (r *gitRepo) old(ctx context.Context, rel string) ([]byte, error) {
	if !r.head {
		return nil, nil
	}
	b, err := git(ctx, r.ws, "show", "HEAD:"+r.prefix+rel)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") || strings.Contains(err.Error(), "exists on disk, but not in") {
			return nil, nil
		}
		return nil, err
	}
	return b, nil
}

func (r *gitRepo) revert(ctx context.Context, fc FileChange) error {
	switch fc.Status {
	case StatusAdded:
		// not in HEAD: remove it (from the index too when it was added there)
		_, _ = git(ctx, r.ws, "rm", "--cached", "--quiet", "--ignore-unmatch", "--", fc.Path)
		return wsRemove(r.ws, fc.Path)
	case StatusRenamed:
		if _, err := git(ctx, r.ws, "restore", "--source=HEAD", "--staged", "--worktree", "--", fc.From); err != nil {
			return err
		}
		_, _ = git(ctx, r.ws, "rm", "--cached", "--quiet", "--ignore-unmatch", "--", fc.Path)
		return wsRemove(r.ws, fc.Path)
	default:
		_, err := git(ctx, r.ws, "restore", "--source=HEAD", "--staged", "--worktree", "--", fc.Path)
		return err
	}
}

// ---------------------------------------------------------------- tracked

func (c *changes) list(agentID, ws string) ChangesView {
	c.mu.Lock()
	idx := c.load(agentID, ws)
	c.mu.Unlock()
	v := ChangesView{Source: SourceTracked, Files: []FileChange{}}
	for rel, kf := range idx.Files {
		fc, ok := c.state(agentID, ws, rel, kf)
		if !ok {
			continue
		}
		v.Files = append(v.Files, fc)
	}
	sort.Slice(v.Files, func(i, j int) bool { return v.Files[i].Path < v.Files[j].Path })
	if len(v.Files) > maxChangedFiles {
		v.Files, v.More = v.Files[:maxChangedFiles], true
	}
	return finish(v)
}

// state compares a kept file with the workspace; ok=false when unchanged.
func (c *changes) state(agentID, ws, rel string, kf keptFile) (FileChange, bool) {
	fc := FileChange{Path: rel}
	p := filepath.Join(ws, filepath.FromSlash(rel))
	st, err := os.Stat(p)
	exists := err == nil && st.Mode().IsRegular()
	switch {
	case !kf.Existed && !exists:
		return fc, false
	case !kf.Existed:
		fc.Status = StatusAdded
	case !exists:
		fc.Status = StatusDeleted
	default:
		fc.Status = StatusModified
	}
	if exists {
		fc.Updated = st.ModTime()
	}
	if kf.Large || (exists && st.Size() > maxDiffFileBytes) {
		fc.Large = true
		return fc, true
	}
	old, _ := c.blob(agentID, ws, kf.Blob)
	var cur []byte
	if exists {
		cur, _ = os.ReadFile(p)
	}
	if kf.Existed && exists && bytes.Equal(old, cur) {
		return fc, false
	}
	if isBinary(old) || isBinary(cur) {
		fc.Binary = true
		return fc, true
	}
	_, fc.Added, fc.Removed = Diff(string(old), string(cur), 0)
	return fc, true
}

func (c *changes) revert(agentID, ws, rel string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	idx := c.load(agentID, ws)
	kf, ok := idx.Files[rel]
	if !ok {
		return os.ErrNotExist
	}
	if !kf.Existed {
		if err := wsRemove(ws, rel); err != nil {
			return err
		}
	} else {
		if kf.Large {
			return errors.New("the file was too large to keep a copy of; it cannot be put back")
		}
		b, err := c.blob(agentID, ws, kf.Blob)
		if err != nil {
			return err
		}
		if err := wsWrite(ws, rel, b); err != nil {
			return err
		}
	}
	delete(idx.Files, rel)
	return c.save(agentID, ws, idx)
}

// accept forgets every kept file: the workspace as it is becomes the new
// starting point.
func (c *changes) accept(agentID, ws string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return os.RemoveAll(c.dir(agentID, ws))
}

// ---------------------------------------------------------------- shared

func finish(v ChangesView) ChangesView {
	for _, f := range v.Files {
		v.Added += f.Added
		v.Removed += f.Removed
	}
	return v
}

func removeFile(p string) error {
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Writing back and removing files inside a workspace go through os.Root:
// it refuses any path that leaves the workspace, by ".." or by a symlink
// (an agent can turn a folder it was given into a link to anywhere, and
// an undo must not follow it there).

// wsWrite writes data to rel inside ws, making its folders.
func wsWrite(ws, rel string, data []byte) error {
	root, err := os.OpenRoot(ws)
	if err != nil {
		return err
	}
	defer root.Close()
	rel = filepath.FromSlash(rel)
	if d := filepath.Dir(rel); d != "." {
		if err := root.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	// a link in the file's own place is replaced, not written through
	if fi, err := root.Lstat(rel); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		if err := root.Remove(rel); err != nil {
			return err
		}
	}
	return root.WriteFile(rel, data, 0o644)
}

// wsRemove removes rel inside ws (gone already is fine).
func wsRemove(ws, rel string) error {
	root, err := os.OpenRoot(ws)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Remove(filepath.FromSlash(rel)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func isBinary(b []byte) bool {
	n := len(b)
	if n > 8000 {
		n = 8000
	}
	return bytes.IndexByte(b[:n], 0) >= 0 || !utf8.Valid(b[:n])
}

// countFile is a new file's line count (or that it is binary / too large).
func countFile(p string) (lines int, binary, large bool) {
	st, err := os.Stat(p)
	if err != nil || !st.Mode().IsRegular() {
		return 0, false, false
	}
	if st.Size() > maxDiffFileBytes {
		return 0, false, true
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return 0, false, false
	}
	if isBinary(b) {
		return 0, true, false
	}
	return len(splitLines(string(b))), false, false
}

// view lists the changes of the agent working in ws.
func (c *changes) view(ctx context.Context, agentID, ws string) (ChangesView, error) {
	if r := repoOf(ws); r != nil {
		return r.list(ctx)
	}
	return c.list(agentID, ws), nil
}

// diff is one file's diff.
func (c *changes) diff(ctx context.Context, agentID, ws, rel string) (FileDiff, error) {
	v, err := c.view(ctx, agentID, ws)
	if err != nil {
		return FileDiff{}, err
	}
	var fc *FileChange
	for i := range v.Files {
		if v.Files[i].Path == rel {
			fc = &v.Files[i]
		}
	}
	if fc == nil {
		return FileDiff{}, os.ErrNotExist
	}
	fd := FileDiff{FileChange: *fc, Hunks: []Hunk{}}
	if fc.Binary || fc.Large {
		return fd, nil
	}
	var old, cur []byte
	if r := repoOf(ws); r != nil {
		from := rel
		if fc.From != "" {
			from = fc.From
		}
		if old, err = r.old(ctx, from); err != nil {
			return fd, err
		}
	} else {
		c.mu.Lock()
		kf := c.load(agentID, ws).Files[rel]
		c.mu.Unlock()
		old, _ = c.blob(agentID, ws, kf.Blob)
	}
	if fc.Status != StatusDeleted {
		cur, _ = os.ReadFile(filepath.Join(ws, filepath.FromSlash(rel)))
	}
	if isBinary(old) || isBinary(cur) {
		fd.Binary = true
		return fd, nil
	}
	fd.Hunks, fd.Added, fd.Removed = Diff(string(old), string(cur), 3)
	return fd, nil
}

// undo puts one file back as it was.
func (c *changes) undo(ctx context.Context, agentID, ws, rel string) error {
	if r := repoOf(ws); r != nil {
		v, err := r.list(ctx)
		if err != nil {
			return err
		}
		for _, f := range v.Files {
			if f.Path == rel {
				return r.revert(ctx, f)
			}
		}
		return os.ErrNotExist
	}
	return c.revert(agentID, ws, rel)
}

func (c *changes) String() string { return fmt.Sprintf("changes(%s)", c.root) }
