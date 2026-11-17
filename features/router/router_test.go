package router_test

import (
	"context"
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

// Media tasks only route to models of their kind, chat tasks never to
// image / video models, and an agent's order skips media ids.
func TestRoutesFollowModelKind(t *testing.T) {
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	h.Load(session.Plugin, kernel.LoadOptions{})
	h.Load(prompt.Plugin, kernel.LoadOptions{})
	h.Load(tools.Plugin, kernel.LoadOptions{})
	h.Load(llm.Plugin, kernel.LoadOptions{})
	h.Load(mock.Plugin, kernel.LoadOptions{Config: map[string]any{"ids": []string{"gw"}}})
	llms := kernel.Use[*llm.Registry](h.Root(), llm.ServiceKey)
	gw, _ := llms.Get("gw")
	gw.(*mock.Adapter).AddModel(llm.ModelInfo{ID: "painter", Kind: llm.KindImage, Enabled: true})
	gw.(*mock.Adapter).AddModel(llm.ModelInfo{ID: "gpt-image-2", Enabled: true}) // kind from the id
	rt := router.New(h.Root(), llms, nil)
	rt.SetRoutes([]router.Route{
		{Task: router.TaskSummarize, Model: "gw/painter"}, // wrong kind: skipped
		{Task: router.TaskSummarize, Model: "gw/default"},
		{Task: router.TaskImage, Model: "gw/default"}, // a chat model cannot draw
		{Task: router.TaskImage, Model: "gw/painter"},
		{Task: router.TaskVideo, Model: "gw/gpt-image-2"},
	})
	if ref, ok := rt.RouteFor(router.TaskSummarize); !ok || ref.Model != "default" {
		t.Fatalf("summarize → %v %v", ref, ok)
	}
	if ref, ok := rt.RouteFor(router.TaskImage); !ok || ref.Model != "painter" {
		t.Fatalf("image → %v %v", ref, ok)
	}
	if ref, ok := rt.RouteFor(router.TaskVideo); ok {
		t.Fatalf("an image model must not serve video: %v", ref)
	}
	h.Load(agent.Plugin, kernel.LoadOptions{Config: map[string]any{"root": t.TempDir()}})
	a, _ := kernel.Use[*agent.Registry](h.Root(), agent.ServiceKey).Create(agent.Spec{Name: "Ana",
		Models: []llm.ModelRef{{Provider: "gw", Model: "gpt-image-2"}, {Provider: "gw", Model: "default"}}})
	if ref, _ := rt.Select(a); ref.Model != "default" {
		t.Fatalf("chat order picked %v", ref)
	}
}

func TestClassifierIsConservative(t *testing.T) {
	for _, txt := range []string{"Implement the summary view", "在摘要页加一个按钮", "the log cleanup job failed"} {
		if got := router.Classify(agent.Input{Text: txt}); got == router.TaskSummarize {
			t.Errorf("%q must not route as a summary", txt)
		}
	}
	for _, txt := range []string{"Summarize this page", "请帮我总结这篇文章", "总结一下会议记录"} {
		if got := router.Classify(agent.Input{Text: txt}); got != router.TaskSummarize {
			t.Errorf("%q: got %q", txt, got)
		}
	}
}

func TestLocalOnlyAgentNeverRoutedToCloud(t *testing.T) {
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, nil}, {prompt.Plugin, nil}, {tools.Plugin, nil},
		{llm.Plugin, nil}, {mock.Plugin, map[string]any{"ids": []string{"ollama", "cloud"}}},
		{router.Plugin, map[string]any{"routes": []map[string]string{{"task": "summarize", "model": "cloud/default"}}}},
		{agent.Plugin, map[string]any{"root": t.TempDir()}},
		{agentloop.Plugin, nil},
	} {
		h.Load(x.s, kernel.LoadOptions{Config: x.cfg})
	}
	llms := kernel.Use[*llm.Registry](h.Root(), llm.ServiceKey)
	ol, _ := llms.Get("ollama")
	ol.(*mock.Adapter).SetKind("local")
	a, _ := kernel.Use[*agent.Registry](h.Root(), agent.ServiceKey).Create(agent.Spec{Name: "Private", Models: []llm.ModelRef{{Provider: "ollama", Model: "default"}}})
	a.Enqueue(agent.Input{Text: "Summarize this confidential page"})
	agentloop.WaitIdle(a, 2*time.Second)
	cl, _ := llms.Get("cloud")
	if n := len(cl.(*mock.Adapter).Requests()); n != 0 {
		t.Fatalf("local-only agent's content was sent to a cloud model (%d requests)", n)
	}
}

