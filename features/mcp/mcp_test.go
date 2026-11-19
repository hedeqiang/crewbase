package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/capabilities/sandbox"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/features/mcp"
	"github.com/hedeqiang/crewbase/kernel"
	sandboxnone "github.com/hedeqiang/crewbase/providers/sandbox/none"
	"github.com/hedeqiang/crewbase/providers/sandbox/seatbelt"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type env struct {
	h     *kernel.Host
	dir   string
	svc   *mcp.Service
	tools *tools.Registry
	creds *credentials.Service
}

type load struct {
	spec kernel.Spec
	cfg  map[string]any
}

func newEnv(t *testing.T, cfg map[string]any, extra ...load) *env {
	t.Helper()
	dir := t.TempDir() // first: removed after the host has stopped (cleanups run in reverse)
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	if cfg == nil {
		cfg = map[string]any{}
	}
	if _, ok := cfg["dir"]; !ok {
		cfg["dir"] = dir
	} else {
		dir = cfg["dir"].(string)
	}
	cfg["backoffMin"], cfg["backoffMax"] = "50ms", "200ms"
	ls := []load{{tools.Plugin, nil}, {credentials.Plugin, nil}}
	ls = append(ls, extra...)
	ls = append(ls, load{mcp.Plugin, cfg})
	for _, l := range ls {
		if _, err := h.Load(l.spec, kernel.LoadOptions{Config: l.cfg}); err != nil {
			t.Fatal(err)
		}
	}
	r := h.Root()
	svc, ok := kernel.Lookup[*mcp.Service](r, mcp.ServiceKey)
	if !ok {
		t.Fatalf("mcp service missing: %+v", h.Plugins())
	}
	return &env{h: h, dir: dir, svc: svc, tools: kernel.Use[*tools.Registry](r, tools.ServiceKey), creds: kernel.Use[*credentials.Service](r, credentials.ServiceKey)}
}

