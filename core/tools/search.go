package tools

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// In-process BM25 over tool names, descriptions and parameter names, for
// the generic tool_search meta-tool (providers without a native search).

// SearchHit is one ranked tool.
type SearchHit struct {
	Tool  *Tool
	Score float64
}

// Index is a BM25 index of tools.
type Index struct {
	tools []*Tool
	tf    []map[string]float64 // weighted term frequency per tool
	lens  []float64
	avg   float64
	df    map[string]int
}

// Field weights: a word in the name says more than one in the prose.
const (
	weightName  = 3.0
	weightParam = 1.5
	weightDesc  = 1.0
	bm25K1      = 1.2
	bm25B       = 0.75
)

// NewIndex indexes ts.
func NewIndex(ts []*Tool) *Index {
	ix := &Index{tools: ts, df: map[string]int{}}
	total := 0.0
	for _, t := range ts {
		tf := map[string]float64{}
		add := func(text string, w float64) {
			for _, tok := range Tokenize(text) {
				tf[tok] += w
			}
		}
		add(t.Name, weightName)
		add(firstLine(stripMCPPrefix(t.Description), 600), weightDesc)
		for _, p := range paramNames(t.Params) {
			add(p, weightParam)
		}
		n := 0.0
		for tok, f := range tf {
			n += f
			ix.df[tok]++
		}
		ix.tf = append(ix.tf, tf)
		ix.lens = append(ix.lens, n)
		total += n
	}
	if len(ts) > 0 {
		ix.avg = total / float64(len(ts))
	}
	return ix
}

// Search ranks the tools for query; at most limit hits with a score > 0.
func (ix *Index) Search(query string, limit int) []SearchHit {
	// Each query word scores a tool by its own match or, when the tool
	// lacks the word, by its best synonym at half weight. A word naming a tool's group (an MCP server) boosts
	// the tools of that group instead of being scored as text.
	var words []string
	seen := map[string]bool{}
	for _, tok := range Tokenize(query) {
		if !stopwords[tok] && !seen[tok] {
			seen[tok] = true
			words = append(words, tok)
		}
	}
	n := float64(len(ix.tools))
	idf := func(term string) float64 {
		df := float64(ix.df[term])
		return math.Log(1 + (n-df+0.5)/(df+0.5))
	}
	// termScore is BM25 of term in tool i, weighted by the rarity of as
	// (a synonym counts as rare as the word it stands in for).
	termScore := func(i int, term, as string) float64 {
		f := ix.tf[i][term]
		if f == 0 {
			return 0
		}
		w := idf(as)
		if ix.df[as] == 0 {
			w = idf(term)
		}
		norm := 1 - bm25B + bm25B*ix.lens[i]/math.Max(ix.avg, 1)
		return w * f * (bm25K1 + 1) / (f + bm25K1*norm)
	}
	var hits []SearchHit
	for i, t := range ix.tools {
		group := strings.ToLower(GroupOf(t))
		s, boost := 0.0, 1.0
		for _, w := range words {
			if w == group {
				// the group name is in every member's name: it ranks
				// nothing within the group, it selects the group
				boost += 0.5
				s += 0.01
				continue
			}
			best := termScore(i, w, w)
			if best == 0 { // synonyms only stand in for a missing word
				for _, syn := range synonyms[w] {
					best = math.Max(best, 0.5*termScore(i, syn, w))
				}
			}
			s += best
		}
		if s > 0 {
			hits = append(hits, SearchHit{Tool: t, Score: s * boost})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].Score > hits[b].Score })
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// Tokenize lowercases text and splits it into stemmed words, breaking
// snake_case, kebab-case and camelCase.
func Tokenize(text string) []string {
	var out []string
	emit := func(w string) {
		if w = stem(strings.ToLower(w)); w != "" {
			out = append(out, w)
		}
	}
	// word: a run of letters and digits; a camelCase word also yields
	// its parts (pullNumber → pullnumber, pull, number).
	word := func(rs []rune) {
		var parts []string
		start := 0
		for i := 1; i < len(rs); i++ {
			if unicode.IsUpper(rs[i]) && unicode.IsLower(rs[i-1]) {
				parts = append(parts, string(rs[start:i]))
				start = i
			}
		}
		if len(parts) == 0 {
			emit(string(rs))
			return
		}
		parts = append(parts, string(rs[start:]))
		emit(string(rs))
		for _, p := range parts {
			emit(p)
		}
	}
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			word(cur)
			cur = cur[:0]
		}
	}
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r):
			flush() // CJK: one token per character
			out = append(out, string(r))
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			cur = append(cur, r)
		default:
			flush()
		}
	}
	flush()
	return out
}

