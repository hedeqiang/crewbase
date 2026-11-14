package taskboard_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/taskboard"
	"github.com/hedeqiang/crewbase/kernel"
)

func setup(t *testing.T) (*taskboard.Service, *session.Store, *session.Session) {
	t.Helper()
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	if _, err := h.Load(session.Plugin, kernel.LoadOptions{Config: map[string]any{"dir": t.TempDir()}}); err != nil {
		t.Fatal(err)
	}
	st := kernel.Use[*session.Store](h.Root(), session.ServiceKey)
	known := map[string]bool{"lead": true, "dev": true, "qa": true, "member:owner": true}
	s := taskboard.New(h, st, func(o string) bool { return known[o] })
	root, _ := st.Create(session.Meta{Kind: "agent", Title: "lead", Agents: []string{"lead"}})
	return s, st, root
}

func str(s string) *string        { return &s }
func list(xs ...string) *[]string { return &xs }

func TestDAG(t *testing.T) {
	s, st, root := setup(t)
	lead := taskboard.Actor{Agent: "lead"}
	a, err := s.Add(root.ID(), lead, taskboard.Change{Title: str("设计接口"), Owner: str("lead"), Scope: list("api/", "./docs/api.md")})
	if err != nil || a.ID != "1" || !a.Ready || strings.Join(a.Scope, ",") != "api,docs/api.md" {
		t.Fatal(a, err)
	}
	b, _ := s.Add(root.ID(), lead, taskboard.Change{Title: str("实现"), Owner: str("dev"), Deps: list("1")})
	c, _ := s.Add(root.ID(), lead, taskboard.Change{Title: str("测试"), Deps: list("#2", "1")})
	if b.Ready || len(b.Waiting) != 1 || c.Ready {
		t.Fatalf("deps not waiting: %+v %+v", b, c)
	}
	// a sub-agent session shares the root's board
	child, _ := st.Create(session.Meta{Kind: "agent", Parent: root.ID(), Agents: []string{"dev"}})
	board, _ := s.Get(child.ID())
	if board.Root != root.ID() || len(board.Tasks) != 3 {
		t.Fatalf("child sees %+v", board)
	}
	dev := taskboard.Actor{Agent: "dev"}
	// cannot start before deps are done
	if _, err := s.Update(child.ID(), "2", dev, taskboard.Change{Status: str("doing")}); err == nil || !strings.Contains(err.Error(), "#1") {
		t.Fatalf("started early: %v", err)
	}
	// cannot touch someone else's task
	if _, err := s.Update(child.ID(), "1", dev, taskboard.Change{Status: str("done")}); err == nil {
		t.Fatal("dev finished lead's task")
	}
	// cycles are refused (1 → 3 → 1)
	if _, err := s.Update(root.ID(), "1", taskboard.Actor{Person: "你"}, taskboard.Change{Deps: list("3")}); err != taskboard.ErrCycle {
		t.Fatalf("cycle: %v", err)
	}
	if _, err := s.Update(root.ID(), "1", lead, taskboard.Change{Status: str("done")}); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Update(child.ID(), "2", dev, taskboard.Change{Status: str("doing")}); err != nil || v.Status != "doing" {
		t.Fatal(v, err)
	}
	// an agent claims an unassigned task by starting it... once its deps are done
	s.Update(child.ID(), "2", dev, taskboard.Change{Status: str("done")})
	v, err := s.Update(child.ID(), "3", taskboard.Actor{Agent: "qa"}, taskboard.Change{Status: str("doing")})
	if err != nil || v.Owner != "qa" {
		t.Fatalf("claim: %+v %v", v, err)
	}
	// agents cannot assign to others; blocked needs a reason
	if _, err := s.Update(root.ID(), "3", taskboard.Actor{Agent: "qa"}, taskboard.Change{Owner: str("dev")}); err == nil {
		t.Fatal("agent reassigned a task")
	}
	if _, err := s.Update(root.ID(), "3", taskboard.Actor{Agent: "qa"}, taskboard.Change{Status: str("blocked")}); err == nil {
		t.Fatal("blocked without a note")
	}
	if _, err := s.Add(root.ID(), lead, taskboard.Change{Title: str("x"), Owner: str("ghost")}); err == nil {
		t.Fatal("unknown owner accepted")
	}
	if _, err := s.Add(root.ID(), lead, taskboard.Change{Title: str("x"), Scope: list("../etc")}); err == nil {
		t.Fatal("scope outside the workspace accepted")
	}
	// people remove tasks; dependents lose the edge
	if err := s.Remove(root.ID(), "2", lead); err == nil {
		t.Fatal("agent removed a task")
	}
	if err := s.Remove(root.ID(), "2", taskboard.Actor{Person: "你"}); err != nil {
		t.Fatal(err)
	}
	board, _ = s.Get(root.ID())
	if len(board.Tasks) != 2 || strings.Join(board.Tasks[1].Deps, ",") != "1" {
		t.Fatalf("after remove: %+v", board.Tasks)
	}
	// the board is the session log: a fresh service replays it
	s2 := taskboard.New(nil, st, nil)
	if b2, _ := s2.Get(child.ID()); len(b2.Tasks) != 2 || b2.Tasks[1].Owner != "qa" {
		t.Fatalf("replay %+v", b2)
	}
	if !strings.Contains(taskboard.Format(board, "qa"), "#3 [doing] 测试 — qa (you); after #1") {
		t.Fatalf("format:\n%s", taskboard.Format(board, "qa"))
	}
}

