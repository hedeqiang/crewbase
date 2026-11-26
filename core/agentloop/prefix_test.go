package agentloop_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

// prefixKeeper is a mock that wants append-only history.
type prefixKeeper struct{ *mock.Adapter }

func (prefixKeeper) KeepsPrefix(string) bool { return true }

// A provider that keeps the prefix gets the system prompt's parts and a
// store kept with the session; others get neither.
func TestPrefixKeeperGetsStoreAndParts(t *testing.T) {
	e := newEnv(t, t.TempDir())
	m := mock.New("keeper", 200000)
	if err := e.llms.Register(e.h.Root(), prefixKeeper{m}); err != nil {
		t.Fatal(err)
	}
	a, _ := e.agents.Create(agent.Spec{Name: "Keeper", Models: []llm.ModelRef{{Provider: "keeper", Model: "default"}}})
	ss, _ := a.Session()
	send(t, a, "hello")
	agentloop.WaitIdle(a, 5*time.Second)
	reqs := m.Requests()
	if len(reqs) == 0 {
		t.Fatal("no request")
	}
	req := reqs[0]
	if req.Store == nil || len(req.SystemParts) == 0 {
		t.Fatalf("store %v parts %v", req.Store, req.SystemParts)
	}
	var joined string
	for i, p := range req.SystemParts {
		if i > 0 {
			joined += "\n\n"
		}
		joined += p.Text
	}
	if joined != req.System || req.SystemParts[0].Name != "base" {
		t.Fatalf("parts %+v do not make the system prompt", req.SystemParts)
	}
	if err := req.Store.Save("k", []byte(`{"x":1}`)); err != nil {
		t.Fatal(err)
	}
	if b, ok := req.Store.Load("k"); !ok || string(b) != `{"x":1}` {
		t.Fatalf("load %s %v", b, ok)
	}
	if _, err := os.Stat(filepath.Join(ss.Dir(), "llm-state.json")); err != nil {
		t.Fatalf("not kept with the session: %v", err)
	}

	// the default mock: nothing extra
	b, _ := e.agents.Create(agent.Spec{Name: "Plain", Models: []llm.ModelRef{{Provider: "codex", Model: "default"}}})
	send(t, b, "hello")
	agentloop.WaitIdle(b, 5*time.Second)
	cm, _ := e.llms.Get("codex")
	rs := cm.(*mock.Adapter).Requests()
	if r := rs[len(rs)-1]; r.Store != nil || r.SystemParts != nil {
		t.Fatal("a provider that does not keep the prefix got the store")
	}
}
