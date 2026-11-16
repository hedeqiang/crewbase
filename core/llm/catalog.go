package llm

import (
	"context"

	"github.com/hedeqiang/crewbase/kernel"
)

// Pricing is a model's API list price in USD. Token prices are per
// million tokens; Image is per generated image. Zero means unknown.
type Pricing struct {
	Input      float64 `json:"input,omitempty"`
	Output     float64 `json:"output,omitempty"`
	CacheRead  float64 `json:"cacheRead,omitempty"`
	CacheWrite float64 `json:"cacheWrite,omitempty"`
	Image      float64 `json:"image,omitempty"`
	// Source names where the price came from ("openrouter", "litellm").
	Source string `json:"source,omitempty"`
}

// Known reports whether a token price is set.
func (p *Pricing) Known() bool { return p != nil && (p.Input > 0 || p.Output > 0) }

// Cost values u at p in USD. Cache reads / writes use their own prices
// when known, otherwise the input price.
func (p *Pricing) Cost(u Usage) float64 {
	if p == nil {
		return 0
	}
	in, cost := float64(u.InputTokens), 0.0
	if p.CacheRead > 0 && u.CacheRead > 0 {
		in -= float64(u.CacheRead)
		cost += float64(u.CacheRead) * p.CacheRead
	}
	if p.CacheWrite > 0 && u.CacheWrite > 0 {
		in -= float64(u.CacheWrite)
		cost += float64(u.CacheWrite) * p.CacheWrite
	}
	if in < 0 {
		in = 0
	}
	cost += in*p.Input + float64(u.OutputTokens)*p.Output
	return cost / 1e6
}

// CatalogEntry is what a model catalog knows about one model id.
type CatalogEntry struct {
	ContextWindow int
	MaxOutput     int
	Pricing       *Pricing
	// Kind is the catalog's output kind (chat | image | video): a hint for
	// people choosing models, never applied to a provider's own listing.
	Kind string
}

// Catalog looks up model metadata by model id (features/modelcatalog).
type Catalog interface {
	Lookup(id string) (CatalogEntry, bool)
}

// SetCatalog installs the model catalog for c's lifetime.
func (r *Registry) SetCatalog(c *kernel.Context, cat Catalog) {
	r.mu.Lock()
	r.catalog = cat
	r.mu.Unlock()
	AdaptersChanged.Emit(c.Host(), struct{}{})
	c.Defer(func() {
		r.mu.Lock()
		if r.catalog == cat {
			r.catalog = nil
		}
		r.mu.Unlock()
	})
}

// Lookup asks the installed catalog about a model id.
func (r *Registry) Lookup(id string) (CatalogEntry, bool) {
	r.mu.RLock()
	cat := r.catalog
	r.mu.RUnlock()
	if cat == nil {
		return CatalogEntry{}, false
	}
	return cat.Lookup(id)
}

// Enrich fills in what provider's listing left out, from the catalog:
// the context window when the provider did not report one (or the
// adapter only guessed it), max output tokens and the list price. What a
// provider reports is never overridden. Local models get no price.
func (r *Registry) Enrich(provider string, ms []ModelInfo) []ModelInfo {
	r.mu.RLock()
	cat := r.catalog
	local := false
	if a, ok := r.adapters[provider]; ok {
		local = a.Kind() == "local"
	}
	r.mu.RUnlock()
	if cat == nil || len(ms) == 0 {
		return ms
	}
	out := make([]ModelInfo, len(ms))
	for i, m := range ms {
		out[i] = m
		e, ok := cat.Lookup(m.ID)
		if !ok {
			continue
		}
		chat := m.OutputKind() == KindChat
		if chat && e.ContextWindow > 0 && (m.ContextWindow == 0 || m.ContextEstimated) {
			out[i].ContextWindow, out[i].ContextEstimated = e.ContextWindow, false
		}
		if chat && m.MaxOutput == 0 {
			out[i].MaxOutput = e.MaxOutput
		}
		if m.Pricing == nil && e.Pricing != nil && !local {
			p := *e.Pricing
			out[i].Pricing = &p
		}
	}
	return out
}

// ModelsOf lists a's models enriched from the catalog, with the ones
// switched off (see Switches) marked Enabled=false.
func (r *Registry) ModelsOf(ctx context.Context, a Adapter) ([]ModelInfo, error) {
	ms, err := a.Models(ctx)
	if err != nil {
		return ms, err
	}
	ms = r.Enrich(a.ID(), ms)
	r.mu.RLock()
	sw := r.switches
	r.mu.RUnlock()
	if sw != nil {
		for i := range ms {
			if ms[i].Enabled && sw.Off(a.ID(), ms[i].ID) {
				ms[i].Enabled = false
			}
		}
	}
	return ms, nil
}

// Switches turn single models off (Settings › Models): an off model is
// offered by no picker and never chosen automatically (routing, default
// model orders). Adapters keep listing it, with Enabled=false.
type Switches interface {
	Off(provider, model string) bool
}

// SetSwitches installs the model switches for c's lifetime.
func (r *Registry) SetSwitches(c *kernel.Context, s Switches) {
	r.mu.Lock()
	r.switches = s
	r.mu.Unlock()
	AdaptersChanged.Emit(c.Host(), struct{}{})
	c.Defer(func() {
		r.mu.Lock()
		if r.switches == s {
			r.switches = nil
		}
		r.mu.Unlock()
	})
}

// ModelOff reports whether the person switched ref's model off.
func (r *Registry) ModelOff(ref ModelRef) bool {
	r.mu.RLock()
	sw := r.switches
	r.mu.RUnlock()
	return sw != nil && sw.Off(ref.Provider, ref.Model)
}

// PriceOf returns the list price of ref when the catalog knows it (nil
// otherwise, and for local models).
func (r *Registry) PriceOf(ref ModelRef) *Pricing {
	ms := r.Enrich(ref.Provider, []ModelInfo{{ID: ref.Model, Provider: ref.Provider}})
	if p := ms[0].Pricing; p.Known() {
		return p
	}
	return nil
}
