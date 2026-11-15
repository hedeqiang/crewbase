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
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/memory"
	"github.com/hedeqiang/crewbase/kernel"
)

func workingEnv(t *testing.T) (*env, *memory.Working, *prompt.Assembler) {
	t.Helper()
	e := newEnv(t, t.TempDir(), false)
	if _, err := e.h.Load(memory.WorkingPlugin, kernel.LoadOptions{}); err != nil {
		t.Fatal(err)
	}
	r := e.h.Root()
	return e, kernel.Use[*memory.Working](r, memory.WorkingKey), kernel.Use[*prompt.Assembler](r, prompt.ServiceKey)
}

func runTool(t *testing.T, e *env, a *agent.Agent, name string, args any) tools.Execution {
	t.Helper()
	raw, _ := json.Marshal(args)
	ss, _ := a.Session()
	return e.tools.Run(context.Background(), e.h.Root(), tools.Call{ID: name + "-1", Name: name, Args: raw, Agent: a.ID(), Session: ss})
}

func TestTaskProgressFiles(t *testing.T) {
	e, _, pa := workingEnv(t)
	a, _ := e.agents.Create(agent.Spec{Name: "Ops"})
	if s := pa.Assemble(a.ID()); strings.Contains(s, "Tasks in progress") {
		t.Fatal("empty task list shown")
	}
	ex := runTool(t, e, a, "task_progress", map[string]any{"task": "Migrate billing/v2", "text": "# Move billing to v2\n- [x] schema\n- [ ] backfill"})
	if ex.Result.IsError {
		t.Fatal(ex.Result.Content)
	}
	s := pa.Assemble(a.ID())
	if !strings.Contains(s, "- Migrate-billing-v2 (updated") || !strings.Contains(s, "Move billing to v2") {
		t.Fatalf("prompt: %s", s)
	}
	if strings.Contains(s, "backfill") {
		t.Fatal("only the first line belongs in the prompt")
	}
	if got := runTool(t, e, a, "task_progress_read", map[string]any{"task": "Migrate billing/v2"}).Result.Content; !strings.Contains(got, "backfill") {
		t.Fatalf("read: %q", got)
	}
	// marking done archives it and takes it off the list
	if ex := runTool(t, e, a, "task_progress", map[string]any{"task": "Migrate-billing-v2", "done": true}); ex.Result.IsError {
		t.Fatal(ex.Result.Content)
	}
	if s := pa.Assemble(a.ID()); strings.Contains(s, "Tasks in progress") {
		t.Fatal("done task still listed")
	}
	done, _ := os.ReadDir(filepath.Join(a.Home(), "tasks", "done"))
	if len(done) != 1 {
		t.Fatalf("archived = %d", len(done))
	}
	// names cannot escape the tasks folder
	runTool(t, e, a, "task_progress", map[string]any{"task": "../../evil", "text": "x"})
	if _, err := os.Stat(filepath.Join(a.Home(), "..", "evil.md")); err == nil {
		t.Fatal("escaped the tasks folder")
	}
}

