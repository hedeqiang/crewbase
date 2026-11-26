// Package llm defines the model vocabulary (messages, stream chunks), the
// adapter contract every model provider implements, and the `llm` service
// where adapters register. Providers live in providers/llm-*.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
	// Attachments are images a tool saved in the agent workspace for the
	// model to look at (e.g. a window screenshot). Most APIs cannot carry
	// images inside a tool result, so the history projection hands them
	// to the model as a user message right after the step's results, and
	// only for the latest few (session.MaxToolImages).
	Attachments []Attachment `json:"attachments,omitempty"`
}

// Part is one piece of a message.
type Part struct {
	Type       string      `json:"type"` // text | thinking | tool_call | tool_result | attachment
	Text       string      `json:"text,omitempty"`
	ToolCall   *ToolCall   `json:"toolCall,omitempty"`
	ToolResult *ToolResult `json:"toolResult,omitempty"`
	// Attachment is a file the person attached (PartAttachment). The log
	// records only the reference; the bytes stay in the agent workspace
	// and are loaded right before the request (see Attachment.Data).
	Attachment *Attachment `json:"attachment,omitempty"`
	// Meta carries opaque provider data that must round-trip with the
	// part (e.g. thinking signatures). It is recorded in the session log,
	// never shown to people, and only read back by the provider that wrote
	// it (keys are namespaced "<provider>:<name>").
	Meta map[string]string `json:"meta,omitempty"`
}

// PartAttachment is a user-attached file (image, PDF, Office, text).
const PartAttachment = "attachment"

// Attachment references a file in the agent workspace.
type Attachment struct {
	Path   string `json:"path"` // workspace-relative, e.g. uploads/2026-11-15/report.pdf
	Name   string `json:"name"` // original file name
	Mime   string `json:"mime"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"`

	// Filled in right before a request (never logged):
	//   Data — the bytes, when the model can take this file natively
	//          (images for vision models, PDFs for providers that read them);
	//   Text — what a model that cannot read the file natively sees: a
	//          header with the workspace path plus the extracted text.
	// Adapters send Data in their native form when they support the type,
	// otherwise Text.
	Data []byte `json:"-"`
	Text string `json:"-"`
	// Alt is the full text form of a document sent natively (Data set,
	// Text then only a label), for adapters whose native path failed
	// (e.g. a provider file upload).
	Alt string `json:"-"`
	// Tokens is the context the attachment took when it was sent (set on
	// a recorded copy of a request whose bytes were dropped); 0 = unknown.
	Tokens int `json:"-"`
}

// IsImage reports whether the attachment is a raster image models can view.
func (a *Attachment) IsImage() bool {
	switch a.Mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return false
}

// IsPDF reports whether the attachment is a PDF.
func (a *Attachment) IsPDF() bool { return a.Mime == "application/pdf" }

// Fallback is the text form of the attachment for models (or adapters)
// that cannot take its bytes.
func (a *Attachment) Fallback() string {
	if a.Text != "" {
		return a.Text
	}
	return fmt.Sprintf("[附件 %s（%s，%d 字节）已保存在工作区 %s]", a.Name, a.Mime, a.Size, a.Path)
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
	// Deferred asks a provider with native tool search (ToolSearcher) to
	// keep the tool out of the model's context until the model finds it.
	// The agent loop sets it only for adapters that are ToolSearchers.
	Deferred bool `json:"deferred,omitempty"`
	// Group is the family of a deferred tool (an MCP server, a connector),
	// for providers that group deferred tools (OpenAI's namespaces).
	Group string `json:"group,omitempty"`
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
	// NativeTools asks for provider-hosted tools (NativeWebSearch,
	// NativeReadURL). Adapters that cannot serve them ignore them.
	NativeTools []string `json:"nativeTools,omitempty"`
	// Context about who is asking; never sent to the model. Adapters that
	// need a working directory or per-session state use it.
	Agent     string `json:"-"`
	Session   string `json:"-"`
	Workspace string `json:"-"`
	// Mode is the effective run mode of the turn (the stricter of the
	// agent's and the input's).
	Mode string `json:"-"`
	// SystemParts names the parts of System (see SystemPart), when the
	// agent loop knows them; nil otherwise.
	SystemParts []SystemPart `json:"-"`
	// Store keeps an adapter's state for this agent's conversation (see
	// PrefixKeeper); nil outside a session.
	Store Store `json:"-"`
}

// Provider-hosted tools an agent may enable. They run on the provider's
// side (no local side effects), so they do not pass the tool pipeline.
const (
	NativeWebSearch = "web_search"
	NativeReadURL   = "read_url"
)

// Extra part types produced by providers with hosted tools. They are for
// people (sources, what was searched) and are not sent back to models.
const (
	PartCitation   = "citation"    // Text = title, Meta["url"]
	PartServerTool = "server_tool" // Text = human summary, Meta["kind"]
)

