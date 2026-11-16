// Package gemini is the Google Gemini provider, built on the official Google
// Gen AI Go SDK (google.golang.org/genai): streamGenerateContent with
// function calling and thinking.
//
// Config (a single instance at top level, or a "providers" list):
//
//	{"providers": [{
//	    "id": "gemini",                              // default "gemini"
//	    "baseURL": "",                               // default https://generativelanguage.googleapis.com/
//	    "apiVersion": "",                            // default v1beta (SDK default)
//	    "apiKey": "", "apiKeyRef": "keychain:crewbase/gemini", "apiKeyEnv": "GEMINI_API_KEY",
//	    "models": ["gemini-2.5-pro", {"id": "gemini-2.5-flash", "contextWindow": 1048576}],
//	    "defaultModel": "gemini-2.5-flash",
//	    "headers": {},
//	    "thinkingBudgets": {"low": 1024, "medium": 8192, "high": 24576}
//	}]}
package gemini

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/genai"

	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/kernel"
)

// ProviderConfig configures one Gemini instance.
type ProviderConfig struct {
	ID              string            `json:"id"`
	BaseURL         string            `json:"baseURL,omitempty"`
	APIVersion      string            `json:"apiVersion,omitempty"`
	APIKey          string            `json:"apiKey,omitempty"`
	APIKeyRef       string            `json:"apiKeyRef,omitempty"`
	APIKeyEnv       string            `json:"apiKeyEnv,omitempty"`
	Models          []ModelSpec       `json:"models,omitempty"`
	DefaultModel    string            `json:"defaultModel,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	ThinkingBudgets map[string]int    `json:"thinkingBudgets,omitempty"`
}

// Config is the plugin config.
type Config struct {
	ProviderConfig
	Providers []ProviderConfig `json:"providers"`
}

var defaultBudgets = map[string]int{"minimal": 512, "low": 1024, "medium": 8192, "high": 24576, "xhigh": 32768, "max": 32768}

// skipSignature is Google's documented placeholder for function calls whose
// thought signature is unknown (e.g. produced by another model).
const skipSignature = "skip_thought_signature_validator"

// Adapter is one Gemini provider instance.
type Adapter struct {
	id, baseURL, apiVersion, defModel string
	keys                              keySource
	headers                           map[string]string
	static                            []ModelSpec
	budgets                           map[string]int
	httpClient                        *http.Client
	pollEvery                         time.Duration // URI-delivered video files (default 5s)

	mu       sync.Mutex
	cached   []llm.ModelInfo
	cachedAt time.Time
	cacheTTL time.Duration
	seq      atomic.Int64
}

// New builds an adapter; c (may be nil) is used for credential lookups.
func New(c *kernel.Context, pc ProviderConfig) *Adapter {
	if pc.ID == "" {
		pc.ID = "gemini"
	}
	a := &Adapter{
		id: pc.ID, baseURL: strings.TrimSpace(pc.BaseURL), apiVersion: pc.APIVersion, defModel: pc.DefaultModel,
		headers: pc.Headers, static: pc.Models, budgets: map[string]int{}, httpClient: &http.Client{},
		cacheTTL: 5 * time.Minute,
	}
	if a.baseURL != "" && !strings.HasSuffix(a.baseURL, "/") {
		a.baseURL += "/"
	}
	for k, v := range defaultBudgets {
		a.budgets[k] = v
	}
	for k, v := range pc.ThinkingBudgets {
		a.budgets[strings.ToLower(k)] = v
	}
	env := pc.APIKeyEnv
	if env == "" && pc.ID == "gemini" {
		env = "GEMINI_API_KEY"
	}
	a.keys = keySource{C: c, Literal: pc.APIKey, Ref: pc.APIKeyRef, Env: env}
	if pc.APIKey == "" && pc.APIKeyRef == "" {
		a.keys.DefRef = "keychain:crewbase/" + pc.ID
	}
	return a
}

// SetHTTPClient replaces the HTTP client.
func (a *Adapter) SetHTTPClient(c *http.Client) { a.httpClient = c }

func (a *Adapter) ID() string   { return a.id }
func (a *Adapter) Kind() string { return "api" }

// client builds an SDK client with the key resolved now. The SDK's own error
// text (which can embed its config) is never surfaced.
func (a *Adapter) client(ctx context.Context) (*genai.Client, string, error) {
	key, err := a.keys.Key(ctx, a.id)
	if err != nil {
		return nil, "", err
	}
	if key == "" {
		return nil, "", &llm.Error{Kind: llm.ErrAuth, Provider: a.id, Message: "no API key configured"}
	}
	h := http.Header{}
	for k, v := range a.headers {
		h.Set(k, v)
	}
	cl, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:     key,
		Backend:    genai.BackendGeminiAPI,
		HTTPClient: a.httpClient,
		HTTPOptions: genai.HTTPOptions{
			BaseURL:    a.baseURL,
			APIVersion: a.apiVersion,
			Headers:    h,
		},
	})
	if err != nil {
		return nil, "", &llm.Error{Kind: llm.ErrBadRequest, Provider: a.id, Message: "cannot create client"}
	}
	return cl, key, nil
}

// ---- models ----

// Models returns the static list, or the models API (cached; stale on error).
func (a *Adapter) Models(ctx context.Context) ([]llm.ModelInfo, error) {
	if len(a.static) > 0 {
		out := make([]llm.ModelInfo, 0, len(a.static))
		for _, m := range a.static {
			mi := m.Info(a.id, nil)
			if mi.Capabilities == nil && mi.Kind == "" {
				mi.Capabilities = []string{"tools", "vision", "long-context"}
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
	ms, err := a.fetchModels(ctx)
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

func (a *Adapter) fetchModels(ctx context.Context) ([]llm.ModelInfo, error) {
	cl, key, err := a.client(ctx)
	if err != nil {
		return nil, err
	}
	var out []llm.ModelInfo
	for m, err := range cl.Models.All(ctx) {
		if err != nil {
			return nil, a.classifyErr(ctx, err, key)
		}
		id := strings.TrimPrefix(m.Name, "models/")
		if k := llm.ClassifyModel(id); k != llm.KindChat {
			// image / video models (generateContent, predict,
			// predictLongRunning or the Interactions API alike)
			out = append(out, mediaInfo(llm.ModelInfo{ID: id, Provider: a.id, Name: m.DisplayName, Kind: k, Enabled: true}))
			continue
		}
		gen := len(m.SupportedActions) == 0
		embed := false
		for _, act := range m.SupportedActions {
			switch act {
			case "generateContent", "streamGenerateContent":
				gen = true
			case "embedContent":
				embed = true
			}
		}
		if !gen && !embed {
			continue
		}
		var caps []string
		if gen {
			caps = append(caps, "tools", "vision")
		}
		if embed && !gen {
			caps = append(caps, "embedding")
		}
		if m.Thinking {
			caps = append(caps, "reasoning")
		}
		if m.InputTokenLimit >= 500000 {
			caps = append(caps, "long-context")
		}
		out = append(out, llm.ModelInfo{ID: id, Provider: a.id, Name: m.DisplayName,
			ContextWindow: int(m.InputTokenLimit), Capabilities: caps, Enabled: true})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ---- thought signatures ----
//
// Gemini's opaque thoughtSignature (required on function calls sent back
// in the current turn by Gemini 3) travels base64-encoded in
// llm.Part.Meta, so it is recorded in the session log and survives
// restarts. Signatures on text / thought parts are kept the same way.

// MetaThoughtSignature is the Part.Meta key of a thought signature.
const MetaThoughtSignature = "gemini:thoughtSignature"

func sigMeta(sig []byte) map[string]string {
	if len(sig) == 0 {
		return nil
	}
	return map[string]string{MetaThoughtSignature: base64.StdEncoding.EncodeToString(sig)}
}

func sigOf(p llm.Part) []byte {
	v := p.Meta[MetaThoughtSignature]
	if v == "" {
		return nil
	}
	b, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return nil
	}
	return b
}

const genIDPrefix = "gemini_call_"

// ---- request mapping ----

func resultResponse(r *llm.ToolResult) map[string]any {
	if r.IsError {
		return map[string]any{"error": r.Content}
	}
	var obj map[string]any
	if t := strings.TrimSpace(r.Content); strings.HasPrefix(t, "{") && json.Unmarshal([]byte(t), &obj) == nil {
		return obj
	}
	return map[string]any{"output": r.Content}
}

// AcceptsFile: Gemini reads images and PDFs inline (multimodal models).
func (a *Adapter) AcceptsFile(model, mime string) bool {
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf":
		ms, _ := a.Models(context.Background())
		if has, known := llm.ModelCap(ms, model, "vision"); known {
			return has
		}
		return strings.HasPrefix(model, "gemini")
	}
	return false
}

func (a *Adapter) mapContents(model string, msgs []llm.Message) []*genai.Content {
	names := toolNames(msgs)
	needSig := strings.Contains(model, "gemini-3")
	var out []*genai.Content
	for _, m := range msgs {
		role := genai.RoleUser
		if m.Role == llm.RoleAssistant {
			role = genai.RoleModel
		}
		var parts []*genai.Part
		for _, p := range m.Parts {
			switch p.Type {
			case "text":
				if p.Text != "" {
					gp := &genai.Part{Text: p.Text}
					if role == genai.RoleModel {
						gp.ThoughtSignature = sigOf(p)
					}
					parts = append(parts, gp)
				}
			case "thinking":
				// Only our own signed thoughts go back; other providers'
				// thinking and plain thinking text are skipped.
				if sig := sigOf(p); sig != nil && role == genai.RoleModel {
					parts = append(parts, &genai.Part{Text: p.Text, Thought: true, ThoughtSignature: sig})
				}
			case "tool_call":
				if tc := p.ToolCall; tc != nil && role == genai.RoleModel {
					var args map[string]any
					_ = json.Unmarshal(argsObject(tc.Args), &args)
					fc := &genai.FunctionCall{Name: tc.Name, Args: args}
					if !strings.HasPrefix(tc.ID, genIDPrefix) {
						fc.ID = tc.ID
					}
					part := &genai.Part{FunctionCall: fc}
					if sig := sigOf(p); sig != nil {
						part.ThoughtSignature = sig
					} else if needSig {
						part.ThoughtSignature = []byte(skipSignature)
					}
					parts = append(parts, part)
				}
			case "tool_result":
				if r := p.ToolResult; r != nil {
					name := r.Name
					if name == "" {
						name = names[r.CallID]
					}
					fr := &genai.FunctionResponse{Name: name, Response: resultResponse(r)}
					if !strings.HasPrefix(r.CallID, genIDPrefix) {
						fr.ID = r.CallID
					}
					parts = append(parts, &genai.Part{FunctionResponse: fr})
				}
			case llm.PartAttachment:
				if at := p.Attachment; at != nil && role == genai.RoleUser {
					parts = append(parts, &genai.Part{Text: at.Fallback()})
					if len(at.Data) > 0 && (at.IsImage() || at.IsPDF()) {
						parts = append(parts, &genai.Part{InlineData: &genai.Blob{MIMEType: at.Mime, Data: at.Data}})
					}
				}
			}
		}
		if len(parts) == 0 {
			continue
		}
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Parts = append(out[n-1].Parts, parts...)
			continue
		}
		out = append(out, &genai.Content{Role: role, Parts: parts})
	}
	return out
}

var levels = map[string]genai.ThinkingLevel{
	"minimal": genai.ThinkingLevelMinimal, "low": genai.ThinkingLevelLow, "medium": genai.ThinkingLevelMedium,
	"high": genai.ThinkingLevelHigh, "xhigh": genai.ThinkingLevelHigh, "max": genai.ThinkingLevelHigh,
}

func (a *Adapter) config(req llm.Request, model string) *genai.GenerateContentConfig {
	cfg := &genai.GenerateContentConfig{}
	if req.System != "" {
		cfg.SystemInstruction = &genai.Content{Parts: []*genai.Part{{Text: req.System}}}
	}
	if req.MaxTokens > 0 {
		cfg.MaxOutputTokens = int32(req.MaxTokens)
	}
	if len(req.Tools) > 0 {
		decls := make([]*genai.FunctionDeclaration, 0, len(req.Tools))
		for _, t := range req.Tools {
			decls = append(decls, &genai.FunctionDeclaration{Name: t.Name, Description: t.Description, ParametersJsonSchema: schemaOrEmpty(t.Parameters)})
		}
		cfg.Tools = []*genai.Tool{{FunctionDeclarations: decls}}
	}
	if e := strings.ToLower(req.Effort); e != "" {
		tc := &genai.ThinkingConfig{IncludeThoughts: true}
		switch {
		case e == "none" || e == "off":
			tc.IncludeThoughts = false
			zero := int32(0)
			tc.ThinkingBudget = &zero
		case strings.Contains(model, "gemini-3"):
			if l, ok := levels[e]; ok {
				tc.ThinkingLevel = l
			}
		default:
			if b, ok := a.budgets[e]; ok {
				v := int32(b)
				tc.ThinkingBudget = &v
			}
		}
		cfg.ThinkingConfig = tc
	}
	return cfg
}

// ---- streaming ----

var blockedReasons = map[genai.FinishReason]bool{
	genai.FinishReasonSafety: true, genai.FinishReasonRecitation: true, genai.FinishReasonBlocklist: true,
	genai.FinishReasonProhibitedContent: true, genai.FinishReasonSPII: true, genai.FinishReasonMalformedFunctionCall: true,
	genai.FinishReasonUnexpectedToolCall: true,
}

// Stream performs streamGenerateContent (SSE).
func (a *Adapter) Stream(ctx context.Context, req llm.Request, onChunk func(llm.Chunk) error) (llm.Response, error) {
	model := req.Model
	if model == "" {
		model = a.defModel
	}
	if model == "" {
		return llm.Response{}, &llm.Error{Kind: llm.ErrBadRequest, Provider: a.id, Message: "no model specified"}
	}
	cl, key, err := a.client(ctx)
	if err != nil {
		return llm.Response{}, err
	}
	var text, thinking strings.Builder
	var calls []*llm.ToolCall
	var callSigs [][]byte
	var textSig, thoughtSig []byte
	var usage llm.Usage
	var finish genai.FinishReason
	blockReason := ""
	for resp, err := range cl.Models.GenerateContentStream(ctx, model, a.mapContents(model, req.Messages), a.config(req, model)) {
		if err != nil {
			return llm.Response{}, a.classifyErr(ctx, err, key)
		}
		if u := resp.UsageMetadata; u != nil {
			usage = llm.Usage{InputTokens: int(u.PromptTokenCount), OutputTokens: int(u.CandidatesTokenCount + u.ThoughtsTokenCount), CacheRead: int(u.CachedContentTokenCount)}
		}
		if pf := resp.PromptFeedback; pf != nil && pf.BlockReason != "" {
			blockReason = string(pf.BlockReason)
		}
		if len(resp.Candidates) == 0 {
			continue
		}
		cand := resp.Candidates[0]
		if cand.FinishReason != "" {
			finish = cand.FinishReason
		}
		if cand.Content == nil {
			continue
		}
		for _, p := range cand.Content.Parts {
			if p.FunctionCall == nil && len(p.ThoughtSignature) > 0 {
				if p.Thought {
					thoughtSig = p.ThoughtSignature
				} else {
					textSig = p.ThoughtSignature
				}
			}
			switch {
			case p.FunctionCall != nil:
				fc := p.FunctionCall
				id := fc.ID
				if id == "" {
					id = fmt.Sprintf("%s%d_%d", genIDPrefix, time.Now().UnixNano(), a.seq.Add(1))
				}
				args := json.RawMessage("{}")
				if fc.Args != nil {
					if b, err := json.Marshal(fc.Args); err == nil {
						args = b
					}
				}
				tc := &llm.ToolCall{ID: id, Name: fc.Name, Args: args}
				calls = append(calls, tc)
				callSigs = append(callSigs, p.ThoughtSignature)
				if err := onChunk(llm.Chunk{Type: "tool_call", ToolCall: tc}); err != nil {
					return llm.Response{}, err
				}
			case p.Thought && p.Text != "":
				thinking.WriteString(p.Text)
				if err := onChunk(llm.Chunk{Type: "thinking", Text: p.Text}); err != nil {
					return llm.Response{}, err
				}
			case p.Text != "":
				text.WriteString(p.Text)
				if err := onChunk(llm.Chunk{Type: "text", Text: p.Text}); err != nil {
					return llm.Response{}, err
				}
			}
		}
	}
	if ctx.Err() != nil {
		return llm.Response{}, ctx.Err()
	}
	empty := text.Len() == 0 && len(calls) == 0
	if blockReason != "" && empty {
		return llm.Response{}, &llm.Error{Kind: llm.ErrBadRequest, Provider: a.id, Message: "prompt blocked: " + blockReason}
	}
	if blockedReasons[finish] && empty {
		return llm.Response{}, &llm.Error{Kind: llm.ErrBadRequest, Provider: a.id, Message: "response stopped: " + string(finish)}
	}
	if finish == "" {
		return llm.Response{}, &llm.Error{Kind: llm.ErrUnavailable, Provider: a.id, Message: "stream ended before completion"}
	}
	msg := llm.Message{Role: llm.RoleAssistant}
	if t := thinking.String(); t != "" {
		msg.Parts = append(msg.Parts, llm.Part{Type: "thinking", Text: t, Meta: sigMeta(thoughtSig)})
	}
	if t := text.String(); t != "" {
		msg.Parts = append(msg.Parts, llm.Part{Type: "text", Text: t, Meta: sigMeta(textSig)})
	}
	for i, tc := range calls {
		msg.Parts = append(msg.Parts, llm.Part{Type: "tool_call", ToolCall: tc, Meta: sigMeta(callSigs[i])})
	}
	stop := "end"
	switch {
	case len(calls) > 0:
		stop = "tool_use"
	case finish == genai.FinishReasonMaxTokens:
		stop = "max_tokens"
	}
	return llm.Response{Message: msg, Usage: estimateUsage(usage, req, msg), StopReason: stop}, nil
}

// classifyErr maps genai.APIError (HTTP status + google.rpc details) to
// *llm.Error. 429 RESOURCE_EXHAUSTED is a rate limit unless the violated
// quota is a daily one (or the message says billing), which is exhaustion.
func (a *Adapter) classifyErr(ctx context.Context, err error, key string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var ae genai.APIError
	if !errors.As(err, &ae) {
		var pae *genai.APIError
		if errors.As(err, &pae) && pae != nil {
			ae = *pae
		} else {
			var le *llm.Error
			if errors.As(err, &le) {
				return le
			}
			return transportError(ctx, a.id, err, key)
		}
	}
	var retry time.Duration
	daily := false
	for _, d := range ae.Details {
		typ, _ := d["@type"].(string)
		switch {
		case strings.HasSuffix(typ, "RetryInfo"):
			if s, ok := d["retryDelay"].(string); ok {
				retry, _ = time.ParseDuration(s)
			}
		case strings.HasSuffix(typ, "QuotaFailure"):
			if vs, ok := d["violations"].([]any); ok {
				for _, v := range vs {
					if vm, ok := v.(map[string]any); ok {
						if q, _ := vm["quotaId"].(string); strings.Contains(q, "PerDay") {
							daily = true
						}
					}
				}
			}
		case strings.HasSuffix(typ, "ErrorInfo"):
			if r, _ := d["reason"].(string); r == "API_KEY_INVALID" {
				ae.Status += " api_key_invalid"
			}
		}
	}
	code := ae.Code
	if code == 0 {
		code = statusFromRPC(ae.Status)
	}
	e := classify(a.id, code, nil, ae.Message, ae.Status, key)
	if code == http.StatusTooManyRequests {
		// Gemini words even per-minute limits as "exceeded your current quota",
		// so only daily quotas or billing problems count as exhaustion.
		switch {
		case daily:
			e.Kind = llm.ErrQuotaExhausted
		case retry == 0 && containsAny(strings.ToLower(ae.Message), "billing", "credit", "prepayment"):
			e.Kind = llm.ErrQuotaExhausted
		default:
			e.Kind = llm.ErrRateLimited
		}
	}
	if e.Kind == llm.ErrRateLimited && retry > 0 {
		e.RetryAfter = retry
		e.ResetsAt = time.Now().Add(retry)
	}
	return e
}

func statusFromRPC(s string) int {
	switch s {
	case "RESOURCE_EXHAUSTED":
		return 429
	case "UNAUTHENTICATED":
		return 401
	case "PERMISSION_DENIED":
		return 403
	case "INVALID_ARGUMENT", "FAILED_PRECONDITION", "NOT_FOUND":
		return 400
	case "UNAVAILABLE", "INTERNAL", "DEADLINE_EXCEEDED":
		return 503
	}
	return 0
}

// Plugin registers the configured Gemini instance(s).
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/llm-gemini",
	Inject:      []string{llm.ServiceKey},
	Optional:    []string{CredentialsKey},
	Description: "Google Gemini provider (official Gen AI SDK).",
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
