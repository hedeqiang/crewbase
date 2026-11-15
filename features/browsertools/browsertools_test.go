package browsertools_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/browser"
	"github.com/hedeqiang/crewbase/capabilities/notify"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/browsertools"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/browser/chrome"
)

const token = "test-token-0123456789abcdef0123456789"

func site(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	page := func(title, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, "<!doctype html><title>%s</title><body>%s</body>", title, body)
		}
	}
	mux.HandleFunc("/{$}", page("Home", `<h1>Welcome</h1><p>Some intro text.</p>
		<a href="/form">Next page</a>
		<button onclick="document.getElementById('out').textContent='pressed!'">Press me</button>
		<div id=out></div><div style="display:none">invisible text</div>`))
	mux.HandleFunc("/form", page("Form", `<form action="/search">
		<label for=q>Query</label><input id=q name=q>
		<input type=password name=pw placeholder="Password">
		<button type=submit>Go</button></form>`))
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		page("Results", "<p>results for "+r.URL.Query().Get("q")+"</p>")(w, r)
	})
	mux.HandleFunc("/login", page("Sign in", `<h2>Sign in to continue</h2>
		<input name=user placeholder=Email><input type=password name=pass placeholder=Password>
		<p>Enter the verification code we sent you.</p><button>Sign in</button>`))
	mux.HandleFunc("/secret", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "human-login", Path: "/", MaxAge: 3600})
		page("Account", "<p>TOPSECRET-BALANCE-4242</p>")(w, r)
	})
	mux.HandleFunc("/who", func(w http.ResponseWriter, r *http.Request) {
		v := "anonymous"
		if c, err := r.Cookie("sid"); err == nil {
			v = c.Value
		}
		page("Who", "<p>user="+v+"</p>")(w, r)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

type env struct {
	h     *kernel.Host
	reg   *tools.Registry
	a     *agent.Agent
	ss    *session.Session
	api   string
	mu    sync.Mutex
	sent  []notify.Notification
	stats []agent.StatusEvent
}

func setup(t *testing.T, toolCfg map[string]any) *env {
	t.Helper()
	if chrome.FindExecutable() == "" {
		t.Skip("no Chrome/Chromium executable found")
	}
	root := t.TempDir()
	e := &env{h: kernel.NewHost(nil)}
	t.Cleanup(e.h.Close)
	addr := make(chan string, 1)
	gateway.Listening.Listen(e.h.Root(), func(a string) { addr <- a })
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, nil}, {prompt.Plugin, nil}, {tools.Plugin, nil},
		{agent.Plugin, map[string]any{"root": root}}, {notify.Plugin, nil},
		{gateway.Plugin, map[string]any{"addr": "127.0.0.1:0", "token": token}},
		{chrome.Plugin, nil}, {browsertools.Plugin, toolCfg}, {browsertools.APIPlugin, nil},
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
	e.api = "http://" + <-addr
	r := e.h.Root()
	notify.Sent.Listen(r, func(n notify.Notification) { e.mu.Lock(); e.sent = append(e.sent, n); e.mu.Unlock() })
	agent.StatusChanged.Listen(r, func(s agent.StatusEvent) { e.mu.Lock(); e.stats = append(e.stats, s); e.mu.Unlock() })
	e.reg = kernel.Use[*tools.Registry](r, tools.ServiceKey)
	a, err := kernel.Use[*agent.Registry](r, agent.ServiceKey).Create(agent.Spec{Name: "Otto"})
	if err != nil {
		t.Fatal(err)
	}
	e.a = a
	if e.ss, err = a.Session(); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) run(t *testing.T, name string, args any) tools.Result {
	t.Helper()
	b, _ := json.Marshal(args)
	exe := e.reg.Run(context.Background(), e.h.Root(), tools.Call{ID: session.NewID("call"), Name: name, Args: b,
		Agent: e.a.ID(), Session: e.ss, Workspace: e.a.Workspace()})
	return exe.Result
}

func (e *env) call(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.api+path, rd)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func want(t *testing.T, r tools.Result, subs ...string) {
	t.Helper()
	if r.IsError {
		t.Fatalf("unexpected error: %s", r.Content)
	}
	for _, s := range subs {
		if !strings.Contains(r.Content, s) {
			t.Fatalf("missing %q in:\n%s", s, r.Content)
		}
	}
}

func TestOpenSnapshotClickTypeBack(t *testing.T) {
	e := setup(t, nil)
	srv := site(t)
	r := e.run(t, browsertools.ToolOpen, map[string]any{"url": srv.URL + "/"})
	want(t, r, "Page: Home", "# Welcome", "Some intro text.", `[1] link "Next page" -> `+srv.URL+"/form", `[2] button "Press me"`)
	if strings.Contains(r.Content, "invisible text") {
		t.Fatal("hidden text must not be in the snapshot")
	}
	want(t, e.run(t, browsertools.ToolClick, map[string]any{"ref": 2}), "Clicked [2]", "pressed!")
	want(t, e.run(t, browsertools.ToolSnapshot, nil), "pressed!")
	r = e.run(t, browsertools.ToolClick, map[string]any{"ref": 1})
	want(t, r, "Page: Form", `[1] input[text] "Query"`, `[2] input[password] "Password"`, `[3] button "Go"`, "Hint:", "browser_request_takeover")

	// Password field refused, nothing typed.
	r = e.run(t, browsertools.ToolType, map[string]any{"ref": 2, "text": "hunter2"})
	if !r.IsError || !strings.Contains(r.Content, "password") || !strings.Contains(r.Content, "browser_request_takeover") {
		t.Fatalf("password typing must be refused: %+v", r)
	}
	r = e.run(t, browsertools.ToolSnapshot, nil)
	if strings.Contains(r.Content, "hunter2") || strings.Contains(r.Content, "(filled)") {
		t.Fatalf("password field must stay empty:\n%s", r.Content)
	}

	want(t, e.run(t, browsertools.ToolType, map[string]any{"ref": 1, "text": "draft"}), `value="draft"`)
	r = e.run(t, browsertools.ToolType, map[string]any{"ref": 1, "text": "crewbase", "submit": true})
	want(t, r, "pressed Enter", "Page: Results", "results for crewbase")
	want(t, e.run(t, browsertools.ToolBack, nil), "Went back", "Page: Form")

	// Stale and bad refs.
	if r := e.run(t, browsertools.ToolClick, map[string]any{"ref": 99}); !r.IsError || !strings.Contains(r.Content, "browser_snapshot") {
		t.Fatalf("stale ref: %+v", r)
	}
	if r := e.run(t, browsertools.ToolOpen, map[string]any{"url": "file:///etc/passwd"}); !r.IsError {
		t.Fatal("file URL must be refused")
	}
	// Login page detection.
	want(t, e.run(t, browsertools.ToolOpen, map[string]any{"url": srv.URL + "/login"}), "login form", "verification code step", "browser_request_takeover")
	// Categories.
	for name, cat := range map[string]string{browsertools.ToolOpen: "read", browsertools.ToolSnapshot: "read", browsertools.ToolClick: "read",
		browsertools.ToolType: "write", browsertools.ToolBack: "read", browsertools.ToolRequestTakeover: "none"} {
		if tl, ok := e.reg.Get(e.a.ID(), name); !ok || tl.Category != cat {
			t.Fatalf("%s category %v", name, tl)
		}
	}
}

func TestTakeoverBlocksToolsAndResumes(t *testing.T) {
	e := setup(t, map[string]any{"takeoverTimeout": "30s"})
	srv := site(t)
	want(t, e.run(t, browsertools.ToolOpen, map[string]any{"url": srv.URL + "/login"}), "login form")

	// Live SSE stream.
	sseCtx, stopSSE := context.WithCancel(context.Background())
	defer stopSSE()
	req, _ := http.NewRequestWithContext(sseCtx, "GET", e.api+"/api/events?token="+token, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sse := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "event: ") {
				sse <- strings.TrimPrefix(sc.Text(), "event: ")
			}
		}
	}()

	// Screenshot not available while the agent is working… but it is idle now.
	if code, body := e.call(t, "GET", "/api/agents/"+e.a.ID()+"/browser/screenshot", nil); code != 200 {
		t.Fatalf("idle screenshot %d %s", code, body)
	}
	// Input before takeover is refused.
	if code, _ := e.call(t, "POST", "/api/agents/"+e.a.ID()+"/browser/input", map[string]any{"type": "key", "text": "x"}); code != 409 {
		t.Fatalf("input without takeover: %d", code)
	}

	done := make(chan tools.Result, 1)
	go func() {
		done <- e.run(t, browsertools.ToolRequestTakeover, map[string]any{"reason": "sign in to the test site"})
	}()
	waitFor(t, "agent waiting", func() bool {
		st, d := e.a.Status()
		return st == agent.StatusWaiting && d == "takeover:sign in to the test site"
	})
	waitFor(t, "needs-you notification", func() bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		return len(e.sent) == 1 && e.sent[0].Kind == notify.KindNeedsYou && e.sent[0].Urgent
	})

	id := e.a.ID()
	code, body := e.call(t, "POST", "/api/agents/"+id+"/browser/takeover", nil)
	var st browser.State
	_ = json.Unmarshal(body, &st)
	if code != 200 || st.TakenOverBy == "" || !strings.HasPrefix(st.TakenOverBy, "desktop:") || st.View != "frames" {
		t.Fatalf("takeover %d %s", code, body)
	}
	waitSSE(t, sse, "browser.state")

	// The local desktop may move the takeover to a window (not opened here:
	// no window pops up in tests; see the provider's window tests).
	_, body = e.call(t, "GET", "/api/agents/"+id+"/browser", nil)
	var view struct {
		CanWindow  bool `json:"canWindow"`
		WindowHere bool `json:"windowHere"`
		Mine       bool `json:"mine"`
	}
	_ = json.Unmarshal(body, &view)
	if !view.Mine || view.WindowHere != view.CanWindow {
		t.Fatalf("browser view %s", body)
	}
	if code, _ := e.call(t, "POST", "/api/agents/"+id+"/browser/window", map[string]any{}); code != 400 {
		t.Fatalf("window without open: %d", code)
	}
	if code, body := e.call(t, "POST", "/api/agents/"+id+"/browser/window", map[string]any{"open": false}); code != 200 {
		t.Fatalf("back to frames (no-op): %d %s", code, body)
	}

	// Every tool is blocked and learns nothing about the page.
	for _, name := range []string{browsertools.ToolSnapshot, browsertools.ToolBack} {
		r := e.run(t, name, nil)
		if !r.IsError || !strings.Contains(r.Content, "the user is controlling the browser") || strings.Contains(r.Content, "Sign in") {
			t.Fatalf("%s during takeover: %+v", name, r)
		}
	}
	if r := e.run(t, browsertools.ToolOpen, map[string]any{"url": srv.URL + "/"}); !r.IsError || !strings.Contains(r.Content, "controlling") {
		t.Fatalf("open during takeover: %+v", r)
	}
	if r := e.run(t, browsertools.ToolClick, map[string]any{"ref": 1}); !r.IsError || !strings.Contains(r.Content, "controlling") {
		t.Fatalf("click during takeover: %+v", r)
	}

	// The person works through frames + input.
	code, body = e.call(t, "POST", "/api/agents/"+id+"/browser/input", map[string]any{"type": "navigate", "url": srv.URL + "/secret?token=abc"})
	if code != 200 {
		t.Fatalf("input navigate %d %s", code, body)
	}
	for _, in := range []map[string]any{{"type": "click", "x": 10, "y": 10}, {"type": "key", "text": "Tab"}, {"type": "scroll", "dy": 100}} {
		if code, body := e.call(t, "POST", "/api/agents/"+id+"/browser/input", in); code != 200 {
			t.Fatalf("input %v: %d %s", in, code, body)
		}
	}
	if code, _ := e.call(t, "POST", "/api/agents/"+id+"/browser/input", map[string]any{"type": "teleport"}); code != 400 {
		t.Fatalf("bad input type: %d", code)
	}
	code, body = e.call(t, "GET", "/api/agents/"+id+"/browser/screenshot", nil)
	var shot browsertools.Screenshot
	_ = json.Unmarshal(body, &shot)
	img, _ := base64.StdEncoding.DecodeString(shot.Data)
	if code != 200 || shot.Mime != "image/png" || !bytes.HasPrefix(img, []byte("\x89PNG")) || shot.Width == 0 || shot.Title != "Account" {
		t.Fatalf("screenshot %d %+v", code, shot.Title)
	}
	// raw frames: the PNG itself, size in headers, never cached
	rq, _ := http.NewRequest("GET", e.api+"/api/agents/"+id+"/browser/screenshot?raw=1", nil)
	rq.Header.Set("Authorization", "Bearer "+token)
	rs, err := http.DefaultClient.Do(rq)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(rs.Body)
	rs.Body.Close()
	if rs.StatusCode != 200 || rs.Header.Get("Content-Type") != "image/png" || !bytes.HasPrefix(raw, []byte("\x89PNG")) ||
		rs.Header.Get("X-Crewbase-Width") != strconv.Itoa(shot.Width) || rs.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("raw frame %d %v", rs.StatusCode, rs.Header)
	}
	select {
	case r := <-done:
		t.Fatalf("request_takeover returned before handback: %+v", r)
	default:
	}

	code, body = e.call(t, "POST", "/api/agents/"+id+"/browser/handback", nil)
	if code != 200 {
		t.Fatalf("handback %d %s", code, body)
	}
	var r tools.Result
	select {
	case r = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("request_takeover did not resume on handback")
	}
	want(t, r, "control returned; page now at "+srv.URL+"/secret ", `"Account"`)
	if strings.Contains(r.Content, "token=abc") {
		t.Fatal("query strings must not be echoed")
	}
	if st, _ := e.a.Status(); st != agent.StatusRunning {
		t.Fatalf("status after handback: %v", st)
	}
	// The agent continues with the person's login (in the profile).
	want(t, e.run(t, browsertools.ToolOpen, map[string]any{"url": srv.URL + "/who"}), "user=human-login")

	// Nothing the person saw during the takeover reached the session log.
	for _, ev := range e.ss.Events() {
		if strings.Contains(string(ev.Data), "TOPSECRET") {
			t.Fatalf("takeover content leaked into the log: %s", ev.Data)
		}
	}
	// Handback by nobody / twice.
	if code, _ := e.call(t, "POST", "/api/agents/"+id+"/browser/handback", nil); code != 409 {
		t.Fatalf("second handback: %d", code)
	}
	if code, _ := e.call(t, "GET", "/api/agents/nobody/browser", nil); code != 404 {
		t.Fatalf("unknown agent: %d", code)
	}
}

