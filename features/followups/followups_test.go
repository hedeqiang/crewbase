package followups

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/features/schedule"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type env struct {
	h      *kernel.Host
	agents *agent.Registry
	sched  *schedule.Service
	s      *Service
	tools  *tools.Registry
	clk    *clock
}

func newEnv(t *testing.T, root string, clk *clock) *env {
	t.Helper()
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	load := func(s kernel.Spec, cfg map[string]any) {
		if _, err := h.Load(s, kernel.LoadOptions{Config: cfg}); err != nil {
			t.Fatal(err)
		}
	}
	load(session.Plugin, map[string]any{"dir": root + "/sessions"})
	load(prompt.Plugin, nil)
	load(tools.Plugin, nil)
	load(approval.Plugin, nil)
	load(llm.Plugin, nil)
	load(mock.Plugin, map[string]any{"ids": []string{"m"}})
	load(router.Plugin, nil)
	load(agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"m/default"}})
	load(agentloop.Plugin, nil)
	load(kernel.Spec{Name: "test-schedule", Apply: func(c *kernel.Context) error {
		s, err := schedule.New(c, root, clk.now)
		if err != nil {
			return err
		}
		return c.Provide(schedule.ServiceKey, s)
	}}, nil)
	load(Plugin, nil)
	r := h.Root()
	e := &env{h: h, agents: kernel.Use[*agent.Registry](r, agent.ServiceKey), sched: kernel.Use[*schedule.Service](r, schedule.ServiceKey),
		s: kernel.Use[*Service](r, ServiceKey), tools: kernel.Use[*tools.Registry](r, tools.ServiceKey), clk: clk}
	e.s.now = clk.now
	return e
}

func (e *env) call(t *testing.T, a *agent.Agent, ss *session.Session, name, args string) tools.Result {
	t.Helper()
	exe := e.tools.Run(context.Background(), e.h.Root(), tools.Call{ID: "c" + name, Name: name, Agent: a.ID(), Session: ss, Args: json.RawMessage(args)})
	return exe.Result
}

func TestFollowupComesBack(t *testing.T) {
	root := t.TempDir()
	clk := &clock{t: time.Date(2026, 11, 15, 10, 0, 0, 0, time.Local)}
	e := newEnv(t, root, clk)
	a, _ := e.agents.Create(agent.Spec{Name: "Ops"})
	ss, _ := a.Session()

	// bad requests
	for _, args := range []string{`{"note":"x"}`, `{"in":"20s","note":"x"}`, `{"in":"100d","note":"x"}`, `{"in":"20m"}`, `{"in":"20m","at":"09:00","note":"x"}`, `{"in":"soon","note":"x"}`} {
		if r := e.call(t, a, ss, "followup_set", args); !r.IsError {
			t.Fatalf("%s accepted: %s", args, r.Content)
		}
	}
	r := e.call(t, a, ss, "followup_set", `{"in":"20m","note":"check whether CI on PR #12 passed"}`)
	if r.IsError || !strings.Contains(r.Content, "10:20") {
		t.Fatalf("set: %+v", r)
	}
	e.call(t, a, ss, "followup_set", `{"at":"09:00","note":"morning standup notes"}`) // tomorrow 09:00
	if r := e.call(t, a, ss, "followup_list", `{}`); !strings.Contains(r.Content, "check whether CI") || !strings.Contains(r.Content, "2026-11-16 09:00") {
		t.Fatalf("list: %s", r.Content)
	}

	// not yet due
	clk.add(19 * time.Minute)
	e.sched.Tick(context.Background())
	if q := a.Queue(); len(q) != 0 {
		t.Fatalf("fired early: %+v", q)
	}
	// due: the note comes back in the same conversation, once
	a.Pause()
	clk.add(2 * time.Minute)
	e.sched.Tick(context.Background())
	e.sched.Tick(context.Background())
	q := a.Queue()
	if len(q) != 1 || q[0].Session != ss.ID() || q[0].From != "followup" || !strings.Contains(q[0].Text, "check whether CI on PR #12 passed") || !strings.HasPrefix(q[0].Tag, "followup:") {
		t.Fatalf("queue: %+v", q)
	}
	// the fired job is removed; the other stays
	deadline := time.Now().Add(2 * time.Second)
	for len(e.sched.List(Owner)) != 1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if fs := e.s.List(a.ID()); len(fs) != 1 || !strings.Contains(fs[0].Note, "standup") {
		t.Fatalf("pending after fire: %+v", fs)
	}

	// cancel, and only one's own
	other, _ := e.agents.Create(agent.Spec{Name: "Other"})
	id := e.s.List(a.ID())[0].ID
	if r := e.call(t, other, nil, "followup_cancel", `{"id":"`+id+`"}`); !r.IsError {
		t.Fatal("another agent cancelled it")
	}
	if r := e.call(t, a, ss, "followup_cancel", `{"id":"`+id+`"}`); r.IsError {
		t.Fatalf("cancel: %s", r.Content)
	}
	if len(e.s.List(a.ID())) != 0 {
		t.Fatal("not cancelled")
	}

	// a deleted agent's follow-ups go with it
	e.call(t, other, nil, "followup_set", `{"in":"1h","note":"x"}`)
	if err := e.agents.Delete(other.ID()); err != nil {
		t.Fatal(err)
	}
	if n := len(e.sched.List(Owner)); n != 0 {
		t.Fatalf("%d jobs left", n)
	}
}

func TestFollowupSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	clk := &clock{t: time.Date(2026, 11, 15, 10, 0, 0, 0, time.Local)}
	e := newEnv(t, root, clk)
	a, _ := e.agents.Create(agent.Spec{Name: "Ops"})
	ss, _ := a.Session()
	e.call(t, a, ss, "followup_set", `{"in":"1h","note":"deploy finished?"}`)
	e.h.Close()

	e2 := newEnv(t, root, clk)
	a2, _ := e2.agents.Get(a.ID())
	a2.Pause()
	clk.add(61 * time.Minute)
	e2.sched.Tick(context.Background())
	if q := a2.Queue(); len(q) != 1 || !strings.Contains(q[0].Text, "deploy finished?") {
		t.Fatalf("after restart: %+v", q)
	}
}

func TestParse(t *testing.T) {
	for in, want := range map[string]time.Duration{"20m": 20 * time.Minute, "2h30m": 150 * time.Minute, "1d": 24 * time.Hour, "1d12h": 36 * time.Hour} {
		if d, err := parseIn(in); err != nil || d != want {
			t.Errorf("%s = %v %v", in, d, err)
		}
	}
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.Local)
	if at, _ := parseAt("09:00", now); at.Day() != 16 || at.Hour() != 9 {
		t.Errorf("past time today should be tomorrow: %v", at)
	}
	if at, _ := parseAt("11:30", now); at.Day() != 15 {
		t.Errorf("later today: %v", at)
	}
}
