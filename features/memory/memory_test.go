package memory_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/memory"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

type env struct {
	h      *kernel.Host
	agents *agent.Registry
	tools  *tools.Registry
	mem    *memory.Store
}

func newEnv(t *testing.T, root string, withMemory bool) *env {
	t.Helper()
	e := &env{h: kernel.NewHost(nil)}
	t.Cleanup(e.h.Close)
	load := func(s kernel.Spec, cfg map[string]any) {
		if _, err := e.h.Load(s, kernel.LoadOptions{Config: cfg}); err != nil {
			t.Fatal(err)
		}
	}
	load(session.Plugin, map[string]any{"dir": root + "/sessions"})
	load(prompt.Plugin, nil)
	load(tools.Plugin, nil)
	load(llm.Plugin, nil)
	load(kernel.Spec{Name: "test-llm", Inject: []string{llm.ServiceKey}, Apply: func(c *kernel.Context) error {
		return kernel.Use[*llm.Registry](c, llm.ServiceKey).Register(c, mock.New("mock", 0))
	}}, nil)
	load(agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"mock/default"}})
	load(agentloop.Plugin, nil)
	if withMemory {
		load(memory.Plugin, nil)
	}
	for _, p := range e.h.Plugins() {
		if p.State != kernel.StateActive {
			t.Fatalf("plugin not active: %+v", p)
		}
	}
	r := e.h.Root()
	e.agents = kernel.Use[*agent.Registry](r, agent.ServiceKey)
	e.tools = kernel.Use[*tools.Registry](r, tools.ServiceKey)
	if withMemory {
		e.mem = kernel.Use[*memory.Store](r, memory.ServiceKey)
	}
	return e
}

func send(t *testing.T, a *agent.Agent, text string) {
	t.Helper()
	if _, err := a.Enqueue(agent.Input{Text: text}); err != nil {
		t.Fatal(err)
	}
	if !agentloop.WaitIdle(a, 5*time.Second) {
		t.Fatal("not idle")
	}
}

func (e *env) run(t *testing.T, agentID, tool string, args any) tools.Execution {
	t.Helper()
	b, _ := json.Marshal(args)
	return e.tools.Run(context.Background(), e.h.Root(), tools.Call{ID: "t", Name: tool, Agent: agentID, Args: b})
}

func TestIncrementalIndexAndReindex(t *testing.T) {
	root := t.TempDir()
	e := newEnv(t, root, true)
	a, _ := e.agents.Create(agent.Spec{Name: "Kai"})
	b, _ := e.agents.Create(agent.Spec{Name: "Otto"})
	send(t, a, "the quokka migration is scheduled for friday")
	send(t, b, "otto talks about the quokka too")
	send(t, a, "/tool memory_search {\"query\":\"x\"}")

	// indexed incrementally, without Reindex, and scoped to the agent
	hits, err := e.mem.SearchHistory(a.ID(), "quokka friday", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	ss, _ := a.Session()
	if len(hits) < 2 || hits[0].Session != ss.ID() {
		t.Fatalf("hits %+v", hits)
	}
	for _, h := range hits {
		if h.Session != ss.ID() {
			t.Fatalf("leaked other agent's session: %+v", h)
		}
	}
	roles := map[string]bool{}
	for _, h := range hits {
		roles[h.Role] = true
	}
	if !roles["user"] || !roles["assistant"] {
		t.Fatalf("roles %v", roles)
	}
	// punctuation in queries is safe
	if _, err := e.mem.SearchHistory(a.ID(), `quokka" OR (NEAR`, "", 5); err != nil {
		t.Fatal(err)
	}

	count := func() int {
		h, _ := e.mem.SearchHistory(a.ID(), "quokka", "", 50)
		return len(h)
	}
	before := count()
	if err := e.mem.Reindex(context.Background()); err != nil {
		t.Fatal(err)
	}
	if after := count(); after != before || after == 0 {
		t.Fatalf("reindex changed results: %d vs %d", after, before)
	}

	// Events written while the plugin was not loaded are caught up on start.
	e.h.Close()
	e2 := newEnv(t, root, false)
	a2, _ := e2.agents.Get(a.ID())
	send(t, a2, "the platypus arrives later")
	e2.h.Close()
	e3 := newEnv(t, root, true)
	deadline := time.Now().Add(3 * time.Second)
	for {
		h, err := e3.mem.SearchHistory(a.ID(), "platypus", "", 5)
		if err != nil {
			t.Fatal(err)
		}
		if len(h) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("catch-up did not index missed events")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(root, "index.sqlite")); err != nil {
		t.Fatal(err)
	}
}

// MEMORY.md indexed by an earlier run is not indexed again on start; one
// changed while the daemon was stopped is.
func TestMemoryIndexKeptAcrossRestarts(t *testing.T) {
	root := t.TempDir()
	e := newEnv(t, root, true)
	a, _ := e.agents.Create(agent.Spec{Name: "Iris"})
	if ex := e.run(t, a.ID(), "memory_add", map[string]string{"text": "the wombat lives in staging"}); ex.Result.IsError {
		t.Fatalf("%+v", ex)
	}
	id := a.ID()
	path := filepath.Join(a.Home(), "MEMORY.md")
	e.h.Close()
	hits := func(e *env, q string) int {
		h, err := e.mem.SearchMemory(id, q, 10)
		if err != nil {
			t.Fatal(err)
		}
		return len(h)
	}
	e2 := newEnv(t, root, true)
	if n := hits(e2, "wombat"); n != 1 {
		t.Fatalf("after restart: %d hits", n)
	}
	e2.h.Close()
	b, _ := os.ReadFile(path)
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(b), "wombat", "numbat")), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	os.Chtimes(path, future, future)
	e3 := newEnv(t, root, true)
	if n, m := hits(e3, "numbat"), hits(e3, "wombat"); n != 1 || m != 0 {
		t.Fatalf("edited while stopped: numbat %d wombat %d", n, m)
	}
}

