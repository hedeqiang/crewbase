// Package redact scrubs secrets from tool results, streamed model text, logs
// and diagnostics (docs/06 §5, docs/05 §5). It replaces every secret value
// known to the credentials service (min length 6) and common token shapes
// with "[redacted]".
package redact

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

// Placeholder replaces redacted content.
const Placeholder = "[redacted]"

// MinSecretLen is the shortest known value that is scrubbed literally;
// shorter values would mangle ordinary text.
const MinSecretLen = 6

// MaxDiagnostic is the cap on diagnostic text (docs/06 §5).
const MaxDiagnostic = 4096

// SecretSource yields secret values to scrub (the credentials service
// implements it).
type SecretSource interface {
	Secrets() []string
}

var patterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_\-]{16,}`), Placeholder},
	{regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}`), Placeholder},
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`), Placeholder},
	{regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9\-]{10,}`), Placeholder},
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), Placeholder},
	{regexp.MustCompile(`(?i)\b(Bearer)\s+[A-Za-z0-9\-._~+/]{8,}=*`), "${1} " + Placeholder},
}

var (
	sourcesMu sync.Mutex
	sources   = map[*kernel.Context]func() []string{}
)

// addSource registers a lazily evaluated source for ctx's lifetime.
func addSource(c *kernel.Context, fn func() []string) {
	sourcesMu.Lock()
	sources[c] = fn
	sourcesMu.Unlock()
	c.Defer(func() {
		sourcesMu.Lock()
		delete(sources, c)
		sourcesMu.Unlock()
	})
}

func globalSecrets() []string {
	sourcesMu.Lock()
	fns := make([]func() []string, 0, len(sources))
	for _, fn := range sources {
		fns = append(fns, fn)
	}
	sourcesMu.Unlock()
	var out []string
	for _, fn := range fns {
		out = append(out, fn()...)
	}
	return out
}

// With redacts s using the given secret values plus the token patterns.
func With(s string, secrets []string) string {
	if s == "" {
		return s
	}
	vals := make([]string, 0, len(secrets)*2)
	for _, v := range secrets {
		if len(v) < MinSecretLen {
			continue
		}
		vals = append(vals, v)
		// also the JSON-escaped form, for structured payloads
		if b, err := json.Marshal(v); err == nil {
			if esc := string(b[1 : len(b)-1]); esc != v {
				vals = append(vals, esc)
			}
		}
	}
	// longest first, so a secret containing another is replaced whole
	sort.Slice(vals, func(i, j int) bool { return len(vals[i]) > len(vals[j]) })
	for _, v := range vals {
		if strings.Contains(s, v) {
			s = strings.ReplaceAll(s, v, Placeholder)
		}
	}
	for _, p := range patterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return s
}

// Redact scrubs s with every secret known to loaded redact plugins (via
// the credentials service) and the token patterns. Safe for logs and
// memory files; usable without a loaded plugin (patterns only).
func Redact(s string) string { return With(s, globalSecrets()) }

// Diagnostic redacts s and caps it at MaxDiagnostic bytes (on a UTF-8
// boundary), for sub-agent and plugin diagnostics.
func Diagnostic(s string) string {
	s = Redact(s)
	if len(s) <= MaxDiagnostic {
		return s
	}
	const marker = "…[truncated]"
	cut := MaxDiagnostic - len(marker)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + marker
}

// Result redacts a tool result's content and structured data.
func Result(r tools.Result, secrets []string) tools.Result {
	r.Content = With(r.Content, secrets)
	if r.Data != nil {
		if b, err := json.Marshal(r.Data); err == nil {
			if red := With(string(b), secrets); red != string(b) {
				r.Data = json.RawMessage(red)
			}
		}
	}
	return r
}

// Response redacts a complete model reply: consecutive text parts are
// joined first (so a secret split across stream chunks is caught), then
// text, thinking text and every tool call's argument strings are scrubbed.
// A signed thinking part (provider Meta) whose text had to change loses its
// Meta: the signature would no longer verify, and unsigned thinking is not
// sent back to models.
func Response(r llm.Response, secrets []string) llm.Response {
	parts := make([]llm.Part, 0, len(r.Message.Parts))
	for _, p := range r.Message.Parts {
		if n := len(parts); n > 0 && p.Type == "text" && parts[n-1].Type == "text" && p.Meta == nil && parts[n-1].Meta == nil {
			parts[n-1].Text += p.Text
			continue
		}
		parts = append(parts, p)
	}
	for i := range parts {
		p := &parts[i]
		switch {
		case p.Type == "text":
			p.Text = With(p.Text, secrets)
		case p.Type == "thinking":
			if red := With(p.Text, secrets); red != p.Text {
				p.Text = red
				p.Meta = nil
			}
		case p.ToolCall != nil:
			tc := *p.ToolCall
			tc.Args = JSON(tc.Args, secrets)
			p.ToolCall = &tc
		}
	}
	r.Message.Parts = parts
	return r
}

// JSON redacts every string value (and object key) inside a JSON document,
// keeping it valid JSON. Invalid input is redacted as plain text.
func JSON(raw json.RawMessage, secrets []string) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var v any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return json.RawMessage(With(string(raw), secrets))
	}
	changed := false
	var walk func(any) any
	walk = func(x any) any {
		switch t := x.(type) {
		case string:
			if red := With(t, secrets); red != t {
				changed = true
				return red
			}
			return t
		case []any:
			for i := range t {
				t[i] = walk(t[i])
			}
			return t
		case map[string]any:
			out := make(map[string]any, len(t))
			for k, val := range t {
				nk := With(k, secrets)
				if nk != k {
					changed = true
				}
				out[nk] = walk(val)
			}
			return out
		}
		return x
	}
	v = walk(v)
	if !changed {
		return raw
	}
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// Plugin registers redaction on tools/post-execute, agent/response and
// llm/stream. It
// prepends both middlewares and redacts on the way out, so it sees the
// final output of every other listener. Uses the `credentials` service
// when present (looked up per call, so load order does not matter).
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/redact",
	Optional:    []string{"credentials"},
	Description: "Redacts secrets from tool results, streams and logs.",
	Apply: func(c *kernel.Context) error {
		secrets := func() []string {
			if src, ok := kernel.Lookup[SecretSource](c, "credentials"); ok {
				return src.Secrets()
			}
			return nil
		}
		addSource(c, secrets)
		tools.PostExecute.Prepend(c, func(ctx context.Context, e tools.Execution, next kernel.Next[tools.Execution, tools.Result]) (tools.Result, error) {
			r, err := next(ctx, e)
			if err != nil {
				// The registry would fall back to the unredacted
				// e.Result on error: return a redacted copy instead.
				c.Log().Warn("post-execute failed; redacting original result", "err", Diagnostic(err.Error()))
				return Result(e.Result, secrets()), nil
			}
			return Result(r, secrets()), nil
		})
		agentloop.Response.Prepend(c, func(ctx context.Context, r llm.Response, next kernel.Next[llm.Response, llm.Response]) (llm.Response, error) {
			out, err := next(ctx, r)
			if err != nil {
				return Response(r, secrets()), err
			}
			return Response(out, secrets()), nil
		})
		llm.StreamChunk.Prepend(c, func(ctx context.Context, ch llm.Chunk, next kernel.Next[llm.Chunk, llm.Chunk]) (llm.Chunk, error) {
			s := secrets()
			ch.Text = With(ch.Text, s)
			out, err := next(ctx, ch)
			out.Text = With(out.Text, s)
			return out, err
		})
		return nil
	},
})
