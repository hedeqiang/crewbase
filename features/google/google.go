// Package google is Crewbase's built-in Google connector: Gmail, Google
// Calendar, Drive, Docs, Sheets and Slides through Google's own sign-in,
// with nothing to install (plain REST over net/http, no SDK, no uvx).
//
// Sign-in is the OAuth 2.0 flow for installed apps
// (developers.google.com/identity/protocols/oauth2/native-app): an
// authorization code with PKCE (S256) and a loopback redirect to the
// gateway's own callback (http://127.0.0.1:<port>/api/google/callback).
// Crewbase ships its own "Desktop app" OAuth client; Google treats its
// secret as non-confidential. The client comes from plugin config
// (clientId / clientSecret), else CREWBASE_GOOGLE_CLIENT_ID /
// CREWBASE_GOOGLE_CLIENT_SECRET, else the build-time defaults below
// (-ldflags -X). Without one the connector says so and does nothing.
//
// One Google account per host. Each product adds its scopes to the grant
// (the request names every connected product's scopes, so the new refresh
// token covers them all). The refresh token lives only in the system
// keychain (keychain:crewbase/google-oauth); access tokens stay in memory.
// google.json in the data directory keeps what is not secret: the
// account's email, the granted scopes, connected products and who may use
// them. A refresh answered with invalid_grant marks the account "needs
// sign-in" until the person connects again. Signing out revokes the grant
// at Google and deletes the keychain item.
package google

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

// Build-time defaults for Crewbase's own OAuth client. The client id is
// public; the default is Crewbase's own "Desktop app" client. Its secret
// (not confidential for a desktop client, but kept out of the repository)
// is embedded from client_secret.txt next to this file, which builds write
// from the GOOGLE_CLIENT_SECRET repository secret and git ignores; -ldflags
// "-X github.com/hedeqiang/crewbase/features/google.DefaultClientSecret=…"
// also works.
var (
	DefaultClientID     = "609097189154-sj7spdlk0b5hjupgfjooeglso78rnega.apps.googleusercontent.com"
	DefaultClientSecret = strings.TrimSpace(embeddedSecret)
)

// Environment variables naming the OAuth client (between config and the
// build-time defaults).
const (
	EnvClientID     = "CREWBASE_GOOGLE_CLIENT_ID"
	EnvClientSecret = "CREWBASE_GOOGLE_CLIENT_SECRET"
)

// ServiceKey is the key of the *Service.
const ServiceKey = "google"

// DefaultTokenRef is where the refresh token is kept.
const DefaultTokenRef = "keychain:crewbase/google-oauth"

// CallbackPath is the gateway route Google redirects to.
const CallbackPath = "/api/google/callback"

// Errors.
var (
	ErrNotConfigured = errors.New("Google sign-in is not set up in this build (no OAuth client)")
	ErrNotConnected  = errors.New("Google is not connected; ask the user to connect it in Settings › Extensions")
	ErrNeedsLogin    = errors.New("the Google sign-in expired or was revoked; ask the user to connect Google again in Settings › Extensions")
	ErrUnknown       = errors.New("unknown Google product")
	ErrState         = errors.New("no Google sign-in is waiting for this response")
	ErrDenied        = errors.New("the Google sign-in was cancelled or access was denied")
	ErrScopes        = errors.New("Google did not grant the permissions this product needs; connect again and leave its boxes ticked")
	ErrNoRedirect    = errors.New("Google sign-in needs the local gateway (no callback address)")
)

