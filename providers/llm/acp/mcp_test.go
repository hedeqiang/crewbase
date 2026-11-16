//go:build !windows

package acp

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/llm"
)

const fakeMCPToken = "tok-0123456789abcdef"

type fakeGrant struct {
	b       MCPBinding
	allowed map[string]bool

	mu      sync.Mutex
	revoked int
	turns   []context.Context
	modes   []string
	ended   int
	stale   bool
}

func (g *fakeGrant) Server() (string, string, string) {
	return "crewbase", "http://127.0.0.1:1/mcp", fakeMCPToken
}
func (g *fakeGrant) Turn(ctx context.Context, mode string) func() {
	g.mu.Lock()
	g.turns = append(g.turns, ctx)
	g.modes = append(g.modes, mode)
	g.mu.Unlock()
	return func() { g.mu.Lock(); g.ended++; g.mu.Unlock() }
}
func (g *fakeGrant) Allows(tool string) bool { return g.allowed[tool] }
func (g *fakeGrant) Stale() bool             { g.mu.Lock(); defer g.mu.Unlock(); return g.stale }
func (g *fakeGrant) Revoke()                 { g.mu.Lock(); g.revoked++; g.mu.Unlock() }
func (g *fakeGrant) isRevoked() bool         { g.mu.Lock(); defer g.mu.Unlock(); return g.revoked > 0 }

type granter struct {
	mu     sync.Mutex
	grants []*fakeGrant
}

func (gr *granter) open(b MCPBinding) MCPGrant {
	g := &fakeGrant{b: b, allowed: map[string]bool{"computer_act": true, "browser_open": true}}
	gr.mu.Lock()
	gr.grants = append(gr.grants, g)
	gr.mu.Unlock()
	return g
}

func (gr *granter) all() []*fakeGrant {
	gr.mu.Lock()
	defer gr.mu.Unlock()
	return append([]*fakeGrant(nil), gr.grants...)
}

var httpMCPEnv = map[string]string{"FAKE_ACP_HTTP_MCP": "1"}

func agentReq(text string) llm.Request {
	return llm.Request{Messages: []llm.Message{user(text)}, Agent: "lead", Session: "S1", Workspace: "", Mode: "sandbox"}
}

func TestMCPServerInSessionNew(t *testing.T) {
	gr := &granter{}
	var traceMu sync.Mutex
	var trace strings.Builder
	a := newFake(t, Options{MCP: gr.open, Trace: func(_ string, msg []byte) {
		traceMu.Lock()
		trace.Write(msg)
		traceMu.Unlock()
	}}, httpMCPEnv)
	resp, _, err := stream(t, a, agentReq("/mcpservers"))
	if err != nil {
		t.Fatal(err)
	}
	txt := resp.Message.Text()
	i := strings.Index(txt, "mcp=")
	if i < 0 {
		t.Fatalf("reply %q", txt)
	}
	var servers []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(txt[i+4:])), &servers); err != nil {
		t.Fatalf("%v: %q", err, txt)
	}
	want := `[{"headers":[{"name":"Authorization","value":"Bearer ` + fakeMCPToken + `"}],"name":"crewbase","type":"http","url":"http://127.0.0.1:1/mcp"}]`
	if got, _ := json.Marshal(servers); string(got) != want {
		t.Fatalf("mcpServers\n got %s\nwant %s", got, want)
	}
	gs := gr.all()
	if len(gs) != 1 || gs[0].b.Agent != "lead" || gs[0].b.Session != "S1" || gs[0].b.Mode != "sandbox" {
		t.Fatalf("grants %+v", gs)
	}
	if len(gs[0].turns) != 1 || gs[0].modes[0] != "sandbox" || gs[0].ended != 1 {
		t.Fatalf("turn not activated / ended: %+v", gs[0])
	}
	traceMu.Lock()
	tr := trace.String()
	traceMu.Unlock()
	if tr == "" || strings.Contains(tr, fakeMCPToken) {
		t.Fatalf("token in trace (or no trace): %d bytes", len(tr))
	}
	if gs[0].isRevoked() {
		t.Fatal("revoked while the session lives")
	}
	// the session is reused: no new grant
	req := agentReq("/mcpservers")
	req.Messages = append(req.Messages, resp.Message, user("again"))
	if _, _, err := stream(t, a, req); err != nil {
		t.Fatal(err)
	}
	if len(gr.all()) != 1 || len(gs[0].turns) != 2 {
		t.Fatalf("grants %d turns %d", len(gr.all()), len(gs[0].turns))
	}
	// adapter close revokes
	a.Close()
	if !gs[0].isRevoked() {
		t.Fatal("not revoked on close")
	}
}

