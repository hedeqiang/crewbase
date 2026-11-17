package tools

// Connector scoping: tools that come from a connector (an MCP server, a
// built-in connector such as Gmail) carry its name in Tool.Source. An agent
// may be limited to some connectors (agent.Spec.Connectors); the registry
// then hides the other connectors' tools from it — in Visible, SchemasFor
// and Get, so they can neither be offered nor called. Tools without a
// Source (the built-in tools) are never filtered this way.

import (
	"slices"
	"sort"
	"sync"

	"github.com/hedeqiang/crewbase/kernel"
)

// ConnectorInfo describes one connector agents can be given, as a plugin
// that provides connector tools reports it (for checklists and checks).
type ConnectorInfo struct {
	// Name is the Tool.Source value of its tools.
	Name string `json:"name"`
	// Label is its display name ("" = Name).
	Label string `json:"label,omitempty"`
	// Kind is "mcp" or "builtin".
	Kind string `json:"kind"`
	// Scope is "global", an agent id or team:<id>.
	Scope string `json:"scope"`
	// Tools it registers now.
	Tools int `json:"tools"`
}

type connectorLister struct {
	list func() []ConnectorInfo
}

type connectorState struct {
	mu      sync.Mutex
	listers []*connectorLister
	// of resolves an agent's connector list (ok=false: no limit).
	of func(agent string) (names []string, ok bool)
}

// SetConnectorsOf installs the agent → connector list resolver (set by the
// agents plugin). ok=false means the agent may use every connector its
// scope allows.
func (r *Registry) SetConnectorsOf(f func(agent string) (names []string, ok bool)) {
	r.conns.mu.Lock()
	r.conns.of = f
	r.conns.mu.Unlock()
	r.Touch()
}

// Touch reports that what agents can see changed without a registration
// (e.g. an agent's connector list was edited).
func (r *Registry) Touch() {
	if r.host != nil {
		Changed.Emit(r.host, struct{}{})
	}
}

// connectorAllowed: t is not from a connector, or agent may use its connector.
func (r *Registry) connectorAllowed(agent string, t *Tool) bool {
	if t.Source == "" || agent == "" {
		return true
	}
	r.conns.mu.Lock()
	of := r.conns.of
	r.conns.mu.Unlock()
	if of == nil {
		return true
	}
	names, limited := of(agent)
	return !limited || slices.Contains(names, t.Source)
}

// AddConnectors registers a source of ConnectorInfo for ctx's lifetime.
func (r *Registry) AddConnectors(c *kernel.Context, list func() []ConnectorInfo) {
	l := &connectorLister{list: list}
	r.conns.mu.Lock()
	r.conns.listers = append(r.conns.listers, l)
	r.conns.mu.Unlock()
	c.Defer(func() {
		r.conns.mu.Lock()
		r.conns.listers = slices.DeleteFunc(r.conns.listers, func(x *connectorLister) bool { return x == l })
		r.conns.mu.Unlock()
	})
}

// Connectors lists every known connector, sorted by name.
func (r *Registry) Connectors() []ConnectorInfo {
	r.conns.mu.Lock()
	ls := slices.Clone(r.conns.listers)
	r.conns.mu.Unlock()
	seen := map[string]bool{}
	out := []ConnectorInfo{}
	for _, l := range ls {
		for _, x := range l.list() {
			if x.Name == "" || seen[x.Name] {
				continue
			}
			seen[x.Name] = true
			out = append(out, x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// HasConnector reports whether name is a known connector.
func (r *Registry) HasConnector(name string) bool {
	for _, x := range r.Connectors() {
		if x.Name == name {
			return true
		}
	}
	return false
}
