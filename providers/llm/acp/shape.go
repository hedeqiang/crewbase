package acp

import (
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/hedeqiang/crewbase/core/llm"
)

// How raw model lists become the models Crewbase offers, per CLI. Every
// id a bridge reported keeps working when sent back (old agent configs
// name them); shaping only changes what pickers show.
//
//   - codex: the CLI's own list (codex.go) when read, one model per slug
//     with its reasoning levels; else the bridge's "<slug>/<effort>" ids
//     folded the same way.
//   - cursor: cursor-agent's ACP reports one id per variant,
//     "claude-opus-5-5[context=300k,effort=medium,fast=false]", and its
//     session/set_model takes exactly those (unstable_setSessionModel ->
//     applyVariantModelConfigOption matches variantStringRepresentation;
//     the short ids of `cursor-agent models` are refused there). Variants
//     that differ only in effort become one model with reasoning levels;
//     the id shown is the base model plus the parameters that tell its
//     variants apart ("claude-opus-5-5-fast").
//   - antigravity: agy lists levels as models ("gemini-4-carbon-low",
//     "-medium", "-high"); they fold into "gemini-4-carbon" with levels,
//     and the level's own id is sent.
//   - claude-code: the bridge's aliases and the person's modelPicker
//     entries as reported; exact duplicates dropped, a readable name for
//     ids the bridge does not name.
//   - anything else: as reported.

// modelEntry is one model as offered to Crewbase.
type modelEntry struct {
	ID          string
	Name        string
	Description string
	Efforts     []string
	// Wire maps a reasoning level ("" = none chosen) to the id the bridge
	// takes; empty for models sent as their own id.
	Wire map[string]string
	// Native: a codex model from the CLI's own list; the id sent depends
	// on the bridge (see codexWire).
	Native        bool
	DefaultEffort string
	ContextWindow int
	Vision        bool
}

// shapeModels turns what the bridge reported (acp) and the CLI's own list
// (native) into the offered models.
func shapeModels(agentID string, acp []acpModel, native []nativeModel) []modelEntry {
	acp = dedupe(acp)
	switch agentID {
	case "codex":
		if len(native) > 0 {
			return codexNative(acp, native)
		}
		return foldSlash(acp)
	case "cursor":
		names := map[string]string{}
		for _, n := range native {
			names[n.ID] = n.Name
		}
		return foldVariants(acp, names)
	case "antigravity":
		return foldSuffix(acp)
	case claudeCodeID:
		out := plain(acp)
		for i := range out {
			if out[i].Name == "" || out[i].Name == out[i].ID {
				out[i].Name = readableName(out[i].ID)
			}
		}
		return out
	}
	return plain(acp)
}

// dedupe drops repeated ids and the bridge's "default" (the adapter lists
// its own).
func dedupe(ms []acpModel) []acpModel {
	seen := map[string]bool{}
	out := make([]acpModel, 0, len(ms))
	for _, m := range ms {
		if m.ModelID == "" || m.ModelID == defaultModel || seen[m.ModelID] {
			continue
		}
		seen[m.ModelID] = true
		out = append(out, m)
	}
	return out
}

func plain(ms []acpModel) []modelEntry {
	out := make([]modelEntry, 0, len(ms))
	for _, m := range ms {
		out = append(out, modelEntry{ID: m.ModelID, Name: m.Name, Description: m.Description})
	}
	return out
}

// readableName names an id like "auto/claude-sonnet-5-5[1m]" (a Claude
// Code modelPicker entry routed through a gateway) as
// "claude-sonnet-5-5 [1m] · auto".
func readableName(id string) string {
	via, model, ok := strings.Cut(id, "/")
	if !ok {
		via, model = "", id
	}
	if i := strings.IndexByte(model, '['); i > 0 && strings.HasSuffix(model, "]") {
		model = model[:i] + " " + model[i:]
	}
	if via != "" {
		return model + " · " + via
	}
	return model
}

