package openai

// Shared provider plumbing: credential resolution, error classification and
// small mapping helpers. The same file is kept (only the package clause
// differs) in providers/llm/{openai,anthropic,gemini,local} so every
// provider classifies failures identically without depending on another
// provider package.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/kernel"
)

// CredentialsKey is the service key of the optional credentials service.
const CredentialsKey = "credentials"

// Resolver is the credentials service contract: it turns a reference such
// as "keychain:crewbase/deepseek" into a secret. The secret never leaves the
// adapter (it is not logged nor put into errors).
type Resolver interface {
	Resolve(ctx context.Context, ref string) (string, error)
}

// keySource resolves an API key at request time, in order: literal,
// explicit ref ("env:NAME" handled locally, anything else via the
// credentials service), environment variable, best-effort default ref.
type keySource struct {
	c       *kernel.Context
	literal string
	ref     string
	env     string
	defRef  string
}

func (k keySource) resolver() (Resolver, bool) {
	if k.c == nil {
		return nil, false
	}
	return kernel.Lookup[Resolver](k.c, CredentialsKey)
}

func (k keySource) key(ctx context.Context, provider string) (string, error) {
	if k.literal != "" {
		return k.literal, nil
	}
	if k.ref != "" {
		if name, ok := strings.CutPrefix(k.ref, "env:"); ok {
			if v := os.Getenv(name); v != "" {
				return v, nil
			}
			return "", &llm.Error{Kind: llm.ErrAuth, Provider: provider, Message: "apiKeyRef " + k.ref + " is not set"}
		}
		r, ok := k.resolver()
		if !ok {
			return "", &llm.Error{Kind: llm.ErrAuth, Provider: provider, Message: "credentials service unavailable; cannot resolve apiKeyRef " + k.ref}
		}
		v, err := r.Resolve(ctx, k.ref)
		if err != nil || v == "" {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", &llm.Error{Kind: llm.ErrAuth, Provider: provider, Message: "could not resolve apiKeyRef " + k.ref}
		}
		return v, nil
	}
	if k.env != "" {
		if v := os.Getenv(k.env); v != "" {
			return v, nil
		}
	}
	if k.defRef != "" {
		if r, ok := k.resolver(); ok {
			if v, err := r.Resolve(ctx, k.defRef); err == nil && v != "" {
				return v, nil
			}
		}
	}
	return "", nil
}

// ---- error classification ----

// apiErrorBody covers the common error envelopes:
// OpenAI {"error":{"message","type","code"}}, Anthropic
// {"type":"error","error":{"type","message"}}, Gemini
// {"error":{"code","message","status","details"}}, Ollama {"error":"..."}.
type apiErrorBody struct {
	Error   json.RawMessage `json:"error"`
	Message string          `json:"message"`
	Detail  any             `json:"detail"`
}

type apiErrorObj struct {
	Message string          `json:"message"`
	Type    string          `json:"type"`
	Code    any             `json:"code"`
	Status  string          `json:"status"`
	Details json.RawMessage `json:"details"`
}

// parseErrorBody extracts a human message and a machine code from body.
func parseErrorBody(body []byte) (msg, code string, details json.RawMessage) {
	body = []byte(strings.TrimSpace(string(body)))
	var env apiErrorBody
	if json.Unmarshal(body, &env) == nil {
		if len(env.Error) > 0 {
			var s string
			if json.Unmarshal(env.Error, &s) == nil {
				return s, "", nil
			}
			var o apiErrorObj
			if json.Unmarshal(env.Error, &o) == nil {
				codes := []string{}
				for _, c := range []string{o.Type, o.Status, fmt.Sprint(nilToEmpty(o.Code))} {
					if c != "" {
						codes = append(codes, c)
					}
				}
				return o.Message, strings.Join(codes, " "), o.Details
			}
		}
		if env.Message != "" {
			return env.Message, "", nil
		}
		if env.Detail != nil {
			b, _ := json.Marshal(env.Detail)
			return string(b), "", nil
		}
	}
	return string(body), "", nil
}

func nilToEmpty(v any) any {
	if v == nil {
		return ""
	}
	return v
}

// httpError reads resp (body limited) and returns a classified error.
func httpError(provider string, resp *http.Response, secret string) *llm.Error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	msg, code, _ := parseErrorBody(body)
	return classify(provider, resp.StatusCode, resp.Header, msg, code, secret)
}

