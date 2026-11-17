package acp

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/providers/llm/acp/transcript"
)

const (
	fixClaudeID = "11111111-2222-4333-8444-555555555555"
	fixCodexID  = "01a1e7ba-0000-7000-8000-000000000001"
)

func recorderService(t *testing.T, state string, enabled *bool) (*Service, *Adapter, *Adapter) {
	t.Helper()
	td, _ := filepath.Abs("transcript/testdata")
	cl := newAdapter("acp-claude-code", resolved{AgentConfig: AgentConfig{ID: "claude-code", CLI: "claude",
		Env: map[string]string{"CLAUDE_CONFIG_DIR": filepath.Join(td, "claude")}}}, Options{StateDir: state})
	cx := newAdapter("acp-codex", resolved{AgentConfig: AgentConfig{ID: "codex", CLI: "codex",
		Env: map[string]string{"CODEX_HOME": filepath.Join(td, "codex")}}}, Options{StateDir: state})
	svc := &Service{cfg: Config{ReadCLITranscripts: enabled}, adapters: map[string]*Adapter{"claude-code": cl, "codex": cx}, reader: transcript.NewReader()}
	return svc, cl, cx
}

var _ agentloop.RequestRecorder = (*Service)(nil)

func TestLastRequestFromClaudeTranscript(t *testing.T) {
	svc, cl, _ := recorderService(t, t.TempDir(), nil)
	if _, ok := svc.LastRequest("ag", "s1"); ok {
		t.Fatal("no mapping yet")
	}
	cl.noteSession("ag", "s1", &sessionState{id: fixClaudeID, cwd: "/tmp/proj"})
	rec, ok := svc.LastRequest("ag", "s1")
	if !ok {
		t.Fatal("not found")
	}
	if len(rec.System) != 2 || len(rec.Tools) != 4 || len(rec.Messages) != 5 {
		t.Fatalf("system %d tools %d messages %d", len(rec.System), len(rec.Tools), len(rec.Messages))
	}
	if u := rec.Usage; u.InputTokens != 5424 || u.CacheRead != 5300 || u.CacheWrite != 120 || u.OutputTokens != 10 {
		t.Fatalf("usage %+v", u)
	}
	// built-ins leave the group to be derived; MCP tools name their server
	if rec.Tools[0].Group != "" || rec.Tools[2].Group != "crewbase" || !rec.Tools[3].Deferred {
		t.Fatalf("tools %+v", rec.Tools)
	}
	// redacted: the token in the system prompt
	if strings.Contains(rec.System[1].Text, "sk-fixture") || !strings.Contains(rec.System[1].Text, "[redacted]") {
		t.Fatalf("system not redacted: %q", rec.System[1].Text)
	}
	// another Crewbase session (or agent) sees nothing
	if _, ok := svc.LastRequest("ag", "s2"); ok {
		t.Fatal("other session surfaced")
	}
	if _, ok := svc.LastRequest("other", "s1"); ok {
		t.Fatal("other agent surfaced")
	}
}

func TestLastRequestLatestSessionWins(t *testing.T) {
	svc, cl, cx := recorderService(t, t.TempDir(), nil)
	cl.noteSession("ag", "s1", &sessionState{id: fixClaudeID})
	cx.noteSession("ag", "s1", &sessionState{id: fixCodexID}) // switched to Codex
	c, err := svc.CLIRequest("ag", "s1")
	if err != nil || c.CLI != transcript.Codex || c.Session != fixCodexID {
		t.Fatalf("%v %+v", err, c)
	}
	rec, ok := svc.LastRequest("ag", "s1")
	if !ok || rec.Usage.InputTokens != 12000 || rec.Usage.CacheRead != 11000 || len(rec.Messages) != 5 {
		t.Fatalf("%v %+v", ok, rec)
	}
	// a new ACP session for the same conversation (compaction, model switch)
	cx.noteSession("ag", "s1", &sessionState{id: "01a1e7ba-0000-7000-8000-00000000dead"})
	if _, ok := svc.LastRequest("ag", "s1"); ok {
		t.Fatal("stale session read after the conversation moved")
	}
}

