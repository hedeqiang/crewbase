// Package mock is a deterministic model provider for tests, demos and
// offline use. Behaviour:
//
//   - A user message "/tool <name> <json-args>" makes the model call that tool.
//   - After a tool result, the model replies "Tool <name> returned: <content>".
//   - Otherwise it echoes: "<model>: <last user text>".
//   - SetExhausted(true) makes every request fail with quota_exhausted.
package mock

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/kernel"
)

// Adapter is the mock provider.
type Adapter struct {
	id        string
	kind      string
	models    []llm.ModelInfo
	exhausted atomic.Bool
	delay     time.Duration
	// promptCache: report usage as provider counts with simulated
	// prompt-cache reads (demos of the context map).
	promptCache bool
	lastInput   int

	mu       sync.Mutex
	requests []llm.Request
	// Script, when set, overrides the default behaviour.
	Script func(req llm.Request) (llm.Response, error)
	// StreamScript, when set, streams by hand (a reply cut off mid-way).
	StreamScript func(ctx context.Context, req llm.Request, onChunk func(llm.Chunk) error) (llm.Response, error)
}

// New creates a mock adapter.
func New(id string, window int) *Adapter {
	if window == 0 {
		window = 200000
	}
	return &Adapter{id: id, kind: "api", models: []llm.ModelInfo{
		{ID: "default", Provider: id, Name: id + " default", ContextWindow: window, Capabilities: []string{"tools"}, Enabled: true},
	}}
}

func (a *Adapter) ID() string   { return a.id }
func (a *Adapter) Kind() string { return a.kind }
func (a *Adapter) Models(context.Context) ([]llm.ModelInfo, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.ModelInfo(nil), a.models...), nil
}

// AddModel lists one more model (e.g. an image model in tests).
func (a *Adapter) AddModel(m llm.ModelInfo) {
	m.Provider = a.id
	a.mu.Lock()
	a.models = append(a.models, m)
	a.mu.Unlock()
}

// SetKind sets the provider kind reported by Kind (subscription/api/local).
func (a *Adapter) SetKind(k string) { a.kind = k }

// SetDelay makes every request take d (cancellable, like a real model).
func (a *Adapter) SetDelay(d time.Duration) {
	a.mu.Lock()
	a.delay = d
	a.mu.Unlock()
}

// SetExhausted toggles simulated quota exhaustion.
func (a *Adapter) SetExhausted(v bool) { a.exhausted.Store(v) }

// Requests returns every request received (for asserting reconstruction).
func (a *Adapter) Requests() []llm.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Request(nil), a.requests...)
}

func (a *Adapter) Quota(context.Context) llm.Quota {
	if a.exhausted.Load() {
		return llm.Quota{Known: true, Remaining: 0, ResetsAt: time.Now().Add(time.Hour)}
	}
	return llm.Quota{Known: true, Remaining: 1}
}

func (a *Adapter) Stream(ctx context.Context, req llm.Request, onChunk func(llm.Chunk) error) (llm.Response, error) {
	a.mu.Lock()
	a.requests = append(a.requests, req)
	script := a.Script
	delay := a.delay
	byHand := a.StreamScript
	a.mu.Unlock()
	if byHand != nil {
		return byHand(ctx, req, onChunk)
	}
	if a.exhausted.Load() {
		return llm.Response{}, &llm.Error{Kind: llm.ErrQuotaExhausted, Provider: a.id, Message: "weekly limit reached", ResetsAt: time.Now().Add(time.Hour)}
	}
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
	}
	var resp llm.Response
	var err error
	if script != nil {
		resp, err = script(req)
	} else {
		resp = a.respond(req)
	}
	if err != nil {
		return resp, err
	}
	for _, p := range resp.Message.Parts {
		var ch llm.Chunk
		switch p.Type {
		case "text":
			ch = llm.Chunk{Type: "text", Text: p.Text}
		case "tool_call":
			ch = llm.Chunk{Type: "tool_call", ToolCall: p.ToolCall}
		default:
			continue
		}
		if err := onChunk(ch); err != nil {
			return llm.Response{}, err
		}
	}
	in := llm.EstimateRequest(req)
	resp.Usage = llm.Usage{InputTokens: in, OutputTokens: llm.EstimateTokens(resp.Message.Text()), Estimated: true}
	if a.promptCache {
		// a provider count with a prompt cache: the previous request's
		// input is read from the cache, the rest is written to it
		a.mu.Lock()
		read := min(a.lastInput, in)
		a.lastInput = in
		a.mu.Unlock()
		resp.Usage.Estimated = false
		resp.Usage.CacheRead, resp.Usage.CacheWrite = read, in-read
	}
	return resp, nil
}