func containsAny(s string, subs ...string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

var (
	quotaWords = []string{"insufficient_quota", "quota", "billing", "credit balance", "insufficient balance",
		"insufficient_balance", "exceeded your current", "payment required", "out of credits", "balance is not enough",
		"余额", "欠费", "额度"}
	rateWords = []string{"rate limit", "rate_limit", "ratelimit", "rate-limit", "too many requests", "per minute",
		"per min", "requests per", "tokens per", "rpm", "tpm", "concurrency", "slow down"}
	overflowWords = []string{"context_length_exceeded", "maximum context length", "context length", "context window",
		"context_window", "prompt is too long", "prompt too long", "input is too long", "input too long",
		"reduce the length", "exceeds the maximum number of tokens", "maximum input length", "too many input tokens",
		"input token count", "exceeds the context", "上下文"}
	authWords = []string{"invalid api key", "invalid_api_key", "api key not valid", "api_key_invalid", "incorrect api key",
		"invalid x-api-key", "authentication", "unauthorized", "permission denied"}
	unavailableWords = []string{"overloaded", "unavailable", "internal error", "internal server error", "server error",
		"timeout", "timed out", "try again later", "capacity"}
)

// classify maps an HTTP status (0 for in-stream errors) and provider message
// to an *llm.Error. secret is redacted from the message.
func classify(provider string, status int, h http.Header, msg, code, secret string) *llm.Error {
	lower := strings.ToLower(msg + " " + code)
	e := &llm.Error{Provider: provider, Message: sanitize(msg, secret)}
	if e.Message == "" {
		if status > 0 {
			e.Message = fmt.Sprintf("HTTP %d %s", status, http.StatusText(status))
		} else {
			e.Message = "stream error"
		}
	} else if status > 0 {
		e.Message = fmt.Sprintf("HTTP %d: %s", status, e.Message)
	}
	retry, resets := retryAfter(h)
	switch {
	case status == http.StatusPaymentRequired:
		e.Kind, e.ResetsAt = llm.ErrQuotaExhausted, resets
	case status == http.StatusTooManyRequests:
		switch {
		case strings.Contains(lower, "insufficient_quota"):
			e.Kind = llm.ErrQuotaExhausted
		case containsAny(lower, rateWords...):
			e.Kind = llm.ErrRateLimited
		case containsAny(lower, quotaWords...):
			e.Kind = llm.ErrQuotaExhausted
		default:
			e.Kind = llm.ErrRateLimited
		}
		if e.Kind == llm.ErrRateLimited {
			e.RetryAfter = retry
		}
		e.ResetsAt = resets
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		e.Kind = llm.ErrAuth
	case status == http.StatusRequestEntityTooLarge:
		e.Kind = llm.ErrContextOverflow
	case (status == 0 || (status >= 400 && status < 500)) && containsAny(lower, overflowWords...):
		e.Kind = llm.ErrContextOverflow
	case (status == 0 || (status >= 400 && status < 500)) && containsAny(lower, quotaWords...):
		e.Kind, e.ResetsAt = llm.ErrQuotaExhausted, resets
	case (status == 0 || (status >= 400 && status < 500)) && containsAny(lower, authWords...):
		e.Kind = llm.ErrAuth
	case status == 0 && containsAny(lower, rateWords...):
		e.Kind, e.RetryAfter = llm.ErrRateLimited, retry
	case status == http.StatusNotFound,
		status >= 400 && status < 500 && containsAny(lower, "model_not_found", "model not found", "no such model", "unknown model", "knows no model", "does not exist"):
		e.Kind = llm.ErrModelNotFound
	case status >= 500, status == http.StatusRequestTimeout:
		e.Kind, e.RetryAfter = llm.ErrUnavailable, retry
	case status >= 400:
		e.Kind = llm.ErrBadRequest
	case containsAny(lower, unavailableWords...):
		e.Kind = llm.ErrUnavailable
	default:
		e.Kind = llm.ErrUnavailable
	}
	return e
}

// retryAfter parses Retry-After (seconds or HTTP date), retry-after-ms and
// the OpenAI / Anthropic rate-limit reset headers.
func retryAfter(h http.Header) (time.Duration, time.Time) {
	if h == nil {
		return 0, time.Time{}
	}
	var d time.Duration
	if v := h.Get("Retry-After-Ms"); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil && ms > 0 {
			d = time.Duration(ms * float64(time.Millisecond))
		}
	}
	if v := strings.TrimSpace(h.Get("Retry-After")); d == 0 && v != "" {
		if s, err := strconv.ParseFloat(v, 64); err == nil && s >= 0 {
			d = time.Duration(s * float64(time.Second))
		} else if t, err := http.ParseTime(v); err == nil {
			d = time.Until(t)
		}
	}
	if d == 0 {
		for _, k := range []string{"X-Ratelimit-Reset-Requests", "X-Ratelimit-Reset-Tokens"} {
			if v := h.Get(k); v != "" {
				if x, err := time.ParseDuration(v); err == nil && x > d {
					d = x
				}
			}
		}
	}
	var resets time.Time
	for _, k := range []string{"Anthropic-Ratelimit-Requests-Reset", "Anthropic-Ratelimit-Tokens-Reset", "Anthropic-Ratelimit-Unified-Reset"} {
		if v := h.Get(k); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil && t.After(resets) {
				resets = t
			}
		}
	}
	if d < 0 {
		d = 0
	}
	if resets.IsZero() && d > 0 {
		resets = time.Now().Add(d)
	}
	return d, resets
}

