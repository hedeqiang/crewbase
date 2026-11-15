package teams_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/api"
	"github.com/hedeqiang/crewbase/features/channel"
	"github.com/hedeqiang/crewbase/features/devices"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/features/members"
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/features/teams"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

type aenv struct {
	base string
	h    *kernel.Host
	ds   *devices.Service
}

func astart(t *testing.T, root string) *aenv {
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
		{agentloop.Plugin, nil}, {channel.Plugin, nil}, {channel.APIPlugin, nil},
		{gateway.Plugin, map[string]any{"addr": "127.0.0.1:0", "token": tok}},
		{api.Plugin, nil},
		{devices.Plugin, map[string]any{"dir": root}},
		{members.Plugin, map[string]any{"dir": root}},
		{teams.Plugin, map[string]any{"dir": root}},
	} {
		if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
			t.Fatal(x.s.Name, err)
		}
	}
	return &aenv{base: "http://" + addr, h: h, ds: kernel.Use[*devices.Service](h.Root(), devices.ServiceKey)}
}

func (e *aenv) do(t *testing.T, token, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, e.base+path, rd)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (e *aenv) member(t *testing.T, name, role string) (id, token string) {
	t.Helper()
	code, m := e.do(t, tok, "POST", "/api/members", map[string]any{"name": name, "role": role})
	if code != 200 {
		t.Fatal(code, m)
	}
	id = m["id"].(string)
	code, p := e.do(t, tok, "POST", "/api/devices/pairing", map[string]any{"member": id})
	if code != 200 {
		t.Fatal(code, p)
	}
	r, err := e.ds.Redeem(p["code"].(string), name+" phone", "", "")
	if err != nil {
		t.Fatal(err)
	}
	return id, r.Token
}

func names(out map[string]any, key, field string) string {
	var xs []string
	for _, x := range out[key].([]any) {
		xs = append(xs, x.(map[string]any)[field].(string))
	}
	return strings.Join(xs, ",")
}

