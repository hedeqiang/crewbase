// Package agentdocs connects Open Agent Docs (github.com/kadaliao/open-agent-docs),
// a self-hosted document server where people and agents write pages
// together, as an app:
//
//   - the person connects a server once (its address and an admin agent
//     token, kept in the vault);
//   - each Crewbase agent gets its own identity there (so pages and
//     comments say "Kai wrote this") and its own MCP connection, visible
//     only to that agent (tools docs-<id>__read_page …);
//   - when someone @mentions an agent in a comment (or replies in its
//     thread), the notification becomes a task for that agent in its own
//     conversation; it answers in the thread through the MCP tools.
//
// Nothing runs until a server is connected. The server itself is not run
// by Crewbase.
package agentdocs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/features/mcp"
	"github.com/hedeqiang/crewbase/kernel"
)

// ServiceKey is the agentdocs service.
const ServiceKey = "agentdocs"

const (
	adminRef   = "vault:crewbase/agentdocs-admin"
	serverPref = "docs-" // MCP server names: docs-<short agent hash>
	// From is Input.From of the tasks comments become.
	From = "agentdocs"
)

// Settings is what the person configured (stored in agentdocs.json;
// the admin token itself is in the vault).
type Settings struct {
	URL       string `json:"url"`
	Workspace string `json:"workspace"`
	// WorkspaceName is shown in the settings page.
	WorkspaceName string `json:"workspaceName,omitempty"`
	// Agents is which Crewbase agents may use the docs: empty = all.
	Agents []string `json:"agents,omitempty"`
	// Scope of the identities: write (default) or read.
	Scope string `json:"scope,omitempty"`
	// Wake: comments that @mention an agent become its tasks.
	Wake bool `json:"wake"`
}

// Identity is a Crewbase agent's identity on the docs server.
type Identity struct {
	Agent string `json:"agent"` // Crewbase agent id
	ID    string `json:"id"`    // docs agent id
	Name  string `json:"name"`  // how it signs (and is @mentioned)
	// After is the last notification handled.
	After int64 `json:"after,omitempty"`
}

type state struct {
	Settings   Settings            `json:"settings"`
	Identities map[string]Identity `json:"identities,omitempty"` // by Crewbase agent id
	// Managed: Crewbase runs the server (一键启动, local.go).
	Managed *Managed `json:"managed,omitempty"`
}

// Status is the API view.
type Status struct {
	Connected  bool        `json:"connected"`
	Settings   Settings    `json:"settings"`
	Identities []Identity  `json:"identities"`
	Error      string      `json:"error,omitempty"`
	Local      LocalStatus `json:"local"`
	// Woken counts comments turned into tasks since start.
	Woken int `json:"woken"`
}

// Service is the agentdocs service.
type Service struct {
	c      *kernel.Context
	dir    string
	agents *agent.Registry
	client *http.Client

	mu      sync.Mutex
	st      state
	err     string
	woken   int
	cancel  context.CancelFunc // the listeners
	syncing sync.Mutex
	lc      local
}

func (s *Service) creds() *credentials.Service {
	cs, _ := kernel.Lookup[*credentials.Service](s.c, credentials.ServiceKey)
	return cs
}

func (s *Service) mcps() *mcp.Service {
	m, _ := kernel.Lookup[*mcp.Service](s.c, mcp.ServiceKey)
	return m
}

func (s *Service) path() string { return filepath.Join(s.dir, "agentdocs.json") }

func (s *Service) load() {
	b, err := os.ReadFile(s.path())
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &s.st)
}

func (s *Service) saveLocked() error {
	if s.dir == "" {
		return nil
	}
	b, _ := json.MarshalIndent(s.st, "", "  ")
	tmp := s.path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path())
}

func (s *Service) status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{Connected: s.st.Settings.URL != "", Settings: s.st.Settings, Identities: []Identity{}, Error: s.err, Woken: s.woken}
	for _, id := range s.st.Identities {
		st.Identities = append(st.Identities, id)
	}
	sort.Slice(st.Identities, func(i, j int) bool { return st.Identities[i].Name < st.Identities[j].Name })
	return st
}

