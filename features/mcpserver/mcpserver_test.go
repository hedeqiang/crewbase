package mcpserver_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/features/mcpserver"
	"github.com/hedeqiang/crewbase/features/rules"
	"github.com/hedeqiang/crewbase/features/runmode"
	"github.com/hedeqiang/crewbase/kernel"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const deskToken = "desktop-token-for-tests-0123456789abcdef"

type env struct {
	h      *kernel.Host
	reg    *tools.Registry
	inbox  *approval.Inbox
	svc    *mcpserver.Service
	gw     *gateway.Gateway
	agents *agent.Registry
	a, b   *agent.Agent
	ssA    *session.Session
	ssB    *session.Session

	mu      sync.Mutex
	ran     []tools.Call
	started chan struct{}
	stopped chan error
}

func setup(t *testing.T, cfg map[string]any) *env {
	t.Helper()
	dir := t.TempDir()
	e := &env{h: kernel.NewHost(nil), started: make(chan struct{}, 4), stopped: make(chan error, 4)}
	t.Cleanup(e.h.Close)
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, nil}, {prompt.Plugin, nil}, {tools.Plugin, map[string]any{"requirePolicy": "rules"}},
		{approval.Plugin, nil}, {approval.DesktopPlugin, nil}, {rules.Plugin, nil}, {runmode.Plugin, nil},
		{agent.Plugin, map[string]any{"root": filepath.Join(dir, "root")}},
		{gateway.Plugin, map[string]any{"addr": "127.0.0.1:0", "token": deskToken}},
		{mcpserver.Plugin, cfg},
	} {
		if _, err := e.h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range e.h.Plugins() {
		if p.State != kernel.StateActive {
			t.Fatalf("plugin %s %s %v %s", p.ID, p.State, p.Missing, p.Error)
		}
	}
	r := e.h.Root()
	e.reg = kernel.Use[*tools.Registry](r, tools.ServiceKey)
	e.inbox = kernel.Use[*approval.Inbox](r, approval.InboxKey)
	e.svc = kernel.Use[*mcpserver.Service](r, mcpserver.ServiceKey)
	e.gw = kernel.Use[*gateway.Gateway](r, gateway.ServiceKey)
	e.agents = kernel.Use[*agent.Registry](r, agent.ServiceKey)
	var err error
	if e.a, err = e.agents.Create(agent.Spec{Name: "Ada"}); err != nil {
		t.Fatal(err)
	}
	if e.b, err = e.agents.Create(agent.Spec{Name: "Bo"}); err != nil {
		t.Fatal(err)
	}
	if e.ssA, err = e.a.Session(); err != nil {
		t.Fatal(err)
	}
	if e.ssB, err = e.b.Session(); err != nil {
		t.Fatal(err)
	}
	record := func(res tools.Result) func(context.Context, tools.Call) (tools.Result, error) {
		return func(_ context.Context, c tools.Call) (tools.Result, error) {
			e.mu.Lock()
			e.ran = append(e.ran, c)
			e.mu.Unlock()
			return res, nil
		}
	}
	e.reg.Register(r, &tools.Tool{Name: "browser_look", Description: "look", Category: tools.CategoryRead,
		Params: tools.Schema(map[string]any{"url": tools.Prop("string", "u")}, "url"), Execute: record(tools.Text("looked"))})
	e.reg.Register(r, &tools.Tool{Name: "computer_send", Description: "send", Category: tools.CategoryPublish, Execute: record(tools.Text("sent"))})
	e.reg.Register(r, &tools.Tool{Name: "computer_write", Description: "write", Category: tools.CategoryWrite, Execute: record(tools.Text("wrote"))})
	e.reg.Register(r, &tools.Tool{Name: "computer_shot", Description: "shot", Category: tools.CategoryRead,
		Execute: func(_ context.Context, c tools.Call) (tools.Result, error) {
			var b bytes.Buffer
			_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2)))
			if err := os.MkdirAll(filepath.Join(c.Workspace, "generated"), 0o755); err != nil {
				return tools.Result{}, err
			}
			if err := os.WriteFile(filepath.Join(c.Workspace, "generated", "s.png"), b.Bytes(), 0o644); err != nil {
				return tools.Result{}, err
			}
			return tools.Result{Content: "screenshot", Attachments: []llm.Attachment{{Path: "generated/s.png", Name: "s.png", Mime: "image/png"}}}, nil
		}})
	e.reg.Register(r, &tools.Tool{Name: "computer_wait", Description: "waits", Category: tools.CategoryRead,
		Execute: func(ctx context.Context, c tools.Call) (tools.Result, error) {
			e.started <- struct{}{}
			<-ctx.Done()
			e.stopped <- ctx.Err()
			return tools.Errorf("stopped: %v", ctx.Err()), nil
		}})
	e.reg.Register(r, &tools.Tool{Name: "bash", Description: "shell", Category: tools.CategoryExec, Execute: record(tools.Text("ran"))})
	// a tool only agent B has
	e.reg.Register(r.Fork(e.b.ID()), &tools.Tool{Name: "browser_secret", Description: "B only", Category: tools.CategoryRead, Execute: record(tools.Text("secret"))})
	return e
}

