package mcp

// OAuth for Streamable HTTP servers (MCP authorization spec): when a server
// answers 401, the SDK's authorization-code handler discovers the
// authorization server (protected resource metadata → AS metadata),
// registers Crewbase as a client (dynamic client registration, or the
// configured client id), and asks us for a code. We publish the login URL
// in the server's status (state "login"); the person opens it in their
// browser; the authorization server redirects to the gateway's callback
// (GET /api/mcp/oauth/callback), which hands the code back here.
//
// The token (with what is needed to refresh it) is stored in the system
// keychain under keychain:crewbase/mcp-oauth-<server>, never in mcp.json,
// and refreshed tokens are written back. Removing the server or signing
// out deletes it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"

	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/kernel"
)

// StateLogin means the server waits for the person to sign in.
const StateLogin = "login"

// loginTimeout bounds how long a connection waits for the person to sign in.
var loginTimeout = 10 * time.Minute

// OAuth configures sign-in for an http server.
type OAuth struct {
	// ClientID of a client registered with the authorization server
	// beforehand; empty = dynamic client registration.
	ClientID string `json:"clientId,omitempty"`
	// ClientSecret is a credential ref (or literal) for a confidential client.
	ClientSecret string `json:"clientSecret,omitempty"`
}

func oauthRef(server string) string { return "keychain:crewbase/mcp-oauth-" + server }

// storedToken is what the keychain holds for a server.
type storedToken struct {
	Token        *oauth2.Token `json:"token"`
	TokenURL     string        `json:"tokenUrl"`
	AuthStyle    int           `json:"authStyle,omitempty"`
	ClientID     string        `json:"clientId"`
	ClientSecret string        `json:"clientSecret,omitempty"`
	Scopes       []string      `json:"scopes,omitempty"`
}

// logins holds the pending sign-ins (state → code channel).
type logins struct {
	mu sync.Mutex
	m  map[string]chan auth.AuthorizationResult
}

func (l *logins) add(state string) chan auth.AuthorizationResult {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil {
		l.m = map[string]chan auth.AuthorizationResult{}
	}
	ch := make(chan auth.AuthorizationResult, 1)
	l.m[state] = ch
	return ch
}

func (l *logins) drop(state string) {
	l.mu.Lock()
	delete(l.m, state)
	l.mu.Unlock()
}

func (l *logins) pending(state string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.m[state]
	return ok
}

// deliver hands a callback's result to the waiting sign-in.
func (l *logins) deliver(r auth.AuthorizationResult) bool {
	l.mu.Lock()
	ch, ok := l.m[r.State]
	delete(l.m, r.State)
	l.mu.Unlock()
	if ok {
		ch <- r
	}
	return ok
}

// OAuthPending reports whether a callback state belongs to a sign-in.
func (s *Service) OAuthPending(state string) bool { return s.logins.pending(state) }

// OAuthCallback completes a sign-in with the authorization server's answer.
func (s *Service) OAuthCallback(code, state, iss string) bool {
	return s.logins.deliver(auth.AuthorizationResult{Code: code, State: state, Iss: iss})
}

// SetRedirectBase sets the origin the authorization server redirects to
// (the gateway, e.g. http://127.0.0.1:7788).
func (s *Service) SetRedirectBase(base string) {
	s.mu.Lock()
	s.redirectBase = base
	s.mu.Unlock()
}

func (s *Service) redirectURL() (string, error) {
	s.mu.Lock()
	b := s.redirectBase
	s.mu.Unlock()
	if b == "" {
		return "", errors.New("sign-in needs the local gateway (no callback address)")
	}
	return b + "/api/mcp/oauth/callback", nil
}

func (s *Service) creds() *credentials.Service {
	cr, _ := kernel.Lookup[*credentials.Service](s.c, credentials.ServiceKey)
	return cr
}

// SignOut forgets a server's token.
func (s *Service) SignOut(name string) error {
	cs := s.creds()
	if cs == nil {
		return nil
	}
	return cs.Delete(oauthRef(name))
}

// savingSource persists refreshed tokens.
type savingSource struct {
	src   oauth2.TokenSource
	save  func(*oauth2.Token)
	mu    sync.Mutex
	last  string
}

