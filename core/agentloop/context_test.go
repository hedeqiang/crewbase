package agentloop_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

type recCompactor struct {
	mu      sync.Mutex
	reasons []string
}

func (r *recCompactor) Compact(_ context.Context, _ *agent.Agent, _ *session.Session, _ llm.ModelRef, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reasons = append(r.reasons, reason)
	return nil
}

func (r *recCompactor) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reasons)
}

func noop(context.Context, tools.Call) (tools.Result, error) { return tools.Result{Content: "ok"}, nil }

// Blank chats have no skills catalog (chatFragments), so they are not
// offered skill_load either; agents are.
func TestBlankChatHasNoTools(t *testing.T) {
	e := newEnv(t, t.TempDir())
	r := e.h.Root()
	tr := kernel.Use[*tools.Registry](r, tools.ServiceKey)
	tr.Register(r, &tools.Tool{Name: "skill_load", Description: "x", Execute: noop})
	tr.Register(r, &tools.Tool{Name: "read_file", Description: "x", Execute: noop})
	c, err := e.agents.Create(agent.Spec{Kind: agent.KindChat})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := e.agents.Create(agent.Spec{Name: "Otto"})
	has := func(ts []llm.ToolSchema, name string) bool {
		for _, t := range ts {
			if t.Name == name {
				return true
			}
		}
		return false
	}
	if ts := agentloop.ToolSchemas(tr, c, "agent"); len(ts) != 0 {
		t.Fatalf("a blank chat offers no tools: %+v", ts)
	}
	if ts := agentloop.ToolSchemas(tr, a, "agent"); !has(ts, "skill_load") {
		t.Fatal("agents keep skill_load")
	}
	ad, _ := e.llms.Get("claude-code")
	m := ad.(*mock.Adapter)
	send(t, c, "hi")
	agentloop.WaitIdle(c, 2*time.Second)
	reqs := m.Requests()
	if len(reqs) == 0 {
		t.Fatal("no request")
	}
	if len(reqs[0].Tools) != 0 || reqs[0].System != "" {
		t.Fatalf("chat request: system %q, tools %+v", reqs[0].System, reqs[0].Tools)
	}
}