// Status reports the connection and the server Crewbase runs.
func (s *Service) Status() Status {
	st := s.status()
	st.Local = s.Local()
	return st
}

// ---------------------------------------------------------------- the docs API

// APIError is an error answer of the docs server.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("docs server: %s (%d %s)", e.Message, e.Status, e.Code)
	}
	return fmt.Sprintf("docs server: HTTP %d", e.Status)
}

func (s *Service) call(ctx context.Context, base, token, method, p string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+"/api/v1"+p, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach the docs server at %s: %w", base, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(b, &e)
		return &APIError{Status: resp.StatusCode, Code: e.Error.Code, Message: e.Error.Message}
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

type meOut struct {
	User struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Kind string `json:"kind"`
	} `json:"user"`
	Workspaces []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Role string `json:"role"`
	} `json:"workspaces"`
}

type docsAgent struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Scope     string `json:"scope"`
	RevokedAt *int64 `json:"revoked_at"`
}

// ---------------------------------------------------------------- connecting

// ConnectRequest connects a docs server.
type ConnectRequest struct {
	URL   string `json:"url"`
	Token string `json:"token"` // an admin agent token (oad_…); empty keeps the stored one
	// Workspace id; empty = the token's first workspace.
	Workspace string   `json:"workspace,omitempty"`
	Agents    []string `json:"agents,omitempty"`
	Scope     string   `json:"scope,omitempty"`
	Wake      *bool    `json:"wake,omitempty"`
}

var urlRe = regexp.MustCompile(`^https?://[^\s/]+(:\d+)?(/[^\s]*)?$`)

// Connect checks the server and token, stores them, and sets every agent up.
func (s *Service) Connect(ctx context.Context, r ConnectRequest) (Status, error) {
	base := strings.TrimRight(strings.TrimSpace(r.URL), "/")
	base = strings.TrimSuffix(base, "/mcp")
	if !urlRe.MatchString(base) {
		return Status{}, errors.New("give the docs server's address, e.g. http://127.0.0.1:8848")
	}
	cs := s.creds()
	if cs == nil {
		return Status{}, errors.New("the vault is not available")
	}
	token := strings.TrimSpace(r.Token)
	if token == "" {
		t, err := cs.Resolve(ctx, adminRef)
		if err != nil || t == "" {
			return Status{}, errors.New("paste an admin agent token (oad_…): create one under Agents in the docs web app, with scope admin")
		}
		token = t
	}
	var me meOut
	if err := s.call(ctx, base, token, "GET", "/me", nil, &me); err != nil {
		var ae *APIError
		if errors.As(err, &ae) && ae.Status == 401 {
			return Status{}, errors.New("the docs server does not accept this token")
		}
		return Status{}, err
	}
	if len(me.Workspaces) == 0 {
		return Status{}, errors.New("this token has no workspace")
	}
	ws := me.Workspaces[0]
	if r.Workspace != "" {
		found := false
		for _, w := range me.Workspaces {
			if w.ID == r.Workspace {
				ws, found = w, true
			}
		}
		if !found {
			return Status{}, fmt.Errorf("the token has no workspace %q", r.Workspace)
		}
	}
	// an admin token may create agents: check it now, not on the first agent
	var list struct {
		Agents []docsAgent `json:"agents"`
	}
	if err := s.call(ctx, base, token, "GET", "/workspaces/"+url.PathEscape(ws.ID)+"/agents", nil, &list); err != nil {
		var ae *APIError
		if errors.As(err, &ae) && ae.Status == 403 {
			return Status{}, errors.New("this token cannot create agents: give it the admin scope (Agents › the agent › Scope: admin)")
		}
		return Status{}, err
	}
	if err := cs.Set(adminRef, token); err != nil {
		return Status{}, err
	}
	scope := r.Scope
	if scope != "read" {
		scope = "write"
	}
	s.mu.Lock()
	prev := s.st.Settings
	s.st.Settings = Settings{URL: base, Workspace: ws.ID, WorkspaceName: ws.Name, Agents: r.Agents, Scope: scope, Wake: prev.Wake || prev.URL == ""}
	if r.Wake != nil {
		s.st.Settings.Wake = *r.Wake
	}
	if prev.URL != "" && (prev.URL != base || prev.Workspace != ws.ID) {
		// another server: the old identities mean nothing there
		s.dropAllLocked()
	}
	s.err = ""
	err := s.saveLocked()
	s.mu.Unlock()
	if err != nil {
		return Status{}, err
	}
	s.Sync(ctx)
	return s.Status(), nil
}

