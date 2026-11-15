// Package usage computes the "profile & usage" statistics (docs/03 §5.4)
// from local session logs only — nothing is uploaded: tokens, messages,
// daily activity (53-week heatmap), model source share, per-agent totals,
// automatic model switches (and how many needed a person) and the
// estimated savings of reusing subscriptions / local models versus API
// billing.
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
	From          time.Time      `json:"from"`
	Tokens        int            `json:"tokens"`
	InputTokens   int            `json:"inputTokens"`
	OutputTokens  int            `json:"outputTokens"`
	Messages      int            `json:"messages"`
	PeakDay       string         `json:"peakDay,omitempty"`
	PeakTokens    int            `json:"peakTokens"`
	Days          map[string]int `json:"days"` // YYYY-MM-DD → tokens
	ByProvider    []Share        `json:"byProvider"`
	ByAgent       []AgentShare   `json:"byAgent"`
	Switches      int            `json:"switches"`
	Interventions int            `json:"interventions"` // switches that still ended in a failed turn
	SavedUSD      float64        `json:"savedUsd"`
	SpentUSD      float64        `json:"spentUsd"` // estimated API spend
	Reference     Price          `json:"reference"`
	Kinds         map[string]int `json:"kinds"` // subscription/api/local → tokens
}

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
				cost := float64(am.Usage.InputTokens)/1e6*s.ref.Input + float64(am.Usage.OutputTokens)/1e6*s.ref.Output
				switch kind {
				case "gateway":
					// billed (or not) inside the gateway, e.g. magpie: neither
					// saved nor spent from Crewbase's point of view
				case "api":
					if p, ok := s.prices[am.Model.Provider]; ok {
						cost = float64(am.Usage.InputTokens)/1e6*p.Input + float64(am.Usage.OutputTokens)/1e6*p.Output
					}
					st.SpentUSD += cost
				default:
					st.SavedUSD += cost
				}
				as := byAgent[e.Agent]
				if as == nil {
					as = &AgentShare{Agent: e.Agent}
					byAgent[e.Agent] = as
				}
				as.Tokens += n
				as.Model = am.Model.String()
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
		a.Name = names[a.Agent]
		st.ByAgent = append(st.ByAgent, *a)
	}
	sort.Slice(st.ByAgent, func(i, j int) bool { return st.ByAgent[i].Tokens > st.ByAgent[j].Tokens })
	if st.ByProvider == nil {
		st.ByProvider = []Share{}
	}
	if st.ByAgent == nil {
		st.ByAgent = []AgentShare{}
	}
	s.mu.Lock()
	s.cached, s.at = &st, time.Now()
	s.mu.Unlock()
	return st
}

// Config of the usage plugin.
type Config struct {
	// Reference is the API price used to value subscription/local tokens.
	Reference *Price `json:"reference"`
	// Prices are per-provider API prices for spend estimates.
	Prices map[string]Price `json:"prices"`
}

// Plugin provides usage statistics and GET /api/usage.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/usage",
	Inject:      []string{session.ServiceKey, agent.ServiceKey, llm.ServiceKey},
	Description: "Local usage statistics and subscription-reuse savings.",
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