// A side file holding JSON null (hand-edited, or a crash mid-write of an
// older version) must not leave a nil map that a write then panics on.
func TestNullUsedFileIsAnEmptyMap(t *testing.T) {
	e := newEnv(t, t.TempDir(), true)
	a, _ := e.agents.Create(agent.Spec{Name: "Iris"})
	e.run(t, a.ID(), "memory_add", map[string]string{"text": "the heron lives in staging"})
	dir := filepath.Join(a.Home(), ".memory")
	os.MkdirAll(dir, 0o700)
	if err := os.WriteFile(filepath.Join(dir, "used.json"), []byte("null"), 0o600); err != nil {
		t.Fatal(err)
	}
	ex := e.run(t, a.ID(), "memory_search", map[string]string{"query": "heron"})
	if ex.Result.IsError || !strings.Contains(ex.Result.Content, "heron") {
		t.Fatalf("%+v", ex.Result)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "used.json"))
	if !strings.Contains(string(b), "heron") {
		t.Fatalf("recall not noted: %s", b)
	}
}

func TestMemoryToolsAndHistoryRead(t *testing.T) {
	e := newEnv(t, t.TempDir(), true)
	a, _ := e.agents.Create(agent.Spec{Name: "Iris"})

	ex := e.run(t, a.ID(), "memory_add", map[string]string{"text": "The staging database is called heron-db"})
	if ex.Result.IsError || ex.Category != tools.CategoryWrite {
		t.Fatalf("%+v", ex)
	}
	mem := a.Memory()
	if !strings.Contains(mem, "- "+time.Now().Format("2006-01-02")+": The staging database is called heron-db") {
		t.Fatalf("MEMORY.md: %q", mem)
	}
	// MEMORY.md is part of the system prompt
	sys := kernel.Use[*prompt.Assembler](e.h.Root(), prompt.ServiceKey).Assemble(a.ID())
	if !strings.Contains(sys, "heron-db") {
		t.Fatal("memory not in system prompt")
	}
	ex = e.run(t, a.ID(), "memory_search", map[string]string{"query": "staging database"})
	if !strings.Contains(ex.Result.Content, "heron") || ex.Category != tools.CategoryRead {
		t.Fatalf("%+v", ex.Result)
	}
	// hand edits of MEMORY.md are picked up
	f, _ := os.OpenFile(filepath.Join(a.Workspace(), "MEMORY.md"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("- 用户喜欢简短的回答\n")
	f.Close()
	future := time.Now().Add(2 * time.Second)
	os.Chtimes(filepath.Join(a.Workspace(), "MEMORY.md"), future, future)
	ex = e.run(t, a.ID(), "memory_search", map[string]string{"query": "简短"})
	if !strings.Contains(ex.Result.Content, "简短") {
		t.Fatalf("CJK / hand edit: %+v", ex.Result)
	}

	send(t, a, "deploy the albatross service")
	ex = e.run(t, a.ID(), "history_search", map[string]string{"query": "albatross"})
	hits, _ := ex.Result.Data.([]memory.Hit)
	if len(hits) == 0 {
		t.Fatalf("%+v", ex.Result)
	}
	ex = e.run(t, a.ID(), "history_read", map[string]any{"session": hits[0].Session, "from": hits[0].Seq, "to": hits[0].Seq + 10})
	if !strings.Contains(ex.Result.Content, "[user] deploy the albatross service") || !strings.Contains(ex.Result.Content, "default: deploy the albatross service") {
		t.Fatalf("history_read: %q", ex.Result.Content)
	}
	// size cap
	out, err := e.mem.Read(a.ID(), hits[0].Session, 1, 100, 40)
	if err != nil || len(out) > 200 || !strings.Contains(out, "output limit reached") {
		t.Fatalf("cap: %q %v", out, err)
	}
}

func TestChineseSearch(t *testing.T) {
	e := newEnv(t, t.TempDir(), true)
	a, _ := e.agents.Create(agent.Spec{Name: "Lin"})
	send(t, a, "我们下周五要完成数据库迁移计划，然后通知运维团队")
	send(t, a, "今天天气很好")
	ss, _ := a.Session()
	// a word in the middle of a sentence (no spaces): trigram index
	for _, q := range []string{"数据库迁移", "迁移计划", "运维团队 数据库", "通知运维"} {
		hits, err := e.mem.SearchHistory(a.ID(), q, "", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) == 0 || hits[0].Session != ss.ID() || !strings.Contains(hits[0].Snippet, "«") {
			t.Fatalf("%q: %+v", q, hits)
		}
		for _, h := range hits {
			if strings.Contains(h.Snippet, "天气") {
				t.Fatalf("%q matched the wrong message: %+v", q, h)
			}
		}
	}
	// two characters: substring fallback
	hits, _ := e.mem.SearchHistory(a.ID(), "天气", "", 10)
	if len(hits) == 0 || !strings.Contains(hits[0].Snippet, "天气") {
		t.Fatalf("short CJK: %+v", hits)
	}
	// AND falls back to OR across terms
	if hits, _ := e.mem.SearchHistory(a.ID(), "数据库迁移 不存在的词语", "", 10); len(hits) == 0 {
		t.Fatal("OR fallback")
	}
	if hits, _ := e.mem.SearchHistory(a.ID(), "完全不存在的内容", "", 10); len(hits) != 0 {
		t.Fatalf("false positive: %+v", hits)
	}
	// memory entries too, and they survive Reindex
	if _, err := e.mem.Add(a.ID(), "用户偏好使用简体中文回复"); err != nil {
		t.Fatal(err)
	}
	if h, _ := e.mem.SearchMemory(a.ID(), "简体中文", 5); len(h) != 1 {
		t.Fatalf("memory: %+v", h)
	}
	if _, err := e.mem.Add(a.ID(), "第二条记忆"); err != nil { // re-index of memory replaces trigram rows
		t.Fatal(err)
	}
	if h, _ := e.mem.SearchMemory(a.ID(), "简体中文", 5); len(h) != 1 {
		t.Fatalf("memory after refresh: %+v", h)
	}
	if err := e.mem.Reindex(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hits, _ := e.mem.SearchHistory(a.ID(), "迁移计划", "", 10); len(hits) == 0 {
		t.Fatal("after reindex")
	}
}

func TestAsyncIndexingNeverBlocksAppends(t *testing.T) {
	e := newEnv(t, t.TempDir(), true)
	a, _ := e.agents.Create(agent.Spec{Name: "Ada"})
	ss, _ := a.Session()
	release := e.mem.BlockIndexing()
	start := time.Now()
	n := memory.QueueSize + 200
	for i := 0; i < n; i++ {
		if _, err := ss.Append(session.TypeUserMessage, "", session.UserMessage{Text: "filler message number " + strconv.Itoa(i)}); err != nil {
			release()
			t.Fatal(err)
		}
	}
	ss.Append(session.TypeUserMessage, "", session.UserMessage{Text: "the final narwhal message"})
	elapsed := time.Since(start)
	if !e.mem.Dirty() {
		release()
		t.Fatal("queue overflow should mark the index dirty")
	}
	release()
	if elapsed > 3*time.Second {
		t.Fatalf("appends blocked on indexing: %s", elapsed)
	}
	if err := e.mem.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.mem.Dirty() {
		t.Fatal("catch-up did not run")
	}
	// the dropped events were caught up from the log
	for _, q := range []string{"narwhal", "number " + strconv.Itoa(n-1)} {
		hits, err := e.mem.SearchHistory(a.ID(), q, ss.ID(), 5)
		if err != nil || len(hits) == 0 {
			t.Fatalf("%q: %+v %v", q, hits, err)
		}
	}
}
