// Package prompt assembles the system prompt from scoped fragments. Agent
// fragments shadow global fragments with the same name.
package prompt

import (
	"sort"
	"strings"

	"github.com/hedeqiang/crewbase/core/scope"
	"github.com/hedeqiang/crewbase/kernel"
)

const ServiceKey = "prompt"

// Fragment is a named piece of the system prompt. Lower Order renders first.
type Fragment struct {
	Name   string
	Order  int
	Render func(agent string) string
}

// Static returns a fragment with fixed text.
func Static(name string, order int, text string) *Fragment {
	return &Fragment{Name: name, Order: order, Render: func(string) string { return text }}
}

// Assembler is the `prompt` service.
type Assembler struct {
	reg *scope.Registry[*Fragment]
}

func NewAssembler() *Assembler { return &Assembler{reg: scope.New[*Fragment](nil)} }

// Register adds a fragment in ctx's scope.
func (a *Assembler) Register(c *kernel.Context, f *Fragment) { a.reg.Register(c, f.Name, f) }

// Section is one rendered fragment (used by the context-usage breakdown).
type Section struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// Sections renders the fragments visible to agent in order.
func (a *Assembler) Sections(agent string) []Section {
	vis := a.reg.Visible(agent)
	sort.SliceStable(vis, func(i, j int) bool { return vis[i].Value.Order < vis[j].Value.Order })
	var out []Section
	for _, n := range vis {
		if t := strings.TrimSpace(n.Value.Render(agent)); t != "" {
			out = append(out, Section{Name: n.Name, Text: t})
		}
	}
	return out
}

// Assemble renders the system prompt for agent.
func (a *Assembler) Assemble(agent string) string {
	var parts []string
	for _, s := range a.Sections(agent) {
		parts = append(parts, s.Text)
	}
	return strings.Join(parts, "\n\n")
}

// Plugin provides the `prompt` service with a base fragment.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/prompt",
	Description: "System prompt assembly from scoped fragments.",
	Apply: func(c *kernel.Context) error {
		a := NewAssembler()
		a.Register(c, Static("base", 0, "You are an agent in Crewbase, a local-first multi-agent workspace. "+
			"Work inside your workspace directory. Use tools to act; explain briefly what you did. "+
			"Some actions need human approval; if one is not approved, re-plan instead of retrying it."))
		return c.Provide(ServiceKey, a)
	},
})