func TestRequestTakeoverTimesOut(t *testing.T) {
	e := setup(t, map[string]any{"takeoverTimeout": "300ms"})
	r := e.run(t, browsertools.ToolRequestTakeover, map[string]any{"reason": "captcha"})
	if !r.IsError || !strings.Contains(r.Content, "nobody took over") {
		t.Fatalf("%+v", r)
	}
	if st, _ := e.a.Status(); st == agent.StatusWaiting {
		t.Fatal("status must not stay waiting")
	}
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitSSE(t *testing.T, ch <-chan string, typ string) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev == typ {
				return
			}
		case <-timeout:
			t.Fatalf("no %s SSE event", typ)
		}
	}
}

func TestHostAllowed(t *testing.T) {
	allow := []string{"example.com", ".docs.io", "*.cdn.net"}
	for host, want := range map[string]bool{
		"example.com": true, "EXAMPLE.com.": true, "www.example.com": false,
		"docs.io": true, "a.docs.io": true, "cdn.net": true, "x.y.cdn.net": true,
		"evil-docs.io": false, "docs.io.evil.com": false, "": false,
	} {
		if got := browsertools.HostAllowed(allow, host); got != want {
			t.Errorf("%q: %v", host, got)
		}
	}
}

// In confined modes the browser only loads allowed hosts; a link to
// another host lands on about:blank with an explanation.
func TestBrowserAllowlist(t *testing.T) {
	outside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><title>Outside</title><body>secret page</body></html>`)
	}))
	defer outside.Close()
	// "localhost" is allowed; the outside server is reached via 127.0.0.1
	inside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<html><title>Inside</title><body><a href="%s/x">leave</a></body></html>`, outside.URL)
	}))
	defer inside.Close()
	insideURL := strings.Replace(inside.URL, "127.0.0.1", "localhost", 1)
	e := setup(t, map[string]any{"allowHosts": []string{"localhost"}})
	r := e.run(t, browsertools.ToolOpen, map[string]any{"url": insideURL})
	if r.IsError || !strings.Contains(r.Content, "Inside") {
		t.Fatalf("allowed open: %+v", r)
	}
	if r := e.run(t, browsertools.ToolOpen, map[string]any{"url": outside.URL}); !r.IsError || !strings.Contains(r.Content, "not on this agent's allowed hosts") {
		t.Fatalf("blocked open: %+v", r)
	}
	// follow the link out: blocked after the fact, page cleared
	e.run(t, browsertools.ToolOpen, map[string]any{"url": insideURL})
	r = e.run(t, browsertools.ToolClick, map[string]any{"ref": 1})
	if !r.IsError || !strings.Contains(r.Content, "allowed hosts") {
		t.Fatalf("click out: %+v", r)
	}
	if snap := e.run(t, browsertools.ToolSnapshot, map[string]any{}); strings.Contains(snap.Content, "secret page") {
		t.Fatalf("page outside the allowlist still shown: %s", snap.Content)
	}
}

