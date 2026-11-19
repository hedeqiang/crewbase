// Package anthropic is the Anthropic Messages API provider, built on the
// official SDK (github.com/anthropics/anthropic-sdk-go/v2): streaming,
// tool_use / tool_result blocks, adaptive thinking with effort.
//
// Config (a single instance at top level, or a "providers" list):
//
//	{"providers": [{
//	    "id": "anthropic",                          // default "anthropic"
//	    "baseURL": "https://api.anthropic.com",     // optional (Anthropic-compatible endpoints work too)
//	    "apiKey": "", "apiKeyRef": "vault:crewbase/anthropic", "apiKeyEnv": "ANTHROPIC_API_KEY",
//	    "models": ["claude-opus-5-5", {"id": "claude-sonnet-5-5", "contextWindow": 200000}],
//	    "defaultModel": "claude-opus-5-5",
//	    "headers": {"anthropic-beta": "..."},
//	    "maxTokens": 32000,                         // output budget when the request has none
//	    "thinking": true,                           // adaptive thinking (default true)
//	    "promptCache": true,                        // cache breakpoints on tools, system and history (default true)
//	    "toolSearch": null                          // native tool search: null = by model, true / false to force
//	}]}
package anthropic

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/anthropics/anthropic-sdk-go/v2/packages/param"

	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/kernel"
)

// DefaultModel is used when a request names no model.
const DefaultModel = "claude-opus-5-5"

// DefaultModels is reported when no static list is configured and the
// Models API is unreachable.
var DefaultModels = []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5", "claude-fable-5-2"}