func (e *env) grant(t *testing.T, a *agent.Agent, ss *session.Session) *mcpserver.Grant {
	t.Helper()
	g, err := e.svc.Open(mcpserver.Binding{Agent: a.ID(), Session: ss.ID(), Workspace: a.Workspace(), Mode: agent.ModeSandbox, Own: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Revoke)
	return g
}

func (e *env) calls() []tools.Call {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]tools.Call(nil), e.ran...)
}

type rpcResp struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// post sends a JSON-RPC message with token; it returns the status and body.
func post(t *testing.T, u, token string, msg any) (int, []byte) {
	t.Helper()
	b, _ := json.Marshal(msg)
	req, _ := http.NewRequest(http.MethodPost, u, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

func rpc(t *testing.T, g *mcpserver.Grant, method string, params any) rpcResp {
	t.Helper()
	_, u, tok := g.Server()
	st, body := post(t, u, tok, map[string]any{"jsonrpc": "2.0", "id": 7, "method": method, "params": params})
	if st != 200 {
		t.Fatalf("%s: status %d %s", method, st, body)
	}
	var r rpcResp
	if err := json.Unmarshal(body, &r); err != nil || string(r.ID) != "7" {
		t.Fatalf("%s: %s", method, body)
	}
	return r
}

func callTool(t *testing.T, g *mcpserver.Grant, name string, args any) mcpserver.CallResult {
	t.Helper()
	r := rpc(t, g, "tools/call", map[string]any{"name": name, "arguments": args})
	if r.Error != nil {
		t.Fatalf("%s: rpc error %+v", name, r.Error)
	}
	var cr mcpserver.CallResult
	if err := json.Unmarshal(r.Result, &cr); err != nil {
		t.Fatal(err)
	}
	return cr
}

func listNames(t *testing.T, g *mcpserver.Grant) []string {
	t.Helper()
	r := rpc(t, g, "tools/list", map[string]any{})
	var res struct {
		Tools []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(r.Result, &res); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, x := range res.Tools {
		if x.InputSchema["type"] != "object" {
			t.Fatalf("%s schema %v", x.Name, x.InputSchema)
		}
		out = append(out, x.Name)
	}
	return out
}

func turn(g *mcpserver.Grant) (context.CancelFunc, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	end := g.Turn(ctx, agent.ModeSandbox)
	return cancel, end
}

func TestInitializeListCall(t *testing.T) {
	e := setup(t, nil)
	g := e.grant(t, e.a, e.ssA)
	name, u, tok := g.Server()
	if name != "crewbase" || !strings.HasPrefix(u, "http://127.0.0.1:") || !strings.HasSuffix(u, "/mcp") || len(tok) < 32 {
		t.Fatalf("server %s %s", name, u)
	}
	r := rpc(t, g, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "t", "version": "1"}})
	var ir struct {
		ProtocolVersion string                     `json:"protocolVersion"`
		Capabilities    map[string]json.RawMessage `json:"capabilities"`
		ServerInfo      struct{ Name string }      `json:"serverInfo"`
	}
	_ = json.Unmarshal(r.Result, &ir)
	if ir.ProtocolVersion != "2025-06-18" || ir.ServerInfo.Name != "crewbase" || ir.Capabilities["tools"] == nil {
		t.Fatalf("initialize %s", r.Result)
	}
	// unknown version: the newest we serve
	r = rpc(t, g, "initialize", map[string]any{"protocolVersion": "1999-01-01"})
	_ = json.Unmarshal(r.Result, &ir)
	if ir.ProtocolVersion != "2025-11-25" {
		t.Fatalf("negotiated %s", ir.ProtocolVersion)
	}
	// notifications are accepted without a body
	st, body := post(t, u, tok, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	if st != http.StatusAccepted || len(body) != 0 {
		t.Fatalf("notification: %d %s", st, body)
	}
	if r := rpc(t, g, "ping", nil); r.Error != nil || string(r.Result) != "{}" {
		t.Fatalf("ping %s", r.Result)
	}
	if r := rpc(t, g, "resources/list", nil); r.Error == nil || r.Error.Code != -32601 {
		t.Fatalf("unknown method %+v", r)
	}

	// allow-list: computer_* and browser_* that A can see; never bash or B's tool
	got := strings.Join(listNames(t, g), ",")
	if got != "browser_look,computer_send,computer_shot,computer_wait,computer_write" {
		t.Fatalf("tools %s", got)
	}

	cancel, end := turn(g)
	defer cancel()
	defer end()
	res := callTool(t, g, "browser_look", map[string]string{"url": "https://example.com"})
	if res.IsError || len(res.Content) != 1 || res.Content[0].Text != "looked" {
		t.Fatalf("result %+v", res)
	}
	cs := e.calls()
	if len(cs) != 1 || cs[0].Agent != e.a.ID() || cs[0].Session == nil || cs[0].Session.ID() != e.ssA.ID() ||
		cs[0].Mode != agent.ModeSandbox || cs[0].Workspace != e.a.Workspace() || string(cs[0].Args) != `{"url":"https://example.com"}` {
		t.Fatalf("call %+v", cs)
	}
	// recorded in the session like any tool call
	var sawCall, sawResult bool
	for _, ev := range e.ssA.Events() {
		sawCall = sawCall || (ev.Type == session.TypeToolCall && ev.Agent == e.a.ID() && strings.Contains(string(ev.Data), "browser_look"))
		sawResult = sawResult || (ev.Type == session.TypeToolResult && strings.Contains(string(ev.Data), "looked"))
	}
	if !sawCall || !sawResult {
		t.Fatal("call not recorded in the session")
	}
	// validation runs as in the pipeline
	if res := callTool(t, g, "browser_look", map[string]string{}); !res.IsError || !strings.Contains(res.Content[0].Text, "missing required: url") {
		t.Fatalf("validation %+v", res)
	}
}

func TestUnknownAndNotOfferedTools(t *testing.T) {
	e := setup(t, nil)
	g := e.grant(t, e.a, e.ssA)
	cancel, end := turn(g)
	defer cancel()
	defer end()
	for _, name := range []string{"nope", "bash", "computer_nope", "browser_secret"} {
		r := rpc(t, g, "tools/call", map[string]any{"name": name, "arguments": map[string]any{}})
		if r.Error == nil || r.Error.Code != -32602 || !strings.Contains(r.Error.Message, "Unknown tool") {
			t.Fatalf("%s: %+v %s", name, r.Error, r.Result)
		}
	}
	if len(e.calls()) != 0 {
		t.Fatal("a refused tool ran")
	}
	for _, ev := range e.ssA.Events() {
		if ev.Type == session.TypeToolCall {
			t.Fatalf("refused call recorded: %s", ev.Data)
		}
	}
}

func TestAllowListConfig(t *testing.T) {
	e := setup(t, map[string]any{"tools": []string{"browser_*"}})
	g := e.grant(t, e.a, e.ssA)
	if got := strings.Join(listNames(t, g), ","); got != "browser_look" {
		t.Fatalf("tools %s", got)
	}
	if g.Allows("computer_send") || !g.Allows("browser_look") || g.Allows("bash") {
		t.Fatal("Allows does not follow the allow-list")
	}
	if !e.svc.Offered("browser_x") || e.svc.Offered("computer_x") {
		t.Fatal("Offered")
	}
}

func TestAuth(t *testing.T) {
	e := setup(t, nil)
	g := e.grant(t, e.a, e.ssA)
	_, u, tok := g.Server()
	msg := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}
	for _, bad := range []string{"", "wrong", deskToken, tok + "x"} {
		if st, _ := post(t, u, bad, msg); st != http.StatusUnauthorized {
			t.Fatalf("token %q: %d", bad, st)
		}
	}
	// the MCP token authenticates nothing else on the gateway
	base := strings.TrimSuffix(u, "/mcp")
	for _, p := range []string{"/api/agents", "/api/events"} {
		req, _ := http.NewRequest(http.MethodGet, base+p, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s with the MCP token: %d", p, resp.StatusCode)
		}
	}
	// foreign browser origins are refused
	b, _ := json.Marshal(msg)
	req, _ := http.NewRequest(http.MethodPost, u, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Origin", "https://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: %d", resp.StatusCode)
	}
	// GET (no SSE stream) is 405 once authenticated
	req, _ = http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", resp.StatusCode)
	}
	if st, _ := post(t, u, tok, msg); st != 200 {
		t.Fatalf("valid token: %d", st)
	}
	// expired (revoked) token
	g.Revoke()
	if st, _ := post(t, u, tok, msg); st != http.StatusUnauthorized {
		t.Fatalf("revoked token: %d", st)
	}
	if e.svc.Grants() != 0 {
		t.Fatal("grant kept after revoke")
	}
	// unloading the server revokes every grant
	g2 := e.grant(t, e.a, e.ssA)
	_, _, tok2 := g2.Server()
	for _, p := range e.h.Plugins() {
		if p.Name == "@crewbase/mcp-server" {
			if !e.h.Unload(p.ID) {
				t.Fatal("unload failed")
			}
		}
	}
	if st, _ := post(t, u, tok2, msg); st == 200 {
		t.Fatal("token valid after the server unloaded")
	}
}

