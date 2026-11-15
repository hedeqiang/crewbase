package teams_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	capsub "github.com/hedeqiang/crewbase/capabilities/subagent"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/api"
	"github.com/hedeqiang/crewbase/features/channel"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/features/subagent"
	"github.com/hedeqiang/crewbase/features/teams"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

const tok = "desk-token-xxxxxxxxxxxxxxxxxxxxxxxxxxxx"

type env struct {
	base   string
	h      *kernel.Host
	agents *agent.Registry
	ch     *channel.Service
	root   string
}

func start(t *testing.T, root string) *env {
	t.Helper()
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	var addr string
	gateway.Listening.Listen(h.Root(), func(a string) { addr = a })
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, map[string]any{"dir": root + "/sessions"}},
		{prompt.Plugin, nil}, {tools.Plugin, nil}, {approval.Plugin, nil}, {approval.DesktopPlugin, nil},
		{llm.Plugin, nil}, {mock.Plugin, nil}, {router.Plugin, nil},
		{agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"mock/default"}}},
		{agentloop.Plugin, nil}, {channel.Plugin, nil},
		{capsub.Plugin, nil}, {subagent.RunnerPlugin, nil}, {subagent.LocalPlugin, nil}, {subagent.DelegatePlugin, nil},
		{gateway.Plugin, map[string]any{"addr": "127.0.0.1:0", "token": tok}},
		{api.Plugin, nil}, {teams.Plugin, map[string]any{"dir": root}},
	} {
		if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
			t.Fatal(err)
		}
	}
	r := h.Root()
	return &env{base: "http://" + addr, h: h, root: root, agents: kernel.Use[*agent.Registry](r, agent.ServiceKey), ch: kernel.Use[*channel.Service](r, channel.ServiceKey)}
}

func (e *env) do(t *testing.T, method, path string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, e.base+path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestTeams(t *testing.T) {
	root := t.TempDir()
	e := start(t, root)

	// the default team exists and holds what was there before
	old, _ := e.agents.Create(agent.Spec{Name: "Old"})
	code, out := e.do(t, "GET", "/api/teams", nil)
	list := out["teams"].([]any)
	if code != 200 || len(list) != 1 || list[0].(map[string]any)["id"] != agent.DefaultTeam || list[0].(map[string]any)["agents"].(float64) != 1 {
		t.Fatalf("%d %v", code, out)
	}

	// create teams; names are unique, visibility checked
	code, shop := e.do(t, "POST", "/api/teams", map[string]any{"name": "电商店铺", "visibility": "private"})
	if code != 200 || shop["visibility"] != "private" || !strings.Contains(string(mustJSON(shop["people"])), `"owner"`) {
		t.Fatalf("%d %v", code, shop)
	}
	if code, _ := e.do(t, "POST", "/api/teams", map[string]any{"name": "电商店铺"}); code != 400 {
		t.Fatal("duplicate name accepted")
	}
	if code, _ := e.do(t, "POST", "/api/teams", map[string]any{"name": "x", "visibility": "secret"}); code != 400 {
		t.Fatal("bad visibility accepted")
	}
	shopID := shop["id"].(string)

	// agents are created in a team; unknown teams are refused
	if code, _ := e.do(t, "POST", "/api/agents", map[string]any{"name": "Nope", "team": "team-missing"}); code != 400 {
		t.Fatal("unknown team accepted")
	}
	_, a1 := e.do(t, "POST", "/api/agents", map[string]any{"name": "Ops", "team": shopID})
	_, a2 := e.do(t, "POST", "/api/agents", map[string]any{"name": "Writer", "team": shopID})
	ops, _ := e.agents.Get(a1["id"].(string))
	writer, _ := e.agents.Get(a2["id"].(string))

	// a channel stays inside one team
	if _, err := e.ch.Create("mixed", []string{ops.ID(), old.ID()}, "", "desktop"); err == nil || !strings.Contains(err.Error(), "another team") {
		t.Fatalf("mixed channel: %v", err)
	}
	v, err := e.ch.Create("launch", []string{ops.ID(), writer.ID()}, "", "desktop")
	if err != nil || v.Meta.Team != shopID {
		t.Fatalf("channel: %v %+v", err, v.Meta)
	}
	if err := e.ch.AddMember(v.Meta.ID, old.ID()); err == nil {
		t.Fatal("an agent of another team joined the channel")
	}

	// the roster and delegation stay inside the team
	if r := subagent.Team(e.agents, ops.ID()); !strings.Contains(r, "Writer") || strings.Contains(r, "Old") {
		t.Fatalf("roster: %q", r)
	}
	reg := kernel.Use[*tools.Registry](e.h.Root(), tools.ServiceKey)
	ss, _ := ops.Session()
	args, _ := json.Marshal(map[string]any{"task": "x", "agent": old.ID()})
	exe := reg.Run(t.Context(), e.h.Root(), tools.Call{ID: "d", Name: "delegate", Agent: ops.ID(), Session: ss, Args: args})
	if !exe.Result.IsError || !strings.Contains(exe.Result.Content, "another team") {
		t.Fatalf("cross-team delegation: %+v", exe.Result)
	}

	// moving: not while in a channel of the current team; then it moves
	if code, out := e.do(t, "POST", "/api/teams/"+agent.DefaultTeam+"/agents", map[string]any{"agent": ops.ID()}); code != 409 || !strings.Contains(out["error"].(map[string]any)["message"].(string), "launch") {
		t.Fatalf("move while in a channel: %d %v", code, out)
	}
	if code, _ := e.do(t, "POST", "/api/teams/"+shopID+"/agents", map[string]any{"agent": old.ID()}); code != 200 {
		t.Fatal("move")
	}
	if a, _ := e.agents.Get(old.ID()); a.Spec().TeamID() != shopID {
		t.Fatal("not moved")
	}

	// deleting: only empty teams, never the default one
	if code, _ := e.do(t, "DELETE", "/api/teams/"+shopID, nil); code != 409 {
		t.Fatal("non-empty team deleted")
	}
	if code, _ := e.do(t, "DELETE", "/api/teams/"+agent.DefaultTeam, nil); code != 409 {
		t.Fatal("default team deleted")
	}
	_, empty := e.do(t, "POST", "/api/teams", map[string]any{"name": "Empty"})
	if code, _ := e.do(t, "DELETE", "/api/teams/"+empty["id"].(string), nil); code != 200 {
		t.Fatal("empty team not deleted")
	}

	// teams persist
	if code, out := e.do(t, "PATCH", "/api/teams/"+shopID, map[string]any{"name": "Shop", "visibility": "public"}); code != 200 || out["name"] != "Shop" {
		t.Fatalf("rename: %d %v", code, out)
	}
	e.h.Close()
	e2 := start(t, root)
	_, out = e2.do(t, "GET", "/api/teams", nil)
	if got := string(mustJSON(out["teams"])); !strings.Contains(got, `"name":"Shop"`) || !strings.Contains(got, `"visibility":"public"`) || strings.Contains(got, "Empty") {
		t.Fatalf("after restart: %s", got)
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