func TestLastRequestSwitchOff(t *testing.T) {
	off := false
	svc, cl, _ := recorderService(t, t.TempDir(), &off)
	cl.noteSession("ag", "s1", &sessionState{id: fixClaudeID})
	if _, ok := svc.LastRequest("ag", "s1"); ok {
		t.Fatal("read with readCliTranscripts off")
	}
}

func TestCLISessionsPersist(t *testing.T) {
	state := t.TempDir()
	_, cl, _ := recorderService(t, state, nil)
	cl.noteSession("ag", "s1", &sessionState{id: fixClaudeID, cwd: "/tmp/proj"})
	svc2, cl2, _ := recorderService(t, state, nil)
	cl2.loadCLISessions()
	if s, ok := cl2.cliSession("ag", "s1"); !ok || s.ID != fixClaudeID || s.Cwd != "/tmp/proj" {
		t.Fatalf("%+v %v", s, ok)
	}
	if _, ok := svc2.LastRequest("ag", "s1"); !ok {
		t.Fatal("not found after restart")
	}
	// a CLI whose files are not understood records nothing
	o := newAdapter("acp-cursor", resolved{AgentConfig: AgentConfig{ID: "cursor", CLI: "cursor-agent"}}, Options{StateDir: state})
	o.noteSession("ag", "s1", &sessionState{id: fixClaudeID})
	if _, ok := o.cliSession("ag", "s1"); ok {
		t.Fatal("cursor mapping recorded")
	}
}

func TestRecordedRedactsMessages(t *testing.T) {
	tok := "sk-" + strings.Repeat("Z", 30)
	c := &transcript.Composition{Messages: nil}
	raw, _ := json.Marshal(map[string]any{"command": "curl -H 'x: " + tok + "'"})
	c.Messages = append(c.Messages, msgWith(tok, raw))
	rec := Recorded(c)
	b, _ := json.Marshal(rec)
	if strings.Contains(string(b), tok) {
		t.Fatalf("secret left: %s", b)
	}
	if !json.Valid(rec.Messages[0].Parts[1].ToolCall.Args) {
		t.Fatal("args not JSON")
	}
}

func TestStreamRecordsLatestACPSession(t *testing.T) {
	r := fakeResolved(t, nil)
	r.ID = "claude-code" // a CLI whose session files are read
	a := newAdapter("acp-claude-code", r, Options{IdleTimeout: -1})
	t.Cleanup(a.Close)
	req := llm.Request{Agent: "ag", Session: "s1", Messages: []llm.Message{user("hello")}}
	if _, _, err := stream(t, a, req); err != nil {
		t.Fatal(err)
	}
	if s, ok := a.cliSession("ag", "s1"); !ok || s.ID != "s1" || s.Cwd != r.Cwd {
		t.Fatalf("%+v %v", s, ok)
	}
	// history rewritten: a new ACP session for the same conversation
	req.Messages = []llm.Message{user("hello"), llm.TextMessage(llm.RoleAssistant, "summary"), user("next")}
	if _, _, err := stream(t, a, req); err != nil {
		t.Fatal(err)
	}
	if s, _ := a.cliSession("ag", "s1"); s.ID != "s2" {
		t.Fatalf("latest ACP session not kept: %+v", s)
	}
	// requests without agent + session are not mapped
	if _, _, err := stream(t, a, llm.Request{Messages: []llm.Message{user("x")}}); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	n := len(a.cliSessions)
	a.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d mappings", n)
	}
}

func msgWith(secret string, args json.RawMessage) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{
		{Type: "text", Text: "key " + secret},
		{Type: "tool_call", ToolCall: &llm.ToolCall{ID: "1", Name: "Bash", Args: args}},
		{Type: "tool_result", ToolResult: &llm.ToolResult{CallID: "1", Content: "echo " + secret}},
	}}
}
