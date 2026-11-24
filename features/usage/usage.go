// Package usage computes the "profile & usage" statistics (docs/03 §5.4)
// from local session logs only — nothing is uploaded: tokens, messages,
// daily activity (53-week heatmap), model source share, per-agent totals,
// automatic model switches (and how many needed a person), the estimated
// API spend and what local models saved versus API billing.
package usage

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

const ServiceKey = "usage"

// Price per million tokens (USD) used as the "had you paid by API" reference.
type Price struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
}

// Stats is the usage report.
type Stats struct {
	From         time.Time      `json:"from"`
	Tokens       int            `json:"tokens"`
	InputTokens  int            `json:"inputTokens"`
	OutputTokens int            `json:"outputTokens"`
	Messages     int            `json:"messages"`
	PeakDay      string         `json:"peakDay,omitempty"`
	PeakTokens   int            `json:"peakTokens"`
	Days         map[string]int `json:"days"` // YYYY-MM-DD → tokens
	ByProvider   []Share        `json:"byProvider"`
	ByAgent      []AgentShare   `json:"byAgent"`
	// ByModel is every model's use, most used first.
	ByModel       []ModelShare `json:"byModel"`
	Switches      int          `json:"switches"`
	Interventions int          `json:"interventions"` // switches that still ended in a failed turn
	SavedUSD      float64      `json:"savedUsd"`
	SpentUSD      float64      `json:"spentUsd"` // estimated API spend (direct keys and gateways)
	// GatewayUSD is the part of SpentUSD that went through a gateway such
	// as magpie, valued at the model's list price (the gateway's own
	// billing may differ).
	GatewayUSD float64 `json:"gatewayUsd"`
	Reference  Price   `json:"reference"`
	// PricedTokens were valued at their model's own list price (from the
	// model catalog); the rest at Reference.
	PricedTokens int            `json:"pricedTokens"`
	Kinds        map[string]int `json:"kinds"` // api/gateway/local → tokens
	// Generated counts images and videos made per provider (media/generated
	// events; providers bill these per image / per second, not counted in
	// SpentUSD).
	Generated []GenShare `json:"generated"`
}

// GenShare is generated files per provider.
type GenShare struct {
	Provider string `json:"provider"`
	Images   int    `json:"images"`
	Videos   int    `json:"videos"`
}

// typeGenerated is features/media's event (not imported: usage only reads logs).
const typeGenerated = "media/generated"

// Share is tokens per provider.
type Share struct {
	Provider string `json:"provider"`
	Kind     string `json:"kind"`
	Tokens   int    `json:"tokens"`
}

// AgentShare is tokens per agent.
type AgentShare struct {
	Agent  string `json:"agent"`
	Name   string `json:"name"`
	Model  string `json:"model"` // most recent model
	Tokens int    `json:"tokens"`
	// Messages are its replies; SpentUSD what they cost (as Stats.SpentUSD).
	Messages int     `json:"messages"`
	SpentUSD float64 `json:"spentUsd"`
	// Models are the models its replies came from, most used first.
	Models []ModelShare `json:"models"`
	// Deleted: the agent no longer exists (removed, or a finished sub-agent).
	Deleted bool `json:"deleted,omitempty"`
}

// ModelShare is one model's use: tokens, replies, and what they cost.
type ModelShare struct {
	Model    string `json:"model"` // provider/model
	Provider string `json:"provider"`
	Kind     string `json:"kind"` // the provider's kind: api | gateway | local | …
	Tokens   int    `json:"tokens"`
	Input    int    `json:"input"`
	Output   int    `json:"output"`
	Messages int    `json:"messages"`
	// SpentUSD is what it cost (API keys and gateways, as Stats.SpentUSD);
	// SavedUSD what local or subscription use would have cost by API.
	SpentUSD float64 `json:"spentUsd"`
	SavedUSD float64 `json:"savedUsd"`
	// Unpriced: some of its tokens had no known price (a gateway model the
	// catalog does not know counts 0; a direct key falls to the reference).
	Unpriced bool `json:"unpriced,omitempty"`
}

func (m *ModelShare) add(am session.AssistantMessage, spent, saved float64, priced bool) {
	m.Tokens += am.Usage.InputTokens + am.Usage.OutputTokens
	m.Input += am.Usage.InputTokens
	m.Output += am.Usage.OutputTokens
	m.Messages++
	m.SpentUSD += spent
	m.SavedUSD += saved
	if !priced {
		m.Unpriced = true
	}
}

