package usage_test

import (
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/features/usage"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

func TestUsageAndSavings(t *testing.T) {
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	root := t.TempDir()
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, nil}, {prompt.Plugin, nil}, {tools.Plugin, nil}, {approval.Plugin, nil},
		{llm.Plugin, nil}, {mock.Plugin, map[string]any{"ids": []string{"claude-code", "codex"}}}, {router.Plugin, nil},
		{agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"claude-code/default", "codex/default"}}},
		{agentloop.Plugin, nil},
		{usage.Plugin, map[string]any{"reference": map[string]float64{"input": 1e6, "output": 1e6}}},
	} {
		h.Load(x.s, kernel.LoadOptions{Config: x.cfg})
	}
	r := h.Root()
	llms := kernel.Use[*llm.Registry](r, llm.ServiceKey)
	cc, _ := llms.Get("claude-code")
	cx, _ := llms.Get("codex")
	cc.(*mock.Adapter).SetKind("subscription")
	a, _ := kernel.Use[*agent.Registry](r, agent.ServiceKey).Create(agent.Spec{Name: "Kai"})
	a.Enqueue(agent.Input{Text: "hello"})
	agentloop.WaitIdle(a, 2*time.Second)
	cc.(*mock.Adapter).SetExhausted(true)
	a.Enqueue(agent.Input{Text: "next"}) // switches to codex, succeeds
	agentloop.WaitIdle(a, 2*time.Second)
	cx.(*mock.Adapter).SetExhausted(true)
	a.Enqueue(agent.Input{Text: "fail"}) // nothing left → needs a person
	agentloop.WaitIdle(a, 2*time.Second)

	ss, _ := a.Session()
	for _, g := range []map[string]string{{"kind": "image", "model": "magpie/openai/gpt-image-1-mini"}, {"kind": "image", "model": "magpie/openai/gpt-image-2"}, {"kind": "video", "model": "gemini/gemini-omni-1.2-flash"}} {
		ss.Append("media/generated", a.ID(), g)
	}

	st := kernel.Use[*usage.Service](r, usage.ServiceKey).Compute()
	if len(st.Generated) != 2 || st.Generated[0] != (usage.GenShare{Provider: "magpie", Images: 2}) || st.Generated[1] != (usage.GenShare{Provider: "gemini", Videos: 1}) {
		t.Fatalf("generated %+v", st.Generated)
	}
	if st.Messages != 2 || st.Tokens == 0 || st.PeakDay != time.Now().Format("2006-01-02") /* the host's own day */ {
		t.Fatalf("%+v", st)
	}
	if st.Switches < 1 || st.Interventions != 1 {
		t.Fatalf("switches=%d interventions=%d", st.Switches, st.Interventions)
	}
	if st.Kinds["subscription"] == 0 || st.Kinds["api"] == 0 || st.SavedUSD <= 0 || st.SpentUSD <= 0 {
		t.Fatalf("%+v", st)
	}
	if len(st.ByAgent) != 1 || st.ByAgent[0].Name != "Kai" || len(st.ByProvider) != 2 {
		t.Fatalf("%+v %+v", st.ByAgent, st.ByProvider)
	}
	// per model, overall and per member: the two models it used, adding up
	if len(st.ByModel) != 2 {
		t.Fatalf("by model %+v", st.ByModel)
	}
	var tokens, msgs int
	var spent, saved float64
	for _, m := range st.ByModel {
		tokens, msgs, spent, saved = tokens+m.Tokens, msgs+m.Messages, spent+m.SpentUSD, saved+m.SavedUSD
		if m.Input+m.Output != m.Tokens || m.Messages != 1 {
			t.Fatalf("model %+v", m)
		}
	}
	if today := st.DaysUSD[time.Now().Format("2006-01-02")]; today != st.SpentUSD {
		t.Fatalf("today's spend %v, total %v", today, st.SpentUSD)
	}
	if tokens != st.Tokens || msgs != st.Messages || spent != st.SpentUSD || saved != st.SavedUSD {
		t.Fatalf("models do not add up: %d/%d %d/%d %v/%v %v/%v", tokens, st.Tokens, msgs, st.Messages, spent, st.SpentUSD, saved, st.SavedUSD)
	}
	kai := st.ByAgent[0]
	if kai.Deleted || kai.Messages != 2 || len(kai.Models) != 2 || kai.SpentUSD != st.SpentUSD {
		t.Fatalf("member %+v", kai)
	}
	// a removed agent's use is still counted, and marked
	if err := kernel.Use[*agent.Registry](r, agent.ServiceKey).Delete(a.ID()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(6 * time.Second) // past the 5 s cache
	if st := kernel.Use[*usage.Service](r, usage.ServiceKey).Compute(); len(st.ByAgent) != 1 || !st.ByAgent[0].Deleted {
		t.Fatalf("deleted %+v", st.ByAgent)
	}
}

