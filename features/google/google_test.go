package google

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/catalog"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

func init() { backoffBase = time.Millisecond }

type env struct {
	h     *kernel.Host
	f     *fakeGoogle
	s     *Service
	reg   *tools.Registry
	kc    *credentials.Memory // the "system keychain"
	base  string
	dir   string
	httpc *http.Client
}

func newEnv(t *testing.T) *env {
	t.Helper()
	t.Setenv(EnvClientID, "")
	t.Setenv(EnvClientSecret, "")
	f := newFake(t)
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	addr := make(chan string, 1)
	gateway.Listening.Listen(h.Root(), func(a string) { addr <- a })
	dir := t.TempDir()
	kc := credentials.NewMemory()
	for _, l := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{tools.Plugin, nil}, {credentials.Plugin, nil},
		{gateway.Plugin, map[string]any{"addr": "127.0.0.1:0", "token": "tok"}},
		{Plugin, map[string]any{"dir": dir, "clientId": testClientID, "clientSecret": testClientSecret,
			"oauthBase": f.srv.URL, "apiBase": f.srv.URL}},
		{APIPlugin, nil},
	} {
		if l.s.Name == Plugin.Name {
			kernel.Use[*credentials.Service](h.Root(), credentials.ServiceKey).SetBackend("keychain", kc)
		}
		if _, err := h.Load(l.s, kernel.LoadOptions{Config: l.cfg}); err != nil {
			t.Fatal(err)
		}
	}
	if info, _ := h.Plugin("@crewbase/google-api"); info.State != kernel.StateActive {
		t.Fatalf("%+v", info)
	}
	e := &env{h: h, f: f, kc: kc, dir: dir,
		s:    kernel.Use[*Service](h.Root(), ServiceKey),
		reg:  kernel.Use[*tools.Registry](h.Root(), tools.ServiceKey),
		base: "http://" + <-addr,
		// a browser: follows Google's redirect to the callback, stops at the app
		httpc: &http.Client{CheckRedirect: func(r *http.Request, _ []*http.Request) error {
			if r.URL.Path == "/" {
				return http.ErrUseLastResponse
			}
			return nil
		}},
	}
	return e
}

func (e *env) do(t *testing.T, method, path string, body any) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.base+path, rd)
	req.Header.Set("Authorization", "Bearer tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(raw)
}

func (e *env) status(t *testing.T) View {
	t.Helper()
	code, raw := e.do(t, "GET", "/api/google", nil)
	if code != 200 {
		t.Fatal(code, raw)
	}
	var v View
	_ = json.Unmarshal([]byte(raw), &v)
	return v
}

// connect signs product in through the API and a "browser"; it returns
// where the browser ended up.
func (e *env) connect(t *testing.T, product string) *url.URL {
	t.Helper()
	code, raw := e.do(t, "POST", "/api/google/connect", map[string]string{"product": product})
	if code != 200 {
		t.Fatal(code, raw)
	}
	var r struct {
		AuthURL string `json:"authUrl"`
	}
	_ = json.Unmarshal([]byte(raw), &r)
	res, err := e.httpc.Get(r.AuthURL)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	loc, _ := url.Parse(res.Header.Get("Location")) // the app page it lands on
	return loc
}

func (e *env) product(v View, id string) ProductView {
	for _, p := range v.Products {
		if p.ID == id {
			return p
		}
	}
	return ProductView{}
}

func (e *env) run(t *testing.T, name string, args any) (tools.Result, string) {
	t.Helper()
	tl, ok := e.reg.Get("", name)
	if !ok {
		t.Fatalf("tool %s not registered", name)
	}
	b, _ := json.Marshal(args)
	call := tools.Call{Name: name, Args: b, Workspace: e.dir}
	cat, sum := tl.Category, ""
	if tl.Classify != nil {
		if c, s := tl.Classify(call); c != "" {
			cat, sum = c, s
		}
	}
	res, err := tl.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	return res, cat + "|" + sum
}

// ---------------------------------------------------------------- client config

