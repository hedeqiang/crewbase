package credentials

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// Keychain entries are per data root. Refs in config files stay
// "keychain:crewbase/<account>"; the backend stores them under the service
// "<service>-<root id>" for every root except ~/.crewbase, so a second
// install or a test root can neither read nor delete the main root's
// logins and keys. ~/.crewbase keeps the plain names (nothing to migrate).
//
// A root that already had data before scoping existed reads an entry it
// lacks from the old shared name once and copies it to its own name
// (never deleting the old one, which another root may own). Accounts that
// were migrated, written or deleted are recorded so the old name is never
// consulted for them again. A new root never looks at the old names.

const scopeFile = "keychain-scope.json"

type scopeState struct {
	Root string `json:"root"`
	// Legacy is true when the root had data from before scoping.
	Legacy bool `json:"legacy"`
	// Done lists "<service>/<account>" ids that no longer fall back.
	Done []string `json:"done,omitempty"`
}

// ScopedBackend stores keychain ids under a root-specific service name.
type ScopedBackend struct {
	inner  Backend
	suffix string // "-<8 hex>"
	path   string // <root>/keychain-scope.json

	mu    sync.Mutex
	state scopeState
}

// RootID is the short id that scopes a data root's keychain entries.
func RootID(root string) string {
	sum := sha256.Sum256([]byte(cleanRoot(root)))
	return hex.EncodeToString(sum[:4])
}

func cleanRoot(root string) string {
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	return filepath.Clean(root)
}

// isMainRoot reports whether root is ~/.crewbase, whose entries keep their
// plain names.
func isMainRoot(root string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	return cleanRoot(root) == cleanRoot(filepath.Join(home, ".crewbase"))
}

// hadData reports whether root holds data written before this version.
// Plugins loaded earlier may already have created empty directories, so
// only non-empty ones count.
func hadData(root string) bool {
	if _, err := os.Stat(filepath.Join(root, "providers.json")); err == nil {
		return true
	}
	for _, n := range []string{"sessions", "agents", "workspaces"} {
		if es, err := os.ReadDir(filepath.Join(root, n)); err == nil && len(es) > 0 {
			return true
		}
	}
	return false
}

// Scope wraps a keychain backend for root, or returns it unchanged for
// ~/.crewbase.
func Scope(inner Backend, root string) Backend {
	if inner == nil || root == "" || isMainRoot(root) {
		return inner
	}
	s := &ScopedBackend{inner: inner, suffix: "-" + RootID(root), path: filepath.Join(root, scopeFile)}
	b, err := os.ReadFile(s.path)
	if err == nil && json.Unmarshal(b, &s.state) == nil {
		return s
	}
	s.state = scopeState{Root: cleanRoot(root), Legacy: hadData(root)}
	s.save()
	return s
}

func (s *ScopedBackend) Name() string { return s.inner.Name() }

func (s *ScopedBackend) own(id string) string {
	svc, acct, ok := strings.Cut(id, "/")
	if !ok {
		return id // let the inner backend reject it
	}
	return svc + s.suffix + "/" + acct
}

func (s *ScopedBackend) done(id string) bool { return slices.Contains(s.state.Done, id) }

// markDone stops the fallback for id; callers hold s.mu.
func (s *ScopedBackend) markDone(id string) {
	if !s.state.Legacy || s.done(id) {
		return
	}
	s.state.Done = append(s.state.Done, id)
	s.save()
}

func (s *ScopedBackend) save() {
	b, _ := json.MarshalIndent(s.state, "", "  ")
	_ = os.MkdirAll(filepath.Dir(s.path), 0o700)
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, s.path)
	}
}

func (s *ScopedBackend) Get(ctx context.Context, id string) (string, error) {
	v, err := s.inner.Get(ctx, s.own(id))
	if err == nil || !errors.Is(err, ErrNotFound) {
		return v, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.state.Legacy || s.done(id) {
		return "", ErrNotFound
	}
	old, err := s.inner.Get(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			s.markDone(id)
		}
		return "", err
	}
	if err := s.inner.Set(ctx, s.own(id), old); err != nil {
		return old, nil // still usable; try copying again next time
	}
	s.markDone(id)
	return old, nil
}

func (s *ScopedBackend) Set(ctx context.Context, id, value string) error {
	if err := s.inner.Set(ctx, s.own(id), value); err != nil {
		return err
	}
	s.mu.Lock()
	s.markDone(id)
	s.mu.Unlock()
	return nil
}

func (s *ScopedBackend) Delete(ctx context.Context, id string) error {
	if err := s.inner.Delete(ctx, s.own(id)); err != nil {
		return err
	}
	s.mu.Lock()
	s.markDone(id) // the old shared entry may belong to another root: keep it
	s.mu.Unlock()
	return nil
}
