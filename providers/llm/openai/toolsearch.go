package openai

// OpenAI's hosted tool search (Responses API, GPT-5.4 and later; guide
// "Tool search"): the request carries {"type":"tool_search"} and the tools
// the agent loop deferred (llm.ToolSchema.Deferred) with defer_loading:
// true, grouped into namespaces by tool group (an MCP server, a
// connector; ToolSchema.Group), at most nine functions each as the guide
// advises. The search runs on OpenAI's side (execution "server") and the
// response carries tool_search_call and tool_search_output items; the
// output lists the loaded tool definitions inline, so nothing depends on
// stored server state and the items are replayed in place as input on
// later requests (store: false). They are recorded as
// llm.PartProviderBlock parts (the item's JSON in Meta[MetaBlock]) plus a
// llm.PartServerTool summary naming the loaded tools, as the Anthropic
// adapter does. Calls to a namespaced function carry its namespace, kept
// in the tool_call part's Meta[MetaNamespace].

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/hedeqiang/crewbase/core/llm"
)

// Part.Meta keys of tool search.
const (
	// MetaBlock is a recorded Responses item replayed as it came.
	MetaBlock = "openai:block"
	// MetaNamespace is the namespace of a function_call.
	MetaNamespace = "openai:namespace"
)

// namespaceSize is the most functions put in one namespace.
const namespaceSize = 9

var gptVersion = regexp.MustCompile(`^gpt-(\d+)(?:\.(\d+))?`)

// toolSearchModel: GPT-5.4 and later take the tool_search tool.
func toolSearchModel(model string) bool {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	v := gptVersion.FindStringSubmatch(m)
	if v == nil {
		return false
	}
	major, _ := strconv.Atoi(v[1])
	minor := 0
	if v[2] != "" {
		minor, _ = strconv.Atoi(v[2])
	}
	return major > 5 || (major == 5 && minor >= 4)
}

// SearchesTools (llm.ToolSearcher) reports whether requests for model use
// OpenAI's hosted tool search: they must go through Responses, and the
// model must take it. On by default for OpenAI's own API only, since
// gateways may not pass the tool through; "toolSearch" forces it.
func (a *Adapter) SearchesTools(model string) bool {
	if a.routes.responsesMissing() || !a.routesToResponses(model) {
		return false
	}
	if a.toolSearch != nil {
		return *a.toolSearch
	}
	return a.official && toolSearchModel(model)
}

// searchTools: req uses tool search (some tools deferred).
func (a *Adapter) searchTools(req llm.Request) bool {
	for _, t := range req.Tools {
		if t.Deferred {
			return a.SearchesTools(req.Model)
		}
	}
	return false
}

var badNamespaceChar = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// namespacePrefix starts every namespace Crewbase sends. OpenAI keeps
// names for its own tools and refuses a function namespace by one of them
// ("Function 'history.history_search' is not allowed in reserved namespace
// 'history'", and 'browser' before it); which names it keeps is not
// published and grows, and groups come from tool names and from MCP server
// names people choose. A prefix of our own never meets one.
const namespacePrefix = "crew_"

// namespaceName makes a group name a namespace name: the prefix, then
// the group with characters a namespace may not hold replaced.
func namespaceName(group string) string {
	n := strings.Trim(badNamespaceChar.ReplaceAllString(group, "_"), "_")
	if n == "" {
		n = "tools"
	}
	n = namespacePrefix + n
	if len(n) > 48 {
		n = n[:48]
	}
	return n
}