func TestNotConfiguredNeverCrashes(t *testing.T) {
	t.Setenv(EnvClientID, "")
	t.Setenv(EnvClientSecret, "")
	// a build without the embedded secret (CI builds have none)
	old := DefaultClientSecret
	DefaultClientSecret = ""
	t.Cleanup(func() { DefaultClientSecret = old })
	s := New(Config{}, credentials.New(), nil, nil)
	s.SetRedirectBase("127.0.0.1:7788")
	v := s.Status()
	if v.Configured || v.Missing != "clientSecret" || v.Connected {
		t.Fatalf("%+v", v)
	}
	if _, err := s.Connect("gmail", ""); !errors.Is(err, ErrNotConfigured) {
		t.Fatal(err)
	}
	st := connector{s}.State("gmail")
	if st.Configured || st.Installed || !strings.Contains(st.Note.En, "not set up in this build") {
		t.Fatalf("%+v", st)
	}
	if _, err := s.Token(context.Background()); !errors.Is(err, ErrNotConnected) {
		t.Fatal(err)
	}
	// id from the build, secret from the environment
	t.Setenv(EnvClientSecret, "s3")
	if v := s.Status(); !v.Configured || v.Source != "build" {
		t.Fatalf("%+v", v)
	}
	// config wins over env
	t.Setenv(EnvClientID, "env-id")
	if c, _ := s.client(context.Background()); c.id != "env-id" || c.source != "env" {
		t.Fatalf("%+v", c)
	}
	s.cfg.ClientID = "cfg-id"
	if c, _ := s.client(context.Background()); c.id != "cfg-id" || c.source != "config" {
		t.Fatalf("%+v", c)
	}
}

// With Google's real endpoints the sign-in link is Google's, with PKCE
// S256, offline access and a loopback redirect to the gateway.
func TestAuthURLForGoogle(t *testing.T) {
	t.Setenv(EnvClientID, "")
	t.Setenv(EnvClientSecret, "x")
	s := New(Config{}, credentials.New(), nil, nil)
	if _, err := s.Connect("gmail", ""); !errors.Is(err, ErrNoRedirect) {
		t.Fatal(err)
	}
	s.SetRedirectBase("[::]:7788") // all interfaces → loopback IP in the redirect
	raw, err := s.Connect("gmail", "")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	if u.Scheme != "https" || u.Host != "accounts.google.com" || u.Path != "/o/oauth2/v2/auth" {
		t.Fatal(raw)
	}
	want := map[string]string{
		"client_id": DefaultClientID, "redirect_uri": "http://127.0.0.1:7788/api/google/callback", "response_type": "code",
		"code_challenge_method": "S256", "access_type": "offline", "include_granted_scopes": "true", "prompt": "consent",
	}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
	if len(q.Get("code_challenge")) != 43 || len(q.Get("state")) < 32 {
		t.Fatal(q)
	}
	for _, sc := range []string{"openid", "email", scopePrefix + "gmail.readonly", scopePrefix + "gmail.compose"} {
		if !strings.Contains(" "+q.Get("scope")+" ", " "+sc+" ") {
			t.Errorf("scope %q missing: %s", sc, q.Get("scope"))
		}
	}
	if strings.Contains(raw, "client_secret") {
		t.Fatal("secret in the browser URL")
	}
	// the verifier stays here and matches the challenge
	s.mu.Lock()
	p := s.pending[q.Get("state")]
	s.mu.Unlock()
	if p == nil || challengeS256(p.verifier) != q.Get("code_challenge") || len(p.verifier) < 43 {
		t.Fatal("verifier")
	}
}

// ---------------------------------------------------------------- sign-in

