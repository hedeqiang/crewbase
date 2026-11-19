package topics_test

import (
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
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/features/topics"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

// When the brief model fails, the next candidate (the agent's own model)
// writes it, and the agent's first turn waits for it.
func TestBriefFallsBackAndFirstTurnWaits(t *testing.T) {
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, nil}, {prompt.Plugin, nil}, {tools.Plugin, nil},
		{llm.Plugin, nil}, {mock.Plugin, map[string]any{"ids": []string{"big", "cheap"}}},
		{router.Plugin, map[string]any{"routes": []map[string]string{{"task": "title", "model": "cheap/default"}}}},
		{agent.Plugin, map[string]any{"root": t.TempDir(), "defaultModels": []string{"big/default"}}},
		{agentloop.Plugin, nil}, {topics.BriefPlugin, nil},
	} {
		if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
			t.Fatal(err)
		}
	}
	llms := kernel.Use[*llm.Registry](h.Root(), llm.ServiceKey)
	cheap, _ := llms.Get("cheap")
	cheap.(*mock.Adapter).Script = func(llm.Request) (llm.Response, error) {
		return llm.Response{}, &llm.Error{Kind: llm.ErrUnavailable, Message: "EOF (502)"}
	}
	big, _ := llms.Get("big")
	var system string
	big.(*mock.Adapter).Script = func(req llm.Request) (llm.Response, error) {
		if strings.Contains(req.System, "working instructions of an AI agent") {
			time.Sleep(300 * time.Millisecond) // the first message arrives meanwhile
			return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "你是翻译：把文本准确地译成目标语言。\n- 术语一致")}, nil
		}
		system = req.System
		return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: "end"}, nil
	}
	a, _ := kernel.Use[*agent.Registry](h.Root(), agent.ServiceKey).Create(agent.Spec{Name: "翻译"})
	time.Sleep(50 * time.Millisecond)
	a.Enqueue(agent.Input{Text: "hello"})
	agentloop.WaitIdle(a, 5*time.Second)
	b, _ := os.ReadFile(filepath.Join(a.Home(), "INSTRUCTIONS.md"))
	if !strings.HasPrefix(string(b), "你是翻译") {
		t.Fatalf("brief not written by the fallback: %q", b)
	}
	if !strings.Contains(system, "你是翻译") {
		t.Fatalf("first turn did not wait for the brief: %q", system)
	}
}
