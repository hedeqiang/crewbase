package compaction

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/redact"
)

// Item kinds are the segment keys, plus SegOther: what the provider counted
// beyond the estimate (a tokenizer difference; nothing to show).
const SegOther = "other"

// MaxItems caps Breakdown.Items; the smallest items beyond it are rolled
// up into one "N more" item per kind.
const MaxItems = 300

// MaxItemContent caps the text of one item's content (bytes).
const MaxItemContent = 100 << 10

// Item is one block of the context map: a system prompt section, a tool
// definition, a tool call with its result, a message, or a roll-up.
// The items of a kind add up to the segment of that kind.
type Item struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Group  string `json:"group,omitempty"`
	Tokens int    `json:"tokens"`
	// Count is the number of items a roll-up ("N more") stands for.
	Count int `json:"count,omitempty"`

	content func() []ContentPart
}

// ContentPart is one titled block of an item's content.
type ContentPart struct {
	// Title: text | description | parameters | arguments | result | items.
	Title string `json:"title"`
	// Format: markdown | json | text.
	Format string `json:"format"`
	Text   string `json:"text"`
}

// ItemContent is what GET …/context/item returns.
type ItemContent struct {
	Item
	Parts []ContentPart `json:"parts"`
	// Truncated: the content was cut at MaxItemContent.
	Truncated bool `json:"truncated,omitempty"`
}

// Item returns the content of one context item of agentID in sessionID
// ("" = current), redacted and capped.
func (s *Service) Item(agentID, sessionID, id string) (ItemContent, error) {
	_, items, err := s.usage(agentID, sessionID)
	if err != nil {
		return ItemContent{}, err
	}
	for _, it := range items {
		if it.ID != id {
			continue
		}
		out := ItemContent{Item: *it}
		if it.content != nil {
			out.Parts, out.Truncated = capParts(it.content(), MaxItemContent)
		}
		if out.Parts == nil {
			out.Parts = []ContentPart{}
		}
		return out, nil
	}
	return ItemContent{}, fmt.Errorf("context item %q not found", id)
}

// capParts redacts parts and cuts them to max bytes in all.
func capParts(parts []ContentPart, max int) ([]ContentPart, bool) {
	out := make([]ContentPart, 0, len(parts))
	left := max
	for _, p := range parts {
		p.Text = redact.Redact(p.Text)
		if len(p.Text) > left {
			cut := left
			for cut > 0 && !utf8.RuneStart(p.Text[cut]) {
				cut--
			}
			p.Text = p.Text[:cut]
			if p.Format == "json" {
				p.Format = "text" // no longer valid JSON
			}
			out = append(out, p)
			return out, true
		}
		left -= len(p.Text)
		out = append(out, p)
	}
	return out, false
}

func one(title, format, text string) func() []ContentPart {
	return func() []ContentPart { return []ContentPart{{Title: title, Format: format, Text: text}} }
}

