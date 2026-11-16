//go:build !windows

package acp

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/features/mcpserver"
	"github.com/hedeqiang/crewbase/features/rules"
	"github.com/hedeqiang/crewbase/features/runmode"
	"github.com/hedeqiang/crewbase/kernel"
)

// The fake CLI calls a Crewbase tool through the real MCP server it was
// given in session/new; the call runs in Crewbase's pipeline as the agent.
func TestMCPEndToEnd(t *testing.T) {
	dir := t.TempDir()
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, nil}, {prompt.Plugin, nil}, {tools.Plugin, map[string]any{"requirePolicy": "rules"}},
		{approval.Plugin, nil}, {approval.DesktopPlugin, nil}, {rules.Plugin, nil}, {runmode.Plugin, nil},
		{agent.Plugin, map[string]any{"root": filepath.Join(dir, "root")}},
		{gateway.Plugin, map[string]any{"addr": "127.0.0.1:0", "token": "desk-token-for-tests"}},
		{mcpserver.Plugin, nil},
	} {
		if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
			t.Fatal(err)
		}
	}
	r := h.Root()
	reg := kernel.Use[*tools.Registry](r, tools.ServiceKey)
	var mu sync.Mutex
	var got []tools.Call
	reg.Register(r, &tools.Tool{Name: "browser_echo", Category: tools.CategoryRead, Execute: func(_ context.Context, c tools.Call) (tools.Result, error) {
		mu.Lock()
		got = append(got, c)
		mu.Unlock()
		return tools.Text("echoed"), nil
	}})
	reg.Register(r, &tools.Tool{Name: "bash", Category: tools.CategoryExec, Execute: func(context.Context, tools.Call) (tools.Result, error) {
		return tools.Text("ran"), nil
	}})
	a, err := kernel.Use[*agent.Registry](r, agent.ServiceKey).Create(agent.Spec{Name: "Ada"})
	if err != nil {
		t.Fatal(err)
	}
	ss, err := a.Session()
	if err != nil {
		t.Fatal(err)
	}
	svc := kernel.Use[*mcpserver.Service](r, mcpserver.ServiceKey)
	ad := newFake(t, Options{MCP: mcpGranter(r)}, httpMCPEnv)
	req := llm.Request{Messages: []llm.Message{user("/mcpcall browser_echo")}, Agent: a.ID(), Session: ss.ID(), Mode: agent.ModeSandbox}
	resp, _, err := stream(t, ad, req)
	if err != nil {
		t.Fatal(err)
	}
	if txt := resp.Message.Text(); !strings.Contains(txt, "mcpcall: ok echoed") {
		t.Fatalf("reply %q", txt)
	}
	mu.Lock()
	calls := append([]tools.Call(nil), got...)
	mu.Unlock()
	if len(calls) != 1 || calls[0].Agent != a.ID() || calls[0].Session.ID() != ss.ID() || calls[0].Mode != agent.ModeSandbox || calls[0].Workspace != a.Workspace() {
		t.Fatalf("calls %+v", calls)
	}
	recorded := false
	for _, ev := range ss.Events() {
		recorded = recorded || (ev.Type == session.TypeToolCall && strings.Contains(string(ev.Data), "browser_echo"))
	}
	if !recorded {
		t.Fatal("call not in the session")
	}
	req.Messages = append(req.Messages, resp.Message, user("/mcpcall bash"))
	resp, _, err = stream(t, ad, req)
	if err != nil || !strings.Contains(resp.Message.Text(), "rpc error Unknown tool: bash") {
		t.Fatalf("%v %q", err, resp.Message.Text())
	}
	if svc.Grants() != 1 {
		t.Fatalf("grants %d", svc.Grants())
	}
	ad.Close()
	if svc.Grants() != 0 {
		t.Fatal("grant kept after the adapter closed")
	}
}
