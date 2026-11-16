// Package rules is the rules engine (docs/06-security-model.md §3): a
// tools/pre-execute listener matching action categories to Allow / Ask /
// Block.
//
// Layers, from weakest to strongest:
//
//  1. defaults (read/exec/install/write/none → Allow; push/delete/publish/
//     spend → Ask; payment → Block), optionally overridden by config "global";
//  2. per-agent rules, which may only tighten (Allow → Ask/Block, Ask → Block);
//  3. remembered choices ("always allow" on an approval card): an
//     agent-scoped Ask → Allow, never touching Block or payment;
//  4. the admin-enforced layer, which is a floor over everything above and
//     can forbid remembering a category.
//
// External payment is always Block and can never be remembered. Remembering
// "publish" requires that the card carried ConfirmRemember. Rules apply in
// every run mode, including host.
package rules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

// ServiceKey is the key of the rules engine service.
const ServiceKey = "rules"

// Block is the rules-engine name for tools.Deny.
const Block = tools.Deny

// FileName is the persisted state file under the configured dir.
const FileName = "rules.json"

// Changed is emitted whenever rules or remembered choices change (no delta).
var Changed = kernel.NewEmit[struct{}]("rules/changed")

// Defaults is the default category table.
func Defaults() map[string]tools.Verdict {
	return map[string]tools.Verdict{
		tools.CategoryRead:    tools.Allow,
		tools.CategoryExec:    tools.Allow,
		tools.CategoryInstall: tools.Allow,
		tools.CategoryWrite:   tools.Allow,
		tools.CategoryNone:    tools.Allow,
		tools.CategoryPush:    tools.Ask,
		tools.CategoryDelete:  tools.Ask,
		tools.CategoryPublish: tools.Ask,
		tools.CategorySpend:   tools.Ask,
		tools.CategoryPayment: Block,
	}
}

func rank(v tools.Verdict) int {
	switch v {
	case tools.Allow:
		return 0
	case tools.Ask:
		return 1
	}
	return 2 // Deny and anything unknown are treated as strictest
}

// ParseVerdict accepts allow | ask | block (or deny).
func ParseVerdict(s string) (tools.Verdict, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "allow":
		return tools.Allow, nil
	case "ask":
		return tools.Ask, nil
	case "block", "deny":
		return Block, nil
	}
	return "", fmt.Errorf("rules: unknown verdict %q (want allow, ask or block)", s)
}

// ErrLoosen is returned when a per-agent rule would loosen the global rule.
var ErrLoosen = errors.New("rules: agent rules may only tighten")

// Enforced is an admin-issued rule. Verdict (optional) is a floor that
// overrides personal choices; NoRemember forbids "always allow".
type Enforced struct {
	Category   string        `json:"category"`
	Verdict    tools.Verdict `json:"verdict,omitempty"`
	NoRemember bool          `json:"noRemember,omitempty"`
}

// Choice is one remembered "always allow" choice.
type Choice struct {
	Agent    string    `json:"agent"`
	Category string    `json:"category"`
	At       time.Time `json:"at"`
	// Overridden is true when an enforced rule currently suspends it.
	Overridden bool `json:"overridden,omitempty"`
}

type state struct {
	Version    int                                 `json:"version"`
	Agents     map[string]map[string]tools.Verdict `json:"agents,omitempty"`
	Remembered []Choice                            `json:"remembered,omitempty"`
}

// Engine is the `rules` service.
type Engine struct {
	host *kernel.Host
	path string // "" = memory only
	now  func() time.Time

	mu         sync.Mutex
	global     map[string]tools.Verdict
	enforced   map[string]Enforced
	agents     map[string]map[string]tools.Verdict
	remembered map[string]map[string]Choice
	// armed counts approval cards for (agent, publish) that carried
	// ConfirmRemember and may still turn into a remember.
	armed   map[string]int
	pending map[string]string // callID → armed key
}

// Options configure an Engine.
type Options struct {
	Dir      string
	Global   map[string]tools.Verdict
	Enforced []Enforced
}

