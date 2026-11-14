package openai

// DeepSeek dialect of Chat Completions (api-docs.deepseek.com, 2026-11):
//   - thinking: {"type": "enabled" | "disabled"} instead of reasoning_effort
//   - provider-hosted tools in `tools`: {"type": "deepseek_web_search"},
//     {"type": "deepseek_read_url"}
//   - safety_check: {"level": "high"}; a flagged reply stops with "refusal"
//   - stream deltas carry `annotations` (url_citation) and
//     `server_tool_calls` (what was searched / read); usage carries
//     `server_tool_use` (billed per call)

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/hedeqiang/crewbase/core/llm"
)

var deepseekModel = regexp.MustCompile(`(?i)(^|/)deepseek`)

// isDeepSeek reports whether model speaks the DeepSeek dialect on this
// adapter (the deepseek preset, or deepseek/* models behind a gateway).
func (a *Adapter) isDeepSeek(model string) bool {
	switch a.effortStyle {
	case "deepseek":
		return true
	case "auto":
		return deepseekModel.MatchString(model)
	}
	return false
}

var nativeTypes = map[string]string{
	llm.NativeWebSearch: "deepseek_web_search",
	llm.NativeReadURL:   "deepseek_read_url",
}

// deepseekTools returns the full tools array (function tools + hosted
// tools) when hosted tools are requested; nil otherwise.
func (a *Adapter) deepseekTools(req llm.Request) []any {
	if !a.isDeepSeek(req.Model) || len(req.NativeTools) == 0 {
		return nil
	}
	var out []any
	for _, t := range req.Tools {
		fn := map[string]any{"name": t.Name, "parameters": schemaOrEmpty(t.Parameters)}
		if t.Description != "" {
			fn["description"] = t.Description
		}
		out = append(out, map[string]any{"type": "function", "function": fn})
	}
	added := false
	for _, n := range req.NativeTools {
		if typ, ok := nativeTypes[n]; ok {
			out = append(out, map[string]any{"type": typ})
			added = true
		}
	}
	if !added {
		return nil
	}
	return out
}

type dsAnnotation struct {
	Type        string `json:"type"`
	URLCitation struct {
		URL   string `json:"url"`
		Title string `json:"title"`
	} `json:"url_citation"`
}

type dsServerToolCall struct {
	Reasoning string `json:"reasoning_content"`
	Calls     []struct {
		ID     string `json:"id"`
		Type   string `json:"type"`
		Status string `json:"status"`
		Action struct {
			Type    string   `json:"type"`
			Queries []string `json:"queries"`
			URL     string   `json:"url"`
		} `json:"action"`
	} `json:"calls"`
}

// dsCollector accumulates DeepSeek extras across stream chunks.
type dsCollector struct {
	citations []llm.Part
	seenURL   map[string]bool
	tools     []llm.Part
	seenCall  map[string]bool
	reasoning strings.Builder
}

func newDSCollector() *dsCollector {
	return &dsCollector{seenURL: map[string]bool{}, seenCall: map[string]bool{}}
}

func (d *dsCollector) annotations(raw string, emit func(llm.Chunk) error) error {
	var anns []dsAnnotation
	if json.Unmarshal([]byte(raw), &anns) != nil {
		return nil
	}
	for _, an := range anns {
		u := an.URLCitation.URL
		if an.Type != "url_citation" || u == "" || d.seenURL[u] {
			continue
		}
		d.seenURL[u] = true
		title := an.URLCitation.Title
		if title == "" {
			title = u
		}
		d.citations = append(d.citations, llm.Part{Type: llm.PartCitation, Text: title, Meta: map[string]string{"url": u}})
	}
	return nil
}

func (d *dsCollector) serverTools(raw string, emit func(llm.Chunk) error) error {
	var groups []dsServerToolCall
	if json.Unmarshal([]byte(raw), &groups) != nil {
		return nil
	}
	for _, g := range groups {
		if g.Reasoning != "" {
			d.reasoning.WriteString(g.Reasoning)
			if err := emit(llm.Chunk{Type: "thinking", Text: g.Reasoning}); err != nil {
				return err
			}
		}
		for _, c := range g.Calls {
			if c.ID == "" || d.seenCall[c.ID] {
				continue
			}
			var text string
			switch c.Action.Type {
			case "search":
				text = "已搜索：" + strings.Join(c.Action.Queries, "；")
			case "open_page":
				text = "已阅读：" + c.Action.URL
			default:
				continue
			}
			d.seenCall[c.ID] = true
			p := llm.Part{Type: llm.PartServerTool, Text: text, Meta: map[string]string{"kind": c.Action.Type, "tool": c.Type}}
			d.tools = append(d.tools, p)
			if err := emit(llm.Chunk{Type: llm.PartServerTool, Text: text}); err != nil {
				return err
			}
		}
	}
	return nil
}

// serverToolUse parses usage.server_tool_use.
func serverToolUse(raw string) map[string]int {
	var m map[string]int
	if json.Unmarshal([]byte(raw), &m) != nil {
		return nil
	}
	out := map[string]int{}
	for k, v := range m {
		if v > 0 {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
