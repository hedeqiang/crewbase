package git

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/fs"
	"github.com/hedeqiang/crewbase/capabilities/vcs"
	"github.com/hedeqiang/crewbase/kernel"
)

// LockRequest freezes a file or lines of it.
type LockRequest struct {
	Repo   string // vcs.RepoInfo.ID
	Path   string // relative to the repository root
	Start  int
	End    int
	Holder string // actor
	Reason string
}

// AddLock freezes a range. Overlapping another holder's lock is refused.
func (s *Service) AddLock(r LockRequest) (Lock, error) {
	p, err := cleanRel(r.Path)
	if err != nil {
		return Lock{}, err
	}
	if r.Start < 0 || r.End < 0 || (r.End > 0 && r.Start == 0) || (r.Start > 0 && r.End < r.Start) {
		return Lock{}, fmt.Errorf("lines %d-%d: give start ≤ end (both 0 for the whole file)", r.Start, r.End)
	}
	l := Lock{ID: newID("lk"), Repo: r.Repo, Path: p, Start: r.Start, End: r.End, Holder: r.Holder, Reason: trunc(strings.TrimSpace(r.Reason), 300), Created: time.Now().UTC()}
	s.st.mu.Lock()
	for _, x := range s.st.st.Locks {
		if x.Repo == l.Repo && x.Path == l.Path && x.Holder != l.Holder && overlaps(*x, l.Start, l.End) {
			s.st.mu.Unlock()
			return Lock{}, fail(CodeLockFrozen, 409, "%s is already frozen by %s (%s)", span(*x), s.name(x.Holder), x.Reason)
		}
	}
	s.st.st.Locks = append(s.st.st.Locks, &l)
	err = s.st.save()
	s.st.mu.Unlock()
	if err != nil {
		return Lock{}, err
	}
	s.changed(vcs.Event{Type: "git/lock.added", Repo: l.Repo, ID: l.ID, Actor: l.Holder, Data: l})
	return l, nil
}

// RemoveLock lifts a lock: its holder may (or its side), and so may a person.
func (s *Service) RemoveLock(id, actor string) error {
	var found *Lock
	for _, l := range s.Locks("") {
		if l.ID == id {
			found = &l
			break
		}
	}
	if found == nil {
		return ErrNotFound
	}
	if !strings.HasPrefix(actor, "person:") && !s.sameOrigin(actor, found.Holder) {
		return fail(CodeLockFrozen, 403, "only %s (or a person) can lift this lock", s.name(found.Holder))
	}
	s.st.mu.Lock()
	for i, l := range s.st.st.Locks {
		if l.ID == id {
			s.st.st.Locks = append(s.st.st.Locks[:i], s.st.st.Locks[i+1:]...)
			break
		}
	}
	err := s.st.save()
	s.st.mu.Unlock()
	if err == nil {
		s.changed(vcs.Event{Type: "git/lock.removed", Repo: found.Repo, ID: id, Actor: actor, Data: *found})
	}
	return err
}

// Locks are the locks of a repository ("" = all).
func (s *Service) Locks(repo string) []Lock {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	out := []Lock{}
	for _, l := range s.st.st.Locks {
		if repo == "" || l.Repo == repo {
			out = append(out, *l)
		}
	}
	return out
}

func (s *Service) anyLocks() bool {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	return len(s.st.st.Locks) > 0
}

func span(l Lock) string {
	if l.Whole() {
		return l.Path
	}
	return fmt.Sprintf("%s:%d-%d", l.Path, l.Start, l.End)
}

// overlaps reports whether lines a-b (an insertion when b < a) fall in l.
func overlaps(l Lock, a, b int) bool {
	if l.Whole() || (a <= 0 && b <= 0) {
		return true
	}
	if b < a { // an insertion before line a: inside when between two locked lines
		return a > l.Start && a <= l.End
	}
	return a <= l.End && b >= l.Start
}

func cleanRel(p string) (string, error) {
	p = filepath.ToSlash(filepath.Clean(strings.TrimSpace(p)))
	if p == "." || p == "" || strings.HasPrefix(p, "../") || p == ".." || strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("%q is not a path inside the repository", p)
	}
	return p, nil
}

