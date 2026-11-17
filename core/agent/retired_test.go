package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

func TestDropRetired(t *testing.T) {
	if dropRetired([]llm.ModelRef{{Provider: "magpie", Model: "x"}}) != nil {
		t.Fatal("nothing to drop")
	}
	got := dropRetired([]llm.ModelRef{{Provider: "acp-claude-code", Model: "opus"}, {Provider: "magpie", Model: "x"}, {Provider: "acp-codex", Model: "default"}})
	if len(got) != 1 || got[0].Provider != "magpie" {
		t.Fatalf("got %v", got)
	}
}

// An agent saved while subscription CLIs existed loses those models at
// load; one that had nothing else moves to the models connected now.
func TestAgentsOnRetiredCLIsMoveToConnectedModels(t *testing.T) {
	root := t.TempDir()
	write := func(s Spec) {
		b, _ := json.Marshal(s)
		_ = os.MkdirAll(filepath.Join(root, "workspaces", s.ID), 0o755)
		_ = os.MkdirAll(filepath.Join(root, "agents"), 0o755)
		if err := os.WriteFile(filepath.Join(root, "agents", s.ID+".json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(Spec{ID: "mixed", Name: "Mixed", Mode: ModeSandbox, Created: time.Now(), Models: []llm.ModelRef{{Provider: "acp-codex", Model: "gpt-6-astra"}, {Provider: "magpie", Model: "claude"}}})
	write(Spec{ID: "cli", Name: "CLI only", Mode: ModeSandbox, Created: time.Now(), Models: []llm.ModelRef{{Provider: "acp-claude-code", Model: "opus"}}})

	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, map[string]any{"dir": root + "/sessions"}},
		{prompt.Plugin, nil}, {tools.Plugin, nil}, {llm.Plugin, nil},
		{mock.Plugin, map[string]any{"ids": []string{"magpie"}}},
		{Plugin, map[string]any{"root": root, "defaultModels": []string{"acp-claude-code/default"}}},
	} {
		if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
			t.Fatal(err)
		}
	}
	r := kernel.Use[*Registry](h.Root(), ServiceKey)
	if d := r.DefaultModels(); len(d) != 0 {
		t.Fatalf("retired defaults kept: %v", d)
	}
	mixed, _ := r.Get("mixed")
	if ms := mixed.Spec().Models; len(ms) != 1 || ms[0].String() != "magpie/claude" {
		t.Fatalf("mixed: %v", ms)
	}
	cli, _ := r.Get("cli")
	deadline := time.Now().Add(3 * time.Second)
	for {
		ms := cli.Spec().Models
		if len(ms) == 1 && ms[0].Provider == "magpie" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cli-only agent still on %v", ms)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
