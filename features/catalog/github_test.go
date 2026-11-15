package catalog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hedeqiang/crewbase/features/mcp"
	"github.com/hedeqiang/crewbase/kernel"
)

// fakeGitHub serves GitHub's device flow endpoints, /user/installations and
// an MCP server that accepts only the current access token.
type fakeGitHub struct {
	t        *testing.T
	ts       *httptest.Server
	mu       sync.Mutex
	polls    int
	deny     bool
	token    string // current valid access token
	refresh  string
	expires  int // expires_in in answers (0 = token does not expire)
	gotScope []string
	slowAt   map[int]bool
	busy     int // answer this many refreshes with 503
}

// firstToken is the access token the fake hands out first.
const firstToken = "ghu_first"

func newFakeGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{t: t, token: firstToken, refresh: "ghr_first", slowAt: map[int]bool{2: true}}
	srv := sdk.NewServer(&sdk.Implementation{Name: "gh", Version: "1"}, nil)
	srv.AddTool(&sdk.Tool{Name: "get_me", Description: "Me.", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "octocat"}}}, nil
		})
	mcpH := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil)
	answer := func(w http.ResponseWriter, v map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/device/code", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		f.gotScope = append(f.gotScope, r.Form.Get("scope"))
		f.mu.Unlock()
		if r.Header.Get("Accept") != "application/json" || r.Form.Get("client_id") != "Iv1.test" {
			answer(w, map[string]any{"error": "device_flow_disabled", "error_description": "Device Flow must be explicitly enabled for this App"})
			return
		}
		answer(w, map[string]any{"device_code": "dev-123", "user_code": "WDJB-MJHT", "verification_uri": f.ts.URL + "/login/device",
			"expires_in": 900, "interval": 1})
	})
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Form.Get("client_id") != "Iv1.test" || r.Form.Get("client_secret") != "" {
			answer(w, map[string]any{"error": "incorrect_client_credentials"})
			return
		}
		switch r.Form.Get("grant_type") {
		case "urn:ietf:params:oauth:grant-type:device_code":
			if r.Form.Get("device_code") != "dev-123" {
				answer(w, map[string]any{"error": "incorrect_device_code"})
				return
			}
			f.polls++
			switch {
			case f.deny:
				answer(w, map[string]any{"error": "access_denied", "error_description": "The authorization request was denied."})
			case f.slowAt[f.polls]:
				answer(w, map[string]any{"error": "slow_down", "interval": 2})
			case f.polls < 3:
				answer(w, map[string]any{"error": "authorization_pending"})
			default:
				v := map[string]any{"access_token": f.token, "token_type": "bearer", "scope": ""}
				if f.expires > 0 {
					v["expires_in"], v["refresh_token"], v["refresh_token_expires_in"] = f.expires, f.refresh, 15897600
				}
				answer(w, v)
			}
		case "refresh_token":
			if f.busy > 0 {
				f.busy--
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"message":"try later"}`))
				return
			}
			if r.Form.Get("refresh_token") != f.refresh {
				answer(w, map[string]any{"error": "bad_refresh_token"})
				return
			}
			f.token, f.refresh = "ghu_second", "ghr_second"
			answer(w, map[string]any{"access_token": f.token, "token_type": "bearer", "expires_in": 28800, "refresh_token": f.refresh})
		default:
			answer(w, map[string]any{"error": "unsupported_grant_type"})
		}
	})
	mux.HandleFunc("GET /user/installations", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+f.current() {
			http.Error(w, "unauthorized", 401)
			return
		}
		answer(w, map[string]any{"total_count": 0, "installations": []any{}})
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+f.current() {
			http.Error(w, "unauthorized", 401)
			return
		}
		mcpH.ServeHTTP(w, r)
	})
	f.ts = httptest.NewServer(mux)
	t.Cleanup(f.ts.Close)
	return f
}

func (f *fakeGitHub) current() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.token
}

func githubPreset(url string) Preset {
	return Preset{ID: "github", Category: "dev", Name: "GitHub", Auth: "token", SignIn: "github", Docs: "https://example.com",
		Server: "github", Transport: "http", URL: url, Headers: map[string]string{"Authorization": "Bearer {token}"},
		Inputs: []Input{{ID: "token", Secret: true}}}
}

func (e *env) useFake(t *testing.T, f *fakeGitHub, clientID, slug string) *githubSignIn {
	t.Helper()
	s := kernel.Use[*Service](e.h.Root(), ServiceKey)
	g := newGitHubSignIn(s, Config{GitHubClientID: clientID, GitHubAppSlug: slug, GitHubURL: f.ts.URL, GitHubAPIURL: f.ts.URL})
	g.unit = 10 * time.Millisecond
	s.gh = g
	return g
}

func (e *env) signIn(t *testing.T, want string) SignInView {
	t.Helper()
	var v SignInView
	waitFor(t, "sign-in "+want, func() bool {
		code, raw := e.do(t, "GET", "/api/catalog/github/signin", nil)
		if code != 200 {
			t.Fatal(code, raw)
		}
		_ = json.Unmarshal([]byte(raw), &v)
		return v.State != "pending"
	})
	if v.State != want {
		t.Fatalf("%+v", v)
	}
	return v
}

// The device flow: code shown, pending and slow_down honoured, the token
// stored in the keychain (config holds the reference), the server created
// and connected, the refresh token kept and used before expiry; removing
// the server deletes all of it.
func TestGitHubDeviceFlow(t *testing.T) {
	t.Setenv("CREWBASE_GITHUB_CLIENT_ID", "")
	f := newFakeGitHub(t)
	f.expires = 28800
	withPresets(t, githubPreset(f.ts.URL+"/mcp"))
	e := newEnv(t)
	e.useFake(t, f, "Iv1.test", "crewbase")

	if it := e.listing(t)["github"]; it.Auth != "device" || it.SignIn != "github" || len(it.Inputs) != 1 {
		t.Fatalf("%+v", it)
	}
	if code, _ := e.do(t, "GET", "/api/catalog/github/signin", nil); code != 404 {
		t.Fatal("no sign-in yet", code)
	}
	code, raw := e.do(t, "POST", "/api/catalog/github/signin", Request{})
	if code != 200 {
		t.Fatal(code, raw)
	}
	var v SignInView
	_ = json.Unmarshal([]byte(raw), &v)
	if v.State != "pending" || v.UserCode != "WDJB-MJHT" || !strings.HasSuffix(v.VerificationURI, "/login/device") || v.ExpiresAt.IsZero() {
		t.Fatalf("%+v", v)
	}
	if f.gotScope[0] != "" {
		t.Fatal("a GitHub App client asks no scope", f.gotScope)
	}
	v = e.signIn(t, "done")
	if v.Server != "github" || !v.NoInstallation || v.InstallURL != f.ts.URL+"/apps/crewbase/installations/new" {
		t.Fatalf("%+v", v)
	}
	if f.polls < 3 {
		t.Fatal("polls", f.polls)
	}
	ref := mcp.SecretRef("github", "token")
	if tok, _ := e.creds.Resolve(context.Background(), ref); tok != firstToken {
		t.Fatal(tok)
	}
	sv, _ := e.mcp.Get("github")
	if sv.Headers["Authorization"] != "Bearer ${"+ref+"}" || sv.Scope != mcp.ScopeGlobal {
		t.Fatalf("%+v", sv.Server)
	}
	waitFor(t, "connected", func() bool { st, _ := e.mcp.Status("github"); return st.State == mcp.StateReady })
	raw2, _ := e.creds.Resolve(context.Background(), mcp.SignInRef("github"))
	if !strings.Contains(raw2, "ghr_first") {
		t.Fatal(raw2)
	}

	// not due yet: nothing happens
	g := kernel.Use[*Service](e.h.Root(), ServiceKey).gh
	g.refreshDue(context.Background())
	if f.current() != firstToken {
		t.Fatal("refreshed too early")
	}
	// due, but GitHub is busy: the refresh token is kept for the next tick
	g.refreshBefore = 9 * time.Hour
	f.mu.Lock()
	f.busy = 1
	f.mu.Unlock()
	g.refreshDue(context.Background())
	if raw, _ := e.creds.Resolve(context.Background(), mcp.SignInRef("github")); !strings.Contains(raw, "ghr_first") {
		t.Fatalf("refresh token dropped after a 503: %q", raw)
	}
	// due: refreshed without a client secret, stored, reconnected
	g.refreshDue(context.Background())
	if tok, _ := e.creds.Resolve(context.Background(), ref); tok != "ghu_second" {
		t.Fatal(tok)
	}
	waitFor(t, "reconnected", func() bool { st, _ := e.mcp.Status("github"); return st.State == mcp.StateReady })
	if it := e.listing(t)["github"]; !it.Installed {
		t.Fatalf("%+v", it)
	}

	if code, raw := e.do(t, "DELETE", "/api/catalog/github", nil); code >= 300 {
		t.Fatal(code, raw)
	}
	for _, r := range []string{ref, mcp.SignInRef("github")} {
		if _, err := e.creds.Resolve(context.Background(), r); err == nil {
			t.Fatal("kept after disconnect:", r)
		}
	}
}

// Signing in again replaces the token of the existing server; a declined
// sign-in reports "denied"; an OAuth App client asks for the scopes.
func TestGitHubDeviceFlowAgainAndDenied(t *testing.T) {
	t.Setenv("CREWBASE_GITHUB_CLIENT_ID", "")
	f := newFakeGitHub(t)
	withPresets(t, githubPreset(f.ts.URL+"/mcp"))
	e := newEnv(t)
	e.useFake(t, f, "Iv1.test", "")
	if _, err := (&Service{c: e.h.Root()}).Install("github", Request{Inputs: map[string]string{"token": "stale"}}); err != nil {
		t.Fatal(err)
	}
	if code, raw := e.do(t, "POST", "/api/catalog/github/signin", Request{}); code != 200 {
		t.Fatal(code, raw)
	}
	if v := e.signIn(t, "done"); v.InstallURL != "" || !v.NoInstallation {
		t.Fatalf("no slug, no link: %+v", v)
	}
	if tok, _ := e.creds.Resolve(context.Background(), mcp.SecretRef("github", "token")); tok != firstToken {
		t.Fatal(tok)
	}
	waitFor(t, "connected", func() bool { st, _ := e.mcp.Status("github"); return st.State == mcp.StateReady })
	if _, err := e.creds.Resolve(context.Background(), mcp.SignInRef("github")); err == nil {
		t.Fatal("a token that does not expire keeps no refresh state")
	}

	f.mu.Lock()
	f.deny, f.polls = true, 0
	f.mu.Unlock()
	if code, _ := e.do(t, "POST", "/api/catalog/github/signin", Request{}); code != 200 {
		t.Fatal(code)
	}
	if v := e.signIn(t, "denied"); !strings.Contains(v.Error, "denied") {
		t.Fatalf("%+v", v)
	}
	if code, _ := e.do(t, "DELETE", "/api/catalog/github/signin", nil); code >= 300 {
		t.Fatal(code)
	}

	// a classic OAuth App client asks for the scopes (the fake rejects it)
	g := kernel.Use[*Service](e.h.Root(), ServiceKey).gh
	g.clientID = "Ov23classic"
	if _, err := g.Start("github", ""); err == nil || !strings.Contains(err.Error(), "device_flow_disabled") {
		t.Fatal(err)
	}
	f.mu.Lock()
	last := f.gotScope[len(f.gotScope)-1]
	f.mu.Unlock()
	if last != githubScope {
		t.Fatal(f.gotScope)
	}
	// and the API reports GitHub's refusal for the dialog
	if code, raw := e.do(t, "POST", "/api/catalog/github/signin", Request{}); code != 502 || !strings.Contains(raw, "enabled") {
		t.Fatal(code, raw)
	}
}

// Without a client id the github preset asks for a token, and the sign-in
// endpoint says it is not available.
func TestGitHubWithoutClientID(t *testing.T) {
	t.Setenv("CREWBASE_GITHUB_CLIENT_ID", "")
	prev := GitHubClientID
	GitHubClientID = ""
	t.Cleanup(func() { GitHubClientID = prev })
	f := newFakeGitHub(t)
	withPresets(t, githubPreset(f.ts.URL+"/mcp"))
	e := newEnv(t)
	if it := e.listing(t)["github"]; it.Auth != "token" || it.SignIn != "" {
		t.Fatalf("%+v", it)
	}
	if code, _ := e.do(t, "POST", "/api/catalog/github/signin", Request{}); code != 400 {
		t.Fatal(code)
	}
	if g := newGitHubSignIn(nil, Config{GitHubClientID: "from-config"}); g.clientID != "from-config" {
		t.Fatal(g.clientID)
	}
	t.Setenv("CREWBASE_GITHUB_CLIENT_ID", "from-env")
	if g := newGitHubSignIn(nil, Config{GitHubClientID: "from-config"}); g.clientID != "from-env" {
		t.Fatal(g.clientID)
	}
}
