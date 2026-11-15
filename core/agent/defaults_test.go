package agent_test

import (
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

// A chat made before any model was connected (fresh install) moves to the
// first model connected afterwards; an order someone chose stays as it is.
func TestAgentsAdoptFirstConnectedModel(t *testing.T) {
	root := t.TempDir()
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	load := func(s kernel.Spec, cfg map[string]any) {
		t.Helper()
		if _, err := h.Load(s, kernel.LoadOptions{Config: cfg}); err != nil {
			t.Fatal(err)
		}
	}
	load(session.Plugin, map[string]any{"dir": root + "/sessions"})
	load(prompt.Plugin, nil)
	load(tools.Plugin, nil)
	load(llm.Plugin, nil)
	load(agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"claude-code/default", "codex/default"}})
	reg := kernel.Use[*agent.Registry](h.Root(), agent.ServiceKey)

	chat, err := reg.Create(agent.Spec{Name: "chat", Kind: agent.KindChat})
	if err != nil {
		t.Fatal(err)
	}
	if got := chat.Spec().Models; len(got) == 0 || got[0].Provider != "claude-code" {
		t.Fatalf("before any model: %v", got)
	}
	chosen, err := reg.Create(agent.Spec{Name: "Kai", Models: []llm.ModelRef{{Provider: "gemini", Model: "pro"}}})
	if err != nil {
		t.Fatal(err)
	}

	load(mock.Plugin, map[string]any{"ids": []string{"deepseek"}})

	deadline := time.Now().Add(3 * time.Second)
	for {
		ms := chat.Spec().Models
		if len(ms) == 1 && ms[0].Provider == "deepseek" && ms[0].Model == "default" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("chat still on %v", ms)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ms := chosen.Spec().Models; len(ms) != 1 || ms[0].Provider != "gemini" {
		t.Fatalf("a chosen order changed: %v", ms)
	}
	// persisted: a restart keeps it
	again, ok := reg.Get(chat.ID())
	if !ok || again.Spec().Models[0].Provider != "deepseek" {
		t.Fatal("not kept")
	}
}