// Disconnect removes every identity (revoking it on the server when it
// answers), the MCP connections and the stored token.
func (s *Service) Disconnect(ctx context.Context) error {
	s.stopListeners()
	s.mu.Lock()
	base := s.st.Settings.URL
	ids := s.st.Identities
	s.mu.Unlock()
	if cs := s.creds(); cs != nil && base != "" {
		if token, err := cs.Resolve(ctx, adminRef); err == nil {
			for _, id := range ids {
				_ = s.call(ctx, base, token, "DELETE", "/agents/"+url.PathEscape(id.ID), nil, nil)
			}
		}
	}
	s.mu.Lock()
	s.dropAllLocked()
	s.st.Settings = Settings{}
	s.err = ""
	err := s.saveLocked()
	s.mu.Unlock()
	if cs := s.creds(); cs != nil {
		_ = cs.Delete(adminRef)
		_ = cs.Delete(sessionRef)
	}
	s.removeLocal(false)
	return err
}

// dropAllLocked removes the MCP connections and tokens of every identity.
func (s *Service) dropAllLocked() {
	for agentID := range s.st.Identities {
		s.removeConnection(agentID)
	}
	s.st.Identities = nil
}

// UpdateSettings changes which agents use the docs and whether comments wake them.
func (s *Service) UpdateSettings(ctx context.Context, agents *[]string, wake *bool) (Status, error) {
	s.mu.Lock()
	if s.st.Settings.URL == "" {
		s.mu.Unlock()
		return Status{}, errors.New("no docs server is connected")
	}
	if agents != nil {
		s.st.Settings.Agents = *agents
	}
	if wake != nil {
		s.st.Settings.Wake = *wake
	}
	err := s.saveLocked()
	s.mu.Unlock()
	if err != nil {
		return Status{}, err
	}
	s.Sync(ctx)
	return s.Status(), nil
}

// ---------------------------------------------------------------- identities

// ServerName is the MCP server of a Crewbase agent's docs connection.
func ServerName(agentID string) string {
	h := sha256.Sum256([]byte(agentID))
	return serverPref + hex.EncodeToString(h[:4])
}

func tokenRef(agentID string) string {
	return mcp.SecretRef(ServerName(agentID), "token")
}

var badName = regexp.MustCompile(`[\s@\[\]()<>"'“”‘’，,]+`)

// docsName is how an agent signs on the docs server (1–40 characters,
// none of the ones mentions use).
func docsName(a *agent.Agent) string {
	n := strings.Trim(badName.ReplaceAllString(a.Spec().Name, "-"), "-")
	if n == "" {
		n = a.ID()
	}
	if r := []rune(n); len(r) > 40 {
		n = string(r[:40])
	}
	return n
}

func (s *Service) wanted(a *agent.Agent) bool {
	if a.Spec().Kind == agent.KindChat {
		return false // plain chats have no tools
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.st.Settings.Agents) == 0 {
		return true
	}
	for _, x := range s.st.Settings.Agents {
		if x == a.ID() {
			return true
		}
	}
	return false
}