// searchReplayable reports whether m's tool search items may be replayed
// into a request with these namespaces: every namespace a recorded search
// loaded must still be there. One recorded under another name (before
// namespacePrefix, or a group since renamed) is left out, with the search
// call that went with it; the function calls keep their place, without
// the namespace (responsesInput).
func searchReplayable(m llm.Message, namespaces map[string]bool) bool {
	for _, p := range m.Parts {
		raw := p.Meta[MetaBlock]
		if p.Type != llm.PartProviderBlock || raw == "" {
			continue
		}
		var it struct {
			Type  string `json:"type"`
			Tools []struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"tools"`
		}
		if json.Unmarshal([]byte(raw), &it) != nil {
			return false
		}
		for _, t := range it.Tools {
			if t.Type == "namespace" && !namespaces[t.Name] {
				return false
			}
		}
	}
	return true
}

// searchToolList maps tools for a tool search request: loaded tools as
// functions, deferred ones in namespaces, then the tool_search tool. It
// returns the namespace names used.
func (a *Adapter) searchToolList(ts []llm.ToolSchema) ([]any, map[string]bool) {
	var tl []any
	var order []string
	groups := map[string][]llm.ToolSchema{}
	for _, t := range ts {
		if !t.Deferred {
			tl = append(tl, functionTool(t, false))
			continue
		}
		g := t.Group
		if g == "" {
			g = groupOf(t.Name)
		}
		g = namespaceName(g)
		if _, ok := groups[g]; !ok {
			order = append(order, g)
		}
		groups[g] = append(groups[g], t)
	}
	names := map[string]bool{}
	for _, g := range order {
		members := groups[g]
		for i := 0; i < len(members); i += namespaceSize {
			chunk := members[i:min(i+namespaceSize, len(members))]
			name := g
			if i > 0 {
				name = g + "_" + strconv.Itoa(i/namespaceSize+1)
			}
			var fns []any
			var fnames []string
			for _, t := range chunk {
				fns = append(fns, functionTool(t, true))
				fnames = append(fnames, t.Name)
			}
			names[name] = true
			tl = append(tl, map[string]any{"type": "namespace", "name": name,
				"description": "Tools of " + g + ": " + strings.Join(fnames, ", "), "tools": fns})
		}
	}
	tl = append(tl, map[string]any{"type": "tool_search"})
	return tl, names
}

// groupOf guesses a tool's group from its name ("github__create_issue",
// "computer_click"), as tools.GroupOf does.
func groupOf(name string) string {
	if i := strings.Index(name, "__"); i > 0 {
		return name[:i]
	}
	if i := strings.Index(name, "_"); i > 0 {
		return name[:i]
	}
	return name
}

// searchParts records a tool_search_call / tool_search_output item: the
// item to replay and, for an output, a summary naming the loaded tools.
// query carries the last call's arguments over to its output.
func searchParts(it rItem, raw json.RawMessage, query *string) []llm.Part {
	parts := []llm.Part{{Type: llm.PartProviderBlock, Meta: map[string]string{MetaBlock: string(raw)}}}
	if it.Type == "tool_search_call" {
		*query = searchQuery(it.Args)
		return parts
	}
	var names []string
	for _, t := range it.Tools {
		var d struct {
			Type  string `json:"type"`
			Name  string `json:"name"`
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if json.Unmarshal(t, &d) != nil {
			continue
		}
		if d.Type == "namespace" {
			for _, f := range d.Tools {
				names = append(names, f.Name)
			}
		} else if d.Name != "" {
			names = append(names, d.Name)
		}
	}
	text := "搜索工具：" + *query
	if len(names) > 0 {
		text += " → " + strings.Join(names, ", ")
	}
	*query = ""
	return append(parts, llm.Part{Type: llm.PartServerTool, Text: text,
		Meta: map[string]string{"kind": llm.ServerToolSearch, "tools": strings.Join(names, ",")}})
}

// searchQuery renders tool_search_call arguments ({"paths": [...]},
// {"goal": "..."}, or a JSON string of either) for people.
func searchQuery(args json.RawMessage) string {
	var s string
	if json.Unmarshal(args, &s) == nil {
		args = json.RawMessage(s)
	}
	var m map[string]any
	if json.Unmarshal(args, &m) != nil {
		return strings.TrimSpace(string(args))
	}
	var out []string
	for _, k := range []string{"query", "goal", "paths"} {
		switch v := m[k].(type) {
		case string:
			out = append(out, v)
		case []any:
			for _, x := range v {
				if s, ok := x.(string); ok {
					out = append(out, s)
				}
			}
		}
	}
	return strings.Join(out, ", ")
}