type offSwitch map[string]bool

func (s offSwitch) Off(provider, model string) bool { return s[provider+"/"+model] }

// A model switched off in Settings › Models is never chosen automatically:
// the agent's order skips it.
func TestSwitchedOffModelSkipped(t *testing.T) {
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	root := t.TempDir()
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, nil}, {prompt.Plugin, nil}, {tools.Plugin, nil},
		{llm.Plugin, nil}, {mock.Plugin, map[string]any{"ids": []string{"claude-code", "ollama"}}},
		{router.Plugin, nil},
		{agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"claude-code/default", "ollama/default"}}},
		{agentloop.Plugin, nil},
	} {
		h.Load(x.s, kernel.LoadOptions{Config: x.cfg})
	}
	r := h.Root()
	kernel.Use[*llm.Registry](r, llm.ServiceKey).SetSwitches(r, offSwitch{"claude-code/default": true})
	a, _ := kernel.Use[*agent.Registry](r, agent.ServiceKey).Create(agent.Spec{Name: "Iris"})
	ss, _ := a.Session()
	a.Enqueue(agent.Input{Text: "hello"})
	agentloop.WaitIdle(a, 2*time.Second)
	e, _ := ss.Last(session.TypeAssistantMessage)
	var m session.AssistantMessage
	e.Decode(&m)
	if m.Model.Provider != "ollama" {
		t.Fatalf("switched-off model chosen: %s", m.Model.Provider)
	}
}

// A model the account cannot use (Codex on a ChatGPT plan) fails over and
// stays aside for hours, unlike a passing outage.
func TestModelUnavailableOnAccountKeptAside(t *testing.T) {
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	root := t.TempDir()
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, nil}, {prompt.Plugin, nil}, {tools.Plugin, nil},
		{llm.Plugin, nil}, {mock.Plugin, map[string]any{"ids": []string{"claude-code", "ollama"}}},
		{agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"claude-code/default", "ollama/default"}}},
	} {
		h.Load(x.s, kernel.LoadOptions{Config: x.cfg})
	}
	r := h.Root()
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	rt := router.New(r, kernel.Use[*llm.Registry](r, llm.ServiceKey), func() time.Time { return now })
	router.QuotaExhausted.Listen(r, func(_ context.Context, e router.Exhausted) (llm.ModelRef, bool) {
		return llm.ModelRef{Provider: "ollama", Model: "default"}, true
	})
	a, _ := kernel.Use[*agent.Registry](r, agent.ServiceKey).Create(agent.Spec{Name: "Iris"})
	failed := llm.ModelRef{Provider: "claude-code", Model: "default"}
	cause := &llm.Error{Kind: llm.ErrModelUnavailable, Provider: "claude-code",
		Message: "The 'gpt-6.1-sol' model is not supported when using Codex with a ChatGPT account."}
	if !llm.ShouldFailover(cause) {
		t.Fatal("model_unavailable should fail over")
	}
	if next, ok := rt.Failover(context.Background(), a, failed, cause); !ok || next.Provider != "ollama" {
		t.Fatalf("failover %v %v", next, ok)
	}
	st := rt.States()
	if len(st) != 1 || st[0].Reason != "model_unavailable" || st[0].Until.Sub(now) < 6*time.Hour {
		t.Fatalf("states %+v", st)
	}
	if sel, _ := rt.Select(a); sel.Provider != "ollama" {
		t.Fatalf("unavailable model selected again: %v", sel)
	}
	now = now.Add(3 * time.Hour) // a 10-minute outage backoff would have expired
	if sel, _ := rt.Select(a); sel.Provider != "ollama" {
		t.Fatalf("unavailable model back too soon: %v", sel)
	}
}