func stdioServer(name string, extraEnv map[string]string) mcp.Server {
	e := map[string]string{"CREWBASE_MCP_TEST_SERVER": "1"}
	for k, v := range extraEnv {
		e[k] = v
	}
	return mcp.Server{Name: name, Transport: mcp.TransportStdio, Command: []string{os.Args[0]}, Env: e, Enabled: true, Trust: mcp.TrustHost}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (e *env) waitState(t *testing.T, name, state string) mcp.Status {
	t.Helper()
	var st mcp.Status
	waitFor(t, name+" "+state, func() bool {
		st, _ = e.svc.Status(name)
		return st.State == state
	})
	return st
}

func (e *env) call(agentID, name, args string) tools.Execution {
	return e.tools.Run(context.Background(), e.h.Root(), tools.Call{ID: "c-" + name, Name: name, Args: json.RawMessage(args), Agent: agentID})
}

func (e *env) visible(agentID string) []string {
	var out []string
	for _, t := range e.tools.Visible(agentID) {
		out = append(out, t.Name)
	}
	return out
}

func alive(pid int) bool { return pid > 0 && syscall.Kill(pid, 0) == nil }

func waitDead(t *testing.T, pid int) {
	t.Helper()
	waitFor(t, fmt.Sprintf("pid %d to die", pid), func() bool { return !alive(pid) })
}

// ---------------------------------------------------------------- unit

func TestToolNameAndCategory(t *testing.T) {
	if n := mcp.ToolName("gh", "create.issue/v2"); n != "gh__create_issue_v2" {
		t.Fatal(n)
	}
	long := mcp.ToolName("server", strings.Repeat("x", 100))
	long2 := mcp.ToolName("server", strings.Repeat("x", 99)+"y")
	if len(long) != mcp.MaxToolName || long == long2 || !strings.HasPrefix(long, "server__xxx") {
		t.Fatal(long, long2)
	}
	for _, c := range []struct {
		a     *sdk.ToolAnnotations
		trust bool
		want  string
	}{
		{nil, true, tools.CategoryExec},
		{&sdk.ToolAnnotations{ReadOnlyHint: true}, true, tools.CategoryRead},
		{&sdk.ToolAnnotations{ReadOnlyHint: true}, false, tools.CategoryExec},
		{&sdk.ToolAnnotations{DestructiveHint: ptr(true)}, false, tools.CategoryDelete},
		{&sdk.ToolAnnotations{DestructiveHint: ptr(true)}, true, tools.CategoryDelete},
		{&sdk.ToolAnnotations{DestructiveHint: ptr(false), OpenWorldHint: ptr(true)}, true, tools.CategoryExec},
	} {
		if got := mcp.Category(c.a, c.trust); got != c.want {
			t.Fatalf("%+v trust=%v: %s", c.a, c.trust, got)
		}
	}
	r := mcp.ConvertResult(&sdk.CallToolResult{IsError: true, Content: []sdk.Content{
		&sdk.TextContent{Text: "a"}, &sdk.ImageContent{MIMEType: "image/png", Data: []byte("xyz")},
		&sdk.EmbeddedResource{Resource: &sdk.ResourceContents{URI: "file:///x", Text: "body"}},
		&sdk.ResourceLink{URI: "https://e/x", Name: "x"},
	}})
	if !r.IsError || r.Content != "a\n[image: image/png, 3 bytes]\n[resource file:///x]\nbody\n[resource link: x <https://e/x>]" {
		t.Fatalf("%q", r.Content)
	}
	if r := mcp.ConvertResult(&sdk.CallToolResult{StructuredContent: map[string]any{"k": 1}}); r.Content != `{"k":1}` {
		t.Fatal(r.Content)
	}
}

func TestValidation(t *testing.T) {
	bad := []mcp.Server{
		{Name: "Bad Name", Transport: "stdio", Command: []string{"x"}},
		{Name: "a", Transport: "stdio"},
		{Name: "a", Transport: "http", URL: "ftp://x"},
		{Name: "a", Transport: "http", URL: "https://x", Command: []string{"x"}},
		{Name: "a", Transport: "stdio", Command: []string{"x"}, Trust: "yolo"},
		{Name: "a", Transport: "ws"},
		{Name: "a", Transport: "stdio", Command: []string{"x"}, Scope: "Not An Agent"},
	}
	for _, s := range bad {
		if _, err := s.Normalize(); !errors.Is(err, mcp.ErrInvalid) {
			t.Fatalf("%+v: %v", s, err)
		}
	}
	if _, err := (mcp.Server{Name: "a", Transport: "stdio", Command: []string{"x"}, Categories: map[string]string{"t": "sudo"}}).Normalize(); !errors.Is(err, mcp.ErrInvalid) {
		t.Fatal("unknown category override accepted", err)
	}
	n, err := mcp.Server{Name: "gh", Transport: "stdio", Command: []string{"x"}}.Normalize()
	if err != nil || n.Trust != mcp.TrustSandboxed || n.Scope != mcp.ScopeGlobal {
		t.Fatal(n, err)
	}
}

// ---------------------------------------------------------------- stdio

func TestRegisterCallAndCategories(t *testing.T) {
	e := newEnv(t, nil)
	srv1 := stdioServer("t1", nil)
	srv1.TrustAnnotations = true
	if _, err := e.svc.Add(srv1); err != nil {
		t.Fatal(err)
	}
	st := e.waitState(t, "t1", mcp.StateReady)
	if st.Tools != 2 || st.PID == 0 || st.Enforcement != sandbox.EnforcementNone {
		t.Fatalf("%+v", st)
	}
	for name, cat := range map[string]string{"t1__echo": tools.CategoryRead, "t1__wipe": tools.CategoryDelete} {
		tl, ok := e.tools.Get("any-agent", name)
		if !ok || tl.Category != cat || !strings.HasPrefix(tl.Description, "[MCP t1]") {
			t.Fatalf("%s: %+v", name, tl)
		}
	}
	x := e.call("", "t1__echo", `{"text":"hello"}`)
	if x.Result.IsError || x.Result.Content != "hello" || x.Category != tools.CategoryRead {
		t.Fatalf("%+v", x)
	}
	// required args are validated by the pipeline from the MCP schema
	if x := e.call("", "t1__echo", `{}`); !x.Result.IsError || !strings.Contains(x.Result.Content, "missing required: text") {
		t.Fatalf("%+v", x)
	}
	x = e.call("", "t1__wipe", `{}`)
	if x.Result.IsError || !strings.Contains(x.Result.Content, "[image: image/png, 3 bytes]") {
		t.Fatalf("%+v", x)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "mcp", "t1", "wiped.txt")); err != nil {
		t.Fatal("global server cwd should be ${dir}/mcp/<name>:", err)
	}
	if x := e.call("", "t1__wipe", `{"fail":true}`); !x.Result.IsError || x.Result.Content != "refused" {
		t.Fatalf("%+v", x)
	}
	infos, _ := e.svc.Tools("t1")
	if len(infos) != 2 || infos[0].Original == "" {
		t.Fatalf("%+v", infos)
	}
	// persisted; a new host restores it from mcp.json
	b, err := os.ReadFile(filepath.Join(e.dir, "mcp.json"))
	if err != nil || !strings.Contains(string(b), `"t1"`) {
		t.Fatal(string(b), err)
	}
	if fi, _ := os.Stat(filepath.Join(e.dir, "mcp.json")); fi.Mode().Perm() != 0o600 {
		t.Fatal(fi.Mode())
	}
	e2 := newEnv(t, map[string]any{"dir": e.dir})
	e2.waitState(t, "t1", mcp.StateReady)
	if tl, ok := e2.tools.Get("", "t1__echo"); !ok || tl.Category != tools.CategoryRead {
		t.Fatal("trustAnnotations not persisted", tl)
	}
	if _, err := e2.svc.Add(stdioServer("t1", nil)); !errors.Is(err, mcp.ErrExists) {
		t.Fatal(err)
	}
}

