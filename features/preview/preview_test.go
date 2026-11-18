package preview_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/features/preview"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T, u string) (int, string, http.Header) {
	t.Helper()
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := cl.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b), res.Header
}

// raw sends a request line as is (no client-side path cleaning).
func raw(t *testing.T, base, target, host string) int {
	t.Helper()
	u, _ := url.Parse(base)
	c, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if host == "" {
		host = u.Host
	}
	fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", target, host)
	res, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

// site lays out a workspace with a built single-page app in dist/.
func site(t *testing.T) string {
	ws := t.TempDir()
	write(t, filepath.Join(ws, "dist/index.html"), "<h1>app</h1>")
	write(t, filepath.Join(ws, "dist/assets/app.js"), "console.log(1)")
	write(t, filepath.Join(ws, "dist/docs/guide.html"), "guide")
	write(t, filepath.Join(ws, "dist/empty/readme.txt"), "x")
	write(t, filepath.Join(ws, "dist/.env"), "SECRET=1")
	write(t, filepath.Join(ws, "secret.txt"), "outside the root")
	return ws
}

func TestStaticServer(t *testing.T) {
	ws := site(t)
	m := preview.NewManager(0, 0)
	defer m.Close()
	base, err := m.Serve("kai", filepath.Join(ws, "dist"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(base, "http://127.0.0.1:") {
		t.Fatalf("must listen on 127.0.0.1: %s", base)
	}
	// same root → same server; another root → another origin
	if again, _ := m.Serve("kai", filepath.Join(ws, "dist")); again != base {
		t.Fatalf("reuse: %s vs %s", again, base)
	}
	other, _ := m.Serve("kai", filepath.Join(ws, "dist/docs"))
	if other == base {
		t.Fatal("another root must get its own origin")
	}

	code, body, h := get(t, base+"/")
	if code != 200 || body != "<h1>app</h1>" || !strings.HasPrefix(h.Get("Content-Type"), "text/html") {
		t.Fatalf("index: %d %q %v", code, body, h)
	}
	if h.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("no CORS")
	}
	if code, body, h = get(t, base+"/assets/app.js"); code != 200 || body != "console.log(1)" || !strings.Contains(h.Get("Content-Type"), "javascript") {
		t.Fatalf("asset: %d %q %v", code, body, h)
	}
	// SPA routes fall back to index.html; missing files do not
	if code, body, _ = get(t, base+"/users/42"); code != 200 || body != "<h1>app</h1>" {
		t.Fatalf("spa fallback: %d %q", code, body)
	}
	if code, _, _ = get(t, base+"/assets/missing.js"); code != 404 {
		t.Fatalf("missing asset: %d", code)
	}
	// a folder: its index.html (after a slash redirect), never a listing
	if code, _, h = get(t, base+"/docs"); code != 404 && code != 200 {
		t.Fatalf("docs: %d", code)
	}
	if code, body, _ = get(t, base+"/empty/"); code != 200 || strings.Contains(body, "readme.txt") {
		t.Fatalf("listing: %d %q", code, body)
	}
	if code, body, _ = get(t, base+"/assets/"); strings.Contains(body, "app.js") {
		t.Fatalf("listing: %d %q", code, body)
	}
	// dotfiles
	if code, _, _ = get(t, base+"/.env"); code != 404 {
		t.Fatalf(".env: %d", code)
	}
	// read-only
	res, err := http.Post(base+"/index.html", "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST: %d", res.StatusCode)
	}
	// no ../ escape, raw or encoded
	for _, p := range []string{"/../secret.txt", "/%2e%2e/secret.txt", "/assets/../../secret.txt", "/..%2fsecret.txt"} {
		if code := raw(t, base, p, ""); code == 200 {
			t.Fatalf("%s escaped the root", p)
		}
	}
	// a foreign Host (DNS rebinding) is refused
	if code := raw(t, base, "/", "evil.example:"+strings.TrimPrefix(base, "http://127.0.0.1:")); code != 403 {
		t.Fatalf("foreign host: %d", code)
	}
	if code := raw(t, base, "/", "localhost:"+strings.TrimPrefix(base, "http://127.0.0.1:")); code != 200 {
		t.Fatalf("localhost host: %d", code)
	}
}

func TestStaticServerNestedIndex(t *testing.T) {
	ws := site(t)
	write(t, filepath.Join(ws, "dist/docs/index.html"), "docs home")
	m := preview.NewManager(0, 0)
	defer m.Close()
	base, _ := m.Serve("kai", filepath.Join(ws, "dist"))
	if code, _, h := get(t, base+"/docs?x=1"); code != http.StatusMovedPermanently || h.Get("Location") != "/docs/?x=1" {
		t.Fatalf("redirect: %d %v", code, h)
	}
	if code, body, _ := get(t, base+"/docs/"); code != 200 || body != "docs home" {
		t.Fatalf("%d %q", code, body)
	}
}

func TestStaticServerSymlinks(t *testing.T) {
	ws := site(t)
	root := filepath.Join(ws, "dist")
	if err := os.Symlink(filepath.Join(ws, "secret.txt"), filepath.Join(root, "leak.txt")); err != nil {
		t.Skip(err)
	}
	os.Symlink(ws, filepath.Join(root, "up"))
	os.Symlink(filepath.Join(root, "assets/app.js"), filepath.Join(root, "alias.js"))
	m := preview.NewManager(0, 0)
	defer m.Close()
	base, _ := m.Serve("kai", root)
	for _, p := range []string{"/leak.txt", "/up/secret.txt"} {
		if code, body, _ := get(t, base+p); code == 200 || strings.Contains(body, "outside") {
			t.Fatalf("%s followed a symlink out of the root: %d %q", p, code, body)
		}
	}
	if code, body, _ := get(t, base+"/alias.js"); code != 200 || body != "console.log(1)" {
		t.Fatalf("a symlink inside the root is fine: %d %q", code, body)
	}
}

func TestIdleShutdown(t *testing.T) {
	ws := site(t)
	m := preview.NewManager(150*time.Millisecond, 20*time.Millisecond)
	defer m.Close()
	base, _ := m.Serve("kai", filepath.Join(ws, "dist"))
	// requests keep it alive
	for range 5 {
		time.Sleep(60 * time.Millisecond)
		if code, _, _ := get(t, base+"/"); code != 200 {
			t.Fatal(code)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(m.Running("")) > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if len(m.Running("")) != 0 {
		t.Fatal("idle server not closed")
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := http.Get(base + "/"); err == nil {
		t.Fatal("closed server still answers")
	}
	// opening again starts a fresh one
	if b2, err := m.Serve("kai", filepath.Join(ws, "dist")); err != nil || b2 == "" {
		t.Fatal(err)
	}
}

func TestCloseAll(t *testing.T) {
	ws := site(t)
	m := preview.NewManager(0, 0)
	a, _ := m.Serve("a", filepath.Join(ws, "dist"))
	b, _ := m.Serve("b", filepath.Join(ws, "dist"))
	if a == b {
		t.Fatal("agents do not share servers")
	}
	m.CloseAgent("a")
	if got := m.Running(""); len(got) != 1 || got[0] != b {
		t.Fatal(got)
	}
	m.Close()
	if len(m.Running("")) != 0 {
		t.Fatal("close")
	}
	if _, err := m.Serve("a", filepath.Join(ws, "dist")); err == nil {
		t.Fatal("serve after close")
	}
}

func TestResolve(t *testing.T) {
	ws := site(t)
	write(t, filepath.Join(ws, "page.html"), "p")
	for _, c := range []struct{ in, root, page, rel string }{
		{"dist", "dist", "/", "dist/"},
		{"dist/", "dist", "/", "dist/"},
		{"dist/index.html", "dist", "/index.html", "dist/index.html"},
		{"page.html", ".", "/page.html", "page.html"},
		{filepath.Join(ws, "dist/docs/guide.html"), "dist/docs", "/guide.html", "dist/docs/guide.html"},
	} {
		tg, err := preview.Resolve(ws, c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		wsr, _ := filepath.EvalSymlinks(ws)
		if tg.Root != filepath.Join(wsr, c.root) || tg.Page != c.page || tg.Rel != c.rel {
			t.Fatalf("%s: %+v", c.in, tg)
		}
	}
	for in, want := range map[string]string{
		"../":          "outside the workspace",
		"/etc/hosts":   "outside the workspace",
		"missing.html": "does not exist",
		"dist/empty":   "no index.html",
		"dist/../../x": "",
		"":             "no index.html",
	} {
		_, err := preview.Resolve(ws, in)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%q: %v", in, err)
		}
	}
	if _, err := preview.Resolve("", "x.html"); err == nil {
		t.Fatal("no workspace")
	}
}

func TestNormalizeURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://localhost:5173":     "http://localhost:5173",
		"http://0.0.0.0:5173/app":   "http://127.0.0.1:5173/app",
		"https://example.com/x?y=1": "https://example.com/x?y=1",
	} {
		if got, err := preview.NormalizeURL(in); err != nil || got != want {
			t.Fatalf("%s → %s %v", in, got, err)
		}
	}
	for _, in := range []string{"file:///etc/passwd", "javascript:alert(1)", "localhost:5173", "http://", "http://u:p@127.0.0.1:1/"} {
		if _, err := preview.NormalizeURL(in); err == nil {
			t.Fatalf("%s accepted", in)
		}
	}
}

// ---------------------------------------------------------------- plugin

const tok = "tok-desktop"

type env struct {
	h      *kernel.Host
	agents *agent.Registry
	reg    *tools.Registry
	gw     *gateway.Gateway
	m      *preview.Manager
}

func start(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, map[string]any{"dir": filepath.Join(root, "sessions")}}, {prompt.Plugin, nil}, {tools.Plugin, nil},
		{approval.Plugin, nil}, {approval.DesktopPlugin, nil}, {llm.Plugin, nil}, {mock.Plugin, nil},
		{agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"mock/default"}}},
		{gateway.Plugin, map[string]any{"addr": "127.0.0.1:0", "token": tok}},
		{preview.Plugin, nil}, {preview.APIPlugin, nil},
	} {
		p, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg})
		if err != nil {
			t.Fatal(err)
		}
		if info, _ := h.Plugin(p.ID()); info.State != kernel.StateActive {
			t.Fatalf("%+v", info)
		}
	}
	r := h.Root()
	return &env{h: h, agents: kernel.Use[*agent.Registry](r, agent.ServiceKey), reg: kernel.Use[*tools.Registry](r, tools.ServiceKey),
		gw: kernel.Use[*gateway.Gateway](r, gateway.ServiceKey), m: kernel.Use[*preview.Manager](r, preview.ServiceKey)}
}

