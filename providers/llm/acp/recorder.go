package acp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/features/redact"
	"github.com/hedeqiang/crewbase/providers/llm/acp/transcript"
)

// A Crewbase conversation runs in an ACP session; the CLIs name their
// session files by that id:
//
//   - claude-agent-acp passes its ACP sessionId to Claude Code as
//     --session-id, so the transcript is <CLAUDE_CONFIG_DIR>/projects/<cwd
//     slug>/<sessionId>.jsonl (bridge source; checked end to end);
//   - codex-acp's ACP sessionId is the Codex thread id, the suffix of
//     <CODEX_HOME>/sessions/YYYY/MM/DD/rollout-<time>-<id>.jsonl (checked by
//     a session/new against the bridge).
//
// The adapter records the latest ACP session of every agent + Crewbase
// session (a conversation that moved to a new ACP session — model switch,
// compaction, quarantine — keeps the newest), and the Service reads that
// session's file (agentloop.RequestRecorder).

// cliSession is the ACP session a Crewbase conversation ran in last.
type cliSession struct {
	ID  string    `json:"id"`
	Cwd string    `json:"cwd"`
	At  time.Time `json:"at"`
}

// maxCLISessions bounds the recorded mapping per adapter.
const maxCLISessions = 512

const cliSessionsFile = "acp-cli-sessions.json"

var cliSessionsMu sync.Mutex

func sessKey(agent, session string) string { return agent + "\x00" + session }

// family is the CLI whose session files the adapter's agent writes ("" =
// none understood).
func family(r resolved) string {
	cli := strings.TrimSuffix(filepath.Base(r.CLI), ".exe")
	switch {
	case r.ID == "claude-code" || cli == "claude":
		return transcript.ClaudeCode
	case r.ID == "codex" || cli == "codex":
		return transcript.Codex
	}
	return ""
}

// noteSession records that agent's Crewbase session runs in ACP session
// s (called on every turn).
func (a *Adapter) noteSession(agent, session string, s *sessionState) {
	if agent == "" || session == "" || s == nil || family(a.cfg) == "" {
		return
	}
	a.mu.Lock()
	if a.cliSessions == nil {
		a.cliSessions = map[string]cliSession{}
	}
	k := sessKey(agent, session)
	prev, had := a.cliSessions[k]
	a.cliSessions[k] = cliSession{ID: s.id, Cwd: s.cwd, At: time.Now().UTC()}
	if len(a.cliSessions) > maxCLISessions {
		var oldK string
		var oldT time.Time
		for k, v := range a.cliSessions {
			if oldK == "" || v.At.Before(oldT) {
				oldK, oldT = k, v.At
			}
		}
		delete(a.cliSessions, oldK)
	}
	changed := !had || prev.ID != s.id || prev.Cwd != s.cwd
	var snap map[string]cliSession
	if changed && a.stateDir != "" {
		snap = make(map[string]cliSession, len(a.cliSessions))
		for k, v := range a.cliSessions {
			snap[k] = v
		}
	}
	a.mu.Unlock()
	if snap != nil {
		writeCLISessions(a.stateDir, a.cfg.ID, snap)
	}
}

// cliSession is the ACP session agent's Crewbase session ran in last.
func (a *Adapter) cliSession(agent, session string) (cliSession, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.cliSessions[sessKey(agent, session)]
	return s, ok
}

// loadCLISessions restores the mapping kept in the state dir.
func (a *Adapter) loadCLISessions() {
	m := readCLISessions(a.stateDir)[a.cfg.ID]
	if len(m) == 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cliSessions == nil {
		a.cliSessions = map[string]cliSession{}
	}
	for k, v := range m {
		if cur, ok := a.cliSessions[k]; !ok || v.At.After(cur.At) {
			a.cliSessions[k] = v
		}
	}
}

// the file: {adapter config id: {"agent\x00session": cliSession}}
func readCLISessions(dir string) map[string]map[string]cliSession {
	out := map[string]map[string]cliSession{}
	if dir == "" {
		return out
	}
	b, err := os.ReadFile(filepath.Join(dir, cliSessionsFile))
	if err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

func writeCLISessions(dir, id string, m map[string]cliSession) {
	cliSessionsMu.Lock()
	defer cliSessionsMu.Unlock()
	all := readCLISessions(dir)
	all[id] = m
	b, err := json.Marshal(all)
	if err != nil {
		return
	}
	tmp := filepath.Join(dir, cliSessionsFile+".tmp")
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, filepath.Join(dir, cliSessionsFile))
	}
}