func (t *savingSource) Token() (*oauth2.Token, error) {
	tok, err := t.src.Token()
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	changed := tok.AccessToken != t.last
	t.last = tok.AccessToken
	t.mu.Unlock()
	if changed {
		t.save(tok)
	}
	return tok, nil
}

// oauthHandler builds the SDK handler for srv on connection c.
func (c *conn) oauthHandler(ctx context.Context) (auth.OAuthHandler, error) {
	srv := c.srv
	redirect, err := c.s.redirectURL()
	if err != nil {
		return nil, err
	}
	cs := c.s.creds()
	ref := oauthRef(srv.Name)
	var stored storedToken
	if cs != nil {
		if v, err := cs.Resolve(ctx, ref); err == nil && v != "" {
			_ = json.Unmarshal([]byte(v), &stored)
		}
	}
	save := func(st storedToken) {
		if cs == nil {
			return
		}
		b, _ := json.Marshal(st)
		if err := cs.Set(ref, string(b)); err != nil {
			c.ctx.Log().Warn("mcp: could not store the sign-in token", "server", srv.Name, "err", err)
		}
	}
	wrap := func(cfg *oauth2.Config, src oauth2.TokenSource) oauth2.TokenSource {
		base := storedToken{TokenURL: cfg.Endpoint.TokenURL, AuthStyle: int(cfg.Endpoint.AuthStyle), ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, Scopes: cfg.Scopes}
		return &savingSource{src: src, save: func(t *oauth2.Token) {
			st := base
			st.Token = t
			save(st)
		}}
	}
	conf := &auth.AuthorizationCodeHandlerConfig{
		RedirectURL:         redirect,
		RequestRefreshToken: true,
		Client:              c.s.HTTPClient,
		AuthorizationCodeFetcher: func(ctx context.Context, a *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			u, err := url.Parse(a.URL)
			if err != nil || (u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname()))) {
				return nil, fmt.Errorf("refusing a sign-in URL that is not https: %q", a.URL)
			}
			state := u.Query().Get("state")
			ch := c.s.logins.add(state)
			defer c.s.logins.drop(state)
			c.setStatus(func(st *Status) { st.State, st.Error, st.LoginURL = StateLogin, "", a.URL })
			select {
			case r := <-ch:
				c.setStatus(func(st *Status) { st.State, st.LoginURL = StateConnecting, "" })
				return &r, nil
			case <-ctx.Done():
				c.setStatus(func(st *Status) { st.LoginURL = "" })
				return nil, errors.New("sign-in was not completed in time")
			}
		},
		NewTokenSource: func(ctx context.Context, cfg *oauth2.Config, tok *oauth2.Token) (oauth2.TokenSource, error) {
			src := wrap(cfg, cfg.TokenSource(context.WithoutCancel(ctx), tok))
			_, _ = src.Token() // store at once
			return src, nil
		},
	}
	if srv.OAuth != nil && srv.OAuth.ClientID != "" {
		cc := &oauthex.ClientCredentials{ClientID: srv.OAuth.ClientID}
		if srv.OAuth.ClientSecret != "" {
			secret, err := resolveValue(ctx, c.s.resolver(), srv.OAuth.ClientSecret)
			if err != nil {
				return nil, fmt.Errorf("oauth client secret: %w", err)
			}
			cc.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: secret}
		}
		conf.PreregisteredClient = cc
	} else {
		conf.DynamicClientRegistrationConfig = &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{
			ClientName:              "Crewbase",
			RedirectURIs:            []string{redirect},
			GrantTypes:              []string{"authorization_code", "refresh_token"},
			ResponseTypes:           []string{"code"},
			TokenEndpointAuthMethod: "none",
		}}
	}
	if stored.Token != nil && stored.TokenURL != "" {
		cfg := &oauth2.Config{ClientID: stored.ClientID, ClientSecret: stored.ClientSecret, Scopes: stored.Scopes,
			Endpoint: oauth2.Endpoint{TokenURL: stored.TokenURL, AuthStyle: oauth2.AuthStyle(stored.AuthStyle)}}
		conf.InitialTokenSource = wrap(cfg, cfg.TokenSource(context.Background(), stored.Token))
	}
	return auth.NewAuthorizationCodeHandler(conf)
}

func isLoopback(h string) bool { return h == "127.0.0.1" || h == "localhost" || h == "::1" }

