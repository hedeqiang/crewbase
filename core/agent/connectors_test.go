package agent_test

import (
	"context"
	"testing"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

// Spec.Connectors drives the registry's connector filter and survives a
// restart: unset = every connector, [] = none, a list = those.
func TestSpecConnectors(t *testing.T) {
	root := t.TempDir()
	load := func() (*agent.Registry, *tools.Registry, *kernel.Host) {
		h := kernel.NewHost(nil)
		t.Cleanup(h.Close)
		for _, x := range []struct {
			s   kernel.Spec
			cfg map[string]any
		}{
			{session.Plugin, map[string]any{"dir": root + "/sessions"}},
			{prompt.Plugin, nil}, {tools.Plugin, nil}, {llm.Plugin, nil},
			{agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"claude-code/default"}}},
		} {
			if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
				t.Fatal(err)
			}
		}
		tr := kernel.Use[*tools.Registry](h.Root(), tools.ServiceKey)
		noop := func(context.Context, tools.Call) (tools.Result, error) { return tools.Text(""), nil }
		tr.Register(h.Root(), &tools.Tool{Name: "bash", Execute: noop})
		tr.Register(h.Root(), &tools.Tool{Name: "notion__search", Source: "notion", Execute: noop})
		tr.Register(h.Root(), &tools.Tool{Name: "deepwiki__ask", Source: "deepwiki", Execute: noop})
		return kernel.Use[*agent.Registry](h.Root(), agent.ServiceKey), tr, h
	}
	reg, tr, _ := load()
	a, err := reg.Create(agent.Spec{Name: "Kai"})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(tr.Visible("kai")); n != 3 {
		t.Fatalf("unset list: %d tools", n)
	}
	if err := a.Update(func(s *agent.Spec) { s.Connectors = &[]string{} }); err != nil {
		t.Fatal(err)
	}
	if got := names(tr.Visible("kai")); len(got) != 1 || got[0] != "bash" {
		t.Fatalf("empty list: %v", got)
	}
	if err := a.Update(func(s *agent.Spec) { s.Connectors = &[]string{"deepwiki"} }); err != nil {
		t.Fatal(err)
	}
	if got := names(tr.Visible("kai")); len(got) != 2 || got[0] != "bash" || got[1] != "deepwiki__ask" {
		t.Fatalf("list: %v", got)
	}
	// other agents are unaffected
	if _, err := reg.Create(agent.Spec{Name: "Nova"}); err != nil {
		t.Fatal(err)
	}
	if n := len(tr.Visible("nova")); n != 3 {
		t.Fatalf("nova: %d", n)
	}
	// persisted
	reg2, tr2, _ := load()
	if a2, ok := reg2.Get("kai"); !ok || a2.Spec().Connectors == nil {
		t.Fatal("connectors not persisted")
	}
	if n := len(tr2.Visible("kai")); n != 2 {
		t.Fatalf("after restart: %d", n)
	}
}

func names(ts []*tools.Tool) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Name)
	}
	return out
}
