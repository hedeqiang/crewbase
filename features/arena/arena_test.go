package arena

import (
	"context"
	"strings"
	"testing"
	"time"

	capsub "github.com/hedeqiang/crewbase/capabilities/subagent"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/features/subagent"
	"github.com/hedeqiang/crewbase/features/usage"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

func TestCategorize(t *testing.T) {
	for in, want := range map[string]string{
		"帮我写一个 Go 函数，反转链表":             CatCode,
		"这段 SQL 为什么这么慢":                CatCode,
		"把下面这段话翻译成英文：今天天气很好":           CatTranslate,
		"Translate this into Japanese": CatTranslate,
		"写一篇关于秋天的散文":                   CatWriting,
		"帮我润色这封邮件":                     CatWriting,
		"对比一下 React 和 Vue 的优缺点":        CatCode, // a programming word wins
		"分析一下今年新能源汽车的销量趋势":             CatAnalysis,
		"光速是多少？":                       CatQA,
		"why is the sky blue":          CatQA,
		"你好":                           CatOther,
		"":                             CatOther,
	} {
		if got := Categorize(in); got != want {
			t.Errorf("Categorize(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestStoreStatsAndSuggest(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	add := func(id, task, winner string, costs ...float64) {
		r := Record{ID: id, Task: task, Category: Categorize(task), Models: []string{"p/a", "p/b"}}
		for i, m := range r.Models {
			r.Runs = append(r.Runs, RunStat{Model: m, Status: "completed", Ms: 1000, CostUSD: costs[i], Priced: true})
		}
		if err := st.Add(r); err != nil {
			t.Fatal(err)
		}
		if winner != "" {
			_ = st.SetWinner(id, winner)
		}
	}
	add("1", "写一个 Python 脚本", "p/b", 0.01, 0.03)
	add("2", "修复这个 bug", "p/b", 0.01, 0.05)
	add("3", "写个函数", "p/a", 0.01, 0.01)
	add("4", "光速是多少？", "", 0.01, 0.01)

	again, err := OpenStore(dir) // reloaded from disk
	if err != nil || len(again.Records()) != 4 {
		t.Fatalf("reload: %v %d", err, len(again.Records()))
	}
	s := again.Stats()
	if s.Arenas != 4 || s.Picked != 3 || s.Categories[0].Category != CatCode {
		t.Fatalf("stats %+v", s)
	}
	code := s.Categories[0]
	if code.Models[0].Model != "p/b" || code.Models[0].Wins != 2 || code.Models[0].AvgCostUSD != 0.03 {
		t.Fatalf("code %+v", code.Models)
	}
	sg := again.Suggest("用 Go 写一个 HTTP 服务器的代码")
	if sg.Category != CatCode || sg.Model != "p/b" || sg.Wins != 2 || sg.Picked != 3 || len(sg.Ranked) != 2 {
		t.Fatalf("suggest %+v", sg)
	}
	if sg := again.Suggest("光速是多少？"); sg.Model != "" || sg.Category != CatQA {
		t.Fatalf("no winner yet: %+v", sg)
	}
}

func TestArenaRunAndPick(t *testing.T) {
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	root := t.TempDir()
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, nil}, {prompt.Plugin, nil}, {tools.Plugin, nil},
		{llm.Plugin, nil}, {mock.Plugin, map[string]any{"ids": []string{"ma", "mb", "mc"}}}, {router.Plugin, nil},
		{agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"ma/default"}}},
		{agentloop.Plugin, nil},
		{capsub.Plugin, map[string]any{"maxConcurrent": 1}}, {subagent.RunnerPlugin, nil}, {subagent.LocalPlugin, nil},
		{usage.Plugin, nil},
	} {
		if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
			t.Fatal(err)
		}
	}
	r := h.Root()
	store, _ := OpenStore(root)
	s := &Service{c: r, store: store, running: map[string]context.CancelFunc{},
		agents: kernel.Use[*agent.Registry](r, agent.ServiceKey), sessions: kernel.Use[*session.Store](r, session.ServiceKey),
		subs: kernel.Use[*capsub.Service](r, capsub.ServiceKey), llms: kernel.Use[*llm.Registry](r, llm.ServiceKey)}
	kai, err := s.agents.Create(agent.Spec{Name: "Kai"})
	if err != nil {
		t.Fatal(err)
	}
	ss, _ := kai.Session()

	if _, err := s.Start(ss.ID(), StartRequest{Text: "hi", Models: []string{"ma/default"}}); err == nil {
		t.Fatal("one model accepted")
	}
	if _, err := s.Start(ss.ID(), StartRequest{Text: "hi", Models: []string{"ma/default", "nope/x"}}); err == nil {
		t.Fatal("unknown provider accepted")
	}
	// three models although a parent may run one sub-agent at a time:
	// the arena is not a delegation
	d, err := s.Start(ss.ID(), StartRequest{Text: "写一个排序函数", Models: []string{"ma/default", "mb/default", "mc/default"}})
	if err != nil {
		t.Fatal(err)
	}
	if d.Category != CatCode || d.Agent != kai.ID() {
		t.Fatalf("started %+v", d)
	}
	results := waitResults(t, ss, d.ID, 3)
	for i, res := range results {
		want := "default: 写一个排序函数"
		if res.Status != "completed" || !strings.Contains(res.Answer, want) || res.Calls != 1 || res.Input == 0 || res.Output == 0 {
			t.Fatalf("result %d: %+v", i, res)
		}
	}
	runs := 0
	for _, e := range ss.Events() {
		if e.Type == TypeRun {
			runs++
		}
	}
	if runs != 3 {
		t.Fatalf("runs %d", runs)
	}
	if _, err := s.Pick(ss.ID(), d.ID, 5); err == nil {
		t.Fatal("picked a missing run")
	}
	p, err := s.Pick(ss.ID(), d.ID, 1)
	if err != nil || p.Model != "mb/default" || p.Agent != kai.ID() {
		t.Fatalf("pick %+v %v", p, err)
	}
	if _, err := s.Pick(ss.ID(), d.ID, 0); err == nil {
		t.Fatal("picked twice")
	}
	// the conversation goes on from the task and the picked answer
	msgs := session.DeriveMessages(ss.Events(), kai.ID(), nil)
	if len(msgs) != 2 || msgs[0].Role != llm.RoleUser || msgs[0].Text() != "写一个排序函数" || msgs[1].Role != llm.RoleAssistant || !strings.Contains(msgs[1].Text(), "default: 写一个排序函数") {
		t.Fatalf("history %+v", msgs)
	}
	if _, err := kai.Enqueue(agent.Input{Text: "thanks"}); err != nil {
		t.Fatal(err)
	}
	agentloop.WaitIdle(kai, 3*time.Second)
	if sg := s.Suggest("再写一个二分查找函数"); sg.Model != "mb/default" || sg.Wins != 1 {
		t.Fatalf("suggest %+v", sg)
	}
	// the sub-agents are gone
	for _, a := range s.agents.List() {
		if a.Spec().Ephemeral {
			t.Fatalf("left over %s", a.ID())
		}
	}
}

func waitResults(t *testing.T, ss *session.Session, id string, n int) []ResultData {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, res, _ := find(ss, id)
		if len(res) == n {
			out := make([]ResultData, n)
			for i := range n {
				out[i] = res[i]
			}
			return out
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("results did not come")
	return nil
}
