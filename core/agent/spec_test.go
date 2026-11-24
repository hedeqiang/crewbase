package agent_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

// startAgents loads the agents plugin over root, as a (re)start does.
func startAgents(t *testing.T, root string) *agent.Registry {
	t.Helper()
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, map[string]any{"dir": root + "/sessions"}},
		{prompt.Plugin, nil},
		{tools.Plugin, nil},
		{llm.Plugin, nil},
		{agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"claude-code/default"}}},
	} {
		if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
			t.Fatal(err)
		}
	}
	return kernel.Use[*agent.Registry](h.Root(), agent.ServiceKey)
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// The spec is kept outside the agent's (writable) workspace, so editing a
// file there cannot change its run mode.
func TestSpecOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	reg := startAgents(t, root)
	a, err := reg.Create(agent.Spec{Name: "Kai"})
	if err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(root, "agents", "kai.json")) {
		t.Fatal("spec not in <root>/agents")
	}
	if exists(filepath.Join(a.Workspace(), "agent.json")) {
		t.Fatal("spec written into the workspace")
	}
	// what a sandboxed agent could write: ignored when a spec exists
	forged, _ := json.Marshal(agent.Spec{ID: "kai", Name: "Kai", Mode: agent.ModeHost, ModeUntil: time.Now().Add(time.Hour)})
	if err := os.WriteFile(filepath.Join(a.Workspace(), "agent.json"), forged, 0o644); err != nil {
		t.Fatal(err)
	}
	// and a forged ephemeral marker does not get the workspace swept
	_ = os.WriteFile(filepath.Join(a.Workspace(), ".ephemeral"), nil, 0o644)

	reg2 := startAgents(t, root)
	b, ok := reg2.Get("kai")
	if !ok {
		t.Fatal("agent not restored")
	}
	if b.Mode() != agent.ModeSandbox {
		t.Fatalf("mode %s", b.Mode())
	}
	if !exists(filepath.Join(b.Workspace(), "INSTRUCTIONS.md")) {
		t.Fatal("workspace swept")
	}

	// delete archives the spec with the workspace; it is not restored
	if err := reg2.Delete("kai"); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(root, "agents", "kai.json")) {
		t.Fatal("spec left behind")
	}
	reg3 := startAgents(t, root)
	if _, ok := reg3.Get("kai"); ok {
		t.Fatal("deleted agent restored")
	}
}

