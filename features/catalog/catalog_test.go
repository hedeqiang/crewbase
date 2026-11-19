package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/features/mcp"
	"github.com/hedeqiang/crewbase/features/skills"
	"github.com/hedeqiang/crewbase/kernel"
)

// The embedded catalog must be complete and every preset must turn into a
// valid server config without leaking a secret outside the keychain.
func TestBuiltinCatalogIsValid(t *testing.T) {
	cats := map[string]bool{}
	for _, c := range builtin.Categories {
		if c.Title.Zh == "" || c.Title.En == "" || c.Desc.Zh == "" || c.Desc.En == "" {
			t.Errorf("category %s: missing text", c.ID)
		}
		cats[c.ID] = true
	}
	ids := map[string]bool{}
	for _, p := range builtin.Presets {
		if ids[p.ID] {
			t.Errorf("duplicate id %s", p.ID)
		}
		ids[p.ID] = true
		if !cats[p.Category] {
			t.Errorf("%s: unknown category %q", p.ID, p.Category)
		}
		if p.Name == "" || p.Desc.Zh == "" || p.Desc.En == "" {
			t.Errorf("%s: missing name or description", p.ID)
		}
		if u, err := url.Parse(p.Docs); err != nil || u.Scheme != "https" {
			t.Errorf("%s: docs link %q", p.ID, p.Docs)
		}
		if !slices.Contains([]string{"none", "oauth", "token", "setup"}, p.Auth) {
			t.Errorf("%s: auth %q", p.ID, p.Auth)
		}
		if (p.Auth == "oauth") != p.OAuth {
			t.Errorf("%s: auth %q but oauth=%v", p.ID, p.Auth, p.OAuth)
		}
		in := map[string]string{}
		for _, x := range p.Inputs {
			if x.Label.Zh == "" || x.Label.En == "" {
				t.Errorf("%s/%s: missing label", p.ID, x.ID)
			}
			in[x.ID] = "/tmp/secret-" + x.ID
		}
		srv, secrets, err := p.build(in, "/srv")
		if err != nil {
			t.Errorf("%s: build: %v", p.ID, err)
			continue
		}
		if _, err := srv.Normalize(); err != nil {
			t.Errorf("%s: %v", p.ID, err)
		}
		b, _ := json.Marshal(srv)
		for ref, v := range secrets {
			if strings.Contains(string(b), v) {
				t.Errorf("%s: secret value in config: %s", p.ID, b)
			}
			if !strings.Contains(string(b), ref) {
				t.Errorf("%s: secret ref %s not used: %s", p.ID, ref, b)
			}
		}
		for _, a := range srv.Command {
			if strings.Contains(a, "{") || strings.Contains(a, "keychain:") {
				t.Errorf("%s: command arg %q (secrets must not be argv)", p.ID, a)
			}
		}
		// optional inputs left empty drop what they appear in
		opt := map[string]string{}
		for _, x := range p.Inputs {
			if !x.Optional {
				opt[x.ID] = "v"
			}
		}
		srv, _, err = p.build(opt, "/srv")
		if err != nil {
			t.Errorf("%s: build without optional inputs: %v", p.ID, err)
		}
		b, _ = json.Marshal(srv)
		for _, x := range p.Inputs {
			if strings.Contains(string(b), "{"+x.ID+"}") {
				t.Errorf("%s: unfilled placeholder: %s", p.ID, b)
			}
		}
	}
	if len(builtin.Skills) < 6 {
		t.Fatalf("only %d skills", len(builtin.Skills))
	}
	for _, sk := range builtin.Skills {
		files, err := skillFiles(sk.Name)
		if err != nil || files["SKILL.md"] == nil {
			t.Fatalf("%s: %v %v", sk.Name, err, files)
		}
		head := string(files["SKILL.md"])
		if !strings.HasPrefix(head, "---\nname: "+sk.Name+"\ndescription: ") {
			t.Errorf("%s: front matter: %.80q", sk.Name, head)
		}
		if sk.Desc.Zh == "" || sk.Desc.En == "" {
			t.Errorf("%s: missing description", sk.Name)
		}
	}
}