// frozen is the lock that stops actor changing lines of rel in repo (nil
// when free). Its holder, and whoever works for the same origin, pass.
func (s *Service) frozen(repo, rel, actor string, ranges [][2]int, prefix bool) *Lock {
	s.st.mu.Lock()
	var hits []Lock
	for _, l := range s.st.st.Locks {
		if l.Repo != repo {
			continue
		}
		if l.Path == rel || (prefix && strings.HasPrefix(l.Path, rel+"/")) {
			hits = append(hits, *l)
		}
	}
	s.st.mu.Unlock()
	for _, l := range hits {
		if l.Holder == actor || s.sameOrigin(l.Holder, actor) {
			continue
		}
		if prefix || len(ranges) == 0 {
			return &l
		}
		for _, r := range ranges {
			if overlaps(l, r[0], r[1]) {
				return &l
			}
		}
	}
	return nil
}

// writeIntent is the fs.WriteIntent middleware: a write that touches a
// frozen range is refused before it happens.
func (s *Service) writeIntent(ctx context.Context, in *fs.Intent, next kernel.Next[*fs.Intent, *fs.Intent]) (*fs.Intent, error) {
	if in == nil || in.Agent == "" || !in.Exists || !s.anyLocks() {
		return next(ctx, in)
	}
	info, rel, ok := s.locate(ctx, in.Path)
	if !ok {
		return next(ctx, in)
	}
	if l := s.frozen(info.ID, rel, agentActor(in.Agent), in.Lines, in.Op == fs.OpDelete); l != nil {
		why := ""
		if l.Reason != "" {
			why = " (" + l.Reason + ")"
		}
		in.Deny = fmt.Sprintf("%s: %s is frozen by %s%s; ask them to lift the lock, or change other lines", CodeLockFrozen, span(*l), s.name(l.Holder), why)
		s.changed(vcs.Event{Type: "git/lock.blocked", Repo: info.ID, ID: l.ID, Actor: agentActor(in.Agent), Data: map[string]any{"path": rel, "lines": in.Lines, "op": in.Op}})
		return in, nil
	}
	return next(ctx, in)
}

// locate is the repository a file lies in and its path there.
func (s *Service) locate(ctx context.Context, p string) (vcs.RepoInfo, string, bool) {
	dir := filepath.Dir(p)
	info, err := s.Repo(ctx, dir)
	if err != nil {
		return vcs.RepoInfo{}, "", false
	}
	rel, err := filepath.Rel(info.Root, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return vcs.RepoInfo{}, "", false
	}
	return info, filepath.ToSlash(rel), true
}

// preMerge refuses a merge that changes lines someone else froze.
func (s *Service) preMerge(ctx context.Context, c *vcs.PreMergeCheck, next kernel.Next[*vcs.PreMergeCheck, *vcs.PreMergeCheck]) (*vcs.PreMergeCheck, error) {
	if c == nil || !s.anyLocks() {
		return next(ctx, c)
	}
	for _, f := range c.Changed {
		var ranges [][2]int
		for _, h := range f.Hunks {
			ranges = append(ranges, hunkRange(h))
		}
		if f.Status == vcs.StDeleted || f.Status == vcs.StAdded {
			ranges = nil // the whole file
		}
		who := c.Author
		if who == "" {
			who = c.Actor
		}
		if l := s.frozen(c.Repo, f.Path, who, ranges, false); l != nil {
			c.Deny = fmt.Sprintf("%s is frozen by %s; the merge would change it", span(*l), s.name(l.Holder))
			c.Code = CodeLockFrozen
			return c, nil
		}
	}
	return next(ctx, c)
}

// hunkRange is the old-side lines a hunk changes.
func hunkRange(h vcs.Hunk) [2]int {
	lo, hi := 0, 0
	at := h.OldStart
	for _, l := range h.Lines {
		switch l.Kind {
		case "-":
			if lo == 0 {
				lo = l.Old
			}
			hi = l.Old
			at = l.Old + 1
		case "+":
			if lo == 0 {
				lo, hi = at, at-1 // an insertion before line at
			}
		default:
			at = l.Old + 1
		}
	}
	if lo == 0 {
		return [2]int{h.OldStart, h.OldStart + max(h.OldLines, 1) - 1}
	}
	return [2]int{lo, max(hi, lo-1)}
}