// A token acts as its own agent and session only.
func TestTokenBoundToAgentAndSession(t *testing.T) {
	e := setup(t, nil)
	ga := e.grant(t, e.a, e.ssA)
	gb := e.grant(t, e.b, e.ssB)
	for _, g := range []*mcpserver.Grant{ga, gb} {
		cancel, end := turn(g)
		defer cancel()
		defer end()
	}
	// arguments cannot change who is calling
	callTool(t, ga, "browser_look", map[string]string{"url": "x", "agent": e.b.ID(), "session": e.ssB.ID()})
	if r := rpc(t, ga, "tools/call", map[string]any{"name": "browser_secret"}); r.Error == nil {
		t.Fatal("A's token reached B's tool")
	}
	if res := callTool(t, gb, "browser_secret", nil); res.IsError || res.Content[0].Text != "secret" {
		t.Fatalf("B: %+v", res)
	}
	cs := e.calls()
	if len(cs) != 2 || cs[0].Agent != e.a.ID() || cs[0].Session.ID() != e.ssA.ID() || cs[1].Agent != e.b.ID() || cs[1].Session.ID() != e.ssB.ID() {
		t.Fatalf("calls %+v", cs)
	}
	for _, ev := range e.ssB.Events() {
		if ev.Type == session.TypeToolCall && ev.Agent != e.b.ID() {
			t.Fatalf("A's call landed in B's session: %s", ev.Data)
		}
	}
}

