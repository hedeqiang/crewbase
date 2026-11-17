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
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

// cliAdapter is an ACP-like adapter: its model runs its own tools and
// reports its real context size.
type cliAdapter struct {
	*mock.Adapter
	context int
}

func (*cliAdapter) RunsOwnTools() bool { return true }

func (c *cliAdapter) Stream(ctx context.Context, req llm.Request, on func(llm.Chunk) error) (llm.Response, error) {
	r, err := c.Adapter.Stream(ctx, req, on)
	r.Usage.Context = c.context
	return r, err
}

type bridge []llm.ToolSchema

func (b bridge) BridgedSchemas(string, string) []llm.ToolSchema { return b }

func nop(context.Context, tools.Call) (tools.Result, error) { return tools.Text("ok"), nil }

func TestUsageForACLIThatRunsItsOwnTools(t *testing.T) {
	const win = 200000
	cli := &cliAdapter{Adapter: mock.New("cli", win)}
	e := newEnv(t, t.TempDir(), nil, func(load func(kernel.Spec, map[string]any)) {
		load(kernel.Spec{Name: "test-cli", Inject: []string{llm.ServiceKey, tools.ServiceKey}, Apply: func(c *kernel.Context) error {
			reg := kernel.Use[*tools.Registry](c, tools.ServiceKey)
			big := strings.Repeat("a long tool description ", 200)
			for i := range 30 {
				reg.Register(c, &tools.Tool{Name: "reg_" + string(rune('a'+i)), Description: big, Execute: nop})
			}
			return kernel.Use[*llm.Registry](c, llm.ServiceKey).Register(c, cli)
		}}, nil)
	})
	r := e.h.Root()
	if err := r.Provide(agentloop.ToolBridgeKey, bridge{{Name: "computer_act", Description: "act on an app"}}); err != nil {
		t.Fatal(err)
	}
	a, err := e.agents.Create(agent.Spec{Name: "Cc", Models: []llm.ModelRef{{Provider: "cli", Model: "default"}}})
	if err != nil {
		t.Fatal(err)
	}
	ss, _ := a.Session()

	// before any reply: an estimate whose tools are only the bridged one
	b, err := e.comp.Usage(a.ID(), "")
	if err != nil {
		t.Fatal(err)
	}
	full := 0
	for range e.tools.SchemasFor(a.ID(), ss.Meta().Kind) {
		full++
	}
	var tools, cliSeg int
	var managed bool
	for _, s := range b.Segments {
		switch s.Key {
		case "tools":
			tools, managed = s.Tokens, s.Managed
		case "cli":
			cliSeg = s.Tokens
		}
	}
	if !b.ToolsManaged || !managed || tools <= 0 || tools > 100 || full < 30 {
		t.Fatalf("tools segment %d managed=%v counts the registry (%d tools): %+v", tools, managed, full, b)
	}
	if b.Reported != 0 || b.Used != b.Estimated || cliSeg != 0 {
		t.Fatalf("nothing reported yet: %+v", b)
	}

	// after a reply: the CLI's reported usage is the total
	cli.context = 50000
	send(t, a, ss.ID(), "hello")
	b, _ = e.comp.Usage(a.ID(), "")
	if b.Reported != 50000 || b.Used != 50000 || b.Estimated >= 50000 || b.Free != win-50000 {
		t.Fatalf("reported usage does not win: %+v", b)
	}
	sum := 0
	for _, s := range b.Segments {
		if s.Key == "cli" {
			cliSeg = s.Tokens
		}
		if s.Key != "free" {
			sum += s.Tokens
		}
	}
	if sum != b.Used || cliSeg != b.Reported-b.Estimated {
		t.Fatalf("segments %+v do not add up to the reported %d", b.Segments, b.Reported)
	}
	if b.OverThreshold {
		t.Fatal("over threshold")
	}

	// an API agent with the same tools counts them all
	api, err := e.agents.Create(agent.Spec{Name: "Api"})
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := e.comp.Usage(api.ID(), "")
	for _, s := range b2.Segments {
		if s.Key == "tools" && (s.Managed || s.Tokens < 30*200) {
			t.Fatalf("API agent tools segment %+v", s)
		}
	}
	if b2.ToolsManaged || b2.Reported != 0 {
		t.Fatalf("API agent %+v", b2)
	}
}

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