func TestMCPServerSkipped(t *testing.T) {
	// the agent does not advertise http MCP servers
	gr := &granter{}
	a := newFake(t, Options{MCP: gr.open}, nil)
	resp, _, err := stream(t, a, agentReq("/mcpservers"))
	if err != nil || !strings.Contains(resp.Message.Text(), "mcp=[]") {
		t.Fatalf("%v %q", err, resp.Message.Text())
	}
	if len(gr.all()) != 0 {
		t.Fatal("grant opened for an agent without http MCP")
	}
	// no agent / session: nothing to bind to
	b := newFake(t, Options{MCP: gr.open}, httpMCPEnv)
	resp, _, err = stream(t, b, llm.Request{Messages: []llm.Message{user("/mcpservers")}})
	if err != nil || !strings.Contains(resp.Message.Text(), "mcp=[]") || len(gr.all()) != 0 {
		t.Fatalf("%v %q", err, resp.Message.Text())
	}
	// no bridge configured
	c := newFake(t, Options{}, httpMCPEnv)
	resp, _, err = stream(t, c, agentReq("/mcpservers"))
	if err != nil || !strings.Contains(resp.Message.Text(), "mcp=[]") {
		t.Fatalf("%v %q", err, resp.Message.Text())
	}
	// the opener declines (server not running)
	d := newFake(t, Options{MCP: func(MCPBinding) MCPGrant { return nil }}, httpMCPEnv)
	resp, _, err = stream(t, d, agentReq("/mcpservers"))
	if err != nil || !strings.Contains(resp.Message.Text(), "mcp=[]") {
		t.Fatalf("%v %q", err, resp.Message.Text())
	}
}

