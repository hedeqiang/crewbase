package local

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/kernel"
)

// Ollama is a native Ollama adapter (/api/chat, /api/tags, /api/show).
type Ollama struct {
	id, baseURL string
	keys        keySource
	numCtx      int
	http        *http.Client

	mu       sync.Mutex
	cached   []llm.ModelInfo
	cachedAt time.Time
	cacheTTL time.Duration
	shown    map[string]showInfo // digest → details
	seq      atomic.Int64
}

type showInfo struct {
	window int
	caps   []string
}

// NewOllama builds an Ollama adapter for cfg (id, baseURL, numCtx, key).
func NewOllama(c *kernel.Context, cfg ServerConfig) *Ollama {
	id := cfg.ID
	if id == "" {
		id = "ollama"
	}
	return &Ollama{
		id: id, baseURL: trimSlash(cfg.BaseURL), numCtx: cfg.NumCtx, http: &http.Client{},
		keys:     keySource{c: c, literal: cfg.APIKey, ref: cfg.APIKeyRef},
		cacheTTL: 30 * time.Second, shown: map[string]showInfo{},
	}
}

// SetHTTPClient replaces the HTTP client.
func (o *Ollama) SetHTTPClient(c *http.Client) { o.http = c }

func (o *Ollama) ID() string   { return o.id }
func (o *Ollama) Kind() string { return "local" }

func (o *Ollama) do(ctx context.Context, method, path string, body any) (*http.Response, string, error) {
	key, err := o.keys.key(ctx, o.id)
	if err != nil {
		return nil, "", err
	}
	var rd *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, "", &llm.Error{Kind: llm.ErrBadRequest, Provider: o.id, Message: "cannot encode request"}
		}
		rd = bytes.NewReader(b)
	}
	var req *http.Request
	if rd != nil {
		req, err = http.NewRequestWithContext(ctx, method, o.baseURL+path, rd)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, o.baseURL+path, nil)
	}
	if err != nil {
		return nil, "", &llm.Error{Kind: llm.ErrBadRequest, Provider: o.id, Message: "bad base URL"}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := o.http.Do(req)
	if err != nil {
		return nil, key, transportError(ctx, o.id, err, key)
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, key, httpError(o.id, resp, key)
	}
	return resp, key, nil
}

// ---- models ----

type tagsResp struct {
	Models []struct {
		Name    string `json:"name"`
		Model   string `json:"model"`
		Size    int64  `json:"size"`
		Digest  string `json:"digest"`
		Details struct {
			ParameterSize     string `json:"parameter_size"`
			QuantizationLevel string `json:"quantization_level"`
		} `json:"details"`
	} `json:"models"`
}

// HumanSize formats bytes like "4.7 GB".
func HumanSize(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}