// Config is the plugin config.
type Config struct {
	// ClientID / ClientSecret of the OAuth client ("Desktop app" type).
	// The secret may be a credential ref (keychain:… / env:…).
	ClientID     string `json:"clientId,omitempty"`
	ClientSecret string `json:"clientSecret,omitempty"`
	// Dir holds google.json (default ${root} via the bundle).
	Dir string `json:"dir,omitempty"`
	// TokenRef overrides where the refresh token is kept.
	TokenRef string `json:"tokenRef,omitempty"`
	// OAuthBase / APIBase point every Google endpoint at another origin
	// (tests and local fakes): {OAuthBase}/o/oauth2/v2/auth, /token,
	// /revoke, /v1/userinfo; {APIBase}/gmail/v1/…, /calendar/v3/…,
	// /drive/v3/…, /upload/drive/v3/…, /v1/documents/…, /v4/spreadsheets/…,
	// /v1/presentations/….
	OAuthBase string `json:"oauthBase,omitempty"`
	APIBase   string `json:"apiBase,omitempty"`
}

// Endpoints are the Google URLs used.
type Endpoints struct {
	Auth, Token, Revoke, Userinfo                        string
	Gmail, Calendar, Drive, Upload, Docs, Sheets, Slides string
}

// DefaultEndpoints are Google's production endpoints.
func DefaultEndpoints() Endpoints {
	return Endpoints{
		Auth:     "https://accounts.google.com/o/oauth2/v2/auth",
		Token:    "https://oauth2.googleapis.com/token",
		Revoke:   "https://oauth2.googleapis.com/revoke",
		Userinfo: "https://openidconnect.googleapis.com/v1/userinfo",
		Gmail:    "https://gmail.googleapis.com/gmail/v1/users/me",
		Calendar: "https://www.googleapis.com/calendar/v3",
		Drive:    "https://www.googleapis.com/drive/v3",
		Upload:   "https://www.googleapis.com/upload/drive/v3",
		Docs:     "https://docs.googleapis.com/v1/documents",
		Sheets:   "https://sheets.googleapis.com/v4/spreadsheets",
		Slides:   "https://slides.googleapis.com/v1/presentations",
	}
}

func endpointsFor(cfg Config) Endpoints {
	e := DefaultEndpoints()
	if b := strings.TrimRight(cfg.OAuthBase, "/"); b != "" {
		e.Auth, e.Token, e.Revoke, e.Userinfo = b+"/o/oauth2/v2/auth", b+"/token", b+"/revoke", b+"/v1/userinfo"
	}
	if b := strings.TrimRight(cfg.APIBase, "/"); b != "" {
		e.Gmail, e.Calendar, e.Drive, e.Upload = b+"/gmail/v1/users/me", b+"/calendar/v3", b+"/drive/v3", b+"/upload/drive/v3"
		e.Docs, e.Sheets, e.Slides = b+"/v1/documents", b+"/v4/spreadsheets", b+"/v1/presentations"
	}
	return e
}

// baseScopes identify the account (its email address).
var baseScopes = []string{"openid", "email"}

// state is google.json: nothing secret.
type state struct {
	Email    string                  `json:"email,omitempty"`
	Scopes   []string                `json:"scopes,omitempty"` // granted
	Products map[string]productState `json:"products,omitempty"`
	// NeedsLogin: Google refused the refresh token (invalid_grant).
	NeedsLogin bool `json:"needsLogin,omitempty"`
}

type productState struct {
	// Scope: "global" / "" (every agent), an agent id or team:<id>.
	Scope string    `json:"scope,omitempty"`
	Since time.Time `json:"since"`
}

// pending is a sign-in waiting for Google's redirect.
type pending struct {
	product  string
	scope    string
	verifier string
	redirect string
	scopes   []string
	at       time.Time
}

// pendingTTL bounds how long a sign-in link stays valid.
var pendingTTL = 15 * time.Minute

// backoffBase is the first retry delay on 429 / 5xx.
var backoffBase = 500 * time.Millisecond

// Service is the Google connector.
type Service struct {
	c   *kernel.Context
	cfg Config
	ep  Endpoints
	// HTTP is used for every Google request (default: 60 s timeout).
	HTTP *http.Client
	// creds overrides the credentials lookup (tests).
	creds    *credentials.Service
	tokenRef string
	reg      *tools.Registry
	// teamExists / agentExists validate scopes (optional).
	changed func()

	mu           sync.Mutex
	st           state
	access       string
	expiry       time.Time
	pending      map[string]*pending
	redirectBase string
	lastErr      string
	subs         map[string]*kernel.Context // product → its registered tools
	subScope     map[string]string

	refreshMu sync.Mutex
}