// A new ACP session for the conversation revokes the old one's grant; so
// does the process going away.
func TestMCPGrantRevoked(t *testing.T) {
	gr := &granter{}
	a := newFake(t, Options{MCP: gr.open}, httpMCPEnv)
	if _, _, err := stream(t, a, agentReq("hello")); err != nil {
		t.Fatal(err)
	}
	// not a continuation: a new session starts
	if _, _, err := stream(t, a, agentReq("other")); err != nil {
		t.Fatal(err)
	}
	gs := gr.all()
	if len(gs) != 2 || !gs[0].isRevoked() || gs[1].isRevoked() {
		t.Fatalf("grants %d, revoked %v %v", len(gs), gs[0].isRevoked(), gs[len(gs)-1].isRevoked())
	}
	// a stale tool list starts a new session too
	resp, _, err := stream(t, a, agentReq("third"))
	if err != nil {
		t.Fatal(err)
	}
	g3 := gr.all()[2]
	g3.mu.Lock()
	g3.stale = true
	g3.mu.Unlock()
	req := agentReq("third")
	req.Messages = append(req.Messages, resp.Message, user("more"))
	if _, _, err := stream(t, a, req); err != nil {
		t.Fatal(err)
	}
	if gs := gr.all(); len(gs) != 4 || !g3.isRevoked() {
		t.Fatalf("stale: grants %d revoked %v", len(gs), g3.isRevoked())
	}
	// the process exits
	if _, _, err := stream(t, a, agentReq("/crash")); err == nil {
		t.Fatal("crash not reported")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		all := true
		for _, g := range gr.all() {
			all = all && g.isRevoked()
		}
		if all {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("grants kept after the process exited")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Cancelling the turn cancels the grant's turn context (and so the MCP
// calls running under it).
func TestMCPTurnCancelled(t *testing.T) {
	gr := &granter{}
	a := newFake(t, Options{MCP: gr.open, CancelGrace: 2 * time.Second}, httpMCPEnv)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := a.Stream(ctx, agentReq("/slow"), nil)
		done <- err
	}()
	var g *fakeGrant
	deadline := time.Now().Add(10 * time.Second)
	for g == nil || func() bool { g.mu.Lock(); defer g.mu.Unlock(); return len(g.turns) == 0 }() {
		if time.Now().After(deadline) {
			t.Fatal("turn not started")
		}
		if gs := gr.all(); len(gs) > 0 {
			g = gs[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	g.mu.Lock()
	tctx := g.turns[0]
	g.mu.Unlock()
	cancel()
	select {
	case <-tctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("turn context not cancelled")
	}
	if err := <-done; err == nil {
		t.Fatal("cancel not reported")
	}
	g.mu.Lock()
	ended := g.ended
	g.mu.Unlock()
	if ended != 1 {
		t.Fatalf("turn not ended: %d", ended)
	}
}

// Permission requests for crewbase MCP tools are allowed without asking
// the person (Crewbase's pipeline asks itself); everything else still asks.
func TestMCPPermissionAutoAllowed(t *testing.T) {
	gr := &granter{}
	var mu sync.Mutex
	var asked []string
	a := newFake(t, Options{MCP: gr.open, Approve: func(_ context.Context, r approval.Request) approval.Outcome {
		mu.Lock()
		asked = append(asked, r.Summary)
		mu.Unlock()
		return approval.OutcomeRejected
	}}, httpMCPEnv)
	cases := []struct {
		cmd  string
		auto bool
	}{
		{"/mcpperm mcp__crewbase__computer_act other", true},
		{"/mcpperm mcp__crewbase__browser_open -", true},
		{"/mcpperm computer_act+(crewbase+MCP+Server) other", true},
		{"/mcpperm crewbase/computer_act other", true},
		{"/mcpperm mcp__crewbase__bash other", false},           // not offered
		{"/mcpperm mcp__crewbase__computer_act execute", false}, // wrong kind
		{"/mcpperm mcp__other__computer_act other", false},      // another server
		{"/mcpperm rm+mcp__crewbase__computer_act other", false},
		{"/perm", false},
	}
	for _, c := range cases {
		mu.Lock()
		asked = nil
		mu.Unlock()
		resp, _, err := stream(t, a, agentReq(c.cmd))
		if err != nil {
			t.Fatal(err)
		}
		txt := resp.Message.Text()
		mu.Lock()
		n := len(asked)
		mu.Unlock()
		if c.auto {
			if !strings.Contains(txt, "permission: selected/yes") || n != 0 || !strings.Contains(txt, "Crewbase's own policy applies") {
				t.Fatalf("%s: %q asked=%d", c.cmd, txt, n)
			}
		} else if !strings.Contains(txt, "permission: selected/no") || n != 1 {
			t.Fatalf("%s: %q asked=%d", c.cmd, txt, n)
		}
	}
	// without a grant (agent without http MCP) nothing is auto-allowed
	b := newFake(t, Options{MCP: gr.open, Approve: func(context.Context, approval.Request) approval.Outcome { return approval.OutcomeRejected }}, nil)
	resp, _, err := stream(t, b, agentReq("/mcpperm mcp__crewbase__computer_act other"))
	if err != nil || !strings.Contains(resp.Message.Text(), "permission: selected/no") {
		t.Fatalf("%v %q", err, resp.Message.Text())
	}
}

// Claude Code in an agent's own workspace: the settingSources _meta and the
// MCP server travel together in session/new.
func TestMCPServerWithClaudeCodeMeta(t *testing.T) {
	gr := &granter{}
	r := fakeResolved(t, httpMCPEnv)
	r.ID = claudeCodeID
	a := newAdapter("acp-claude-code", r, Options{MCP: gr.open, IdleTimeout: -1, ToolNotes: true})
	t.Cleanup(a.Close)
	req := agentReq("/mcpservers")
	req.Workspace, req.OwnWorkspace = r.Cwd, true
	resp, _, err := stream(t, a, req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Message.Text(), "mcp=[{") {
		t.Fatalf("mcp %q", resp.Message.Text())
	}
	req = agentReq("/meta")
	req.Workspace, req.OwnWorkspace = r.Cwd, true
	resp, _, err = stream(t, a, req)
	if err != nil || !strings.Contains(resp.Message.Text(), `"settingSources":["user"]`) {
		t.Fatalf("%v %q", err, resp.Message.Text())
	}
}

func TestCrewbaseToolName(t *testing.T) {
	codex := json.RawMessage(`[{"type":"content","content":{"type":"text","text":"Allow the computer_act tool?\nServer: crewbase"}}]`)
	other := json.RawMessage(`[{"type":"content","content":{"type":"text","text":"Allow?\nServer: evil"}}]`)
	if got := crewbaseToolName("", "Approve computer_act", "", codex); got != "computer_act" {
		t.Errorf("codex: %q", got)
	}
	if got := crewbaseToolName("", "Approve computer_act", "", other); got != "" {
		t.Errorf("codex, other server: %q", got)
	}
	if got := crewbaseToolName("", "Approve MCP tool call", "", codex); got != "" {
		t.Errorf("codex, no title: %q", got)
	}
	for _, c := range []struct{ kind, title, meta, want string }{
		{"other", "mcp__crewbase__computer_act", "", "computer_act"},
		{"", "x", "mcp__crewbase__browser_open", "browser_open"},
		{"other", "Tool: crewbase/computer_observe", "", "computer_observe"},
		{"other", "crewbase.computer_open", "", "computer_open"},
		{"other", "crewbase: computer_act", "", "computer_act"},
		{"other", "crewbase_browser_open", "", "browser_open"},
		{"other", "crewbase__computer_act", "", "computer_act"},
		{"other", "computer_act (crewbase MCP Server)", "", "computer_act"},
		{"execute", "mcp__crewbase__computer_act", "", ""},
		{"other", "mcp__crewbase__computer_act; rm -rf /", "", ""},
		{"other", "mcp__crewbased__x", "", ""},
		{"other", "", "", ""},
	} {
		if got := crewbaseToolName(c.kind, c.title, c.meta, nil); got != c.want {
			t.Errorf("%+v: %q", c, got)
		}
	}
}
