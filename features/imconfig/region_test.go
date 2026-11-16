package imconfig_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	"github.com/hedeqiang/crewbase/features/imconfig"
	"github.com/hedeqiang/crewbase/kernel"
)

const badTok = "999:not-the-token"

const appSecret = "feishu-app-secret-for-tests"

// fakeFeishu is an open platform that refuses the long connection; it
// counts requests and answers the credentials check for app "cli_ok".
func fakeFeishu(t *testing.T) (*httptest.Server, *atomic.Int32) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/tenant_access_token/internal"):
			var b map[string]string
			json.NewDecoder(r.Body).Decode(&b)
			if b["app_id"] == "cli_ok" && b["app_secret"] == appSecret {
				io.WriteString(w, `{"code":0,"tenant_access_token":"t-1","expire":7200}`)
				return
			}
			io.WriteString(w, `{"code":10014,"msg":"app secret invalid"}`)
		case strings.HasSuffix(r.URL.Path, "/bot/v3/info"):
			io.WriteString(w, `{"code":0,"bot":{"app_name":"Crew"}}`)
		default:
			io.WriteString(w, `{"code":1,"msg":"fake"}`)
		}
	}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })
	return srv, &hits
}

// withRegions points the 飞书 / Lark regions at test servers.
func withRegions(t *testing.T, cn, intl string) {
	saved := imconfig.Platforms
	t.Cleanup(func() { imconfig.Platforms = saved })
	imconfig.Platforms = append([]imconfig.Platform(nil), saved...)
	for i := range imconfig.Platforms {
		p := &imconfig.Platforms[i]
		if p.ID != "feishu" {
			continue
		}
		p.Regions = append([]imconfig.Region(nil), p.Regions...)
		for j := range p.Regions {
			switch r := &p.Regions[j]; r.ID {
			case "cn":
				if r.Domain != "https://open.feishu.cn" || r.TokenDoc != "https://open.feishu.cn/app" {
					t.Fatalf("cn region %+v", r)
				}
				r.Domain = cn
			case "intl":
				if r.Domain != "https://open.larksuite.com" || r.TokenDoc != "https://open.larksuite.com/app" {
					t.Fatalf("intl region %+v", r)
				}
				r.Domain = intl
			}
		}
	}
}

// imHost loads the im-config plugin over dir; seed runs before it loads.
func imHost(t *testing.T, dir string, provider map[string]any, seed func(cs *credentials.Service)) *imconfig.Service {
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
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
	cs := kernel.Use[*credentials.Service](h.Root(), credentials.ServiceKey)
	cs.SetBackend("keychain", credentials.NewMemory())
	if seed != nil {
		seed(cs)
	}
	if _, err := h.Load(imconfig.Plugin, kernel.LoadOptions{Config: map[string]any{"dir": dir, "provider": provider}}); err != nil {
		t.Fatal(err)
	}
	return kernel.Use[*imconfig.Service](h.Root(), imconfig.ServiceKey)
}

func waitHits(t *testing.T, n *atomic.Int32, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for n.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if n.Load() == 0 {
		t.Fatalf("%s never called its domain", what)
	}
}

// 飞书 and Lark are one platform: the region picks the open platform the
// gateway talks to (token and long connection).
func TestRegionPicksDomain(t *testing.T) {
	cn, cnHits := fakeFeishu(t)
	intl, intlHits := fakeFeishu(t)
	withRegions(t, cn.URL, intl.URL)
	s := imHost(t, t.TempDir(), nil, nil)
	v, err := s.Save("lark", imconfig.Input{Platform: "feishu", Region: "intl", Channel: "oc_1", AppID: "cli_x", BotToken: "secret", AllowUsers: []string{"ou_1"}})
	if err != nil {
		t.Fatal(err)
	}
	if v.Platform != "feishu" || v.Region != "intl" {
		t.Fatalf("saved %+v", v.Gateway)
	}
	waitHits(t, intlHits, "the Lark gateway")
	if cnHits.Load() != 0 {
		t.Fatal("the Lark gateway called open.feishu.cn")
	}
	// a new gateway without a region is in mainland China
	if v, err := s.Save("feishu", imconfig.Input{Platform: "feishu", Channel: "oc_2", AppID: "cli_y", BotToken: "secret"}); err != nil || v.Region != "cn" {
		t.Fatal(v, err)
	}
	waitHits(t, cnHits, "the 飞书 gateway")
	if _, err := s.Save("x", imconfig.Input{Platform: "feishu", Region: "mars", Channel: "oc_3", AppID: "cli_z", BotToken: "secret"}); err == nil {
		t.Fatal("saved an unknown region")
	}
}

