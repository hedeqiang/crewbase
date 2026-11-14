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

	mu       sync.Mutex
	requests []llm.Request
	// Script, when set, overrides the default behaviour.
	Script func(req llm.Request) (llm.Response, error)
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
	return append([]llm.ModelInfo(nil), a.models...), nil
}

// SetKind sets the provider kind reported by Kind (subscription/api/local).
func (a *Adapter) SetKind(k string) { a.kind = k }

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
	a.mu.Unlock()
	if a.exhausted.Load() {
		return llm.Response{}, &llm.Error{Kind: llm.ErrQuotaExhausted, Provider: a.id, Message: "weekly limit reached", ResetsAt: time.Now().Add(time.Hour)}
	}
	if a.delay > 0 {
		select {
		case <-time.After(a.delay):
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
	resp.Usage = llm.Usage{InputTokens: llm.EstimateRequest(req), OutputTokens: llm.EstimateTokens(resp.Message.Text())}
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
	return text(fmt.Sprintf("%s: %s", req.Model, t))
}

func text(s string) llm.Response {
	return llm.Response{StopReason: "end", Message: llm.TextMessage(llm.RoleAssistant, s)}
}

// Plugin registers mock adapters. Config: {"ids": ["mock"], "delayMs": 0}.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/llm-mock",
	Inject:      []string{llm.ServiceKey},
	Description: "Deterministic mock model provider.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[struct {
			IDs     []string `json:"ids"`
			DelayMs int      `json:"delayMs"`
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
