// Package llm defines the model vocabulary (messages, stream chunks), the
// adapter contract every model provider implements, and the `llm` service
// where adapters register. Providers live in providers/llm-*.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/kernel"
)

// ServiceKey is the context key of the adapter registry.
const ServiceKey = "llm"

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall is a model-requested tool invocation.
type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// ToolResult answers a ToolCall.
type ToolResult struct {
	CallID  string `json:"callId"`
	Name    string `json:"name,omitempty"`
	Content string `json:"content"`
	IsError bool   `json:"isError,omitempty"`
}

// Part is one piece of a message.
type Part struct {
	Type       string      `json:"type"` // text | thinking | tool_call | tool_result
	Text       string      `json:"text,omitempty"`
	ToolCall   *ToolCall   `json:"toolCall,omitempty"`
	ToolResult *ToolResult `json:"toolResult,omitempty"`
}

// Message is a provider-neutral chat message.
type Message struct {
	Role  Role   `json:"role"`
	Parts []Part `json:"parts"`
}

// Text returns concatenated text parts.
func (m Message) Text() string {
	var b strings.Builder
	for _, p := range m.Parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// ToolCalls returns the message's tool calls.
func (m Message) ToolCalls() []ToolCall {
	var out []ToolCall
	for _, p := range m.Parts {
		if p.Type == "tool_call" && p.ToolCall != nil {
			out = append(out, *p.ToolCall)
		}
	}
	return out
}

func TextMessage(role Role, text string) Message {
	return Message{Role: role, Parts: []Part{{Type: "text", Text: text}}}
}

// ToolSchema describes a tool to the model (JSON Schema parameters).
type ToolSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// Request is a single model request. It must be reconstructible from the
// session log ("model-visible means recorded").
type Request struct {
	Model     string       `json:"model"` // model id within the provider
	System    string       `json:"system"`
	Messages  []Message    `json:"messages"`
	Tools     []ToolSchema `json:"tools,omitempty"`
	Effort    string       `json:"effort,omitempty"` // low | medium | high
	MaxTokens int          `json:"maxTokens,omitempty"`
}

// Usage counts tokens.
type Usage struct {
	InputTokens  int `json:"input"`
	OutputTokens int `json:"output"`
}

// Chunk is one streaming piece.
type Chunk struct {
	Type     string    `json:"type"` // text | thinking | tool_call
	Text     string    `json:"text,omitempty"`
	ToolCall *ToolCall `json:"toolCall,omitempty"`
}

// Response is the completed assistant turn.
type Response struct {
	Message    Message `json:"message"`
	Usage      Usage   `json:"usage"`
	StopReason string  `json:"stopReason"` // end | tool_use | max_tokens
}

// ModelInfo describes an available model.
type ModelInfo struct {
	ID            string   `json:"id"`
	Provider      string   `json:"provider"`
	Name          string   `json:"name,omitempty"`
	ContextWindow int      `json:"contextWindow"`
	Capabilities  []string `json:"capabilities,omitempty"` // tools, vision, reasoning, long-context, code, embedding
	Enabled       bool     `json:"enabled"`
}

// Quota is remaining subscription/API allowance, when known.
type Quota struct {
	Known     bool      `json:"known"`
	Remaining float64   `json:"remaining"` // 0..1
	ResetsAt  time.Time `json:"resetsAt,omitzero"`
}

// Adapter is the contract every model provider implements.
type Adapter interface {
	// ID is the provider id, e.g. "anthropic", "ollama", "acp-claude-code".
	ID() string
	// Kind is "subscription" | "api" | "local".
	Kind() string
	Models(ctx context.Context) ([]ModelInfo, error)
	// Stream performs the request, calling onChunk for every streamed piece,
	// and returns the full response. Errors should be *Error when classifiable.
	Stream(ctx context.Context, req Request, onChunk func(Chunk) error) (Response, error)
}

// QuotaReporter is optionally implemented by adapters that know their quota.
type QuotaReporter interface {
	Quota(ctx context.Context) Quota
}

// ErrorKind classifies provider failures for routing.
type ErrorKind string

const (
	ErrRateLimited     ErrorKind = "rate_limited"
	ErrQuotaExhausted  ErrorKind = "quota_exhausted"
	ErrUnavailable     ErrorKind = "unavailable"
	ErrAuth            ErrorKind = "auth"
	ErrBadRequest      ErrorKind = "bad_request"
	ErrContextOverflow ErrorKind = "context_overflow"
)

// Error is a classified provider error.
type Error struct {
	Kind       ErrorKind
	Provider   string
	Message    string
	RetryAfter time.Duration
	ResetsAt   time.Time
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s: %s", e.Provider, e.Kind, e.Message) }

// KindOf returns the error kind or "" for unclassified errors.
func KindOf(err error) ErrorKind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return ""
}

// ShouldFailover reports whether the router should try the next model.
func ShouldFailover(err error) bool {
	switch KindOf(err) {
	case ErrRateLimited, ErrQuotaExhausted, ErrUnavailable, ErrAuth:
		return true
	}
	return false
}

// ModelRef identifies a model as "provider/model" with an effort.
type ModelRef struct {
	Provider string `json:"provider" yaml:"provider"`
	Model    string `json:"model" yaml:"model"`
	Effort   string `json:"effort,omitempty" yaml:"effort,omitempty"`
}

func (m ModelRef) String() string { return m.Provider + "/" + m.Model }

// ParseRef parses "provider/model[@effort]".
func ParseRef(s string) (ModelRef, error) {
	var effort string
	if i := strings.LastIndex(s, "@"); i > 0 {
		s, effort = s[:i], s[i+1:]
	}
	p, m, ok := strings.Cut(s, "/")
	if !ok || p == "" || m == "" {
		return ModelRef{}, fmt.Errorf("llm: bad model ref %q (want provider/model)", s)
	}
	return ModelRef{Provider: p, Model: m, Effort: effort}, nil
}

// Registry is the `llm` service.
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]Adapter
}