func sortedModels(ms map[string]*ModelShare) []ModelShare {
	out := make([]ModelShare, 0, len(ms))
	for _, m := range ms {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tokens != out[j].Tokens {
			return out[i].Tokens > out[j].Tokens
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// Service computes stats (cached briefly; logs are the source of truth).
type Service struct {
	sessions *session.Store
	agents   *agent.Registry
	llms     *llm.Registry
	ref      Price
	prices   map[string]Price // provider → actual API price, for spend estimates

	mu     sync.Mutex
	cached *Stats
	at     time.Time
}

func (s *Service) kindOf(provider string) string {
	if a, ok := s.llms.Get(provider); ok {
		return a.Kind()
	}
	return "api"
}

// Compute builds stats over the last 53 weeks.
func (s *Service) Compute() Stats {
	s.mu.Lock()
	if s.cached != nil && time.Since(s.at) < 5*time.Second {
		st := *s.cached
		s.mu.Unlock()
		return st
	}
	s.mu.Unlock()

	from := time.Now().UTC().AddDate(0, 0, -7*53)
	st := Stats{From: from, Days: map[string]int{}, Kinds: map[string]int{}, Reference: s.ref}
	byProv := map[string]int{}
	byAgent := map[string]*AgentShare{}
	byModel := map[string]*ModelShare{}
	agentModels := map[string]map[string]*ModelShare{}
	gens := map[string]*GenShare{}
	prices := map[string]*llm.Pricing{} // model → catalog price (nil = unknown)
	for _, m := range s.sessions.List() {
		ss, err := s.sessions.Get(m.ID)
		if err != nil {
			continue
		}
		for _, e := range ss.Events() {
			if e.Time.Before(from) {
				continue
			}
			switch e.Type {
			case session.TypeAssistantMessage:
				var am session.AssistantMessage
				if e.Decode(&am) != nil {
					continue
				}
				n := am.Usage.InputTokens + am.Usage.OutputTokens
				st.Messages++
				st.Tokens += n
				st.InputTokens += am.Usage.InputTokens
				st.OutputTokens += am.Usage.OutputTokens
				st.Days[e.Time.Local().Format("2006-01-02")] += n // the host's own calendar day
				byProv[am.Model.Provider] += n
				kind := s.kindOf(am.Model.Provider)
				st.Kinds[kind] += n
				// Value the tokens at the model's own list price when the
				// model catalog knows it, else at the reference price.
				cost := float64(am.Usage.InputTokens)/1e6*s.ref.Input + float64(am.Usage.OutputTokens)/1e6*s.ref.Output
				p, cached := prices[am.Model.String()]
				if !cached {
					p = s.llms.PriceOf(am.Model)
					prices[am.Model.String()] = p
				}
				var spent, saved float64
				priced := p != nil
				switch kind {
				case "gateway":
					// billed inside the gateway (e.g. magpie): counted as spent
					// at the model's list price when it is known, the same
					// figure the reply footer shows; unknown prices count 0
					// rather than a guess at the reference price
					if p != nil {
						spent = p.Cost(am.Usage)
						st.PricedTokens += n
						st.GatewayUSD += spent
					}
				case "api":
					if cp, ok := s.prices[am.Model.Provider]; ok {
						cost, priced = float64(am.Usage.InputTokens)/1e6*cp.Input+float64(am.Usage.OutputTokens)/1e6*cp.Output, true
					} else if p != nil {
						cost = p.Cost(am.Usage)
						st.PricedTokens += n
					}
					spent = cost
				default:
					if p != nil {
						cost = p.Cost(am.Usage)
						st.PricedTokens += n
					}
					saved = cost
				}
				st.SpentUSD += spent
				st.SavedUSD += saved
				ref := am.Model.String()
				ms := byModel[ref]
				if ms == nil {
					ms = &ModelShare{Model: ref, Provider: am.Model.Provider, Kind: kind}
					byModel[ref] = ms
				}
				ms.add(am, spent, saved, priced)
				as := byAgent[e.Agent]
				if as == nil {
					as = &AgentShare{Agent: e.Agent}
					byAgent[e.Agent] = as
					agentModels[e.Agent] = map[string]*ModelShare{}
				}
				as.Tokens += n
				as.Messages++
				as.SpentUSD += spent
				as.Model = ref
				am2 := agentModels[e.Agent][ref]
				if am2 == nil {
					am2 = &ModelShare{Model: ref, Provider: am.Model.Provider, Kind: kind}
					agentModels[e.Agent][ref] = am2
				}
				am2.add(am, spent, saved, priced)
			case typeGenerated:
				var g struct {
					Kind  string `json:"kind"`
					Model string `json:"model"`
				}
				if e.Decode(&g) != nil {
					continue
				}
				prov, _, _ := strings.Cut(g.Model, "/")
				gs := gens[prov]
				if gs == nil {
					gs = &GenShare{Provider: prov}
					gens[prov] = gs
				}
				if g.Kind == "video" {
					gs.Videos++
				} else {
					gs.Images++
				}
			case session.TypeModelSwitched:
				st.Switches++
			case session.TypeTurnEnd:
				// Failover could not continue: a person had to step in.
				var te agentloop.TurnEnd
				if e.Decode(&te) == nil && te.Status == "failed" && strings.Contains(te.Error, "no available model") {
					st.Interventions++
				}
			}
		}
	}
	for d, n := range st.Days {
		if n > st.PeakTokens || (n == st.PeakTokens && d > st.PeakDay) {
			st.PeakDay, st.PeakTokens = d, n
		}
	}
	for p, n := range byProv {
		st.ByProvider = append(st.ByProvider, Share{Provider: p, Kind: s.kindOf(p), Tokens: n})
	}
	sort.Slice(st.ByProvider, func(i, j int) bool { return st.ByProvider[i].Tokens > st.ByProvider[j].Tokens })
	names := s.agents.Names()
	for _, a := range byAgent {
		n, ok := names[a.Agent]
		a.Name, a.Deleted = n, !ok
		a.Models = sortedModels(agentModels[a.Agent])
		st.ByAgent = append(st.ByAgent, *a)
	}
	st.ByModel = sortedModels(byModel)
	sort.Slice(st.ByAgent, func(i, j int) bool { return st.ByAgent[i].Tokens > st.ByAgent[j].Tokens })
	if st.ByProvider == nil {
		st.ByProvider = []Share{}
	}
	if st.ByAgent == nil {
		st.ByAgent = []AgentShare{}
	}
	st.Generated = []GenShare{}
	for _, g := range gens {
		st.Generated = append(st.Generated, *g)
	}
	sort.Slice(st.Generated, func(i, j int) bool {
		a, b := st.Generated[i], st.Generated[j]
		if a.Images+a.Videos != b.Images+b.Videos {
			return a.Images+a.Videos > b.Images+b.Videos
		}
		return a.Provider < b.Provider
	})
	s.mu.Lock()
	s.cached, s.at = &st, time.Now()
	s.mu.Unlock()
	return st
}

// Config of the usage plugin.
type Config struct {
	// Reference is the API price used to value local-model tokens.
	Reference *Price `json:"reference"`
	// Prices are per-provider API prices for spend estimates.
	Prices map[string]Price `json:"prices"`
}

// Plugin provides usage statistics and GET /api/usage.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/usage",
	Inject:      []string{session.ServiceKey, agent.ServiceKey, llm.ServiceKey},
	Description: "Local usage statistics and estimated API spend.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		ref := Price{Input: 4, Output: 20} // default: a frontier API model's list price
		if cfg.Reference != nil {
			ref = *cfg.Reference
		}
		s := &Service{
			sessions: kernel.Use[*session.Store](c, session.ServiceKey),
			agents:   kernel.Use[*agent.Registry](c, agent.ServiceKey),
			llms:     kernel.Use[*llm.Registry](c, llm.ServiceKey),
			ref:      ref, prices: cfg.Prices,
		}
		return c.Provide(ServiceKey, s)
	},
})

// APIPlugin serves GET /api/usage; it waits for the gateway, so it works
// whatever order the bundles load in.
var APIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/usage-api",
	Inject:      []string{gateway.ServiceKey, ServiceKey},
	Description: "GET /api/usage.",
	Apply: func(c *kernel.Context) error {
		s := kernel.Use[*Service](c, ServiceKey)
		kernel.Use[*gateway.Gateway](c, gateway.ServiceKey).Handle(c, gateway.Route{Method: "GET", Path: "/api/usage",
			Handle: func(context.Context, *gateway.Request) (any, error) { return s.Compute(), nil }})
		return nil
	},
})

// TokensByAgent is each agent's tokens in the current period (for teams).
func (s *Service) TokensByAgent() map[string]int {
	out := map[string]int{}
	for _, a := range s.Compute().ByAgent {
		out[a.Agent] += a.Tokens
	}
	return out
}