// ServerToolSearch is the PartServerTool kind of a provider's own tool
// search; Meta["tools"] lists the tools it loaded, comma-separated.
const ServerToolSearch = "tool_search"

// PartProviderBlock is an opaque provider content block that must be sent
// back to the provider that wrote it (e.g. Anthropic's server_tool_use and
// tool_search_tool_result blocks). The block is in Meta under a key
// namespaced by that provider; other providers skip the part.
const PartProviderBlock = "provider_block"

// StopRefusal is the stop reason when a provider's safety check declined.
const StopRefusal = "refusal"

// Usage counts tokens.
type Usage struct {
	InputTokens  int `json:"input"` // all input tokens, cache reads/writes included
	OutputTokens int `json:"output"`
	CacheRead    int `json:"cacheRead,omitempty"`
	CacheWrite   int `json:"cacheWrite,omitempty"`
	// ServerTools counts provider-hosted tool calls (billed per call).
	ServerTools map[string]int `json:"serverTools,omitempty"`
	// Estimated marks usage the adapter filled from heuristics because the
	// provider reported none (it is not a provider count).
	Estimated bool `json:"estimated,omitempty"`
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
	Capabilities  []string `json:"capabilities,omitempty"` // tools, vision, reasoning, long-context, code, embedding, image-edit
	// Kind is what the model produces: "" / chat, image or video (see
	// media.go). Chat pickers and routes only use chat models.
	Kind      string `json:"kind,omitempty"`
	Enabled   bool   `json:"enabled"`
	SizeBytes int64  `json:"sizeBytes,omitempty"` // local models
	// MaxOutput is the most tokens one reply may have, when known.
	MaxOutput int `json:"maxOutput,omitempty"`
	// Pricing is the model's API list price, when known (see catalog.go).
	Pricing *Pricing `json:"pricing,omitempty"`
	// ContextEstimated marks a ContextWindow the adapter guessed (a table
	// or a default) instead of the provider reporting it; the model
	// catalog may replace it.
	ContextEstimated bool `json:"contextEstimated,omitempty"`
	// Efforts are the reasoning levels the model takes (a subset of
	// Efforts, in that order). Nil means the adapter does not say, so all
	// levels are offered; empty means it takes none (pickers hide the
	// choice).
	Efforts []string `json:"efforts"`
}

// NoEfforts marks a model that takes no reasoning level.
var NoEfforts = []string{}

// Quota is remaining allowance (a gateway subscription or API balance), when known.
type Quota struct {
	Known     bool      `json:"known"`
	Remaining float64   `json:"remaining"` // 0..1, the tightest window
	ResetsAt  time.Time `json:"resetsAt,omitzero"`
	// Label names the allowance Remaining is of (e.g. "codex · 7 days").
	Label string `json:"label,omitempty"`
	// Windows are the individual allowances (e.g. "5h", "week").
	Windows []QuotaWindow `json:"windows,omitempty"`
	// Accounts are the accounts behind the provider, each with all of its
	// allowances as the vendor reports them (a gateway with several
	// subscriptions of one vendor); for display. Windows above stays what
	// routing and the composer read.
	Accounts []QuotaAccount `json:"accounts,omitempty"`
}

// QuotaAccount is one account of a vendor behind a gateway.
type QuotaAccount struct {
	Vendor     string `json:"vendor"`               // e.g. "copilot", "codex"
	VendorName string `json:"vendorName,omitempty"` // e.g. "Copilot"
	Account    string `json:"account,omitempty"`    // the account's own name
	Plan       string `json:"plan,omitempty"`
	// Balance is a prepaid balance as the vendor shows it ("¥96.45"),
	// for accounts without allowances.
	Balance string               `json:"balance,omitempty"`
	Windows []QuotaAccountWindow `json:"windows,omitempty"`
}

// QuotaAccountWindow is one allowance of an account.
type QuotaAccountWindow struct {
	Name string `json:"name"`
	// Used is the share used, 0..1 (only when Display is empty or alongside it).
	Used float64 `json:"used"`
	// Spent and Limit in Unit, when the vendor counts them ("14301.6 / 16000").
	Spent float64 `json:"spent,omitempty"`
	Limit float64 `json:"limit,omitempty"`
	Unit  string  `json:"unit,omitempty"`
	// Display is the vendor's own text for an allowance without a
	// meaningful share ("Unlimited", "$41.26"); Text marks it display-only.
	Display  string    `json:"display,omitempty"`
	Text     bool      `json:"text,omitempty"`
	ResetsAt time.Time `json:"resetsAt,omitzero"`
}

// QuotaWindow is one allowance window (e.g. a gateway subscription's 5-hour limit).
type QuotaWindow struct {
	Label     string    `json:"label"`
	Remaining float64   `json:"remaining"` // 0..1
	ResetsAt  time.Time `json:"resetsAt,omitzero"`
}