// Calls run only during a turn; the turn's end and cancellation stop them.
func TestTurnScopeAndCancellation(t *testing.T) {
	e := setup(t, nil)
	g := e.grant(t, e.a, e.ssA)
	res := callTool(t, g, "browser_look", map[string]string{"url": "x"})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "while Crewbase is running a turn") {
		t.Fatalf("outside a turn: %+v", res)
	}
	if len(e.calls()) != 0 {
		t.Fatal("ran outside a turn")
	}

	for _, how := range []string{"turn cancelled", "turn ended", "revoked", "notifications/cancelled"} {
		cancel, end := turn(g)
		done := make(chan mcpserver.CallResult, 1)
		go func() { done <- callTool(t, g, "computer_wait", nil) }()
		select {
		case <-e.started:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: tool did not start", how)
		}
		switch how {
		case "turn cancelled":
			cancel()
		case "turn ended":
			end()
		case "revoked":
			g.Revoke()
		case "notifications/cancelled":
			_, u, tok := g.Server()
			if st, _ := post(t, u, tok, map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": 7}}); st != http.StatusAccepted {
				t.Fatalf("cancel notification: %d", st)
			}
		}
		select {
		case err := <-e.stopped:
			if err == nil {
				t.Fatalf("%s: no ctx error", how)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: tool not cancelled", how)
		}
		if how != "revoked" {
			if r := <-done; !r.IsError {
				t.Fatalf("%s: %+v", how, r)
			}
		}
		cancel()
		end()
		if how == "revoked" {
			g = e.grant(t, e.a, e.ssA)
		}
	}
}

