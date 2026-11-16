package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
)

// Codex's own model list. codex-acp bundles a copy of Codex's core and
// reports the models that copy knows, so an older bridge offers a stale
// list (e.g. codex-acp 0.8 lists gpt-5.2-codex/low… while Codex 0.167 has
// the gpt-6 family). `codex debug models` prints what the installed CLI
// knows:
//
//	{"models":[{"slug","display_name","description","default_reasoning_level",
//	  "supported_reasoning_levels":[{"effort"}],"visibility":"list"|"hide",
//	  "context_window","input_modalities":["text","image"]}, ...]}
//
// Only visibility "list" models are offered (the others are internal).
// Each model's levels become its reasoning efforts; which id is sent for
// a model + effort depends on the bridge (see proc.selectModel):
//
//   - codex-acp < 0.9 (session/set_model): "<slug>/<effort>", parsed by
//     the bridge into model + effort (conversation.rs parse_model_id);
//   - codex-acp >= 0.9 (session config options only): the "model" option
//     takes the slug, the "reasoning_effort" option the effort
//     (thread.rs handle_set_config_option).

// nativeModel is a model from the CLI's own list.
type nativeModel struct {
	ID            string   `json:"id"`
	Name          string   `json:"name,omitempty"`
	Description   string   `json:"description,omitempty"`
	DefaultEffort string   `json:"defaultEffort,omitempty"`
	Efforts       []string `json:"efforts,omitempty"`
	ContextWindow int      `json:"contextWindow,omitempty"`
	Vision        bool     `json:"vision,omitempty"`
}

// nativeTimeout bounds `codex debug models`.
var nativeTimeout = 20 * time.Second

// nativeLister returns how to read the CLI's own model list, nil when it
// has none or is not installed: Codex (`codex debug models`, its models)
// and Cursor (`cursor-agent models`, names for its ACP variants; see
// shape.go). Both only list.
func nativeLister(r resolved) func(context.Context) ([]nativeModel, error) {
	if r.cliPath == "" || len(r.Models) > 0 {
		return nil
	}
	switch r.ID {
	case "codex":
		return listWith(r.cliPath, []string{"debug", "models"}, parseCodexModels)
	case "cursor":
		return listWith(r.cliPath, []string{"models"}, parseCursorModels)
	}
	return nil
}

func listWith(path string, args []string, parse func([]byte) ([]nativeModel, error)) func(context.Context) ([]nativeModel, error) {
	return func(ctx context.Context) ([]nativeModel, error) {
		ctx, cancel := context.WithTimeout(ctx, nativeTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, path, args...)
		setProcessGroup(cmd)
		cmd.WaitDelay = time.Second
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			if msg := bytes.TrimSpace(stderr.Bytes()); len(msg) > 0 {
				return nil, errors.New(string(msg[:min(len(msg), 300)]))
			}
			return nil, err
		}
		return parse(out)
	}
}

// parseCursorModels reads `cursor-agent models`: "id - Name" lines.
func parseCursorModels(b []byte) ([]nativeModel, error) {
	var out []nativeModel
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(strings.Map(func(r rune) rune {
			if r == '\u200b' || r == '\ufeff' {
				return -1
			}
			return r
		}, line))
		id, name, ok := strings.Cut(line, " - ")
		if !ok || id == "" || strings.ContainsAny(id, " \t") {
			continue
		}
		name = strings.Join(strings.Fields(name), " ")
		name = strings.TrimSuffix(name, " (default)")
		out = append(out, nativeModel{ID: id, Name: name})
	}
	if len(out) == 0 {
		return nil, errors.New("cursor-agent models: no model list")
	}
	return out, nil
}

// parseCodexModels reads `codex debug models` output.
func parseCodexModels(b []byte) ([]nativeModel, error) {
	var raw struct {
		Models []struct {
			Slug             string `json:"slug"`
			DisplayName      string `json:"display_name"`
			Description      string `json:"description"`
			DefaultReasoning string `json:"default_reasoning_level"`
			Levels           []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
			Visibility      string   `json:"visibility"`
			ContextWindow   int      `json:"context_window"`
			InputModalities []string `json:"input_modalities"`
		} `json:"models"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	if raw.Models == nil {
		return nil, errors.New("codex debug models: no model list")
	}
	var out []nativeModel
	seen := map[string]bool{}
	for _, m := range raw.Models {
		if m.Slug == "" || m.Visibility != "list" || seen[m.Slug] {
			continue
		}
		seen[m.Slug] = true
		n := nativeModel{ID: m.Slug, Name: m.DisplayName, Description: m.Description, ContextWindow: m.ContextWindow,
			Vision: slices.Contains(m.InputModalities, "image")}
		for _, l := range m.Levels {
			// Crewbase's levels only (Codex's "ultra" has no counterpart)
			if slices.Contains(llm.Efforts, l.Effort) && !slices.Contains(n.Efforts, l.Effort) {
				n.Efforts = append(n.Efforts, l.Effort)
			}
		}
		slices.SortFunc(n.Efforts, func(x, y string) int { return slices.Index(llm.Efforts, x) - slices.Index(llm.Efforts, y) })
		if slices.Contains(n.Efforts, m.DefaultReasoning) {
			n.DefaultEffort = m.DefaultReasoning
		}
		out = append(out, n)
	}
	return out, nil
}