func TestListChangedRefreshAndFailedRefreshKeepsTools(t *testing.T) {
	e := newEnv(t, nil)
	e.svc.Add(stdioServer("lc", nil))
	st := e.waitState(t, "lc", mcp.StateReady)
	syscall.Kill(st.PID, syscall.SIGUSR1)
	waitFor(t, "extra tool", func() bool { return slices.Contains(e.visible(""), "lc__extra") })
	if x := e.call("", "lc__extra", `{}`); x.Result.Content != "extra" {
		t.Fatalf("%+v", x)
	}
	syscall.Kill(st.PID, syscall.SIGUSR2) // tools/list now fails, then list_changed
	waitFor(t, "refresh error", func() bool { s, _ := e.svc.Status("lc"); return s.RefreshError != "" })
	v := e.visible("")
	if !slices.Contains(v, "lc__extra") || !slices.Contains(v, "lc__echo") || slices.Contains(v, "lc__extra2") {
		t.Fatal(v)
	}
	if s, _ := e.svc.Status("lc"); s.State != mcp.StateReady || s.Tools != 3 {
		t.Fatalf("%+v", s)
	}
}

func TestCrashReconnects(t *testing.T) {
	e := newEnv(t, nil)
	e.svc.Add(stdioServer("cr", nil))
	st := e.waitState(t, "cr", mcp.StateReady)
	syscall.Kill(st.PID, syscall.SIGKILL)
	waitFor(t, "reconnect", func() bool {
		s, _ := e.svc.Status("cr")
		return s.State == mcp.StateReady && s.PID != st.PID && s.PID != 0
	})
	if x := e.call("", "cr__echo", `{"text":"back"}`); x.Result.Content != "back" {
		t.Fatalf("%+v", x)
	}
}

func TestDisableRemovesToolsAndKillsTree(t *testing.T) {
	e := newEnv(t, nil)
	childFile := filepath.Join(t.TempDir(), "child.pid")
	e.svc.Add(stdioServer("dk", map[string]string{"CREWBASE_MCP_TEST_CHILD": childFile}))
	st := e.waitState(t, "dk", mcp.StateReady)
	var child int
	waitFor(t, "child pid", func() bool {
		b, _ := os.ReadFile(childFile)
		child, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		return child > 0
	})
	if !alive(st.PID) || !alive(child) {
		t.Fatal("not running")
	}
	v, err := e.svc.SetEnabled("dk", false)
	if err != nil || v.Status.State != mcp.StateDisabled {
		t.Fatal(v, err)
	}
	if slices.ContainsFunc(e.visible(""), func(n string) bool { return strings.HasPrefix(n, "dk__") }) {
		t.Fatal("tools survived disable", e.visible(""))
	}
	waitDead(t, st.PID)
	waitDead(t, child)
	// re-enable, then unloading the plugin kills it too
	e.svc.SetEnabled("dk", true)
	st = e.waitState(t, "dk", mcp.StateReady)
	e.h.Close()
	waitDead(t, st.PID)
	if len(e.visible("")) != 0 {
		t.Fatal(e.visible(""))
	}
}

