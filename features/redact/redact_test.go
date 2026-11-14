package redact_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/redact"
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

func TestPatterns(t *testing.T) {
	cases := []string{
		"key sk-proj-abcdefghijklmnop1234 end",
		"token ghp_ABCDEFGHIJKLMNOPQRSTuvwx12",
		"slack xoxb-1234567890-abcdef",
		"aws AKIAIOSFODNN7EXAMPLE here",
	}
	for _, in := range cases {
		out := redact.Redact(in)
		if !strings.Contains(out, redact.Placeholder) {
			t.Errorf("not redacted: %q → %q", in, out)
		}
	}
	if out := redact.Redact("Authorization: Bearer eyJhbGciOi.abc.def"); out != "Authorization: Bearer [redacted]" {
		t.Error(out)
	}
	if out := redact.Redact("plain text sk-short"); out != "plain text sk-short" {
		t.Error("over-redaction", out)
	}
}

func TestWithKnownValues(t *testing.T) {
	out := redact.With(`pw=hunter22 and "q\"uote99"`, []string{"hunter22", `q"uote99`, "abc"})
	if strings.Contains(out, "hunter22") || strings.Contains(out, "uote99") {
		t.Fatal(out)
	}
	if out := redact.With("abc def", []string{"abc"}); out != "abc def" {
		t.Fatal("values shorter than 6 are not scrubbed", out)
	}
}

func TestDiagnosticCap(t *testing.T) {
	s := strings.Repeat("é", 5000) + " sk-abcdefghijklmnopqrstuv"
	d := redact.Diagnostic(s)
	if len(d) > redact.MaxDiagnostic || !strings.HasSuffix(d, "[truncated]") {
		t.Fatal(len(d))
	}
	if d := redact.Diagnostic("err: sk-abcdefghijklmnopqrstuv"); d != "err: [redacted]" {
		t.Fatal(d)
	}
}