func TestBuildFillsInputs(t *testing.T) {
	p := Preset{Server: "x", Transport: "stdio", Command: []string{"run", "--tz={tz}", "{dir}"},
		Env:    map[string]string{"KEY": "{key}", "AUTH": "Token {key}", "TZ": "{tz}"},
		Inputs: []Input{{ID: "key", Secret: true}, {ID: "dir"}, {ID: "tz", Optional: true}}}
	if _, _, err := p.build(map[string]string{"key": "k"}, ""); err == nil {
		t.Fatal("required dir missing")
	}
	srv, secrets, err := p.build(map[string]string{"key": " s3cr3t ", "dir": "/d"}, "")
	if err != nil {
		t.Fatal(err)
	}
	ref := mcp.SecretRef("x", "key")
	if !slices.Equal(srv.Command, []string{"run", "/d"}) || srv.Env["KEY"] != ref || srv.Env["AUTH"] != "Token ${"+ref+"}" {
		t.Fatalf("%+v", srv)
	}
	if _, ok := srv.Env["TZ"]; ok || secrets[ref] != "s3cr3t" || len(secrets) != 1 {
		t.Fatalf("%+v %v", srv.Env, secrets)
	}
}

// ---------------------------------------------------------------- install

type env struct {
	h     *kernel.Host
	base  string
	creds *credentials.Service
	mcp   *mcp.Service
	reg   *agent.Registry
	dir   string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	addr := make(chan string, 1)
	gateway.Listening.Listen(h.Root(), func(a string) { addr <- a })
	dir := t.TempDir()
	for _, l := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, nil}, {prompt.Plugin, nil}, {tools.Plugin, nil}, {credentials.Plugin, nil},
		{agent.Plugin, map[string]any{"root": dir}},
		{gateway.Plugin, map[string]any{"addr": "127.0.0.1:0", "token": "tok"}},
		{mcp.Plugin, map[string]any{"dir": dir, "backoffMin": "50ms", "backoffMax": "200ms"}},
		{skills.Plugin, map[string]any{"dirs": []string{filepath.Join(dir, "skills")}}},
		{skills.APIPlugin, nil}, {Plugin, nil}, {APIPlugin, nil},
	} {
		if _, err := h.Load(l.s, kernel.LoadOptions{Config: l.cfg}); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"@crewbase/catalog-api", "@crewbase/skills-api"} {
		if info, _ := h.Plugin(n); info.State != kernel.StateActive {
			t.Fatalf("%+v", info)
		}
	}
	r := h.Root()
	cs := kernel.Use[*credentials.Service](r, credentials.ServiceKey)
	mb := credentials.NewMemory() // the vault and its old name, in memory
	cs.SetBackend("vault", mb)
	cs.SetBackend("keychain", mb)
	return &env{h: h, base: "http://" + <-addr, creds: cs, mcp: kernel.Use[*mcp.Service](r, mcp.ServiceKey),
		reg: kernel.Use[*agent.Registry](r, agent.ServiceKey), dir: dir}
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