func TestConnectRefreshAndSignOut(t *testing.T) {
	e := newEnv(t)
	if v := e.status(t); !v.Configured || v.Connected || v.Source != "config" {
		t.Fatalf("%+v", v)
	}
	if _, ok := e.reg.Get("", "gmail_search"); ok {
		t.Fatal("tools before connecting")
	}
	end := e.connect(t, "gmail")
	if end.Fragment != "/settings/market?google=connected" {
		t.Fatal(end)
	}
	v := e.status(t)
	if !v.Connected || v.Email != "me@example.com" || !e.product(v, "gmail").Connected || e.product(v, "calendar").Connected {
		t.Fatalf("%+v", v)
	}
	if tl, ok := e.reg.Get("", "gmail_search"); !ok || tl.Source != "gmail" {
		t.Fatal("gmail tools missing or untagged", tl)
	}
	if cs := e.reg.Connectors(); len(cs) != 1 || cs[0].Name != "gmail" || cs[0].Kind != "builtin" || cs[0].Tools == 0 {
		t.Fatalf("connectors: %+v", cs)
	}
	if _, ok := e.reg.Get("", "calendar_list_events"); ok {
		t.Fatal("calendar tools before connecting calendar")
	}
	// the refresh token is only in the keychain; nothing secret on disk
	rt, err := e.kc.Get(context.Background(), "crewbase/google-oauth")
	if err != nil || !strings.HasPrefix(rt, "rt-") {
		t.Fatal(rt, err)
	}
	b, _ := os.ReadFile(filepath.Join(e.dir, "google.json"))
	if !strings.Contains(string(b), "me@example.com") || strings.Contains(string(b), "rt-") || strings.Contains(string(b), "at-") {
		t.Fatal(string(b))
	}

	// a state is accepted once; unknown states are refused
	for _, q := range []string{"state=nope&code=x", ""} {
		res, err := e.httpc.Get(e.base + CallbackPath + "?" + q)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 401 {
			t.Fatal(q, res.StatusCode)
		}
	}

	// adding Calendar asks for both products' scopes; the new refresh
	// token replaces the old one
	code, raw := e.do(t, "POST", "/api/google/connect", map[string]string{"product": "calendar"})
	var r struct{ AuthURL string }
	_ = json.Unmarshal([]byte(raw), &r)
	if code != 200 || !strings.Contains(r.AuthURL, url.QueryEscape(scopePrefix+"gmail.readonly")) || !strings.Contains(r.AuthURL, "login_hint=me%40example.com") {
		t.Fatal(code, r.AuthURL)
	}
	res, err := e.httpc.Get(r.AuthURL)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	v = e.status(t)
	if !e.product(v, "gmail").Connected || !e.product(v, "calendar").Connected {
		t.Fatalf("%+v", v)
	}
	if _, ok := e.reg.Get("", "calendar_free_busy"); !ok {
		t.Fatal("calendar tools missing")
	}

	// an expired access token is refreshed with the stored refresh token
	e.s.mu.Lock()
	e.s.expiry = time.Now().Add(-time.Minute)
	e.s.mu.Unlock()
	before := e.f.called("POST /token")
	if res, _ := e.run(t, "gmail_search", map[string]any{"q": "from:ann"}); res.IsError {
		t.Fatal(res.Content)
	}
	if e.f.called("POST /token") != before+1 {
		t.Fatal("no refresh")
	}
	// a 401 from the API refreshes once and retries
	e.s.mu.Lock()
	e.s.access = "stale"
	e.s.expiry = time.Now().Add(time.Hour)
	e.s.mu.Unlock()
	if res, _ := e.run(t, "calendar_list_events", map[string]any{}); res.IsError || !strings.Contains(res.Content, "Standup") {
		t.Fatal(res.Content)
	}

	// removing one product keeps the account and its other product
	if code, raw := e.do(t, "POST", "/api/google/disconnect", map[string]string{"product": "calendar"}); code != 200 {
		t.Fatal(code, raw)
	}
	if _, ok := e.reg.Get("", "calendar_list_events"); ok {
		t.Fatal("calendar tools after disconnecting it")
	}
	if len(e.f.revoked) != 0 {
		t.Fatal("revoked while a product remains")
	}
	// removing the last signs out: revoke + keychain item deleted
	rt, _ = e.kc.Get(context.Background(), "crewbase/google-oauth")
	if code, raw := e.do(t, "POST", "/api/google/disconnect", map[string]string{"product": "gmail"}); code != 200 {
		t.Fatal(code, raw)
	}
	if len(e.f.revoked) != 1 || e.f.revoked[0] != rt {
		t.Fatal(e.f.revoked)
	}
	if v, _ := e.kc.Get(context.Background(), "crewbase/google-oauth"); v != "" {
		t.Fatal("keychain item left")
	}
	if v := e.status(t); v.Connected || v.Email != "" {
		t.Fatalf("%+v", v)
	}
	if _, ok := e.reg.Get("", "gmail_search"); ok {
		t.Fatal("tools after signing out")
	}
}

