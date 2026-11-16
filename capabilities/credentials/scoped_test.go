package credentials

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Two roots never see or delete each other's keychain entries; the main
// root keeps the plain names.
func TestScopedKeychainPerRoot(t *testing.T) {
	ctx := context.Background()
	shared := NewMemory() // stands in for the system keychain
	shared.Set(ctx, "crewbase/provider-ds", "main-key")

	newRoot := t.TempDir()
	os.MkdirAll(filepath.Join(newRoot, "sessions"), 0o700) // created by an earlier plugin
	fresh := Scope(shared, newRoot)
	if _, err := fresh.Get(ctx, "crewbase/provider-ds"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a new root must not see the main root's key: %v", err)
	}
	fresh.Set(ctx, "crewbase/provider-ds", "test-key")
	fresh.Delete(ctx, "crewbase/provider-ds")
	if v, _ := shared.Get(ctx, "crewbase/provider-ds"); v != "main-key" {
		t.Fatalf("another root deleted or changed the main key: %q", v)
	}

	home, _ := os.UserHomeDir()
	if Scope(shared, filepath.Join(home, ".crewbase")) != Backend(shared) {
		t.Fatal("~/.crewbase must keep plain names")
	}
	if RootID("/a/b") == RootID("/a/c") {
		t.Fatal("root ids collide")
	}
}

// A root with data from before scoping copies an entry it lacks from the
// old shared name once, then never falls back for it again.
func TestScopedKeychainMigratesOldRoot(t *testing.T) {
	ctx := context.Background()
	shared := NewMemory()
	shared.Set(ctx, "crewbase/provider-ds", "old-key")
	shared.Set(ctx, "crewbase/github", "gh")
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "sessions", "chat-1"), 0o700)

	b := Scope(shared, root)
	if v, err := b.Get(ctx, "crewbase/provider-ds"); err != nil || v != "old-key" {
		t.Fatalf("migration read: %q %v", v, err)
	}
	if v, _ := shared.Get(ctx, "crewbase-"+RootID(root)+"/provider-ds"); v != "old-key" {
		t.Fatalf("not copied to the root's own name: %q", v)
	}
	// deleting removes only this root's copy, and the old entry does not come back
	b.Delete(ctx, "crewbase/provider-ds")
	if v, _ := shared.Get(ctx, "crewbase/provider-ds"); v != "old-key" {
		t.Fatal("old shared entry must be kept")
	}
	if _, err := b.Get(ctx, "crewbase/provider-ds"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted key came back through the fallback: %v", err)
	}
	// the record survives a restart
	b2 := Scope(shared, root)
	if _, err := b2.Get(ctx, "crewbase/provider-ds"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after restart: %v", err)
	}
	if v, _ := b2.Get(ctx, "crewbase/github"); v != "gh" {
		t.Fatalf("untouched account still migrates: %q", v)
	}
}
