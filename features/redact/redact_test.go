package redact_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/redact"
	"github.com/hedeqiang/crewbase/kernel"
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
