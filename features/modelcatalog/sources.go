package modelcatalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Entry is what one source says about one model. Prices are USD per
// million tokens (Image: per generated image). Short JSON keys keep the
// embedded snapshot and the cache small.
type Entry struct {
	Context    int     `json:"ctx,omitempty"`
	MaxOutput  int     `json:"max,omitempty"`
	Input      float64 `json:"in,omitempty"`
	Output     float64 `json:"out,omitempty"`
	CacheRead  float64 `json:"cr,omitempty"`
	CacheWrite float64 `json:"cw,omitempty"`
	Image      float64 `json:"img,omitempty"`
	// Kind is "" for chat, image or video.
	Kind string `json:"k,omitempty"`
}

func (e Entry) priced() bool { return e.Input > 0 || e.Output > 0 || e.Image > 0 }

// Source formats.
const (
	FormatOpenRouter = "openrouter"
	FormatLiteLLM    = "litellm"
)

// Normalize maps a model id as providers and gateways spell it to the
// key both sources are indexed by: lower case; a gateway or vendor path
// dropped ("magpie/claude/claude-opus-5-5", "anthropic/claude-opus-5.5",
// "openrouter/x-ai/grok-4" keep only the last segment); a variant suffix
// dropped ("@500k" context variants behind gateways, Vertex "@20250514"
// dates, OpenRouter ":free" / ":batch", Bedrock ":0"); dots and
// underscores as dashes, so "claude-opus-5.5" (OpenRouter) and
// "claude-opus-5-5" (Anthropic) meet.
func Normalize(id string) string {
	s := strings.ToLower(strings.TrimSpace(id))
	if i := strings.IndexByte(s, '@'); i > 0 {
		s = s[:i]
	}
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.IndexByte(s, ':'); i > 0 {
		s = s[:i]
	}
	return strings.NewReplacer(".", "-", "_", "-").Replace(s)
}

var (
	// a release date: -20250514, -2025-05-14
	dateRe = regexp.MustCompile(`-(20\d{6}|20\d{2}-\d{2}-\d{2})$`)
	// a short snapshot tag: -0613, -2411, -1017
	tagRe = regexp.MustCompile(`-\d{4}$`)
)

// candidates are the keys tried for id, most specific first: the
// normalised id, then without a release date / snapshot tag, then
// without "-latest". A dated id finds the undated entry, never a
// different model.
func candidates(id string) []string {
	k := Normalize(id)
	if k == "" {
		return nil
	}
	out := []string{k}
	add := func(s string) {
		if s != "" && s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	add(dateRe.ReplaceAllString(k, ""))
	add(tagRe.ReplaceAllString(out[len(out)-1], ""))
	add(strings.TrimSuffix(out[len(out)-1], "-latest"))
	return out
}

// addAliases lets an undated id find a dated-only entry
// ("claude-sonnet-4-5" → "claude-sonnet-4-5-20250929") when the source
// has no undated one; the newest date wins.
func addAliases(m map[string]Entry) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys))) // newest date first
	for _, k := range keys {
		base := dateRe.ReplaceAllString(k, "")
		if base == k {
			continue
		}
		if _, ok := m[base]; !ok {
			m[base] = m[k]
		}
	}
}

// Parse detects the format of a source document and parses it.
func Parse(b []byte) (string, map[string]Entry, error) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || b[0] != '{' {
		return "", nil, errors.New("not a JSON object")
	}
	var probe struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(b, &probe) == nil && len(probe.Data) > 0 && probe.Data[0] == '[' {
		m, err := ParseOpenRouter(b)
		return FormatOpenRouter, m, err
	}
	m, err := ParseLiteLLM(b)
	return FormatLiteLLM, m, err
}

// num accepts a JSON number or a numeric string; anything else is 0
// (LiteLLM's sample_spec has prose in number fields).
type num float64

func (n *num) UnmarshalJSON(b []byte) error {
	var f float64
	if json.Unmarshal(b, &f) == nil {
		*n = num(f)
		return nil
	}
	var s string
	if json.Unmarshal(b, &s) == nil {
		if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
			*n = num(f)
		}
	}
	return nil
}

// perM turns a USD-per-token price into USD per million tokens, rounded
// to 6 significant digits; negative (OpenRouter's "-1" = variable) is 0.
func perM(v num) float64 {
	f := float64(v) * 1e6
	if f <= 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return round6(f)
}

func round6(f float64) float64 {
	if f == 0 {
		return 0
	}
	p := math.Pow(10, 5-math.Floor(math.Log10(math.Abs(f))))
	return math.Round(f*p) / p
}