// Models lists installed models (GET /api/tags) with SizeBytes and
// context window / capabilities from POST /api/show (cached per digest).
func (o *Ollama) Models(ctx context.Context) ([]llm.ModelInfo, error) {
	o.mu.Lock()
	if o.cached != nil && time.Since(o.cachedAt) < o.cacheTTL {
		out := append([]llm.ModelInfo(nil), o.cached...)
		o.mu.Unlock()
		return out, nil
	}
	o.mu.Unlock()
	resp, _, err := o.do(ctx, http.MethodGet, "/api/tags", nil)
	if err != nil {
		o.mu.Lock()
		defer o.mu.Unlock()
		if o.cached != nil {
			return append([]llm.ModelInfo(nil), o.cached...), nil
		}
		return nil, err
	}
	var tr tagsResp
	err = json.NewDecoder(resp.Body).Decode(&tr)
	resp.Body.Close()
	if err != nil {
		return nil, &llm.Error{Kind: llm.ErrUnavailable, Provider: o.id, Message: "bad /api/tags response"}
	}
	out := make([]llm.ModelInfo, 0, len(tr.Models))
	for _, m := range tr.Models {
		info := o.show(ctx, m.Name, m.Digest)
		w := info.window
		if o.numCtx > 0 {
			w = o.numCtx
		}
		out = append(out, llm.ModelInfo{ID: m.Name, Provider: o.id, Name: m.Name, ContextWindow: w, Capabilities: info.caps, Enabled: true, SizeBytes: m.Size})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	o.mu.Lock()
	o.cached, o.cachedAt = out, time.Now()
	o.mu.Unlock()
	return append([]llm.ModelInfo(nil), out...), nil
}

var capMap = map[string]string{"tools": "tools", "vision": "vision", "thinking": "reasoning", "embedding": "embedding"}

func (o *Ollama) show(ctx context.Context, name, digest string) showInfo {
	o.mu.Lock()
	if si, ok := o.shown[digest]; ok && digest != "" {
		o.mu.Unlock()
		return si
	}
	o.mu.Unlock()
	sctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	resp, _, err := o.do(sctx, http.MethodPost, "/api/show", map[string]any{"model": name})
	if err != nil {
		return showInfo{}
	}
	defer resp.Body.Close()
	var sr struct {
		Capabilities []string       `json:"capabilities"`
		ModelInfo    map[string]any `json:"model_info"`
	}
	if json.NewDecoder(resp.Body).Decode(&sr) != nil {
		return showInfo{}
	}
	var si showInfo
	for k, v := range sr.ModelInfo {
		if strings.HasSuffix(k, ".context_length") {
			if f, ok := v.(float64); ok {
				si.window = int(f)
			}
		}
	}
	for _, c := range sr.Capabilities {
		if m, ok := capMap[c]; ok {
			si.caps = append(si.caps, m)
		}
	}
	o.mu.Lock()
	o.shown[digest] = si
	o.mu.Unlock()
	return si
}

// ---- chat ----

type ollamaToolCall struct {
	ID       string `json:"id,omitempty"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

type ollamaMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	Thinking  string           `json:"thinking,omitempty"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
	ToolName  string           `json:"tool_name,omitempty"`
}

func mapOllama(system string, msgs []llm.Message) []ollamaMessage {
	names := toolNames(msgs)
	var out []ollamaMessage
	if system != "" {
		out = append(out, ollamaMessage{Role: "system", Content: system})
	}
	for _, m := range msgs {
		var text strings.Builder
		var calls []ollamaToolCall
		var results []ollamaMessage
		for _, p := range m.Parts {
			switch p.Type {
			case "text":
				text.WriteString(p.Text)
			case "tool_call":
				if tc := p.ToolCall; tc != nil {
					var oc ollamaToolCall
					oc.Function.Name = tc.Name
					oc.Function.Arguments = argsObject(tc.Args)
					calls = append(calls, oc)
				}
			case "tool_result":
				if r := p.ToolResult; r != nil {
					name := r.Name
					if name == "" {
						name = names[r.CallID]
					}
					c := r.Content
					if r.IsError {
						c = "[error] " + c
					}
					results = append(results, ollamaMessage{Role: "tool", Content: c, ToolName: name})
				}
			}
		}
		if m.Role == llm.RoleAssistant {
			// Thinking parts (and their provider metadata) are not
			// understood here; a message holding nothing else is skipped.
			if text.Len() == 0 && len(calls) == 0 {
				continue
			}
			out = append(out, ollamaMessage{Role: "assistant", Content: text.String(), ToolCalls: calls})
			continue
		}
		out = append(out, results...)
		if t := text.String(); t != "" || (len(results) == 0 && m.Role == llm.RoleUser) {
			out = append(out, ollamaMessage{Role: "user", Content: t})
		}
	}
	return out
}

type chatLine struct {
	Message         ollamaMessage `json:"message"`
	Done            bool          `json:"done"`
	DoneReason      string        `json:"done_reason"`
	PromptEvalCount int           `json:"prompt_eval_count"`
	EvalCount       int           `json:"eval_count"`
	Error           string        `json:"error"`
}

func (o *Ollama) body(req llm.Request) map[string]any {
	b := map[string]any{"model": req.Model, "messages": mapOllama(req.System, req.Messages), "stream": true}
	opts := map[string]any{}
	if req.MaxTokens > 0 {
		opts["num_predict"] = req.MaxTokens
	}
	if o.numCtx > 0 {
		opts["num_ctx"] = o.numCtx
	}
	if len(opts) > 0 {
		b["options"] = opts
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{
				"name": t.Name, "description": t.Description, "parameters": schemaOrEmpty(t.Parameters),
			}})
		}
		b["tools"] = tools
	}
	switch e := strings.ToLower(req.Effort); {
	case e == "":
	case e == "none" || e == "off":
		b["think"] = false
	case strings.Contains(req.Model, "gpt-oss"): // accepts levels
		b["think"] = e
	default:
		b["think"] = true
	}
	return b
}

