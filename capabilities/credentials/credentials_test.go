package credentials_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestVaultRoundTrip(t *testing.T) {
	root := t.TempDir()
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	if _, err := h.Load(credentials.Plugin, kernel.LoadOptions{Config: map[string]any{"root": root}}); err != nil {
		t.Fatal(err)
	}
	s := kernel.Use[*credentials.Service](h.Root(), credentials.ServiceKey)
	ctx := context.Background()
	secret := `sk-vault "quoted" \back $(x) 密钥`
	if _, err := s.Resolve(ctx, "vault:crewbase/openai"); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.Set("vault:crewbase/openai", secret); err != nil {
		t.Fatal(err)
	}
	// the name configs used before the vault reads the same item
	if v, err := s.Resolve(ctx, "keychain:crewbase/openai"); err != nil || v != secret {
		t.Fatalf("keychain alias: %v", err)
	}
	if err := s.Set("keychain:crewbase/openai", secret+"-2"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Resolve(ctx, "vault:crewbase/openai"); v != secret+"-2" {
		t.Fatal("replace failed")
	}
	if info := s.Describe("vault:crewbase/openai"); !info.Configured || info.Source != "vault" || strings.Contains(info.Masked, "quoted") {
		t.Fatalf("%+v", info)
	}

	// on disk: owner-only files, no plaintext
	dir := filepath.Join(root, credentials.VaultDir)
	for _, f := range []string{"vault.json", "master.key"} {
		st, err := os.Stat(filepath.Join(dir, f))
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", f, err, st)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "vault.json"))
	if strings.Contains(string(raw), "quoted") || strings.Contains(string(raw), "sk-vault") {
		t.Fatal("plaintext in vault.json")
	}

	// a sealed value moved to another id does not open
	var d map[string]any
	_ = json.Unmarshal(raw, &d)
	items := d["items"].(map[string]any)
	items["crewbase/other"] = items["crewbase/openai"]
	b, _ := json.Marshal(d)
	if err := os.WriteFile(filepath.Join(dir, "vault.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	_ = os.Chtimes(filepath.Join(dir, "vault.json"), time.Now(), time.Now()) // seen as changed
	if _, err := s.Resolve(ctx, "vault:crewbase/other"); err == nil {
		t.Fatal("a value moved to another id opened")
	}

	// another process (a second open) sees the same items
	v2, err := credentials.OpenVault(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := v2.Get(ctx, "crewbase/openai"); got != secret+"-2" {
		t.Fatal("second open differs")
	}
	if list, _ := v2.List(); len(list) != 2 || list[0].ID != "crewbase/openai" || list[0].Masked == "" || list[1].Masked != "" {
		t.Fatalf("list %+v", list)
	}

	if err := s.Delete("vault:crewbase/openai"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve(ctx, "vault:crewbase/openai"); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatal(err)
	}
	// ids are checked before they are stored
	if err := s.Set("vault:crewbase/bad id", "x"); !errors.Is(err, credentials.ErrUnsupported) {
		t.Fatal(err)
	}

	// a vault whose key is gone is not silently replaced
	if err := os.Remove(filepath.Join(dir, "master.key")); err != nil {
		t.Fatal(err)
	}
	if _, err := credentials.OpenVault(dir); !errors.Is(err, credentials.ErrVaultLocked) {
		t.Fatalf("want locked, got %v", err)
	}
}
