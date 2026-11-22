// Package local is fs-local: the local-disk provider of the `fs`
// capability. Every path is resolved (symlinks followed) and must fall
// inside the call's roots and the configured hard roots; escapes through
// `..` or symlinks are rejected. Writes are atomic (temp file + rename) and
// go through the fs/write-intent waterfall first. Read-before-edit is
// tracked per (agent, resolved path) with the file's mtime and size.
package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	iofs "io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/fs"
	"github.com/hedeqiang/crewbase/kernel"
)

// DefaultMaxRead is the largest file Read returns.
const DefaultMaxRead = 10 << 20

// Config is the plugin config.
type Config struct {
	// Roots are hard bounds applied to every call, including host mode.
	// Empty = no hard bound (calls are still confined by Access.Roots).
	Roots []string `json:"roots,omitempty"`
	// MaxReadBytes caps Read (default 10MiB).
	MaxReadBytes int64 `json:"maxReadBytes,omitempty"`
	// AllowBlindOverwrite lets Write replace an existing file that the
	// agent has not read (default false: read first).
	AllowBlindOverwrite bool `json:"allowBlindOverwrite,omitempty"`
}

type stamp struct {
	mod  time.Time
	size int64
}

type key struct{ agent, path string }

// Local implements fs.FS.
type Local struct {
	d       kernel.Dispatcher
	roots   []string
	maxRead int64
	blind   bool

	mu   sync.Mutex // serializes mutations and guards seen
	seen map[key]stamp
}

// New creates a provider. d dispatches fs/write-intent (nil = no listeners).
func New(d kernel.Dispatcher, cfg Config) (*Local, error) {
	l := &Local{d: d, maxRead: cfg.MaxReadBytes, blind: cfg.AllowBlindOverwrite, seen: map[key]stamp{}}
	if l.maxRead <= 0 {
		l.maxRead = DefaultMaxRead
	}
	for _, r := range cfg.Roots {
		if !filepath.IsAbs(r) {
			return nil, fmt.Errorf("fs-local: root must be absolute: %q", r)
		}
		l.roots = append(l.roots, realPath(filepath.Clean(r)))
	}
	return l, nil
}

// realPath resolves symlinks in p; for a path that does not exist yet it
// resolves the deepest existing ancestor and appends the rest. The final
// component of a missing path may be a dangling symlink: writes replace it
// by rename (never follow it), so that is safe.
func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p
	}
	return filepath.Join(realPath(parent), filepath.Base(p))
}