func NewRegistry() *Registry { return &Registry{adapters: map[string]Adapter{}} }

// Register adds an adapter bound to ctx's lifetime. Duplicate ids fail.
func (r *Registry) Register(c *kernel.Context, a Adapter) error {
	r.mu.Lock()
	if _, ok := r.adapters[a.ID()]; ok {
		r.mu.Unlock()
		return fmt.Errorf("llm: adapter %q already registered", a.ID())
	}
	r.adapters[a.ID()] = a
	r.mu.Unlock()
	AdaptersChanged.Emit(c.Host(), struct{}{})
	c.Defer(func() {
		r.mu.Lock()
		if r.adapters[a.ID()] == a {
			delete(r.adapters, a.ID())
		}
		r.mu.Unlock()
		AdaptersChanged.Emit(c.Host(), struct{}{})
	})
	return nil
}

func (r *Registry) Get(id string) (Adapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[id]
	return a, ok
}

func (r *Registry) List() []Adapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Adapter, 0, len(r.adapters))
	for _, a := range r.adapters {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}

// Events.
var (
	// AdaptersChanged notifies that the adapter set changed (no delta).
	AdaptersChanged = kernel.NewEmit[struct{}]("llm/adapters-changed")
	// StreamChunk lets plugins translate, redact or meter streamed chunks.
	StreamChunk = kernel.NewWaterfall[Chunk, Chunk]("llm/stream")
)

// Plugin provides the `llm` registry service.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/llm",
	Description: "Model adapter registry (llm service).",
	Apply: func(c *kernel.Context) error {
		return c.Provide(ServiceKey, NewRegistry())
	},
})

// EstimateTokens is a cheap heuristic (≈4 chars/token, CJK ≈1 char/token).
func EstimateTokens(s string) int {
	n := 0
	ascii := 0
	for _, r := range s {
		if r < 128 {
			ascii++
		} else {
			n++
		}
	}
	return n + (ascii+3)/4
}

// EstimateRequest estimates tokens of a request.
func EstimateRequest(req Request) int {
	n := EstimateTokens(req.System)
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			n += EstimateTokens(p.Text) + 4
			if p.ToolCall != nil {
				n += EstimateTokens(string(p.ToolCall.Args)) + 8
			}
			if p.ToolResult != nil {
				n += EstimateTokens(p.ToolResult.Content) + 8
			}
		}
	}
	for _, t := range req.Tools {
		b, _ := json.Marshal(t)
		n += EstimateTokens(string(b))
	}
	return n
}