func TestRemoveAndConfigDeclared(t *testing.T) {
	decl := stdioServer("decl", nil)
	decl.Enabled = false
	e := newEnv(t, map[string]any{"servers": []any{decl}})
	if err := e.svc.Remove("decl"); !errors.Is(err, mcp.ErrDeclared) {
		t.Fatal(err)
	}
	if v, _ := e.svc.Get("decl"); v.Source != "config" || v.Status.State != mcp.StateDisabled {
		t.Fatalf("%+v", v)
	}
	e.svc.Add(stdioServer("rm", nil))
	st := e.waitState(t, "rm", mcp.StateReady)
	if err := e.svc.Remove("rm"); err != nil {
		t.Fatal(err)
	}
	waitDead(t, st.PID)
	if _, ok := e.svc.Get("rm"); ok {
		t.Fatal("still listed")
	}
	if err := e.svc.Remove("rm"); !errors.Is(err, mcp.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestProbeRegistersNothing(t *testing.T) {
	e := newEnv(t, nil)
	list, err := e.svc.Probe(context.Background(), stdioServer("pr", nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "pr__echo" || list[0].Category != tools.CategoryExec || list[1].Category != tools.CategoryDelete {
		t.Fatalf("%+v", list)
	}
	if len(e.visible("")) != 0 {
		t.Fatal(e.visible(""))
	}
	if _, err := e.svc.Probe(context.Background(), mcp.Server{Name: "bad", Transport: "stdio", Command: []string{"/nonexistent/x"}, Trust: mcp.TrustHost}); err == nil {
		t.Fatal("probe of a missing binary should fail")
	}
}

func TestAgentScoped(t *testing.T) {
	e := newEnv(t, map[string]any{}, load{session.Plugin, nil}, load{prompt.Plugin, nil}, load{agent.Plugin, map[string]any{"root": t.TempDir()}})
	agents := kernel.Use[*agent.Registry](e.h.Root(), agent.ServiceKey)
	ana, err := agents.Create(agent.Spec{Name: "Ana"})
	if err != nil {
		t.Fatal(err)
	}
	srv := stdioServer("priv", nil)
	srv.Scope = "otto"
	if _, err := e.svc.Add(srv); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.svc.Status("priv"); st.State != mcp.StateError || !strings.Contains(st.Error, "not loaded") {
		t.Fatalf("%+v", st)
	}
	// agent/created waits for the agent's servers (rule 4): ready on return
	otto, err := agents.Create(agent.Spec{Name: "Otto"})
	if err != nil || otto.ID() != "otto" {
		t.Fatal(otto, err)
	}
	if st, _ := e.svc.Status("priv"); st.State != mcp.StateReady {
		t.Fatalf("not ready after agent/created: %+v", st)
	}
	if !slices.Contains(e.visible("otto"), "priv__echo") || slices.Contains(e.visible(ana.ID()), "priv__echo") || slices.Contains(e.visible(""), "priv__echo") {
		t.Fatal(e.visible("otto"), e.visible(ana.ID()), e.visible(""))
	}
	if x := e.call(ana.ID(), "priv__echo", `{"text":"x"}`); !x.Result.IsError {
		t.Fatal("other agent could call it")
	}
	if x := e.call("otto", "priv__wipe", `{}`); x.Result.IsError || !strings.Contains(x.Result.Content, otto.Workspace()) {
		t.Fatalf("agent server should run in the agent workspace: %+v", x)
	}
	pid, _ := e.svc.Status("priv")
	if err := agents.Delete("otto"); err != nil {
		t.Fatal(err)
	}
	waitDead(t, pid.PID)
	if st, _ := e.svc.Status("priv"); st.State != mcp.StateError || slices.Contains(e.visible("otto"), "priv__echo") {
		t.Fatalf("%+v %v", st, e.visible("otto"))
	}
}

func TestCredentialRefsAndMasking(t *testing.T) {
	e := newEnv(t, nil)
	secret := "s3cret-token-value"
	if err := e.creds.Set("mem:mcp-test", secret); err != nil {
		t.Fatal(err)
	}
	srv := stdioServer("cred", map[string]string{"MCP_TEST_TOKEN": "mem:mcp-test", "LITERAL": "plain-literal-secret"})
	e.svc.Add(srv)
	e.waitState(t, "cred", mcp.StateReady)
	x := e.call("", "cred__echo", `{"text":"v","env":"MCP_TEST_TOKEN"}`)
	if x.Result.Content != "v|"+secret {
		t.Fatalf("%+v", x)
	}
	if x := e.call("", "cred__echo", `{"text":"v","env":"LITERAL"}`); x.Result.Content != "v|plain-literal-secret" {
		t.Fatalf("%+v", x)
	}
	v, _ := e.svc.Get("cred")
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), secret) || strings.Contains(string(b), "plain-literal-secret") {
		t.Fatal(string(b))
	}
	if v.Env["MCP_TEST_TOKEN"] != "mem:mcp-test" || v.Env["LITERAL"] != mcp.Masked {
		t.Fatalf("%+v", v.Env)
	}
	// echoing masked values back keeps them
	if _, err := e.svc.Update("cred", func(s *mcp.Server) { s.Env = v.Env }); err != nil {
		t.Fatal(err)
	}
	e.waitState(t, "cred", mcp.StateReady)
	if x := e.call("", "cred__echo", `{"text":"v","env":"LITERAL"}`); x.Result.Content != "v|plain-literal-secret" {
		t.Fatalf("%+v", x)
	}
	// an unresolvable ref fails the connection without starting anything
	bad := stdioServer("badref", map[string]string{"TOKEN": "mem:missing"})
	e.svc.Add(bad)
	st := e.waitState(t, "badref", mcp.StateError)
	if !strings.Contains(st.Error, "mem:missing") || st.PID != 0 {
		t.Fatalf("%+v", st)
	}
}

func TestSandboxedFailsClosed(t *testing.T) {
	for name, extra := range map[string][]load{"no sandbox": nil, "sandbox-none": {{sandboxnone.Plugin, nil}}} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, nil, extra...)
			marker := filepath.Join(t.TempDir(), "child.pid")
			srv := stdioServer("sb", map[string]string{"CREWBASE_MCP_TEST_CHILD": marker})
			srv.Trust = "" // default: sandboxed
			v, err := e.svc.Add(srv)
			if err != nil || v.Trust != mcp.TrustSandboxed {
				t.Fatal(v, err)
			}
			st := e.waitState(t, "sb", mcp.StateError)
			if !strings.Contains(st.Error, sandbox.CodeUnavailable) || st.PID != 0 || st.Tools != 0 {
				t.Fatalf("%+v", st)
			}
			time.Sleep(200 * time.Millisecond)
			if _, err := os.Stat(marker); err == nil {
				t.Fatal("server process was started without a sandbox")
			}
			if _, err := e.svc.ProbeNamed(context.Background(), "sb"); !errors.Is(err, sandbox.ErrUnavailable) {
				t.Fatal(err)
			}
		})
	}
}

