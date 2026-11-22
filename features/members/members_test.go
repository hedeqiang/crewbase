package members_test

import (
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
	"github.com/hedeqiang/crewbase/features/devices"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/features/members"
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/features/rules"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

const owner = "owner-token-members-test-xxxxxxxxxxxxxxx"

type env struct {
	base string
	h    *kernel.Host
	ds   *devices.Service
}

func start(t *testing.T) env {
	t.Helper()
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	var addr string
	gateway.Listening.Listen(h.Root(), func(a string) { addr = a })
	root := t.TempDir()
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, map[string]any{"dir": root + "/sessions"}},
		{prompt.Plugin, nil}, {tools.Plugin, map[string]any{"requirePolicy": "rules"}}, {approval.Plugin, nil}, {approval.DesktopPlugin, nil},
		{rules.Plugin, map[string]any{"dir": root}},
		{llm.Plugin, nil}, {mock.Plugin, nil}, {router.Plugin, nil},
		{agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"mock/default"}}},
		{agentloop.Plugin, nil},
		{gateway.Plugin, map[string]any{"addr": "127.0.0.1:0", "token": owner}},
		{api.Plugin, nil},
		{devices.Plugin, map[string]any{"dir": root}},
		{members.Plugin, map[string]any{"dir": root}},
	} {
		if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
			t.Fatal(x.s.Name, err)
		}
	}
	return env{base: "http://" + addr, h: h, ds: kernel.Use[*devices.Service](h.Root(), devices.ServiceKey)}
}

