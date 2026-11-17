package agentloop_test

import (
	"context"
	"strings"
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

// ownTools is a mock adapter whose model runs its own tools, like a coding
// CLI over ACP.
type ownTools struct{ *mock.Adapter }

func (ownTools) RunsOwnTools() bool { return true }

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

type fakeBridge []llm.ToolSchema

func (b fakeBridge) BridgedSchemas(string, string) []llm.ToolSchema { return b }

func noop(context.Context, tools.Call) (tools.Result, error) { return tools.Result{Content: "ok"}, nil }

// A CLI that runs its own tools never sees Crewbase's tool definitions, so
// they must not push it over the compaction threshold; an API model that
// is sent the same tools still counts them.
func TestCompactionIgnoresToolsTheCLINeverSees(t *testing.T) {
	e := newEnv(t, t.TempDir())
	r := e.h.Root()
	cp := &recCompactor{}
	if err := r.Provide(agentloop.CompactionKey, cp); err != nil {
		t.Fatal(err)
	}
	tr := kernel.Use[*tools.Registry](r, tools.ServiceKey)
	big := strings.Repeat("a long tool description ", 400) // ≈2.4k tokens each
	for _, n := range []string{"t1", "t2", "t3", "t4", "t5"} {
		tr.Register(r, &tools.Tool{Name: n, Description: big, Execute: noop})
	}
	// window 14k: the tools alone (≈12k) are over 80%
	cli := ownTools{mock.New("cli", 14000)}
	api := mock.New("api", 14000)
	if err := e.llms.Register(r, cli); err != nil {
		t.Fatal(err)
	}
	if err := e.llms.Register(r, api); err != nil {
		t.Fatal(err)
	}

	ca, err := e.agents.Create(agent.Spec{Name: "Cli", Models: []llm.ModelRef{{Provider: "cli", Model: "default"}}})
	if err != nil {
		t.Fatal(err)
	}
	send(t, ca, "hello")
	agentloop.WaitIdle(ca, 2*time.Second)
	if n := cp.count(); n != 0 {
		t.Fatalf("a CLI agent compacted %d times over tools it never sees", n)
	}
	if len(cli.Requests()) == 0 {
		t.Fatal("cli not asked")
	}
	ss, _ := e.agents.Sessions().Get(ca.Spec().Session)
	var st agentloop.StepStart
	if ev, ok := ss.Last(session.TypeStepStart); !ok || ev.Decode(&st) != nil || st.ContextTokens > 2000 {
		t.Fatalf("CLI step context %d counts the tool registry", st.ContextTokens)
	}

	// bridged tools (Crewbase's MCP server) do count for the CLI
	if err := r.Provide(agentloop.ToolBridgeKey, fakeBridge{{Name: "computer_act", Description: big}}); err != nil {
		t.Fatal(err)
	}
	req := llm.Request{Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, Tools: tr.SchemasFor(ca.ID(), "")}
	n := agentloop.EstimateContext(r, cli, ca, "", req)
	if n < 2000 || n > 3000 {
		t.Fatalf("bridged estimate %d, want only the one bridged tool", n)
	}
	if got := agentloop.EstimateContext(r, api, ca, "", req); got != llm.EstimateRequest(req) {
		t.Fatalf("API estimate %d != EstimateRequest %d", got, llm.EstimateRequest(req))
	}

	aa, err := e.agents.Create(agent.Spec{Name: "Api", Models: []llm.ModelRef{{Provider: "api", Model: "default"}}})
	if err != nil {
		t.Fatal(err)
	}
	send(t, aa, "hello")
	agentloop.WaitIdle(aa, 2*time.Second)
	if n := cp.count(); n != 1 {
		t.Fatalf("an API agent sent the same tools must compact (got %d)", n)
	}
}

// Blank chats have no skills catalog (chatFragments), so they are not
// offered skill_load either; agents are.
func TestBlankChatHasNoSkillLoad(t *testing.T) {
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
	if ts := agentloop.ToolSchemas(tr, c, "agent"); has(ts, "skill_load") || !has(ts, "read_file") {
		t.Fatalf("chat tools %+v", ts)
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
	if has(reqs[0].Tools, "skill_load") || !has(reqs[0].Tools, "read_file") {
		t.Fatalf("chat request tools %+v", reqs[0].Tools)
	}
}
