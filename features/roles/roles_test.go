package roles_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/features/roles"
	"github.com/hedeqiang/crewbase/kernel"
)

func hermes() []roles.File {
	f := func(p, s string) roles.File { return roles.File{Path: ".hermes/" + p, Data: []byte(s)} }
	return []roles.File{
		f("SOUL.md", "# 小研\n\n你是一位严谨的行业研究员，回答先给结论再给出处。\n"),
		f("config.yaml", "model: anthropic/claude\napi_key: sk-should-not-move\n"),
		f(".env", "OPENROUTER_API_KEY=sk-secret\n"),
		f("auth.json", `{"token":"x"}`),
		f("memories/MEMORY.md", "用户喜欢简短回答"),
		f("memories/USER.md", "用户在上海"),
		f("sessions/2026-11-01.jsonl", `{"role":"user","content":"hi"}`),
		f("state.db", "SQLite"),
		f("skills/research/weekly-report/SKILL.md", "---\nname: weekly-report\ndescription: 每周行业周报\n---\n\n1. 搜集本周新闻\n2. 写周报\n"),
		f("skills/research/weekly-report/template.md", "# 周报模板"),
		f("skills/research/weekly-report/scripts/fetch.py", "print('hi')"),
		f("skills/notes/README.md", "no SKILL.md here"),
	}
}

func TestParseHermesKeepsOnlyInstructionsAndSkills(t *testing.T) {
	tp, err := roles.Parse(roles.Source{Files: hermes()})
	if err != nil {
		t.Fatal(err)
	}
	if tp.Format != roles.FormatHermes || tp.Name != "小研" || !strings.Contains(tp.Role, "严谨的行业研究员") {
		t.Fatalf("%+v", tp)
	}
	if len(tp.Skills) != 1 || tp.Skills[0].Name != "weekly-report" || tp.Skills[0].Description != "每周行业周报" || len(tp.Skills[0].FileNames) != 3 {
		t.Fatalf("skills %+v", tp.Skills)
	}
	why := map[string]string{}
	for _, s := range tp.Skipped {
		why[s.Path] = s.Reason
	}
	want := map[string]string{
		"config.yaml": roles.SkipSettings, ".env": roles.SkipSecret, "auth.json": roles.SkipSecret,
		"memories/MEMORY.md": roles.SkipMemory, "memories/USER.md": roles.SkipMemory,
		"sessions/2026-11-01.jsonl": roles.SkipConversation, "state.db": roles.SkipConversation,
		"skills/notes/README.md": roles.SkipUnsupported,
	}
	for p, r := range want {
		if why[p] != r {
			t.Errorf("%s: got %q want %q (all %v)", p, why[p], r, why)
		}
	}
	// nothing secret or remembered leaks into the instructions
	for _, bad := range []string{"sk-", "上海", "简短回答", "hi"} {
		if strings.Contains(tp.Instructions, bad) {
			t.Fatalf("instructions contain %q", bad)
		}
	}
}