func (e *env) run(t *testing.T, a *agent.Agent, ss *session.Session, args any) tools.Execution {
	t.Helper()
	b, _ := json.Marshal(args)
	return e.reg.Run(context.Background(), e.h.Root(), tools.Call{ID: session.NewID("call"), Name: "preview_open", Args: b, Agent: a.ID(), Session: ss, Mode: "sandbox", Workspace: a.Workspace()})
}

func (e *env) api(t *testing.T, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	e.gw.ServeHTTP(w, req)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func opened(ss *session.Session) []preview.Opened {
	var out []preview.Opened
	for _, ev := range ss.Events() {
		if ev.Type == preview.EventOpened {
			var o preview.Opened
			ev.Decode(&o)
			out = append(out, o)
		}
	}
	return out
}

func TestTool(t *testing.T) {
	preview.URLWait = 600 * time.Millisecond
	e := start(t)
	a, err := e.agents.Create(agent.Spec{Name: "Ada"})
	if err != nil {
		t.Fatal(err)
	}
	ss, _ := a.Session()
	write(t, filepath.Join(a.Workspace(), "site/index.html"), "<p>hello</p>")
	write(t, filepath.Join(a.Workspace(), "site/about.html"), "about")

	tl, ok := e.reg.Get(a.ID(), "preview_open")
	if !ok {
		t.Fatal("preview_open not registered")
	}
	if cat, _ := tl.Classify(tools.Call{Args: json.RawMessage(`{"path":"site"}`)}); cat != tools.CategoryRead {
		t.Fatal(cat)
	}
	for _, w := range []string{"npm create vite", "background: true", "127.0.0.1", "dist"} {
		if !strings.Contains(tl.Description, w) {
			t.Fatalf("description lacks %q", w)
		}
	}

	// a workspace path → a static server, and an event
	exe := e.run(t, a, ss, map[string]string{"path": "site/about.html", "title": "About"})
	if exe.Result.IsError {
		t.Fatal(exe.Result.Content)
	}
	evs := opened(ss)
	if len(evs) != 1 || evs[0].Kind != "path" || evs[0].Path != "site/about.html" || evs[0].Title != "About" || evs[0].Source != "tool" || evs[0].Agent != a.ID() {
		t.Fatalf("%+v", evs)
	}
	if !strings.HasPrefix(evs[0].URL, "http://127.0.0.1:") || !strings.HasSuffix(evs[0].URL, "/about.html") || !strings.Contains(exe.Result.Content, evs[0].URL) {
		t.Fatalf("%+v %s", evs[0], exe.Result.Content)
	}
	if code, body, _ := get(t, evs[0].URL); code != 200 || body != "about" {
		t.Fatal(code, body)
	}

	// a URL: as is (0.0.0.0 → 127.0.0.1) once something answers there
	dev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "vite") }))
	defer dev.Close()
	devURL := strings.Replace(dev.URL, "127.0.0.1", "0.0.0.0", 1)
	if exe = e.run(t, a, ss, map[string]string{"url": devURL}); exe.Result.IsError {
		t.Fatal(exe.Result.Content)
	}
	evs = opened(ss)
	if len(evs) != 2 || evs[1].Kind != "url" || evs[1].URL != dev.URL || evs[1].Title != strings.TrimPrefix(dev.URL, "http://") {
		t.Fatalf("%+v", evs)
	}

	// nothing listening: refused, nothing recorded
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := "http://" + ln.Addr().String()
	ln.Close()
	exe = e.run(t, a, ss, map[string]string{"url": dead})
	if !exe.Result.IsError || !strings.Contains(exe.Result.Content, "Nothing answers") {
		t.Fatal(exe.Result.Content)
	}

	// bad arguments
	for _, args := range []map[string]string{
		{},
		{"path": "site", "url": dev.URL},
		{"path": "../"},
		{"path": "/etc/hosts"},
		{"path": "nope.html"},
		{"path": "."}, // no index.html at the workspace root
		{"url": "file:///etc/passwd"},
		{"url": "javascript:alert(1)"},
	} {
		if exe = e.run(t, a, ss, args); !exe.Result.IsError {
			t.Fatalf("%v: %s", args, exe.Result.Content)
		}
	}
	if n := len(opened(ss)); n != 2 {
		t.Fatalf("refused calls must not be recorded: %d", n)
	}

	// the session's list: newest first
	code, out := e.api(t, "GET", "/api/sessions/"+ss.ID()+"/previews", nil)
	list, _ := out["previews"].([]any)
	if code != 200 || len(list) != 2 || list[0].(map[string]any)["kind"] != "url" {
		t.Fatal(code, out)
	}

	// deleting the agent closes its servers
	if len(e.m.Running(a.ID())) != 1 {
		t.Fatal(e.m.Running(a.ID()))
	}
	if err := e.agents.Delete(a.ID()); err != nil {
		t.Fatal(err)
	}
	if len(e.m.Running(a.ID())) != 0 {
		t.Fatal("servers survive the agent")
	}
	if _, err := http.Get(evs[0].URL); err == nil {
		t.Fatal("deleted agent's server still answers")
	}
}