// A spec from an older version (inside the workspace) is moved out once,
// without trusting the privileges it claims: it may have been edited by
// the agent.
func TestLegacySpecMigration(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "workspaces")
	write := func(s agent.Spec) {
		t.Helper()
		_ = os.MkdirAll(filepath.Join(ws, s.ID), 0o755)
		b, _ := json.Marshal(s)
		if err := os.WriteFile(filepath.Join(ws, s.ID, "agent.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	models := []llm.ModelRef{{Provider: "claude-code", Model: "default"}}
	write(agent.Spec{ID: "kai", Name: "Kai", Mode: agent.ModeHost, ModeUntil: time.Now().Add(time.Hour), Models: models, Role: "writer", Created: time.Now()})
	write(agent.Spec{ID: "ro", Name: "Ro", Mode: agent.ModeReadOnly, Models: models, Created: time.Now()})
	// forged: another id, ephemeral with the file-system root as workspace
	write(agent.Spec{ID: "evil", Name: "Evil", Mode: "sandbox", Ephemeral: true, SharedWorkspace: "/", Models: models, Created: time.Now()})
	// a sub-agent left behind by an older version's crash
	_ = os.MkdirAll(filepath.Join(ws, "sub-1"), 0o755)
	_ = os.WriteFile(filepath.Join(ws, "sub-1", ".ephemeral"), []byte("kai"), 0o644)

	reg := startAgents(t, root)
	kai, ok := reg.Get("kai")
	if !ok {
		t.Fatal("kai not migrated")
	}
	if kai.Mode() != agent.ModeSandbox || !kai.Spec().ModeUntil.IsZero() || kai.Spec().Role != "writer" {
		t.Fatalf("kai: %+v", kai.Spec())
	}
	if ro, ok := reg.Get("ro"); !ok || ro.Mode() != agent.ModeReadOnly {
		t.Fatal("read-only not kept")
	}
	evil, ok := reg.Get("evil")
	if !ok {
		t.Fatal("evil not migrated")
	}
	if s := evil.Spec(); s.Ephemeral || s.SharedWorkspace != "" || evil.Workspace() != filepath.Join(ws, "evil") {
		t.Fatalf("evil: %+v", s)
	}
	for _, id := range []string{"kai", "ro", "evil"} {
		if exists(filepath.Join(ws, id, "agent.json")) {
			t.Fatalf("%s: old spec kept", id)
		}
		if !exists(filepath.Join(root, "agents", id+".json")) {
			t.Fatalf("%s: new spec missing", id)
		}
	}
	if exists(filepath.Join(ws, "sub-1")) {
		t.Fatal("old sub-agent leftover not swept")
	}

	// the migrated spec is trusted from now on
	if err := kai.Update(func(s *agent.Spec) { s.Mode = agent.ModeHost }); err != nil {
		t.Fatal(err)
	}
	reg2 := startAgents(t, root)
	if k, _ := reg2.Get("kai"); k == nil || k.Mode() != agent.ModeHost {
		t.Fatal("granted host mode lost")
	}
}

// A live sub-agent's marker lives outside its workspace and is swept with
// it after a crash.
func TestEphemeralSweep(t *testing.T) {
	root := t.TempDir()
	reg := startAgents(t, root)
	sub, err := reg.Create(agent.Spec{Name: "sub", Ephemeral: true, Parent: "kai"})
	if err != nil {
		t.Fatal(err)
	}
	dir := sub.Workspace()
	if !exists(filepath.Join(root, "agents", sub.ID()+".ephemeral")) || exists(filepath.Join(dir, ".ephemeral")) {
		t.Fatal("marker not in <root>/agents")
	}
	// "crash": start again without deleting it
	reg2 := startAgents(t, root)
	if _, ok := reg2.Get(sub.ID()); ok || exists(dir) || exists(filepath.Join(root, "agents", sub.ID()+".ephemeral")) {
		t.Fatal("leftover not swept")
	}
}

// Host grants used to run 24h. Now one lasts until the mode is changed: a
// grant from before still running becomes lasting when the agents load; one
// that already ran out stays sandbox.
func TestOldHostGrantsOnLoad(t *testing.T) {
	root := t.TempDir()
	reg := startAgents(t, root)
	live, _ := reg.Create(agent.Spec{Name: "Live"})
	gone, _ := reg.Create(agent.Spec{Name: "Gone"})
	live.Update(func(s *agent.Spec) { s.Mode, s.ModeUntil = agent.ModeHost, time.Now().Add(3*time.Hour) })
	gone.Update(func(s *agent.Spec) { s.Mode, s.ModeUntil = agent.ModeHost, time.Now().Add(-time.Hour) })
	if gone.Mode() != agent.ModeSandbox {
		t.Fatal("an old grant that ran out reads as host")
	}
	// the daemon starts again
	reg2 := startAgents(t, root)
	l, _ := reg2.Get(live.ID())
	g, _ := reg2.Get(gone.ID())
	if l.Mode() != agent.ModeHost || !l.Spec().ModeUntil.IsZero() {
		t.Fatalf("live grant: %s %v", l.Mode(), l.Spec().ModeUntil)
	}
	if g.Mode() != agent.ModeSandbox {
		t.Fatalf("expired grant came back: %s", g.Mode())
	}
	// a new grant has no end, and keeps none through other changes
	l.Update(func(s *agent.Spec) { s.Mode = agent.ModeSandbox })
	l.Update(func(s *agent.Spec) { s.Mode, s.ModeUntil = agent.ModeHost, time.Time{} })
	l.Update(func(s *agent.Spec) { s.Role = "ops" })
	if l.Mode() != agent.ModeHost || !l.Spec().ModeUntil.IsZero() {
		t.Fatal("a new grant got an end")
	}
}
