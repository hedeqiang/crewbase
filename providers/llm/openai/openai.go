// Package openai is the OpenAI-compatible Chat Completions provider, built
// on the official SDK (github.com/openai/openai-go/v3). One plugin instance
// registers several provider instances (OpenAI, DeepSeek, Moonshot, Qwen,
// xAI, OpenRouter, ...), each with its own base URL and key. xAI has no
// official Go SDK; its documented path is this OpenAI SDK pointed at
// https://api.x.ai/v1, which is what the "xai" preset does.
//
// Config:
//
//	{"providers": [{
//	    "id": "deepseek",                        // adapter id; also the preset name by default
//	    "preset": "deepseek",                    // optional, defaults to id
//	    "baseURL": "https://api.deepseek.com/v1", // optional when a preset exists
//	    "apiKey": "",                            // literal key (discouraged)
//	    "apiKeyRef": "keychain:crewbase/deepseek", // via the credentials service ("env:NAME" also works)
//	    "apiKeyEnv": "DEEPSEEK_API_KEY",         // optional, preset default
//	    "models": ["deepseek-chat", {"id": "deepseek-reasoner", "contextWindow": 128000, "capabilities": ["reasoning"]}],
//	    "headers": {"HTTP-Referer": "..."},
//	    "kind": "api",                           // api | local | subscription
//	    "effortStyle": "openai",                 // openai | openrouter | qwen | zhipu | none
//	    "maxTokensField": "max_tokens",          // or max_completion_tokens
//	    "streamUsage": true,                     // send stream_options.include_usage
//	    "shortToolIDs": false,                   // remap tool call ids to 9 alnum chars (Mistral)
//	    "defaultContextWindow": 0                // reported for fetched models of unknown window
//	}]}
package openai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	sdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/packages/respjson"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/shared"

	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/kernel"
)

// Preset holds defaults for a well-known OpenAI-compatible provider.
type Preset struct {
	BaseURL        string
	APIKeyEnv      string
	EffortStyle    string
	MaxTokensField string
	NoStreamUsage  bool
	ShortToolIDs   bool
	// EffortModels, when set, limits effort parameters to matching models
	// (unless a static model spec says otherwise).
	EffortModels *regexp.Regexp
}