// stem is a light English suffix stripper (pages → page, listing → list).
func stem(w string) string {
	if len(w) <= 3 {
		return w
	}
	switch {
	case strings.HasSuffix(w, "ies") && len(w) > 4:
		return w[:len(w)-3] + "y"
	case strings.HasSuffix(w, "sses"):
		return w[:len(w)-2]
	case strings.HasSuffix(w, "ing") && len(w) > 5:
		return w[:len(w)-3]
	case strings.HasSuffix(w, "ed") && len(w) > 4 && !strings.HasSuffix(w, "eed"):
		return w[:len(w)-2]
	case strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && !strings.HasSuffix(w, "us"):
		return w[:len(w)-1]
	}
	return w
}

var stopwords = map[string]bool{
	"a": true, "an": true, "the": true, "to": true, "of": true, "in": true, "on": true, "for": true,
	"and": true, "or": true, "with": true, "my": true, "me": true, "i": true, "some": true, "all": true,
	"from": true, "into": true, "by": true, "at": true, "is": true, "it": true, "thi": true, "that": true,
	"tool": true, "use": true,
}

// synonyms widen a query word to the verbs APIs use for it (half weight).
var synonyms = map[string][]string{
	"create": {"post", "add", "new", "insert", "make"},
	"add":    {"create", "post", "insert"},
	"make":   {"create"},
	"open":   {"create"},
	"new":    {"create"},
	"list":   {"get", "search", "query", "retrieve"},
	"find":   {"search", "query", "list", "get"},
	"search": {"find", "query", "list"},
	"get":    {"retrieve", "read", "fetch"},
	"read":   {"get", "retrieve", "fetch"},
	"fetch":  {"get", "retrieve", "read"},
	"update": {"patch", "edit", "modify", "set"},
	"edit":   {"update", "patch", "modify"},
	"delete": {"remove", "archive", "trash"},
	"remove": {"delete", "archive"},
	"send":   {"post", "create"},
	"pr":     {"pull", "request"},
	"email":  {"gmail", "mail", "message"},
	"mail":   {"gmail", "email"},
}

// paramNames lists a JSON Schema object's property names.
func paramNames(schema map[string]any) []string {
	props, _ := schema["properties"].(map[string]any)
	out := make([]string, 0, len(props))
	for k := range props {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// stripMCPPrefix drops the "[MCP <server>] " prefix MCP descriptions get.
func stripMCPPrefix(desc string) string {
	if strings.HasPrefix(desc, "[MCP ") {
		if i := strings.Index(desc, "] "); i > 0 {
			return desc[i+2:]
		}
	}
	return desc
}

// firstLine is the start of s: up to the first blank line, at most n bytes.
func firstLine(s string, n int) string {
	if i := strings.Index(s, "\n\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > n {
		s = s[:n]
	}
	return s
}

// OneLine is a tool description shortened to one line for search results
// (the "[MCP server] " prefix dropped).
func OneLine(desc string) string {
	desc = strings.TrimSpace(stripMCPPrefix(desc))
	if i := strings.IndexAny(desc, "\n"); i >= 0 {
		desc = desc[:i]
	}
	if i := strings.Index(desc, ". "); i > 0 && i < 200 {
		desc = desc[:i+1]
	}
	if r := []rune(desc); len(r) > 200 {
		desc = string(r[:199]) + "…"
	}
	return desc
}