// ---------------------------------------------------------------- client config

type client struct {
	id, secret, source string
}

func (s *Service) client(ctx context.Context) (client, error) {
	var c client
	pick := func(cfg, env, build string) (string, string) {
		switch {
		case strings.TrimSpace(cfg) != "":
			return strings.TrimSpace(cfg), "config"
		case strings.TrimSpace(os.Getenv(env)) != "":
			return strings.TrimSpace(os.Getenv(env)), "env"
		case strings.TrimSpace(build) != "":
			return strings.TrimSpace(build), "build"
		}
		return "", ""
	}
	c.id, c.source = pick(s.cfg.ClientID, EnvClientID, DefaultClientID)
	c.secret, _ = pick(s.cfg.ClientSecret, EnvClientSecret, DefaultClientSecret)
	// Google's token endpoint wants the secret of a "Desktop app" client
	// even with PKCE (it is not confidential there): both are required.
	if c.id == "" || c.secret == "" {
		return c, ErrNotConfigured
	}
	if strings.HasPrefix(c.secret, "keychain:") || strings.HasPrefix(c.secret, "env:") {
		cs := s.credSvc()
		if cs == nil {
			return c, errors.New("no credential store to resolve the Google client secret")
		}
		v, err := cs.Resolve(ctx, c.secret)
		if err != nil {
			return c, fmt.Errorf("Google client secret: %w", err)
		}
		c.secret = v
	}
	return c, nil
}

// Configured reports whether an OAuth client is set up.
func (s *Service) Configured() bool {
	_, err := s.client(context.Background())
	return err == nil
}

func (s *Service) credSvc() *credentials.Service {
	if s.creds != nil {
		return s.creds
	}
	if s.c == nil {
		return nil
	}
	cs, _ := kernel.Lookup[*credentials.Service](s.c, credentials.ServiceKey)
	return cs
}

func (s *Service) httpc() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

// ---------------------------------------------------------------- state

func (s *Service) statePath() string {
	if s.cfg.Dir == "" {
		return ""
	}
	return filepath.Join(s.cfg.Dir, "google.json")
}

func (s *Service) load() {
	p := s.statePath()
	if p == "" {
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var st state
	if json.Unmarshal(b, &st) == nil {
		s.st = st
	}
}

// saveLocked writes google.json. Callers hold s.mu.
func (s *Service) saveLocked() error {
	p := s.statePath()
	if p == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s.st, "", "  ")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (s *Service) notify() {
	if s.changed != nil {
		s.changed()
	}
}

// ---------------------------------------------------------------- views

// ProductView is one product as the API lists it.
type ProductView struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	CatalogID string   `json:"catalogId"`
	Connected bool     `json:"connected"`
	Scope     string   `json:"scope,omitempty"`
	Scopes    []string `json:"scopes"`
	Tools     []string `json:"tools"`
}

// View is GET /api/google.
type View struct {
	Configured bool   `json:"configured"`
	Source     string `json:"source,omitempty"` // config | env | build
	// Missing names what keeps sign-in from working: clientId | clientSecret.
	Missing    string        `json:"missing,omitempty"`
	Connected  bool          `json:"connected"`
	Email      string        `json:"email,omitempty"`
	NeedsLogin bool          `json:"needsLogin,omitempty"`
	Scopes     []string      `json:"scopes,omitempty"`
	Products   []ProductView `json:"products"`
	// Pending names the product whose sign-in is waiting ("" = none).
	Pending     string `json:"pending,omitempty"`
	LastError   string `json:"lastError,omitempty"`
	RedirectURI string `json:"redirectUri,omitempty"`
}

