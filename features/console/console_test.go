package console_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/capabilities/notify"
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
		{approval.Plugin, nil}, {approval.DesktopPlugin, nil}, {rules.Plugin, nil}, {notify.Plugin, nil},
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

func TestSystemAndProfileSettings(t *testing.T) {
	base, _, _ := start(t)
	code, out := do(t, base, "GET", "/api/system", nil)
	if code != 200 || out["version"] == "" || out["hostname"] == nil || out["startedAt"] == nil {
		t.Fatal(code, out)
	}
	until := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if code, out := do(t, base, "PATCH", "/api/settings", map[string]any{"displayName": "林夏", "dndUntil": until}); code != 200 || out["displayName"] != "林夏" || out["dndUntil"] != until {
		t.Fatal(code, out)
	}
	if code, _ := do(t, base, "PATCH", "/api/settings", map[string]any{"dndUntil": "tomorrow"}); code != 400 {
		t.Fatal("bad dndUntil accepted")
	}
	// turning it off with "" keeps the name
	if _, out := do(t, base, "PATCH", "/api/settings", map[string]any{"dndUntil": ""}); out["displayName"] != "林夏" || out["dndUntil"] != nil {
		t.Fatal(out)
	}
}

func TestFolderRenameAndDissolve(t *testing.T) {
	base, _, h := start(t)
	agents := kernel.Use[*agent.Registry](h.Root(), agent.ServiceKey)
	a, _ := agents.Create(agent.Spec{Name: "A", Folder: "定价改版"})
	b, _ := agents.Create(agent.Spec{Name: "B", Folder: "定价改版"})
	agents.Create(agent.Spec{Name: "C", Folder: "其他"})
	if code, out := do(t, base, "PATCH", "/api/folders/"+url.PathEscape("定价改版"), map[string]string{"name": "定价 2.0"}); code != 200 || out["moved"].(float64) != 2 {
		t.Fatal(code, out)
	}
	if a.Spec().Folder != "定价 2.0" || b.Spec().Folder != "定价 2.0" {
		t.Fatal(a.Spec().Folder, b.Spec().Folder)
	}
	if code, _ := do(t, base, "DELETE", "/api/folders/"+url.PathEscape("定价 2.0"), nil); code != 200 {
		t.Fatal(code)
	}
	if a.Spec().Folder != "" {
		t.Fatal("folder not dissolved")
	}
	if _, ok := agents.Get(a.ID()); !ok {
		t.Fatal("dissolving a folder must not delete its agents")
	}
	if code, _ := do(t, base, "DELETE", "/api/folders/nope", nil); code != 404 {
		t.Fatal(code)
	}
}

func TestNotifySettingsPersistAndApply(t *testing.T) {
	base, root, h := start(t)
	code, out := do(t, base, "GET", "/api/notify", nil)
	m := out["matrix"].(map[string]any)
	if code != 200 || m["task-done"].(map[string]any)["desktop"] != true || m["model-switch"].(map[string]any)["desktop"] != false {
		t.Fatal(code, out)
	}
	prefs := map[string]any{"matrix": map[string]any{"task-done": map[string]any{"desktop": false}}, "quiet": map[string]any{"on": true, "start": "22:00", "end": "08:00"}}
	if code, out := do(t, base, "PATCH", "/api/settings", map[string]any{"notify": prefs}); code != 200 || out["notify"] == nil {
		t.Fatal(code, out)
	}
	for _, bad := range []map[string]any{
		{"quiet": map[string]any{"on": true, "start": "22", "end": "08:00"}},
		{"matrix": map[string]any{"bogus": map[string]any{"desktop": true}}},
	} {
		if code, _ := do(t, base, "PATCH", "/api/settings", map[string]any{"notify": bad}); code != 400 {
			t.Fatalf("accepted %v", bad)
		}
	}
	_, out = do(t, base, "GET", "/api/notify", nil)
	if out["matrix"].(map[string]any)["task-done"].(map[string]any)["desktop"] != false || out["quiet"].(map[string]any)["start"] != "22:00" {
		t.Fatal(out)
	}
	n := kernel.Use[*notify.Service](h.Root(), notify.ServiceKey)
	if n.Enabled(notify.KindTaskDone, "desktop") || !n.Prefs().Quiet.On {
		t.Fatal("prefs not applied")
	}
	b, _ := os.ReadFile(filepath.Join(root, "settings.json"))
	if !strings.Contains(string(b), `"quiet"`) {
		t.Fatalf("not persisted: %s", b)
	}
	// other settings edits keep the choices
	do(t, base, "PATCH", "/api/settings", map[string]any{"displayName": "林夏"})
	if n.Enabled(notify.KindTaskDone, "desktop") {
		t.Fatal("prefs lost")
	}
	if code, _ := do(t, base, "POST", "/api/notify/test", map[string]any{"channel": "desktop"}); code != 200 {
		t.Fatal(code)
	}
	if code, _ := do(t, base, "POST", "/api/notify/test", map[string]any{"channel": "telegram"}); code != 400 {
		t.Fatal("unconnected channel")
	}
}

func TestEmptyFoldersPersist(t *testing.T) {
	base, root, h := start(t)
	agents := kernel.Use[*agent.Registry](h.Root(), agent.ServiceKey)
	agents.Create(agent.Spec{Name: "A", Folder: "已有"})
	for _, bad := range []string{"", "   ", "a/b", strings.Repeat("长", 61)} {
		if code, _ := do(t, base, "POST", "/api/folders", map[string]string{"name": bad}); code != 400 {
			t.Fatalf("accepted %q", bad)
		}
	}
	if code, out := do(t, base, "POST", "/api/folders", map[string]string{"name": " 新项目 "}); code != 200 || out["name"] != "新项目" {
		t.Fatal(code, out)
	}
	do(t, base, "POST", "/api/folders", map[string]string{"name": "新项目"}) // idempotent
	list := func() map[string]float64 {
		_, out := do(t, base, "GET", "/api/folders", nil)
		m := map[string]float64{}
		for _, f := range out["folders"].([]any) {
			f := f.(map[string]any)
			m[f["name"].(string)] = f["count"].(float64)
		}
		return m
	}
	if got := list(); len(got) != 2 || got["新项目"] != 0 || got["已有"] != 1 {
		t.Fatal(got)
	}
	// renaming an empty folder keeps it
	if code, _ := do(t, base, "PATCH", "/api/folders/"+url.PathEscape("新项目"), map[string]string{"name": "Q4"}); code != 200 {
		t.Fatal(code)
	}
	if got := list(); got["Q4"] != 0 || len(got) != 2 {
		t.Fatal(got)
	}
	b, _ := os.ReadFile(filepath.Join(root, "settings.json"))
	if !strings.Contains(string(b), `"Q4"`) || strings.Contains(string(b), "新项目") {
		t.Fatalf("%s", b)
	}
	// other settings edits keep folders
	do(t, base, "PATCH", "/api/settings", map[string]any{"displayName": "x"})
	if _, ok := list()["Q4"]; !ok {
		t.Fatal("folder lost")
	}
	// dissolving an empty folder removes it
	if code, _ := do(t, base, "DELETE", "/api/folders/Q4", nil); code != 200 {
		t.Fatal(code)
	}
	if code, _ := do(t, base, "DELETE", "/api/folders/Q4", nil); code != 404 {
		t.Fatal(code)
	}
	if got := list(); len(got) != 1 {
		t.Fatal(got)
	}
}