func TestReferenceNotesIndexed(t *testing.T) {
	e, w, pa := workingEnv(t)
	a, _ := e.agents.Create(agent.Spec{Name: "Sales"})
	body := "# 2026 价格表\n" + strings.Repeat("专业版每席位每月 99 元。\n", 2000)
	if _, err := w.WriteNote(a, "pricing.md", body); err != nil {
		t.Fatal(err)
	}
	s := pa.Assemble(a.ID())
	if !strings.Contains(s, "- pricing.md — 2026 价格表") {
		t.Fatalf("index: %s", s)
	}
	if strings.Count(s, "99 元") > 0 {
		t.Fatal("note body must not be in the prompt")
	}
	part := runTool(t, e, a, "note_read", map[string]any{"name": "pricing.md"}).Result.Content
	if !strings.Contains(part, "99 元") || !strings.Contains(part, "continue with from=") {
		t.Fatalf("read: %.200q", part)
	}
	if _, err := w.WriteNote(a, "pricing.md", ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(pa.Assemble(a.ID()), "pricing.md") {
		t.Fatal("removed note still indexed")
	}
	if _, err := w.WriteNote(a, "bin.md", string([]byte{0xff, 0xfe})); err == nil {
		t.Fatal("binary note accepted")
	}
}

func TestToldRecordFromReports(t *testing.T) {
	e, w, pa := workingEnv(t)
	a, _ := e.agents.Create(agent.Spec{Name: "Watcher"})
	ss, _ := a.Session()
	// a routine's report is recorded; a person's own conversation is not
	if _, err := a.Enqueue(agent.Input{Session: ss.ID(), Tag: "routine:daily:1", From: "routine", Text: "检查竞品价格"}); err != nil {
		t.Fatal(err)
	}
	agentloop.WaitIdle(a, 3*time.Second)
	send(t, a, "随便聊聊")
	agentloop.WaitIdle(a, 3*time.Second)
	told := w.Told(a)
	if len(told) != 1 || told[0].Via != "routine" || !strings.Contains(told[0].Text, "检查竞品价格") {
		t.Fatalf("told = %+v", told)
	}
	if s := pa.Assemble(a.ID()); !strings.Contains(s, "What you already told the user") || !strings.Contains(s, "routine: default: 检查竞品价格") {
		t.Fatalf("prompt: %s", s)
	}
	// the list is capped
	for i := 0; i < memory.ToldKeep+5; i++ {
		_, _ = a.Enqueue(agent.Input{Session: ss.ID(), Tag: "followup:x", Text: "ping"})
		agentloop.WaitIdle(a, 3*time.Second)
	}
	if n := len(w.Told(a)); n != memory.ToldKeep {
		t.Fatalf("kept %d", n)
	}
	if n := strings.Count(pa.Assemble(a.ID()), ", followup: "); n != memory.ToldShown {
		t.Fatalf("shown %d", n)
	}
}

// Chats and sub-agents keep none of it.
func TestWorkingMemoryNotForChats(t *testing.T) {
	e, _, pa := workingEnv(t)
	c, _ := e.agents.Create(agent.Spec{Name: "chat", Kind: agent.KindChat})
	if ex := runTool(t, e, c, "task_progress", map[string]any{"task": "x", "text": "y"}); !ex.Result.IsError {
		t.Fatal("chat kept a task")
	}
	if s := pa.Assemble(c.ID()); strings.Contains(s, "Tasks in progress") {
		t.Fatal(s)
	}
}

// Notes put in notes/ by hand (or by the agent's file tools) keep their
// names: as listed, they can be read and removed.
func TestNotesWithAnyName(t *testing.T) {
	e, w, _ := workingEnv(t)
	a, _ := e.agents.Create(agent.Spec{Name: "Ops"})
	dir := filepath.Join(a.Home(), "notes")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	names := []string{"Q3 plan.md", "README.MD", "价格(2026).txt"}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("# "+n+"\nbody"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	listed := map[string]bool{}
	for _, n := range w.Notes(a) {
		listed[n.Name] = true
	}
	for _, n := range names {
		if !listed[n] {
			t.Fatalf("%q not listed: %v", n, listed)
		}
		if s, err := w.ReadNote(a, n, 0); err != nil || !strings.Contains(s, "body") {
			t.Fatalf("read %q: %q %v", n, s, err)
		}
	}
	if _, err := w.WriteNote(a, "Q3 plan.md", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Q3 plan.md")); !os.IsNotExist(err) {
		t.Fatal("not removed")
	}
	// still confined to notes/
	_ = os.WriteFile(filepath.Join(a.Home(), "secret.md"), []byte("x"), 0o600)
	if _, err := w.ReadNote(a, "../secret.md", 0); err == nil {
		t.Fatal("read outside notes/")
	}
}
