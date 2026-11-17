package agent

import (
	"context"
	"testing"

	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

// cli is a subscription adapter listing its own models after "default".
type cli struct {
	*mock.Adapter
	ids []string
}

func (*cli) Kind() string { return "subscription" }

func (c *cli) Models(context.Context) ([]llm.ModelInfo, error) {
	var out []llm.ModelInfo
	for _, id := range c.ids {
		out = append(out, llm.ModelInfo{ID: id, Provider: c.ID(), Enabled: true})
	}
	return out, nil
}

func TestCLIDefaultsBecomeConcreteModels(t *testing.T) {
	root := t.TempDir()
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, map[string]any{"dir": root + "/sessions"}},
		{prompt.Plugin, nil}, {tools.Plugin, nil}, {llm.Plugin, nil},
		{Plugin, map[string]any{"root": root, "defaultModels": []string{"acp-claude-code/default", "acp-gemini-gone/default", "acp-codex/default"}}},
	} {
		if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
			t.Fatal(err)
		}
	}
	reg := kernel.Use[*llm.Registry](h.Root(), llm.ServiceKey)
	for _, c := range []*cli{
		{mock.New("acp-claude-code", 200000), []string{"default", "opus", "sonnet"}},
		{mock.New("acp-codex", 200000), []string{"gpt-6.1-sol", "gpt-6-luna"}},
	} {
		if err := reg.Register(h.Root(), c); err != nil {
			t.Fatal(err)
		}
	}
	r := kernel.Use[*Registry](h.Root(), ServiceKey)
	// a new agent gets concrete models; the provider that is gone is left out
	got := r.AvailableDefaults()
	if len(got) != 2 || got[0].String() != "acp-claude-code/opus" || got[1].String() != "acp-codex/gpt-6.1-sol" {
		t.Fatalf("defaults %v", got)
	}
	// an existing agent on a placeholder is named once the CLI lists models
	a, err := r.Create(Spec{Name: "Old", Models: []llm.ModelRef{{Provider: "acp-claude-code", Model: "default"}, {Provider: "acp-claude-code", Model: "opus"}, {Provider: "acp-codex", Model: "gpt-6-luna"}}})
	if err != nil {
		t.Fatal(err)
	}
	if n := r.resolvePlaceholders(context.Background(), reg); n != 1 {
		t.Fatalf("changed %d agents", n)
	}
	ms := a.Spec().Models
	if len(ms) != 2 || ms[0].String() != "acp-claude-code/opus" || ms[1].String() != "acp-codex/gpt-6-luna" {
		t.Fatalf("models %v", ms)
	}
}
