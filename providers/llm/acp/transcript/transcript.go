// Package transcript reads the session files subscription coding CLIs
// write locally, to show what a CLI really sends its model: the system
// prompt, the tool definitions, the messages and the usage of the last
// main-thread request of one session.
//
// Claude Code writes ~/.claude/projects/<cwd-slug>/<sessionId>.jsonl (see
// claude.go); Codex writes ~/.codex/sessions/YYYY/MM/DD/rollout-<time>-<id>
// .jsonl (see codex.go). The files are only read, never written, and only
// the lines of the asked session are used.
package transcript

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
)

// CLI families whose session files are understood.
const (
	ClaudeCode = "claude-code"
	Codex      = "codex"
)

// BuiltinGroup is the group of a CLI's own tools.
const BuiltinGroup = "built-in"

// ErrNotFound: no session file for the id, or no request in it.
var ErrNotFound = errors.New("transcript: not found")

// Composition is the last main-thread request of a CLI session.
type Composition struct {
	CLI     string    `json:"cli"`
	Session string    `json:"session"` // the CLI's session id
	Path    string    `json:"path"`
	Model   string    `json:"model,omitempty"`
	At      time.Time `json:"at,omitzero"`

	System   []Block       `json:"system"`
	Tools    []Tool        `json:"tools"`
	Messages []llm.Message `json:"messages"`
	// Usage is the provider's count for this request. InputTokens is every
	// input token, cache reads and writes included (llm.Usage).
	Usage llm.Usage `json:"usage"`
	// Counted: Usage came from the CLI's record (not zero / estimated).
	Counted bool `json:"counted"`

	// SystemRecorded / ToolsRecorded: the CLI records these at all. Codex
	// rollouts may lack the base instructions (older versions) and never
	// carry the built-in tool schemas.
	SystemRecorded bool `json:"systemRecorded"`
	ToolsRecorded  bool `json:"toolsRecorded"`
	// Unresolved counts messages the request references whose content the
	// file does not hold (Claude Code blobs not found).
	Unresolved int `json:"unresolved,omitempty"`
	// FromTranscript: messages came from the transcript, not the request
	// record (a Claude Code version that does not write api-request lines).
	FromTranscript bool `json:"fromTranscript,omitempty"`
}

// Block is one system prompt block.
type Block struct {
	Name string `json:"name"` // its first line, shortened
	Text string `json:"text"`
}

// Chars is the block's length in characters.
func (b Block) Chars() int { return len([]rune(b.Text)) }

// Tokens estimates the block's tokens.
func (b Block) Tokens() int { return llm.EstimateTokens(b.Text) }

// Tool is one tool definition sent.
type Tool struct {
	Name        string         `json:"name"`
	Group       string         `json:"group"` // BuiltinGroup or an MCP server
	Description string         `json:"description"`
	Schema      map[string]any `json:"schema,omitempty"`
	// SchemaChars is the size of the definition as sent (JSON).
	SchemaChars int `json:"schemaChars"`
	// Deferred: kept out of the context until the model searches for it
	// (Anthropic defer_loading).
	Deferred bool `json:"deferred,omitempty"`
}

// ToolGroup is the group of a tool name: the server of mcp__<server>__x,
// else BuiltinGroup.
func ToolGroup(name string) string {
	if rest, ok := strings.CutPrefix(name, "mcp__"); ok {
		if i := strings.Index(rest, "__"); i > 0 {
			return rest[:i]
		}
	}
	return BuiltinGroup
}

// blockName is a short label for a system block: its first non-empty line.
func blockName(text string) string {
	for line := range strings.SplitSeq(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			r := []rune(line)
			if len(r) > 60 {
				return string(r[:60]) + "…"
			}
			return line
		}
	}
	return ""
}

// Preview shortens s to n runes for listings.
func Preview(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// validID: session ids are UUID-like; anything else (path separators, glob
// metacharacters) never reaches a glob.
var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{7,127}$`)

// ClaudeDir is Claude Code's config dir: CLAUDE_CONFIG_DIR from env (the
// CLI's own environment first, then the process's), else ~/.claude.
func ClaudeDir(env map[string]string) string {
	return dirOf(env, "CLAUDE_CONFIG_DIR", ".claude")
}

// CodexDir is Codex's home: CODEX_HOME, else ~/.codex.
func CodexDir(env map[string]string) string {
	return dirOf(env, "CODEX_HOME", ".codex")
}

func dirOf(env map[string]string, key, def string) string {
	if v := env[key]; v != "" {
		return v
	}
	if v := os.Getenv(key); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, def)
}

// FindClaude locates the main transcript of Claude Code session id under
// dir: projects/*/<id>.jsonl (sub-agent transcripts live deeper and are not
// matched). The newest match wins.
func FindClaude(dir, id string) (string, error) {
	if dir == "" || !validID.MatchString(id) {
		return "", ErrNotFound
	}
	return newest(filepath.Join(dir, "projects", "*", id+".jsonl"))
}

// FindCodex locates the rollout of Codex thread id under dir:
// sessions/YYYY/MM/DD/rollout-<time>-<id>.jsonl, else archived_sessions.
func FindCodex(dir, id string) (string, error) {
	if dir == "" || !validID.MatchString(id) {
		return "", ErrNotFound
	}
	if p, err := newest(filepath.Join(dir, "sessions", "*", "*", "*", "rollout-*-"+id+".jsonl")); err == nil {
		return p, nil
	}
	return newest(filepath.Join(dir, "archived_sessions", "rollout-*-"+id+".jsonl"))
}

func newest(pattern string) (string, error) {
	ms, _ := filepath.Glob(pattern)
	best, bt := "", time.Time{}
	for _, m := range ms {
		fi, err := os.Stat(m)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if best == "" || fi.ModTime().After(bt) {
			best, bt = m, fi.ModTime()
		}
	}
	if best == "" {
		return "", ErrNotFound
	}
	return best, nil
}