var secretPatterns = regexp.MustCompile(`(sk-[A-Za-z0-9_\-]{12,}|AIza[0-9A-Za-z_\-]{20,}|Bearer\s+[A-Za-z0-9._\-]{12,})`)

// sanitize removes secrets from provider messages and bounds their length.
func sanitize(msg, secret string) string {
	msg = strings.TrimSpace(msg)
	if secret != "" {
		msg = strings.ReplaceAll(msg, secret, "[redacted]")
	}
	msg = secretPatterns.ReplaceAllString(msg, "[redacted]")
	if r := []rune(msg); len(r) > 500 {
		msg = string(r[:500]) + "…"
	}
	return msg
}

// transportError classifies a failure to reach the server. Context
// cancellation is returned as-is so the caller can tell it apart.
func transportError(ctx context.Context, provider string, err error, secret string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &llm.Error{Kind: llm.ErrUnavailable, Provider: provider, Message: sanitize("request failed: "+err.Error(), secret)}
}

// streamReadError classifies an error that happened while reading a body.
func streamReadError(ctx context.Context, provider string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return &llm.Error{Kind: llm.ErrUnavailable, Provider: provider, Message: "stream interrupted: " + sanitize(err.Error(), "")}
}

// ---- misc ----

// normalizeArgs makes tool-call arguments valid JSON: empty → {}, invalid
// text → a JSON string holding it (the tool layer then reports bad args).
func normalizeArgs(s string) json.RawMessage {
	s = strings.TrimSpace(s)
	if s == "" {
		return json.RawMessage("{}")
	}
	if json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	b, _ := json.Marshal(s)
	return json.RawMessage(b)
}

// argsObject returns tool-call args as a JSON object value (providers that
// want an object rather than a string).
func argsObject(raw json.RawMessage) json.RawMessage {
	t := strings.TrimSpace(string(raw))
	if t == "" || t == "null" || !json.Valid([]byte(t)) || !strings.HasPrefix(t, "{") {
		return json.RawMessage("{}")
	}
	return json.RawMessage(t)
}

// toolNames maps tool-call ids to names across a conversation so tool
// results without a Name can be labelled (Gemini, Ollama need names).
func toolNames(msgs []llm.Message) map[string]string {
	out := map[string]string{}
	for _, m := range msgs {
		for _, p := range m.Parts {
			if p.ToolCall != nil {
				out[p.ToolCall.ID] = p.ToolCall.Name
			}
		}
	}
	return out
}

func schemaOrEmpty(p map[string]any) map[string]any {
	if p == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return p
}

func trimSlash(s string) string { return strings.TrimRight(strings.TrimSpace(s), "/") }

// estimateUsage fills usage from heuristics when the server sent none.
func estimateUsage(u llm.Usage, req llm.Request, msg llm.Message) llm.Usage {
	if u.InputTokens == 0 && u.OutputTokens == 0 {
		u.InputTokens = llm.EstimateRequest(req)
		n := 0
		for _, p := range msg.Parts {
			n += llm.EstimateTokens(p.Text)
			if p.ToolCall != nil {
				n += llm.EstimateTokens(string(p.ToolCall.Args)) + 8
			}
		}
		u.OutputTokens = n
	}
	return u
}

// ModelSpec is a statically configured model. In JSON it may be a bare
// string (the id) or an object.
type ModelSpec struct {
	ID            string   `json:"id"`
	Name          string   `json:"name,omitempty"`
	ContextWindow int      `json:"contextWindow,omitempty"`
	Capabilities  []string `json:"capabilities,omitempty"`
}

func (m *ModelSpec) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*m = ModelSpec{ID: s}
		return nil
	}
	type plain ModelSpec
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*m = ModelSpec(p)
	return nil
}

func (m ModelSpec) info(provider string, defWindow func(string) int) llm.ModelInfo {
	w := m.ContextWindow
	if w == 0 && defWindow != nil {
		w = defWindow(m.ID)
	}
	return llm.ModelInfo{ID: m.ID, Provider: provider, Name: m.Name, ContextWindow: w, Capabilities: m.Capabilities, Enabled: true}
}

func findSpec(specs []ModelSpec, id string) (ModelSpec, bool) {
	for _, s := range specs {
		if s.ID == id {
			return s, true
		}
	}
	return ModelSpec{}, false
}

func hasCap(caps []string, c string) bool {
	for _, x := range caps {
		if x == c {
			return true
		}
	}
	return false
}