func TestSeatbeltSandboxedServer(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("seatbelt is macOS only")
	}
	if _, err := os.Stat(seatbelt.DefaultBinary); err != nil {
		t.Skip("sandbox-exec missing")
	}
	if _, err := seatbelt.New(seatbelt.Config{}).Check(context.Background(), sandbox.Policy{Mode: sandbox.ModeReadOnly}); err != nil {
		t.Skip("seatbelt unavailable here:", err)
	}
	e := newEnv(t, nil, load{seatbelt.Plugin, nil})
	srv := stdioServer("sbx", nil)
	srv.Trust = mcp.TrustSandboxed
	e.svc.Add(srv)
	st := e.waitState(t, "sbx", mcp.StateReady)
	if st.Enforcement != sandbox.EnforcementFull {
		t.Fatalf("%+v", st)
	}
	if x := e.call("", "sbx__wipe", `{}`); x.Result.IsError {
		t.Fatalf("write in the server dir should be allowed: %+v", x)
	}
	if x := e.call("", "sbx__echo", `{"text":"ok"}`); x.Result.Content != "ok" {
		t.Fatalf("%+v", x)
	}
	e.svc.SetEnabled("sbx", false)
	waitDead(t, st.PID)
}

// ---------------------------------------------------------------- http

func TestHTTPTransport(t *testing.T) {
	srv := newTestServer(nil)
	var mu sync.Mutex
	var auth []string
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = append(auth, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer http-tok-123" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	e := newEnv(t, nil)
	e.creds.Set("mem:http-tok", "http-tok-123")
	if _, err := e.svc.Add(mcp.Server{Name: "web", Transport: mcp.TransportHTTP, URL: ts.URL, Enabled: true,
		Headers: map[string]string{"Authorization": "Bearer ${mem:http-tok}"}}); err != nil {
		t.Fatal(err)
	}
	st := e.waitState(t, "web", mcp.StateReady)
	if st.Tools != 2 || st.PID != 0 {
		t.Fatalf("%+v", st)
	}
	if x := e.call("", "web__echo", `{"text":"over http"}`); x.Result.Content != "over http" {
		t.Fatalf("%+v", x)
	}
	addExtra(srv, "extra")
	waitFor(t, "http list_changed", func() bool { return slices.Contains(e.visible(""), "web__extra") })
	v, _ := e.svc.Get("web")
	if v.Headers["Authorization"] != "Bearer ${mem:http-tok}" {
		t.Fatal(v.Headers)
	}
	// wrong credential: error state, secret never shown
	e.svc.Add(mcp.Server{Name: "web2", Transport: mcp.TransportHTTP, URL: ts.URL, Enabled: true, Headers: map[string]string{"Authorization": "Bearer wrong-literal-xyz"}})
	st = e.waitState(t, "web2", mcp.StateError)
	if strings.Contains(st.Error, "wrong-literal-xyz") {
		t.Fatal(st.Error)
	}
}

// ---------------------------------------------------------------- api

func TestAPI(t *testing.T) {
	addr := make(chan string, 1)
	dir := t.TempDir() // first: removed after the host has stopped
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	gateway.Listening.Listen(h.Root(), func(a string) { addr <- a })
	for _, l := range []load{{tools.Plugin, nil}, {credentials.Plugin, nil},
		{gateway.Plugin, map[string]any{"addr": "127.0.0.1:0", "token": "tok"}},
		{mcp.Plugin, map[string]any{"dir": dir, "backoffMin": "50ms"}}, {mcp.APIPlugin, nil}} {
		if _, err := h.Load(l.spec, kernel.LoadOptions{Config: l.cfg}); err != nil {
			t.Fatal(err)
		}
	}
	if info, _ := h.Plugin("@crewbase/mcp-api"); info.State != kernel.StateActive {
		t.Fatalf("%+v", info)
	}
	base := "http://" + <-addr
	do := func(method, path string, body any) (int, map[string]any, string) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, base+path, rd)
		req.Header.Set("Authorization", "Bearer tok")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		return res.StatusCode, m, string(raw)
	}
	srv := stdioServer("api", map[string]string{"SECRET_LITERAL": "do-not-show-me"})
	srv.Enabled = false
	if code, _, raw := do("POST", "/api/mcp", srv); code != 200 {
		t.Fatal(code, raw)
	}
	if code, _, _ := do("POST", "/api/mcp", srv); code != 409 {
		t.Fatal("duplicate", code)
	}
	if code, _, _ := do("POST", "/api/mcp", map[string]any{"name": "x y", "transport": "stdio", "command": []string{"a"}}); code != 400 {
		t.Fatal("invalid", code)
	}
	if code, _, _ := do("POST", "/api/mcp", map[string]any{"name": "h", "transport": "http"}); code != 400 {
		t.Fatal("http without url", code)
	}
	code, m, raw := do("POST", "/api/mcp/api/probe", nil)
	if code != 200 || m["ok"] != true || len(m["tools"].([]any)) != 2 {
		t.Fatal(code, raw)
	}
	if code, _, raw := do("PATCH", "/api/mcp/api", map[string]any{"enabled": true}); code != 200 {
		t.Fatal(code, raw)
	}
	waitFor(t, "ready via api", func() bool {
		_, m, _ := do("GET", "/api/mcp/api/tools", nil)
		st, _ := m["status"].(map[string]any)
		return st != nil && st["state"] == mcp.StateReady && len(m["tools"].([]any)) == 2
	})
	code, m, raw = do("GET", "/api/mcp", nil)
	if code != 200 || strings.Contains(raw, "do-not-show-me") || !strings.Contains(raw, mcp.Masked) {
		t.Fatal(code, raw)
	}
	servers := m["servers"].([]any)
	if len(servers) != 1 || servers[0].(map[string]any)["trust"] != mcp.TrustHost {
		t.Fatal(raw)
	}
	if code, _, _ := do("PATCH", "/api/mcp/nope", map[string]any{"enabled": true}); code != 404 {
		t.Fatal(code)
	}
	if code, _, _ := do("PATCH", "/api/mcp/api", map[string]any{"transport": "carrier-pigeon"}); code != 400 {
		t.Fatal(code)
	}
	if code, _, raw := do("DELETE", "/api/mcp/api", nil); code >= 300 {
		t.Fatal(code, raw)
	}
	if code, _, _ := do("GET", "/api/mcp/api/tools", nil); code != 404 {
		t.Fatal(code)
	}
}