// Adapter is the contract every model provider implements.
type Adapter interface {
	// ID is the provider id, e.g. "anthropic", "ollama", "magpie".
	ID() string
	// Kind is "api" | "gateway" | "local".
	Kind() string
	Models(ctx context.Context) ([]ModelInfo, error)
	// Stream performs the request, calling onChunk for every streamed piece,
	// and returns the full response. Errors should be *Error when classifiable.
	Stream(ctx context.Context, req Request, onChunk func(Chunk) error) (Response, error)
}

// FileAccepter is optionally implemented by adapters that take some
// attachments natively (images for vision models, PDFs, documents through
// a provider file API). Attachments it does not accept are sent as text
// (Attachment.Text): extracted content plus the workspace path.
type FileAccepter interface {
	AcceptsFile(model, mime string) bool
}

// ToolSearcher is optionally implemented by adapters whose provider can
// search deferred tools itself (ToolSchema.Deferred): the agent loop then
// sends every tool, the deferred ones marked, instead of its own
// tool_search meta-tool.
type ToolSearcher interface {
	SearchesTools(model string) bool
}

// ModelCap reports whether model lists capability c in ms; known=false
// when the model is not listed or lists no capabilities at all.
func ModelCap(ms []ModelInfo, model, c string) (has, known bool) {
	for _, m := range ms {
		if m.ID == model && len(m.Capabilities) > 0 {
			return slices.Contains(m.Capabilities, c), true
		}
	}
	return false, false
}

// QuotaReporter is optionally implemented by adapters that know their quota.
type QuotaReporter interface {
	Quota(ctx context.Context) Quota
}

// ModelQuotaReporter is optionally implemented by adapters that front
// several accounts (a gateway): the allowance behind one model, unknown
// when none is (a pay-as-you-go key, a routing group).
type ModelQuotaReporter interface {
	QuotaFor(ctx context.Context, model string) Quota
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
	// ErrModelNotFound: the provider no longer serves this model (removed,
	// renamed, not enabled). Fails over like an outage.
	ErrModelNotFound ErrorKind = "model_not_found"
	// ErrModelUnavailable: the provider serves the model, but not to this
	// account or plan (e.g. "The 'gpt-6.1-sol' model is not supported when
	// using Codex with a ChatGPT account"). Retrying will not help until
	// the plan changes, so the router keeps the model aside for long.
	ErrModelUnavailable ErrorKind = "model_unavailable"
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
	case ErrRateLimited, ErrQuotaExhausted, ErrUnavailable, ErrAuth, ErrModelNotFound, ErrModelUnavailable:
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

// Full renders the ref including the effort, as ParseRef accepts it.
func (m ModelRef) Full() string {
	if m.Effort == "" {
		return m.String()
	}
	return m.String() + "@" + m.Effort
}

// Efforts are the recognised reasoning effort levels.
var Efforts = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

// ParseRef parses "provider/model[@effort]". A trailing "@x" is an effort
// only when x is a known level, so model ids that contain "@" (e.g.
// "cursor/grok-4.7@500k" behind a gateway) stay intact.
func ParseRef(s string) (ModelRef, error) {
	var effort string
	if i := strings.LastIndex(s, "@"); i > 0 && slices.Contains(Efforts, s[i+1:]) {
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
	catalog  Catalog
	switches Switches
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

// EstimateAttachment estimates the context an attachment takes as it will
// be sent: its bytes when loaded, its text form otherwise.
func EstimateAttachment(at *Attachment) int {
	switch {
	case at.Tokens > 0:
		return at.Tokens
	case len(at.Data) > 0 && at.IsImage():
		return 1600 // a large image is about 1.6k tokens on most providers
	case len(at.Data) > 0:
		return len(at.Data)/40 + 1600 // PDFs: text plus page images, roughly
	case at.Text == "" && at.Size > 0:
		// not loaded yet (the log keeps only the reference): judge by size
		switch {
		case at.IsImage():
			return 1600
		case at.IsPDF():
			return int(at.Size)/40 + 1600
		case strings.HasPrefix(at.Mime, "text/") || strings.Contains(at.Mime, "json") || strings.Contains(at.Mime, "xml"):
			// ≈4 bytes a token over mixed text; the inline text is capped
			// near 120k characters
			return min(int(at.Size)/4, 120_000)
		}
	}
	return EstimateTokens(at.Fallback())
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
			if at := p.Attachment; at != nil {
				n += EstimateAttachment(at)
			}
		}
	}
	for _, t := range req.Tools {
		if t.Deferred { // outside the context until the model loads it
			continue
		}
		b, _ := json.Marshal(t)
		n += EstimateTokens(string(b))
	}
	return n
}
