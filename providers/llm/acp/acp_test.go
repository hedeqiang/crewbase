//go:build !windows

package acp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/kernel"
)

func TestMain(m *testing.M) {
	if os.Getenv(fakeEnv) == "1" {
		runFakeAgent()
		os.Exit(0)
	}
	probe = false // tests start CLIs themselves
	os.Exit(m.Run())
}

func fakeResolved(t *testing.T, env map[string]string) resolved {
	t.Helper()
	e := map[string]string{fakeEnv: "1"}
	for k, v := range env {
		e[k] = v
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return resolved{AgentConfig: AgentConfig{ID: "fake", Name: "Fake", Command: []string{os.Args[0], "-test.run=^$"}, Env: e, Cwd: dir, ContextWindow: 1000}, cliPath: os.Args[0]}
}

func newFake(t *testing.T, opts Options, env map[string]string) *Adapter {
	t.Helper()
	if opts.IdleTimeout == 0 {
		opts.IdleTimeout = -1
	}
	opts.ToolNotes = true
	a := newAdapter("acp-fake", fakeResolved(t, env), opts)
	t.Cleanup(a.Close)
	return a
}

func user(s string) llm.Message { return llm.TextMessage(llm.RoleUser, s) }

func stream(t *testing.T, a *Adapter, req llm.Request) (llm.Response, []llm.Chunk, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var mu sync.Mutex
	var chunks []llm.Chunk
	resp, err := a.Stream(ctx, req, func(c llm.Chunk) error {
		mu.Lock()
		chunks = append(chunks, c)
		mu.Unlock()
		return nil
	})
	return resp, chunks, err
}

func TestStreamAndSessionReuse(t *testing.T) {
	a := newFake(t, Options{}, nil)
	req := llm.Request{Model: "default", System: "be brief", Messages: []llm.Message{user("hello")}}
	resp, chunks, err := stream(t, a, req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "end" || resp.Message.Role != llm.RoleAssistant {
		t.Fatalf("resp %+v", resp)
	}
	txt := resp.Message.Text()
	if !strings.Contains(txt, "[Fake tool: Read README.md]") || !strings.Contains(txt, "[s1] echo: ") {
		t.Fatalf("text %q", txt)
	}
	// fresh session carries the system prompt
	if !strings.Contains(txt, "<instructions>\nbe brief") || !strings.Contains(txt, "Current message:\nhello") {
		t.Fatalf("fresh prompt not rendered: %q", txt)
	}
	if len(resp.Message.ToolCalls()) != 0 {
		t.Fatal("ACP tool calls must not become Crewbase tool calls")
	}
	var sawThinking, sawText bool
	for _, c := range chunks {
		sawThinking = sawThinking || (c.Type == "thinking" && c.Text == "thinking")
		sawText = sawText || c.Type == "text"
	}
	if !sawThinking || !sawText {
		t.Fatalf("chunks %+v", chunks)
	}
	if v := a.AgentVersion(); v != "9.9.9" {
		t.Fatalf("version %q", v)
	}
	ms, _ := a.Models(context.Background())
	if len(ms) != 3 || ms[1].ID != "fast" || ms[2].ID != "smart" {
		t.Fatalf("models %+v", ms)
	}
	pid := a.Pid()

	// prefix extension: same session, only the new message is sent
	req.Messages = append(req.Messages, resp.Message, user("again"))
	resp2, _, err := stream(t, a, req)
	if err != nil {
		t.Fatal(err)
	}
	if got := resp2.Message.Text(); !strings.Contains(got, "[s1] echo: again") {
		t.Fatalf("not reused: %q", got)
	}
	if a.Pid() != pid {
		t.Fatal("process restarted")
	}

	// history rewritten (e.g. compaction / edited message): new session with transcript
	req.Messages = []llm.Message{user("hello"), llm.TextMessage(llm.RoleAssistant, "summary reply"), user("third")}
	resp3, _, err := stream(t, a, req)
	if err != nil {
		t.Fatal(err)
	}
	got := resp3.Message.Text()
	if !strings.Contains(got, "[s2]") || !strings.Contains(got, "<assistant>\nsummary reply\n</assistant>") || !strings.Contains(got, "Current message:\nthird") {
		t.Fatalf("fresh session transcript: %q", got)
	}

	// a different conversation (other first message) gets its own session
	resp4, _, err := stream(t, a, llm.Request{Messages: []llm.Message{user("other")}})
	if err != nil {
		t.Fatal(err)
	}
	if got := resp4.Message.Text(); !strings.Contains(got, "[s3] echo: other") {
		t.Fatalf("other conv: %q", got)
	}
}

func TestCancellation(t *testing.T) {
	a := newFake(t, Options{CancelGrace: 5 * time.Second}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan struct{})
	var once sync.Once
	errc := make(chan error, 1)
	go func() {
		_, err := a.Stream(ctx, llm.Request{Messages: []llm.Message{user("/slow")}}, func(c llm.Chunk) error {
			once.Do(func() { close(got) })
			return nil
		})
		errc <- err
	}()
	select {
	case <-got:
	case <-time.After(20 * time.Second):
		t.Fatal("no chunk")
	}
	start := time.Now()
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want canceled, got %v", err)
		}
		if time.Since(start) > 3*time.Second {
			t.Fatal("agent did not see session/cancel")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancel not honoured")
	}
	// the process survives and serves the next turn
	if _, _, err := stream(t, a, llm.Request{Messages: []llm.Message{user("after")}}); err != nil {
		t.Fatal(err)
	}
}

func TestChunkErrorAborts(t *testing.T) {
	a := newFake(t, Options{}, nil)
	boom := errors.New("sink failed")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := a.Stream(ctx, llm.Request{Messages: []llm.Message{user("/slow")}}, func(llm.Chunk) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
}

func TestPermission(t *testing.T) {
	for _, auto := range []bool{false, true} {
		a := newFake(t, Options{AutoApprove: auto}, nil)
		resp, _, err := stream(t, a, llm.Request{Messages: []llm.Message{user("/perm")}})
		if err != nil {
			t.Fatal(err)
		}
		txt := resp.Message.Text()
		want, note := "permission: selected/no", "[permission denied by Crewbase: rm -rf build]"
		if auto {
			want, note = "permission: selected/yes", "[auto-approved: rm -rf build]"
		}
		if !strings.Contains(txt, want) || !strings.Contains(txt, note) {
			t.Fatalf("auto=%v: %q", auto, txt)
		}
	}
}

func TestFileSystemRestricted(t *testing.T) {
	a := newFake(t, Options{}, nil)
	in := filepath.Join(a.cfg.Cwd, "a.txt")
	if err := os.WriteFile(in, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp, _, err := stream(t, a, llm.Request{Messages: []llm.Message{user("/read " + in)}})
	if err != nil || !strings.Contains(resp.Message.Text(), "read: two\n") {
		t.Fatalf("read: %v %q", err, resp.Message.Text())
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	_ = os.WriteFile(outside, []byte("secret"), 0o600)
	resp, _, _ = stream(t, a, llm.Request{Messages: []llm.Message{user("/read " + outside)}})
	if txt := resp.Message.Text(); !strings.Contains(txt, "read error") || strings.Contains(txt, "secret\n") {
		t.Fatalf("outside read allowed: %q", txt)
	}
	// symlink escaping the cwd
	link := filepath.Join(a.cfg.Cwd, "link")
	if err := os.Symlink(filepath.Dir(outside), link); err != nil {
		t.Fatal(err)
	}
	resp, _, _ = stream(t, a, llm.Request{Messages: []llm.Message{user("/read " + filepath.Join(link, "secret.txt"))}})
	if !strings.Contains(resp.Message.Text(), "read error") {
		t.Fatalf("symlink escape allowed: %q", resp.Message.Text())
	}
	newFile := filepath.Join(a.cfg.Cwd, "sub", "new.txt")
	resp, _, _ = stream(t, a, llm.Request{Messages: []llm.Message{user("/write " + newFile)}})
	if b, err := os.ReadFile(newFile); err != nil || string(b) != "written" || !strings.Contains(resp.Message.Text(), "write ok") {
		t.Fatalf("write: %v %q %q", err, b, resp.Message.Text())
	}
	bad := filepath.Join(filepath.Dir(outside), "evil.txt")
	resp, _, _ = stream(t, a, llm.Request{Messages: []llm.Message{user("/write " + bad)}})
	if _, err := os.Stat(bad); err == nil || !strings.Contains(resp.Message.Text(), "write error") {
		t.Fatalf("outside write allowed: %q", resp.Message.Text())
	}
}

func TestQuotaErrors(t *testing.T) {
	a := newFake(t, Options{}, nil)
	_, _, err := stream(t, a, llm.Request{Messages: []llm.Message{user("/quota")}})
	var le *llm.Error
	if !errors.As(err, &le) || le.Kind != llm.ErrQuotaExhausted || le.Provider != "acp-fake" {
		t.Fatalf("got %v", err)
	}
	if !le.ResetsAt.Equal(time.Unix(4102444800, 0)) {
		t.Fatalf("resets %v", le.ResetsAt)
	}
	if !llm.ShouldFailover(err) {
		t.Fatal("quota must fail over")
	}
	if q := a.Quota(context.Background()); !q.Known || q.Remaining != 0 {
		t.Fatalf("quota %+v", q)
	}

	_, _, err = stream(t, a, llm.Request{Messages: []llm.Message{user("/banner")}})
	if !errors.As(err, &le) || le.Kind != llm.ErrQuotaExhausted || le.RetryAfter != 2*time.Hour {
		t.Fatalf("banner: %v", err)
	}

	_, _, err = stream(t, a, llm.Request{Messages: []llm.Message{user("/auth")}})
	if llm.KindOf(err) != llm.ErrAuth {
		t.Fatalf("auth: %v", err)
	}
	// success resets the known-exhausted quota
	if _, _, err := stream(t, a, llm.Request{Messages: []llm.Message{user("ok")}}); err != nil {
		t.Fatal(err)
	}
	if q := a.Quota(context.Background()); q.Known {
		t.Fatalf("quota still known: %+v", q)
	}
}

func TestInitFailureUsesStderr(t *testing.T) {
	a := newFake(t, Options{}, map[string]string{"FAKE_ACP_INIT_FAIL": "1"})
	_, _, err := stream(t, a, llm.Request{Messages: []llm.Message{user("hi")}})
	if llm.KindOf(err) != llm.ErrQuotaExhausted {
		t.Fatalf("got %v", err)
	}
}

func TestCrashIsUnavailableAndRestarts(t *testing.T) {
	a := newFake(t, Options{}, nil)
	if _, _, err := stream(t, a, llm.Request{Messages: []llm.Message{user("warm")}}); err != nil {
		t.Fatal(err)
	}
	pid := a.Pid()
	_, _, err := stream(t, a, llm.Request{Messages: []llm.Message{user("/crash")}})
	var le *llm.Error
	if !errors.As(err, &le) || le.Kind != llm.ErrUnavailable || !strings.Contains(le.Message, "exit status 3") {
		t.Fatalf("got %v", err)
	}
	resp, _, err := stream(t, a, llm.Request{Messages: []llm.Message{user("back")}})
	if err != nil {
		t.Fatal(err)
	}
	if a.Pid() == pid || !strings.Contains(resp.Message.Text(), "[s1] echo: back") {
		t.Fatalf("not restarted: pid %d->%d %q", pid, a.Pid(), resp.Message.Text())
	}
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func waitDead(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid %d still alive", pid)
}

func TestIdleShutdown(t *testing.T) {
	a := newFake(t, Options{IdleTimeout: 200 * time.Millisecond}, nil)
	if _, _, err := stream(t, a, llm.Request{Messages: []llm.Message{user("hi")}}); err != nil {
		t.Fatal(err)
	}
	pid := a.Pid()
	if pid == 0 {
		t.Fatal("not running")
	}
	waitDead(t, pid)
	deadline := time.Now().Add(5 * time.Second)
	for a.Running() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if a.Running() {
		t.Fatal("adapter still reports running")
	}
	// started again on demand
	if _, _, err := stream(t, a, llm.Request{Messages: []llm.Message{user("hi")}}); err != nil {
		t.Fatal(err)
	}
}

func TestProcessGroupCleanupOnDispose(t *testing.T) {
	h := kernel.NewHost(nil)
	defer h.Close()
	reg := llm.NewRegistry()
	var a *Adapter
	p, err := h.Load(kernel.Spec{Name: "acp-test", Apply: func(c *kernel.Context) error {
		a = newAdapter("acp-fake", fakeResolved(t, nil), Options{IdleTimeout: -1, Go: func(fn func(context.Context)) bool {
			if c.Disposed() {
				return false
			}
			c.Go(fn)
			return true
		}})
		c.Defer(a.Close)
		return reg.Register(c, a)
	}}, kernel.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_ = p
	resp, _, err := stream(t, a, llm.Request{Messages: []llm.Message{user("/spawn")}})
	if err != nil {
		t.Fatal(err)
	}
	txt := resp.Message.Text()
	i := strings.Index(txt, "pid=")
	if i < 0 {
		t.Fatalf("no pid: %q", txt)
	}
	child, _ := strconv.Atoi(strings.TrimSpace(txt[i+4:]))
	agent := a.Pid()
	if !alive(child) || !alive(agent) {
		t.Fatal("processes not running")
	}
	if _, ok := reg.Get("acp-fake"); !ok {
		t.Fatal("not registered")
	}
	h.Unload("acp-test") // dispose: cancels ctx.Go readers and closes the adapter
	waitDead(t, agent)
	waitDead(t, child)
	if _, ok := reg.Get("acp-fake"); ok {
		t.Fatal("still registered")
	}
	// a disposed adapter refuses work
	if _, _, err := stream(t, a, llm.Request{Messages: []llm.Message{user("x")}}); llm.KindOf(err) != llm.ErrUnavailable {
		t.Fatalf("after dispose: %v", err)
	}
}

func TestClassifyText(t *testing.T) {
	cases := map[string]llm.ErrorKind{
		"Claude AI usage limit reached|1735689600":                                          llm.ErrQuotaExhausted,
		"You've hit your usage limit. Upgrade to Pro":                                       llm.ErrQuotaExhausted,
		"5-hour limit reached ∙ resets 3pm":                                                 llm.ErrQuotaExhausted,
		"Weekly limit reached":                                                              llm.ErrQuotaExhausted,
		"Quota exceeded for quota metric 'Gemini 2.5 Pro Requests'":                         llm.ErrQuotaExhausted,
		"[API Error: got status: RESOURCE_EXHAUSTED]":                                       llm.ErrQuotaExhausted,
		"Rate limit exceeded, please retry":                                                 llm.ErrRateLimited,
		"429 Too Many Requests":                                                             llm.ErrRateLimited,
		"Invalid API key · Please run /login":                                               llm.ErrAuth,
		"Authentication required":                                                           llm.ErrAuth,
		"Codex ran out of room in the model's context window":                               llm.ErrContextOverflow,
		"We're currently experiencing high demand":                                          llm.ErrUnavailable,
		"file not found":                                                                    "",
		"The 'gpt-6.1-sol' model is not supported when using Codex with a ChatGPT account.": llm.ErrModelUnavailable,
		"stream disconnected before completion":                                             "",
		"API Error: 529 {\"type\":\"error\",\"error\":{\"type\":\"overloaded":               llm.ErrUnavailable,
	}
	for in, want := range cases {
		if got := classifyText(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
	now := time.Unix(1000, 0)
	if r, at := resetHints("try again in 30 minutes", now); r != 30*time.Minute || !at.Equal(now.Add(30*time.Minute)) {
		t.Errorf("hints %v %v", r, at)
	}
}

func TestResolveAndDetect(t *testing.T) {
	dir := t.TempDir()
	bin := func(name string) string {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, []byte("#!/bin/sh\necho \""+name+" 1.2.3 (test)\"\n"), 0o755)
		return p
	}
	bin("claude")
	bin("claude-code-acp")
	bin("codex") // installed, but no ACP adapter and no npx
	bin("agy")
	t.Setenv("PATH", dir)
	home := t.TempDir()
	_ = os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	_ = os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte("{}"), 0o600)
	oldHome := homeDir
	homeDir = func() (string, error) { return home, nil }
	defer func() { homeDir = oldHome }()
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")

	infos := Detect(context.Background(), Config{Agents: []AgentConfig{{ID: "antigravity", Cwd: "/work"}}})
	by := map[string]CLIInfo{}
	for _, i := range infos {
		by[i.ID] = i
	}
	cc := by["claude-code"]
	if !cc.Installed || cc.Version != "1.2.3" || cc.ACPCommand[0] != filepath.Join(dir, "claude-code-acp") || cc.LoginCommand != "claude /login" || cc.Provider != "acp-claude-code" {
		t.Fatalf("claude %+v", cc)
	}
	cx := by["codex"]
	if !cx.Installed || len(cx.ACPCommand) != 0 || cx.LoggedIn != LoggedInYes {
		t.Fatalf("codex %+v", cx)
	}
	gm := by["antigravity"]
	if !gm.Installed || strings.Join(gm.ACPCommand[1:], " ") != "acp" || gm.LoggedIn != LoggedInUnknown {
		t.Fatalf("antigravity %+v", gm)
	}
	if op := by["opencode"]; op.Installed || op.LoggedIn != LoggedInNo {
		t.Fatalf("opencode %+v", op)
	}

	// the plugin registers only runnable agents
	h := kernel.NewHost(nil)
	defer h.Close()
	if _, err := h.Load(llm.Plugin, kernel.LoadOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Load(Plugin, kernel.LoadOptions{Config: map[string]any{"idleTimeoutSec": 5}}); err != nil {
		t.Fatal(err)
	}
	if info, _ := h.Plugin(Plugin.Name); info.State != kernel.StateActive {
		t.Fatalf("plugin %+v", info)
	}
	reg := kernel.Use[*llm.Registry](h.Root(), llm.ServiceKey)
	var ids []string
	for _, a := range reg.List() {
		ids = append(ids, a.ID())
		if a.Kind() != "subscription" {
			t.Fatal("kind")
		}
	}
	if strings.Join(ids, ",") != "acp-antigravity,acp-claude-code" {
		t.Fatalf("registered %v", ids)
	}
	svc := kernel.Use[*Service](h.Root(), ServiceKey)
	if got := svc.Detect(context.Background()); len(got) != len(builtins) {
		t.Fatalf("service detect %d", len(got))
	}
}

func TestRendering(t *testing.T) {
	args := []byte(`{"q":1}`)
	req := llm.Request{System: "sys", Messages: []llm.Message{
		user("[alice]: hi"),
		{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: "text", Text: "calling"}, {Type: "tool_call", ToolCall: &llm.ToolCall{ID: "c1", Name: "grep", Args: args}}}},
		{Role: llm.RoleTool, Parts: []llm.Part{{Type: "tool_result", ToolResult: &llm.ToolResult{CallID: "c1", Name: "grep", Content: "3 matches"}}}},
		user("continue please"),
	}}
	out := renderFresh(req)
	for _, want := range []string{"<instructions>\nsys\n</instructions>", "<user>\n[alice]: hi\n</user>", "[called tool grep with {\"q\":1}]", "[tool grep result]\n3 matches", "Current message:\ncontinue please"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if got := renderFresh(llm.Request{Messages: []llm.Message{user("plain")}}); got != "plain" {
		t.Errorf("plain: %q", got)
	}
	// thinking parts do not affect fingerprints (DeriveMessages drops them)
	m1 := llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: "thinking", Text: "x"}, {Type: "text", Text: "y"}}}
	m2 := llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: "text", Text: "y"}}}
	if fingerprint(m1) != fingerprint(m2) {
		t.Error("thinking changes fingerprint")
	}
}

