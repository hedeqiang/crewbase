package transcript

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestRealFiles reads the person's latest Claude Code transcript and Codex
// rollout, read-only, and prints only counts and sizes (never content).
// Skipped unless CREWBASE_REAL_TRANSCRIPTS=1. CREWBASE_REAL_CLAUDE /
// CREWBASE_REAL_CODEX pick a file instead of the newest.
func TestRealFiles(t *testing.T) {
	if os.Getenv("CREWBASE_REAL_TRANSCRIPTS") != "1" {
		t.Skip("set CREWBASE_REAL_TRANSCRIPTS=1 to read the local CLI session files (read-only)")
	}
	r := NewReader()
	cpath := os.Getenv("CREWBASE_REAL_CLAUDE")
	if cpath == "" {
		cpath = newestFile(t, filepath.Join(ClaudeDir(nil), "projects", "*", "*.jsonl"))
	}
	if cpath != "" {
		id := strings.TrimSuffix(filepath.Base(cpath), ".jsonl")
		fi, _ := os.Stat(cpath)
		start := time.Now()
		c, err := r.Claude(cpath, id)
		if err != nil {
			t.Fatalf("claude: %v", err)
		}
		first := time.Since(start)
		start = time.Now()
		if _, err := r.Claude(cpath, id); err != nil {
			t.Fatal(err)
		}
		again := time.Since(start)
		report(t, "claude-code", fi.Size(), c, first, again)
	}
	xpath := os.Getenv("CREWBASE_REAL_CODEX")
	if xpath == "" {
		xpath = newestFile(t, filepath.Join(CodexDir(nil), "sessions", "*", "*", "*", "rollout-*.jsonl"))
	}
	if xpath != "" {
		base := strings.TrimSuffix(filepath.Base(xpath), ".jsonl")
		id := base[len(base)-36:]
		fi, _ := os.Stat(xpath)
		start := time.Now()
		c, err := r.Codex(xpath, id)
		if err != nil {
			t.Fatalf("codex: %v", err)
		}
		report(t, "codex", fi.Size(), c, time.Since(start), 0)
	}
}

func report(t *testing.T, cli string, size int64, c *Composition, first, again time.Duration) {
	sysChars := 0
	var blocks []int
	for _, b := range c.System {
		sysChars += b.Chars()
		blocks = append(blocks, b.Chars())
	}
	builtin, mcp, deferred, schema := 0, 0, 0, 0
	servers := map[string]int{}
	for _, tl := range c.Tools {
		schema += tl.SchemaChars
		if tl.Deferred {
			deferred++
		}
		if tl.Group == BuiltinGroup {
			builtin++
		} else {
			mcp++
			servers[tl.Group]++
		}
	}
	roles := map[string]int{}
	calls, results, msgChars := 0, 0, 0
	for _, m := range c.Messages {
		roles[string(m.Role)]++
		for _, p := range m.Parts {
			msgChars += len(p.Text)
			if p.ToolCall != nil {
				calls++
				msgChars += len(p.ToolCall.Args)
			}
			if p.ToolResult != nil {
				results++
				msgChars += len(p.ToolResult.Content)
			}
		}
	}
	u := c.Usage
	hit := 0.0
	if u.InputTokens > 0 {
		hit = float64(u.CacheRead) * 100 / float64(u.InputTokens)
	}
	t.Logf("%s: file %d bytes, parse %v (cached re-read %v)", cli, size, first.Round(time.Millisecond), again.Round(time.Microsecond))
	t.Logf("  system recorded=%v blocks=%d chars=%d per-block=%v", c.SystemRecorded, len(c.System), sysChars, blocks)
	t.Logf("  tools recorded=%v count=%d built-in=%d mcp=%d (servers %d) deferred=%d schema chars=%d", c.ToolsRecorded, len(c.Tools), builtin, mcp, len(servers), deferred, schema)
	t.Logf("  messages=%d roles=%v tool calls=%d results=%d chars=%d unresolved=%d fromTranscript=%v", len(c.Messages), roles, calls, results, msgChars, c.Unresolved, c.FromTranscript)
	t.Logf("  usage counted=%v input=%d (cache read %d, write %d, uncached %d) output=%d cache hit=%.1f%%", c.Counted, u.InputTokens, u.CacheRead, u.CacheWrite, u.InputTokens-u.CacheRead-u.CacheWrite, u.OutputTokens, hit)
}

func newestFile(t *testing.T, pattern string) string {
	ms, _ := filepath.Glob(pattern)
	sort.Slice(ms, func(i, j int) bool {
		a, _ := os.Stat(ms[i])
		b, _ := os.Stat(ms[j])
		return a.ModTime().After(b.ModTime())
	})
	if len(ms) == 0 {
		t.Logf("no file matches %s", pattern)
		return ""
	}
	return ms[0]
}