func setup(t *testing.T) (*kernel.Host, *tools.Registry, *session.Session, *credentials.Service) {
	t.Helper()
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	for _, s := range []kernel.Spec{session.Plugin, tools.Plugin, approval.Plugin, redact.Plugin, credentials.Plugin} {
		if _, err := h.Load(s, kernel.LoadOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	st := kernel.Use[*session.Store](h.Root(), session.ServiceKey)
	ss, err := st.Create(session.Meta{Kind: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	return h, kernel.Use[*tools.Registry](h.Root(), tools.ServiceKey), ss,
		kernel.Use[*credentials.Service](h.Root(), credentials.ServiceKey)
}

func TestSecretNeverReachesSessionLog(t *testing.T) {
	// credentials loads after redact: lookup is lazy, so it still works
	h, reg, ss, creds := setup(t)
	const secret = "my-db-password-Zq9" // no token shape: only the vault knows it
	creds.Set("mem:db", secret)

	reg.Register(h.Root(), &tools.Tool{Name: "leak", Execute: func(ctx context.Context, _ tools.Call) (tools.Result, error) {
		v, err := creds.Resolve(ctx, "mem:db")
		if err != nil {
			return tools.Result{}, err
		}
		return tools.Result{Content: "connected with " + v, Data: map[string]string{"dsn": "pg://u:" + v + "@db"}}, nil
	}})
	var seen tools.Execution
	tools.OnResult.Listen(h.Root(), func(e tools.Execution) { seen = e })

	exe := reg.Run(context.Background(), h.Root(), tools.Call{ID: "c1", Name: "leak", Args: []byte(`{}`), Agent: "kai", Session: ss})
	if strings.Contains(exe.Result.Content, secret) || !strings.Contains(exe.Result.Content, redact.Placeholder) {
		t.Fatal(exe.Result.Content)
	}
	for _, e := range ss.Events() {
		if strings.Contains(string(e.Data), secret) {
			t.Fatalf("secret reached the session log in %s: %s", e.Type, e.Data)
		}
	}
	if strings.Contains(seen.Result.Content, secret) {
		t.Fatal("tools/result listeners saw the secret")
	}
	if b, ok := exe.Result.Data.(json.RawMessage); !ok || strings.Contains(string(b), secret) {
		t.Fatal("data leaked")
	}
	if strings.Contains(redact.Redact("log: "+secret), secret) {
		t.Fatal("Redact must use tracked values")
	}

	// a failing inner post-execute listener must not leak the raw result
	h.Load(kernel.Spec{Name: "broken", Apply: func(c *kernel.Context) error {
		tools.PostExecute.Use(c, func(context.Context, tools.Execution, kernel.Next[tools.Execution, tools.Result]) (tools.Result, error) {
			return tools.Result{}, errors.New("boom")
		})
		return nil
	}}, kernel.LoadOptions{})
	reg.Run(context.Background(), h.Root(), tools.Call{ID: "c2", Name: "leak", Args: []byte(`{}`), Agent: "kai", Session: ss})
	last, _ := ss.Last(session.TypeToolResult)
	if strings.Contains(string(last.Data), secret) || !strings.Contains(string(last.Data), "connected with") {
		t.Fatal(string(last.Data))
	}

	// unloading the plugin removes its source
	h.Unload("@crewbase/redact")
	if !strings.Contains(redact.Redact(secret), secret) {
		t.Fatal("source should be gone after unload")
	}
}

func TestStreamChunk(t *testing.T) {
	h, _, _, creds := setup(t)
	creds.Set("mem:k", "streamed-secret-1")
	out, err := llm.StreamChunk.Run(h, context.Background(), llm.Chunk{Type: "text", Text: "x streamed-secret-1 y"},
		func(_ context.Context, c llm.Chunk) (llm.Chunk, error) { return c, nil })
	if err != nil || out.Text != "x [redacted] y" {
		t.Fatal(out, err)
	}
}

func TestResponseRedactedAcrossChunks(t *testing.T) {
	root := t.TempDir()
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	load := func(s kernel.Spec, cfg map[string]any) {
		t.Helper()
		if _, err := h.Load(s, kernel.LoadOptions{Config: cfg}); err != nil {
			t.Fatal(err)
		}
	}
	load(session.Plugin, map[string]any{"dir": root + "/sessions"})
	load(prompt.Plugin, nil)
	load(tools.Plugin, nil)
	load(approval.Plugin, nil)
	load(llm.Plugin, nil)
	load(mock.Plugin, map[string]any{"ids": []string{"m"}})
	load(router.Plugin, nil)
	load(agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"m/default"}})
	load(agentloop.Plugin, nil)
	load(credentials.Plugin, nil)
	load(redact.Plugin, nil)
	r := h.Root()
	creds := kernel.Use[*credentials.Service](r, credentials.ServiceKey)
	const secret = "my-db-password-Zq9"
	creds.Set("mem:db", secret)
	half := len(secret) / 2

	kernel.Use[*tools.Registry](r, tools.ServiceKey).Register(r, &tools.Tool{Name: "echo", Category: tools.CategoryRead,
		Execute: func(_ context.Context, c tools.Call) (tools.Result, error) { return tools.Text("ok"), nil }})
	ad, _ := kernel.Use[*llm.Registry](r, llm.ServiceKey).Get("m")
	var n int
	ad.(*mock.Adapter).Script = func(req llm.Request) (llm.Response, error) {
		n++
		if n > 1 {
			return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: "text", Text: "done"}}}, StopReason: "end_turn"}, nil
		}
		return llm.Response{StopReason: "tool_use", Message: llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{
			{Type: "thinking", Text: "the password is " + secret, Meta: map[string]string{"x:signature": "sig"}},
			{Type: "text", Text: "using pw " + secret[:half]},
			{Type: "text", Text: secret[half:] + " now"},
			{Type: "tool_call", ToolCall: &llm.ToolCall{ID: "c1", Name: "echo", Args: json.RawMessage(`{"q":"` + secret + `","n":[1,"x ` + secret + `"]}`)}},
		}}}, nil
	}
	a, err := kernel.Use[*agent.Registry](r, agent.ServiceKey).Create(agent.Spec{Name: "Kai"})
	if err != nil {
		t.Fatal(err)
	}
	ss, _ := a.Session()
	if _, err := a.Enqueue(agent.Input{Text: "go"}); err != nil {
		t.Fatal(err)
	}
	if !agentloop.WaitIdle(a, 3*time.Second) {
		t.Fatal("not idle")
	}
	var sawAssistant, sawCall bool
	for _, e := range ss.Events() {
		if strings.Contains(string(e.Data), secret) || strings.Contains(string(e.Data), secret[half:]) {
			t.Fatalf("secret in %s: %s", e.Type, e.Data)
		}
		switch e.Type {
		case session.TypeAssistantMessage:
			var m session.AssistantMessage
			e.Decode(&m)
			if strings.Contains(m.Message.Text(), "using pw") {
				sawAssistant = true
				if m.Message.Text() != "using pw [redacted] now" {
					t.Fatalf("text %q", m.Message.Text())
				}
				if m.Message.Parts[0].Meta != nil {
					t.Fatal("changed signed thinking must drop its signature")
				}
			}
		case session.TypeToolCall:
			var c session.ToolCallData
			e.Decode(&c)
			var v map[string]any
			if err := json.Unmarshal(c.Call.Args, &v); err != nil || v["q"] != redact.Placeholder {
				t.Fatalf("args %s %v", c.Call.Args, err)
			}
			sawCall = true
		}
	}
	if !sawAssistant || !sawCall {
		t.Fatal("events missing", sawAssistant, sawCall)
	}
}