// Status returns the connector's state.
func (s *Service) Status() View {
	c, err := s.client(context.Background())
	s.mu.Lock()
	defer s.mu.Unlock()
	v := View{Configured: err == nil, Source: c.source, Email: s.st.Email, NeedsLogin: s.st.NeedsLogin,
		Scopes: slices.Clone(s.st.Scopes), Connected: len(s.st.Products) > 0, LastError: s.lastErr}
	switch {
	case v.Configured:
		v.RedirectURI, _ = s.redirectLocked()
	case c.id == "":
		v.Missing = "clientId"
	case c.secret == "":
		v.Missing = "clientSecret"
	}
	var newest time.Time
	for _, p := range s.pending {
		if p.at.After(newest) && time.Since(p.at) < pendingTTL {
			newest, v.Pending = p.at, p.product
		}
	}
	for _, p := range Products {
		ps, ok := s.st.Products[p.ID]
		pv := ProductView{ID: p.ID, Name: p.Name, CatalogID: p.CatalogID, Connected: ok, Scope: ps.Scope, Scopes: p.Scopes}
		for _, t := range p.tools(s) {
			pv.Tools = append(pv.Tools, t.Name)
		}
		v.Products = append(v.Products, pv)
	}
	return v
}

// ---------------------------------------------------------------- redirect

// SetRedirectBase records the gateway's listen address (host:port).
func (s *Service) SetRedirectBase(addr string) {
	s.mu.Lock()
	s.redirectBase = addr
	s.mu.Unlock()
}

