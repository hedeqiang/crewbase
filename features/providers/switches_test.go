package providers_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/features/providers"
	"github.com/hedeqiang/crewbase/kernel"
)

// cliAdapter stands in for a subscription CLI or a local model server.
type cliAdapter struct{ id string }

func (a cliAdapter) ID() string   { return a.id }
func (a cliAdapter) Kind() string { return "subscription" }
func (a cliAdapter) Models(context.Context) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{{ID: "default", Enabled: true}, {ID: "gpt-6.1-sol", Enabled: true}, {ID: "gpt-5.6-luna", Enabled: true}}, nil
}
func (a cliAdapter) Stream(context.Context, llm.Request, func(llm.Chunk) error) (llm.Response, error) {
	return llm.Response{}, nil
}

func enabled(ms []llm.ModelInfo) map[string]bool {
	out := map[string]bool{}
	for _, m := range ms {
		out[m.ID] = m.Enabled
	}
	return out
}

// CLI and local models: off ids per provider in model-switches.json, seen
// by every listing through the registry.
func TestSwitchCLIModel(t *testing.T) {
	h, s, reg, dir := setup(t)
	if err := reg.Register(h.Root(), cliAdapter{"acp-codex"}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.SetModelEnabled(ctx, "acp-codex", "gpt-5.6-luna", false); err != nil {
		t.Fatal(err)
	}
	ad, _ := reg.Get("acp-codex")
	ms, _ := reg.ModelsOf(ctx, ad)
	if en := enabled(ms); len(ms) != 3 || en["gpt-5.6-luna"] || !en["gpt-6.1-sol"] {
		t.Fatalf("listing %+v", ms)
	}
	if !reg.ModelOff(llm.ModelRef{Provider: "acp-codex", Model: "gpt-5.6-luna"}) || reg.ModelOff(llm.ModelRef{Provider: "acp-codex", Model: "gpt-6.1-sol"}) {
		t.Fatal("ModelOff")
	}
	sw, err := s.ModelSwitches(ctx, "acp-codex")
	if err != nil || len(sw) != 3 || enabled(sw)["gpt-5.6-luna"] {
		t.Fatalf("switches %+v %v", sw, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "model-switches.json"))
	if !strings.Contains(string(b), `"acp-codex"`) || !strings.Contains(string(b), "gpt-5.6-luna") {
		t.Fatalf("model-switches.json %s", b)
	}

	// kept across restarts
	h2 := kernel.NewHost(nil)
	t.Cleanup(h2.Close)
	h2.Load(llm.Plugin, kernel.LoadOptions{})
	if _, err := h2.Load(providers.Plugin, kernel.LoadOptions{Config: map[string]any{"dir": dir}}); err != nil {
		t.Fatal(err)
	}
	reg2 := kernel.Use[*llm.Registry](h2.Root(), llm.ServiceKey)
	if !reg2.ModelOff(llm.ModelRef{Provider: "acp-codex", Model: "gpt-5.6-luna"}) {
		t.Fatal("switch not kept")
	}

	if err := s.SetModelEnabled(ctx, "acp-codex", "gpt-5.6-luna", true); err != nil {
		t.Fatal(err)
	}
	if reg.ModelOff(llm.ModelRef{Provider: "acp-codex", Model: "gpt-5.6-luna"}) {
		t.Fatal("switched on again")
	}
	if err := s.SetModelEnabled(ctx, "nope", "x", false); err == nil {
		t.Fatal("unknown provider")
	}
}

// Providers added here: the switch is Provider.Models (the chosen models).
func TestSwitchAddedProviderModel(t *testing.T) {
	srv := fakeOpenAI(t)
	defer srv.Close()
	_, s, reg, _ := setup(t)
	ctx := context.Background()
	if _, err := s.Add(providers.Input{ID: "ds", Name: "DeepSeek", API: "openai", BaseURL: srv.URL, APIKey: secret}); err != nil {
		t.Fatal(err)
	}
	// none chosen = all on; switching one off chooses the others
	if err := s.SetModelEnabled(ctx, "ds", "deepseek-flash", false); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Get("ds"); !slices.Equal(p.Models, []string{"deepseek-v4-pro"}) {
		t.Fatalf("models %v", p.Models)
	}
	if !reg.ModelOff(llm.ModelRef{Provider: "ds", Model: "deepseek-flash"}) {
		t.Fatal("ModelOff")
	}
	// the settings list still shows it, off (from the provider's listing)
	sw, err := s.ModelSwitches(ctx, "ds")
	if en := enabled(sw); err != nil || len(sw) != 2 || en["deepseek-flash"] || !en["deepseek-v4-pro"] {
		t.Fatalf("switches %+v %v", sw, err)
	}
	if err := s.SetModelEnabled(ctx, "ds", "deepseek-v4-pro", false); err == nil {
		t.Fatal("the last model must stay on")
	}
	if err := s.SetModelEnabled(ctx, "ds", "deepseek-flash", true); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Get("ds"); len(p.Models) != 2 {
		t.Fatalf("models %v", p.Models)
	}
}