// codexNative offers the CLI's list, plus models only the bridge reports
// as a bare id (a model set in the person's Codex config that is not a
// preset).
func codexNative(acp []acpModel, native []nativeModel) []modelEntry {
	out := make([]modelEntry, 0, len(native))
	have := map[string]bool{}
	for _, n := range native {
		have[n.ID] = true
		out = append(out, modelEntry{ID: n.ID, Name: n.Name, Description: n.Description, Efforts: n.Efforts, Native: true,
			DefaultEffort: n.DefaultEffort, ContextWindow: n.ContextWindow, Vision: n.Vision})
	}
	for _, m := range acp {
		if !strings.Contains(m.ModelID, "/") && !have[m.ModelID] {
			have[m.ModelID] = true
			out = append(out, modelEntry{ID: m.ModelID, Name: m.Name, Description: m.Description})
		}
	}
	return out
}

// effortOf maps a level as CLIs write it to Crewbase's ("" if none).
func effortOf(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "extra high", "extra-high", "extra_high", "x-high":
		return "xhigh"
	}
	if slices.Contains(llm.Efforts, s) {
		return s
	}
	return ""
}

// group collects variants of one model.
type group struct {
	key     string
	base    string
	params  map[string]string // non-effort parameters (cursor)
	members []acpModel
	efforts []string // parallel to members ("" = none)
}

// lvlOf returns a variant's level value as spelled ("medium", "high").
func lvlOf(params [][2]string) string {
	for _, kv := range params {
		if isEffortKey(kv[0]) {
			return kv[1]
		}
	}
	return ""
}

// cliName finds a variant's name in `cursor-agent models`, whose ids are
// the base plus level and "-fast" ("claude-opus-5-5-high-fast"). The level
// word is dropped when the model offers several.
func cliName(cli map[string]string, g *group, rep acpModel) string {
	if len(cli) == 0 || g.params == nil {
		return ""
	}
	_, params, _ := variant(rep.ModelID)
	lvl := lvlOf(params)
	fast := g.params["fast"] == "true"
	var tries []string
	for _, l := range []string{lvl, ""} {
		id := g.base
		if l != "" {
			id += "-" + l
		}
		if fast {
			tries = append(tries, id+"-fast")
		} else {
			tries = append(tries, id)
		}
	}
	for _, id := range tries {
		if n := cli[id]; n != "" {
			if len(g.members) > 1 {
				n = strings.TrimSuffix(n, " Fast")
				n = stripLevel(n)
				if fast {
					n += " Fast"
				}
			}
			return n
		}
	}
	return ""
}

// fold turns groups into entries: a group whose members all carry a
// distinct level becomes one model with those levels; otherwise its
// members stay separate models.
func fold(groups []*group, id func(g *group) string, name func(g *group, rep acpModel) string) []modelEntry {
	var out []modelEntry
	for _, g := range groups {
		levels := map[string]string{}
		ok := true
		for i, m := range g.members {
			e := g.efforts[i]
			if e == "" || levels[e] != "" {
				ok = false
				break
			}
			levels[e] = m.ModelID
		}
		if len(g.members) == 1 {
			// one variant: its own model, under the short id
			m := g.members[0]
			e := modelEntry{ID: m.ModelID, Name: m.Name, Description: m.Description}
			if gid := id(g); gid != m.ModelID {
				e.ID, e.Name, e.Wire = gid, name(g, m), map[string]string{"": m.ModelID}
			}
			out = append(out, e)
			continue
		}
		if !ok || len(levels) < 2 {
			for _, m := range g.members {
				out = append(out, modelEntry{ID: m.ModelID, Name: m.Name, Description: m.Description})
			}
			continue
		}
		var efforts []string
		for _, e := range llm.Efforts {
			if levels[e] != "" {
				efforts = append(efforts, e)
			}
		}
		rep := g.members[0]
		def := "medium"
		if levels[def] == "" {
			def = efforts[0]
		}
		for i, m := range g.members {
			if g.efforts[i] == def {
				rep = m
			}
		}
		wire := map[string]string{"": levels[def]}
		for e, w := range levels {
			wire[e] = w
		}
		out = append(out, modelEntry{ID: id(g), Name: name(g, rep), Description: rep.Description, Efforts: efforts, Wire: wire, DefaultEffort: def})
	}
	// an id made from a base must not shadow another reported model
	seen := map[string]int{}
	for _, e := range out {
		seen[e.ID]++
	}
	for i, e := range out {
		if seen[e.ID] > 1 && e.Wire != nil {
			out[i].ID = e.Wire[""]
		}
	}
	return out
}