// Subresources are filtered too: an allowed page cannot pull data from a
// host outside the list (here: a script fetch to the outside server).
func TestBrowserAllowlistSubresources(t *testing.T) {
	var hits atomic.Int32
	outside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Access-Control-Allow-Origin", "*")
		fmt.Fprint(w, "leaked")
	}))
	defer outside.Close()
	inside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<html><title>Inside</title><body><img src="%s/pixel.png">
<script>fetch(%q).then(r=>r.text()).then(t=>document.title="got "+t).catch(()=>document.title="blocked")</script></body></html>`, outside.URL, outside.URL+"/data")
	}))
	defer inside.Close()
	insideURL := strings.Replace(inside.URL, "127.0.0.1", "localhost", 1)
	e := setup(t, map[string]any{"allowHosts": []string{"localhost"}})
	e.run(t, browsertools.ToolOpen, map[string]any{"url": insideURL})
	time.Sleep(700 * time.Millisecond)
	snap := e.run(t, browsertools.ToolSnapshot, map[string]any{})
	if !strings.Contains(snap.Content, "blocked") || strings.Contains(snap.Content, "got leaked") || hits.Load() != 0 {
		t.Fatalf("subresource reached the outside host (%d hits): %s", hits.Load(), snap.Content)
	}
	// a host-mode call lifts the filter
	b, _ := json.Marshal(map[string]any{"url": insideURL})
	e.reg.Run(context.Background(), e.h.Root(), tools.Call{ID: session.NewID("call"), Name: browsertools.ToolOpen, Args: b,
		Agent: e.a.ID(), Session: e.ss, Workspace: e.a.Workspace(), Mode: "host"})
	time.Sleep(700 * time.Millisecond)
	if hits.Load() == 0 {
		t.Fatal("host mode still filtered")
	}
}

// Links that would open a new tab load in the agent's tab instead.
func TestNewTabLinksStayInTab(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/next":
			fmt.Fprint(w, `<html><title>Next page</title><body>arrived</body></html>`)
		case "/js":
			fmt.Fprint(w, `<html><title>JS opener</title><body><button onclick="window.open('/next')">open</button></body></html>`)
		default:
			fmt.Fprint(w, `<html><title>Start</title><body><a href="/next" target="_blank">go</a></body></html>`)
		}
	}))
	defer srv.Close()
	e := setup(t, nil)
	e.run(t, browsertools.ToolOpen, map[string]any{"url": srv.URL})
	if r := e.run(t, browsertools.ToolClick, map[string]any{"ref": 1}); !strings.Contains(r.Content, "Next page") {
		t.Fatalf("target=_blank left the tab: %s", r.Content)
	}
	e.run(t, browsertools.ToolOpen, map[string]any{"url": srv.URL + "/js"})
	if r := e.run(t, browsertools.ToolClick, map[string]any{"ref": 1}); !strings.Contains(r.Content, "Next page") {
		t.Fatalf("window.open left the tab: %s", r.Content)
	}
}
