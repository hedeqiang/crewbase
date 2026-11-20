package library

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/kernel"
)

const summaryPrompt = `You index documents for search. Read the document and answer with JSON only, no prose:
{"summary": "...", "keywords": ["...", "..."]}
summary: two or three plain sentences saying what the document is about and what it is for, in the document's own language.
keywords: 8 to 15 short search terms someone might type to find it: its topics, names, and synonyms of them, in both Chinese and English.`

// summarizer writes summaries with the model the router gives the
// summarize task (a cheap one by default).
func summarizer(c *kernel.Context) Summarizer {
	return func(ctx context.Context, name, text string) (string, []string, error) {
		rt, ok := kernel.Lookup[agentloop.Router](c, agentloop.RouterKey)
		if !ok {
			return "", nil, errors.New("no model router")
		}
		r, ok := rt.(*router.Router)
		if !ok {
			return "", nil, errors.New("no model router")
		}
		ref, ok := r.RouteFor(router.TaskSummarize)
		if !ok {
			return "", nil, errors.New("no model for summaries")
		}
		llms, ok := kernel.Lookup[*llm.Registry](c, llm.ServiceKey)
		if !ok {
			return "", nil, errors.New("no models")
		}
		ad, ok := llms.Get(ref.Provider)
		if !ok {
			return "", nil, errors.New("the summary model's provider is not connected")
		}
		resp, err := ad.Stream(ctx, llm.Request{Model: ref.Model, Effort: "minimal", MaxTokens: 400, System: summaryPrompt,
			Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "Document: "+name+"\n\n"+text)}},
			func(llm.Chunk) error { return nil })
		if err != nil {
			return "", nil, err
		}
		return parseSummary(resp.Message.Text())
	}
}

// parseSummary reads the model's JSON (inside a code fence or prose too).
func parseSummary(out string) (string, []string, error) {
	i, j := strings.Index(out, "{"), strings.LastIndex(out, "}")
	if i < 0 || j <= i {
		return "", nil, errors.New("no JSON in the answer")
	}
	var v struct {
		Summary  string   `json:"summary"`
		Keywords []string `json:"keywords"`
	}
	if err := json.Unmarshal([]byte(out[i:j+1]), &v); err != nil {
		return "", nil, err
	}
	return strings.TrimSpace(v.Summary), v.Keywords, nil
}