// redirectLocked is the loopback callback URL. Google's desktop clients
// accept http://127.0.0.1:<any port>/<path>; the gateway also answers on
// 127.0.0.1 when it listens on all interfaces or "localhost".
func (s *Service) redirectLocked() (string, error) {
	a := s.redirectBase
	if a == "" {
		return "", ErrNoRedirect
	}
	if strings.HasPrefix(a, "http://") {
		return strings.TrimRight(a, "/") + CallbackPath, nil
	}
	host, port, err := net.SplitHostPort(a)
	if err != nil {
		return "", ErrNoRedirect
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() || ip.To4() == nil {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + CallbackPath, nil
}

// ---------------------------------------------------------------- sign-in

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// challengeS256 is the PKCE code challenge for verifier.
func challengeS256(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func normScope(sc string) string {
	switch sc {
	case "email":
		return "https://www.googleapis.com/auth/userinfo.email"
	case "profile":
		return "https://www.googleapis.com/auth/userinfo.profile"
	}
	return sc
}

func hasScopes(granted, want []string) bool {
	set := map[string]bool{}
	for _, g := range granted {
		set[normScope(g)] = true
	}
	for _, w := range want {
		if !set[normScope(w)] {
			return false
		}
	}
	return true
}

// Connect starts a sign-in that adds product (shared with scope) and
// returns the URL to open in a browser.
func (s *Service) Connect(product, scope string) (string, error) {
	p, ok := productByID(product)
	if !ok {
		return "", ErrUnknown
	}
	c, err := s.client(context.Background())
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	redirect, err := s.redirectLocked()
	if err != nil {
		return "", err
	}
	for k, x := range s.pending {
		if time.Since(x.at) > pendingTTL {
			delete(s.pending, k)
		}
	}
	// Ask for everything already connected plus this product: installed
	// apps get no incremental authorization, so the new refresh token must
	// cover every product on its own.
	set := map[string]bool{}
	for _, sc := range baseScopes {
		set[sc] = true
	}
	for id := range s.st.Products {
		if q, ok := productByID(id); ok {
			for _, sc := range q.Scopes {
				set[sc] = true
			}
		}
	}
	for _, sc := range p.Scopes {
		set[sc] = true
	}
	scopes := make([]string, 0, len(set))
	for sc := range set {
		scopes = append(scopes, sc)
	}
	sort.Strings(scopes)
	st, verifier := randomString(24), randomString(48) // 64-char verifier
	if s.pending == nil {
		s.pending = map[string]*pending{}
	}
	s.pending[st] = &pending{product: p.ID, scope: scope, verifier: verifier, redirect: redirect, scopes: scopes, at: time.Now()}
	s.lastErr = ""
	q := url.Values{
		"client_id":              {c.id},
		"redirect_uri":           {redirect},
		"response_type":          {"code"},
		"scope":                  {strings.Join(scopes, " ")},
		"state":                  {st},
		"code_challenge":         {challengeS256(verifier)},
		"code_challenge_method":  {"S256"},
		"access_type":            {"offline"},
		"include_granted_scopes": {"true"},
		// consent every time: the answer must carry a refresh token for
		// the whole set of scopes
		"prompt": {"consent"},
	}
	if s.st.Email != "" {
		q.Set("login_hint", s.st.Email)
	}
	return s.ep.Auth + "?" + q.Encode(), nil
}

// Pending reports whether state belongs to a sign-in that is waiting.
func (s *Service) Pending(state string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[state]
	return ok && state != "" && time.Since(p.at) < pendingTTL
}

// tokenResponse is Google's token endpoint answer.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// oauthError is an error answer from the token endpoint.
type oauthError struct {
	Status int
	Code   string
	Desc   string
}

func (e *oauthError) Error() string {
	if e.Desc != "" {
		return fmt.Sprintf("Google sign-in: %s (%s)", e.Code, e.Desc)
	}
	return fmt.Sprintf("Google sign-in: %s (HTTP %d)", e.Code, e.Status)
}

func (s *Service) postToken(ctx context.Context, form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", s.ep.Token, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := s.httpc().Do(req)
	if err != nil {
		return nil, errors.New("Google sign-in: could not reach the token endpoint")
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tr tokenResponse
	_ = json.Unmarshal(b, &tr)
	if resp.StatusCode != 200 || tr.Error != "" || tr.AccessToken == "" {
		code := tr.Error
		if code == "" {
			code = "token_error"
		}
		return nil, &oauthError{Status: resp.StatusCode, Code: code, Desc: tr.ErrorDesc}
	}
	return &tr, nil
}

// Finish completes a sign-in with Google's redirect parameters. Each state
// is accepted once.
func (s *Service) Finish(ctx context.Context, stateParam, code, errParam string) error {
	s.mu.Lock()
	p, ok := s.pending[stateParam]
	if ok {
		delete(s.pending, stateParam)
	}
	s.mu.Unlock()
	if !ok || stateParam == "" || time.Since(p.at) > pendingTTL {
		return ErrState
	}
	fail := func(err error) error {
		s.mu.Lock()
		s.lastErr = err.Error()
		s.mu.Unlock()
		s.notify()
		return err
	}
	if errParam != "" || code == "" {
		return fail(ErrDenied)
	}
	c, err := s.client(ctx)
	if err != nil {
		return fail(err)
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {c.id},
		"redirect_uri":  {p.redirect},
		"code_verifier": {p.verifier},
	}
	if c.secret != "" {
		form.Set("client_secret", c.secret)
	}
	tr, err := s.postToken(ctx, form)
	if err != nil {
		return fail(err)
	}
	granted := strings.Fields(tr.Scope)
	if len(granted) == 0 {
		granted = p.scopes
	}
	email, err := s.userEmail(ctx, tr.AccessToken)
	if err != nil {
		return fail(err)
	}
	cs := s.credSvc()
	if cs == nil {
		return fail(errors.New("no credential store is available to keep the Google sign-in"))
	}
	if tr.RefreshToken != "" {
		if err := cs.Set(s.tokenRef, tr.RefreshToken); err != nil {
			return fail(fmt.Errorf("could not store the Google sign-in in the keychain: %w", err))
		}
	} else if v, err := cs.Resolve(ctx, s.tokenRef); err != nil || v == "" {
		return fail(errors.New("Google returned no refresh token; disconnect and connect again"))
	}

	s.mu.Lock()
	if s.st.Email != "" && !strings.EqualFold(s.st.Email, email) {
		// another account: it replaces the old one
		s.st.Products = nil
	}
	s.st.Email, s.st.Scopes, s.st.NeedsLogin = email, granted, false
	s.access, s.expiry = tr.AccessToken, time.Now().Add(time.Duration(max(tr.ExpiresIn, 60))*time.Second)
	if s.st.Products == nil {
		s.st.Products = map[string]productState{}
	}
	for id := range s.st.Products {
		if q, ok := productByID(id); ok && !hasScopes(granted, q.Scopes) {
			delete(s.st.Products, id)
		}
	}
	prod, _ := productByID(p.product)
	var result error
	if hasScopes(granted, prod.Scopes) {
		old := s.st.Products[p.product]
		if old.Since.IsZero() {
			old.Since = time.Now().UTC()
		}
		old.Scope = p.scope
		s.st.Products[p.product] = old
		s.lastErr = ""
	} else {
		result = ErrScopes
		s.lastErr = ErrScopes.Error()
	}
	err = s.saveLocked()
	s.mu.Unlock()
	s.syncTools()
	s.notify()
	if err != nil {
		return err
	}
	return result
}

// FinishURL completes a sign-in from the full redirect URL (pasted by the
// person when the browser that signed in is not on this computer).
func (s *Service) FinishURL(ctx context.Context, raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ErrState
	}
	q := u.Query()
	return s.Finish(ctx, q.Get("state"), q.Get("code"), q.Get("error"))
}

func (s *Service) userEmail(ctx context.Context, access string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", s.ep.Userinfo, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := s.httpc().Do(req)
	if err != nil {
		return "", errors.New("Google sign-in: could not read the account's email address")
	}
	defer resp.Body.Close()
	var u struct {
		Email string `json:"email"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&u) != nil || u.Email == "" {
		return "", fmt.Errorf("Google sign-in: could not read the account's email address (HTTP %d)", resp.StatusCode)
	}
	return u.Email, nil
}

// Token returns a valid access token, refreshing it when needed.
func (s *Service) Token(ctx context.Context) (string, error) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	s.mu.Lock()
	if len(s.st.Products) == 0 {
		s.mu.Unlock()
		return "", ErrNotConnected
	}
	if s.st.NeedsLogin {
		s.mu.Unlock()
		return "", ErrNeedsLogin
	}
	if s.access != "" && time.Until(s.expiry) > time.Minute {
		a := s.access
		s.mu.Unlock()
		return a, nil
	}
	s.mu.Unlock()
	c, err := s.client(ctx)
	if err != nil {
		return "", err
	}
	cs := s.credSvc()
	if cs == nil {
		return "", errors.New("no credential store")
	}
	rt, err := cs.Resolve(ctx, s.tokenRef)
	if err != nil || rt == "" {
		s.markNeedsLogin()
		return "", ErrNeedsLogin
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}, "client_id": {c.id}}
	if c.secret != "" {
		form.Set("client_secret", c.secret)
	}
	tr, err := s.postToken(ctx, form)
	if err != nil {
		var oe *oauthError
		if errors.As(err, &oe) && (oe.Code == "invalid_grant" || oe.Code == "unauthorized_client" || oe.Code == "invalid_client") {
			s.markNeedsLogin()
			return "", ErrNeedsLogin
		}
		return "", err
	}
	if tr.RefreshToken != "" && tr.RefreshToken != rt {
		_ = cs.Set(s.tokenRef, tr.RefreshToken)
	}
	s.mu.Lock()
	s.access, s.expiry = tr.AccessToken, time.Now().Add(time.Duration(max(tr.ExpiresIn, 60))*time.Second)
	s.mu.Unlock()
	return tr.AccessToken, nil
}

func (s *Service) markNeedsLogin() {
	s.mu.Lock()
	changed := !s.st.NeedsLogin
	s.st.NeedsLogin, s.access = true, ""
	_ = s.saveLocked()
	s.mu.Unlock()
	if changed {
		s.notify()
	}
}

// expire drops the cached access token (after a 401).
func (s *Service) expire() {
	s.mu.Lock()
	s.access = ""
	s.mu.Unlock()
}

// Disconnect removes product ("" = all). Removing the last product signs
// out: the grant is revoked at Google and the keychain item deleted.
func (s *Service) Disconnect(ctx context.Context, product string) error {
	if product != "" {
		if _, ok := productByID(product); !ok {
			return ErrUnknown
		}
	}
	s.mu.Lock()
	if _, ok := s.st.Products[product]; product != "" && !ok {
		s.mu.Unlock()
		return nil
	}
	if product != "" && len(s.st.Products) > 1 {
		delete(s.st.Products, product)
		err := s.saveLocked()
		s.mu.Unlock()
		s.syncTools()
		s.notify()
		return err
	}
	s.mu.Unlock()
	return s.SignOut(ctx)
}

// SignOut revokes the grant and forgets the account. Local state is
// cleared even when Google cannot be reached (the error is returned).
func (s *Service) SignOut(ctx context.Context) error {
	var revokeErr error
	if cs := s.credSvc(); cs != nil {
		if rt, err := cs.Resolve(ctx, s.tokenRef); err == nil && rt != "" {
			revokeErr = s.revoke(ctx, rt)
		}
		if err := cs.Delete(s.tokenRef); err != nil {
			return fmt.Errorf("could not delete the Google sign-in from the keychain: %w", err)
		}
	}
	s.mu.Lock()
	s.st, s.access, s.expiry, s.lastErr = state{}, "", time.Time{}, ""
	err := s.saveLocked()
	s.mu.Unlock()
	s.syncTools()
	s.notify()
	if revokeErr != nil {
		return revokeErr
	}
	return err
}

func (s *Service) revoke(ctx context.Context, token string) error {
	req, err := http.NewRequestWithContext(ctx, "POST", s.ep.Revoke, strings.NewReader(url.Values{"token": {token}}.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.httpc().Do(req)
	if err != nil {
		return errors.New("signed out here, but Google could not be reached to revoke access; remove Crewbase at myaccount.google.com/permissions")
	}
	resp.Body.Close()
	// 400 invalid_token: already revoked or expired — nothing left to do
	if resp.StatusCode != 200 && resp.StatusCode != 400 {
		return fmt.Errorf("signed out here, but Google answered %d to the revocation; remove Crewbase at myaccount.google.com/permissions", resp.StatusCode)
	}
	return nil
}

// ---------------------------------------------------------------- tools

// syncTools registers the tools of connected products and removes the
// others (a product's tools follow its scope).
func (s *Service) syncTools() {
	if s.reg == nil || s.c == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subs == nil {
		s.subs, s.subScope = map[string]*kernel.Context{}, map[string]string{}
	}
	for _, p := range Products {
		ps, want := s.st.Products[p.ID]
		sub, have := s.subs[p.ID]
		if have && (!want || s.subScope[p.ID] != ps.Scope) {
			sub.Dispose()
			delete(s.subs, p.ID)
			have = false
		}
		if want && !have {
			scope := ps.Scope
			var sub *kernel.Context
			var teams []string
			switch {
			case scope == "" || scope == "global":
				sub = s.c.Fork("")
			case strings.HasPrefix(scope, "team:"):
				sub, teams = s.c.Fork(""), []string{strings.TrimPrefix(scope, "team:")}
			default:
				sub = s.c.Fork(scope) // one agent
			}
			for _, t := range p.tools(s) {
				t.Teams = teams
				s.reg.Register(sub, t)
			}
			s.subs[p.ID], s.subScope[p.ID] = sub, scope
		}
	}
}

// ---------------------------------------------------------------- API calls

// APIError is an error answer from a Google API.
type APIError struct {
	Status  int
	Message string
	Reason  string
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	switch e.Status {
	case 401:
		return "Google rejected the sign-in (401); ask the user to connect Google again"
	case 403:
		return "Google refused (403): " + msg
	case 404:
		return "not found (404): " + msg
	case 429:
		return "Google rate limit reached; try again shortly (429)"
	}
	return fmt.Sprintf("Google API %d: %s", e.Status, msg)
}

func decodeAPIError(status int, b []byte) *APIError {
	e := &APIError{Status: status}
	var env struct {
		Error struct {
			Message string `json:"message"`
			Errors  []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &env) == nil && env.Error.Message != "" {
		e.Message = env.Error.Message
		if len(env.Error.Errors) > 0 {
			e.Reason = env.Error.Errors[0].Reason
		}
	} else {
		e.Message = clip(strings.TrimSpace(string(b)), 300)
	}
	return e
}

// maxResponse caps what is read from any Google response.
const maxResponse = 16 << 20

// do performs an authorized request. Retries: 429 always (Google did not
// act on it); 5xx only for idempotent requests; a 401 refreshes the
// access token once.
func (s *Service) do(ctx context.Context, method, u, ctype string, body []byte) ([]byte, http.Header, error) {
	idempotent := method == "GET" || method == "PUT" || method == "DELETE"
	refreshed := false
	for attempt := 0; ; attempt++ {
		tok, err := s.Token(ctx)
		if err != nil {
			return nil, nil, err
		}
		var rd io.Reader
		if body != nil {
			rd = strings.NewReader(string(body))
		}
		req, err := http.NewRequestWithContext(ctx, method, u, rd)
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Accept", "application/json")
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		resp, err := s.httpc().Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			if idempotent && attempt < 2 {
				if !sleep(ctx, backoffBase<<attempt) {
					return nil, nil, ctx.Err()
				}
				continue
			}
			return nil, nil, errors.New("could not reach Google: " + strings.ReplaceAll(err.Error(), tok, "***"))
		}
		b, rerr := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
		resp.Body.Close()
		if rerr != nil {
			return nil, nil, rerr
		}
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			return b, resp.Header, nil
		case resp.StatusCode == 401 && !refreshed:
			refreshed = true
			s.expire()
			continue
		case attempt < 3 && (resp.StatusCode == 429 || (resp.StatusCode >= 500 && idempotent) || rateLimited(resp.StatusCode, b)):
			d := backoffBase << attempt
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				var secs int
				if _, err := fmt.Sscanf(ra, "%d", &secs); err == nil && secs > 0 && secs <= 30 {
					d = time.Duration(secs) * time.Second
				}
			}
			if !sleep(ctx, d) {
				return nil, nil, ctx.Err()
			}
			continue
		}
		return nil, nil, decodeAPIError(resp.StatusCode, b)
	}
}

// rateLimited: Google also reports quota exhaustion as 403 with reason
// rateLimitExceeded / userRateLimitExceeded.
func rateLimited(status int, b []byte) bool {
	if status != 403 {
		return false
	}
	e := decodeAPIError(status, b)
	return e.Reason == "rateLimitExceeded" || e.Reason == "userRateLimitExceeded"
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// getJSON / sendJSON are JSON helpers over do.
func (s *Service) getJSON(ctx context.Context, u string, out any) error {
	b, _, err := s.do(ctx, "GET", u, "", nil)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}

func (s *Service) sendJSON(ctx context.Context, method, u string, in, out any) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return err
		}
	}
	ct := ""
	if in != nil {
		ct = "application/json"
	}
	b, _, err := s.do(ctx, method, u, ct, body)
	if err != nil {
		return err
	}
	if out == nil || len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, out)
}

// ---------------------------------------------------------------- helpers

// clip cuts s to n runes.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// capText clips s to n runes with a note the model can act on.
func capText(s string, n int, hint string) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	out := string(r[:n]) + fmt.Sprintf("\n\n[truncated at %d characters", n)
	if hint != "" {
		out += "; " + hint
	}
	return out + "]"
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// toolErr turns an error into a result the model can act on.
func toolErr(err error) tools.Result {
	var ae *APIError
	if errors.As(err, &ae) && (ae.Status == 429 || ae.Status >= 500) {
		return tools.Errorf("%v (temporary; you may retry once)", err)
	}
	return tools.Errorf("%v", err)
}