// The tools work end to end through the plugin and the tool registry.
func TestTools(t *testing.T) {
	h := kernel.NewHost(nil)
	defer h.Close()
	root := t.TempDir()
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, map[string]any{"dir": root + "/sessions"}},
		{prompt.Plugin, nil}, {tools.Plugin, nil}, {llm.Plugin, nil},
		{agent.Plugin, map[string]any{"root": root}},
		{taskboard.Plugin, nil},
	} {
		if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
			t.Fatal(err)
		}
	}
	agents := kernel.Use[*agent.Registry](h.Root(), agent.ServiceKey)
	lead, _ := agents.Create(agent.Spec{Name: "lead"})
	dev, _ := agents.Create(agent.Spec{Name: "dev"})
	chat, _ := agents.Create(agent.Spec{Kind: agent.KindChat})
	ss, _ := lead.Session()
	reg := kernel.Use[*tools.Registry](h.Root(), tools.ServiceKey)
	call := func(who *agent.Agent, name string, args any) tools.Result {
		t.Helper()
		tl, ok := reg.Get(who.ID(), name)
		if !ok {
			t.Fatalf("tool %s missing", name)
		}
		b, _ := json.Marshal(args)
		r, err := tl.Execute(context.Background(), tools.Call{Agent: who.ID(), Session: ss, Args: b})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := call(lead, "task_add", map[string]any{"title": "plan", "owner": lead.ID()}); r.IsError || r.Content != "added task #1" {
		t.Fatal(r)
	}
	if r := call(lead, "task_add", map[string]any{"title": "build", "owner": dev.ID(), "deps": []any{1}}); r.IsError {
		t.Fatal(r)
	}
	if r := call(lead, "task_add", map[string]any{"title": "x", "owner": chat.ID()}); !r.IsError {
		t.Fatal("a blank chat cannot own tasks")
	}
	if r := call(lead, "task_update", map[string]any{"id": "1", "status": "done"}); !strings.Contains(r.Content, "now ready: #2") {
		t.Fatal(r)
	}
	r := call(dev, "task_list", map[string]any{})
	if !strings.Contains(r.Content, "#2 [ready] build — "+dev.ID()+" (you)") {
		t.Fatalf("list:\n%s", r.Content)
	}
	// open tasks reach the model's system prompt
	req, err := agentloop.Request.Run(kernel.For(h.Root(), dev.ID()), context.Background(), agentloop.RequestArgs{Agent: dev, Session: ss, Request: &llm.Request{System: "base"}},
		func(_ context.Context, x agentloop.RequestArgs) (*llm.Request, error) { return x.Request, nil })
	if err != nil || !strings.Contains(req.System, "#2 [ready] build") || !strings.HasPrefix(req.System, "base") {
		t.Fatalf("system: %v %q", err, req.System)
	}
}