func prettyJSON(v any) string {
	if raw, ok := v.(json.RawMessage); ok {
		var x any
		if json.Unmarshal(raw, &x) != nil {
			return string(raw)
		}
		v = x
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// preview is a one-line, at most n-rune excerpt of s.
func preview(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// toolGroup is the family a tool is listed under (MCP server, tool group).
func (s *Service) toolGroup(agentID string, t llm.ToolSchema) string {
	if t.Group != "" {
		return t.Group
	}
	if s.toolsR != nil {
		if rt, ok := s.toolsR.Get(agentID, t.Name); ok {
			return tools.GroupOf(rt)
		}
	}
	return tools.GroupOf(&tools.Tool{Name: t.Name})
}

func toolItem(t llm.ToolSchema, group string, tokens int) *Item {
	return &Item{ID: "tool:" + t.Name, Kind: SegTools, Label: t.Name, Group: group, Tokens: tokens, content: func() []ContentPart {
		ps := []ContentPart{{Title: "description", Format: "markdown", Text: t.Description}}
		if t.Parameters != nil {
			ps = append(ps, ContentPart{Title: "parameters", Format: "json", Text: prettyJSON(t.Parameters)})
		}
		return ps
	}}
}

// messageItems lists agentID's history as items, counted as countMessages
// counts it: one item per message's text parts, one per tool call with its
// result.
func messageItems(msgs []llm.Message) []*Item {
	var out []*Item
	calls := map[string]*Item{}
	for i, m := range msgs {
		var text *Item
		var texts []string
		for _, p := range m.Parts {
			n := llm.EstimateTokens(p.Text) + 4
			switch {
			case p.ToolCall != nil:
				tc := *p.ToolCall
				n += llm.EstimateTokens(string(tc.Args)) + 8
				it := &Item{ID: "call:" + tc.ID, Kind: SegToolIO, Label: tc.Name, Group: preview(string(tc.Args), 60), Tokens: n}
				calls[tc.ID] = it
				args := tc.Args
				it.content = func() []ContentPart {
					return []ContentPart{{Title: "arguments", Format: "json", Text: prettyJSON(args)}}
				}
				out = append(out, it)
			case p.ToolResult != nil:
				tr := *p.ToolResult
				n += llm.EstimateTokens(tr.Content) + 8
				it, ok := calls[tr.CallID]
				if !ok {
					name := cmp.Or(tr.Name, "tool")
					it = &Item{ID: "result:" + tr.CallID, Kind: SegToolIO, Label: name}
					out = append(out, it)
					it.content = func() []ContentPart { return nil }
				}
				it.Tokens += n
				prev := it.content
				it.content = func() []ContentPart {
					return append(prev(), ContentPart{Title: "result", Format: "text", Text: tr.Content})
				}
			default:
				if text == nil {
					text = &Item{ID: fmt.Sprintf("msg:%d", i), Kind: SegMessages, Group: string(m.Role)}
					out = append(out, text)
				}
				text.Tokens += n
				switch {
				case p.Attachment != nil:
					texts = append(texts, fmt.Sprintf("[%s: %s]", p.Attachment.Name, p.Attachment.Mime))
				case p.Type == "thinking":
					texts = append(texts, "> "+strings.ReplaceAll(p.Text, "\n", "\n> "))
				default:
					texts = append(texts, p.Text)
				}
			}
		}
		if text != nil {
			body := strings.Join(texts, "\n\n")
			text.Label = cmp.Or(preview(body, 60), string(m.Role))
			text.content = one("text", "markdown", body)
		}
	}
	return out
}

// BuiltinGroup groups a recorded CLI's own (non-MCP) tools.
const BuiltinGroup = "built-in"

// recordedGroup is the family of a tool in a recorded request: its Group,
// else the server of an MCP name ("mcp__<server>__<tool>" as coding CLIs
// name them, or "<server>__<tool>"), else the CLI's built-ins.
func recordedGroup(t llm.ToolSchema) string {
	if t.Group != "" {
		return t.Group
	}
	name := strings.TrimPrefix(t.Name, "mcp__")
	if i := strings.Index(name, "__"); i > 0 {
		return name[:i]
	}
	return BuiltinGroup
}

// recordedItems are the segments and items of a recorded request, counted
// as the loop's own requests are.
func recordedItems(rec *agentloop.RecordedRequest) (map[string]int, []*Item) {
	segs := map[string]int{}
	var items []*Item
	for i, b := range rec.System {
		n := llm.EstimateTokens(b.Text)
		label := b.Name
		if label == "" {
			label = fmt.Sprintf("system #%d", i+1)
		}
		segs[SegSystem] += n
		items = append(items, &Item{ID: fmt.Sprintf("sys:%d", i), Kind: SegSystem, Label: label, Tokens: n, content: one("text", "markdown", b.Text)})
	}
	for _, t := range rec.Tools {
		if t.Deferred {
			continue
		}
		raw, _ := json.Marshal(t)
		n := llm.EstimateTokens(string(raw))
		segs[SegTools] += n
		items = append(items, toolItem(t, recordedGroup(t), n))
	}
	countParts(segs, rec.Messages)
	items = append(items, messageItems(rec.Messages)...)
	return segs, items
}

// kindOrder is the display order of item kinds.
var kindOrder = map[string]int{SegSystem: 0, SegMemory: 1, SegSkills: 2, SegTools: 3, SegToolIO: 4, SegMessages: 5, SegCLI: 6, SegOther: 7}

// rollUp keeps the largest max items and folds the rest into one
// "more:<kind>" item per kind. The result is in kind order, largest first.
func rollUp(items []*Item, max int) []*Item {
	slices.SortStableFunc(items, func(a, b *Item) int { return b.Tokens - a.Tokens })
	if len(items) > max {
		kinds := map[string]bool{}
		keep := max
		for {
			kinds = map[string]bool{}
			for _, it := range items[keep:] {
				kinds[it.Kind] = true
			}
			if keep+len(kinds) <= max || keep == 0 {
				break
			}
			keep--
		}
		rest := items[keep:]
		items = slices.Clone(items[:keep])
		more := map[string]*Item{}
		var order []string
		for _, it := range rest {
			m, ok := more[it.Kind]
			if !ok {
				m = &Item{ID: "more:" + it.Kind, Kind: it.Kind}
				more[it.Kind] = m
				order = append(order, it.Kind)
			}
			m.Tokens += it.Tokens
			m.Count++
		}
		for _, k := range order {
			m := more[k]
			m.Label = fmt.Sprintf("%d more", m.Count)
			var lines []string
			for _, it := range rest {
				if it.Kind == k {
					lines = append(lines, fmt.Sprintf("- %s · %d", it.Label, it.Tokens))
				}
			}
			m.content = one("items", "markdown", strings.Join(lines, "\n"))
			items = append(items, m)
		}
	}
	slices.SortStableFunc(items, func(a, b *Item) int {
		if c := cmp.Compare(kindOrder[a.Kind], kindOrder[b.Kind]); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Count, b.Count); c != 0 {
			return c // roll-ups last
		}
		return b.Tokens - a.Tokens
	})
	return items
}
