package catalog

// GitHub sign-in for the github preset: the OAuth device flow of a
// Crewbase-registered GitHub App (works the same for a classic OAuth App),
// so no client secret ships in the binary.
//
//  1. POST {github}/login/device/code (client_id[, scope]) → device_code,
//     user_code, verification_uri, expires_in, interval.
//  2. The person enters user_code at verification_uri (the dialog shows it).
//  3. We poll POST {github}/login/oauth/access_token (client_id,
//     device_code, grant_type=urn:ietf:params:oauth:grant-type:device_code)
//     no faster than interval: authorization_pending → keep polling;
//     slow_down → interval += 5s (or the interval the answer gives);
//     expired_token / access_denied end the flow.
//
// The access token is stored like a pasted token (mcp.SecretRef(server,
// "token"), the server's Authorization header references it). GitHub App
// user tokens may expire (8 h) with a refresh token (6 months); tokens made
// with the device flow refresh without the client secret
// (docs.github.com › Refreshing user access tokens), so the refresh token
// and expiry are kept under mcp.SignInRef(server) and refreshLoop renews
// the token before it expires and reconnects the server. When refreshing
// fails the server answers 401 and the UI offers to sign in again.
//
// Scopes: a GitHub App's access comes from its permissions and where it is
// installed, and GitHub ignores scope (except offline_access) for App
// client ids ("Iv…"). For a classic OAuth App client id we ask for
// githubScope, what the GitHub MCP server's tools need: repo (repositories,
// issues, pull requests, Actions, code security alerts) and read:org
// (organisations and teams).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/features/mcp"
)

// GitHubClientID is the built-in client id of Crewbase's GitHub App. Set
// at build time with
//
//	-ldflags "-X github.com/hedeqiang/crewbase/features/catalog.GitHubClientID=…"
//
// and overridden by the plugin config githubClientId and the environment
// variable CREWBASE_GITHUB_CLIENT_ID. Empty = no sign-in; the github
// preset then asks for a personal access token.
var GitHubClientID = "Iv23li29Z9IsHKCUbKuE"

// GitHubAppSlug is the App's URL name (github.com/apps/<slug>), used to
// link to its installation page when the signed-in person has not
// installed it anywhere. Config githubAppSlug, env CREWBASE_GITHUB_APP_SLUG.
var GitHubAppSlug = ""

// githubScope is asked of classic OAuth Apps (GitHub Apps ignore scope).
const githubScope = "repo read:org"

// Config of the catalog plugin.
type Config struct {
	GitHubClientID string `json:"githubClientId"`
	GitHubAppSlug  string `json:"githubAppSlug"`
	// GitHubURL / GitHubAPIURL default to https://github.com and
	// https://api.github.com (tests and GitHub Enterprise point elsewhere).
	GitHubURL    string `json:"githubUrl"`
	GitHubAPIURL string `json:"githubApiUrl"`
}

// ErrNoSignIn: the preset has no built-in sign-in configured.
var ErrNoSignIn = errors.New("sign-in is not available for this item")

type githubSignIn struct {
	s                   *Service
	clientID, slug      string
	base, api           string
	client              *http.Client
	unit                time.Duration // polling interval unit (tests shorten it)
	tick, refreshBefore time.Duration

	mu    sync.Mutex
	flows map[string]*deviceFlow // by preset id
}

func newGitHubSignIn(s *Service, cfg Config) *githubSignIn {
	pick := func(env, cfg, def string) string {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v
		}
		if v := strings.TrimSpace(cfg); v != "" {
			return v
		}
		return def
	}
	trim := func(u, def string) string {
		if u = strings.TrimRight(strings.TrimSpace(u), "/"); u == "" {
			return def
		}
		return u
	}
	return &githubSignIn{s: s,
		clientID: pick("CREWBASE_GITHUB_CLIENT_ID", cfg.GitHubClientID, GitHubClientID),
		slug:     pick("CREWBASE_GITHUB_APP_SLUG", cfg.GitHubAppSlug, GitHubAppSlug),
		base:     trim(cfg.GitHubURL, "https://github.com"),
		api:      trim(cfg.GitHubAPIURL, "https://api.github.com"),
		client:   &http.Client{Timeout: 30 * time.Second},
		unit:     time.Second, tick: time.Minute, refreshBefore: 10 * time.Minute,
		flows: map[string]*deviceFlow{},
	}
}

