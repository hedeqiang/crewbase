package api_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/api"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

const tok = "test-token-0123456789abcdef0123456789"

func start(t *testing.T) (string, *kernel.Host) {
	t.Helper()
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	var addr string
	gateway.Listening.Listen(h.Root(), func(a string) { addr = a })
	root := t.TempDir()
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, map[string]any{"dir": root + "/sessions"}},
		{prompt.Plugin, nil}, {tools.Plugin, nil}, {approval.Plugin, nil}, {approval.DesktopPlugin, nil},
		{llm.Plugin, nil}, {mock.Plugin, nil}, {router.Plugin, nil},
		{agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"mock/default"}}},
		{agentloop.Plugin, nil},
		{gateway.Plugin, map[string]any{"addr": "127.0.0.1:0", "token": tok}},
		{api.Plugin, nil},
	} {
		if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
			t.Fatal(err)
		}
	}
	kernel.Use[*gateway.Gateway](h.Root(), gateway.ServiceKey).AddToken("mobile-token-xxxxxxxxxxxxxxxxxxxxxxxx", "mobile")
	return "http://" + addr, h
}

func do(t *testing.T, base, token, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, base+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestAuthValidateActivate(t *testing.T) {
	base, h := start(t)
	if code, _ := do(t, base, "", "GET", "/api/agents", nil); code != 401 {
		t.Fatal("unauthenticated must be rejected", code)
	}
	// unauthenticated requests do not reveal existence: same 401 for any path
	if code, _ := do(t, base, "wrong", "POST", "/api/agents/x/messages", map[string]string{"text": "hi"}); code != 401 {
		t.Fatal(code)
	}
	code, a := do(t, base, tok, "POST", "/api/agents", map[string]any{"name": "Kai", "modelRefs": []string{"mock/default"}})
	if code != 200 || a["id"] != "kai" {
		t.Fatal(code, a)
	}
	if code, _ := do(t, base, tok, "POST", "/api/agents", map[string]any{"name": "X", "mode": "host"}); code != 400 {
		t.Fatal("host mode at creation must be rejected", code)
	}
	// validation failure must not enqueue (no activation)
	if code, _ := do(t, base, tok, "POST", "/api/agents/kai/messages", map[string]any{"text": ""}); code != 400 {
		t.Fatal(code)
	}
	ag, _ := kernel.Use[*agent.Registry](h.Root(), agent.ServiceKey).Get("kai")
	ss, _ := ag.Session()
	for _, e := range ss.Events() {
		if e.Type == session.TypeInputQueued {
			t.Fatal("rejected call activated the agent")
		}
	}
	// mobile may send messages but not create agents or list plugins
	if code, _ := do(t, base, "mobile-token-xxxxxxxxxxxxxxxxxxxxxxxx", "POST", "/api/agents", map[string]any{"name": "Y"}); code != 403 {
		t.Fatal(code)
	}
	if code, _ := do(t, base, "mobile-token-xxxxxxxxxxxxxxxxxxxxxxxx", "GET", "/api/plugins", nil); code != 403 {
		t.Fatal(code)
	}
	if code, _ := do(t, base, tok, "GET", "/api/agents/missing", nil); code != 404 {
		t.Fatal(code)
	}
}

func TestMessageAndEventStream(t *testing.T) {
	base, _ := start(t)
	do(t, base, tok, "POST", "/api/agents", map[string]any{"name": "Kai"})
	req, _ := http.NewRequest("GET", base+"/api/events?token="+tok, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	code, _ := do(t, base, "mobile-token-xxxxxxxxxxxxxxxxxxxxxxxx", "POST", "/api/agents/kai/messages", map[string]any{"text": "ping"})
	if code != 200 {
		t.Fatal(code)
	}
	sc := bufio.NewScanner(resp.Body)
	deadline := time.After(3 * time.Second)
	got := make(chan bool)
	go func() {
		for sc.Scan() {
			if strings.Contains(sc.Text(), "default: ping") {
				got <- true
				return
			}
		}
	}()
	select {
	case <-got:
	case <-deadline:
		t.Fatal("assistant reply not streamed")
	}
	code, out := do(t, base, tok, "GET", "/api/models", nil)
	if code != 200 || len(out["providers"].([]any)) != 1 {
		t.Fatal(code, out)
	}
}

func TestApprovalIDWithSlashes(t *testing.T) {
	base, h := start(t)
	in := kernel.Use[*approval.Inbox](h.Root(), approval.InboxKey)
	svc := kernel.Use[*approval.Service](h.Root(), approval.ServiceKey)
	st := kernel.Use[*session.Store](h.Root(), session.ServiceKey)
	ss, _ := st.Create(session.Meta{})
	done := make(chan approval.Decision, 1)
	go func() {
		done <- svc.Ask(context.Background(), ss, approval.Request{CallID: "acp/s_1/toolu_2", Session: ss.ID()})
	}()
	for len(in.Pending()) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if code, out := do(t, base, tok, "POST", "/api/approvals/"+url.PathEscape("acp/s_1/toolu_2"), map[string]string{"outcome": "allowed-once"}); code != 200 {
		t.Fatal(code, out)
	}
	if d := <-done; d.Outcome != approval.OutcomeAllowedOnce {
		t.Fatal(d)
	}
}

func TestCreateChat(t *testing.T) {
	base, _ := start(t)
	code, out := do(t, base, tok, "POST", "/api/agents", map[string]any{"kind": "chat"})
	if code != 200 || out["kind"] != "chat" || out["name"] != "新对话" {
		t.Fatal(code, out)
	}
	if code, _ := do(t, base, tok, "POST", "/api/agents", map[string]any{"kind": "robot", "name": "x"}); code != 400 {
		t.Fatalf("unknown kind: %d", code)
	}
	if code, _ := do(t, base, tok, "POST", "/api/agents", map[string]any{}); code != 400 {
		t.Fatalf("agents still need a name: %d", code)
	}
}

// Clients cannot create subagents, and a folder an agent works in directly
// must be a real project folder.
func TestCreateGuards(t *testing.T) {
	base, _ := start(t)
	if code, _ := do(t, base, tok, "POST", "/api/agents", map[string]any{"name": "x", "ephemeral": true}); code != 400 {
		t.Fatalf("ephemeral via API: %d", code)
	}
	if code, _ := do(t, base, tok, "POST", "/api/agents", map[string]any{"name": "x", "parent": "kai"}); code != 400 {
		t.Fatalf("parent via API: %d", code)
	}
	home, _ := os.UserHomeDir()
	for _, dir := range []string{"/", "relative/dir", home, "/no/such/dir"} {
		if code, _ := do(t, base, tok, "POST", "/api/agents", map[string]any{"kind": "chat", "sharedWorkspace": dir}); code != 400 {
			t.Fatalf("project folder %q accepted: %d", dir, code)
		}
	}
	proj := t.TempDir()
	code, out := do(t, base, tok, "POST", "/api/agents", map[string]any{"kind": "chat", "sharedWorkspace": proj})
	real, _ := filepath.EvalSymlinks(proj)
	if code != 200 || out["workspace"] != real {
		t.Fatalf("project folder: %d %v", code, out)
	}
}

// An agent can be pointed at a project folder and back; its own files stay
// in its home.
func TestProjectDir(t *testing.T) {
	base, _ := start(t)
	code, out := do(t, base, tok, "POST", "/api/agents", map[string]any{"name": "Dev"})
	if code != 200 {
		t.Fatal(code, out)
	}
	id := out["id"].(string)
	own := out["workspace"].(string)
	proj := t.TempDir()
	real, _ := filepath.EvalSymlinks(proj)
	code, out = do(t, base, tok, "PATCH", "/api/agents/"+id, map[string]any{"projectDir": proj})
	if code != 200 || out["workspace"] != real {
		t.Fatalf("set: %d %v", code, out)
	}
	if _, err := os.Stat(filepath.Join(real, "MEMORY.md")); err == nil {
		t.Fatal("agent files written into the project")
	}
	_, ev := do(t, base, tok, "GET", "/api/sessions/"+out["session"].(string)+"/events", nil)
	b, _ := json.Marshal(ev)
	if !strings.Contains(string(b), "工作目录已更改") || !strings.Contains(string(b), real) {
		t.Fatal("the agent was not told its folder changed")
	}
	if code, _ := do(t, base, tok, "PATCH", "/api/agents/"+id, map[string]any{"projectDir": "/"}); code != 400 {
		t.Fatal("root accepted")
	}
	code, out = do(t, base, tok, "PATCH", "/api/agents/"+id, map[string]any{"projectDir": ""})
	if code != 200 || out["workspace"] != own {
		t.Fatalf("reset: %d %v", code, out)
	}
}

// Regenerate and edit-and-resend: the model sees the resent message and
// not the superseded answer; the log keeps both.
func TestResend(t *testing.T) {
	base, h := start(t)
	do(t, base, tok, "POST", "/api/agents", map[string]any{"name": "Kai"})
	a, _ := kernel.Use[*agent.Registry](h.Root(), agent.ServiceKey).Get("kai")
	st := kernel.Use[*session.Store](h.Root(), session.ServiceKey)
	sid := a.Spec().Session
	send := func(text string) {
		t.Helper()
		if code, out := do(t, base, tok, "POST", "/api/agents/kai/messages", map[string]any{"text": text}); code != 200 {
			t.Fatal(code, out)
		}
		if !agentloop.WaitIdle(a, 5*time.Second) {
			t.Fatal("not idle")
		}
	}
	userSeq := func(text string) int64 {
		ss, _ := st.Get(sid)
		var seq int64
		for _, e := range ss.Events() {
			var m session.UserMessage
			if e.Type == session.TypeUserMessage && e.Decode(&m) == nil && m.Text == text {
				seq = e.Seq
			}
		}
		return seq
	}
	view := func() []string {
		ss, _ := st.Get(sid)
		var out []string
		for _, m := range session.DeriveMessages(ss.Events(), "kai", nil) {
			out = append(out, string(m.Role)+":"+m.Text())
		}
		return out
	}
	send("one")
	send("two")

	// regenerate the last reply
	code, out := do(t, base, tok, "POST", "/api/agents/kai/resend", map[string]any{"session": sid, "seq": userSeq("two")})
	if code != 200 || out["reason"] != "regenerate" {
		t.Fatal(code, out)
	}
	agentloop.WaitIdle(a, 5*time.Second)
	got := strings.Join(view(), "|")
	if got != "user:one|assistant:default: one|user:two|assistant:default: two" {
		t.Fatalf("after regenerate: %s", got)
	}
	ss, _ := st.Get(sid)
	replies := 0
	for _, e := range ss.Events() {
		if e.Type == session.TypeAssistantMessage {
			replies++
		}
	}
	if replies != 3 {
		t.Fatalf("log should keep the superseded reply: %d replies", replies)
	}

	// edit the first message: everything after it is superseded
	code, out = do(t, base, tok, "POST", "/api/agents/kai/resend", map[string]any{"session": sid, "seq": userSeq("one"), "text": "uno"})
	if code != 200 || out["reason"] != "edit" {
		t.Fatal(code, out)
	}
	agentloop.WaitIdle(a, 5*time.Second)
	if got := strings.Join(view(), "|"); got != "user:uno|assistant:default: uno" {
		t.Fatalf("after edit: %s", got)
	}

	// a message the model no longer sees cannot be resent
	if code, _ := do(t, base, tok, "POST", "/api/agents/kai/resend", map[string]any{"session": sid, "seq": userSeq("two")}); code != 400 {
		t.Fatalf("superseded message resent: %d", code)
	}
	if code, _ := do(t, base, tok, "POST", "/api/agents/kai/resend", map[string]any{"session": sid, "seq": userSeq("uno"), "text": "  "}); code != 400 {
		t.Fatalf("empty edit: %d", code)
	}
	// not while the agent is busy
	a.Pause()
	if code, _ := do(t, base, tok, "POST", "/api/agents/kai/resend", map[string]any{"session": sid, "seq": userSeq("uno")}); code != 409 {
		t.Fatalf("busy: %d", code)
	}
	a.Resume()
	// not in channels
	ch, _ := st.Create(session.Meta{Kind: "channel", Agents: []string{"kai"}})
	m := ch.MustAppend(session.TypeUserMessage, "", session.UserMessage{Text: "hi", To: "kai"})
	if code, _ := do(t, base, tok, "POST", "/api/agents/kai/resend", map[string]any{"session": ch.ID(), "seq": m.Seq}); code != 400 {
		t.Fatalf("channel: %d", code)
	}
}
