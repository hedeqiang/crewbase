package anthropic

// Native tool search and prompt-cache breakpoints.
//
// Tool search (tool_search_tool_bm25_20251119, GA, no beta header): every
// tool is sent on every request; the ones the agent loop deferred
// (llm.ToolSchema.Deferred) carry defer_loading: true and stay out of the
// model's context until its search returns a tool_reference to them. The
// search runs on Anthropic's side: the response carries server_tool_use
// and tool_search_tool_result blocks that must be sent back unchanged, so
// they are recorded as llm.PartProviderBlock parts (the block's JSON in
// Meta[MetaBlock]), plus a llm.PartServerTool summary for people and the
// agent loop (which tools were loaded).
//
// Cache breakpoints (at most 4; prefix order tools → system → messages):
// the last non-deferred tool (deferred tools cannot carry cache_control
// and are not part of the cached prefix anyway), the system prompt, and
// the last block of the last message so a tool loop reuses its history.

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/packages/param"

	"github.com/hedeqiang/crewbase/core/llm"
)

// MetaBlock is the Part.Meta key of a provider block's JSON.
const MetaBlock = "anthropic:block"

// MaxCacheBreakpoints is the API's limit of cache_control blocks.
const MaxCacheBreakpoints = 4

var claudeVersion = regexp.MustCompile(`^claude-(opus|sonnet|haiku)-(\d+)(?:-(\d{1,2}))?(?:-|$)`)

// SearchesTools reports whether model takes the tool search tool: Claude
// Opus / Sonnet / Haiku 4.5 and later, and the Fable / Mythos families
// (platform docs, tool search model compatibility). The "toolSearch"
// provider option forces it on or off (e.g. for a compatible endpoint).
func (a *Adapter) SearchesTools(model string) bool {
	if a.toolSearch != nil {
		return *a.toolSearch
	}
	if model == "" {
		model = a.defModel
	}
	if strings.HasPrefix(model, "claude-fable") || strings.HasPrefix(model, "claude-mythos") {
		return true
	}
	m := claudeVersion.FindStringSubmatch(model)
	if m == nil {
		return false
	}
	major, _ := strconv.Atoi(m[2])
	minor := 0
	if m[3] != "" {
		minor, _ = strconv.Atoi(m[3])
	}
	return major > 4 || (major == 4 && minor >= 5)
}

// toolSearchEntry is the tools entry of the BM25 tool search tool.
func toolSearchEntry() sdk.ToolUnionParam {
	return sdk.ToolUnionParam{OfToolSearchToolBm25_20251119: &sdk.ToolSearchToolBm25_20251119Param{
		Type: sdk.ToolSearchToolBm25_20251119TypeToolSearchToolBm25_20251119,
	}}
}

// placeToolBreakpoint puts cache_control on the last non-deferred custom
// tool or the tool search tool (whichever comes last); it reports whether
// it placed one.
func placeToolBreakpoint(ts []sdk.ToolUnionParam) bool {
	for i := len(ts) - 1; i >= 0; i-- {
		switch t := ts[i]; {
		case t.OfTool != nil && !t.OfTool.DeferLoading.Value:
			t.OfTool.CacheControl = sdk.NewCacheControlEphemeralParam()
			return true
		case t.OfToolSearchToolBm25_20251119 != nil:
			t.OfToolSearchToolBm25_20251119.CacheControl = sdk.NewCacheControlEphemeralParam()
			return true
		}
	}
	return false
}

// placeMessageBreakpoint puts cache_control on the last block of the last
// message that can carry it; it reports whether it placed one.
func placeMessageBreakpoint(msgs []sdk.MessageParam) bool {
	if len(msgs) == 0 {
		return false
	}
	var bs []sdk.ContentBlockParamUnion
	switch last := msgs[len(msgs)-1]; {
	case last.OfUser != nil:
		bs = last.OfUser.Content
	case last.OfAssistant != nil:
		bs = last.OfAssistant.Content
	}
	for i := len(bs) - 1; i >= 0; i-- {
		cc := sdk.NewCacheControlEphemeralParam()
		switch b := bs[i]; {
		case b.OfText != nil:
			b.OfText.CacheControl = cc
		case b.OfToolResult != nil:
			b.OfToolResult.CacheControl = cc
		case b.OfImage != nil:
			b.OfImage.CacheControl = cc
		case b.OfDocument != nil:
			b.OfDocument.CacheControl = cc
		case b.OfToolUse != nil:
			b.OfToolUse.CacheControl = cc
		default:
			continue // thinking, provider blocks: not marked
		}
		return true
	}
	return false
}

// providerBlock replays a recorded server block as it came.
func providerBlock(p llm.Part) (sdk.ContentBlockParamUnion, bool) {
	raw := p.Meta[MetaBlock]
	if raw == "" || !json.Valid([]byte(raw)) {
		return sdk.ContentBlockParamUnion{}, false
	}
	return param.Override[sdk.ContentBlockParamUnion](json.RawMessage(raw)), true
}

// searchParts turns a server_tool_use / tool_search_tool_result block into
// parts: the block to replay, and for a result a summary naming the
// loaded tools.
func searchParts(b sdk.ContentBlockUnion, queries map[string]string) []llm.Part {
	raw, err := json.Marshal(b.ToParam())
	if err != nil {
		return nil
	}
	parts := []llm.Part{{Type: llm.PartProviderBlock, Meta: map[string]string{MetaBlock: string(raw)}}}
	switch b.Type {
	case "server_tool_use":
		var in struct {
			Query   string `json:"query"`
			Pattern string `json:"pattern"`
		}
		_ = json.Unmarshal(b.Input, &in)
		queries[b.ID] = in.Query + in.Pattern
	case "tool_search_tool_result":
		r := b.AsToolSearchToolResult()
		var names []string
		for _, ref := range r.Content.ToolReferences {
			names = append(names, ref.ToolName)
		}
		text := "搜索工具：" + queries[r.ToolUseID]
		if r.Content.ErrorCode != "" {
			text += "（失败：" + string(r.Content.ErrorCode) + "）"
		} else if len(names) > 0 {
			text += " → " + strings.Join(names, ", ")
		}
		parts = append(parts, llm.Part{Type: llm.PartServerTool, Text: text,
			Meta: map[string]string{"kind": llm.ServerToolSearch, "tools": strings.Join(names, ",")}})
	}
	return parts
}
