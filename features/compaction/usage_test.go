package compaction_test

import (
	"context"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

func nop(context.Context, tools.Call) (tools.Result, error) { return tools.Text("ok"), nil }

// The meter counts the tools of the session's kind, as the loop sends them.
func TestUsageFollowsSessionKind(t *testing.T) {
	e := newEnv(t, t.TempDir(), nil, func(load func(kernel.Spec, map[string]any)) {
		load(kernel.Spec{Name: "test-kinds", Inject: []string{tools.ServiceKey}, Apply: func(c *kernel.Context) error {
			kernel.Use[*tools.Registry](c, tools.ServiceKey).Register(c, &tools.Tool{
				Name: "channel_only", Description: strings.Repeat("only in channels ", 300), SessionKinds: []string{"channel"}, Execute: nop})
			return nil
		}}, nil)
	})
	a, _ := e.agents.Create(agent.Spec{Name: "K"})
	ss, _ := a.Session()
	b, err := e.comp.Usage(a.ID(), "")
	if err != nil {
		t.Fatal(err)
	}
	want := 0
	for _, s := range agentloop.ToolSchemas(e.tools, a, ss.Meta().Kind) {
		if s.Name == "channel_only" {
			t.Fatal("channel tool offered in a direct session")
		}
	}
	for _, s := range b.Segments {
		if s.Key == "tools" {
			want = s.Tokens
		}
	}
	if want >= 300*4 {
		t.Fatalf("tools segment %d counts the channel-only tool", want)
	}
}

// Deferred tools held back by tool search are not counted: the meter shows
// what the next request carries, not every registered tool.
func TestUsageLeavesOutDeferredTools(t *testing.T) {
	e := newEnv(t, t.TempDir(), nil, func(load func(kernel.Spec, map[string]any)) {
		load(kernel.Spec{Name: "test-deferred", Inject: []string{tools.ServiceKey}, Apply: func(c *kernel.Context) error {
			reg := kernel.Use[*tools.Registry](c, tools.ServiceKey)
			for i := range 60 {
				reg.Register(c, &tools.Tool{Name: "bulky__tool_" + string(rune('a'+i%26)) + string(rune('a'+i/26)),
					Description: strings.Repeat("a large connector tool ", 60), Load: tools.LoadDeferred, Group: "bulky", Execute: nop})
			}
			return nil
		}}, nil)
	})
	a, _ := e.agents.Create(agent.Spec{Name: "K"})
	b, err := e.comp.Usage(a.ID(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range b.Segments {
		// 60 × ~1400 chars would be ~21k tokens if they were all counted
		if s.Key == "tools" && s.Tokens > 8000 {
			t.Fatalf("tools segment %d counts deferred tools", s.Tokens)
		}
	}
}

// The meter itemises the request the loop really sent: text a Request
// listener added (like an attachment's extracted text) is a block of its
// own, not an unexplained remainder.
func TestUsageShowsWhatTheLoopSent(t *testing.T) {
	big := strings.Repeat("extracted document text ", 2000)
	e := newEnv(t, t.TempDir(), nil, func(load func(kernel.Spec, map[string]any)) {
		load(kernel.Spec{Name: "test-inject", Apply: func(c *kernel.Context) error {
			agentloop.Request.Use(c, func(ctx context.Context, x agentloop.RequestArgs, next kernel.Next[agentloop.RequestArgs, *llm.Request]) (*llm.Request, error) {
				x.Request.System += "\n\n" + big
				return next(ctx, x)
			})
			return nil
		}}, nil)
	})
	a, _ := e.agents.Create(agent.Spec{Name: "K"})
	ss, _ := a.Session()
	before, _ := e.comp.Usage(a.ID(), "")
	send(t, a, ss.ID(), "hello")
	b, err := e.comp.Usage(a.ID(), "")
	if err != nil {
		t.Fatal(err)
	}
	var sys int
	for _, s := range b.Segments {
		if s.Key == "system" {
			sys = s.Tokens
		}
	}
	want := llm.EstimateTokens(big)
	if sys < want || b.Estimated < before.Estimated+want {
		t.Fatalf("system %d, estimated %d → %d; want the %d added tokens counted", sys, before.Estimated, b.Estimated, want)
	}
	found := false
	for _, it := range b.Items {
		if it.Label == "additions" && it.Tokens >= want {
			found = true
		}
	}
	if !found {
		t.Fatalf("no additions item: %+v", b.Items)
	}
}