// New creates an engine and loads persisted state from opts.Dir.
func New(h *kernel.Host, opts Options) (*Engine, error) {
	e := &Engine{
		host: h, now: time.Now,
		global:     Defaults(),
		enforced:   map[string]Enforced{},
		agents:     map[string]map[string]tools.Verdict{},
		remembered: map[string]map[string]Choice{},
		armed:      map[string]int{},
		pending:    map[string]string{},
	}
	for cat, v := range opts.Global {
		if cat == tools.CategoryPayment && v != Block {
			return nil, errors.New("rules: external payment is always block")
		}
		if rank(v) == 2 {
			v = Block
		}
		e.global[cat] = v
	}
	for _, en := range opts.Enforced {
		if en.Category == "" {
			return nil, errors.New("rules: enforced rule without category")
		}
		e.enforced[en.Category] = en
	}
	if opts.Dir != "" {
		if err := os.MkdirAll(opts.Dir, 0o700); err != nil {
			return nil, err
		}
		e.path = filepath.Join(opts.Dir, FileName)
		if err := e.load(); err != nil {
			return nil, err
		}
	}
	return e, nil
}

func (e *Engine) load() error {
	b, err := os.ReadFile(e.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var st state
	if err := json.Unmarshal(b, &st); err != nil {
		// Failing is safer than silently dropping agent tightenings.
		return fmt.Errorf("rules: %s: %w", e.path, err)
	}
	for agent, m := range st.Agents {
		for cat, v := range m {
			if rank(v) == 2 {
				v = Block
			}
			e.setAgentLocked(agent, cat, v)
		}
	}
	for _, c := range st.Remembered {
		if c.Agent == "" || c.Category == tools.CategoryPayment {
			continue
		}
		c.Overridden = false
		e.rememberLocked(c)
	}
	return nil
}

func (e *Engine) saveLocked() error {
	if e.path == "" {
		return nil
	}
	st := state{Version: 1, Agents: e.agents, Remembered: e.rememberedLocked()}
	for i := range st.Remembered {
		st.Remembered[i].Overridden = false
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := e.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, e.path)
}

func (e *Engine) changed() {
	if e.host != nil {
		Changed.Emit(e.host, struct{}{})
	}
}

func (e *Engine) setAgentLocked(agent, cat string, v tools.Verdict) {
	m := e.agents[agent]
	if m == nil {
		m = map[string]tools.Verdict{}
		e.agents[agent] = m
	}
	m[cat] = v
}

func (e *Engine) rememberLocked(c Choice) {
	m := e.remembered[c.Agent]
	if m == nil {
		m = map[string]Choice{}
		e.remembered[c.Agent] = m
	}
	m[c.Category] = c
}

// result of evaluation before/after remembered choices.
type eval struct {
	verdict      tools.Verdict
	reason       string
	rememberable bool
	preRemember  tools.Verdict
}

func (e *Engine) evalLocked(agent, cat string) eval {
	if cat == tools.CategoryPayment {
		return eval{verdict: Block, preRemember: Block, reason: "rules: external payment is always blocked"}
	}
	v, ok := e.global[cat]
	src := "default"
	if !ok {
		v, src = tools.Ask, "unknown category"
	}
	if ar, ok := e.agents[agent][cat]; ok && agent != "" && rank(ar) > rank(v) {
		v, src = ar, "agent rule"
	}
	en, hasEnf := e.enforced[cat]
	floor := tools.Allow
	if hasEnf && en.Verdict != "" {
		floor = en.Verdict
		if rank(floor) == 2 {
			floor = Block
		}
	}
	if rank(floor) > rank(v) {
		v, src = floor, "enforced"
	}
	out := eval{preRemember: v}
	out.rememberable = !(hasEnf && (en.NoRemember || rank(floor) >= 1))
	if v == tools.Ask && out.rememberable && agent != "" {
		if _, ok := e.remembered[agent][cat]; ok {
			v, src = tools.Allow, "remembered"
		}
	}
	out.verdict = v
	switch v {
	case tools.Allow:
		out.reason = fmt.Sprintf("rules: %s allowed (%s)", cat, src)
	case tools.Ask:
		out.reason = fmt.Sprintf("rules: %s requires approval (%s)", cat, src)
	default:
		out.reason = fmt.Sprintf("rules: %s blocked (%s)", cat, src)
	}
	return out
}

// Evaluate returns the effective verdict for agent and category, and the
// matched rule as a human-readable reason.
func (e *Engine) Evaluate(agent, category string) (tools.Verdict, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r := e.evalLocked(agent, category)
	return r.verdict, r.reason
}

// Rememberable reports whether "always allow" may be offered for agent and
// category right now.
func (e *Engine) Rememberable(agent, category string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	r := e.evalLocked(agent, category)
	return r.rememberable && r.preRemember == tools.Ask
}

// SetAgentRule sets a per-agent rule. Rules looser than the global layer
// (default/global config and enforced floor) are rejected with ErrLoosen;
// setting a verdict equal to the global one removes the agent rule.
func (e *Engine) SetAgentRule(agent, category string, v tools.Verdict) error {
	if agent == "" || category == "" {
		return errors.New("rules: agent and category are required")
	}
	if rank(v) == 2 {
		v = Block
	} else if v != tools.Allow && v != tools.Ask {
		return fmt.Errorf("rules: unknown verdict %q", v)
	}
	e.mu.Lock()
	base := e.evalLocked("", category).preRemember
	if rank(v) < rank(base) {
		e.mu.Unlock()
		return fmt.Errorf("%w: %s is %s globally, cannot set %s for %s", ErrLoosen, category, base, v, agent)
	}
	prev, had := e.agents[agent][category]
	if rank(v) == rank(base) {
		if !had {
			e.mu.Unlock()
			return nil
		}
		delete(e.agents[agent], category)
		if len(e.agents[agent]) == 0 {
			delete(e.agents, agent)
		}
	} else {
		if had && prev == v {
			e.mu.Unlock()
			return nil
		}
		e.setAgentLocked(agent, category, v)
	}
	err := e.saveLocked()
	e.mu.Unlock()
	e.changed()
	return err
}

// AgentRules returns a copy of the per-agent rules.
func (e *Engine) AgentRules(agent string) map[string]tools.Verdict {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := map[string]tools.Verdict{}
	for k, v := range e.agents[agent] {
		out[k] = v
	}
	return out
}

// Remember records an "always allow" choice: only Ask → Allow, never for
// Block, payment, or categories the enforced layer forbids remembering.
// It does not check publish confirmation; the tools/remembered listener
// does. It reports whether the choice was stored.
func (e *Engine) Remember(agent, category string) (bool, error) {
	if agent == "" {
		return false, nil
	}
	e.mu.Lock()
	r := e.evalLocked(agent, category)
	if !r.rememberable || r.preRemember != tools.Ask {
		e.mu.Unlock()
		return false, nil
	}
	e.rememberLocked(Choice{Agent: agent, Category: category, At: e.now().UTC()})
	err := e.saveLocked()
	e.mu.Unlock()
	e.changed()
	return true, err
}

func (e *Engine) rememberedLocked() []Choice {
	out := []Choice{}
	for _, m := range e.remembered {
		for _, c := range m {
			r := e.evalLocked(c.Agent, c.Category)
			c.Overridden = !r.rememberable || r.preRemember != tools.Ask
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Agent != out[j].Agent {
			return out[i].Agent < out[j].Agent
		}
		return out[i].Category < out[j].Category
	})
	return out
}

// Remembered lists every remembered choice (settings › approval).
func (e *Engine) Remembered() []Choice {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.rememberedLocked()
}

// Revoke removes a remembered choice; the category returns to Ask.
func (e *Engine) Revoke(agent, category string) error {
	e.mu.Lock()
	if _, ok := e.remembered[agent][category]; !ok {
		e.mu.Unlock()
		return nil
	}
	delete(e.remembered[agent], category)
	if len(e.remembered[agent]) == 0 {
		delete(e.remembered, agent)
	}
	err := e.saveLocked()
	e.mu.Unlock()
	e.changed()
	return err
}

func armKey(agent, cat string) string { return agent + "\x00" + cat }

// onAsked tracks publish cards that carried ConfirmRemember.
func (e *Engine) onAsked(req approval.Request) {
	if req.Category != tools.CategoryPublish || !req.ConfirmRemember || !req.Rememberable {
		return
	}
	k := armKey(req.Agent, req.Category)
	e.mu.Lock()
	e.armed[k]++
	e.pending[req.CallID] = k
	e.mu.Unlock()
}

func (e *Engine) onDecided(d approval.DecidedData) {
	e.mu.Lock()
	defer e.mu.Unlock()
	k, ok := e.pending[d.CallID]
	if !ok {
		return
	}
	delete(e.pending, d.CallID)
	if !d.Remember {
		e.disarmLocked(k)
	}
	// With Remember the arm stays for the tools/remembered event.
}

func (e *Engine) disarmLocked(k string) {
	if e.armed[k] <= 1 {
		delete(e.armed, k)
	} else {
		e.armed[k]--
	}
}

// onRemember handles tools/remembered.
func (e *Engine) onRemember(log interface{ Warn(string, ...any) }, r tools.Remembered) {
	if r.Category == tools.CategoryPublish {
		k := armKey(r.Agent, r.Category)
		e.mu.Lock()
		ok := e.armed[k] > 0 && r.Confirmed
		if e.armed[k] > 0 {
			e.disarmLocked(k)
		}
		e.mu.Unlock()
		if !ok {
			log.Warn("rules: ignoring publish remember without second confirmation", "agent", r.Agent)
			return
		}
	}
	stored, err := e.Remember(r.Agent, r.Category)
	if err != nil {
		log.Warn("rules: persisting remembered choice failed", "err", err)
	}
	if !stored {
		log.Warn("rules: remembered choice not allowed", "agent", r.Agent, "category", r.Category)
	}
}

// Middleware is the tools/pre-execute listener.
func (e *Engine) Middleware(ctx context.Context, d *tools.Decision, next kernel.Next[*tools.Decision, *tools.Decision]) (*tools.Decision, error) {
	agent := ""
	if d.Call != nil {
		agent = d.Call.Agent
	}
	e.mu.Lock()
	r := e.evalLocked(agent, d.Category)
	e.mu.Unlock()
	shape := func(x *tools.Decision) {
		if !r.rememberable || r.preRemember != tools.Ask {
			x.Rememberable = false
		}
		if x.Category == tools.CategoryPublish {
			x.ConfirmRemember = true
		}
		if x.Category == tools.CategoryPayment {
			x.Rememberable = false
		}
	}
	switch r.verdict {
	case Block:
		return d.DenyWith(r.reason), nil
	case tools.Ask:
		d.RequireApproval(r.reason)
	}
	shape(d)
	out, err := next(ctx, d)
	if err != nil || out == nil {
		return out, err
	}
	// Downstream listeners cannot loosen what the rules decided.
	if r.verdict == tools.Ask && out.Verdict == tools.Allow {
		out.RequireApproval(r.reason)
	}
	if out.Category != d.Category {
		// Category rewritten downstream: re-evaluate the new one.
		e.mu.Lock()
		r2 := e.evalLocked(agent, out.Category)
		e.mu.Unlock()
		switch r2.verdict {
		case Block:
			return out.DenyWith(r2.reason), nil
		case tools.Ask:
			if out.Verdict == tools.Allow {
				out.RequireApproval(r2.reason)
			}
		}
		r = r2
	}
	shape(out)
	return out, nil
}

// Config is the plugin config.
type Config struct {
	// Dir holds rules.json; empty keeps state in memory.
	Dir string `json:"dir"`
	// Global overrides the default table (payment cannot be changed).
	Global map[string]string `json:"global"`
	// Agents are per-agent tightenings: {"kai": {"exec": "ask"}}.
	Agents map[string]map[string]string `json:"agents"`
	// Enforced is the admin layer.
	Enforced []struct {
		Category   string `json:"category"`
		Verdict    string `json:"verdict"`
		NoRemember bool   `json:"noRemember"`
	} `json:"enforced"`
}

// Plugin provides the `rules` service and registers the pre-execute
// listener. Config: {"dir": "...", "global": {cat: verdict}, "agents":
// {agent: {cat: verdict}}, "enforced": [{"category", "verdict",
// "noRemember"}]}.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/rules",
	Description: "Rules engine: Allow / Ask / Block by action category.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		opts := Options{Dir: cfg.Dir, Global: map[string]tools.Verdict{}}
		for cat, s := range cfg.Global {
			v, err := ParseVerdict(s)
			if err != nil {
				return err
			}
			opts.Global[cat] = v
		}
		for _, en := range cfg.Enforced {
			x := Enforced{Category: en.Category, NoRemember: en.NoRemember}
			if en.Verdict != "" {
				if x.Verdict, err = ParseVerdict(en.Verdict); err != nil {
					return err
				}
			}
			opts.Enforced = append(opts.Enforced, x)
		}
		e, err := New(c.Host(), opts)
		if err != nil {
			return err
		}
		for agent, m := range cfg.Agents {
			for cat, s := range m {
				v, err := ParseVerdict(s)
				if err != nil {
					return err
				}
				if err := e.SetAgentRule(agent, cat, v); err != nil {
					if errors.Is(err, ErrLoosen) {
						c.Log().Warn("rules: ignoring loosening agent rule", "agent", agent, "category", cat)
						continue
					}
					return err
				}
			}
		}
		log := c.Log()
		tools.PreExecute.Use(c, e.Middleware)
		tools.OnRemember.Listen(c, func(r tools.Remembered) { e.onRemember(log, r) })
		approval.Asked.Listen(c, e.onAsked)
		approval.Decided.Listen(c, e.onDecided)
		return c.Provide(ServiceKey, e)
	},
})