// Presets maps preset id → defaults.
var Presets = map[string]Preset{
	"openai":      {BaseURL: "https://api.openai.com/v1", APIKeyEnv: "OPENAI_API_KEY", EffortStyle: "openai", MaxTokensField: "max_completion_tokens", EffortModels: regexp.MustCompile(`^(o\d|gpt-5)`)},
	"deepseek":    {BaseURL: "https://api.deepseek.com", APIKeyEnv: "DEEPSEEK_API_KEY", EffortStyle: "deepseek"},
	"moonshot":    {BaseURL: "https://api.moonshot.cn/v1", APIKeyEnv: "MOONSHOT_API_KEY", EffortStyle: "none"},
	"qwen":        {BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", APIKeyEnv: "DASHSCOPE_API_KEY", EffortStyle: "qwen", EffortModels: regexp.MustCompile(`qwen3|qwen-plus|qwen-turbo|qwen-flash|qwq`)},
	"xai":         {BaseURL: "https://api.x.ai/v1", APIKeyEnv: "XAI_API_KEY", EffortStyle: "openai", EffortModels: regexp.MustCompile(`^grok-3-mini`)},
	"openrouter":  {BaseURL: "https://openrouter.ai/api/v1", APIKeyEnv: "OPENROUTER_API_KEY", EffortStyle: "openrouter"},
	"siliconflow": {BaseURL: "https://api.siliconflow.cn/v1", APIKeyEnv: "SILICONFLOW_API_KEY", EffortStyle: "none"},
	"groq":        {BaseURL: "https://api.groq.com/openai/v1", APIKeyEnv: "GROQ_API_KEY", EffortStyle: "openai", EffortModels: regexp.MustCompile(`gpt-oss`)},
	"mistral":     {BaseURL: "https://api.mistral.ai/v1", APIKeyEnv: "MISTRAL_API_KEY", EffortStyle: "none", NoStreamUsage: true, ShortToolIDs: true},
	"zhipu":       {BaseURL: "https://open.bigmodel.cn/api/paas/v4", APIKeyEnv: "ZHIPU_API_KEY", EffortStyle: "zhipu", EffortModels: regexp.MustCompile(`^glm-(4\.[5-9]|[5-9])`)},
}

// ProviderConfig configures one provider instance.
type ProviderConfig struct {
	ID             string            `json:"id"`
	Preset         string            `json:"preset,omitempty"`
	BaseURL        string            `json:"baseURL,omitempty"`
	APIKey         string            `json:"apiKey,omitempty"`
	APIKeyRef      string            `json:"apiKeyRef,omitempty"`
	APIKeyEnv      string            `json:"apiKeyEnv,omitempty"`
	Models         []ModelSpec       `json:"models,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	Kind           string            `json:"kind,omitempty"`
	EffortStyle    string            `json:"effortStyle,omitempty"`
	MaxTokensField string            `json:"maxTokensField,omitempty"`
	StreamUsage    *bool             `json:"streamUsage,omitempty"`
	ShortToolIDs   *bool             `json:"shortToolIDs,omitempty"`
	// SafetyCheck enables DeepSeek's moderation ("high"); recommended for
	// anything public-facing. Flagged replies stop with llm.StopRefusal.
	SafetyCheck string `json:"safetyCheck,omitempty"`
	// DefaultContextWindow is reported for fetched models whose window is unknown.
	DefaultContextWindow int `json:"defaultContextWindow,omitempty"`
}

// Config is the plugin config.
type Config struct {
	Providers []ProviderConfig `json:"providers"`
}

// Adapter is one OpenAI-compatible provider instance.
type Adapter struct {
	id, kind     string
	baseURL      string
	keys         keySource
	headers      map[string]string
	static       []ModelSpec
	effortStyle  string
	effortModels *regexp.Regexp
	maxField     string
	streamUsage  bool
	shortIDs     bool
	defWindow    int
	httpClient   *http.Client

	mu       sync.Mutex
	safety   string
	cached   []llm.ModelInfo
	cachedAt time.Time
	cacheTTL time.Duration
}

// New builds an adapter. c is used to look up the credentials service at
// request time and may be nil.
func New(c *kernel.Context, pc ProviderConfig) (*Adapter, error) {
	if pc.ID == "" {
		return nil, fmt.Errorf("llm-openai: provider entry without id")
	}
	pname := pc.Preset
	if pname == "" {
		pname = pc.ID
	}
	pre, hasPreset := Presets[pname]
	if pc.Preset != "" && !hasPreset {
		return nil, fmt.Errorf("llm-openai: unknown preset %q", pc.Preset)
	}
	a := &Adapter{
		id: pc.ID, kind: pc.Kind, baseURL: trimSlash(pc.BaseURL), headers: pc.Headers, static: pc.Models,
		effortStyle: pc.EffortStyle, maxField: pc.MaxTokensField, streamUsage: true, defWindow: pc.DefaultContextWindow,
		httpClient: &http.Client{}, cacheTTL: 5 * time.Minute, safety: pc.SafetyCheck,
	}
	if a.kind == "" {
		a.kind = "api"
	}
	env := pc.APIKeyEnv
	if hasPreset {
		if a.baseURL == "" {
			a.baseURL = pre.BaseURL
		}
		if env == "" {
			env = pre.APIKeyEnv
		}
		if a.effortStyle == "" {
			a.effortStyle = pre.EffortStyle
		}
		if a.maxField == "" {
			a.maxField = pre.MaxTokensField
		}
		a.streamUsage = !pre.NoStreamUsage
		a.shortIDs = pre.ShortToolIDs
		a.effortModels = pre.EffortModels
	}
	if a.baseURL == "" {
		return nil, fmt.Errorf("llm-openai: provider %q needs a baseURL (no preset with that name)", pc.ID)
	}
	if a.effortStyle == "" {
		a.effortStyle = "none"
	}
	if a.maxField == "" {
		a.maxField = "max_tokens"
	}
	if pc.StreamUsage != nil {
		a.streamUsage = *pc.StreamUsage
	}
	if pc.ShortToolIDs != nil {
		a.shortIDs = *pc.ShortToolIDs
	}
	a.keys = keySource{c: c, literal: pc.APIKey, ref: pc.APIKeyRef, env: env}
	if pc.APIKey == "" && pc.APIKeyRef == "" && a.kind != "local" {
		a.keys.defRef = "keychain:crewbase/" + pc.ID
	}
	return a, nil
}

// SetHTTPClient replaces the HTTP client (tests, proxies).
func (a *Adapter) SetHTTPClient(c *http.Client) { a.httpClient = c }

// BaseURL returns the API base URL.
func (a *Adapter) BaseURL() string { return a.baseURL }

func (a *Adapter) ID() string   { return a.id }
func (a *Adapter) Kind() string { return a.kind }

// options returns SDK request options with the key resolved now. Services
// are built directly from these options (not sdk.NewClient) so ambient
// OPENAI_* environment variables never leak into other providers. Retries
// are off: the router fails over instead.
func (a *Adapter) options(ctx context.Context) ([]option.RequestOption, string, error) {
	key, err := a.keys.key(ctx, a.id)
	if err != nil {
		return nil, "", err
	}
	opts := []option.RequestOption{
		option.WithHTTPClient(a.httpClient),
		option.WithBaseURL(a.baseURL + "/"),
		option.WithMaxRetries(0),
		option.WithAPIKey(key),
	}
	for k, v := range a.headers {
		opts = append(opts, option.WithHeader(k, v))
	}
	return opts, key, nil
}

// ---- models ----

// Models returns the static list, or GET /models (cached; stale on error).
func (a *Adapter) Models(ctx context.Context) ([]llm.ModelInfo, error) {
	if len(a.static) > 0 {
		out := make([]llm.ModelInfo, 0, len(a.static))
		for _, m := range a.static {
			out = append(out, m.info(a.id, a.windowFor))
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
	ms, err := a.FetchModels(ctx)
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		if a.cached != nil {
			return append([]llm.ModelInfo(nil), a.cached...), nil
		}
		return nil, err
	}
	a.cached, a.cachedAt = ms, time.Now()
	return append([]llm.ModelInfo(nil), ms...), nil
}

// FetchModels always queries GET /models.
func (a *Adapter) FetchModels(ctx context.Context) ([]llm.ModelInfo, error) {
	opts, key, err := a.options(ctx)
	if err != nil {
		return nil, err
	}
	svc := sdk.NewModelService(opts...)
	it := svc.ListAutoPaging(ctx)
	var out []llm.ModelInfo
	for it.Next() {
		m := it.Current()
		w := 0
		for _, f := range []string{"context_length", "context_window", "max_model_len", "max_context_length"} {
			if fld, ok := m.JSON.ExtraFields[f]; ok {
				var n int
				if json.Unmarshal([]byte(fld.Raw()), &n) == nil && n > w {
					w = n
				}
			}
		}
		if w == 0 {
			w = a.windowFor(m.ID)
		}
		name := ""
		if fld, ok := m.JSON.ExtraFields["name"]; ok {
			_ = json.Unmarshal([]byte(fld.Raw()), &name)
		}
		out = append(out, llm.ModelInfo{ID: m.ID, Provider: a.id, Name: name, ContextWindow: w, Enabled: true})
	}
	if err := it.Err(); err != nil {
		return nil, a.classifyErr(ctx, err, key)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

var knownWindows = []struct {
	prefix string
	window int
}{
	{"gpt-4.1", 1047576}, {"gpt-5", 400000}, {"gpt-4o", 128000}, {"gpt-4-turbo", 128000}, {"o1", 200000},
	{"o3", 200000}, {"o4", 200000}, {"gpt-3.5", 16385}, {"deepseek", 128000}, {"kimi-k2", 262144},
	{"moonshot-v1-8k", 8192}, {"moonshot-v1-32k", 32768}, {"moonshot-v1-128k", 131072}, {"grok-4", 256000},
	{"grok-3", 131072}, {"qwen-max", 32768}, {"qwen-plus", 131072}, {"qwen3", 131072}, {"glm-4.5", 131072},
	{"glm-4.6", 204800}, {"mistral-large", 131072}, {"codestral", 256000},
}

func (a *Adapter) windowFor(id string) int {
	l := strings.ToLower(id)
	if i := strings.LastIndex(l, "/"); i >= 0 { // openrouter-style vendor/model
		l = l[i+1:]
	}
	for _, k := range knownWindows {
		if strings.HasPrefix(l, k.prefix) {
			return k.window
		}
	}
	return a.defWindow
}

// ---- request mapping ----

func (a *Adapter) toolID(id string) string {
	if !a.shortIDs {
		return id
	}
	ok := len(id) == 9
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			ok = false
		}
	}
	if ok {
		return id
	}
	h := sha256.Sum256([]byte(id))
	return hex.EncodeToString(h[:])[:9]
}

// mapMessages converts provider-neutral messages: tool results become role
// "tool" messages with tool_call_id; assistant tool calls become tool_calls.
// Thinking parts are not sent back (DeepSeek rejects reasoning_content).
func (a *Adapter) mapMessages(system string, msgs []llm.Message) []sdk.ChatCompletionMessageParamUnion {
	var out []sdk.ChatCompletionMessageParamUnion
	if system != "" {
		out = append(out, sdk.SystemMessage(system))
	}
	for _, m := range msgs {
		var text strings.Builder
		var calls []sdk.ChatCompletionMessageToolCallUnionParam
		var results []sdk.ChatCompletionMessageParamUnion
		for _, p := range m.Parts {
			switch p.Type {
			case "text":
				text.WriteString(p.Text)
			case "tool_call":
				if tc := p.ToolCall; tc != nil {
					calls = append(calls, sdk.ChatCompletionMessageToolCallUnionParam{OfFunction: &sdk.ChatCompletionMessageFunctionToolCallParam{
						ID:       a.toolID(tc.ID),
						Function: sdk.ChatCompletionMessageFunctionToolCallFunctionParam{Name: tc.Name, Arguments: string(argsObject(tc.Args))},
					}})
				}
			case "tool_result":
				if r := p.ToolResult; r != nil {
					c := r.Content
					if r.IsError {
						c = "[error] " + c
					}
					results = append(results, sdk.ToolMessage(c, a.toolID(r.CallID)))
				}
			}
		}
		if m.Role == llm.RoleAssistant {
			// Thinking parts (and their provider metadata) are not
			// understood here; a message holding nothing else is skipped.
			if text.Len() == 0 && len(calls) == 0 {
				continue
			}
			am := sdk.ChatCompletionAssistantMessageParam{ToolCalls: calls}
			if t := text.String(); t != "" || len(calls) == 0 {
				am.Content.OfString = param.NewOpt(t)
			}
			out = append(out, sdk.ChatCompletionMessageParamUnion{OfAssistant: &am})
			continue
		}
		out = append(out, results...)
		if t := text.String(); t != "" || (len(results) == 0 && m.Role == llm.RoleUser) {
			out = append(out, sdk.UserMessage(t))
		}
	}
	return out
}

func (a *Adapter) effortFor(model string) bool {
	if a.effortStyle == "none" {
		return false
	}
	if a.isDeepSeek(model) {
		return true
	}
	if s, ok := findSpec(a.static, model); ok && len(s.Capabilities) > 0 {
		return hasCap(s.Capabilities, "reasoning")
	}
	if a.effortModels != nil {
		m := strings.ToLower(model)
		if i := strings.LastIndex(m, "/"); i >= 0 {
			m = m[i+1:]
		}
		return a.effortModels.MatchString(m)
	}
	return true
}

var qwenBudgets = map[string]int{"low": 2048, "medium": 8192, "high": 32768}

// params builds the request params plus extra JSON fields for
// provider-specific knobs the SDK does not model.
func (a *Adapter) params(req llm.Request) (sdk.ChatCompletionNewParams, map[string]any) {
	p := sdk.ChatCompletionNewParams{
		Model:    shared.ChatModel(req.Model),
		Messages: a.mapMessages(req.System, req.Messages),
	}
	extra := map[string]any{}
	if a.streamUsage {
		p.StreamOptions = sdk.ChatCompletionStreamOptionsParam{IncludeUsage: param.NewOpt(true)}
	}
	if req.MaxTokens > 0 {
		if a.maxField == "max_completion_tokens" {
			p.MaxCompletionTokens = param.NewOpt(int64(req.MaxTokens))
		} else {
			p.MaxTokens = param.NewOpt(int64(req.MaxTokens))
		}
	}
	for _, t := range req.Tools {
		fd := shared.FunctionDefinitionParam{Name: t.Name, Parameters: shared.FunctionParameters(schemaOrEmpty(t.Parameters))}
		if t.Description != "" {
			fd.Description = param.NewOpt(t.Description)
		}
		p.Tools = append(p.Tools, sdk.ChatCompletionFunctionTool(fd))
	}
	if e := strings.ToLower(req.Effort); e != "" && a.effortFor(req.Model) {
		off := e == "none" || e == "off"
		switch a.effortStyle {
		case "openai":
			p.ReasoningEffort = shared.ReasoningEffort(e)
		case "openrouter":
			extra["reasoning"] = map[string]any{"effort": e}
		case "qwen":
			extra["enable_thinking"] = !off
			if b, ok := qwenBudgets[e]; ok {
				extra["thinking_budget"] = b
			}
		case "zhipu":
			t := "enabled"
			if off {
				t = "disabled"
			}
			extra["thinking"] = map[string]any{"type": t}
		case "deepseek", "auto":
			if a.isDeepSeek(req.Model) {
				t := "enabled"
				if off || e == "minimal" {
					t = "disabled"
				}
				extra["thinking"] = map[string]any{"type": t}
			} else if a.effortStyle == "auto" {
				p.ReasoningEffort = shared.ReasoningEffort(e)
			}
		}
	}
	if a.isDeepSeek(req.Model) {
		if tools := a.deepseekTools(req); tools != nil {
			p.Tools = nil
			extra["tools"] = tools
		}
		if a.safety != "" {
			extra["safety_check"] = map[string]any{"level": a.safety}
		}
	}
	return p, extra
}

// ---- streaming ----

// Stream performs a streaming chat completion. Text and reasoning deltas
// are forwarded as they arrive; tool calls are assembled by the SDK
// accumulator and emitted when the stream completes (JustFinishedToolCall
// is unreliable with parallel, interleaved tool-call deltas).
func (a *Adapter) Stream(ctx context.Context, req llm.Request, onChunk func(llm.Chunk) error) (llm.Response, error) {
	opts, key, err := a.options(ctx)
	if err != nil {
		return llm.Response{}, err
	}
	p, extra := a.params(req)
	svc := sdk.NewChatCompletionService(opts...)
	for k, v := range extra {
		opts = append(opts, option.WithJSONSet(k, v))
	}
	stream := svc.NewStreaming(ctx, p, opts...)
	defer stream.Close()

	var acc sdk.ChatCompletionAccumulator
	var thinking strings.Builder
	ds := newDSCollector()
	var serverUse map[string]int
	var usage llm.Usage
	finish := ""
	emitted := map[int]bool{}
	var calls []*llm.ToolCall
	emitCall := func(idx int, id, name, args string) error {
		if emitted[idx] || name == "" {
			return nil
		}
		emitted[idx] = true
		if id == "" {
			id = fmt.Sprintf("call_%d_%d", time.Now().UnixNano(), idx)
		}
		tc := &llm.ToolCall{ID: id, Name: name, Args: normalizeArgs(args)}
		calls = append(calls, tc)
		return onChunk(llm.Chunk{Type: "tool_call", ToolCall: tc})
	}
	for stream.Next() {
		ch := stream.Current()
		if !acc.AddChunk(ch) {
			return llm.Response{}, &llm.Error{Kind: llm.ErrUnavailable, Provider: a.id, Message: "malformed stream chunk"}
		}
		if ch.JSON.Usage.Valid() && (ch.Usage.PromptTokens > 0 || ch.Usage.CompletionTokens > 0) {
			d := ch.Usage.PromptTokensDetails // prompt_tokens already includes cached tokens
			usage = llm.Usage{InputTokens: int(ch.Usage.PromptTokens), OutputTokens: int(ch.Usage.CompletionTokens),
				CacheRead: int(d.CachedTokens), CacheWrite: int(d.CacheWriteTokens)}
			if f, ok := ch.Usage.JSON.ExtraFields["server_tool_use"]; ok {
				serverUse = serverToolUse(f.Raw())
			}
		}
		for _, c := range ch.Choices {
			if c.Index != 0 {
				continue
			}
			if r := extraString(c.Delta.JSON.ExtraFields, "reasoning_content") + extraString(c.Delta.JSON.ExtraFields, "reasoning"); r != "" {
				thinking.WriteString(r)
				if err := onChunk(llm.Chunk{Type: "thinking", Text: r}); err != nil {
					return llm.Response{}, err
				}
			}
			if f, ok := c.Delta.JSON.ExtraFields["server_tool_calls"]; ok {
				if err := ds.serverTools(f.Raw(), onChunk); err != nil {
					return llm.Response{}, err
				}
			}
			if f, ok := c.Delta.JSON.ExtraFields["annotations"]; ok {
				_ = ds.annotations(f.Raw(), onChunk)
			}
			if c.Delta.Content != "" {
				if err := onChunk(llm.Chunk{Type: "text", Text: c.Delta.Content}); err != nil {
					return llm.Response{}, err
				}
			}
			if c.FinishReason != "" {
				finish = c.FinishReason
			}
		}
	}
	if err := stream.Err(); err != nil {
		return llm.Response{}, a.classifyErr(ctx, err, key)
	}
	if finish == "" && len(acc.Choices) > 0 {
		finish = acc.Choices[0].FinishReason
	}
	if finish == "" {
		if ctx.Err() != nil {
			return llm.Response{}, ctx.Err()
		}
		return llm.Response{}, &llm.Error{Kind: llm.ErrUnavailable, Provider: a.id, Message: "stream ended before completion"}
	}

	msg := llm.Message{Role: llm.RoleAssistant}
	if t := ds.reasoning.String() + thinking.String(); t != "" {
		msg.Parts = append(msg.Parts, llm.Part{Type: "thinking", Text: t})
	}
	msg.Parts = append(msg.Parts, ds.tools...)
	if len(acc.Choices) > 0 {
		cm := acc.Choices[0].Message
		if cm.Content != "" {
			msg.Parts = append(msg.Parts, llm.Part{Type: "text", Text: cm.Content})
		}
		for i, tc := range cm.ToolCalls {
			if err := emitCall(i, tc.ID, tc.Function.Name, tc.Function.Arguments); err != nil {
				return llm.Response{}, err
			}
		}
	}
	for _, tc := range calls {
		msg.Parts = append(msg.Parts, llm.Part{Type: "tool_call", ToolCall: tc})
	}
	msg.Parts = append(msg.Parts, ds.citations...)
	stop := "end"
	switch {
	case finish == "refusal" || finish == "content_filter":
		stop = llm.StopRefusal
	case len(calls) > 0 || finish == "tool_calls" || finish == "function_call":
		stop = "tool_use"
	case finish == "length":
		stop = "max_tokens"
	}
	u := estimateUsage(usage, req, msg)
	u.ServerTools = serverUse
	return llm.Response{Message: msg, Usage: u, StopReason: stop}, nil
}

func extraString(fields map[string]respjson.Field, key string) string {
	f, ok := fields[key]
	if !ok {
		return ""
	}
	var s string
	_ = json.Unmarshal([]byte(f.Raw()), &s)
	return s
}

// classifyErr turns SDK errors into *llm.Error.
func (a *Adapter) classifyErr(ctx context.Context, err error, key string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var ae *sdk.Error
	if errors.As(err, &ae) {
		msg := ae.Message
		if msg == "" {
			msg, _, _ = parseErrorBody([]byte(ae.RawJSON()))
		}
		var h http.Header
		if ae.Response != nil {
			h = ae.Response.Header
		}
		return classify(a.id, ae.StatusCode, h, msg, ae.Code+" "+ae.Type, key)
	}
	var se *ssestream.StreamError
	if errors.As(err, &se) {
		msg, code, _ := parseErrorBody(se.Event.Data)
		return classify(a.id, 0, nil, msg, code, key)
	}
	var le *llm.Error
	if errors.As(err, &le) {
		return le
	}
	return transportError(ctx, a.id, err, key)
}

// Plugin registers every configured provider instance.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/llm-openai",
	Inject:      []string{llm.ServiceKey},
	Optional:    []string{CredentialsKey},
	Description: "OpenAI-compatible Chat Completions providers via the official openai-go SDK (OpenAI, DeepSeek, Moonshot, Qwen, xAI, OpenRouter, ...).",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		reg := kernel.Use[*llm.Registry](c, llm.ServiceKey)
		for _, pc := range cfg.Providers {
			a, err := New(c, pc)
			if err != nil {
				return err
			}
			if err := reg.Register(c, a); err != nil {
				return err
			}
		}
		return nil
	},
})