// Files the CLI changes with its own tools (edit kinds, diffs) or through
// fs/write_text_file are reported once per turn; reads are not.
func TestFilesChangedReported(t *testing.T) {
	var got [][]string
	var who []string
	a := newFake(t, Options{FilesChanged: func(agent, sess string, paths []string) {
		got = append(got, paths)
		who = append(who, agent+"/"+sess)
	}}, nil)
	if _, _, err := stream(t, a, llm.Request{Agent: "kai", Session: "s1", Messages: []llm.Message{user("/edit")}}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || strings.Join(got[0], ",") != "main.go,docs/readme.md" || who[0] != "kai/s1" {
		t.Fatalf("changed %v by %v", got, who)
	}
	notes := filepath.Join(a.cfg.Cwd, "notes.txt")
	if _, _, err := stream(t, a, llm.Request{Agent: "kai", Session: "s2", Messages: []llm.Message{user("/write " + notes)}}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || strings.Join(got[1], ",") != notes {
		t.Fatalf("fs write not reported: %v", got)
	}
	// a turn that changes nothing reports nothing
	stream(t, a, llm.Request{Agent: "kai", Session: "s3", Messages: []llm.Message{user("hello")}})
	if len(got) != 2 {
		t.Fatalf("read-only turn reported changes: %v", got)
	}
}

// A CLI installed after start is registered by the rescan.
func TestRescanFindsNewCLI(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	h := kernel.NewHost(nil)
	defer h.Close()
	if _, err := h.Load(llm.Plugin, kernel.LoadOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Load(Plugin, kernel.LoadOptions{Config: map[string]any{"idleTimeoutSec": 5}}); err != nil {
		t.Fatal(err)
	}
	reg := kernel.Use[*llm.Registry](h.Root(), llm.ServiceKey)
	svc := kernel.Use[*Service](h.Root(), ServiceKey)
	if _, ok := reg.Get("acp-antigravity"); ok {
		t.Fatal("registered before install")
	}
	os.WriteFile(filepath.Join(dir, "agy"), []byte("#!/bin/sh\necho 'agy 1.0.0'\n"), 0o755)
	if ids := svc.Rescan(); len(ids) != 1 || ids[0] != "antigravity" {
		t.Fatalf("rescan %v", ids)
	}
	if _, ok := reg.Get("acp-antigravity"); !ok {
		t.Fatal("not registered after rescan")
	}
	if ids := svc.Rescan(); len(ids) != 0 {
		t.Fatalf("second rescan added %v", ids)
	}
}

// The Claude bridge is pointed at the installed claude (its bundled CLI
// may not start); a choice already made is left alone.
func TestClaudeBridgeUsesInstalledClaude(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"claude", "claude-agent-acp"} {
		_ = os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/sh\n"), 0o755)
	}
	t.Setenv("PATH", dir)
	t.Setenv("CLAUDE_CODE_EXECUTABLE", "")
	r := resolve(AgentConfig{ID: "claude-code"})
	if r.Env["CLAUDE_CODE_EXECUTABLE"] != filepath.Join(dir, "claude") {
		t.Fatalf("env %v", r.Env)
	}
	if r := resolve(AgentConfig{ID: "claude-code", Env: map[string]string{"CLAUDE_CODE_EXECUTABLE": "/opt/my/claude"}}); r.Env["CLAUDE_CODE_EXECUTABLE"] != "/opt/my/claude" {
		t.Fatalf("config choice replaced: %v", r.Env)
	}
	t.Setenv("CLAUDE_CODE_EXECUTABLE", "/from/env")
	if r := resolve(AgentConfig{ID: "claude-code"}); r.Env["CLAUDE_CODE_EXECUTABLE"] != "" {
		t.Fatalf("an inherited choice must win: %v", r.Env)
	}
	if r := resolve(AgentConfig{ID: "codex"}); r.Env["CLAUDE_CODE_EXECUTABLE"] != "" {
		t.Fatal("only the Claude bridge")
	}
}

func TestReportsOwnToolsAndContextUsage(t *testing.T) {
	a := newFake(t, Options{}, nil)
	if !llm.RunsOwnTools(a) {
		t.Fatal("an ACP adapter runs its own tools")
	}
	resp, _, err := stream(t, a, llm.Request{Model: "default", Messages: []llm.Message{user("/usage")}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.Context != 23456 {
		t.Fatalf("reported context %d, want 23456", resp.Usage.Context)
	}
	resp, _, err = stream(t, a, llm.Request{Model: "default", Messages: []llm.Message{user("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.Context != 0 {
		t.Fatalf("no usage_update must report 0, got %d", resp.Usage.Context)
	}
}