func TestRevokedGrantNeedsSignIn(t *testing.T) {
	e := newEnv(t)
	e.connect(t, "gmail")
	e.f.mu.Lock()
	e.f.rejectAll = true
	e.f.mu.Unlock()
	e.s.expire()
	res, _ := e.run(t, "gmail_search", map[string]any{})
	if !res.IsError || !strings.Contains(res.Content, "connect Google again") {
		t.Fatal(res.Content)
	}
	if v := e.status(t); !v.NeedsLogin || !v.Connected {
		t.Fatalf("%+v", v)
	}
	if st := (connector{e.s}).State("gmail"); !st.NeedsLogin || !st.Installed {
		t.Fatalf("%+v", st)
	}
	// no further calls to Google until the person signs in again
	n := e.f.called("POST /token")
	e.run(t, "gmail_search", map[string]any{})
	if e.f.called("POST /token") != n {
		t.Fatal("kept refreshing")
	}
	e.f.mu.Lock()
	e.f.rejectAll = false
	e.f.mu.Unlock()
	e.connect(t, "gmail")
	if v := e.status(t); v.NeedsLogin {
		t.Fatalf("%+v", v)
	}
	if res, _ := e.run(t, "gmail_search", map[string]any{}); res.IsError {
		t.Fatal(res.Content)
	}
}

func TestUntickedScopesAreNotConnected(t *testing.T) {
	e := newEnv(t)
	e.f.grantLess = []string{scopePrefix + "gmail.compose"}
	if end := e.connect(t, "gmail"); end.Fragment != "/settings/market?google=failed" {
		t.Fatal(end)
	}
	v := e.status(t)
	if e.product(v, "gmail").Connected || v.LastError == "" {
		t.Fatalf("%+v", v)
	}
}

func TestDeclinedSignIn(t *testing.T) {
	e := newEnv(t)
	u, err := e.s.Connect("drive", "")
	if err != nil {
		t.Fatal(err)
	}
	st, _ := url.Parse(u)
	if err := e.s.Finish(context.Background(), st.Query().Get("state"), "", "access_denied"); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if err := e.s.Finish(context.Background(), st.Query().Get("state"), "x", ""); !errors.Is(err, ErrState) {
		t.Fatal("state reused", err)
	}
}

// The redirect URL pasted from another computer completes the sign-in.
func TestFinishFromPastedURL(t *testing.T) {
	e := newEnv(t)
	code, raw := e.do(t, "POST", "/api/google/connect", map[string]string{"product": "docs"})
	var r struct{ AuthURL string }
	_ = json.Unmarshal([]byte(raw), &r)
	back := e.f.consent(r.AuthURL)
	if code, raw = e.do(t, "POST", "/api/google/finish", map[string]string{"url": back}); code != 200 {
		t.Fatal(code, raw)
	}
	if !e.product(e.status(t), "docs").Connected {
		t.Fatal("docs not connected")
	}
}

// ---------------------------------------------------------------- tools