func (e *env) listing(t *testing.T) map[string]Item {
	t.Helper()
	code, raw := e.do(t, "GET", "/api/catalog", nil)
	if code != 200 {
		t.Fatal(code, raw)
	}
	var l Listing
	if err := json.Unmarshal([]byte(raw), &l); err != nil {
		t.Fatal(err)
	}
	out := map[string]Item{}
	for _, it := range l.Items {
		out[it.ID] = it
	}
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// withPresets swaps the catalog's presets for the test.
func withPresets(t *testing.T, ps ...Preset) {
	prev := builtin.Presets
	builtin.Presets = ps
	t.Cleanup(func() { builtin.Presets = prev })
}

// A remote preset that needs a pasted token: the token goes to the
// keychain, config holds the reference, the server connects with it and
// its tools are listed; uninstalling removes the server and the secret.
func TestInstallTokenPresetOverHTTP(t *testing.T) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "catalog-test", Version: "1"}, nil)
	srv.AddTool(&sdk.Tool{Name: "lookup", Description: "Look something up.", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
		})
	h := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer pasted-token-123" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	withPresets(t, Preset{ID: "remote", Category: "dev", Name: "Remote", Auth: "token", Docs: "https://example.com",
		Server: "remote", Transport: "http", URL: ts.URL, Headers: map[string]string{"Authorization": "Bearer {token}"},
		Inputs: []Input{{ID: "token", Secret: true}}})

	e := newEnv(t)
	if it := e.listing(t)["remote"]; it.Installed || len(it.Inputs) != 1 {
		t.Fatalf("%+v", it)
	}
	if code, raw := e.do(t, "POST", "/api/catalog/remote/install", Request{}); code != 400 {
		t.Fatal("token is required", code, raw)
	}
	if code, _ := e.do(t, "POST", "/api/catalog/nope/install", Request{}); code != 404 {
		t.Fatal(code)
	}
	if code, _ := e.do(t, "POST", "/api/catalog/remote/install", Request{Inputs: map[string]string{"token": "x"}, Scope: "ghost"}); code != 400 {
		t.Fatal("unknown agent scope", code)
	}
	code, raw := e.do(t, "POST", "/api/catalog/remote/install", Request{Inputs: map[string]string{"token": "pasted-token-123"}})
	if code != 200 || strings.Contains(raw, "pasted-token-123") {
		t.Fatal(code, raw)
	}
	ref := mcp.SecretRef("remote", "token")
	if v, err := e.creds.Resolve(context.Background(), ref); err != nil || v != "pasted-token-123" {
		t.Fatal(v, err)
	}
	waitFor(t, "connected", func() bool { st, _ := e.mcp.Status("remote"); return st.State == mcp.StateReady })
	list, _ := e.mcp.Tools("remote")
	if len(list) != 1 || list[0].Name != "remote__lookup" {
		t.Fatalf("%+v", list)
	}
	b, _ := os.ReadFile(filepath.Join(e.dir, "mcp.json"))
	if strings.Contains(string(b), "pasted-token-123") || !strings.Contains(string(b), ref) {
		t.Fatal(string(b))
	}
	if it := e.listing(t)["remote"]; !it.Installed || it.Server != "remote" || it.Scope != mcp.ScopeGlobal {
		t.Fatalf("%+v", it)
	}
	if code, _ := e.do(t, "POST", "/api/catalog/remote/install", Request{Inputs: map[string]string{"token": "x"}}); code != 409 {
		t.Fatal("second install", code)
	}
	if code, raw := e.do(t, "DELETE", "/api/catalog/remote", nil); code >= 300 {
		t.Fatal(code, raw)
	}
	if _, ok := e.mcp.Get("remote"); ok {
		t.Fatal("server kept")
	}
	if _, err := e.creds.Resolve(context.Background(), ref); err == nil {
		t.Fatal("secret kept after uninstall")
	}
	if code, _ := e.do(t, "DELETE", "/api/catalog/remote", nil); code != 404 {
		t.Fatal(code)
	}
}

