package agent

// The model order new agents get (from the configured providers), chat
// names, and models of retired providers.

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/kernel"
)

// DefaultModels returns the model order given to new agents.
func (r *Registry) DefaultModels() []llm.ModelRef {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]llm.ModelRef(nil), r.defaults...)
}

// AvailableDefaults is the model order a new agent gets now: the
// configured order with concrete models, minus providers that are gone.
func (r *Registry) AvailableDefaults() []llm.ModelRef { return r.availableDefaults() }

// availableDefaults is the default order restricted to configured
// providers; when none of them is configured (e.g. no CLI installed), the
// first model of each available provider is used instead so a new agent
// can work out of the box.
func (r *Registry) availableDefaults() []llm.ModelRef {
	defs := r.DefaultModels()
	reg, ok := kernel.Lookup[*llm.Registry](r.c, llm.ServiceKey)
	if !ok {
		return defs
	}
	r.mu.Lock()
	if r.fallback != nil && time.Since(r.fallbackAt) < 10*time.Minute {
		out := append([]llm.ModelRef(nil), r.fallback...)
		r.mu.Unlock()
		return out
	}
	r.mu.Unlock()
	out := r.computeDefaults(reg, defs)
	r.mu.Lock()
	r.fallback, r.fallbackAt = out, time.Now()
	r.mu.Unlock()
	return append([]llm.ModelRef(nil), out...)
}

func (r *Registry) computeDefaults(reg *llm.Registry, defs []llm.ModelRef) []llm.ModelRef {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var out []llm.ModelRef
	seen := map[string]bool{}
	for _, m := range defs {
		ad, ok := reg.Get(m.Provider)
		if !ok || reg.ModelOff(m) {
			continue // a provider that is gone or a switched-off model
		}
		// "default" is a placeholder for "the provider's own choice": name
		// the concrete model so people see (and the router tracks) what runs.
		if m.Model == "" || m.Model == "default" {
			if ms, err := reg.ModelsOf(ctx, ad); err == nil {
				if id := pickModel(ms); id != "" {
					m.Model = id
				}
			}
		}
		if k := m.String(); !seen[k] {
			seen[k] = true
			out = append(out, m)
		}
	}
	if len(out) > 0 {
		return out
	}
	// Nothing configured is connected: up to three strong models from what
	// is (a gateway such as magpie offers many), so a new agent has a
	// fallback when the first one is out of quota.
	type cand struct {
		ref  llm.ModelRef
		rank int
	}
	var cands []cand
	for _, ad := range reg.List() {
		if ad.ID() == "mock" {
			continue
		}
		ms, err := reg.ModelsOf(ctx, ad) // switched-off models are not enabled
		if err != nil {
			continue
		}
		best := map[int]bool{}
		for _, m := range ms {
			if !m.Enabled || m.OutputKind() != llm.KindChat {
				continue
			}
			for i, want := range preferredModels {
				if strings.Contains(m.ID, want) {
					if !best[i] { // one model per family and provider
						best[i] = true
						cands = append(cands, cand{llm.ModelRef{Provider: ad.ID(), Model: m.ID}, i})
					}
					break
				}
			}
		}
		if len(best) == 0 {
			if id := pickModel(ms); id != "" {
				cands = append(cands, cand{llm.ModelRef{Provider: ad.ID(), Model: id}, len(preferredModels)})
			}
		}
	}
	slices.SortStableFunc(cands, func(a, b cand) int { return a.rank - b.rank })
	for _, c := range cands {
		if len(out) == 3 {
			break
		}
		out = append(out, c.ref)
	}
	if len(out) == 0 {
		return defs
	}
	return out
}

// adoptDefaults moves agents that still have the built-in model order,
// none of it configured (made before any model was connected, e.g. the
// first chat of a fresh install), onto the models that are available now.
// An order someone chose is never touched; failover handles those.
func (r *Registry) adoptDefaults() {
	reg, ok := kernel.Lookup[*llm.Registry](r.c, llm.ServiceKey)
	if !ok {
		return
	}
	defs := r.DefaultModels()
	var now []llm.ModelRef
	for _, a := range r.List() {
		ms := a.Spec().Models
		if len(ms) > 0 && (!sameRefs(ms, defs) || anyConfigured(reg, ms)) {
			continue
		}
		if now == nil {
			now = r.availableDefaults()
			if !anyConfigured(reg, now) {
				return // still nothing to move to
			}
		}
		_ = a.Update(func(s *Spec) {
			if len(s.Models) == 0 || sameRefs(s.Models, defs) {
				s.Models = append([]llm.ModelRef(nil), now...)
			}
		})
	}
}

func anyConfigured(reg *llm.Registry, ms []llm.ModelRef) bool {
	for _, m := range ms {
		if _, ok := reg.Get(m.Provider); ok {
			return true
		}
	}
	return false
}

func sameRefs(a, b []llm.ModelRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Provider != b[i].Provider || a[i].Model != b[i].Model {
			return false
		}
	}
	return true
}

// preferredModels ranks well-known strong models when a provider (such as
// a gateway) offers many; the first enabled match wins.
var preferredModels = []string{"claude-opus-5-5", "gpt-6.2", "gpt-6.1", "claude-sonnet-5-5", "gpt-6", "deepseek-v4-pro", "gemini", "deepseek-flash"}

func pickModel(ms []llm.ModelInfo) string {
	// a CLI's "default" entry only stands in when it lists nothing else
	real := ms[:0:0]
	for _, m := range ms {
		if m.ID != "default" {
			real = append(real, m)
		}
	}
	if len(real) > 0 {
		ms = real
	}
	for _, want := range preferredModels {
		for _, m := range ms {
			if m.Enabled && strings.Contains(m.ID, want) {
				return m.ID
			}
		}
	}
	for _, m := range ms {
		if m.Enabled {
			return m.ID
		}
	}
	return ""
}

// SetDefaultModels changes the model order given to new agents.
func (r *Registry) SetDefaultModels(ms []llm.ModelRef) {
	r.mu.Lock()
	r.defaults = append([]llm.ModelRef(nil), ms...)
	r.fallback = nil
	r.mu.Unlock()
}

// Root returns the data root.
func (r *Registry) Root() string { return r.root }

// Sessions returns the session store.
func (r *Registry) Sessions() *session.Store { return r.sessions }

// ChatNameFrom is the name a blank chat gets from its first message, until
// a generated topic replaces it (an agent named this way still has an
// automatic name, which the topic may replace; a name the person typed
// never matches it).
func ChatNameFrom(text string) string {
	t := strings.Join(strings.Fields(text), " ")
	if r := []rune(t); len(r) > 24 {
		t = string(r[:23]) + "…"
	}
	return t
}

// renamedProviders maps providers that were replaced to their successors:
// Gemini CLI became Antigravity (agy).
// retiredPrefix marks providers Crewbase no longer has: the subscription
// coding CLIs it drove over ACP (acp-claude-code, acp-codex, …), removed in
// 0.5.12 because reusing a CLI login risks the account.
const retiredPrefix = "acp-"

// dropRetired returns ms without models of retired providers, or nil when
// nothing changed. An order left empty is filled by adoptDefaults.
func dropRetired(ms []llm.ModelRef) []llm.ModelRef {
	if !slices.ContainsFunc(ms, func(m llm.ModelRef) bool { return strings.HasPrefix(m.Provider, retiredPrefix) }) {
		return nil
	}
	return slices.DeleteFunc(slices.Clone(ms), func(m llm.ModelRef) bool { return strings.HasPrefix(m.Provider, retiredPrefix) })
}