func TestToolCategoriesAndSummaries(t *testing.T) {
	e := newEnv(t)
	for _, p := range Products {
		e.connect(t, p.ID)
	}
	cases := []struct {
		tool string
		args map[string]any
		want string // category|summary prefix
	}{
		{"gmail_search", map[string]any{"q": "is:unread"}, "read|搜索邮件：is:unread"},
		{"gmail_read", map[string]any{"id": "m1"}, "read|"},
		{"gmail_draft", map[string]any{"to": "a@b.com", "subject": "Hi", "body": "x"}, "write|起草邮件给 a@b.com：主题「Hi」"},
		{"gmail_send", map[string]any{"to": "a@b.com", "subject": "Hi", "body": "x"}, "publish|发送邮件给 a@b.com：主题「Hi」"},
		{"gmail_send", map[string]any{"draft_id": "d1"}, "publish|发送邮件给 bob@example.com：主题「Plan」"},
		{"calendar_list_events", map[string]any{}, "read|"},
		{"calendar_free_busy", map[string]any{"time_min": "2026-11-16T00:00:00Z", "time_max": "2026-11-17T00:00:00Z"}, "read|"},
		{"calendar_create_event", map[string]any{"summary": "Focus", "start": "2026-11-16T10:00:00+08:00", "end": "2026-11-16T11:00:00+08:00"}, "write|创建日程「Focus」"},
		{"calendar_create_event", map[string]any{"summary": "Sync", "start": "2026-11-16", "end": "2026-11-17", "attendees": []string{"ann@example.com"}}, "publish|创建日程「Sync」2026-11-16，并邀请 ann@example.com"},
		{"calendar_update_event", map[string]any{"id": "e3", "summary": "Deep work"}, "write|修改日程 e3「Deep work」"},
		{"calendar_update_event", map[string]any{"id": "e1", "summary": "Standup"}, "publish|修改日程 e1「Standup」（会通知已邀请的人）"},
		{"calendar_delete_event", map[string]any{"id": "e1"}, "delete|删除日程 e1"},
		{"calendar_rsvp", map[string]any{"id": "e1", "response": "accepted"}, "publish|回复日程邀请 e1：接受"},
		{"drive_search", map[string]any{"q": "budget"}, "read|"},
		{"drive_read", map[string]any{"id": "f1"}, "read|"},
		{"drive_share", map[string]any{"id": "f1", "emails": []string{"ann@example.com"}}, "publish|共享云端硬盘文件 f1 给 ann@example.com（reader）"},
		{"docs_read", map[string]any{"id": "doc1"}, "read|"},
		{"docs_create", map[string]any{"title": "Notes", "text": "hi"}, "write|新建文档「Notes」"},
		{"docs_append", map[string]any{"id": "doc1", "text": "more"}, "write|"},
		{"docs_replace", map[string]any{"id": "doc1", "find": "a", "replace": "b"}, "write|"},
		{"sheets_read", map[string]any{"id": "s1", "range": "Sheet1!A1:B2"}, "read|"},
		{"sheets_write", map[string]any{"id": "s1", "range": "Sheet1!A1", "values": [][]any{{"a", 1}}}, "write|"},
		{"sheets_append", map[string]any{"id": "s1", "range": "Sheet1", "values": [][]any{{"b", 2}}}, "write|"},
		{"slides_read", map[string]any{"id": "p1"}, "read|"},
		{"slides_create", map[string]any{"title": "Deck", "slides": []map[string]string{{"title": "One", "body": "a\nb"}}}, "write|新建演示文稿「Deck」"},
	}
	for _, c := range cases {
		res, got := e.run(t, c.tool, c.args)
		if !strings.HasPrefix(got, c.want) {
			t.Errorf("%s: classify %q, want prefix %q", c.tool, got, c.want)
		}
		if res.IsError {
			t.Errorf("%s: %s", c.tool, res.Content)
		}
	}
	// every tool of every product is registered with a category
	for _, p := range Products {
		for _, tl := range p.tools(e.s) {
			if got, ok := e.reg.Get("", tl.Name); !ok || got.Category == "" || got.Description == "" {
				t.Errorf("%s not registered properly", tl.Name)
			}
		}
	}
	if tl, _ := e.reg.Get("", "drive_upload"); tl.Category != tools.CategoryWrite || len(tl.PathArgs) != 1 {
		t.Fatalf("%+v", tl)
	}
}