// ParseOpenRouter reads GET https://openrouter.ai/api/v1/models:
// {"data":[{"id":"anthropic/claude-opus-5.5","context_length":…,
// "pricing":{"prompt":"0.000004","completion":…,"input_cache_read":…,
// "input_cache_write":…} (USD per token, strings),
// "top_provider":{"context_length":…,"max_completion_tokens":…},
// "architecture":{"output_modalities":["text"]}}]}.
// Variant ids ("…:free", "…:batch") are skipped: their prices are not
// the model's list price.
func ParseOpenRouter(b []byte) (map[string]Entry, error) {
	var doc struct {
		Data []struct {
			ID            string `json:"id"`
			ContextLength num    `json:"context_length"`
			Pricing       struct {
				Prompt     num `json:"prompt"`
				Completion num `json:"completion"`
				CacheRead  num `json:"input_cache_read"`
				CacheWrite num `json:"input_cache_write"`
			} `json:"pricing"`
			TopProvider struct {
				ContextLength num `json:"context_length"`
				MaxCompletion num `json:"max_completion_tokens"`
			} `json:"top_provider"`
			Architecture struct {
				Output []string `json:"output_modalities"`
			} `json:"architecture"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("openrouter: %w", err)
	}
	if len(doc.Data) == 0 {
		return nil, errors.New("openrouter: no models")
	}
	out := map[string]Entry{}
	for _, m := range doc.Data {
		if m.ID == "" || strings.Contains(m.ID, ":") {
			continue
		}
		k := Normalize(m.ID)
		if _, dup := out[k]; dup || k == "" {
			continue // the first vendor listed keeps the name
		}
		e := Entry{
			Context: int(m.ContextLength), MaxOutput: int(m.TopProvider.MaxCompletion),
			Input: perM(m.Pricing.Prompt), Output: perM(m.Pricing.Completion),
			CacheRead: perM(m.Pricing.CacheRead), CacheWrite: perM(m.Pricing.CacheWrite),
		}
		if e.Context == 0 {
			e.Context = int(m.TopProvider.ContextLength)
		}
		for _, o := range m.Architecture.Output {
			switch o {
			case "video":
				e.Kind = "video"
			case "image":
				if e.Kind == "" {
					e.Kind = "image"
				}
			}
		}
		out[k] = e
	}
	addAliases(out)
	return out, nil
}

// litellmVendors are the first-party providers kept from LiteLLM: their
// entries carry the vendor's own list price under the ids the vendor's
// API uses. Resellers and clouds (azure, bedrock, vertex partner models,
// fireworks, together, …) list the same ids at other prices and context
// limits, so they are left out.
var litellmVendors = map[string]bool{
	"openai": true, "text-completion-openai": true, "chatgpt": false,
	"anthropic": true, "gemini": true, "vertex_ai-language-models": true,
	"deepseek": true, "xai": true, "mistral": true, "codestral": true,
	"moonshot": true, "dashscope": true, "zai": true, "volcengine": true,
	"minimax": true, "cohere_chat": true, "groq": false,
}

// ParseLiteLLM reads LiteLLM's model_prices_and_context_window.json:
// {"<id>":{"litellm_provider":"anthropic","mode":"chat",
// "max_input_tokens":…,"max_output_tokens":…,"input_cost_per_token":…,
// "output_cost_per_token":…,"cache_read_input_token_cost":…,
// "cache_creation_input_token_cost":…,"output_cost_per_image":…}}.
// Only first-party vendors (litellmVendors) and chat / image / video
// modes are kept; a bare id ("claude-opus-5-5") beats a prefixed one
// ("anthropic/claude-opus-5-5") for the same key.
func ParseLiteLLM(b []byte) (map[string]Entry, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("litellm: %w", err)
	}
	type raw struct {
		Provider   string `json:"litellm_provider"`
		Mode       string `json:"mode"`
		MaxInput   num    `json:"max_input_tokens"`
		MaxTokens  num    `json:"max_tokens"`
		MaxOutput  num    `json:"max_output_tokens"`
		Input      num    `json:"input_cost_per_token"`
		Output     num    `json:"output_cost_per_token"`
		CacheRead  num    `json:"cache_read_input_token_cost"`
		CacheWrite num    `json:"cache_creation_input_token_cost"`
		Image      num    `json:"output_cost_per_image"`
	}
	ids := make([]string, 0, len(doc))
	for id := range doc {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := map[string]Entry{}
	rank := map[string]int{}
	for _, id := range ids {
		var r raw
		if json.Unmarshal(doc[id], &r) != nil || !litellmVendors[r.Provider] {
			continue
		}
		kind := ""
		switch r.Mode {
		case "chat", "responses", "completion":
		case "image_generation", "image_edit":
			kind = "image"
		case "video_generation":
			kind = "video"
		default:
			continue
		}
		k := Normalize(id)
		if k == "" {
			continue
		}
		rk := strings.Count(id, "/")
		if prev, ok := rank[k]; ok && prev <= rk {
			continue
		}
		e := Entry{
			Context: int(r.MaxInput), MaxOutput: int(r.MaxOutput),
			Input: perM(r.Input), Output: perM(r.Output),
			CacheRead: perM(r.CacheRead), CacheWrite: perM(r.CacheWrite),
			Kind: kind,
		}
		if f := float64(r.Image); f > 0 {
			e.Image = round6(f)
		}
		if e.Context == 0 && kind == "" {
			e.Context = int(r.MaxTokens)
		}
		if e.Context == 0 && e.MaxOutput == 0 && !e.priced() {
			continue
		}
		out[k], rank[k] = e, rk
	}
	if len(out) == 0 {
		return nil, errors.New("litellm: no models")
	}
	addAliases(out)
	return out, nil
}
