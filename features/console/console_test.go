package console_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/compaction"
	"github.com/hedeqiang/crewbase/features/console"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/features/rules"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

const tok = "console-test-token-0123456789abcdef"

func start(t *testing.T) (string, string, *kernel.Host) {
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
		{session.Plugin, map[string]any{"dir": root + "/sessions"}}, {prompt.Plugin, nil}, {tools.Plugin, nil},
		{approval.Plugin, nil}, {approval.DesktopPlugin, nil}, {rules.Plugin, nil},
		{llm.Plugin, nil}, {mock.Plugin, nil}, {router.Plugin, nil},
		{agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"mock/default"}}},
		{agentloop.Plugin, nil}, {compaction.Plugin, nil},
		{gateway.Plugin, map[string]any{"addr": "127.0.0.1:0", "token": tok}},
		{console.Plugin, map[string]any{"dir": root}},
	} {
		if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range h.Plugins() {
		if p.State != kernel.StateActive {
			t.Fatalf("%+v", p)
		}
	}
	return "http://" + addr, root, h
}

func do(t *testing.T, base, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	req, _ := http.NewRequest(method, base+path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestConsoleEndpoints(t *testing.T) {
	base, root, h := start(t)
	agents := kernel.Use[*agent.Registry](h.Root(), agent.ServiceKey)
	a, _ := agents.Create(agent.Spec{Name: "Kai", Role: "engineer"})
	a.Enqueue(agent.Input{Text: "hello there"})
	agentloop.WaitIdle(a, 2*time.Second)

	// summaries carry the last line
	_, out := do(t, base, "GET", "/api/sessions/summary", nil)
	s0 := out["sessions"].([]any)[0].(map[string]any)
	if s0["last"].(map[string]any)["text"] != "default: hello there" {
		t.Fatal(s0)
	}
	// files: list, read, write, escape refused
	_, out = do(t, base, "GET", "/api/agents/kai/files", nil)
	if len(out["entries"].([]any)) < 2 {
		t.Fatal(out)
	}
	code, _ := do(t, base, "PUT", "/api/agents/kai/file", map[string]string{"path": "INSTRUCTIONS.md", "content": "# Kai\nBe precise.\n"})
	if code != 200 || a.Instructions() != "# Kai\nBe precise.\n" {
		t.Fatal(code, a.Instructions())
	}
	os.Symlink(root, filepath.Join(a.Workspace(), "escape"))
	for _, p := range []string{"../token", "escape/settings.json", "/etc/passwd"} {
		if code, _ := do(t, base, "GET", "/api/agents/kai/file?path="+p, nil); code != 400 {
			t.Fatalf("%s: %d", p, code)
		}
	}
	if code, _ := do(t, base, "PUT", "/api/agents/kai/file", map[string]string{"path": "escape/x", "content": "x"}); code != 400 {
		t.Fatal("write through symlink escape must fail")
	}
	// context + new conversation
	code, out = do(t, base, "GET", "/api/agents/kai/context", nil)
	if code != 200 || out["window"] == nil {
		t.Fatal(code, out)
	}
	old := a.Spec().Session
	code, out = do(t, base, "POST", "/api/agents/kai/sessions", nil)
	if code != 200 || out["id"] == old || a.Spec().Session != out["id"] {
		t.Fatal(code, out)
	}
	// settings: host refused as default; models apply to new agents
	if code, _ := do(t, base, "PATCH", "/api/settings", map[string]any{"defaultMode": "host"}); code != 400 {
		t.Fatal(code)
	}
	do(t, base, "PATCH", "/api/settings", map[string]any{"defaultModels": []string{"mock/default@high"}})
	b, _ := agents.Create(agent.Spec{Name: "Iris"})
	if b.Spec().Models[0].Effort != "high" {
		t.Fatal(b.Spec().Models)
	}
	// remembered choices list + revoke
	e := kernel.Use[*rules.Engine](h.Root(), rules.ServiceKey)
	e.Remember("kai", tools.CategoryPush)
	_, out = do(t, base, "GET", "/api/approvals/remembered", nil)
	if len(out["remembered"].([]any)) != 1 {
		t.Fatal(out)
	}
	if code, _ := do(t, base, "DELETE", "/api/approvals/remembered/kai/push", nil); code != 200 {
		t.Fatal(code)
	}
	if v, _ := e.Evaluate("kai", tools.CategoryPush); v != tools.Ask {
		t.Fatal(v)
	}
	// router routes
	if code, _ := do(t, base, "PUT", "/api/router/routes", map[string]any{"routes": []map[string]string{{"task": "summarize", "model": "mock/default"}}}); code != 200 {
		t.Fatal(code)
	}
	_, out = do(t, base, "GET", "/api/hosts", nil)
	if len(out["hosts"].([]any)) != 1 {
		t.Fatal(out)
	}
}
