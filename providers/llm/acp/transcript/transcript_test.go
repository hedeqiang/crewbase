package transcript

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/core/llm"
)

// Fixtures are synthetic (testdata/gen.py writes them, computing message
// hashes independently of this package).
const (
	claudeID = "11111111-2222-4333-8444-555555555555"
	legacyID = "22222222-2222-4333-8444-555555555555"
	codexID  = "01a1e7ba-0000-7000-8000-000000000001"
	oldID    = "01a1e7ba-0000-7000-8000-000000000002"
)

func claudeFixture(t *testing.T) string {
	t.Helper()
	p, err := FindClaude("testdata/claude", claudeID)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFindByID(t *testing.T) {
	p := claudeFixture(t)
	if filepath.Base(p) != claudeID+".jsonl" || strings.Contains(p, "subagents") {
		t.Fatalf("claude: %s", p)
	}
	for _, bad := range []string{"", "../x", "*", "a/b", "11111111-*"} {
		if _, err := FindClaude("testdata/claude", bad); err == nil {
			t.Errorf("FindClaude(%q) found a file", bad)
		}
	}
	if _, err := FindClaude("testdata/claude", "33333333-2222-4333-8444-555555555555"); err != ErrNotFound {
		t.Errorf("unknown id: %v", err)
	}
	x, err := FindCodex("testdata/codex", codexID)
	if err != nil || !strings.HasSuffix(x, codexID+".jsonl") || !strings.Contains(x, "sessions/2026/11/17") {
		t.Fatalf("codex: %s %v", x, err)
	}
	a, err := FindCodex("testdata/codex", oldID)
	if err != nil || !strings.Contains(a, "archived_sessions") {
		t.Fatalf("archived codex: %s %v", a, err)
	}
}

func TestDirsFromEnv(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/from/process")
	if d := ClaudeDir(map[string]string{"CLAUDE_CONFIG_DIR": "/from/cli"}); d != "/from/cli" {
		t.Fatal(d)
	}
	if d := ClaudeDir(nil); d != "/from/process" {
		t.Fatal(d)
	}
	t.Setenv("CODEX_HOME", "")
	if d := CodexDir(nil); !strings.HasSuffix(d, ".codex") {
		t.Fatal(d)
	}
}

func TestClaudeShape(t *testing.T) {
	c, err := NewReader().Claude(claudeFixture(t), claudeID)
	if err != nil {
		t.Fatal(err)
	}
	if !c.SystemRecorded || len(c.System) != 2 || c.System[0].Name != "You are Claude Code, Anthropic's official CLI for Claude." {
		t.Fatalf("system: %+v", c.System)
	}
	if c.System[1].Name != "# Environment" || c.System[1].Tokens() == 0 || c.System[1].Chars() != len([]rune(c.System[1].Text)) {
		t.Fatalf("block 2: %+v", c.System[1])
	}
	if strings.Contains(c.System[0].Text+c.System[1].Text, "OTHER SESSION") {
		t.Fatal("another session's shape was read")
	}
	want := []struct {
		name, group string
		deferred    bool
	}{{"Bash", BuiltinGroup, false}, {"Read", BuiltinGroup, false}, {"mcp__crewbase__computer_screenshot", "crewbase", false}, {"mcp__github__create_issue", "github", true}}
	if len(c.Tools) != len(want) {
		t.Fatalf("tools: %+v", c.Tools)
	}
	for i, w := range want {
		tl := c.Tools[i]
		if tl.Name != w.name || tl.Group != w.group || tl.Deferred != w.deferred || tl.SchemaChars == 0 || tl.Schema["type"] != "object" {
			t.Errorf("tool %d: %+v", i, tl)
		}
	}
}

func TestClaudeLastMainRequest(t *testing.T) {
	c, err := NewReader().Claude(claudeFixture(t), claudeID)
	if err != nil {
		t.Fatal(err)
	}
	// r4 (the side "compact" request, the partial last line and the other
	// session's r9 are skipped)
	if c.Model != "claude-opus-4-5" || c.At.IsZero() {
		t.Fatalf("model %q at %v", c.Model, c.At)
	}
	// usage of r4: input = uncached + cache read + cache write
	u := c.Usage
	if !c.Counted || u.InputTokens != 4+5300+120 || u.CacheRead != 5300 || u.CacheWrite != 120 || u.OutputTokens != 10 {
		t.Fatalf("usage: %+v", u)
	}
	// messages: base r2 kept 3 (u1, the merged reply of r1, the tool
	// result), then r2's reply and a blob; one hash names nothing
	if len(c.Messages) != 5 || c.Unresolved != 1 || c.FromTranscript {
		t.Fatalf("messages %d unresolved %d: %+v", len(c.Messages), c.Unresolved, c.Messages)
	}
	m := c.Messages
	if m[0].Role != llm.RoleUser || m[0].Text() != "Fix the bug in main.go" {
		t.Errorf("m0: %+v", m[0])
	}
	if m[1].Role != llm.RoleAssistant || len(m[1].Parts) != 2 || m[1].Parts[0].Type != "thinking" || m[1].Parts[1].ToolCall == nil ||
		m[1].Parts[1].ToolCall.Name != "Read" || string(m[1].Parts[1].ToolCall.Args) != `{"file_path":"main.go"}` {
		t.Errorf("m1 (merged reply): %+v", m[1])
	}
	if m[2].Role != llm.RoleTool || m[2].Parts[0].ToolResult == nil || m[2].Parts[0].ToolResult.CallID != "toolu_1" ||
		!strings.Contains(m[2].Parts[0].ToolResult.Content, "<script>") {
		t.Errorf("m2 (tool result): %+v", m[2])
	}
	if m[3].Text() != "Found it. 修复完成 ✓" {
		t.Errorf("m3: %+v", m[3])
	}
	if m[4].Role != llm.RoleUser || !strings.Contains(m[4].Text(), "system-reminder") || !strings.Contains(m[4].Text(), " ") {
		t.Errorf("m4 (system blob): %+v", m[4])
	}
}

func TestClaudeLegacyTranscript(t *testing.T) {
	p, err := FindClaude("testdata/claude", legacyID)
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewReader().Claude(p, legacyID)
	if err != nil {
		t.Fatal(err)
	}
	if !c.FromTranscript || c.SystemRecorded || len(c.Messages) != 2 || c.Messages[1].Text() != "Hi there" {
		t.Fatalf("%+v", c)
	}
	if !c.Counted || c.Usage.InputTokens != 10 || c.Usage.OutputTokens != 3 || c.Model != "claude-sonnet-4-5" {
		t.Fatalf("usage %+v model %s", c.Usage, c.Model)
	}
}

func TestMessageHashMatchesFixture(t *testing.T) {
	// the hash of u1 as gen.py computed it (Python json.dumps, sort_keys)
	h, err := messageHash("user", json.RawMessage(`"Fix the bug in main.go"`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(claudeFixture(t))
	if !strings.Contains(string(b), `"tail":["`+h+`"]`) {
		t.Fatalf("hash %s not in r1's tail", h)
	}
	// escaping: only ", \ and control characters; HTML and non-ASCII raw
	h1, _ := messageHash("user", json.RawMessage(`"a<b>&é \u0001"`))
	h2, _ := messageHash("user", json.RawMessage("\"a<b>&é \\u0001\""))
	if h1 != h2 {
		t.Fatal("same content, different hash")
	}
}

func TestCodexRollout(t *testing.T) {
	p, _ := FindCodex("testdata/codex", codexID)
	c, err := NewReader().Codex(p, codexID)
	if err != nil {
		t.Fatal(err)
	}
	if !c.SystemRecorded || len(c.System) != 1 || !strings.HasPrefix(c.System[0].Text, "You are Codex") {
		t.Fatalf("system: %+v", c.System)
	}
	if c.ToolsRecorded || len(c.Tools) != 1 || c.Tools[0].Name != "computer_screenshot" || c.Tools[0].Group != "mcp__crewbase__" {
		t.Fatalf("tools: %+v", c.Tools)
	}
	if c.Model != "gpt-5.1-codex" {
		t.Fatalf("model %q", c.Model)
	}
	// usage of the last token_count with info (a later null one is skipped)
	u := c.Usage
	if !c.Counted || u.InputTokens != 12000 || u.CacheRead != 11000 || u.OutputTokens != 30 {
		t.Fatalf("usage: %+v", u)
	}
	// history since the compaction: summary, user, patch call + output, reply
	if len(c.Messages) != 5 {
		t.Fatalf("messages: %+v", c.Messages)
	}
	m := c.Messages
	if !strings.HasPrefix(m[0].Text(), "Summary:") || m[1].Text() != "now delete b.go" {
		t.Errorf("m0/m1: %+v %+v", m[0], m[1])
	}
	if tc := m[2].Parts[0].ToolCall; tc == nil || tc.Name != "apply_patch" || !json.Valid(tc.Args) {
		t.Errorf("m2: %+v", m[2])
	}
	if tr := m[3].Parts[0].ToolResult; m[3].Role != llm.RoleTool || tr == nil || tr.Content != "Done!" {
		t.Errorf("m3: %+v", m[3])
	}
	if m[4].Role != llm.RoleAssistant || m[4].Text() != "Deleted b.go." {
		t.Errorf("m4: %+v", m[4])
	}
	// the thread id must match the file's
	if _, err := NewReader().Codex(p, oldID); err != ErrNotFound {
		t.Errorf("wrong id: %v", err)
	}
}

func TestCodexOlderRollout(t *testing.T) {
	p, _ := FindCodex("testdata/codex", oldID)
	c, err := NewReader().Codex(p, oldID)
	if err != nil {
		t.Fatal(err)
	}
	if c.SystemRecorded || len(c.System) != 0 || len(c.Messages) != 2 || c.Usage.InputTokens != 6997 || c.Usage.CacheRead != 5888 {
		t.Fatalf("%+v", c)
	}
}

// bigTranscript writes n chained main requests (each adds one user blob).
func bigTranscript(t *testing.T, path, id string, from, n int, pad int) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := func(v any) {
		b, _ := json.Marshal(v)
		f.Write(append(b, '\n'))
	}
	if from == 0 {
		enc(map[string]any{"type": "api-request-shape", "sessionId": id, "shapeHash": "s", "shape": map[string]any{"system": []any{map[string]any{"type": "text", "text": "sys"}}, "tools": []any{}}})
	}
	for i := from; i < from+n; i++ {
		text := fmt.Sprintf("message %d %s", i, strings.Repeat("x", pad))
		raw, _ := json.Marshal(text)
		h, _ := messageHash("user", raw)
		enc(map[string]any{"type": "api-request-blob", "sessionId": id, "hash": h, "message": map[string]any{"role": "user", "content": text}})
		q := map[string]any{"type": "api-request", "sessionId": id, "id": fmt.Sprint("r", i), "querySource": "sdk", "shapeHash": "s", "params": map[string]any{"model": "m"}, "tail": []string{h}}
		if i > 0 {
			q["base"], q["keep"] = fmt.Sprint("r", i-1), i
		}
		enc(q)
		enc(map[string]any{"type": "assistant", "sessionId": id, "requestRef": fmt.Sprint("r", i), "message": map[string]any{"id": fmt.Sprint("m", i), "role": "assistant", "content": []any{}, "usage": map[string]any{"input_tokens": 1, "cache_read_input_tokens": i, "output_tokens": 1}}})
	}
}

func TestIncrementalAppend(t *testing.T) {
	id := "44444444-2222-4333-8444-555555555555"
	p := filepath.Join(t.TempDir(), id+".jsonl")
	bigTranscript(t, p, id, 0, 3, 0)
	r := NewReader()
	c, err := r.Claude(p, id)
	if err != nil || len(c.Messages) != 3 || c.Usage.CacheRead != 2 {
		t.Fatalf("%v %+v", err, c)
	}
	bigTranscript(t, p, id, 3, 2, 0)
	fs := r.state("claude\x00" + p + "\x00" + id)
	before := fs.off
	c, err = r.Claude(p, id)
	if err != nil || len(c.Messages) != 5 || c.Usage.CacheRead != 4 || fs.off <= before {
		t.Fatalf("after append: %v %+v", err, c)
	}
	// replaced by a shorter file: parsed again from the start
	os.Remove(p)
	bigTranscript(t, p, id, 0, 1, 0)
	c, err = r.Claude(p, id)
	if err != nil || len(c.Messages) != 1 {
		t.Fatalf("after replace: %v %+v", err, c)
	}
}

func TestLargeFileWindowAndLongLines(t *testing.T) {
	id := "55555555-2222-4333-8444-555555555555"
	p := filepath.Join(t.TempDir(), id+".jsonl")
	bigTranscript(t, p, id, 0, 200, 200)
	fi, _ := os.Stat(p)
	// only the last 10 KB are parsed: the chain reaches before the window,
	// so messages fall back to the transcript lines in it; usage stays
	r := &Reader{MaxBytes: 10 << 10}
	c, err := r.Claude(p, id)
	if err != nil {
		t.Fatal(err)
	}
	if fs := r.state("claude\x00" + p + "\x00" + id); fs.start != fi.Size()-10<<10 {
		t.Fatalf("window start %d of %d", fs.start, fi.Size())
	}
	if c.Usage.CacheRead != 199 || c.SystemRecorded {
		t.Fatalf("usage %+v system %v", c.Usage, c.SystemRecorded)
	}
	// a line over MaxLine is skipped, the ones after it are read
	r2 := &Reader{MaxLine: 300}
	q := filepath.Join(t.TempDir(), id+".jsonl")
	bigTranscript(t, q, id, 0, 1, 1000) // its blob line is too long
	c, err = r2.Claude(q, id)
	if err != nil || c.Unresolved != 1 || len(c.Messages) != 0 || c.Usage.InputTokens != 1 {
		t.Fatalf("%v %+v", err, c)
	}
}