// Calls go through rules, approval cards and run-mode guards.
func TestPipelinePolicy(t *testing.T) {
	e := setup(t, nil)
	g := e.grant(t, e.a, e.ssA)
	cancel, end := turn(g)
	defer cancel()
	defer end()

	// publish asks: the approval card is Crewbase's own
	for _, d := range []approval.Decision{{Outcome: approval.OutcomeRejected}, {Outcome: approval.OutcomeAllowedOnce}} {
		done := make(chan mcpserver.CallResult, 1)
		go func() { done <- callTool(t, g, "computer_send", nil) }()
		var req approval.Request
		deadline := time.Now().Add(5 * time.Second)
	wait:
		for time.Now().Before(deadline) {
			for _, p := range e.inbox.Pending() {
				req = p
				break wait
			}
			time.Sleep(5 * time.Millisecond)
		}
		if req.CallID == "" || req.Agent != e.a.ID() || req.Session != e.ssA.ID() || req.Tool != "computer_send" || req.Category != tools.CategoryPublish {
			t.Fatalf("approval %+v", req)
		}
		e.inbox.Decide(req.CallID, d)
		res := <-done
		if d.Outcome == approval.OutcomeRejected {
			if !res.IsError || !strings.Contains(res.Content[0].Text, "not approved") {
				t.Fatalf("rejected: %+v", res)
			}
		} else if res.IsError || res.Content[0].Text != "sent" {
			t.Fatalf("approved: %+v", res)
		}
	}
	if n := len(e.calls()); n != 1 {
		t.Fatalf("ran %d times", n)
	}

	// an agent rule blocks
	eng := kernel.Use[*rules.Engine](e.h.Root(), rules.ServiceKey)
	if err := eng.SetAgentRule(e.a.ID(), tools.CategoryWrite, rules.Block); err != nil {
		t.Fatal(err)
	}
	if res := callTool(t, g, "computer_write", nil); !res.IsError || !strings.Contains(res.Content[0].Text, "denied") {
		t.Fatalf("blocked: %+v", res)
	}

	// a read-only turn cannot write, whatever the grant was opened with
	ro, err := e.agents.Create(agent.Spec{Name: "Ro", Mode: agent.ModeReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	ssR, _ := ro.Session()
	gr := e.grant(t, ro, ssR)
	cancel2, end2 := turn(gr)
	defer cancel2()
	defer end2()
	if res := callTool(t, gr, "computer_write", nil); !res.IsError || !strings.Contains(res.Content[0].Text, "denied") {
		t.Fatalf("read-only: %+v", res)
	}
	if n := len(e.calls()); n != 1 {
		t.Fatalf("ran %d times", n)
	}
}

func TestImageResult(t *testing.T) {
	e := setup(t, nil)
	g := e.grant(t, e.a, e.ssA)
	cancel, end := turn(g)
	defer cancel()
	defer end()
	res := callTool(t, g, "computer_shot", nil)
	if res.IsError || len(res.Content) != 2 || res.Content[0].Text != "screenshot" || res.Content[1].Type != "image" || res.Content[1].MimeType != "image/png" {
		t.Fatalf("result %+v", res)
	}
	raw, err := base64.StdEncoding.DecodeString(res.Content[1].Data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := png.Decode(bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
}

// The list a client saw goes stale when the offered tools change.
func TestStale(t *testing.T) {
	e := setup(t, nil)
	g := e.grant(t, e.a, e.ssA)
	if g.Stale() {
		t.Fatal("stale before listing")
	}
	listNames(t, g)
	if g.Stale() {
		t.Fatal("stale right after listing")
	}
	c := e.h.Root().Fork("")
	e.reg.Register(c, &tools.Tool{Name: "computer_new", Execute: func(context.Context, tools.Call) (tools.Result, error) { return tools.Text("x"), nil }})
	if !g.Stale() {
		t.Fatal("new tool not noticed")
	}
	c.Dispose()
	if g.Stale() {
		t.Fatal("removal not noticed")
	}
}

type bearerRT struct{ tok string }

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.tok)
	return http.DefaultTransport.RoundTrip(r)
}

// The official MCP client (the one Crewbase's MCP client uses) talks to the
// server: version negotiation, list, call.
func TestSDKClientInterop(t *testing.T) {
	e := setup(t, nil)
	g := e.grant(t, e.a, e.ssA)
	cancel, end := turn(g)
	defer cancel()
	defer end()
	_, u, tok := g.Server()
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	cl := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := cl.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: u, HTTPClient: &http.Client{Transport: bearerRT{tok}}, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	if v := cs.InitializeResult().ProtocolVersion; v != "2025-11-25" {
		t.Fatalf("version %s", v)
	}
	lr, err := cs.ListTools(ctx, nil)
	if err != nil || len(lr.Tools) != 5 {
		t.Fatalf("list %v %+v", err, lr)
	}
	r, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "computer_shot"})
	if err != nil || r.IsError || len(r.Content) != 2 {
		t.Fatalf("call %v %+v", err, r)
	}
	if tc, ok := r.Content[0].(*sdk.TextContent); !ok || tc.Text != "screenshot" {
		t.Fatalf("text %#v", r.Content[0])
	}
	if ic, ok := r.Content[1].(*sdk.ImageContent); !ok || ic.MIMEType != "image/png" || len(ic.Data) == 0 {
		t.Fatalf("image %#v", r.Content[1])
	}
	if _, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "bash"}); err == nil {
		t.Fatal("bash callable")
	}
}

// The service is the agent loop's ToolBridge: what it lists is what the
// context meter counts for a CLI.
func TestBridgedSchemas(t *testing.T) {
	e := setup(t, map[string]any{"tools": []string{"browser_*"}})
	b, ok := kernel.Lookup[agentloop.ToolBridge](e.h.Root(), agentloop.ToolBridgeKey)
	if !ok {
		t.Fatal("the MCP server is not a ToolBridge")
	}
	ts := b.BridgedSchemas(e.a.ID(), "")
	if len(ts) != 1 || ts[0].Name != "browser_look" || ts[0].Parameters["type"] != "object" {
		t.Fatalf("bridged %+v", ts)
	}
}
