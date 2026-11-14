package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hedeqiang/crewbase/core/tools"
)

func (s *Store) registerTools(reg *tools.Registry) {
	reg.Register(s.c, &tools.Tool{
		Name:        "memory_add",
		Description: "Save a durable fact, preference or lesson to your long-term memory (MEMORY.md). It is shown to you in every future conversation; keep entries short and self-contained.",
		Category:    tools.CategoryWrite,
		Params:      tools.Schema(map[string]any{"text": tools.Prop("string", "the fact to remember")}, "text"),
		Classify: func(c tools.Call) (string, string) {
			return "", "remember: " + truncate(c.String("text"), 120)
		},
		Execute: func(_ context.Context, c tools.Call) (tools.Result, error) {
			line, err := s.Add(c.Agent, c.String("text"))
			if err != nil {
				return tools.Errorf("memory_add: %v", err), nil
			}
			return tools.Text("Saved to MEMORY.md: " + line), nil
		},
	})
	reg.Register(s.c, &tools.Tool{
		Name:        "memory_search",
		Description: "Search your long-term memory (MEMORY.md entries).",
		Category:    tools.CategoryRead,
		Params: tools.Schema(map[string]any{
			"query": tools.Prop("string", "words to look for"),
			"limit": tools.Prop("integer", "max results (default 10)"),
		}, "query"),
		Execute: func(_ context.Context, c tools.Call) (tools.Result, error) {
			var a struct {
				Query string `json:"query"`
				Limit int    `json:"limit"`
			}
			_ = c.Decode(&a)
			hits, err := s.SearchMemory(c.Agent, a.Query, a.Limit)
			if err != nil {
				return tools.Errorf("memory_search: %v", err), nil
			}
			if len(hits) == 0 {
				return tools.Text("No memory entries match."), nil
			}
			var b strings.Builder
			for _, h := range hits {
				fmt.Fprintf(&b, "MEMORY.md:%d %s\n", h.Seq, h.Snippet)
			}
			return tools.Result{Content: b.String(), Data: hits}, nil
		},
	})
	reg.Register(s.c, &tools.Tool{
		Name: "history_search",
		Description: "Full-text search over your past conversations, including parts that were summarised away. " +
			"Returns session ids and event seq numbers with snippets; read the surrounding messages with history_read.",
		Category: tools.CategoryRead,
		Params: tools.Schema(map[string]any{
			"query":   tools.Prop("string", "words to look for"),
			"session": tools.Prop("string", "restrict to one session id (optional)"),
			"limit":   tools.Prop("integer", "max results (default 10, max 50)"),
		}, "query"),
		Execute: func(_ context.Context, c tools.Call) (tools.Result, error) {
			var a struct {
				Query   string `json:"query"`
				Session string `json:"session"`
				Limit   int    `json:"limit"`
			}
			_ = c.Decode(&a)
			hits, err := s.SearchHistory(c.Agent, a.Query, a.Session, a.Limit)
			if err != nil {
				return tools.Errorf("history_search: %v", err), nil
			}
			if len(hits) == 0 {
				return tools.Text("No matches in your conversation history."), nil
			}
			var b strings.Builder
			for _, h := range hits {
				fmt.Fprintf(&b, "session=%s seq=%d [%s] %s\n", h.Session, h.Seq, h.Role, h.Snippet)
			}
			return tools.Result{Content: b.String(), Data: hits}, nil
		},
	})
	reg.Register(s.c, &tools.Tool{
		Name:        "history_read",
		Description: "Read messages of one of your conversations by event seq range (from history_search results). Output is size-capped; continue with a later 'from' if truncated.",
		Category:    tools.CategoryRead,
		Params: tools.Schema(map[string]any{
			"session":  tools.Prop("string", "session id (default: the current conversation)"),
			"from":     tools.Prop("integer", "first event seq"),
			"to":       tools.Prop("integer", "last event seq (default from+30)"),
			"maxChars": tools.Prop("integer", "output cap in characters"),
		}, "from"),
		Execute: func(_ context.Context, c tools.Call) (tools.Result, error) {
			var a struct {
				Session  string      `json:"session"`
				From     json.Number `json:"from"`
				To       json.Number `json:"to"`
				MaxChars int         `json:"maxChars"`
			}
			if err := c.Decode(&a); err != nil {
				return tools.Errorf("history_read: %v", err), nil
			}
			if a.Session == "" && c.Session != nil {
				a.Session = c.Session.ID()
			}
			from, _ := a.From.Int64()
			to, _ := a.To.Int64()
			out, err := s.Read(c.Agent, a.Session, from, to, a.MaxChars)
			if err != nil {
				return tools.Errorf("history_read: %v", err), nil
			}
			return tools.Text(out), nil
		},
	})
}