// signInReady reports whether a preset's SignIn can run.
func (s *Service) signInReady(kind string) bool {
	return kind == "github" && s.gh != nil && s.gh.clientID != ""
}

func (g *githubSignIn) app() bool { return strings.HasPrefix(g.clientID, "Iv") }

// SignInView is a device sign-in as the API shows it.
type SignInView struct {
	// State: pending | done | denied | expired | error.
	State           string    `json:"state"`
	UserCode        string    `json:"userCode,omitempty"`
	VerificationURI string    `json:"verificationUri,omitempty"`
	ExpiresAt       time.Time `json:"expiresAt,omitzero"`
	Error           string    `json:"error,omitempty"`
	Server          string    `json:"server,omitempty"`
	// NoInstallation: signed in, but the App is installed on no account,
	// so no repository is visible yet; InstallURL is where to install it
	// (empty when the App's slug is not configured).
	NoInstallation bool   `json:"noInstallation,omitempty"`
	InstallURL     string `json:"installUrl,omitempty"`
}

type deviceFlow struct {
	view   SignInView
	cancel context.CancelFunc
}

// tokenAnswer is GitHub's answer from the device code and token endpoints.
type tokenAnswer struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`

	AccessToken           string `json:"access_token"`
	RefreshToken          string `json:"refresh_token"`
	RefreshTokenExpiresIn int    `json:"refresh_token_expires_in"`

	Error       string `json:"error"`
	Description string `json:"error_description"`
}

func (a tokenAnswer) err() error {
	if a.Description != "" {
		return fmt.Errorf("GitHub: %s (%s)", a.Description, a.Error)
	}
	return fmt.Errorf("GitHub: %s", a.Error)
}

func (g *githubSignIn) post(ctx context.Context, path string, form url.Values) (tokenAnswer, error) {
	var a tokenAnswer
	req, err := http.NewRequestWithContext(ctx, "POST", g.base+path, strings.NewReader(form.Encode()))
	if err != nil {
		return a, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := g.client.Do(req)
	if err != nil {
		return a, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err := json.Unmarshal(b, &a); err != nil {
		return a, fmt.Errorf("GitHub answered %s", res.Status)
	}
	if a.Error == "" && res.StatusCode >= 300 {
		a.Error = res.Status
	}
	return a, nil
}

// Start begins a device sign-in for preset id (replacing one in progress).
// scope is the MCP scope a newly created server gets.
func (g *githubSignIn) Start(id, scope string) (SignInView, error) {
	p, ok := preset(id)
	if !ok {
		return SignInView{}, ErrUnknown
	}
	if p.SignIn != "github" || g.clientID == "" {
		return SignInView{}, ErrNoSignIn
	}
	form := url.Values{"client_id": {g.clientID}}
	if !g.app() {
		form.Set("scope", githubScope)
	}
	ctx, cancel := context.WithCancel(g.s.c.Context())
	a, err := g.post(ctx, "/login/device/code", form)
	if err == nil && a.Error != "" {
		err = a.err()
	}
	if err == nil && (a.DeviceCode == "" || a.UserCode == "") {
		err = errors.New("GitHub: no device code in the answer")
	}
	if err != nil {
		cancel()
		return SignInView{}, err
	}
	if a.VerificationURI == "" {
		a.VerificationURI = g.base + "/login/device"
	}
	if a.ExpiresIn <= 0 {
		a.ExpiresIn = 900
	}
	f := &deviceFlow{cancel: cancel, view: SignInView{State: "pending", UserCode: a.UserCode, VerificationURI: a.VerificationURI,
		ExpiresAt: time.Now().Add(time.Duration(a.ExpiresIn) * time.Second).UTC()}}
	g.mu.Lock()
	if old := g.flows[id]; old != nil {
		old.cancel()
	}
	g.flows[id] = f
	g.mu.Unlock()
	go g.poll(ctx, f, p, scope, a)
	return f.view, nil
}

// View returns the sign-in in progress (or finished) for preset id.
func (g *githubSignIn) View(id string) (SignInView, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	f := g.flows[id]
	if f == nil {
		return SignInView{}, false
	}
	return f.view, true
}

// Cancel stops and forgets preset id's sign-in.
func (g *githubSignIn) Cancel(id string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	f := g.flows[id]
	if f == nil {
		return false
	}
	f.cancel()
	delete(g.flows, id)
	return true
}

func (g *githubSignIn) set(f *deviceFlow, fn func(*SignInView)) {
	g.mu.Lock()
	fn(&f.view)
	g.mu.Unlock()
}

func (g *githubSignIn) poll(ctx context.Context, f *deviceFlow, p Preset, scope string, code tokenAnswer) {
	defer f.cancel()
	interval := time.Duration(max(code.Interval, 1)) * g.unit
	deadline := time.Now().Add(time.Duration(code.ExpiresIn) * time.Second)
	fail := func(state string, err error) {
		g.set(f, func(v *SignInView) { v.State, v.Error = state, err.Error() })
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
		if time.Now().After(deadline) {
			fail("expired", errors.New("the code expired before it was entered"))
			return
		}
		a, err := g.post(ctx, "/login/oauth/access_token", url.Values{"client_id": {g.clientID}, "device_code": {code.DeviceCode},
			"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}})
		if ctx.Err() != nil {
			return
		}
		if err != nil { // a network hiccup: try again
			continue
		}
		switch a.Error {
		case "":
		case "authorization_pending":
			continue
		case "slow_down":
			if a.Interval > 0 {
				interval = time.Duration(a.Interval) * g.unit
			} else {
				interval += 5 * g.unit
			}
			continue
		case "expired_token", "token_expired":
			fail("expired", a.err())
			return
		case "access_denied":
			fail("denied", a.err())
			return
		default:
			fail("error", a.err())
			return
		}
		if a.AccessToken == "" {
			fail("error", errors.New("GitHub: no access token in the answer"))
			return
		}
		server, err := g.store(p, scope, a)
		if err != nil {
			fail("error", err)
			return
		}
		g.set(f, func(v *SignInView) { v.State, v.Server = "done", server })
		if g.app() {
			if n, err := g.installations(ctx, a.AccessToken); err == nil && n == 0 {
				g.set(f, func(v *SignInView) {
					v.NoInstallation = true
					if g.slug != "" {
						v.InstallURL = g.base + "/apps/" + url.PathEscape(g.slug) + "/installations/new"
					}
				})
			}
		}
		return
	}
}

// refreshState is kept under mcp.SignInRef(server).
type refreshState struct {
	RefreshToken  string    `json:"refreshToken"`
	Expiry        time.Time `json:"expiry"`
	RefreshExpiry time.Time `json:"refreshExpiry,omitzero"`
	ClientID      string    `json:"clientId"`
}

func tokenInput(p Preset) string {
	for _, x := range p.Inputs {
		if x.Secret {
			return x.ID
		}
	}
	return "token"
}

// store keeps the token and creates (or, signing in again, updates and
// reconnects) the preset's server.
func (g *githubSignIn) store(p Preset, scope string, a tokenAnswer) (string, error) {
	svc, cs := g.s.mcp(), g.s.creds()
	if svc == nil {
		return "", errors.New("the mcp service is not running")
	}
	if cs == nil {
		return "", errors.New("no credential store is available to keep the token")
	}
	in := map[string]string{tokenInput(p): a.AccessToken}
	if _, ok := svc.Get(p.Server); !ok {
		if _, err := g.s.addServer(svc, p, Request{Inputs: in, Scope: scope}); err != nil {
			return "", err
		}
	} else {
		srv, secrets, err := p.build(in, svc.ServerDir(p.Server))
		if err != nil {
			return "", err
		}
		for ref, v := range secrets {
			if err := cs.Set(ref, v); err != nil {
				return "", fmt.Errorf("store %s: %w", ref, err)
			}
		}
		g.saveRefresh(cs, p.Server, a)
		_, err = svc.Update(p.Server, func(x *mcp.Server) {
			if x.Headers == nil {
				x.Headers = map[string]string{}
			}
			for k, v := range srv.Headers {
				x.Headers[k] = v
			}
			x.Enabled = true
		})
		return p.Server, err
	}
	g.saveRefresh(cs, p.Server, a)
	return p.Server, nil
}

func (g *githubSignIn) saveRefresh(cs *credentials.Service, server string, a tokenAnswer) {
	ref := mcp.SignInRef(server)
	if a.RefreshToken == "" || a.ExpiresIn <= 0 {
		_ = cs.Delete(ref) // a token that does not expire
		return
	}
	st := refreshState{RefreshToken: a.RefreshToken, ClientID: g.clientID,
		Expiry: time.Now().Add(time.Duration(a.ExpiresIn) * time.Second).UTC()}
	if a.RefreshTokenExpiresIn > 0 {
		st.RefreshExpiry = time.Now().Add(time.Duration(a.RefreshTokenExpiresIn) * time.Second).UTC()
	}
	b, _ := json.Marshal(st)
	if err := cs.Set(ref, string(b)); err != nil {
		g.s.c.Log().Warn("catalog: could not keep the GitHub refresh token", "err", err)
	}
}

// installations counts the App installations the token can see.
func (g *githubSignIn) installations(ctx context.Context, token string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", g.api+"/user/installations?per_page=1", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := g.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return 0, fmt.Errorf("GitHub answered %s", res.Status)
	}
	var v struct {
		Total int `json:"total_count"`
	}
	err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&v)
	return v.Total, err
}

// refreshLoop renews expiring GitHub tokens (at start, then every tick).
func (g *githubSignIn) refreshLoop(ctx context.Context) {
	for {
		g.refreshDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(g.tick):
		}
	}
}

// refreshDue refreshes the token of every signed-in GitHub server that
// expires within refreshBefore, and reconnects it.
func (g *githubSignIn) refreshDue(ctx context.Context) {
	svc, cs := g.s.mcp(), g.s.creds()
	if svc == nil || cs == nil {
		return
	}
	for _, p := range builtin.Presets {
		if p.SignIn != "github" {
			continue
		}
		if _, ok := svc.Get(p.Server); !ok {
			continue
		}
		ref := mcp.SignInRef(p.Server)
		raw, err := cs.Resolve(ctx, ref)
		if err != nil || raw == "" {
			continue
		}
		var st refreshState
		if json.Unmarshal([]byte(raw), &st) != nil || st.RefreshToken == "" || time.Until(st.Expiry) > g.refreshBefore {
			continue
		}
		id := st.ClientID
		if id == "" {
			id = g.clientID
		}
		a, err := g.post(ctx, "/login/oauth/access_token", url.Values{"client_id": {id}, "grant_type": {"refresh_token"}, "refresh_token": {st.RefreshToken}})
		if err != nil {
			continue // offline: next tick
		}
		if a.Error != "" || a.AccessToken == "" {
			// revoked or expired: the server answers 401 and the person signs in again
			g.s.c.Log().Warn("catalog: GitHub token refresh failed", "server", p.Server, "err", a.err())
			_ = cs.Delete(ref)
			continue
		}
		if err := cs.Set(mcp.SecretRef(p.Server, strings.ToLower(tokenInput(p))), a.AccessToken); err != nil {
			continue
		}
		g.saveRefresh(cs, p.Server, a)
		if _, err := svc.Update(p.Server, func(*mcp.Server) {}); err != nil { // reconnect with the new token
			g.s.c.Log().Warn("catalog: reconnect after refresh", "server", p.Server, "err", err)
		}
	}
}
