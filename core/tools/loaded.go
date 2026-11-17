package tools

import "github.com/hedeqiang/crewbase/core/session"

// TypeToolsLoaded records deferred tools an agent loaded into a session
// (tool search). It is session state, not message content: it survives
// restarts and compaction, and the agent loop offers the loaded tools
// from the next step on.
const TypeToolsLoaded = "tools/loaded"

// LoadedData is the payload of TypeToolsLoaded.
type LoadedData struct {
	Names []string `json:"names"`
	// Via is how they were loaded: ViaSearch, ViaCall or ViaNative.
	Via string `json:"via,omitempty"`
}

// How tools were loaded (LoadedData.Via).
const (
	ViaSearch = "search" // the tool_search meta-tool
	ViaCall   = "call"   // the model called a deferred tool it had not loaded
	ViaNative = "native" // the provider's own tool search
)

// Loaded lists the tools agent loaded in events, in load order.
func Loaded(events []session.Event, agent string) []string {
	return LoadedVia(events, agent, nil)
}

// LoadedVia lists the tools agent loaded in events in a way keep accepts
// (nil: any), in load order.
func LoadedVia(events []session.Event, agent string, keep func(via string) bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, e := range events {
		if e.Type != TypeToolsLoaded || e.Agent != agent {
			continue
		}
		var d LoadedData
		if e.Decode(&d) != nil || (keep != nil && !keep(d.Via)) {
			continue
		}
		for _, n := range d.Names {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	return out
}

// MarkLoaded records names (those not loaded yet) as loaded for agent in
// ss; it returns the newly loaded ones.
func MarkLoaded(ss *session.Session, agent string, names []string, via string) ([]string, error) {
	have := map[string]bool{}
	for _, n := range Loaded(ss.Events(), agent) {
		have[n] = true
	}
	var add []string
	for _, n := range names {
		if !have[n] {
			have[n] = true
			add = append(add, n)
		}
	}
	if len(add) == 0 {
		return nil, nil
	}
	_, err := ss.Append(TypeToolsLoaded, agent, LoadedData{Names: add, Via: via})
	return add, err
}