var reLevelWord = regexp.MustCompile(`(?i)[\s(\[-]*\b(minimal|low|medium|high|extra high|xhigh|max)\b[)\]]*\s*$`)

// stripLevel removes a trailing level word from a display name.
func stripLevel(name string) string {
	return strings.TrimSpace(reLevelWord.ReplaceAllString(name, ""))
}

// foldSlash folds codex-acp's "<slug>/<effort>" ids.
func foldSlash(ms []acpModel) []modelEntry {
	var groups []*group
	by := map[string]*group{}
	for _, m := range ms {
		base, lvl, ok := strings.Cut(m.ModelID, "/")
		e := ""
		if ok {
			e = effortOf(lvl)
		}
		if !ok || e == "" {
			base, e = m.ModelID, ""
		}
		g := by[base]
		if g == nil {
			g = &group{key: base, base: base}
			by[base] = g
			groups = append(groups, g)
		}
		g.members = append(g.members, m)
		g.efforts = append(g.efforts, e)
	}
	return fold(groups, func(g *group) string { return g.base }, func(_ *group, rep acpModel) string { return stripLevel(rep.Name) })
}

var reSuffixLevel = regexp.MustCompile(`^(.+)-(minimal|low|medium|high|xhigh|max)$`)

// foldSuffix folds "<model>-<level>" ids when a model has two or more.
func foldSuffix(ms []acpModel) []modelEntry {
	count := map[string]int{}
	for _, m := range ms {
		if sm := reSuffixLevel.FindStringSubmatch(m.ModelID); sm != nil {
			count[sm[1]]++
		}
	}
	var groups []*group
	by := map[string]*group{}
	for _, m := range ms {
		base, e := m.ModelID, ""
		if sm := reSuffixLevel.FindStringSubmatch(m.ModelID); sm != nil && count[sm[1]] > 1 {
			base, e = sm[1], sm[2]
		}
		key := base
		if e == "" {
			key = "\x00" + m.ModelID // never joins a folded model
		}
		g := by[key]
		if g == nil {
			g = &group{key: key, base: base}
			by[key] = g
			groups = append(groups, g)
		}
		g.members = append(g.members, m)
		g.efforts = append(g.efforts, e)
	}
	return fold(groups, func(g *group) string { return g.base }, func(_ *group, rep acpModel) string { return stripLevel(rep.Name) })
}

// variant parses "base[k=v,k=v]" (cursor-agent); ok=false for other ids.
func variant(id string) (base string, params [][2]string, ok bool) {
	i := strings.IndexByte(id, '[')
	if i <= 0 || !strings.HasSuffix(id, "]") {
		return "", nil, false
	}
	if id[i+1:len(id)-1] == "" {
		return id[:i], nil, true // "gemini-3.1-pro[]"
	}
	for _, kv := range strings.Split(id[i+1:len(id)-1], ",") {
		k, v, ok := strings.Cut(kv, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" {
			return "", nil, false
		}
		params = append(params, [2]string{k, v})
	}
	return id[:i], params, true
}

func isEffortKey(k string) bool {
	switch strings.ToLower(k) {
	case "effort", "reasoning", "reasoning_effort", "thinking_level", "thought_level":
		return true
	}
	return false
}

