package memory_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