// CLIRequest is what the CLI behind agent's Crewbase session last sent:
// the composition read from its session file (not redacted) and the ACP
// session it came from.
func (s *Service) CLIRequest(agent, session string) (*transcript.Composition, error) {
	type cand struct {
		a  *Adapter
		cs cliSession
	}
	var cs []cand
	s.mu.Lock()
	for _, a := range s.adapters {
		if family(a.cfg) == "" {
			continue
		}
		if x, ok := a.cliSession(agent, session); ok {
			cs = append(cs, cand{a, x})
		}
	}
	s.mu.Unlock()
	if len(cs) == 0 {
		return nil, transcript.ErrNotFound
	}
	// the conversation may have moved between CLIs: the latest wins
	sort.Slice(cs, func(i, j int) bool { return cs[i].cs.At.After(cs[j].cs.At) })
	c := cs[0]
	env := c.a.cfg.Env
	switch family(c.a.cfg) {
	case transcript.ClaudeCode:
		p, err := transcript.FindClaude(transcript.ClaudeDir(env), c.cs.ID)
		if err != nil {
			return nil, err
		}
		return s.reader.Claude(p, c.cs.ID)
	case transcript.Codex:
		p, err := transcript.FindCodex(transcript.CodexDir(env), c.cs.ID)
		if err != nil {
			return nil, err
		}
		return s.reader.Codex(p, c.cs.ID)
	}
	return nil, transcript.ErrNotFound
}

// LastRequest implements agentloop.RequestRecorder from the CLI's session
// file. Everything in it is redacted.
func (s *Service) LastRequest(agent, session string) (*agentloop.RecordedRequest, bool) {
	if !s.readTranscripts() || agent == "" || session == "" {
		return nil, false
	}
	c, err := s.CLIRequest(agent, session)
	if err != nil || c == nil || (len(c.System) == 0 && len(c.Tools) == 0 && len(c.Messages) == 0) {
		return nil, false
	}
	return Recorded(c), true
}

func (s *Service) readTranscripts() bool {
	return s.cfg.ReadCLITranscripts == nil || *s.cfg.ReadCLITranscripts
}

// Recorded converts a composition, redacting every text in it.
func Recorded(c *transcript.Composition) *agentloop.RecordedRequest {
	out := &agentloop.RecordedRequest{Usage: c.Usage}
	for _, b := range c.System {
		out.System = append(out.System, agentloop.RecordedBlock{Name: redact.Redact(b.Name), Text: redact.Redact(b.Text)})
	}
	for _, t := range c.Tools {
		g := t.Group
		if g == transcript.BuiltinGroup {
			g = "" // derived from the name
		}
		out.Tools = append(out.Tools, llm.ToolSchema{Name: t.Name, Description: redact.Redact(t.Description), Parameters: redactSchema(t.Schema), Deferred: t.Deferred, Group: g})
	}
	for _, m := range c.Messages {
		rm := llm.Message{Role: m.Role, Parts: make([]llm.Part, 0, len(m.Parts))}
		for _, p := range m.Parts {
			p.Text = redact.Redact(p.Text)
			p.Meta = nil
			if p.ToolCall != nil {
				tc := *p.ToolCall
				tc.Args = redactJSON(tc.Args)
				p.ToolCall = &tc
			}
			if p.ToolResult != nil {
				tr := *p.ToolResult
				tr.Content = redact.Redact(tr.Content)
				p.ToolResult = &tr
			}
			rm.Parts = append(rm.Parts, p)
		}
		out.Messages = append(out.Messages, rm)
	}
	return out
}

// redactJSON redacts the strings of a JSON value (kept valid JSON).
func redactJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		b, _ := json.Marshal(redact.Redact(string(raw)))
		return b
	}
	b, err := json.Marshal(redactValue(v))
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}

func redactSchema(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	v, _ := redactValue(m).(map[string]any)
	return v
}

func redactValue(v any) any {
	switch x := v.(type) {
	case string:
		return redact.Redact(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = redactValue(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = redactValue(e)
		}
		return out
	}
	return v
}
