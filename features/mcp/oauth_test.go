package mcp_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/features/mcp"
)

// fakeOAuth is an authorization server plus an MCP server that requires
// its tokens.
type fakeOAuth struct {
	ts        *httptest.Server
	registers atomic.Int32
	tokens    atomic.Int32
	codes     map[string]string // code → redirect state check
}

func newFakeOAuth(t *testing.T) *fakeOAuth {
	f := &fakeOAuth{codes: map[string]string{}}
	mcpH := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return newTestServer(nil) }, nil)
	mux := http.NewServeMux()
	var base string
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"resource": base + "/mcp", "authorization_servers": []string{base}, "scopes_supported": []string{"tools"}})
	})
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"resource": base + "/mcp", "authorization_servers": []string{base}, "scopes_supported": []string{"tools"}})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"issuer": base, "authorization_endpoint": base + "/authorize", "token_endpoint": base + "/token",
			"registration_endpoint": base + "/register", "response_types_supported": []string{"code"},
			"code_challenge_methods_supported": []string{"S256"}, "grant_types_supported": []string{"authorization_code", "refresh_token"},
			"token_endpoint_auth_methods_supported": []string{"none"}, "scopes_supported": []string{"tools", "offline_access"},
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		f.registers.Add(1)
		var m map[string]any
		json.NewDecoder(r.Body).Decode(&m)
		w.WriteHeader(201)
		writeJSON(w, map[string]any{"client_id": "crewbase-client", "redirect_uris": m["redirect_uris"], "token_endpoint_auth_method": "none"})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("grant_type") == "authorization_code" && r.Form.Get("code") != "good-code" {
			w.WriteHeader(400)
			writeJSON(w, map[string]any{"error": "invalid_grant"})
			return
		}
		f.tokens.Add(1)
		writeJSON(w, map[string]any{"access_token": "at-ok", "token_type": "Bearer", "refresh_token": "rt-1", "expires_in": 3600})
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at-ok" {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource/mcp"`, base))
			http.Error(w, "unauthorized", 401)
			return
		}
		mcpH.ServeHTTP(w, r)
	})
	f.ts = httptest.NewServer(mux)
	base = f.ts.URL
	t.Cleanup(func() { f.ts.CloseClientConnections(); f.ts.Close() })
	return f
}

func TestOAuthSignIn(t *testing.T) {
	f := newFakeOAuth(t)
	e := newEnv(t, nil)
	mb := credentials.NewMemory() // the vault and its old name, in memory
	e.creds.SetBackend("vault", mb)
	e.creds.SetBackend("keychain", mb)
	// the gateway's callback, as the API plugin would serve it
	cb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if !e.svc.OAuthPending(q.Get("state")) {
			http.Error(w, "unknown state", 401)
			return
		}
		e.svc.OAuthCallback(q.Get("code"), q.Get("state"), q.Get("iss"))
	}))
	t.Cleanup(cb.Close)
	e.svc.SetRedirectBase(cb.URL)

	if _, err := e.svc.Add(mcp.Server{Name: "cloud", Transport: mcp.TransportHTTP, URL: f.ts.URL + "/mcp", Enabled: true, OAuth: &mcp.OAuth{}}); err != nil {
		t.Fatal(err)
	}
	st := e.waitState(t, "cloud", mcp.StateLogin)
	lu, err := url.Parse(st.LoginURL)
	if err != nil || !strings.HasPrefix(st.LoginURL, f.ts.URL+"/authorize?") {
		t.Fatalf("login url %q", st.LoginURL)
	}
	q := lu.Query()
	if q.Get("client_id") != "crewbase-client" || q.Get("code_challenge_method") != "S256" || q.Get("redirect_uri") != cb.URL+"/api/mcp/oauth/callback" {
		t.Fatalf("authorize params %v", q)
	}
	// a forged callback with an unknown state is refused
	if r, _ := http.Get(cb.URL + "/api/mcp/oauth/callback?code=good-code&state=forged"); r.StatusCode != 401 {
		t.Fatalf("forged state: %d", r.StatusCode)
	}
	// the person approves in the browser: the AS redirects back with the code
	if r, err := http.Get(q.Get("redirect_uri") + "?code=good-code&state=" + url.QueryEscape(q.Get("state"))); err != nil || r.StatusCode != 200 {
		t.Fatal(r, err)
	}
	st = e.waitState(t, "cloud", mcp.StateReady)
	if st.Tools != 2 || st.LoginURL != "" {
		t.Fatalf("%+v", st)
	}
	if x := e.call("", "cloud__echo", `{"text":"signed in"}`); x.Result.Content != "signed in" {
		t.Fatalf("%+v", x.Result)
	}
	// the token lives in the keychain, not in mcp.json
	b, _ := os.ReadFile(filepath.Join(e.dir, "mcp.json"))
	if strings.Contains(string(b), "at-ok") || strings.Contains(string(b), "rt-1") {
		t.Fatal("token in mcp.json")
	}
	tok, err := e.creds.Resolve(t.Context(), "keychain:crewbase/mcp-oauth-cloud")
	if err != nil || !strings.Contains(tok, `"refresh_token":"rt-1"`) {
		t.Fatalf("stored token %q %v", tok, err)
	}
	// reconnect: the stored token is used, no new sign-in
	e.svc.SetEnabled("cloud", false)
	e.waitState(t, "cloud", mcp.StateDisabled)
	e.svc.SetEnabled("cloud", true)
	e.waitState(t, "cloud", mcp.StateReady)
	if f.registers.Load() != 1 {
		t.Fatalf("registered %d times", f.registers.Load())
	}
	// removing the server deletes the token
	if err := e.svc.Remove("cloud"); err != nil {
		t.Fatal(err)
	}
	if v, _ := e.creds.Resolve(t.Context(), "keychain:crewbase/mcp-oauth-cloud"); v != "" {
		t.Fatal("token kept after remove")
	}
}
