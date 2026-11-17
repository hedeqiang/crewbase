//go:build !windows

package acp

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
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/kernel"
)

// memStore stands in for the keychain.
type memStore struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *memStore) Set(ref, v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[ref] = v
	return nil
}

func (s *memStore) Resolve(_ context.Context, ref string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[ref]
	if !ok {
		return "", credentials.ErrNotFound
	}
	return v, nil
}

func (s *memStore) Delete(ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, ref)
	return nil
}

// loadACP starts the llm registry and the acp plugin on a fresh host.
func loadACP(t *testing.T, cfg map[string]any) (*kernel.Host, *llm.Registry, *Service) {
	t.Helper()
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	if _, err := h.Load(llm.Plugin, kernel.LoadOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Load(Plugin, kernel.LoadOptions{Config: cfg}); err != nil {
		t.Fatal(err)
	}
	return h, kernel.Use[*llm.Registry](h.Root(), llm.ServiceKey), kernel.Use[*Service](h.Root(), ServiceKey)
}

func modelIDs(t *testing.T, reg *llm.Registry, provider string) []string {
	t.Helper()
	a, ok := reg.Get(provider)
	if !ok {
		t.Fatalf("%s not registered", provider)
	}
	ms, _ := a.Models(context.Background())
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	return ids
}

func ctx30(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// A custom CLI is added (probed at once), edited, disabled, enabled and
// removed, each applied without a restart; env values stay out of the
// overrides file and the API.
func TestManageCustomCLI(t *testing.T) {
	state := t.TempDir()
	t.Setenv("PATH", t.TempDir()) // no built-in CLI is installed
	ctx := ctx30(t)
	cfg := map[string]any{"stateDir": state, "idleTimeoutSec": -1}
	_, reg, svc := loadACP(t, cfg)
	store := &memStore{m: map[string]string{}}
	svc.secrets = func() secretStore { return store }

	const secret = "tok-0123456789abcdef"
	v, err := svc.Add(ctx, NewCLI{ID: "My-Agent", Name: "My Agent", Command: []string{os.Args[0]}, Env: map[string]string{fakeEnv: "1", "TOKEN": secret}})
	if err != nil {
		t.Fatal(err)
	}
	if v.ID != "my-agent" || v.Kind != "custom" || !v.Removable || !v.Enabled || !v.Active || v.Error != "" {
		t.Fatalf("added %+v", v)
	}
	if ids := modelIDs(t, reg, "acp-my-agent"); !slices.Contains(ids, "fast") || !slices.Contains(ids, "smart") {
		t.Fatalf("not probed on add: %v", ids)
	}
	b, _ := os.ReadFile(filepath.Join(state, overridesFile))
	if strings.Contains(string(b), secret) || !strings.Contains(string(b), "TOKEN") {
		t.Fatalf("overrides file: %s", b)
	}
	if got, _ := json.Marshal(svc.List(ctx)); strings.Contains(string(got), secret) {
		t.Fatal("list echoes a secret")
	}

	// clashes and bad input
	for _, in := range []NewCLI{
		{ID: "my-agent", Command: []string{os.Args[0]}},
		{ID: "codex", Command: []string{os.Args[0]}},
		{ID: "bad id!", Command: []string{os.Args[0]}},
		{ID: "nobin", Command: []string{"crewbase-no-such-binary"}},
		{ID: "nocmd"},
		{ID: "badenv", Command: []string{os.Args[0]}, Env: map[string]string{"1X": "y"}},
	} {
		if _, err := svc.Add(ctx, in); err == nil {
			t.Errorf("add %+v: no error", in)
		}
	}

	// edit: new name, env with the secret kept (null value), re-registered
	before, _ := reg.Get("acp-my-agent")
	name := "Renamed"
	env := map[string]*string{fakeEnv: nil, "TOKEN": nil, "EXTRA": ptr("x")}
	v, err = svc.Update(ctx, "my-agent", Patch{Name: &name, Env: &env})
	if err != nil {
		t.Fatal(err)
	}
	after, ok := reg.Get("acp-my-agent")
	if !ok || after == before || v.Name != "Renamed" || len(v.Env) != 3 {
		t.Fatalf("edit not applied: %+v", v)
	}
	if a, _ := svc.Adapter("my-agent"); a.cfg.Env["TOKEN"] != secret || a.cfg.Env["EXTRA"] != "x" {
		t.Fatalf("env %v", a.cfg.Env)
	}
	if _, err := svc.Update(ctx, "nope", Patch{Name: &name}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update unknown: %v", err)
	}

	// kept across a restart
	if _, reg2, _ := loadACP(t, cfg); !slices.Contains(modelIDs(t, reg2, "acp-my-agent"), "fast") {
		t.Fatal("custom CLI or its cached models not back after a restart")
	}

	// disable: unregistered, and Rescan leaves it off
	off := false
	if v, err = svc.Update(ctx, "my-agent", Patch{Enabled: &off}); err != nil || v.Enabled || v.Active {
		t.Fatalf("disable %+v %v", v, err)
	}
	if _, ok := reg.Get("acp-my-agent"); ok {
		t.Fatal("still registered when disabled")
	}
	if ids := svc.Rescan(); len(ids) != 0 {
		t.Fatalf("rescan re-enabled %v", ids)
	}
	on := true
	if v, err = svc.Update(ctx, "my-agent", Patch{Enabled: &on}); err != nil || !v.Active {
		t.Fatalf("enable %+v %v", v, err)
	}

	// remove: gone for good, secret deleted
	deleted, err := svc.Remove(ctx, "my-agent")
	if err != nil || !deleted {
		t.Fatalf("remove %v %v", deleted, err)
	}
	if _, ok := reg.Get("acp-my-agent"); ok {
		t.Fatal("registered after remove")
	}
	if _, ok := svc.Get(ctx, "my-agent"); ok {
		t.Fatal("listed after remove")
	}
	if len(store.m) != 0 {
		t.Fatalf("secret left behind: %v", store.m)
	}
	b, _ = os.ReadFile(filepath.Join(state, overridesFile))
	if strings.Contains(string(b), "my-agent") {
		t.Fatalf("overrides after remove: %s", b)
	}
}

func ptr[T any](v T) *T { return &v }

// Refresh learns the models now, ignoring a fresh cache entry, and the
// registry has the new list right away.
func TestRefreshBypassesCache(t *testing.T) {
	state := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	ov := map[string]any{"agents": []map[string]any{{"id": "fakecli", "custom": true, "command": []string{os.Args[0]}, "env": map[string]string{fakeEnv: "1"}}}}
	b, _ := json.Marshal(ov)
	os.WriteFile(filepath.Join(state, overridesFile), b, 0o600)
	writeModelCache(state, "fakecli", modelCache{Models: []acpModel{{ModelID: "stale", Name: "Stale"}}, Learned: time.Now()})

	_, reg, svc := loadACP(t, map[string]any{"stateDir": state, "idleTimeoutSec": -1})
	if ids := modelIDs(t, reg, "acp-fakecli"); !slices.Contains(ids, "stale") {
		t.Fatalf("cache not loaded: %v", ids)
	}
	v, err := svc.Refresh(ctx30(t), "fakecli")
	if err != nil {
		t.Fatal(err)
	}
	ids := modelIDs(t, reg, "acp-fakecli")
	if slices.Contains(ids, "stale") || !slices.Contains(ids, "fast") || v.Models != len(ids) {
		t.Fatalf("after refresh %v (%d)", ids, v.Models)
	}
	if c := readModelCache(state)["fakecli"]; len(c.Models) == 0 || c.Models[0].ModelID == "stale" {
		t.Fatalf("cache not rewritten: %+v", c)
	}
	if _, err := svc.Refresh(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("refresh unknown: %v", err)
	}
}

// Removing a built-in disables it: it stays listed, the periodic Rescan
// and a restart leave it off, and enabling brings it back.
func TestDisabledBuiltinStaysOff(t *testing.T) {
	state := t.TempDir()
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	os.WriteFile(filepath.Join(bin, "agy"), []byte("#!/bin/sh\necho 'agy 1.0.0'\n"), 0o755)
	ctx := ctx30(t)
	cfg := map[string]any{"stateDir": state, "idleTimeoutSec": -1}
	_, reg, svc := loadACP(t, cfg)
	if _, ok := reg.Get("acp-antigravity"); !ok {
		t.Fatal("not registered")
	}
	deleted, err := svc.Remove(ctx, "antigravity")
	if err != nil || deleted {
		t.Fatalf("remove built-in: deleted=%v err=%v", deleted, err)
	}
	if _, ok := reg.Get("acp-antigravity"); ok {
		t.Fatal("registered after disable")
	}
	if ids := svc.Rescan(); len(ids) != 0 {
		t.Fatalf("rescan re-enabled %v", ids)
	}
	v, ok := svc.Get(ctx, "antigravity")
	if !ok || v.Enabled || v.Active || v.Kind != "builtin" || v.Removable || !v.Installed {
		t.Fatalf("listed %+v", v)
	}
	if _, err := svc.Refresh(ctx, "antigravity"); err == nil {
		t.Fatal("refresh of a disabled CLI")
	}

	_, reg2, svc2 := loadACP(t, cfg)
	if _, ok := reg2.Get("acp-antigravity"); ok {
		t.Fatal("registered after restart")
	}
	on := true
	if v, err := svc2.Update(ctx, "antigravity", Patch{Enabled: &on}); err != nil || !v.Active {
		t.Fatalf("enable %+v %v", v, err)
	}
	if _, ok := reg2.Get("acp-antigravity"); !ok {
		t.Fatal("not registered after enable")
	}

	// a built-in's command can be set and reset
	cmd := []string{"agy", "acp", "--verbose"}
	v, err = svc2.Update(ctx, "antigravity", Patch{Command: &cmd})
	if err != nil || !slices.Equal(v.Command, cmd) || !strings.HasSuffix(strings.Join(v.ACPCommand, " "), "acp --verbose") {
		t.Fatalf("command %+v %v", v, err)
	}
	empty := []string{}
	if v, err = svc2.Update(ctx, "antigravity", Patch{Command: &empty}); err != nil || len(v.Command) != 0 || strings.Contains(strings.Join(v.ACPCommand, " "), "verbose") {
		t.Fatalf("reset %+v %v", v, err)
	}
	bad := []string{"crewbase-no-such-binary"}
	if _, err := svc2.Update(ctx, "antigravity", Patch{Command: &bad}); err == nil {
		t.Fatal("missing program accepted")
	}
}