// Gateways saved before the merge (platform "lark", or 飞书 without a
// region) keep working: same id and tokens, shown as the merged platform.
func TestLegacyLarkGateway(t *testing.T) {
	cn, cnHits := fakeFeishu(t)
	intl, intlHits := fakeFeishu(t)
	withRegions(t, cn.URL, intl.URL)
	dir := t.TempDir()
	old := `[
  {"id":"lark","platform":"lark","channel":"oc_l","allowUsers":["ou_1"],"appId":"cli_l","relayReplies":true,"approvals":true,"enabled":true,"created":"2026-11-01T00:00:00Z"},
  {"id":"feishu","platform":"feishu","channel":"oc_f","allowUsers":[],"appId":"cli_f","relayReplies":true,"approvals":true,"enabled":true,"created":"2026-11-02T00:00:00Z"}
]`
	if err := os.WriteFile(filepath.Join(dir, "im.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	s := imHost(t, dir, nil, func(cs *credentials.Service) {
		cs.Set("keychain:crewbase/im-lark-bot", "secret-l")
		cs.Set("keychain:crewbase/im-feishu-bot", "secret-f")
	})
	list := s.List()
	if len(list) != 2 {
		t.Fatalf("list %+v", list)
	}
	for _, v := range list {
		want := map[string]string{"lark": "intl", "feishu": "cn"}[v.ID]
		if v.Platform != "feishu" || v.Region != want || !v.HasToken || !v.Running {
			t.Fatalf("%s: %+v", v.ID, v)
		}
	}
	waitHits(t, intlHits, "the old Lark gateway")
	waitHits(t, cnHits, "the old 飞书 gateway")
	// an old client still asking for platform "lark" gets the merged one
	if v, err := s.Save("lark-2", imconfig.Input{Platform: "lark", Channel: "oc_9", AppID: "cli_9", BotToken: "secret"}); err != nil || v.Platform != "feishu" || v.Region != "intl" {
		t.Fatal(v, err)
	}
	// editing keeps the region; the file is rewritten in the new shape
	if v, err := s.Save("lark", imconfig.Input{AllowUsers: []string{"ou_2"}}); err != nil || v.Region != "intl" {
		t.Fatal(v, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "im.json"))
	var saved []imconfig.Gateway
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	for _, g := range saved {
		if g.Platform != "feishu" || g.Region == "" {
			t.Fatalf("not migrated: %+v", g)
		}
	}
	for _, p := range imconfig.Platforms {
		if p.ID == "lark" {
			t.Fatal("lark is still its own platform")
		}
	}
}