// Sync makes the identities match the agents: creates missing ones,
// removes those of agents no longer chosen, then (re)starts the listeners.
func (s *Service) Sync(ctx context.Context) {
	s.syncing.Lock()
	defer s.syncing.Unlock()
	s.mu.Lock()
	set := s.st.Settings
	s.mu.Unlock()
	if set.URL == "" {
		return
	}
	cs := s.creds()
	if cs == nil {
		return
	}
	admin, err := cs.Resolve(ctx, adminRef)
	if err != nil || admin == "" {
		s.setErr("the docs admin token is missing from the vault; connect again")
		return
	}
	var errs []string
	want := map[string]bool{}
	for _, a := range s.agents.List() {
		if !s.wanted(a) {
			continue
		}
		want[a.ID()] = true
		if err := s.ensure(ctx, set, admin, a); err != nil {
			errs = append(errs, a.Spec().Name+": "+err.Error())
		}
	}
	s.mu.Lock()
	var gone []Identity
	for id, x := range s.st.Identities {
		if !want[id] {
			gone = append(gone, x)
		}
	}
	s.mu.Unlock()
	for _, x := range gone {
		_ = s.call(ctx, set.URL, admin, "DELETE", "/agents/"+url.PathEscape(x.ID), nil, nil)
		s.removeConnection(x.Agent)
		s.mu.Lock()
		delete(s.st.Identities, x.Agent)
		_ = s.saveLocked()
		s.mu.Unlock()
	}
	if len(errs) > 0 {
		s.setErr(strings.Join(errs, "; "))
	} else {
		s.setErr("")
	}
	s.restartListeners()
}

func (s *Service) setErr(e string) {
	s.mu.Lock()
	s.err = e
	s.mu.Unlock()
}

// ensure gives agent a an identity and its MCP connection.
func (s *Service) ensure(ctx context.Context, set Settings, admin string, a *agent.Agent) error {
	cs := s.creds()
	if s.mcps() == nil {
		return errors.New("MCP is not available")
	}
	s.mu.Lock()
	id, have := s.st.Identities[a.ID()]
	s.mu.Unlock()
	ref := tokenRef(a.ID())
	if have {
		if tok, err := cs.Resolve(ctx, ref); err == nil && tok != "" {
			// still valid on the server?
			if err := s.call(ctx, set.URL, tok, "GET", "/me", nil, nil); err == nil {
				return s.ensureConnection(a, set, ref)
			}
		}
		// revoked, lost or another server: a fresh one
		_ = s.call(ctx, set.URL, admin, "DELETE", "/agents/"+url.PathEscape(id.ID), nil, nil)
	}
	name := docsName(a)
	var created struct {
		Agent docsAgent `json:"agent"`
		Token string    `json:"token"`
	}
	body := map[string]any{"name": name, "scope": set.Scope, "about": strings.TrimSpace("Crewbase agent " + a.Spec().Name + " — " + a.Spec().Role)}
	err := s.call(ctx, set.URL, admin, "POST", "/workspaces/"+url.PathEscape(set.Workspace)+"/agents", body, &created)
	var ae *APIError
	for i := 2; err != nil && errors.As(err, &ae) && ae.Status == 409 && i < 10; i++ {
		// the name is taken (another Crewbase, or a person-made agent)
		body["name"] = fmt.Sprintf("%s-%d", name, i)
		err = s.call(ctx, set.URL, admin, "POST", "/workspaces/"+url.PathEscape(set.Workspace)+"/agents", body, &created)
	}
	if err != nil {
		return err
	}
	if err := cs.Set(ref, created.Token); err != nil {
		return err
	}
	id = Identity{Agent: a.ID(), ID: created.Agent.ID, Name: created.Agent.Name, After: id.After}
	s.mu.Lock()
	if s.st.Identities == nil {
		s.st.Identities = map[string]Identity{}
	}
	s.st.Identities[a.ID()] = id
	err = s.saveLocked()
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.ensureConnection(a, set, ref)
}