func (e env) do(t *testing.T, tok, method, path string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	if body == nil {
		b = nil
	}
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

func (e env) pair(t *testing.T, member string) string {
	t.Helper()
	code, p := e.do(t, owner, "POST", "/api/devices/pairing", map[string]any{"member": member})
	if code != 200 {
		t.Fatal(code, p)
	}
	r, err := e.ds.Redeem(p["code"].(string), member+" phone", "", "")
	if err != nil {
		t.Fatal(err)
	}
	return r.Token
}

func TestRolesApprovalsAndRemoval(t *testing.T) {
	e := start(t)
	add := func(name, role string) string {
		code, m := e.do(t, owner, "POST", "/api/members", map[string]any{"name": name, "role": role})
		if code != 200 {
			t.Fatal(code, m)
		}
		return m["id"].(string)
	}
	ann, bob, cat := add("Ann", "admin"), add("Bob", "member"), add("Cat", "viewer")
	if code, _ := e.do(t, owner, "POST", "/api/members", map[string]any{"name": "bob"}); code != 400 {
		t.Fatal("duplicate name accepted")
	}
	annTok, bobTok, catTok := e.pair(t, ann), e.pair(t, bob), e.pair(t, cat)

	// tiers follow roles
	if code, me := e.do(t, bobTok, "GET", "/api/me", nil); code != 200 || me["name"] != "Bob" || me["tier"] != "mobile" {
		t.Fatal(code, me)
	}
	if code, _ := e.do(t, annTok, "GET", "/api/members", nil); code != 200 {
		t.Fatal("admin cannot manage members:", code)
	}
	if code, _ := e.do(t, bobTok, "GET", "/api/members", nil); code != 403 {
		t.Fatal("member reached admin routes:", code)
	}
	// viewers read and change nothing
	if code, _ := e.do(t, catTok, "GET", "/api/agents", nil); code != 200 {
		t.Fatal("viewer cannot read:", code)
	}
	if code, _ := e.do(t, owner, "POST", "/api/agents", map[string]any{"name": "Kai"}); code != 200 {
		t.Fatal(code)
	}
	if code, _ := e.do(t, catTok, "POST", "/api/agents/kai/messages", map[string]any{"text": "hi"}); code != 403 {
		t.Fatal("viewer sent a message:", code)
	}
	// a member's message carries their name
	if code, _ := e.do(t, bobTok, "POST", "/api/agents/kai/messages", map[string]any{"text": "hello", "from": "Mallory"}); code != 200 {
		t.Fatal(code)
	}
	reg := kernel.Use[*agent.Registry](e.h.Root(), agent.ServiceKey)
	kai, _ := reg.Get("kai")
	agentloop.WaitIdle(kai, 3*time.Second)
	ss, _ := kai.Session()
	var from string
	for _, ev := range ss.Events() {
		if ev.Type == session.TypeUserMessage {
			var m session.UserMessage
			ev.Decode(&m)
			from = m.From
		}
	}
	if from != "Bob" {
		t.Fatalf("message from %q, want Bob", from)
	}

	// approvals: Bob may approve exec, not delete; the owner may approve anything
	in := kernel.Use[*approval.Inbox](e.h.Root(), approval.InboxKey)
	ask := func(id, cat string) chan approval.Decision {
		ch := make(chan approval.Decision, 1)
		go func() {
			d, _ := in.Respond(t.Context(), approval.Request{CallID: id, Agent: "kai", Session: ss.ID(), Tool: "bash", Category: cat, Summary: id})
			ch <- d
		}()
		for i := 0; i < 100; i++ {
			for _, p := range in.Pending() {
				if p.CallID == id {
					return ch
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("approval not pending")
		return nil
	}
	del := ask("c-del", "delete")
	if code, out := e.do(t, bobTok, "POST", "/api/approvals/c-del", map[string]any{"outcome": "allowed-once"}); code != 403 || !strings.Contains(out["error"].(map[string]any)["message"].(string), "delete") {
		t.Fatal("member approved a delete:", code, out)
	}
	exec := ask("c-exec", "exec")
	if code, _ := e.do(t, bobTok, "POST", "/api/approvals/c-exec", map[string]any{"outcome": "allowed-once"}); code != 200 {
		t.Fatal(code)
	}
	if d := <-exec; d.Outcome != approval.OutcomeAllowedOnce || d.By != "Bob" {
		t.Fatalf("decision %+v", d)
	}
	// anyone who can see it may reject
	if code, _ := e.do(t, bobTok, "POST", "/api/approvals/c-del", map[string]any{"outcome": "rejected"}); code != 200 {
		t.Fatal(code)
	}
	<-del
	// promote Bob: delete becomes approvable at once
	if code, _ := e.do(t, owner, "PATCH", "/api/members/"+bob, map[string]any{"approve": []string{"exec", "delete"}}); code != 200 {
		t.Fatal(code)
	}
	del2 := ask("c-del2", "delete")
	if code, _ := e.do(t, bobTok, "POST", "/api/approvals/c-del2", map[string]any{"outcome": "allowed-once"}); code != 200 {
		t.Fatal(code)
	}
	<-del2
	if code, _ := e.do(t, owner, "PATCH", "/api/members/"+bob, map[string]any{"approve": []string{"payment"}}); code != 400 {
		t.Fatal("payment made approvable")
	}
	// demote Bob to viewer: his phone can no longer send
	e.do(t, owner, "PATCH", "/api/members/"+bob, map[string]any{"role": "viewer"})
	if code, _ := e.do(t, bobTok, "POST", "/api/agents/kai/messages", map[string]any{"text": "x"}); code != 403 {
		t.Fatal("demoted member still sends:", code)
	}
	// nobody changes their own role
	if code, _ := e.do(t, annTok, "PATCH", "/api/members/"+ann, map[string]any{"role": "admin"}); code != 403 {
		t.Fatal("self role change:", code)
	}
	// removing a member revokes their devices
	if code, out := e.do(t, owner, "DELETE", "/api/members/"+bob, nil); code != 200 || out["devices"] != float64(1) {
		t.Fatal(code, out)
	}
	if code, _ := e.do(t, bobTok, "GET", "/api/agents", nil); code != 401 {
		t.Fatal("removed member's device still works:", code)
	}
}

// An admin cannot make themselves the owner: a device paired for nobody in
// particular is theirs, and the owner's devices are not theirs to remove.
func TestAdminCannotPairOrRevokeAsOwner(t *testing.T) {
	e := start(t)
	code, m := e.do(t, owner, "POST", "/api/members", map[string]any{"name": "Ann", "role": "admin"})
	if code != 200 {
		t.Fatal(code, m)
	}
	ann := m["id"].(string)
	annTok := e.pair(t, ann)

	// the owner's own phone
	code, p := e.do(t, owner, "POST", "/api/devices/pairing", map[string]any{"tier": "mobile"})
	if code != 200 {
		t.Fatal(code, p)
	}
	ownerPhone, err := e.ds.Redeem(p["code"].(string), "owner phone", "", "")
	if err != nil {
		t.Fatal(err)
	}

	// the admin asks for a desktop code for nobody: it is for the admin
	code, p = e.do(t, annTok, "POST", "/api/devices/pairing", map[string]any{"tier": "desktop"})
	if code != 200 {
		t.Fatal(code, p)
	}
	if p["member"] != ann {
		t.Fatalf("an admin's code for nobody is the owner's: %v", p)
	}
	r, err := e.ds.Redeem(p["code"].(string), "ann laptop", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if code, me := e.do(t, r.Token, "GET", "/api/me", nil); code != 200 || me["name"] != "Ann" {
		t.Fatalf("the new device is not Ann's: %d %v", code, me)
	}

	// the owner's devices: not the admin's to rename or revoke
	if code, _ := e.do(t, annTok, "DELETE", "/api/devices/"+ownerPhone.Device.ID, nil); code != 403 {
		t.Fatalf("admin revoked the owner's phone: %d", code)
	}
	if code, _ := e.do(t, annTok, "PATCH", "/api/devices/"+ownerPhone.Device.ID, map[string]any{"name": "x"}); code != 403 {
		t.Fatalf("admin renamed the owner's phone: %d", code)
	}
	// the owner still can
	if code, _ := e.do(t, owner, "DELETE", "/api/devices/"+ownerPhone.Device.ID, nil); code != 200 {
		t.Fatalf("owner could not revoke their phone: %d", code)
	}
}
