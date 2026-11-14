package topics_test

import (
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

func TestTopicFromCheapRoute(t *testing.T) {
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
		{agentloop.Plugin, nil}, {topics.Plugin, nil},
	} {
		h.Load(x.s, kernel.LoadOptions{Config: x.cfg})
	}
	llms := kernel.Use[*llm.Registry](h.Root(), llm.ServiceKey)
	cheap, _ := llms.Get("cheap")
	cheap.(*mock.Adapter).Script = func(req llm.Request) (llm.Response, error) {
		return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "「定价页改版」\n")}, nil
	}
	a, _ := kernel.Use[*agent.Registry](h.Root(), agent.ServiceKey).Create(agent.Spec{Name: "Kai"})
	a.Enqueue(agent.Input{Text: "帮我改一下定价页"})
	agentloop.WaitIdle(a, 2*time.Second)
	ss, _ := a.Session()
	deadline := time.Now().Add(2 * time.Second)
	for ss.Meta().Topic == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if ss.Meta().Topic != "定价页改版" {
		t.Fatalf("topic %q", ss.Meta().Topic)
	}
	big, _ := llms.Get("big")
	if n := len(big.(*mock.Adapter).Requests()); n != 1 {
		t.Fatalf("main model must not be used for topics (%d requests)", n)
	}
}