// ensureConnection adds (or points) agent a's MCP server at the docs.
func (s *Service) ensureConnection(a *agent.Agent, set Settings, ref string) error {
	m := s.mcps()
	name := ServerName(a.ID())
	srv := mcp.Server{Name: name, Transport: "http", URL: set.URL + "/mcp", Headers: map[string]string{"Authorization": "Bearer ${" + ref + "}"},
		Scope: a.ID(), Enabled: true}
	if v, ok := m.Get(name); ok {
		if v.URL == srv.URL && v.Scope == srv.Scope && v.Enabled {
			return nil
		}
		_, err := m.Update(name, func(x *mcp.Server) {
			x.URL, x.Headers, x.Scope, x.Enabled, x.Transport = srv.URL, srv.Headers, srv.Scope, true, "http"
		})
		return err
	}
	_, err := m.Add(srv)
	return err
}

// removeConnection drops agent's MCP server and its token.
func (s *Service) removeConnection(agentID string) {
	if m := s.mcps(); m != nil {
		_ = m.Remove(ServerName(agentID))
	}
	if cs := s.creds(); cs != nil {
		_ = cs.Delete(tokenRef(agentID))
	}
}

// ---------------------------------------------------------------- comments → tasks

// Note is a notification of the docs server.
type Note struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
	Page struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		URL   string `json:"url"`
	} `json:"page"`
	ThreadID string `json:"thread_id"`
	Comment  struct {
		ID     string `json:"id"`
		Author struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"author"`
		Body string `json:"body"`
	} `json:"comment"`
	BlockID string `json:"block_id"`
	Quote   string `json:"quote"`
	Prompt  string `json:"prompt"`
}

func (s *Service) stopListeners() {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.mu.Unlock()
}

func (s *Service) restartListeners() {
	s.stopListeners()
	s.mu.Lock()
	set := s.st.Settings
	var ids []Identity
	for _, x := range s.st.Identities {
		ids = append(ids, x)
	}
	if set.URL == "" || !set.Wake || len(ids) == 0 {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.mu.Unlock()
	for _, id := range ids {
		go s.listen(ctx, set.URL, id)
	}
}

// listen long-polls one identity's notifications until ctx ends.
func (s *Service) listen(ctx context.Context, base string, id Identity) {
	cs := s.creds()
	backoff := time.Second
	for ctx.Err() == nil {
		tok, err := cs.Resolve(ctx, tokenRef(id.Agent))
		if err != nil || tok == "" {
			return
		}
		s.mu.Lock()
		after := s.st.Identities[id.Agent].After
		s.mu.Unlock()
		var out struct {
			Notifications []Note `json:"notifications"`
		}
		pctx, cancel := context.WithTimeout(ctx, 50*time.Second)
		err = s.call(pctx, base, tok, "GET", fmt.Sprintf("/agent/notifications?after=%d&wait=30&limit=20", after), nil, &out)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 2*time.Minute)
			continue
		}
		backoff = time.Second
		for _, n := range out.Notifications {
			if err := s.deliver(id, n); err != nil {
				s.c.Log().Warn("agentdocs: could not hand a comment to the agent", "agent", id.Agent, "err", err)
				break // not acknowledged: delivered again next time
			}
			_ = s.call(ctx, base, tok, "POST", "/agent/notifications/ack", map[string]any{"ids": []int64{n.ID}}, nil)
			s.mu.Lock()
			if x, ok := s.st.Identities[id.Agent]; ok && n.ID > x.After {
				x.After = n.ID
				s.st.Identities[id.Agent] = x
				_ = s.saveLocked()
			}
			s.woken++
			s.mu.Unlock()
		}
	}
}

// deliver turns a notification into a task for the agent.
func (s *Service) deliver(id Identity, n Note) error {
	a, ok := s.agents.Get(id.Agent)
	if !ok {
		return nil // the agent is gone: drop it
	}
	ss, err := a.Session()
	if err != nil {
		return err
	}
	_, err = a.Enqueue(agent.Input{Session: ss.ID(), Text: TaskText(n, ServerName(id.Agent)), From: From, Tag: fmt.Sprintf("agentdocs:%d", n.ID)})
	return err
}

// TaskText is what the agent is told when a comment calls it.
func TaskText(n Note, server string) string {
	var b strings.Builder
	who := n.Comment.Author.Name
	if who == "" {
		who = "Someone"
	}
	title := n.Page.Title
	if title == "" {
		title = "a page"
	}
	fmt.Fprintf(&b, "%s called you in a comment on the document 「%s」", who, title)
	if n.Page.URL != "" {
		fmt.Fprintf(&b, " (%s)", n.Page.URL)
	}
	b.WriteString(":\n\n")
	for _, l := range strings.Split(strings.TrimSpace(n.Comment.Body), "\n") {
		b.WriteString("> " + l + "\n")
	}
	if q := strings.TrimSpace(n.Quote); q != "" {
		fmt.Fprintf(&b, "\nThe comment is on this passage: \"%s\"\n", clip(q, 600))
	}
	if p := strings.TrimSpace(n.Prompt); p != "" {
		b.WriteString("\n" + clip(p, 4000) + "\n")
	}
	fmt.Fprintf(&b, "\nUse the %s__… tools: read the page (%s__read_page, page id %s), make the change asked for, "+
		"then reply in the comment thread (%s__comment, thread %s) saying what you did. Reply there even when you cannot do it, and say why.",
		server, server, n.Page.ID, server, n.ThreadID)
	return b.String()
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ---------------------------------------------------------------- page cards

// PageCard is what a conversation shows for a link to a docs page.
type PageCard struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	Title    string `json:"title"`
	Icon     string `json:"icon,omitempty"`
	Updated  int64  `json:"updated,omitempty"` // ms
	Markdown string `json:"markdown"`          // the first blocks
	Total    int    `json:"total"`             // blocks in the page
	Shown    int    `json:"shown"`
}

var pageIDRe = regexp.MustCompile(`^/p/([A-Za-z0-9_-]{4,64})/?$`)

// Page reads the start of a docs page for a card (the admin token reads
// it; only pages of the connected server).
func (s *Service) Page(ctx context.Context, link string, blocks int) (PageCard, error) {
	s.mu.Lock()
	base := s.st.Settings.URL
	s.mu.Unlock()
	if base == "" {
		return PageCard{}, errors.New("no docs server is connected")
	}
	u, err := url.Parse(link)
	if err != nil {
		return PageCard{}, errors.New("not a docs link")
	}
	b, _ := url.Parse(base)
	if b == nil || u.Scheme != b.Scheme || u.Host != b.Host {
		return PageCard{}, errors.New("not a page of the connected docs server")
	}
	m := pageIDRe.FindStringSubmatch(u.Path)
	if m == nil {
		return PageCard{}, errors.New("not a page link")
	}
	cs := s.creds()
	if cs == nil {
		return PageCard{}, errors.New("the vault is not available")
	}
	tok, err := cs.Resolve(ctx, adminRef)
	if err != nil || tok == "" {
		return PageCard{}, errors.New("the docs admin token is missing; connect again")
	}
	var pg struct {
		ID        string  `json:"id"`
		Title     string  `json:"title"`
		Icon      *string `json:"icon"`
		UpdatedAt int64   `json:"updated_at"`
		DeletedAt *int64  `json:"deleted_at"`
	}
	if err := s.call(ctx, base, tok, "GET", "/pages/"+url.PathEscape(m[1]), nil, &pg); err != nil {
		return PageCard{}, err
	}
	if pg.DeletedAt != nil {
		return PageCard{}, &APIError{Status: 404, Message: "the page is in the trash"}
	}
	blocks = min(max(blocks, 1), 40)
	var c struct {
		Markdown string `json:"markdown"`
		Total    int    `json:"total"`
	}
	if err := s.call(ctx, base, tok, "GET", fmt.Sprintf("/pages/%s/content?limit=%d", url.PathEscape(m[1]), blocks), nil, &c); err != nil {
		return PageCard{}, err
	}
	card := PageCard{ID: pg.ID, URL: base + "/p/" + pg.ID, Title: pg.Title, Updated: pg.UpdatedAt, Markdown: clip(c.Markdown, 6000), Total: c.Total, Shown: min(blocks, c.Total)}
	if pg.Icon != nil {
		card.Icon = *pg.Icon
	}
	return card, nil
}