// foldVariants folds cursor-agent's variant ids. Its ACP names are the
// bare base ids, so names come from `cursor-agent models` (cli: short id
// -> name, e.g. "claude-opus-5-5-high" -> "Claude Opus 5.5 1M High") when
// read.
func foldVariants(ms []acpModel, cli map[string]string) []modelEntry {
	var groups []*group
	by := map[string]*group{}
	for _, m := range ms {
		base, params, ok := variant(m.ModelID)
		if !ok {
			g := &group{key: "\x00" + m.ModelID, base: m.ModelID}
			g.members, g.efforts = []acpModel{m}, []string{""}
			groups = append(groups, g)
			continue
		}
		e := ""
		rest := map[string]string{}
		var keys []string
		for _, kv := range params {
			if isEffortKey(kv[0]) && e == "" {
				if e = effortOf(kv[1]); e != "" {
					continue
				}
			}
			rest[kv[0]] = kv[1]
			keys = append(keys, kv[0]+"="+kv[1])
		}
		sort.Strings(keys)
		key := base + "[" + strings.Join(keys, ",") + "]"
		g := by[key]
		if g == nil {
			g = &group{key: key, base: base, params: rest}
			by[key] = g
			groups = append(groups, g)
		}
		g.members = append(g.members, m)
		g.efforts = append(g.efforts, e)
	}
	// parameters whose value differs between groups of one base name them
	varies := map[string]map[string]bool{} // base -> keys
	vals := map[string]map[string]string{}
	for _, g := range groups {
		if g.params == nil {
			continue
		}
		if vals[g.base] == nil {
			vals[g.base], varies[g.base] = map[string]string{}, map[string]bool{}
		}
		for k, v := range g.params {
			if old, ok := vals[g.base][k]; ok && old != v {
				varies[g.base][k] = true
			}
			vals[g.base][k] = v
		}
	}
	for _, g := range groups {
		if g.params == nil {
			continue
		}
		for k := range vals[g.base] {
			if _, ok := g.params[k]; !ok {
				varies[g.base][k] = true
			}
		}
	}
	id := func(g *group) string {
		if g.params == nil {
			return g.members[0].ModelID
		}
		var keys []string
		for k := range varies[g.base] {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := g.base
		for _, k := range keys {
			v, ok := g.params[k]
			switch {
			case !ok || v == "" || v == "false":
			case v == "true":
				out += "-" + k
			default:
				out += "-" + v
			}
		}
		return out
	}
	name := func(g *group, rep acpModel) string {
		if n := cliName(cli, g, rep); n != "" {
			return n
		}
		n := rep.Name
		if len(g.members) > 1 {
			n = stripLevel(n)
		}
		if n == "" || n == rep.ModelID {
			n = id(g)
		}
		return n
	}
	out := fold(groups, id, name)
	// variants that did not fold still get a short id when it is free
	taken := map[string]bool{}
	for _, e := range out {
		taken[e.ID] = true
	}
	for i, e := range out {
		if e.Wire != nil {
			continue
		}
		if base, params, ok := variant(e.ID); ok {
			short := base
			for _, kv := range params {
				switch {
				case kv[1] == "" || kv[1] == "false":
				case kv[1] == "true":
					short += "-" + kv[0]
				default:
					short += "-" + kv[1]
				}
			}
			if !taken[short] {
				taken[short] = true
				if out[i].Name == "" || out[i].Name == e.ID {
					out[i].Name = short
				}
				out[i].Wire = map[string]string{"": e.ID}
				out[i].ID = short
			}
		}
	}
	return out
}

// entry finds an offered model by id.
func findEntry(es []modelEntry, id string) *modelEntry {
	for i := range es {
		if es[i].ID == id {
			return &es[i]
		}
	}
	return nil
}

// wire is the id to send for e at a reasoning level when the bridge takes
// one id per variant: the level's own, else the model's default.
func (e *modelEntry) wire(effort string) string {
	if e.Wire == nil {
		return e.ID
	}
	if w := e.Wire[effort]; w != "" {
		return w
	}
	return e.Wire[""]
}