func TestUntrustedAnnotationsAndOverrides(t *testing.T) {
	e := newEnv(t, nil)
	srv := stdioServer("ua", nil)
	if _, err := e.svc.Add(srv); err != nil {
		t.Fatal(err)
	}
	e.waitState(t, "ua", mcp.StateReady)
	if tl, _ := e.tools.Get("", "ua__echo"); tl == nil || tl.Category != tools.CategoryExec {
		t.Fatalf("readOnlyHint trusted by default: %+v", tl)
	}
	if tl, _ := e.tools.Get("", "ua__wipe"); tl == nil || tl.Category != tools.CategoryDelete {
		t.Fatalf("destructive hint must still raise: %+v", tl)
	}
	if _, err := e.svc.Update("ua", func(s *mcp.Server) {
		s.Categories = map[string]string{"echo": tools.CategoryRead, "ua__wipe": tools.CategoryWrite}
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "override", func() bool {
		tl, _ := e.tools.Get("", "ua__echo")
		return tl != nil && tl.Category == tools.CategoryRead
	})
	if tl, _ := e.tools.Get("", "ua__wipe"); tl == nil || tl.Category != tools.CategoryWrite {
		t.Fatalf("namespaced override: %+v", tl)
	}
	infos, _ := e.svc.Tools("ua")
	for _, i := range infos {
		if i.Original == "echo" && i.Category != tools.CategoryRead {
			t.Fatalf("%+v", i)
		}
	}
	var p mcp.Patch
	json.Unmarshal([]byte(`{"trustAnnotations":true,"categories":{"x":"read"}}`), &p)
	var s mcp.Server
	p.Apply(&s)
	if !s.TrustAnnotations || s.Categories["x"] != "read" {
		t.Fatalf("%+v", s)
	}
}

func TestPathArgsFromSchema(t *testing.T) {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"path":        str(""),
		"file":        str(""),
		"filepath":    str(""),
		"file_path":   str(""),
		"dir":         str(""),
		"directory":   str(""),
		"cwd":         str(""),
		"source_dir":  str(""),
		"paths":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"location":    str("Absolute path to the file to read"),
		"repo_root":   str("Working directory for the command"),
		"url":         str("The URL to fetch"),
		"name":        str("Name of the issue"),
		"target":      str("Branch to merge into"),
		"profile":     str(""),
		"path_count":  map[string]any{"type": "integer"},
		"recursive":   map[string]any{"type": "boolean", "description": "list directory path recursively"},
		"filename":    str("Display name"),
		"description": str("Free text; may mention a path"),
	}}
	got := mcp.PathArgs(schema)
	want := []string{"cwd", "dir", "directory", "file", "file_path", "filepath", "location", "path", "paths", "repo_root", "source_dir"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

// policySandbox records the policy of each Confine call.
type policySandbox struct {
	mu   sync.Mutex
	pols []sandbox.Policy
}

func (p *policySandbox) Name() string { return "policy-recorder" }
func (p *policySandbox) Check(context.Context, sandbox.Policy) (string, error) {
	return sandbox.EnforcementFull, nil
}
func (p *policySandbox) Confine(_ context.Context, argv []string, _ string, env []string, pol sandbox.Policy) (sandbox.Confined, error) {
	p.mu.Lock()
	p.pols = append(p.pols, pol)
	p.mu.Unlock()
	c := sandbox.Unconfined(argv, env)
	c.Enforcement = sandbox.EnforcementFull
	return c, nil
}
func (p *policySandbox) last() (sandbox.Policy, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.pols) == 0 {
		return sandbox.Policy{}, 0
	}
	return p.pols[len(p.pols)-1], len(p.pols)
}