func within(p, root string) bool {
	if root == "/" {
		return true
	}
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolve returns the real path of p and checks it against the roots.
// followFinal=false resolves only the parent (delete/stat of a link itself).
func (l *Local) resolve(a fs.Access, p string, followFinal bool) (string, error) {
	if p == "" || !filepath.IsAbs(p) {
		return "", fmt.Errorf("%w: %q", fs.ErrNotAbs, p)
	}
	p = filepath.Clean(p)
	var real string
	if followFinal || p == "/" {
		real = realPath(p)
	} else {
		real = filepath.Join(realPath(filepath.Dir(p)), filepath.Base(p))
	}
	check := func(roots []string) bool {
		for _, r := range roots {
			if within(real, r) {
				return true
			}
		}
		return false
	}
	if len(l.roots) > 0 && !check(l.roots) {
		return "", fmt.Errorf("%w: %s", fs.ErrOutsideRoots, p)
	}
	if len(a.Roots) > 0 {
		rs := make([]string, 0, len(a.Roots))
		for _, r := range a.Roots {
			if !filepath.IsAbs(r) {
				return "", fmt.Errorf("%w: root %q", fs.ErrNotAbs, r)
			}
			rs = append(rs, realPath(filepath.Clean(r)))
		}
		if !check(rs) {
			return "", fmt.Errorf("%w: %s", fs.ErrOutsideRoots, p)
		}
	}
	return real, nil
}

// underFold reports whether p is root or inside it, case-insensitively
// (".CLAUDE" is ".claude" on the default macOS file systems).
func underFold(p, root string) bool {
	if strings.EqualFold(p, root) {
		return true
	}
	prefix := strings.TrimSuffix(root, string(filepath.Separator)) + string(filepath.Separator)
	return len(p) > len(prefix) && strings.EqualFold(p[:len(prefix)], prefix)
}

// protect refuses a mutation of a protected path. It checks the path as
// given (cleaned) and as resolved, against each protected path as given and
// as resolved, so links in either direction do not get around it.
func protect(a fs.Access, p, real string) error {
	if len(a.Protected) == 0 {
		return nil
	}
	p = filepath.Clean(p)
	for _, pr := range a.Protected {
		if !filepath.IsAbs(pr) {
			continue
		}
		pr = filepath.Clean(pr)
		prReal := realPath(pr)
		for _, x := range []string{p, real} {
			if underFold(x, pr) || underFold(x, prReal) {
				return fmt.Errorf("%w: %s is coding-CLI configuration and stays read-only here (it could change what a CLI may do); ask the user to edit it or to grant host mode", fs.ErrProtected, p)
			}
		}
	}
	return nil
}

func (l *Local) isRoot(a fs.Access, real string) bool {
	for _, r := range append(append([]string(nil), l.roots...), a.Roots...) {
		if realPath(filepath.Clean(r)) == real {
			return true
		}
	}
	return real == "/"
}

func info(path string, st iofs.FileInfo) fs.Info {
	return fs.Info{Path: path, Name: st.Name(), Size: st.Size(), Mode: st.Mode().String(), ModTime: st.ModTime(),
		IsDir: st.IsDir(), Symlink: st.Mode()&iofs.ModeSymlink != 0}
}

func notFound(err error, p string) error {
	if errors.Is(err, iofs.ErrNotExist) {
		return fmt.Errorf("%w: %s", fs.ErrNotFound, p)
	}
	return err
}

func (l *Local) remember(agent, path string, st iofs.FileInfo) {
	l.seen[key{agent, path}] = stamp{mod: st.ModTime(), size: st.Size()}
}

func (l *Local) Read(_ context.Context, a fs.Access, p string) (fs.File, error) {
	real, err := l.resolve(a, p, true)
	if err != nil {
		return fs.File{}, err
	}
	st, err := os.Stat(real)
	if err != nil {
		return fs.File{}, notFound(err, p)
	}
	if st.IsDir() {
		return fs.File{}, fmt.Errorf("%w: %s (use list)", fs.ErrIsDir, p)
	}
	if st.Size() > l.maxRead {
		return fs.File{}, fmt.Errorf("%w: %s is %d bytes (limit %d)", fs.ErrTooLarge, p, st.Size(), l.maxRead)
	}
	b, err := os.ReadFile(real)
	if err != nil {
		return fs.File{}, notFound(err, p)
	}
	// Stat again after reading: the stamp must describe what we returned.
	if st2, err := os.Stat(real); err == nil {
		st = st2
	}
	l.mu.Lock()
	l.remember(a.Agent, real, st)
	l.mu.Unlock()
	return fs.File{Info: info(real, st), Content: b}, nil
}

// fresh checks the read-before-edit stamp. Caller holds l.mu.
func (l *Local) fresh(agent, real, shown string, st iofs.FileInfo) error {
	s, ok := l.seen[key{agent, real}]
	if !ok {
		return fmt.Errorf("%w: %s", fs.ErrNotRead, shown)
	}
	if !s.mod.Equal(st.ModTime()) || s.size != st.Size() {
		return fmt.Errorf("%w: %s (modified %s)", fs.ErrStale, shown, st.ModTime().Format(time.RFC3339))
	}
	return nil
}

func (l *Local) intent(ctx context.Context, in *fs.Intent) error {
	if l.d == nil {
		return nil
	}
	reached := false
	out, err := fs.WriteIntent.Run(kernel.For(l.d, in.Agent), ctx, in, func(_ context.Context, x *fs.Intent) (*fs.Intent, error) {
		reached = true
		return x, nil
	})
	switch {
	case err != nil:
		return fmt.Errorf("%w: %v", fs.ErrVetoed, err)
	case out == nil:
		return fmt.Errorf("%w: %s %s", fs.ErrVetoed, in.Op, in.Path)
	case out.Deny != "":
		return fmt.Errorf("%w: %s", fs.ErrVetoed, out.Deny)
	case !reached:
		return fmt.Errorf("%w: %s %s (a policy listener stopped the chain)", fs.ErrVetoed, in.Op, in.Path)
	}
	return nil
}

// writeFile writes atomically, keeping the existing file mode.
func writeFile(real string, content []byte, existing iofs.FileInfo) error {
	dir := filepath.Dir(real)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	mode := iofs.FileMode(0o644)
	if existing != nil {
		mode = existing.Mode().Perm()
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(real)+".crewbase-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		return err
	}
	return os.Rename(name, real)
}

func (l *Local) Write(ctx context.Context, a fs.Access, p string, content []byte) (fs.WriteResult, error) {
	if a.ReadOnly {
		return fs.WriteResult{}, fmt.Errorf("%w: write %s", fs.ErrReadOnly, p)
	}
	real, err := l.resolve(a, p, true)
	if err != nil {
		return fs.WriteResult{}, err
	}
	if err := protect(a, p, real); err != nil {
		return fs.WriteResult{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	st, err := os.Stat(real)
	exists := err == nil
	if exists && st.IsDir() {
		return fs.WriteResult{}, fmt.Errorf("%w: %s", fs.ErrIsDir, p)
	}
	if exists && !l.blind {
		if err := l.fresh(a.Agent, real, p, st); err != nil {
			return fs.WriteResult{}, err
		}
	}
	in := &fs.Intent{Agent: a.Agent, Op: fs.OpWrite, Path: real, Exists: exists, Size: len(content)}
	if exists && st.Size() <= l.maxRead {
		if old, err := os.ReadFile(real); err == nil {
			in.Lines = fs.ChangedLines(old, content)
		}
	}
	if err := l.intent(ctx, in); err != nil {
		return fs.WriteResult{}, err
	}
	var prev iofs.FileInfo
	if exists {
		prev = st
	}
	if err := writeFile(real, content, prev); err != nil {
		return fs.WriteResult{}, err
	}
	if st, err := os.Stat(real); err == nil {
		l.remember(a.Agent, real, st)
	}
	return fs.WriteResult{Path: real, Created: !exists, Bytes: len(content)}, nil
}

func (l *Local) Edit(ctx context.Context, a fs.Access, p, old, new string) (fs.WriteResult, error) {
	if a.ReadOnly {
		return fs.WriteResult{}, fmt.Errorf("%w: edit %s", fs.ErrReadOnly, p)
	}
	if old == "" {
		return fs.WriteResult{}, errors.New("old text must not be empty (use write to create a file)")
	}
	if old == new {
		return fs.WriteResult{}, errors.New("old and new text are identical")
	}
	real, err := l.resolve(a, p, true)
	if err != nil {
		return fs.WriteResult{}, err
	}
	if err := protect(a, p, real); err != nil {
		return fs.WriteResult{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	st, err := os.Stat(real)
	if err != nil {
		return fs.WriteResult{}, notFound(err, p)
	}
	if st.IsDir() {
		return fs.WriteResult{}, fmt.Errorf("%w: %s", fs.ErrIsDir, p)
	}
	if err := l.fresh(a.Agent, real, p, st); err != nil {
		return fs.WriteResult{}, err
	}
	b, err := os.ReadFile(real)
	if err != nil {
		return fs.WriteResult{}, err
	}
	switch n := bytes.Count(b, []byte(old)); {
	case n == 0:
		return fs.WriteResult{}, fmt.Errorf("%w: %s", fs.ErrNoMatch, p)
	case n > 1:
		return fs.WriteResult{}, fmt.Errorf("%w (%d matches in %s)", fs.ErrNotUnique, n, p)
	}
	out := bytes.Replace(b, []byte(old), []byte(new), 1)
	in := &fs.Intent{Agent: a.Agent, Op: fs.OpEdit, Path: real, Exists: true, Size: len(out), Lines: fs.EditLines(b, bytes.Index(b, []byte(old)), len(old))}
	if err := l.intent(ctx, in); err != nil {
		return fs.WriteResult{}, err
	}
	if err := writeFile(real, out, st); err != nil {
		return fs.WriteResult{}, err
	}
	if st, err := os.Stat(real); err == nil {
		l.remember(a.Agent, real, st)
	}
	return fs.WriteResult{Path: real, Bytes: len(out), Replacements: 1}, nil
}

func (l *Local) List(_ context.Context, a fs.Access, p string) ([]fs.Info, error) {
	real, err := l.resolve(a, p, true)
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(real)
	if err != nil {
		return nil, notFound(err, p)
	}
	out := make([]fs.Info, 0, len(ents))
	for _, e := range ents {
		st, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, info(filepath.Join(real, e.Name()), st))
	}
	return out, nil
}

func (l *Local) Delete(ctx context.Context, a fs.Access, p string, recursive bool) error {
	if a.ReadOnly {
		return fmt.Errorf("%w: delete %s", fs.ErrReadOnly, p)
	}
	real, err := l.resolve(a, p, false)
	if err != nil {
		return err
	}
	if l.isRoot(a, real) {
		return fmt.Errorf("refusing to delete a root directory: %s", p)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	st, err := os.Lstat(real)
	if err != nil {
		return notFound(err, p)
	}
	if st.IsDir() && !recursive {
		ents, _ := os.ReadDir(real)
		if len(ents) > 0 {
			return fmt.Errorf("%s is a non-empty directory; pass recursive to delete it", p)
		}
	}
	if err := l.intent(ctx, &fs.Intent{Agent: a.Agent, Op: fs.OpDelete, Path: real, Exists: true}); err != nil {
		return err
	}
	if st.IsDir() && recursive {
		err = os.RemoveAll(real)
	} else {
		err = os.Remove(real)
	}
	if err != nil {
		return err
	}
	for k := range l.seen {
		if k.path == real || within(k.path, real) {
			delete(l.seen, k)
		}
	}
	return nil
}

func (l *Local) Stat(_ context.Context, a fs.Access, p string) (fs.Info, error) {
	real, err := l.resolve(a, p, true)
	if err != nil {
		return fs.Info{}, err
	}
	st, err := os.Stat(real)
	if err != nil {
		return fs.Info{}, notFound(err, p)
	}
	return info(real, st), nil
}

var _ fs.FS = (*Local)(nil)

// Plugin provides `fs` on the local disk.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/fs-local",
	Description: "Local file system confined to allowed roots, with read-before-edit tracking and fs/write-intent.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		l, err := New(c, cfg)
		if err != nil {
			return err
		}
		return c.Provide(fs.ServiceKey, l)
	},
})
