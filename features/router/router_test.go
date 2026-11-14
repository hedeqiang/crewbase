package router_test

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
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

func TestClassify(t *testing.T) {
	cases := map[string]agent.Input{
		router.TaskRoutine:   {Text: "check", Tag: "routine:x:y"},
		router.TaskSummarize: {Text: "请帮我总结这篇文章"},
		router.TaskCode:      {Text: "Refactor the parser and write a test"},
		router.TaskDefault:   {Text: "hello"},
	}
	for want, in := range cases {
		if got := router.Classify(in); got != want {
			t.Errorf("%q: got %q want %q", in.Text, got, want)
		}
	}
}

func TestTaskRoutingFallsBackWhenUnusable(t *testing.T) {
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	root := t.TempDir()
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, nil}, {prompt.Plugin, nil}, {tools.Plugin, nil},
		{llm.Plugin, nil}, {mock.Plugin, map[string]any{"ids": []string{"claude-code", "ollama"}}},
		{router.Plugin, map[string]any{"routes": []map[string]string{{"task": "summarize", "model": "ollama/default"}}}},
		{agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"claude-code/default"}}},
		{agentloop.Plugin, nil},
	} {
		h.Load(x.s, kernel.LoadOptions{Config: x.cfg})
	}
	r := h.Root()
	a, _ := kernel.Use[*agent.Registry](r, agent.ServiceKey).Create(agent.Spec{Name: "Iris"})
	ss, _ := a.Session()
	model := func() string {
		e, _ := ss.Last(session.TypeAssistantMessage)
		var m session.AssistantMessage
		e.Decode(&m)
		return m.Model.Provider
	}
	a.Enqueue(agent.Input{Text: "summarize this page"})
	agentloop.WaitIdle(a, 2*time.Second)
	if model() != "ollama" {
		t.Fatalf("summaries should go local, got %s", model())
	}
	a.Enqueue(agent.Input{Text: "hello"})
	agentloop.WaitIdle(a, 2*time.Second)
	if model() != "claude-code" {
		t.Fatalf("default order, got %s", model())
	}
	ol, _ := kernel.Use[*llm.Registry](r, llm.ServiceKey).Get("ollama")
	ol.(*mock.Adapter).SetExhausted(true)
	a.Enqueue(agent.Input{Text: "summarize again"}) // ollama fails → falls back to the agent's order
	agentloop.WaitIdle(a, 2*time.Second)
	if model() != "claude-code" {
		t.Fatalf("fallback, got %s", model())
	}
}
