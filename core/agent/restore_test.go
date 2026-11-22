package agent

import (
	"testing"

	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

func restartHost(t *testing.T, root string) (*kernel.Host, *Registry) {
	h := kernel.NewHost(nil)
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, map[string]any{"dir": root + "/sessions"}},
		{prompt.Plugin, nil}, {tools.Plugin, nil}, {llm.Plugin, nil},
		{mock.Plugin, map[string]any{"ids": []string{"magpie"}}},
		{Plugin, map[string]any{"root": root, "defaultModels": []string{"magpie/default"}}},
	} {
		if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
			t.Fatal(err)
		}
	}
	return h, kernel.Use[*Registry](h.Root(), ServiceKey)
}

func TestPausedStateSurvivesRestartAcrossConversations(t *testing.T) {
	root := t.TempDir()
	h, r := restartHost(t, root)
	a, err := r.Create(Spec{Name: "Kai", Role: "x"})
	if err != nil {
		t.Fatal(err)
	}
	id := a.ID()
	a.Pause()
	ss2, err := r.Sessions().Create(session.Meta{Kind: "agent", Agents: []string{id}})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetSession(ss2.ID()); err != nil {
		t.Fatal(err)
	}
	a.Resume()
	h.Close()
	paused := 0
	for i := 0; i < 20; i++ {
		h, r := restartHost(t, root)
		b, _ := r.Get(id)
		if b.Paused() {
			paused++
		}
		h.Close()
	}
	if paused > 0 {
		t.Fatalf("an agent resumed before the restart came back paused in %d/20 restarts", paused)
	}
}

func TestRemovedResumedInputStaysRemoved(t *testing.T) {
	root := t.TempDir()
	h, r := restartHost(t, root)
	a, _ := r.Create(Spec{Name: "Kai", Role: "x"})
	id := a.ID()
	a.Pause() // so nothing claims
	in, _ := a.Enqueue(Input{Text: "do it"})
	ss, _ := a.Session()
	a.mu.Lock()
	a.queue = nil
	a.mu.Unlock()
	ss.Append(session.TypeInputClaimed, id, map[string]string{"id": in.ID})
	ss.Append(session.TypeTurnStart, id, map[string]any{"input": in.ID})
	ss.Append(session.TypeTurnEnd, id, map[string]any{"status": "interrupted"})
	h.Close()
	h, r = restartHost(t, root)
	b, _ := r.Get(id)
	q := b.Queue()
	if len(q) != 1 || !q[0].Resumed {
		t.Fatalf("expected resumed input, got %+v", q)
	}
	if !b.Cancel(q[0].ID) {
		t.Fatal("cancel failed")
	}
	h.Close()
	h, r = restartHost(t, root)
	defer h.Close()
	c, _ := r.Get(id)
	if q := c.Queue(); len(q) > 0 {
		t.Fatalf("a resumed input the person removed came back after a restart: %+v", q[0].Text)
	}
}