var callSeq atomic.Int64

func (a *Adapter) respond(req llm.Request) llm.Response {
	if len(req.Messages) == 0 {
		return text("(empty)")
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role == llm.RoleTool {
		r := last.Parts[0].ToolResult
		return text(fmt.Sprintf("Tool %s returned: %s", r.Name, r.Content))
	}
	t := lastInstruction(last.Text())
	if rest, ok := strings.CutPrefix(t, "/tool "); ok {
		line, _, _ := strings.Cut(strings.TrimSpace(rest), "\n")
		name, args, _ := strings.Cut(line, " ")
		args = strings.TrimSpace(args)
		if args == "" || !json.Valid([]byte(args)) {
			args = "{}"
		}
		id := fmt.Sprintf("call_%d", callSeq.Add(1))
		return llm.Response{StopReason: "tool_use", Message: llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{
			{Type: "tool_call", ToolCall: &llm.ToolCall{ID: id, Name: name, Args: json.RawMessage(args)}},
		}}}
	}
	var files []string
	for _, p := range last.Parts {
		if at := p.Attachment; p.Type == llm.PartAttachment && at != nil {
			files = append(files, fmt.Sprintf("%s (%s, %d chars)", at.Name, at.Mime, len([]rune(at.Fallback()))))
		}
	}
	if len(files) > 0 {
		return text(fmt.Sprintf("%s: %s\nattachments: %s", req.Model, t, strings.Join(files, "; ")))
	}
	return text(fmt.Sprintf("%s: %s", req.Model, t))
}

func text(s string) llm.Response {
	return llm.Response{StopReason: "end", Message: llm.TextMessage(llm.RoleAssistant, s)}
}

// Plugin registers mock adapters. Config: {"ids": ["mock"], "delayMs": 0,
// "kind": "api", "pricing": {"input": 3, "output": 15}, "promptCache": false}.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/llm-mock",
	Inject:      []string{llm.ServiceKey},
	Description: "Deterministic mock model provider.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[struct {
			IDs     []string `json:"ids"`
			DelayMs int      `json:"delayMs"`
			// Kind and Pricing let demos show a priced API model or a
			// subscription (the UI's cost display); unset: an API model
			// with no known price.
			Kind    string       `json:"kind"`
			Pricing *llm.Pricing `json:"pricing"`
			// PromptCache reports usage as provider counts with a
			// simulated prompt cache (the context map's counted view).
			PromptCache bool `json:"promptCache"`
		}](c)
		if err != nil {
			return err
		}
		if len(cfg.IDs) == 0 {
			cfg.IDs = []string{"mock"}
		}
		reg := kernel.Use[*llm.Registry](c, llm.ServiceKey)
		for _, id := range cfg.IDs {
			a := New(id, 0)
			a.delay = time.Duration(cfg.DelayMs) * time.Millisecond
			a.promptCache = cfg.PromptCache
			if cfg.Kind != "" {
				a.kind = cfg.Kind
			}
			if cfg.Pricing != nil {
				a.models[0].Pricing = cfg.Pricing
			}
			if err := reg.Register(c, a); err != nil {
				return err
			}
		}
		return nil
	},
})

// lastInstruction picks the paragraph to act on from a (possibly merged)
// user message: the last paragraph that is not another agent's reply
// produced by this mock ("[Name]: Tool … returned" / "[Name]: <model>: …").
// An attribution prefix "[Name]: " is stripped.
func lastInstruction(text string) string {
	paras := strings.Split(strings.TrimSpace(text), "\n\n")
	for i := len(paras) - 1; i >= 0; i-- {
		p := strings.TrimSpace(paras[i])
		body := p
		if strings.HasPrefix(p, "[") {
			if j := strings.Index(p, "]: "); j > 0 {
				body = p[j+3:]
				if strings.HasPrefix(body, "Tool ") || strings.HasPrefix(body, "default: ") {
					continue
				}
			}
		}
		if body != "" {
			return strings.TrimSpace(body)
		}
	}
	return strings.TrimSpace(text)
}
