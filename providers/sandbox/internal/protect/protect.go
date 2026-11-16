// Package protect plans how the mount-based sandboxes (bubblewrap, docker)
// keep a policy's ProtectedRoots read-only inside a writable root.
//
// A protected path that exists is bound read-only over itself. One that does
// not exist must not be creatable either, but a mount cannot block a name
// without something to mount on: the sandbox mounts an empty read-only
// tmpfs there, and creating that mount point leaves an empty directory in
// the host folder (the writable root is the host folder). Nothing is ever
// written into it, so it holds no configuration a CLI could load. The
// Registry removes these placeholder directories again once the last
// command using them has finished (Confined.Release), counting concurrent
// commands so one command's cleanup never pulls the mount point from under
// another one still running.
//
// Placeholders are always directories, also for file names like .mcp.json:
// inside the sandbox creating or writing the name fails either way (EROFS /
// EISDIR), and a tmpfs needs no host source file.
package protect

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// Plan is what a provider mounts for one command.
type Plan struct {
	// Present are protected paths that exist: bind each read-only onto itself.
	Present []string
	// Missing are protected names that do not exist (or are the placeholders
	// of a running command): mount an empty read-only tmpfs on each.
	Missing []string
}

// Within reports whether p is root or lies inside it (clean absolute paths).
func Within(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Registry tracks the placeholder directories mounted by running commands.
// The zero value is ready to use.
type Registry struct {
	mu   sync.Mutex
	held map[string]int
}

// Plan classifies the protected paths (already normalized by the caller)
// that lie inside (or contain) one of writable. Paths away from every
// writable root are
// left out: the rest of the file system is read-only (or not visible)
// anyway. Duplicates are dropped. A missing path whose parent directory
// does not exist is left out as well: the sandbox could not mount there,
// and the command cannot create the parent outside a writable root.
//
// With acquire, the missing paths count as held until the returned release
// runs (it is safe to call more than once); release is nil when nothing was
// acquired.
func (r *Registry) Plan(protected, writable []string, acquire bool) (Plan, func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var pl Plan
	seen := map[string]bool{}
	for _, p := range protected {
		if p == "" || p == "/" || seen[p] {
			continue
		}
		seen[p] = true
		inside := false
		for _, w := range writable {
			// A protected root that contains a writable one covers it too:
			// protected wins, as in the seatbelt profile.
			if w != "" && (Within(p, w) || Within(w, p)) {
				inside = true
				break
			}
		}
		if !inside {
			continue
		}
		if r.held[p] == 0 {
			if _, err := os.Lstat(p); err == nil {
				pl.Present = append(pl.Present, p)
				continue
			}
			if st, err := os.Stat(filepath.Dir(p)); err != nil || !st.IsDir() {
				continue
			}
		}
		pl.Missing = append(pl.Missing, p)
	}
	if !acquire || len(pl.Missing) == 0 {
		return pl, nil
	}
	if r.held == nil {
		r.held = map[string]int{}
	}
	paths := append([]string(nil), pl.Missing...)
	for _, p := range paths {
		r.held[p]++
	}
	var once sync.Once
	return pl, func() { once.Do(func() { r.release(paths) }) }
}

// Held reports how many running commands hold p as a placeholder.
func (r *Registry) Held(p string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.held[p]
}

// release drops one hold on each path and removes the placeholders no
// running command uses any more. Only an empty directory is removed: if
// someone put real content there meanwhile, it stays.
func (r *Registry) release(paths []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range paths {
		r.held[p]--
		if r.held[p] > 0 {
			continue
		}
		delete(r.held, p)
		if st, err := os.Lstat(p); err == nil && st.IsDir() {
			_ = syscall.Rmdir(p) // fails, harmlessly, unless empty
		}
	}
}
