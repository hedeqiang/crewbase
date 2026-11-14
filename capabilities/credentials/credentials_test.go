package credentials_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/kernel"
)

func TestMemoryBackend(t *testing.T) {
	s := credentials.New()
	ctx := context.Background()
	if _, err := s.Resolve(ctx, "mem:openai"); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatal(err)
	}
	if info := s.Describe("mem:openai"); info.Configured || info.Source != "memory" || info.Masked != "" {
		t.Fatalf("%+v", info)
	}
	secret := "sk-test-0123456789abcdefa1b2"
	if err := s.Set("mem:openai", secret); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Resolve(ctx, "mem:openai"); err != nil || v != secret {
		t.Fatal(v, err)
	}
	info := s.Describe("mem:openai")
	if !info.Configured || info.Masked != "sk-…a1b2" || strings.Contains(info.Masked, "0123456789") {
		t.Fatalf("%+v", info)
	}
	if !slices.Contains(s.Secrets(), secret) {
		t.Fatal("secret not tracked")
	}
	// empty value = not found
	if err := s.Set("mem:openai", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve(ctx, "mem:openai"); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.Delete("mem:openai"); err != nil {
		t.Fatal("deleting missing ref is fine", err)
	}
	// bad refs
	for _, ref := range []string{"", "mem:", "nope", "vault:x"} {
		if _, err := s.Resolve(ctx, ref); !errors.Is(err, credentials.ErrUnsupported) {
			t.Fatal(ref, err)
		}
	}
}

func TestEnvBackend(t *testing.T) {
	t.Setenv("CREWBASE_TEST_TOKEN", "ghp_abcdefghijklmnopqrstuvwxyz")
	t.Setenv("CREWBASE_TEST_EMPTY", "")
	s := credentials.New()
	if v, err := s.Resolve(context.Background(), "env:CREWBASE_TEST_TOKEN"); err != nil || v != "ghp_abcdefghijklmnopqrstuvwxyz" {
		t.Fatal(v, err)
	}
	if _, err := s.Resolve(context.Background(), "env:CREWBASE_TEST_EMPTY"); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatal(err)
	}
	if info := s.Describe("env:CREWBASE_TEST_TOKEN"); !info.Configured || info.Source != "env" || info.Masked != "ghp…wxyz" {
		t.Fatalf("%+v", info)
	}
	if err := s.Set("env:CREWBASE_TEST_TOKEN", "x"); !errors.Is(err, credentials.ErrReadOnly) {
		t.Fatal(err)
	}
}

func TestMask(t *testing.T) {
	for in, want := range map[string]string{"": "…", "short": "…", "abcdefghijkl": "ab…ijkl", "abcdefghijklmnop": "abc…mnop"} {
		if got := credentials.Mask(in); got != want {
			t.Errorf("Mask(%q)=%q want %q", in, got, want)
		}
	}
}

func TestRefreshLockedSerializes(t *testing.T) {
	s := credentials.New()
	s.Set("mem:tok", "v0")
	refreshes := 0
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.RefreshLocked("mem:tok", func(old string) (string, error) {
				// only the first caller sees the stale token and refreshes
				if old == "v0" {
					refreshes++
					return "v1-refreshed", nil
				}
				return old, nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if refreshes != 1 {
		t.Fatal("refreshed", refreshes)
	}
	if v, _ := s.Resolve(context.Background(), "mem:tok"); v != "v1-refreshed" {
		t.Fatal(v)
	}
	// error aborts without writing
	boom := errors.New("boom")
	if err := s.RefreshLocked("mem:tok", func(string) (string, error) { return "v2", boom }); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if v, _ := s.Resolve(context.Background(), "mem:tok"); v != "v1-refreshed" {
		t.Fatal(v)
	}
	// absent ref: old is ""
	s.RefreshLocked("mem:new", func(old string) (string, error) {
		if old != "" {
			t.Fatal(old)
		}
		return "fresh-token", nil
	})
	if v, _ := s.Resolve(context.Background(), "mem:new"); v != "fresh-token" {
		t.Fatal(v)
	}
}

func TestPlugin(t *testing.T) {
	h := kernel.NewHost(nil)
	defer h.Close()
	if _, err := h.Load(credentials.Plugin, kernel.LoadOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := kernel.Lookup[*credentials.Service](h.Root(), credentials.ServiceKey); !ok {
		t.Fatal("service missing")
	}
}

func TestKeychainRoundTrip(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin only")
	}
	if _, err := exec.LookPath("security"); err != nil {
		t.Skip("security CLI missing")
	}
	b := make([]byte, 8)
	rand.Read(b)
	id := "test-" + hex.EncodeToString(b)
	ref := "keychain:crewbase/" + id
	s := credentials.New()
	t.Cleanup(func() { _ = s.Delete(ref) })

	if _, err := s.Resolve(context.Background(), ref); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatal(err)
	}
	secret := `sk-kc "quoted" \back $(x) ` + hex.EncodeToString(b)
	if err := s.Set(ref, secret); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Resolve(context.Background(), ref); err != nil || v != secret {
		t.Fatalf("round trip mismatch (err=%v)", err)
	}
	// update in place (-U)
	if err := s.Set(ref, secret+"-2"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Resolve(context.Background(), ref); v != secret+"-2" {
		t.Fatal("update failed")
	}
	if info := s.Describe(ref); !info.Configured || info.Source != "macos-keychain" {
		t.Fatalf("%+v", info)
	}
	if err := s.Delete(ref); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve(context.Background(), ref); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatal(err)
	}
	// invalid ids never reach the CLI
	if err := s.Set("keychain:crewbase/bad id", "x"); !errors.Is(err, credentials.ErrUnsupported) {
		t.Fatal(err)
	}
}