func TestRoutes(t *testing.T) {
	e := start(t)
	a, _ := e.agents.Create(agent.Spec{Name: "Bo"})
	ss, _ := a.Session()
	write(t, filepath.Join(a.Workspace(), "app/dist/index.html"), "built")

	code, out := e.api(t, "POST", "/api/agents/"+a.ID()+"/preview", map[string]string{"path": "app/dist", "session": ss.ID()})
	if code != 200 || out["kind"] != "path" || out["path"] != "app/dist/" || out["source"] != "files" {
		t.Fatal(code, out)
	}
	if c, body, _ := get(t, out["url"].(string)); c != 200 || body != "built" {
		t.Fatal(c, body)
	}
	if code, _ = e.api(t, "POST", "/api/agents/"+a.ID()+"/preview", map[string]string{"path": "../../"}); code != 400 {
		t.Fatal(code)
	}
	if code, _ = e.api(t, "POST", "/api/agents/nobody/preview", map[string]string{"path": "app/dist"}); code != 404 {
		t.Fatal(code)
	}

	// an HTML code block: written under .crewbase/previews, served from there
	html := "<!doctype html><title>t</title><script>document.title='ok'</script>"
	code, out = e.api(t, "POST", "/api/agents/"+a.ID()+"/preview/html", map[string]string{"html": html, "title": "代码块", "session": ss.ID()})
	if code != 200 || out["source"] != "snippet" || out["title"] != "代码块" || !strings.HasPrefix(out["path"].(string), preview.SnippetDir+"/") {
		t.Fatal(code, out)
	}
	if b, err := os.ReadFile(filepath.Join(a.Workspace(), filepath.FromSlash(out["path"].(string)))); err != nil || string(b) != html {
		t.Fatal(err, string(b))
	}
	if c, body, h := get(t, out["url"].(string)); c != 200 || body != html || !strings.HasPrefix(h.Get("Content-Type"), "text/html") {
		t.Fatal(c, body)
	}
	// the same block again: the same file
	_, again := e.api(t, "POST", "/api/agents/"+a.ID()+"/preview/html", map[string]string{"html": html})
	if again["path"] != out["path"] {
		t.Fatal(again, out)
	}
	if code, _ = e.api(t, "POST", "/api/agents/"+a.ID()+"/preview/html", map[string]string{"html": " "}); code != 400 {
		t.Fatal(code)
	}

	// what the person opened shows in the session's list
	code, out = e.api(t, "GET", "/api/sessions/"+ss.ID()+"/previews", nil)
	list, _ := out["previews"].([]any)
	if code != 200 || len(list) != 2 || list[0].(map[string]any)["source"] != "snippet" {
		t.Fatal(code, out)
	}
	if code, _ = e.api(t, "GET", "/api/sessions/nope/previews", nil); code != 404 {
		t.Fatal(code)
	}
	// desktop token only
	req := httptest.NewRequest("GET", "/api/sessions/"+ss.ID()+"/previews", nil)
	w := httptest.NewRecorder()
	e.gw.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}
