package api_test

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
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

const tok = "test-token-0123456789abcdef0123456789"

func start(t *testing.T) (string, *kernel.Host) {
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
		{prompt.Plugin, nil}, {tools.Plugin, nil}, {approval.Plugin, nil}, {approval.DesktopPlugin, nil},
		{llm.Plugin, nil}, {mock.Plugin, nil}, {router.Plugin, nil},
		{agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"mock/default"}}},
		{agentloop.Plugin, nil},
		{gateway.Plugin, map[string]any{"addr": "127.0.0.1:0", "token": tok}},
		{api.Plugin, nil},
	} {
		if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
			t.Fatal(err)
		}
	}
	kernel.Use[*gateway.Gateway](h.Root(), gateway.ServiceKey).AddToken("mobile-token-xxxxxxxxxxxxxxxxxxxxxxxx", "mobile")
	return "http://" + addr, h
}

func do(t *testing.T, base, token, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, base+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestAuthValidateActivate(t *testing.T) {
	base, h := start(t)
	if code, _ := do(t, base, "", "GET", "/api/agents", nil); code != 401 {
		t.Fatal("unauthenticated must be rejected", code)
	}
	// unauthenticated requests do not reveal existence: same 401 for any path
	if code, _ := do(t, base, "wrong", "POST", "/api/agents/x/messages", map[string]string{"text": "hi"}); code != 401 {
		t.Fatal(code)
	}
	code, a := do(t, base, tok, "POST", "/api/agents", map[string]any{"name": "Kai", "modelRefs": []string{"mock/default"}})
	if code != 200 || a["id"] != "kai" {
		t.Fatal(code, a)
	}
	if code, _ := do(t, base, tok, "POST", "/api/agents", map[string]any{"name": "X", "mode": "host"}); code != 400 {
		t.Fatal("host mode at creation must be rejected", code)
	}
	// validation failure must not enqueue (no activation)
	if code, _ := do(t, base, tok, "POST", "/api/agents/kai/messages", map[string]any{"text": ""}); code != 400 {
		t.Fatal(code)
	}
	ag, _ := kernel.Use[*agent.Registry](h.Root(), agent.ServiceKey).Get("kai")
	ss, _ := ag.Session()
	for _, e := range ss.Events() {
		if e.Type == session.TypeInputQueued {
			t.Fatal("rejected call activated the agent")
		}
	}
	// mobile may send messages but not create agents or list plugins
	if code, _ := do(t, base, "mobile-token-xxxxxxxxxxxxxxxxxxxxxxxx", "POST", "/api/agents", map[string]any{"name": "Y"}); code != 403 {
		t.Fatal(code)
	}
	if code, _ := do(t, base, "mobile-token-xxxxxxxxxxxxxxxxxxxxxxxx", "GET", "/api/plugins", nil); code != 403 {
		t.Fatal(code)
	}
	if code, _ := do(t, base, tok, "GET", "/api/agents/missing", nil); code != 404 {
		t.Fatal(code)
	}
}

func TestMessageAndEventStream(t *testing.T) {
	base, _ := start(t)
	do(t, base, tok, "POST", "/api/agents", map[string]any{"name": "Kai"})
	req, _ := http.NewRequest("GET", base+"/api/events?token="+tok, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	code, _ := do(t, base, "mobile-token-xxxxxxxxxxxxxxxxxxxxxxxx", "POST", "/api/agents/kai/messages", map[string]any{"text": "ping"})
	if code != 200 {
		t.Fatal(code)
	}
	sc := bufio.NewScanner(resp.Body)
	deadline := time.After(3 * time.Second)
	got := make(chan bool)
	go func() {
		for sc.Scan() {
			if strings.Contains(sc.Text(), "default: ping") {
				got <- true
				return
			}
		}
	}()
	select {
	case <-got:
	case <-deadline:
		t.Fatal("assistant reply not streamed")
	}
	code, out := do(t, base, tok, "GET", "/api/models", nil)
	if code != 200 || len(out["providers"].([]any)) != 1 {
		t.Fatal(code, out)
	}
}
