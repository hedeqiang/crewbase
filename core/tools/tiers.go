package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hedeqiang/crewbase/core/llm"
)

// Load tiers (Tool.Load).
const (
	LoadCore     = "core"
	LoadDeferred = "deferred"
	LoadMeta     = "meta"
)

// Deferred reports whether t is in the deferred tier.
func (t *Tool) Deferred() bool { return t.Load == LoadDeferred }

// groupSeparator splits "<server>__<tool>" MCP tool names.
const groupSeparator = "__"

// GroupOf is the group a tool is listed under in the tool index: its
// Group, else the MCP server of a "<server>__<tool>" name, else the name's
// first "_" segment ("computer_click" → "computer").
func GroupOf(t *Tool) string {
	if t.Group != "" {
		return t.Group
	}
	if i := strings.Index(t.Name, groupSeparator); i > 0 {
		return t.Name[:i]
	}
	if i := strings.Index(t.Name, "_"); i > 0 {
		return t.Name[:i]
	}
	return t.Name
}

// groupSummaries describe the built-in deferred groups in one line.
var groupSummaries = map[string]string{
	"computer":  "operate this computer's apps and windows (screenshots, clicks, typing, scripts)",
	"browser":   "drive a web browser (open pages, click, fill forms, read)",
	"media":     "generate and edit images and videos",
	"followups": "follow-ups: remind yourself to check back on something later",
	"jobs":      "output of background jobs",
	"history":   "search and read earlier conversation history",
	"notes":     "read working notes",
	"team":      "shared team memory",
}

// GroupSummary is a one-line description of a group of deferred tools
// (members: the group's tools, in index order).
func GroupSummary(group string, members []*Tool) string {
	if s, ok := groupSummaries[group]; ok {
		return s
	}
	var names []string
	for _, t := range members {
		n := strings.TrimPrefix(t.Name, group+groupSeparator)
		n = strings.TrimPrefix(n, group+"_")
		names = append(names, n)
		if len(names) == 5 {
			break
		}
	}
	more := ""
	if len(members) > len(names) {
		more = ", …"
	}
	kind := "tools"
	if len(members) > 0 && strings.HasPrefix(members[0].Name, group+groupSeparator) {
		kind = "MCP server tools"
	}
	return fmt.Sprintf("%s, e.g. %s%s", kind, strings.Join(names, ", "), more)
}

// Group is one entry of the deferred tool index.
type Group struct {
	Name    string `json:"name"`
	Count   int    `json:"count"`
	Summary string `json:"summary"`
}

// Groups indexes ts by group, in first-appearance order.
func Groups(ts []*Tool) []Group {
	byName := map[string][]*Tool{}
	var order []string
	for _, t := range ts {
		g := GroupOf(t)
		if _, ok := byName[g]; !ok {
			order = append(order, g)
		}
		byName[g] = append(byName[g], t)
	}
	out := make([]Group, 0, len(order))
	for _, g := range order {
		out = append(out, Group{Name: g, Count: len(byName[g]), Summary: GroupSummary(g, byName[g])})
	}
	return out
}

// ---------------------------------------------------------------- policy

// Tool-search modes (DeferConfig.Mode, agent.Spec.ToolSearch).
const (
	DeferAuto   = "auto"   // defer when the deferred tier is too large (default)
	DeferAlways = "always" // defer whenever there are deferred-tier tools
	DeferNever  = "never"  // send every tool every step
)

// DeferConfig decides when the deferred tier is held back.
type DeferConfig struct {
	Mode string `json:"mode,omitempty"`
	// ContextPercent: in auto mode, defer once the deferred tier's schemas
	// exceed this fraction of the model's context window (default 0.10).
	ContextPercent float64 `json:"contextPercent,omitempty"`
	// MaxTools: in auto mode, defer once there are more deferred-tier
	// tools than this (default 30).
	MaxTools int `json:"maxTools,omitempty"`
}

// Defaults of DeferConfig.
const (
	DefaultDeferPercent  = 0.10
	DefaultDeferMaxTools = 30
)

// WithDefaults fills unset fields.
func (c DeferConfig) WithDefaults() DeferConfig {
	if c.Mode == "" {
		c.Mode = DeferAuto
	}
	if c.ContextPercent <= 0 {
		c.ContextPercent = DefaultDeferPercent
	}
	if c.MaxTools <= 0 {
		c.MaxTools = DefaultDeferMaxTools
	}
	return c
}

// SchemaTokens estimates the tokens of ts's function schemas.
func SchemaTokens(ts []*Tool) int {
	n := 0
	for _, t := range ts {
		b, _ := json.Marshal(t.Schema())
		n += llm.EstimateTokens(string(b))
	}
	return n
}

// ShouldDefer reports whether the deferred tier (deferred) is held back
// for a model with a context window of window tokens.
func (c DeferConfig) ShouldDefer(deferred []*Tool, window int) bool {
	c = c.WithDefaults()
	if len(deferred) == 0 {
		return false
	}
	switch c.Mode {
	case DeferNever:
		return false
	case DeferAlways:
		return true
	}
	if len(deferred) > c.MaxTools {
		return true
	}
	return window > 0 && float64(SchemaTokens(deferred)) > c.ContextPercent*float64(window)
}

// Split partitions ts into the core and deferred tiers (meta-tools are
// left out), keeping order.
func Split(ts []*Tool) (core, deferred []*Tool) {
	for _, t := range ts {
		switch {
		case t.Load == LoadMeta:
		case t.Deferred():
			deferred = append(deferred, t)
		default:
			core = append(core, t)
		}
	}
	return core, deferred
}