func TestParseMarkdownJSONTextAndZip(t *testing.T) {
	md, err := roles.Parse(roles.Source{Files: []roles.File{{Path: "role.md", Data: []byte("\uFEFF---\nname: 客服小美\ndescription: 处理退款与售后\n---\n\n先确认订单号。\n")}}})
	if err != nil || md.Format != roles.FormatMarkdown || md.Name != "客服小美" || md.Role != "处理退款与售后" || md.Instructions != "先确认订单号。" {
		t.Fatalf("%v %+v", err, md)
	}
	js, err := roles.Parse(roles.Source{Files: []roles.File{{Path: "bot.json", Data: []byte(`{"name":"Ops","description":"on-call helper","system_prompt":"Be calm.","tasks":[{"name":"Triage Alert","description":"triage a page","steps":["read the alert","check dashboards"]}]}`)}}})
	if err != nil || js.Format != roles.FormatJSON || js.Name != "Ops" || js.Instructions != "Be calm." || len(js.Skills) != 1 || js.Skills[0].Name != "triage-alert" {
		t.Fatalf("%v %+v", err, js)
	}
	tx, err := roles.Parse(roles.Source{Text: "# 写作教练\n\n帮我改稿，语气温和。"})
	if err != nil || tx.Format != roles.FormatText || tx.Name != "写作教练" || tx.Role != "帮我改稿，语气温和" {
		t.Fatalf("%v %+v", err, tx)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range hermes() {
		w, _ := zw.Create("export/" + f.Path)
		w.Write(f.Data)
	}
	big, _ := zw.Create("export/.hermes/skills/research/weekly-report/huge.bin")
	big.Write(bytes.Repeat([]byte("x"), roles.MaxFileBytes+10))
	zw.Close()
	zp, err := roles.Parse(roles.Source{Zip: buf.Bytes()})
	if err != nil || zp.Format != roles.FormatHermes || len(zp.Skills) != 1 {
		t.Fatalf("%v %+v", err, zp)
	}
	found := false
	for _, s := range zp.Skipped {
		found = found || (strings.HasSuffix(s.Path, "huge.bin") && s.Reason == roles.SkipTooLarge)
	}
	if !found {
		t.Fatalf("huge file not reported: %+v", zp.Skipped)
	}
	for _, bad := range []roles.Source{{}, {Text: "   "}, {Files: []roles.File{{Path: "notes/x.py", Data: []byte("x")}}}, {Zip: []byte("not a zip")}} {
		if _, err := roles.Parse(bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	// path traversal never becomes a skill file
	tr, err := roles.Parse(roles.Source{Files: []roles.File{
		{Path: "SOUL.md", Data: []byte("# X\n\nhello")},
		{Path: "../../etc/passwd", Data: []byte("root")},
		{Path: "/abs/SKILL.md", Data: []byte("x")},
	}})
	if err != nil || len(tr.Skills) != 0 || len(tr.Skipped) != 2 {
		t.Fatalf("%v %+v", err, tr)
	}
}

func TestImportCreatesAgentWithSkillsOverHTTP(t *testing.T) {
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	var addr string
	gateway.Listening.Listen(h.Root(), func(a string) { addr = a })
	root := t.TempDir()
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, map[string]any{"dir": root + "/sessions"}}, {prompt.Plugin, nil}, {tools.Plugin, nil}, {llm.Plugin, nil},
		{agent.Plugin, map[string]any{"root": root}},
		{gateway.Plugin, map[string]any{"addr": "127.0.0.1:0", "token": "t0k"}},
		{roles.Plugin, nil},
	} {
		if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
			t.Fatal(err)
		}
	}
	post := func(path string, body any) (int, map[string]any) {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", "http://"+addr+path, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer t0k")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return resp.StatusCode, out
	}
	src := map[string]any{"files": hermes()}
	if code, out := post("/api/agents/import/preview", src); code != 200 || out["name"] != "小研" {
		t.Fatal(code, out)
	}
	agents := kernel.Use[*agent.Registry](h.Root(), agent.ServiceKey)
	if len(agents.List()) != 0 {
		t.Fatal("preview must not create anything")
	}
	if code, _ := post("/api/agents/import", map[string]any{"files": hermes(), "options": map[string]any{"mode": "host"}}); code != 400 {
		t.Fatal("host mode accepted")
	}
	code, out := post("/api/agents/import", map[string]any{"files": hermes(), "options": map[string]any{"name": "研究员", "folder": "研究"}})
	if code != 200 || out["skills"].(float64) != 1 {
		t.Fatal(code, out)
	}
	a, ok := agents.Get(out["agent"].(string))
	if !ok || a.Spec().Name != "研究员" || a.Spec().Folder != "研究" || !strings.Contains(a.Instructions(), "严谨的行业研究员") {
		t.Fatalf("%+v", a.Spec())
	}
	if strings.Contains(a.Memory(), "上海") || strings.Contains(a.Memory(), "简短") {
		t.Fatal("memory was migrated")
	}
	sk := filepath.Join(a.Workspace(), "skills", "weekly-report")
	for _, f := range []string{"SKILL.md", "template.md", "scripts/fetch.py"} {
		fi, err := os.Stat(filepath.Join(sk, f))
		if err != nil || fi.Mode()&0o111 != 0 {
			t.Fatalf("%s: %v %v", f, err, fi)
		}
	}
	if _, err := os.Stat(filepath.Join(a.Workspace(), ".env")); err == nil {
		t.Fatal("secret copied")
	}
	// oversize bodies are refused, not truncated
	huge := map[string]any{"text": strings.Repeat("x", 13<<20)}
	if code, _ := post("/api/agents/import/preview", huge); code != 413 {
		t.Fatalf("got %d", code)
	}
}