func TestReadOnlyAgentServerSandboxReadOnly(t *testing.T) {
	sb := &policySandbox{}
	sbSpec := kernel.Spec{Name: "policy-sandbox", Apply: func(c *kernel.Context) error {
		return c.Provide(sandbox.ServiceKey, sandbox.Sandbox(sb))
	}}
	e := newEnv(t, map[string]any{}, load{sbSpec, nil}, load{session.Plugin, nil}, load{prompt.Plugin, nil}, load{agent.Plugin, map[string]any{"root": t.TempDir()}})
	agents := kernel.Use[*agent.Registry](e.h.Root(), agent.ServiceKey)
	ro, err := agents.Create(agent.Spec{Name: "Rory", Mode: agent.ModeReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	srv := stdioServer("rosrv", nil)
	srv.Trust = mcp.TrustSandboxed
	srv.Scope = ro.ID()
	if _, err := e.svc.Add(srv); err != nil {
		t.Fatal(err)
	}
	e.waitState(t, "rosrv", mcp.StateReady)
	if pol, _ := sb.last(); pol.Mode != sandbox.ModeReadOnly || len(pol.WritableRoots) != 0 {
		t.Fatalf("read-only agent's server got %+v", pol)
	}
	// switching the agent to sandbox mode restarts the server writable
	_, n := sb.last()
	if err := ro.Update(func(s *agent.Spec) { s.Mode = agent.ModeSandbox }); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "restart with workspace-write", func() bool {
		pol, m := sb.last()
		return m > n && pol.Mode == sandbox.ModeWorkspaceWrite
	})
	e.waitState(t, "rosrv", mcp.StateReady)
	// a global server is unaffected by agent modes
	g := stdioServer("glob", nil)
	g.Trust = mcp.TrustSandboxed
	e.svc.Add(g)
	e.waitState(t, "glob", mcp.StateReady)
	if pol, _ := sb.last(); pol.Mode != sandbox.ModeWorkspaceWrite {
		t.Fatalf("%+v", pol)
	}
}

// Resources and prompts become <server>__resources / <server>__prompt
// (read category) when the server advertises them.
func TestResourcesAndPrompts(t *testing.T) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "docs", Version: "1"}, nil)
	srv.AddResource(&sdk.Resource{URI: "doc://handbook", Name: "handbook", Title: "员工手册", MIMEType: "text/markdown", Description: "the handbook"},
		func(context.Context, *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
			return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: "doc://handbook", MIMEType: "text/markdown", Text: "# 手册\n每周五发布"}}}, nil
		})
	srv.AddPrompt(&sdk.Prompt{Name: "review", Description: "Review a change", Arguments: []*sdk.PromptArgument{{Name: "topic", Required: true}}},
		func(_ context.Context, req *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
			return &sdk.GetPromptResult{Description: "Code review", Messages: []*sdk.PromptMessage{
				{Role: "user", Content: &sdk.TextContent{Text: "Review the " + req.Params.Arguments["topic"] + " change carefully."}},
			}}, nil
		})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil)
	ts := httptest.NewServer(handler)
	t.Cleanup(func() { ts.CloseClientConnections(); ts.Close() })

	e := newEnv(t, nil)
	if _, err := e.svc.Add(mcp.Server{Name: "docs", Transport: mcp.TransportHTTP, URL: ts.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	st := e.waitState(t, "docs", mcp.StateReady)
	if st.Tools != 2 {
		t.Fatalf("%+v", st)
	}
	for _, n := range []string{"docs__resources", "docs__prompt"} {
		if tl, ok := e.tools.Get("", n); !ok || tl.Category != tools.CategoryRead {
			t.Fatalf("%s: %+v", n, tl)
		}
	}
	if x := e.call("", "docs__resources", `{"action":"list"}`); !strings.Contains(x.Result.Content, "员工手册 <doc://handbook> (text/markdown): the handbook") {
		t.Fatalf("%+v", x.Result)
	}
	if x := e.call("", "docs__resources", `{"action":"read","uri":"doc://handbook"}`); !strings.Contains(x.Result.Content, "每周五发布") {
		t.Fatalf("%+v", x.Result)
	}
	if x := e.call("", "docs__resources", `{"action":"read"}`); !x.Result.IsError {
		t.Fatal("read without uri")
	}
	if x := e.call("", "docs__prompt", `{"action":"list"}`); !strings.Contains(x.Result.Content, "review: Review a change (arguments: topic*") {
		t.Fatalf("%+v", x.Result)
	}
	x := e.call("", "docs__prompt", `{"action":"get","name":"review","arguments":{"topic":"login"}}`)
	if !strings.Contains(x.Result.Content, "Review the login change carefully.") || !strings.Contains(x.Result.Content, "not a message from the user") {
		t.Fatalf("%+v", x.Result)
	}
	// a server without them gets neither tool
	plain := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return newTestServer(nil) }, nil))
	t.Cleanup(func() { plain.CloseClientConnections(); plain.Close() })
	e.svc.Add(mcp.Server{Name: "plain", Transport: mcp.TransportHTTP, URL: plain.URL, Enabled: true})
	e.waitState(t, "plain", mcp.StateReady)
	if _, ok := e.tools.Get("", "plain__resources"); ok {
		t.Fatal("resources tool for a server without resources")
	}
}