func TestAccess(t *testing.T) {
	root := t.TempDir()
	e := astart(t, root)
	// members that existed before teams keep seeing the default team
	_, bobTok := e.member(t, "Bob", "member")
	_, catTok := e.member(t, "Cat", "member")

	_, pub := e.do(t, tok, "POST", "/api/teams", map[string]any{"name": "Public", "visibility": "public"})
	_, priv := e.do(t, tok, "POST", "/api/teams", map[string]any{"name": "Secret", "visibility": "private"})
	pubID, privID := pub["id"].(string), priv["id"].(string)
	e.do(t, tok, "POST", "/api/agents", map[string]any{"name": "Home"})
	e.do(t, tok, "POST", "/api/agents", map[string]any{"name": "Pub", "team": pubID})
	_, sec := e.do(t, tok, "POST", "/api/agents", map[string]any{"name": "Sec", "team": privID})
	secID, secSession := sec["id"].(string), sec["session"].(string)
	_, ownerChat := e.do(t, tok, "POST", "/api/agents", map[string]any{"kind": "chat"})

	// teams: Bob sees the default and the public team, not the private one
	_, out := e.do(t, bobTok, "GET", "/api/teams", nil)
	if got := names(out, "teams", "name"); got != "我的团队,Public" {
		t.Fatalf("bob's teams: %s", got)
	}
	// agents: the private team's agent and the owner's chat are invisible
	_, out = e.do(t, bobTok, "GET", "/api/agents", nil)
	if got := names(out, "agents", "name"); got != "Home,Pub" {
		t.Fatalf("bob's agents: %s", got)
	}
	_, out = e.do(t, bobTok, "GET", "/api/sessions", nil)
	if b, _ := json.Marshal(out); strings.Contains(string(b), secSession) || strings.Contains(string(b), ownerChat["session"].(string)) {
		t.Fatal("hidden sessions listed")
	}
	// direct access to a hidden agent or its session: not found
	if code, _ := e.do(t, bobTok, "GET", "/api/agents/"+secID, nil); code != 404 {
		t.Fatalf("hidden agent: %d", code)
	}
	if code, _ := e.do(t, bobTok, "GET", "/api/sessions/"+secSession+"/events", nil); code != 404 {
		t.Fatalf("hidden session: %d", code)
	}
	if code, _ := e.do(t, bobTok, "POST", "/api/agents/"+secID+"/messages", map[string]any{"text": "hi"}); code != 404 {
		t.Fatalf("message to a hidden agent: %d", code)
	}
	// a public team Bob is not in: read-only until he joins
	if code, _ := e.do(t, bobTok, "POST", "/api/agents/pub/messages", map[string]any{"text": "hi"}); code != 403 {
		t.Fatalf("acted in a public team without joining: %d", code)
	}
	if code, v := e.do(t, bobTok, "POST", "/api/teams/"+pubID+"/join", nil); code != 200 || v["myRole"] != "member" || v["canAct"] != true {
		t.Fatalf("join: %d %v", code, v)
	}
	if code, _ := e.do(t, bobTok, "POST", "/api/agents/pub/messages", map[string]any{"text": "hi"}); code != 200 {
		t.Fatalf("member could not act after joining: %d", code)
	}
	// a private team cannot be joined, only seen by its people
	if code, _ := e.do(t, bobTok, "POST", "/api/teams/"+privID+"/join", nil); code != 404 {
		t.Fatalf("joined a private team: %d", code)
	}
	// the owner adds Cat to the private team as a viewer: she sees it, reads only
	e.do(t, tok, "PATCH", "/api/teams/"+privID, map[string]any{"people": []map[string]string{{"id": "owner", "role": "lead"}, {"id": catID(t, e), "role": "viewer"}}})
	_, out = e.do(t, catTok, "GET", "/api/agents", nil)
	if got := names(out, "agents", "name"); !strings.Contains(got, "Sec") {
		t.Fatalf("cat should see Sec: %s", got)
	}
	if code, _ := e.do(t, catTok, "POST", "/api/agents/"+secID+"/messages", map[string]any{"text": "hi"}); code != 403 {
		t.Fatalf("team viewer acted: %d", code)
	}
	if code, _ := e.do(t, catTok, "PATCH", "/api/teams/"+privID, map[string]any{"name": "Mine"}); code != 403 {
		t.Fatalf("non-lead managed a team: %d", code)
	}

	// chats are personal: Bob's chat is his alone, also hidden from the owner
	_, bobChat := e.do(t, bobTok, "POST", "/api/agents", map[string]any{"kind": "chat"})
	if bobChat["owner"] == nil || bobChat["owner"] == "" {
		t.Fatalf("chat owner not set: %v", bobChat)
	}
	_, out = e.do(t, tok, "GET", "/api/agents", nil)
	if strings.Contains(names(out, "agents", "id"), bobChat["id"].(string)) {
		t.Fatal("the owner sees Bob's chat")
	}
	if code, _ := e.do(t, catTok, "GET", "/api/agents/"+bobChat["id"].(string), nil); code != 404 {
		t.Fatal("cat reached bob's chat")
	}

	// the event stream only carries what Bob may see
	req, _ := http.NewRequest("GET", e.base+"/api/events", nil)
	req.Header.Set("Authorization", "Bearer "+bobTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			got <- sc.Text()
		}
	}()
	<-got // hello
	e.do(t, tok, "POST", "/api/agents/"+secID+"/messages", map[string]any{"text": "secret plan"})
	e.do(t, tok, "POST", "/api/agents/home/messages", map[string]any{"text": "open plan"})
	deadline := time.After(3 * time.Second)
	sawOpen := false
	for !sawOpen {
		select {
		case l := <-got:
			if strings.Contains(l, "secret plan") || strings.Contains(l, secSession) {
				t.Fatalf("hidden event reached bob: %s", l)
			}
			sawOpen = sawOpen || strings.Contains(l, "open plan")
		case <-deadline:
			t.Fatal("bob did not get the visible event")
		}
	}
}

func catID(t *testing.T, e *aenv) string {
	_, out := e.do(t, tok, "GET", "/api/members", nil)
	for _, m := range out["members"].([]any) {
		if m.(map[string]any)["name"] == "Cat" {
			return m.(map[string]any)["id"].(string)
		}
	}
	t.Fatal("no cat")
	return ""
}