// Stream performs POST /api/chat with NDJSON streaming.
func (o *Ollama) Stream(ctx context.Context, req llm.Request, onChunk func(llm.Chunk) error) (llm.Response, error) {
	resp, key, err := o.do(ctx, http.MethodPost, "/api/chat", o.body(req))
	if err != nil {
		return llm.Response{}, err
	}
	defer resp.Body.Close()
	var text, thinking strings.Builder
	var calls []*llm.ToolCall
	var usage llm.Usage
	done := false
	reason := ""
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 32<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var cl chatLine
		if json.Unmarshal(line, &cl) != nil {
			continue
		}
		if cl.Error != "" {
			return llm.Response{}, classify(o.id, 0, nil, cl.Error, "", key)
		}
		if t := cl.Message.Thinking; t != "" {
			thinking.WriteString(t)
			if err := onChunk(llm.Chunk{Type: "thinking", Text: t}); err != nil {
				return llm.Response{}, err
			}
		}
		if t := cl.Message.Content; t != "" {
			text.WriteString(t)
			if err := onChunk(llm.Chunk{Type: "text", Text: t}); err != nil {
				return llm.Response{}, err
			}
		}
		for _, tc := range cl.Message.ToolCalls {
			id := tc.ID
			if id == "" {
				id = fmt.Sprintf("call_%s_%d", o.id, o.seq.Add(1))
			}
			c := &llm.ToolCall{ID: id, Name: tc.Function.Name, Args: normalizeArgs(string(tc.Function.Arguments))}
			calls = append(calls, c)
			if err := onChunk(llm.Chunk{Type: "tool_call", ToolCall: c}); err != nil {
				return llm.Response{}, err
			}
		}
		if cl.Done {
			done, reason = true, cl.DoneReason
			usage = llm.Usage{InputTokens: cl.PromptEvalCount, OutputTokens: cl.EvalCount}
			break
		}
	}
	if err := sc.Err(); err != nil {
		return llm.Response{}, streamReadError(ctx, o.id, err)
	}
	if !done {
		if ctx.Err() != nil {
			return llm.Response{}, ctx.Err()
		}
		return llm.Response{}, &llm.Error{Kind: llm.ErrUnavailable, Provider: o.id, Message: "stream ended before completion"}
	}
	msg := llm.Message{Role: llm.RoleAssistant}
	if t := thinking.String(); t != "" {
		msg.Parts = append(msg.Parts, llm.Part{Type: "thinking", Text: t})
	}
	if t := text.String(); t != "" {
		msg.Parts = append(msg.Parts, llm.Part{Type: "text", Text: t})
	}
	for _, c := range calls {
		msg.Parts = append(msg.Parts, llm.Part{Type: "tool_call", ToolCall: c})
	}
	stop := "end"
	switch {
	case len(calls) > 0:
		stop = "tool_use"
	case reason == "length":
		stop = "max_tokens"
	}
	return llm.Response{Message: msg, Usage: estimateUsage(usage, req, msg), StopReason: stop}, nil
}
