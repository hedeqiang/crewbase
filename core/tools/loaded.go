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
}

// Loaded lists the tools agent loaded in events, in load order.
func Loaded(events []session.Event, agent string) []string {
	var out []string
	seen := map[string]bool{}
	for _, e := range events {
		if e.Type != TypeToolsLoaded || e.Agent != agent {
			continue
		}
		var d LoadedData
		if e.Decode(&d) != nil {
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
func MarkLoaded(ss *session.Session, agent string, names []string) ([]string, error) {
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
	_, err := ss.Append(TypeToolsLoaded, agent, LoadedData{Names: add})
	return add, err
}
