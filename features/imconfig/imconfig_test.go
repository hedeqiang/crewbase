package imconfig_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/capabilities/notify"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/features/im"
	"github.com/hedeqiang/crewbase/features/imconfig"
	"github.com/hedeqiang/crewbase/kernel"
)

const deskTok = "desk-token-imconfig-test-xxxxxxxxxxxxxx"
const botTok = "123:{{SECRET_rrkvoq0b}}"

// a minimal Telegram API: getMe ok for the right token, empty updates.
func fakeTelegram(t *testing.T, sends *atomic.Int32) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/bot"+botTok+"/") {
			w.WriteHeader(401)
			io.WriteString(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
			return
		}
		switch strings.TrimPrefix(r.URL.Path, "/bot"+botTok+"/") {
		case "getMe":
			io.WriteString(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"Crew","username":"crew_bot"}}`)
		case "getUpdates":
			select {
			case <-time.After(300 * time.Millisecond):
			case <-r.Context().Done():
			}
			io.WriteString(w, `{"ok":true,"result":[]}`)
		case "sendMessage":
			sends.Add(1)
			io.WriteString(w, `{"ok":true,"result":{"message_id":7,"chat":{"id":-100,"type":"group"},"text":"x"}}`)
		default:
			io.WriteString(w, `{"ok":true,"result":true}`)
		}
	}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })
	return srv
}

func TestSetupFromUI(t *testing.T) {
	var sends atomic.Int32
	tg := fakeTelegram(t, &sends)
	h := kernel.NewHost(nil)
	defer h.Close()
	var addr string
	gateway.Listening.Listen(h.Root(), func(a string) { addr = a })
	dir := t.TempDir()
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, map[string]any{"dir": dir + "/sessions"}}, {prompt.Plugin, nil},
		{agent.Plugin, map[string]any{"root": dir}}, {tools.Plugin, nil},
		{approval.Plugin, nil}, {notify.Plugin, nil}, {credentials.Plugin, nil},
		{gateway.Plugin, map[string]any{"addr": "127.0.0.1:0", "token": deskTok}},
		{im.APIPlugin, nil},
	} {
		if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
			t.Fatal(err)
		}
	}
	cs := kernel.Use[*credentials.Service](h.Root(), credentials.ServiceKey)
	cs.SetBackend("keychain", credentials.NewMemory())
	if _, err := h.Load(imconfig.Plugin, kernel.LoadOptions{Config: map[string]any{"dir": dir, "provider": map[string]any{"apiBase": tg.URL, "pollTimeout": 1}}}); err != nil {
		t.Fatal(err)
	}
	do := func(method, path string, body any) (int, map[string]any) {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, "http://"+addr+path, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+deskTok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	if code, out := do("GET", "/api/im/config", nil); code != 200 || len(out["platforms"].([]any)) != 5 {
		t.Fatal(code, out)
	}
	// a token is required
	if code, _ := do("PUT", "/api/im/config/tg", map[string]any{"platform": "telegram", "channel": "-100"}); code != 400 {
		t.Fatal("saved without a token")
	}
	code, out := do("PUT", "/api/im/config/tg", map[string]any{"platform": "telegram", "channel": "-100", "allowUsers": []string{"42, 43"}, "botToken": botTok})
	if code != 200 || out["hasToken"] != true || out["running"] != true {
		t.Fatal(code, out)
	}
	if strings.Join(toStrings(out["allowUsers"]), ",") != "42,43" {
		t.Fatalf("allowUsers %v", out["allowUsers"])
	}
	// the gateway runs and connects
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, st := do("GET", "/api/im", nil)
		gws, _ := st["gateways"].([]any)
		if len(gws) == 1 && gws[0].(map[string]any)["connected"] == true {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not connected: %v", st)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if code, r := do("POST", "/api/im/tg/test", nil); code != 200 || r["ok"] != true || sends.Load() == 0 {
		t.Fatal(code, r)
	}
	// the token never reaches im.json
	b, _ := os.ReadFile(filepath.Join(dir, "im.json"))
	if strings.Contains(string(b), botTok) {
		t.Fatal("token in im.json")
	}
	// disable stops it; remove deletes the token
	do("PUT", "/api/im/config/tg", map[string]any{"enabled": false})
	_, st := do("GET", "/api/im", nil)
	if gws, _ := st["gateways"].([]any); len(gws) != 0 {
		t.Fatalf("still running: %v", st)
	}
	if code, _ := do("DELETE", "/api/im/config/tg", nil); code != 200 {
		t.Fatal(code)
	}
	if v, _ := cs.Resolve(t.Context(), "keychain:crewbase/im-tg-bot"); v != "" {
		t.Fatal("token kept after remove")
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

// Lark uses the same gateway against open.larksuite.com: the platform's
// domain reaches the Feishu SDK (token and long-connection endpoint).
func TestLarkUsesItsDomain(t *testing.T) {
	var hits atomic.Int32
	var paths sync.Map
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		paths.Store(r.URL.Path, true)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"code":1,"msg":"fake lark"}`)
	}))
	defer srv.Close()
	saved := imconfig.Platforms
	defer func() { imconfig.Platforms = saved }()
	imconfig.Platforms = append([]imconfig.Platform(nil), saved...)
	found := false
	for i := range imconfig.Platforms {
		if imconfig.Platforms[i].ID == "lark" {
			if imconfig.Platforms[i].Domain != "https://open.larksuite.com" {
				t.Fatalf("lark domain %q", imconfig.Platforms[i].Domain)
			}
			imconfig.Platforms[i].Domain = srv.URL
			found = true
		}
	}
	if !found {
		t.Fatal("no lark platform")
	}
	h := kernel.NewHost(nil)
	defer h.Close()
	dir := t.TempDir()
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, map[string]any{"dir": dir + "/sessions"}}, {prompt.Plugin, nil},
		{agent.Plugin, map[string]any{"root": dir}}, {tools.Plugin, nil},
		{approval.Plugin, nil}, {notify.Plugin, nil}, {credentials.Plugin, nil},
		{gateway.Plugin, map[string]any{"addr": "127.0.0.1:0", "token": deskTok}},
	} {
		if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
			t.Fatal(err)
		}
	}
	kernel.Use[*credentials.Service](h.Root(), credentials.ServiceKey).SetBackend("keychain", credentials.NewMemory())
	if _, err := h.Load(imconfig.Plugin, kernel.LoadOptions{Config: map[string]any{"dir": dir}}); err != nil {
		t.Fatal(err)
	}
	s := kernel.Use[*imconfig.Service](h.Root(), imconfig.ServiceKey)
	if _, err := s.Save("", imconfig.Input{Platform: "lark", Channel: "oc_1", AppID: "cli_x", BotToken: "secret", AllowUsers: []string{"ou_1"}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if hits.Load() == 0 {
		t.Fatal("the Lark gateway never called its domain")
	}
	var got []string
	paths.Range(func(k, _ any) bool { got = append(got, k.(string)); return true })
	t.Logf("lark requests: %v", got)
}