type fakeCatalog map[string]llm.CatalogEntry

func (f fakeCatalog) Lookup(id string) (llm.CatalogEntry, bool) { e, ok := f[id]; return e, ok }

func TestPerModelPrices(t *testing.T) {
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	root := t.TempDir()
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, nil}, {prompt.Plugin, nil}, {tools.Plugin, nil}, {approval.Plugin, nil},
		{llm.Plugin, nil}, {mock.Plugin, map[string]any{"ids": []string{"sub", "api", "gw"}}},
		{agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"sub/default"}}},
		{usage.Plugin, map[string]any{"reference": map[string]float64{"input": 10, "output": 10}}},
	} {
		h.Load(x.s, kernel.LoadOptions{Config: x.cfg})
	}
	r := h.Root()
	llms := kernel.Use[*llm.Registry](r, llm.ServiceKey)
	sub, _ := llms.Get("sub")
	sub.(*mock.Adapter).SetKind("subscription")
	gw, _ := llms.Get("gw")
	gw.(*mock.Adapter).SetKind("gateway")
	llms.SetCatalog(r, fakeCatalog{
		"cheap":     {Pricing: &llm.Pricing{Input: 1, Output: 2}},
		"cache-pro": {Pricing: &llm.Pricing{Input: 4, Output: 20, CacheRead: 0.4}},
	})
	a, _ := kernel.Use[*agent.Registry](r, agent.ServiceKey).Create(agent.Spec{Name: "Kai"})
	ss, _ := a.Session()
	M := 1_000_000
	for _, m := range []session.AssistantMessage{
		{Model: llm.ModelRef{Provider: "api", Model: "cheap"}, Usage: llm.Usage{InputTokens: M, OutputTokens: M}},  // 1 + 2
		{Model: llm.ModelRef{Provider: "api", Model: "unknown"}, Usage: llm.Usage{InputTokens: M}},                 // reference: 10
		{Model: llm.ModelRef{Provider: "sub", Model: "cache-pro"}, Usage: llm.Usage{InputTokens: M, CacheRead: M}}, // all cached: 0.4
		{Model: llm.ModelRef{Provider: "gw", Model: "cheap"}, Usage: llm.Usage{InputTokens: M, OutputTokens: M}},   // gateway, list price: 3
		{Model: llm.ModelRef{Provider: "gw", Model: "unknown"}, Usage: llm.Usage{InputTokens: M}},                  // gateway, no price: not guessed
	} {
		ss.Append(session.TypeAssistantMessage, a.ID(), m)
	}
	st := kernel.Use[*usage.Service](r, usage.ServiceKey).Compute()
	if st.SpentUSD != 16 || st.GatewayUSD != 3 || st.SavedUSD < 0.399 || st.SavedUSD > 0.401 || st.PricedTokens != 5*M {
		t.Fatalf("spent %v gateway %v saved %v priced %d", st.SpentUSD, st.GatewayUSD, st.SavedUSD, st.PricedTokens)
	}
}