// Credentials are checked against the platform without saving anything;
// a rejected token is a result and never echoed back.
func TestVerify(t *testing.T) {
	var sends atomic.Int32
	tg := fakeTelegram(t, &sends)
	fs, _ := fakeFeishu(t)
	withRegions(t, "http://127.0.0.1:1", fs.URL)
	dir := t.TempDir()
	s := imHost(t, dir, map[string]any{"apiBase": tg.URL, "pollTimeout": 1}, nil)
	ctx := t.Context()
	r, err := s.Verify(ctx, imconfig.Check{Platform: "telegram", BotToken: botTok})
	if err != nil || !r.OK || r.Bot != "@crew_bot" {
		t.Fatal(r, err)
	}
	r, err = s.Verify(ctx, imconfig.Check{Platform: "telegram", BotToken: badTok})
	if err != nil || r.OK || r.Error == "" || strings.Contains(r.Error, badTok) {
		t.Fatal(r, err)
	}
	r, err = s.Verify(ctx, imconfig.Check{Platform: "feishu", Region: "intl", AppID: "cli_ok", BotToken: appSecret})
	if err != nil || !r.OK || r.Bot != "Crew" {
		t.Fatal(r, err)
	}
	r, err = s.Verify(ctx, imconfig.Check{Platform: "lark", AppID: "cli_ok", BotToken: "nope"})
	if err != nil || r.OK || !strings.Contains(r.Error, "app secret invalid") {
		t.Fatal(r, err)
	}
	// the mainland region is another (here unreachable) host
	r, err = s.Verify(ctx, imconfig.Check{Platform: "feishu", Region: "cn", AppID: "cli_ok", BotToken: appSecret})
	if err != nil || r.OK || strings.Contains(r.Error, appSecret) {
		t.Fatal(r, err)
	}
	if _, err := s.Verify(ctx, imconfig.Check{Platform: "slack", BotToken: "xoxb-1"}); err == nil {
		t.Fatal("slack without an app-level token")
	}
	// a saved gateway's stored token fills in an empty one (editing)
	off := false
	if _, err := s.Save("tg", imconfig.Input{Platform: "telegram", Channel: "-100", BotToken: botTok, Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if r, err := s.Verify(ctx, imconfig.Check{ID: "tg"}); err != nil || !r.OK {
		t.Fatal(r, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "im.json")); strings.Count(string(b), `"id"`) != 1 {
		t.Fatalf("verify saved something: %s", b)
	}
}

// The connect dialog lists the groups the bot is in (飞书 / Lark), page by
// page, without dissolved ones; a missing permission is a hint, not an
// error; saved secrets fill in when editing.
func TestFeishuChats(t *testing.T) {
	mode := "ok"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/tenant_access_token/internal"):
			var b map[string]string
			json.NewDecoder(r.Body).Decode(&b)
			if b["app_id"] == "cli_ok" && b["app_secret"] == appSecret {
				io.WriteString(w, `{"code":0,"tenant_access_token":"t-1","expire":7200}`)
				return
			}
			io.WriteString(w, `{"code":10014,"msg":"app secret invalid"}`)
		case r.URL.Path == "/open-apis/im/v1/chats":
			if r.Header.Get("Authorization") != "Bearer t-1" {
				io.WriteString(w, `{"code":99991663,"msg":"Invalid access token"}`)
				return
			}
			switch {
			case mode == "denied":
				io.WriteString(w, `{"code":99991672,"msg":"Access denied. One of the following scopes is required: [im:chat:readonly]"}`)
			case mode == "none":
				io.WriteString(w, `{"code":0,"data":{"items":[],"has_more":false}}`)
			case r.URL.Query().Get("page_token") == "":
				io.WriteString(w, `{"code":0,"data":{"items":[{"chat_id":"oc_1","name":"发布群","chat_status":"normal"},{"chat_id":"oc_gone","name":"旧群","chat_status":"dissolved"}],"has_more":true,"page_token":"p2"}}`)
			default:
				io.WriteString(w, `{"code":0,"data":{"items":[{"chat_id":"oc_2","name":"客户群","description":"外部","external":true,"chat_status":"normal"}],"has_more":false}}`)
			}
		default:
			io.WriteString(w, `{"code":1,"msg":"fake"}`)
		}
	}))
	t.Cleanup(srv.Close)
	withRegions(t, srv.URL, "http://127.0.0.1:1")
	s := imHost(t, t.TempDir(), nil, nil)
	ctx := t.Context()

	got, err := s.Chats(ctx, imconfig.Check{Platform: "feishu", Region: "cn", AppID: "cli_ok", BotToken: appSecret})
	if err != nil || got.Hint != "" || len(got.Chats) != 2 || got.Chats[0].ID != "oc_1" || got.Chats[1].Name != "客户群" || !got.Chats[1].External {
		t.Fatalf("%+v %v", got, err)
	}
	mode = "denied"
	if got, err := s.Chats(ctx, imconfig.Check{Platform: "feishu", Region: "cn", AppID: "cli_ok", BotToken: appSecret}); err != nil || got.Hint != imconfig.HintNoPermission {
		t.Fatalf("denied: %+v %v", got, err)
	}
	mode = "none"
	if got, _ := s.Chats(ctx, imconfig.Check{Platform: "feishu", Region: "cn", AppID: "cli_ok", BotToken: appSecret}); got.Hint != imconfig.HintNotInGroups {
		t.Fatalf("none: %+v", got)
	}
	if _, err := s.Chats(ctx, imconfig.Check{Platform: "feishu", Region: "cn", AppID: "cli_ok", BotToken: "wrong"}); err == nil || strings.Contains(err.Error(), "wrong") {
		t.Fatalf("bad secret: %v", err)
	}
	if got, _ := s.Chats(ctx, imconfig.Check{Platform: "telegram", BotToken: "x"}); got.Hint != imconfig.HintNoList {
		t.Fatalf("telegram: %+v", got)
	}
}