// Removing a server (DELETE /api/mcp/{name}) also drops the secrets its preset
// stored, but never a reference the person typed themselves.
func TestRemoveViaMCPDropsOwnSecretsOnly(t *testing.T) {
	withPresets(t, Preset{ID: "p", Category: "dev", Name: "P", Auth: "token", Docs: "https://example.com",
		Server: "p", Transport: "http", URL: "http://127.0.0.1:1/mcp", Headers: map[string]string{"X-Key": "{key}"},
		Inputs: []Input{{ID: "key", Secret: true}}})
	e := newEnv(t)
	if _, err := (&Service{c: e.h.Root()}).Install("p", Request{Inputs: map[string]string{"key": "abc123"}}); err != nil {
		t.Fatal(err)
	}
	e.creds.Set("keychain:crewbase/shared", "keep-me")
	e.mcp.Update("p", func(s *mcp.Server) { s.Headers["X-Other"] = "keychain:crewbase/shared" })
	if err := e.mcp.Remove("p"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.creds.Resolve(context.Background(), mcp.SecretRef("p", "key")); err == nil {
		t.Fatal("own secret kept")
	}
	if v, _ := e.creds.Resolve(context.Background(), "keychain:crewbase/shared"); v != "keep-me" {
		t.Fatal("shared secret deleted")
	}
}

// Parts of one server (e.g. a suite of products): the first part creates the
// server with the inputs, later parts only extend its command, removing
// the last part removes the server.
func TestPartsShareOneServer(t *testing.T) {
	base := Preset{Category: "dev", Auth: "setup", Docs: "https://example.com", Server: "suite", Transport: "stdio",
		Command: []string{"/nonexistent/suite", "--tools", "{parts}"}, Env: map[string]string{"ID": "{id}"},
		Inputs: []Input{{ID: "id"}}}
	mail, cal := base, base
	mail.ID, mail.Name, mail.Part = "mail", "Mail", "mail"
	cal.ID, cal.Name, cal.Part = "cal", "Cal", "calendar"
	withPresets(t, mail, cal)
	e := newEnv(t)
	if code, raw := e.do(t, "POST", "/api/catalog/mail/install", Request{Inputs: map[string]string{"id": "client-1"}}); code != 200 {
		t.Fatal(code, raw)
	}
	items := e.listing(t)
	if !items["mail"].Installed || items["cal"].Installed || len(items["cal"].Inputs) != 0 {
		t.Fatalf("%+v", items)
	}
	if code, raw := e.do(t, "POST", "/api/catalog/cal/install", Request{}); code != 200 {
		t.Fatal(code, raw)
	}
	v, _ := e.mcp.Get("suite")
	if !slices.Equal(v.Command, []string{"/nonexistent/suite", "--tools", "mail", "calendar"}) || v.Env["ID"] != mcp.Masked {
		t.Fatalf("%+v", v.Server)
	}
	if code, _ := e.do(t, "DELETE", "/api/catalog/mail", nil); code >= 300 {
		t.Fatal(code)
	}
	v, _ = e.mcp.Get("suite")
	if !slices.Equal(v.Command, []string{"/nonexistent/suite", "--tools", "calendar"}) {
		t.Fatal(v.Command)
	}
	if code, _ := e.do(t, "DELETE", "/api/catalog/cal", nil); code >= 300 {
		t.Fatal(code)
	}
	if _, ok := e.mcp.Get("suite"); ok {
		t.Fatal("last part removed: server must go")
	}
}

func TestSkillInstallAndRemove(t *testing.T) {
	e := newEnv(t)
	a, err := e.reg.Create(agent.Spec{Name: "Kai"})
	if err != nil {
		t.Fatal(err)
	}
	if code, raw := e.do(t, "POST", "/api/catalog/skill-code-review/install", Request{}); code != 200 {
		t.Fatal(code, raw)
	}
	b, err := os.ReadFile(filepath.Join(e.dir, "skills", "code-review", "SKILL.md"))
	if err != nil || !strings.Contains(string(b), "name: code-review") {
		t.Fatal(err)
	}
	if code, _ := e.do(t, "POST", "/api/catalog/skill-code-review/install", Request{}); code != 409 {
		t.Fatal(code)
	}
	if code, _ := e.do(t, "POST", "/api/catalog/skill-weekly-report/install", Request{Scope: "team:x"}); code != 400 {
		t.Fatal("team scope for a skill", code)
	}
	if code, raw := e.do(t, "POST", "/api/catalog/skill-weekly-report/install", Request{Scope: a.ID()}); code != 200 {
		t.Fatal(code, raw)
	}
	if _, err := os.Stat(filepath.Join(a.Workspace(), "skills", "weekly-report", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	// the agent's prompt catalog sees both
	sys := kernel.Use[*prompt.Assembler](e.h.Root(), prompt.ServiceKey).Assemble(a.ID())
	if !strings.Contains(sys, "- code-review: ") || !strings.Contains(sys, "- weekly-report: ") {
		t.Fatal(sys)
	}
	items := e.listing(t)
	if it := items["skill-weekly-report"]; !it.Installed || it.Scope != a.ID() || it.Kind != "skill" {
		t.Fatalf("%+v", it)
	}
	if items["skill-release-notes"].Installed {
		t.Fatal("not installed")
	}
	code, raw := e.do(t, "GET", "/api/skills", nil)
	if code != 200 || !strings.Contains(raw, `"agent":"`+a.ID()+`"`) || !strings.Contains(raw, `"name":"code-review"`) {
		t.Fatal(code, raw)
	}
	// removal is per target
	if code, _ := e.do(t, "DELETE", "/api/catalog/skill-weekly-report", nil); code != 404 {
		t.Fatal("not in the shared dir", code)
	}
	if code, _ := e.do(t, "DELETE", "/api/catalog/skill-weekly-report?agent="+a.ID(), nil); code >= 300 {
		t.Fatal(code)
	}
	if code, _ := e.do(t, "DELETE", "/api/skills/code-review", nil); code >= 300 {
		t.Fatal(code)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "skills", "code-review")); !os.IsNotExist(err) {
		t.Fatal("skill folder kept", err)
	}
	if code, _ := e.do(t, "DELETE", "/api/skills/code-review", nil); code != 404 {
		t.Fatal(code)
	}
}