func TestToolOutputs(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"gmail", "drive"} {
		e.connect(t, p)
	}
	// rate-limited once: backs off and retries
	e.f.mu.Lock()
	e.f.fail429["/gmail/v1/users/me/messages"] = 1
	e.f.mu.Unlock()
	res, _ := e.run(t, "gmail_search", map[string]any{"q": "from:ann"})
	if res.IsError || !strings.Contains(res.Content, "id=m1") || !strings.Contains(res.Content, "Subject: 你好") || !strings.Contains(res.Content, "Hello & welcome") {
		t.Fatal(res.Content)
	}
	res, _ = e.run(t, "gmail_read", map[string]any{"id": "m1"})
	if strings.Contains(res.Content, "<p>") || strings.Contains(res.Content, "evil") || !strings.Contains(res.Content, "Hello & welcome") ||
		!strings.Contains(res.Content, "- one") || !strings.Contains(res.Content, "invoice.pdf (application/pdf, 51 KB)") {
		t.Fatal(res.Content)
	}
	e.f.bigBody = true
	res, _ = e.run(t, "gmail_read", map[string]any{"id": "m2"})
	if n := len([]rune(res.Content)); n > maxBody+1000 || !strings.Contains(res.Content, "[truncated at 20000 characters") {
		t.Fatal(n)
	}

	// sending: a plain-text RFC 5322 message, subject encoded, threaded reply
	res, _ = e.run(t, "gmail_send", map[string]any{"reply_to_id": "m1", "body": "Thanks!"})
	if res.IsError {
		t.Fatal(res.Content)
	}
	var sent struct {
		Raw      string `json:"raw"`
		ThreadID string `json:"threadId"`
	}
	_ = json.Unmarshal([]byte(e.f.bodies["POST /gmail/v1/users/me/messages/send"]), &sent)
	msg, _ := base64.URLEncoding.DecodeString(sent.Raw)
	if sent.ThreadID != "t-m1" || !strings.Contains(string(msg), "To: \"Ann\" <ann@example.com>") ||
		!strings.Contains(string(msg), "In-Reply-To: <orig@example.com>") || !strings.Contains(string(msg), "Subject: =?UTF-8?b?") {
		t.Fatalf("%s %s", sent.ThreadID, msg)
	}
	// header injection through an address is refused
	if res, _ := e.run(t, "gmail_send", map[string]any{"to": "a@b.com\nBcc: x@evil.com", "body": "x"}); !res.IsError {
		t.Fatal("injected header accepted")
	}

	// Drive: Google Sheets export as CSV; binary files give metadata only
	if res, _ := e.run(t, "drive_read", map[string]any{"id": "f1"}); !strings.Contains(res.Content, "a,b\n1,2") {
		t.Fatal(res.Content)
	}
	if res, _ := e.run(t, "drive_read", map[string]any{"id": "bin"}); !strings.Contains(res.Content, "binary file (image/jpeg, 2.0 MB)") {
		t.Fatal(res.Content)
	}
	if err := os.WriteFile(filepath.Join(e.dir, "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if res, _ := e.run(t, "drive_upload", map[string]any{"path": "notes.txt"}); res.IsError || !strings.Contains(res.Content, "id=up1") {
		t.Fatal(res.Content)
	}
	up := e.f.bodies["POST /upload/drive/v3/files"]
	if !strings.Contains(up, `"name":"notes.txt"`) || !strings.Contains(up, "hello") {
		t.Fatal(up)
	}
}

// A product shared with one agent is visible to that agent only.
func TestAgentScopedProduct(t *testing.T) {
	e := newEnv(t)
	u, err := e.s.Connect("sheets", "kai")
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.httpc.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if _, ok := e.reg.Get("kai", "sheets_read"); !ok {
		t.Fatal("kai cannot see it")
	}
	if _, ok := e.reg.Get("nova", "sheets_read"); ok {
		t.Fatal("nova can see it")
	}
	if p := e.product(e.s.Status(), "sheets"); p.Scope != "kai" {
		t.Fatalf("%+v", p)
	}
}

var _ catalog.Connector = connector{}
