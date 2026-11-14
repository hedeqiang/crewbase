package wasm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/bridge/process"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

// Config of a wasm plugin entry (set by the plugin manager).
type Config struct {
	Dir     string   `json:"dir"`   // plugin directory
	Entry   string   `json:"entry"` // module file, relative to Dir
	Hooks   []string `json:"hooks"`
	Budget  string   `json:"budget,omitempty"` // per-call time budget (default 50ms)
	Version string   `json:"version,omitempty"`
}

// Loader builds the kernel spec of a wasm plugin entry. Only hooks that
// are both declared and granted (entry permissions "hook:<name>") are
// attached.
func Loader(e kernel.Entry) (kernel.Spec, error) {
	granted, err := process.ParsePermissions(e.Permissions)
	if err != nil {
		return kernel.Spec{}, err
	}
	name := e.Name
	return kernel.Spec{
		Name:        name,
		Description: "WebAssembly plugin",
		Apply: func(c *kernel.Context) error {
			cfg, err := kernel.Decode[Config](c)
			if err != nil {
				return err
			}
			if cfg.Dir == "" || cfg.Entry == "" {
				return errors.New("wasm plugin needs dir and entry")
			}
			path := filepath.Join(cfg.Dir, cfg.Entry)
			if rel, err := filepath.Rel(cfg.Dir, path); err != nil || strings.HasPrefix(rel, "..") {
				return fmt.Errorf("entry %q leaves the plugin directory", cfg.Entry)
			}
			var hooks []string
			for _, h := range cfg.Hooks {
				if slices.Contains(granted.Hooks, h) && slices.Contains(process.WASMHooks, h) {
					hooks = append(hooks, h)
				}
			}
			if len(hooks) == 0 {
				return errors.New("no approved hooks")
			}
			budget := DefaultBudget
			if cfg.Budget != "" {
				if d, err := time.ParseDuration(cfg.Budget); err == nil && d > 0 && d <= 5*time.Second {
					budget = d
				}
			}
			m, err := Load(context.Background(), name, path, hooks, budget, c.Log())
			if err != nil {
				return err
			}
			c.Defer(func() { _ = m.Close(context.Background()) })
			Attach(c, m)
			return nil
		},
	}, nil
}

// Attach wires a module's hooks into the pipelines for c's lifetime.
func Attach(c *kernel.Context, m *Module) {
	if m.Has("stream_chunk") {
		llm.StreamChunk.Use(c, func(ctx context.Context, ch llm.Chunk, next kernel.Next[llm.Chunk, llm.Chunk]) (llm.Chunk, error) {
			if (ch.Type == "text" || ch.Type == "thinking") && ch.Text != "" {
				var out struct {
					Text *string `json:"text"`
				}
				if m.Call(ctx, "stream_chunk", map[string]string{"type": ch.Type, "text": ch.Text}, &out) == nil && out.Text != nil {
					ch.Text = *out.Text
				}
			}
			return next(ctx, ch)
		})
	}
	if m.Has("tool_result") {
		tools.PostExecute.Use(c, func(ctx context.Context, e tools.Execution, next kernel.Next[tools.Execution, tools.Result]) (tools.Result, error) {
			r, err := next(ctx, e)
			if err != nil || r.Content == "" {
				return r, err
			}
			var out struct {
				Content *string `json:"content"`
			}
			in := map[string]any{"tool": e.Call.Name, "category": e.Category, "content": r.Content, "isError": r.IsError}
			if m.Call(ctx, "tool_result", in, &out) == nil && out.Content != nil {
				r.Content = *out.Content
			}
			return r, nil
		})
	}
	if m.Has("rule") {
		tools.PreExecute.Use(c, func(ctx context.Context, d *tools.Decision, next kernel.Next[*tools.Decision, *tools.Decision]) (*tools.Decision, error) {
			if d != nil && d.Call != nil {
				var out struct {
					Verdict string `json:"verdict"`
					Reason  string `json:"reason"`
				}
				in := map[string]any{"tool": d.Call.Name, "category": d.Category, "summary": d.Summary, "args": json.RawMessage(orEmpty(d.Call.Args)), "mode": d.Call.Mode}
				if m.Call(ctx, "rule", in, &out) == nil {
					reason := fmt.Sprintf("%s: %s", m.name, strings.TrimSpace(out.Reason))
					// a wasm rule can only tighten
					switch out.Verdict {
					case "deny":
						d.DenyWith(reason)
					case "ask":
						d.RequireApproval(reason)
					}
				}
			}
			return next(ctx, d)
		})
	}
}

func orEmpty(b json.RawMessage) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("{}")
	}
	return b
}
