package agentdocs_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"context"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/agentdocs"
	"github.com/hedeqiang/crewbase/features/mcp"
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

// fakeDocs is the part of the Open Agent Docs API the connector uses.
type fakeDocs struct {
	mu      sync.Mutex
	agents  map[string]string // token → agent id
	names   map[string]bool
	revoked map[string]bool
	notes   map[string][]map[string]any // agent id → pending notifications
	acked   []int64
	n       int
}

func newFake() *fakeDocs {
	return &fakeDocs{agents: map[string]string{"oad_admin": "admin"}, names: map[string]bool{}, revoked: map[string]bool{}, notes: map[string][]map[string]any{}}
}

func (f *fakeDocs) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	me, ok := f.agents[tok]
	if !ok || f.revoked[me] {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":{"code":"unauthorized","message":"bad token"}}`)
		return
	}
	switch {
	case r.Method == "GET" && r.URL.Path == "/api/v1/me":
		fmt.Fprintf(w, `{"user":{"id":%q,"name":"x","kind":"agent"},"workspaces":[{"id":"ws1","name":"团队文档","role":"owner"}]}`, me)
	case r.Method == "GET" && r.URL.Path == "/api/v1/workspaces/ws1/agents":
		if me != "admin" {
			w.WriteHeader(403)
			return
		}
		fmt.Fprint(w, `{"agents":[]}`)
	case r.Method == "POST" && r.URL.Path == "/api/v1/workspaces/ws1/agents":
		var b struct{ Name, Scope string }
		_ = json.NewDecoder(r.Body).Decode(&b)
		if f.names[b.Name] {
			w.WriteHeader(409)
			fmt.Fprint(w, `{"error":{"code":"conflict","message":"name taken"}}`)
			return
		}
		f.names[b.Name] = true
		f.n++
		id := fmt.Sprintf("ag%d", f.n)
		t := "oad_" + id
		f.agents[t] = id
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"agent":{"id":%q,"workspace_id":"ws1","name":%q,"about":"","scope":%q,"created_at":1},"token":%q,"webhook_secret":"whsec_x"}`, id, b.Name, b.Scope, t)
	case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/api/v1/agents/"):
		f.revoked[strings.TrimPrefix(r.URL.Path, "/api/v1/agents/")] = true
		fmt.Fprint(w, `{}`)
	case r.Method == "GET" && r.URL.Path == "/api/v1/agent/notifications":
		var out []map[string]any
		for _, n := range f.notes[me] {
			if int64(n["id"].(int)) > atoi(r.URL.Query().Get("after")) {
				out = append(out, n)
			}
		}
		if out == nil {
			out = []map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"notifications": out})
	case r.Method == "POST" && r.URL.Path == "/api/v1/agent/notifications/ack":
		var b struct{ IDs []int64 }
		_ = json.NewDecoder(r.Body).Decode(&b)
		f.acked = append(f.acked, b.IDs...)
		var keep []map[string]any
		for _, n := range f.notes[me] {
			drop := false
			for _, id := range b.IDs {
				if int64(n["id"].(int)) == id {
					drop = true
				}
			}
			if !drop {
				keep = append(keep, n)
			}
		}
		f.notes[me] = keep
		fmt.Fprintf(w, `{"acked":%d}`, len(b.IDs))
	case r.URL.Path == "/mcp":
		w.WriteHeader(404) // the MCP connection is not exercised here
	default:
		w.WriteHeader(404)
	}
}

func atoi(s string) int64 {
	var n int64
	fmt.Sscan(s, &n)
	return n
}

func TestConnectIdentitiesAndComments(t *testing.T) {
	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()

	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	root := t.TempDir()
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, nil}, {prompt.Plugin, nil}, {tools.Plugin, nil}, {approval.Plugin, nil}, {approval.DesktopPlugin, nil},
		{credentials.Plugin, map[string]any{"root": root}},
		{llm.Plugin, nil}, {mock.Plugin, nil}, {router.Plugin, nil},
		{agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"mock/default"}}},
		{agentloop.Plugin, nil}, {mcp.Plugin, map[string]any{"dir": root}},
		{agentdocs.Plugin, map[string]any{"dir": root}},
	} {
		if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
			t.Fatal(err)
		}
	}
	r := h.Root()
	agents := kernel.Use[*agent.Registry](r, agent.ServiceKey)
	svc := kernel.Use[*agentdocs.Service](r, agentdocs.ServiceKey)
	mcps := kernel.Use[*mcp.Service](r, mcp.ServiceKey)
	ad, _ := kernel.Use[*llm.Registry](r, llm.ServiceKey).Get("mock")
	m := ad.(*mock.Adapter)
	var asked []string
	var mu sync.Mutex
	m.Script = func(req llm.Request) (llm.Response, error) {
		mu.Lock()
		asked = append(asked, req.Messages[len(req.Messages)-1].Text())
		mu.Unlock()
		return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "done")}, nil
	}
	kai, _ := agents.Create(agent.Spec{Name: "Kai Chen"})
	mia, _ := agents.Create(agent.Spec{Name: "Mia"})
	fake.mu.Lock()
	fake.names["Mia"] = true // taken by someone else on the server
	fake.mu.Unlock()
	ctx := context.Background()

	if _, err := svc.Connect(ctx, agentdocs.ConnectRequest{URL: srv.URL, Token: "wrong"}); err == nil || !strings.Contains(err.Error(), "does not accept") {
		t.Fatalf("bad token: %v", err)
	}
	st, err := svc.Connect(ctx, agentdocs.ConnectRequest{URL: srv.URL + "/mcp", Token: "oad_admin"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Settings.URL != srv.URL || st.Settings.WorkspaceName != "团队文档" || len(st.Identities) != 2 || st.Error != "" {
		t.Fatalf("%+v", st)
	}
	names := map[string]string{}
	for _, id := range st.Identities {
		names[id.Agent] = id.Name
	}
	if names[kai.ID()] != "Kai-Chen" || names[mia.ID()] != "Mia-2" {
		t.Fatalf("names %v", names)
	}
	// each agent its own connection, visible only to it, token in the vault
	v, ok := mcps.Get(agentdocs.ServerName(kai.ID()))
	if !ok || v.Scope != kai.ID() || v.URL != srv.URL+"/mcp" {
		t.Fatalf("mcp %+v %v", v, ok)
	}
	if strings.Contains(fmt.Sprint(v.Headers), "oad_ag") {
		t.Fatalf("token in config: %v", v.Headers)
	}

	// a comment that calls Kai becomes its task, and is acknowledged
	fake.mu.Lock()
	kaiDocs := ""
	for _, id := range st.Identities {
		if id.Agent == kai.ID() {
			kaiDocs = id.ID
		}
	}
	fake.notes[kaiDocs] = append(fake.notes[kaiDocs], map[string]any{
		"id": 7, "type": "comment", "page": map[string]any{"id": "p1", "title": "Q4 计划", "url": "http://x/p/p1"},
		"thread_id": "t1", "comment": map[string]any{"id": "c1", "author": map[string]any{"kind": "user", "name": "Ann"}, "body": "@Kai-Chen 改成三点"},
		"quote": "我们要做很多事", "prompt": "Ann asked you to shorten this.",
	})
	fake.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		fake.mu.Lock()
		n := len(fake.acked)
		fake.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	agentloop.WaitIdle(kai, 3*time.Second)
	mu.Lock()
	got := strings.Join(asked, "\n")
	mu.Unlock()
	if !strings.Contains(got, "Ann called you") || !strings.Contains(got, "Q4 计划") || !strings.Contains(got, "改成三点") ||
		!strings.Contains(got, agentdocs.ServerName(kai.ID())+"__comment") || !strings.Contains(got, "thread t1") {
		t.Fatalf("task: %q", got)
	}
	if svc.Status().Woken != 1 {
		t.Fatalf("woken %d", svc.Status().Woken)
	}

	// a deleted agent loses its identity (revoked on the server)
	if err := agents.Delete(mia.ID()); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(svc.Status().Identities) != 1 {
		time.Sleep(20 * time.Millisecond)
	}
	if len(svc.Status().Identities) != 1 {
		t.Fatalf("after delete %+v", svc.Status().Identities)
	}
	if _, ok := mcps.Get(agentdocs.ServerName(mia.ID())); ok {
		t.Fatal("connection left")
	}

	// disconnect: everything revoked and removed
	if err := svc.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	revoked := fake.revoked[kaiDocs]
	fake.mu.Unlock()
	if st := svc.Status(); st.Connected || len(st.Identities) != 0 || !revoked {
		t.Fatalf("disconnect %+v revoked=%v", st, revoked)
	}
	if _, ok := mcps.Get(agentdocs.ServerName(kai.ID())); ok {
		t.Fatal("connection left after disconnect")
	}
}