// ProviderConfig configures one Anthropic(-compatible) instance.
type ProviderConfig struct {
	ID           string            `json:"id"`
	BaseURL      string            `json:"baseURL,omitempty"`
	APIKey       string            `json:"apiKey,omitempty"`
	APIKeyRef    string            `json:"apiKeyRef,omitempty"`
	APIKeyEnv    string            `json:"apiKeyEnv,omitempty"`
	Models       []ModelSpec       `json:"models,omitempty"`
	DefaultModel string            `json:"defaultModel,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	MaxTokens    int               `json:"maxTokens,omitempty"`
	Thinking     *bool             `json:"thinking,omitempty"`
	PromptCache  *bool             `json:"promptCache,omitempty"`
	ToolSearch   *bool             `json:"toolSearch,omitempty"`
}

// Config is the plugin config.
type Config struct {
	ProviderConfig
	Providers []ProviderConfig `json:"providers"`
}

type modelMeta struct {
	maxTokens int
	adaptive  bool
	known     bool
}

// Adapter is one Anthropic provider instance.
type Adapter struct {
	id, baseURL, defModel string
	keys                  keySource
	headers               map[string]string
	static                []ModelSpec
	maxTokens             int
	thinking, cache       bool
	toolSearch            *bool
	httpClient            *http.Client

	mu       sync.Mutex
	cached   []llm.ModelInfo
	meta     map[string]modelMeta
	cachedAt time.Time
	cacheTTL time.Duration
}

// New builds an adapter; c (may be nil) is used for credential lookups.
func New(c *kernel.Context, pc ProviderConfig) *Adapter {
	if pc.ID == "" {
		pc.ID = "anthropic"
	}
	a := &Adapter{
		id: pc.ID, baseURL: trimSlash(pc.BaseURL), defModel: pc.DefaultModel, headers: pc.Headers, static: pc.Models,
		maxTokens: pc.MaxTokens, thinking: true, cache: true, toolSearch: pc.ToolSearch, httpClient: &http.Client{},
		cacheTTL: 5 * time.Minute, meta: map[string]modelMeta{},
	}
	if a.baseURL == "" {
		a.baseURL = "https://api.anthropic.com"
	}
	a.baseURL = strings.TrimSuffix(a.baseURL, "/v1")
	if a.defModel == "" {
		a.defModel = DefaultModel
	}
	if a.maxTokens <= 0 {
		a.maxTokens = 32000
	}
	if pc.Thinking != nil {
		a.thinking = *pc.Thinking
	}
	if pc.PromptCache != nil {
		a.cache = *pc.PromptCache
	}
	env := pc.APIKeyEnv
	if env == "" && pc.ID == "anthropic" {
		env = "ANTHROPIC_API_KEY"
	}
	a.keys = keySource{C: c, Literal: pc.APIKey, Ref: pc.APIKeyRef, Env: env}
	if pc.APIKey == "" && pc.APIKeyRef == "" {
		a.keys.DefRef = "vault:crewbase/" + pc.ID
	}
	return a
}

// SetHTTPClient replaces the HTTP client.
func (a *Adapter) SetHTTPClient(c *http.Client) { a.httpClient = c }

func (a *Adapter) ID() string   { return a.id }
func (a *Adapter) Kind() string { return "api" }

// client builds an SDK client with the key resolved now. Environment
// defaults (ANTHROPIC_* variables, profiles) are ignored on purpose: the
// adapter's own config and the credentials service are authoritative.
// Retries are off so the router can fail over immediately.
func (a *Adapter) client(ctx context.Context) (sdk.Client, string, error) {
	key, err := a.keys.Key(ctx, a.id)
	if err != nil {
		return sdk.Client{}, "", err
	}
	opts := []option.RequestOption{
		option.WithoutEnvironmentDefaults(),
		option.WithoutOpenTelemetry(),
		option.WithBaseURL(a.baseURL + "/"),
		option.WithHTTPClient(a.httpClient),
		option.WithMaxRetries(0),
	}
	if key != "" {
		opts = append(opts, option.WithAPIKey(key))
	}
	for k, v := range a.headers {
		opts = append(opts, option.WithHeader(k, v))
	}
	return sdk.NewClient(opts...), key, nil
}

// ---- models ----

// Models returns the static list, or the Models API (cached), or
// DefaultModels when the API is unreachable.
func (a *Adapter) Models(ctx context.Context) ([]llm.ModelInfo, error) {
	if len(a.static) > 0 {
		out := make([]llm.ModelInfo, 0, len(a.static))
		for _, m := range a.static {
			mi := m.Info(a.id, nil)
			if mi.Capabilities == nil {
				mi.Capabilities = []string{"tools", "vision", "reasoning", "code"}
			}
			out = append(out, mi)
		}
		return out, nil
	}
	a.mu.Lock()
	if a.cached != nil && time.Since(a.cachedAt) < a.cacheTTL {
		out := append([]llm.ModelInfo(nil), a.cached...)
		a.mu.Unlock()
		return out, nil
	}
	a.mu.Unlock()
	ms, meta, err := a.fetchModels(ctx)
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		if a.cached != nil {
			return append([]llm.ModelInfo(nil), a.cached...), nil
		}
		if llm.KindOf(err) == llm.ErrAuth {
			return nil, err
		}
		out := make([]llm.ModelInfo, 0, len(DefaultModels))
		for _, id := range DefaultModels {
			out = append(out, llm.ModelInfo{ID: id, Provider: a.id, Capabilities: []string{"tools", "vision", "reasoning", "code"}, Enabled: true})
		}
		return out, nil
	}
	a.cached, a.meta, a.cachedAt = ms, meta, time.Now()
	return append([]llm.ModelInfo(nil), ms...), nil
}

func (a *Adapter) fetchModels(ctx context.Context) ([]llm.ModelInfo, map[string]modelMeta, error) {
	cl, key, err := a.client(ctx)
	if err != nil {
		return nil, nil, err
	}
	var out []llm.ModelInfo
	meta := map[string]modelMeta{}
	it := cl.Models.ListAutoPaging(ctx, sdk.ModelListParams{Limit: param.NewOpt[int64](1000)})
	for it.Next() {
		m := it.Current()
		caps := []string{"tools", "code"}
		if m.Capabilities.ImageInput.Supported {
			caps = append(caps, "vision")
		}
		if m.Capabilities.Thinking.Supported {
			caps = append(caps, "reasoning")
		}
		if m.MaxInputTokens >= 500000 {
			caps = append(caps, "long-context")
		}
		out = append(out, llm.ModelInfo{ID: m.ID, Provider: a.id, Name: m.DisplayName, ContextWindow: int(m.MaxInputTokens), Capabilities: caps, Enabled: true})
		meta[m.ID] = modelMeta{maxTokens: int(m.MaxTokens), adaptive: m.Capabilities.Thinking.Types.Adaptive.Supported, known: true}
	}
	if err := it.Err(); err != nil {
		return nil, nil, a.classifyErr(ctx, err, key)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, meta, nil
}

func (a *Adapter) metaFor(model string) modelMeta {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.meta[model]
}

// ---- thinking signatures ----
//
// Anthropic requires thinking blocks to be sent back with their opaque
// signature (and redacted thinking with its encrypted data). Both travel
// in llm.Part.Meta, so they are recorded in the session log and survive
// restarts. Thinking parts without them (another provider's thinking, or
// plain text) are dropped.

const (
	// MetaSignature is the Part.Meta key of a thinking block's signature.
	MetaSignature = "anthropic:signature"
	// MetaRedacted is the Part.Meta key of a redacted thinking block's data.
	MetaRedacted = "anthropic:redacted"
)

// ---- request mapping ----

// mapMessages converts messages. Tool results become tool_result blocks at
// the front of a user turn; consecutive same-role turns are merged.
func (a *Adapter) mapMessages(msgs []llm.Message) []sdk.MessageParam {
	type turn struct {
		assistant bool
		content   []sdk.ContentBlockParamUnion
	}
	var turns []turn
	native := map[string]bool{} // toolset calls: their results echo the toolset
	for _, m := range msgs {
		for _, tc := range m.ToolCalls() {
			if c, ok := llm.DecodeComputerCall(tc); ok && c.Vendor == llm.ComputerAnthropic {
				native[tc.ID] = true
			}
		}
	}
	for _, m := range msgs {
		asst := m.Role == llm.RoleAssistant
		var results, blocks []sdk.ContentBlockParamUnion
		for _, p := range m.Parts {
			switch p.Type {
			case "thinking":
				if asst {
					if data := p.Meta[MetaRedacted]; data != "" {
						blocks = append(blocks, sdk.NewRedactedThinkingBlock(data))
					} else if sig := p.Meta[MetaSignature]; sig != "" {
						blocks = append(blocks, sdk.NewThinkingBlock(sig, p.Text))
					}
				}
			case "text":
				if p.Text != "" {
					blocks = append(blocks, sdk.NewTextBlock(p.Text))
				}
			case "tool_call":
				if p.ToolCall != nil && asst {
					if b, ok := nativeUse(p.ToolCall); ok {
						blocks = append(blocks, b)
						continue
					}
					blocks = append(blocks, sdk.NewToolUseBlock(p.ToolCall.ID, argsObject(p.ToolCall.Args), p.ToolCall.Name))
				}
			case "tool_result":
				if r := p.ToolResult; r != nil {
					if native[r.CallID] {
						results = append(results, nativeResult(r))
						continue
					}
					results = append(results, sdk.NewToolResultBlock(r.CallID, r.Content, r.IsError))
				}
			case llm.PartAttachment:
				if at := p.Attachment; at != nil && !asst {
					blocks = append(blocks, attachmentBlocks(at)...)
				}
			case llm.PartProviderBlock:
				if b, ok := providerBlock(p); ok && asst {
					blocks = append(blocks, b)
				}
			}
		}
		if len(results)+len(blocks) == 0 {
			continue
		}
		if n := len(turns); n > 0 && turns[n-1].assistant == asst {
			prev := turns[n-1].content
			if len(results) > 0 { // keep every tool_result ahead of text
				var pr, pt []sdk.ContentBlockParamUnion
				for _, b := range prev {
					if b.OfToolResult != nil {
						pr = append(pr, b)
					} else {
						pt = append(pt, b)
					}
				}
				prev = append(append(pr, results...), pt...)
			}
			turns[n-1].content = append(prev, blocks...)
			continue
		}
		turns = append(turns, turn{assistant: asst, content: append(results, blocks...)})
	}
	out := make([]sdk.MessageParam, 0, len(turns))
	for _, t := range turns {
		if t.assistant {
			out = append(out, sdk.NewAssistantMessage(t.content...))
		} else {
			out = append(out, sdk.NewUserMessage(t.content...))
		}
	}
	return out
}

// attachmentBlocks sends images and PDFs natively (when hydrated with
// Data), with a short text label naming the workspace path; anything else
// as its text form.
func attachmentBlocks(at *llm.Attachment) []sdk.ContentBlockParamUnion {
	label := sdk.NewTextBlock(at.Fallback())
	switch {
	case len(at.Data) > 0 && at.IsImage():
		return []sdk.ContentBlockParamUnion{label, sdk.NewImageBlockBase64(at.Mime, base64.StdEncoding.EncodeToString(at.Data))}
	case len(at.Data) > 0 && at.IsPDF():
		doc := sdk.NewDocumentBlock(sdk.Base64PDFSourceParam{Data: base64.StdEncoding.EncodeToString(at.Data)})
		doc.OfDocument.Title = param.NewOpt(at.Name)
		return []sdk.ContentBlockParamUnion{label, doc}
	}
	return []sdk.ContentBlockParamUnion{label}
}

// AcceptsFile: images for vision models, PDFs for Claude models (third-
// party Anthropic-compatible endpoints get the extracted text).
func (a *Adapter) AcceptsFile(model, mime string) bool {
	if model == "" {
		model = DefaultModel
	}
	claude := strings.HasPrefix(model, "claude")
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		ms, _ := a.Models(context.Background())
		if has, known := llm.ModelCap(ms, model, "vision"); known {
			return has
		}
		return claude
	case "application/pdf":
		return claude
	}
	return false
}

var efforts = map[string]sdk.OutputConfigEffort{
	"minimal": sdk.OutputConfigEffortLow,
	"low":     sdk.OutputConfigEffortLow,
	"medium":  sdk.OutputConfigEffortMedium,
	"high":    sdk.OutputConfigEffortHigh,
	"xhigh":   sdk.OutputConfigEffortXhigh,
	"max":     sdk.OutputConfigEffortMax,
}

func (a *Adapter) params(req llm.Request) sdk.MessageNewParams {
	model := req.Model
	if model == "" {
		model = a.defModel
	}
	meta := a.metaFor(model)
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = a.maxTokens
	}
	if meta.maxTokens > 0 && maxTokens > meta.maxTokens {
		maxTokens = meta.maxTokens
	}
	p := sdk.MessageNewParams{
		Model:     sdk.Model(model),
		MaxTokens: int64(maxTokens),
		Messages:  a.mapMessages(req.Messages),
	}
	if req.System != "" {
		sb := sdk.TextBlockParam{Text: req.System}
		if a.cache {
			sb.CacheControl = sdk.NewCacheControlEphemeralParam()
		}
		p.System = []sdk.TextBlockParam{sb}
	}
	searching := false
	for _, t := range req.Tools {
		if t.Deferred && !searching {
			searching = true
			p.Tools = append(p.Tools, toolSearchEntry())
		}
		schema := schemaOrEmpty(t.Parameters)
		is := sdk.ToolInputSchemaParam{Properties: schema["properties"], ExtraFields: map[string]any{}}
		if r, ok := schema["required"].([]string); ok {
			is.Required = r
		} else if r, ok := schema["required"].([]any); ok {
			for _, x := range r {
				if s, ok := x.(string); ok {
					is.Required = append(is.Required, s)
				}
			}
		}
		for k, v := range schema {
			if k != "type" && k != "properties" && k != "required" {
				is.ExtraFields[k] = v
			}
		}
		tp := sdk.ToolParam{Name: t.Name, InputSchema: is}
		if t.Description != "" {
			tp.Description = param.NewOpt(t.Description)
		}
		if t.Deferred {
			tp.DeferLoading = param.NewOpt(true)
		}
		p.Tools = append(p.Tools, sdk.ToolUnionParam{OfTool: &tp})
	}
	if a.cache {
		// system is one breakpoint; tools and history make three (≤ MaxCacheBreakpoints)
		placeToolBreakpoint(p.Tools)
		placeMessageBreakpoint(p.Messages)
	}
	if ct := a.computerTool(req, model); ct != nil {
		p.Tools = append(p.Tools, *ct)
	}
	adaptive := a.thinking && !strings.HasPrefix(model, "claude-3")
	if meta.known {
		adaptive = a.thinking && meta.adaptive
	}
	if adaptive {
		p.Thinking = sdk.ThinkingConfigParamUnion{OfAdaptive: &sdk.ThinkingConfigAdaptiveParam{
			Display: sdk.ThinkingConfigAdaptiveDisplaySummarized,
		}}
	}
	if e, ok := efforts[strings.ToLower(req.Effort)]; ok {
		p.OutputConfig = sdk.OutputConfigParam{Effort: e}
	}
	return p
}

// ---- streaming ----

// Stream performs a streaming Messages request.
func (a *Adapter) Stream(ctx context.Context, req llm.Request, onChunk func(llm.Chunk) error) (llm.Response, error) {
	cl, key, err := a.client(ctx)
	if err != nil {
		return llm.Response{}, err
	}
	stream := cl.Messages.NewStreaming(ctx, a.params(req))
	defer stream.Close()

	var acc sdk.Message
	stopped := false
	for stream.Next() {
		ev := stream.Current()
		if err := acc.Accumulate(ev); err != nil {
			return llm.Response{}, &llm.Error{Kind: llm.ErrUnavailable, Provider: a.id, Message: "malformed stream: " + sanitize(err.Error(), key)}
		}
		var ch *llm.Chunk
		switch ev.Type {
		case "content_block_delta":
			switch ev.Delta.Type {
			case "text_delta":
				ch = &llm.Chunk{Type: "text", Text: ev.Delta.Text}
			case "thinking_delta":
				ch = &llm.Chunk{Type: "thinking", Text: ev.Delta.Thinking}
			}
		case "content_block_stop":
			if i := int(ev.Index); i < len(acc.Content) && acc.Content[i].Type == "tool_use" {
				b := acc.Content[i]
				tc := llm.ToolCall{ID: b.ID, Name: b.Name, Args: normalizeArgs(string(b.Input))}
				if b.ToolsetName == toolsetName {
					tc = nativeCall(b.ID, b.Name, b.Input, acc.ID)
				}
				ch = &llm.Chunk{Type: "tool_call", ToolCall: &tc}
			}
		case "message_stop":
			stopped = true
		}
		if ch != nil && (ch.Text != "" || ch.ToolCall != nil) {
			if err := onChunk(*ch); err != nil {
				return llm.Response{}, err
			}
		}
	}
	if err := stream.Err(); err != nil {
		return llm.Response{}, a.classifyErr(ctx, err, key)
	}
	if !stopped && acc.StopReason == "" {
		if ctx.Err() != nil {
			return llm.Response{}, ctx.Err()
		}
		return llm.Response{}, &llm.Error{Kind: llm.ErrUnavailable, Provider: a.id, Message: "stream ended before completion"}
	}

	msg := llm.Message{Role: llm.RoleAssistant}
	queries := map[string]string{}
	for _, b := range acc.Content {
		switch b.Type {
		case "server_tool_use", "tool_search_tool_result":
			msg.Parts = append(msg.Parts, searchParts(b, queries)...)
		case "thinking":
			part := llm.Part{Type: "thinking", Text: b.Thinking}
			if b.Signature != "" {
				part.Meta = map[string]string{MetaSignature: b.Signature}
			}
			msg.Parts = append(msg.Parts, part)
		case "redacted_thinking":
			if b.Data != "" {
				msg.Parts = append(msg.Parts, llm.Part{Type: "thinking", Meta: map[string]string{MetaRedacted: b.Data}})
			}
		case "text":
			if b.Text != "" {
				msg.Parts = append(msg.Parts, llm.Part{Type: "text", Text: b.Text})
			}
		case "tool_use":
			tc := llm.ToolCall{ID: b.ID, Name: b.Name, Args: normalizeArgs(string(b.Input))}
			if b.ToolsetName == toolsetName {
				// several members per turn: one batch, run in order
				tc = nativeCall(b.ID, b.Name, b.Input, acc.ID)
			}
			msg.Parts = append(msg.Parts, llm.Part{Type: "tool_call", ToolCall: &tc})
		}
	}
	u := acc.Usage
	usage := llm.Usage{
		InputTokens: int(u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens), OutputTokens: int(u.OutputTokens),
		CacheRead: int(u.CacheReadInputTokens), CacheWrite: int(u.CacheCreationInputTokens),
	}
	stop := "end"
	switch acc.StopReason {
	case sdk.StopReasonToolUse:
		stop = "tool_use"
	case sdk.StopReasonMaxTokens, sdk.StopReasonModelContextWindowExceeded:
		stop = "max_tokens"
	}
	if len(msg.ToolCalls()) > 0 {
		stop = "tool_use"
	}
	return llm.Response{Message: msg, Usage: estimateUsage(usage, req, msg), StopReason: stop}, nil
}

// streamStatus maps Anthropic error types to HTTP statuses for errors that
// arrive inside a 200 stream.
var streamStatus = map[string]int{
	"rate_limit_error": 429, "overloaded_error": 529, "api_error": 500, "authentication_error": 401,
	"permission_error": 403, "request_too_large": 413, "invalid_request_error": 400, "not_found_error": 404,
	"billing_error": 402,
}

// classifyErr turns SDK errors into *llm.Error.
func (a *Adapter) classifyErr(ctx context.Context, err error, key string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var ae *sdk.Error
	if errors.As(err, &ae) {
		status := ae.StatusCode
		typ := string(ae.Type())
		if status/100 == 2 || status == 0 {
			status = streamStatus[typ]
		}
		msg := ae.Message
		if msg == "" {
			msg = extractMessage(ae.RawJSON())
		}
		var h http.Header
		if ae.Response != nil {
			h = ae.Response.Header
		}
		return classify(a.id, status, h, msg, typ, key)
	}
	var le *llm.Error
	if errors.As(err, &le) {
		return le
	}
	if strings.Contains(err.Error(), "received error while streaming") {
		msg, code, _ := parseErrorBody([]byte(strings.TrimSpace(strings.TrimPrefix(err.Error(), "received error while streaming:"))))
		return classify(a.id, 0, nil, msg, code, key)
	}
	return transportError(ctx, a.id, err, key)
}

func extractMessage(raw string) string {
	if raw == "" {
		return ""
	}
	msg, _, _ := parseErrorBody([]byte(raw))
	if json.Valid([]byte(msg)) {
		return ""
	}
	return msg
}

// Plugin registers the configured Anthropic instance(s).
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/llm-anthropic",
	Inject:      []string{llm.ServiceKey},
	Optional:    []string{CredentialsKey},
	Description: "Anthropic Messages API provider (official SDK).",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		list := cfg.Providers
		if len(list) == 0 {
			list = []ProviderConfig{cfg.ProviderConfig}
		}
		reg := kernel.Use[*llm.Registry](c, llm.ServiceKey)
		for _, pc := range list {
			if err := reg.Register(c, New(c, pc)); err != nil {
				return err
			}
		}
		return nil
	},
})
