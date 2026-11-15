// Package mcp connects Crewbase to MCP (Model Context Protocol) servers
// (PRD C-3). Each enabled server is one connection with its own forked
// kernel.Context: the connection initializes, lists the server's tools and
// registers each as "<server>__<tool>" in the tools registry — globally, or
// in one agent's scope. Disposing the connection removes the tools and kills
// the server's process tree.
//
// Transports: stdio (a subprocess; by default confined through the `sandbox`
// service, failing closed when no sandbox can confine it) and Streamable
// HTTP. Env and header values may be credential references resolved
// host-side on every connect; API views mask literal values.
//
// Behaviour (docs/05 §7, §9 rule 4; docs/06 §7; deepseek-harness "mcp"):
//
//   - notifications/tools/list_changed refreshes the tool list; a failed
//     refresh keeps the previous tools.
//   - a dead connection is re-established with exponential backoff; the
//     previous tools stay registered and answer "not connected" meanwhile.
//   - Probe connects without registering anything ("test connection").
//   - agent/created waits (bounded) for that agent's servers to settle.
//
// Sandboxed stdio servers get workspace-write on their working directory
// (the agent's workspace for agent-scoped servers, ${dir}/mcp/<name> for
// global ones; both also get ${dir}/mcp/<name> for caches) and OPEN
// network: MCP servers are connectors to external services, so network
// access is the point. File writes stay confined.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/hedeqiang/crewbase/capabilities/sandbox"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

// ServiceKey is the key of the mcp service.
const ServiceKey = "mcp"

// Transports.
const (
	TransportStdio = "stdio"
	TransportHTTP  = "http"
)

// Trust levels of stdio servers.
const (
	TrustSandboxed = "sandboxed" // default: launched through the sandbox service
	TrustHost      = "host"      // explicit opt-in: runs unconfined on the host
)

// ScopeGlobal makes a server's tools visible to every agent.
const ScopeGlobal = "global"

// TeamScope is the scope prefix of a server a team shares ("team:<id>"):
// one connection, its tools visible to that team's agents only.
const TeamScope = "team:"

// AgentScope is the agent a server is scoped to ("" = global or a team).
func (s Server) AgentScope() string {
	if s.Scope == ScopeGlobal || strings.HasPrefix(s.Scope, TeamScope) {
		return ""
	}
	return s.Scope
}

// TeamOfScope is the team a server is shared with ("" = none).
func (s Server) TeamOfScope() string { return strings.TrimPrefix(s.Scope, TeamScope) }

func (s Server) teamScoped() bool { return strings.HasPrefix(s.Scope, TeamScope) }

// Connection states.
const (
	StateConnecting = "connecting"
	StateReady      = "ready"
	StateError      = "error"
	StateDisabled   = "disabled"
)

// Server is one configured MCP server.
type Server struct {
	// Name is the tool namespace: [a-z0-9_-], at most 32 characters.
	Name      string `json:"name"`
	Transport string `json:"transport"` // stdio | http
	// stdio
	Command []string          `json:"command,omitempty"`
	Env     map[string]string `json:"env,omitempty"` // values may be credential refs
	// http
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"` // values may be credential refs
	// Roots are directories the user grants this server's tools outside
	// the agent workspace (writable in its sandbox, accepted by the path
	// guard). Existing absolute directories in Command are granted too,
	// since the user wrote them there (e.g. server-filesystem <dir>).
	Roots []string `json:"roots,omitempty"`
	// Scope is "global" or an agent id (tools visible to that agent only).
	Scope   string `json:"scope"`
	Enabled bool   `json:"enabled"`
	// Trust is "sandboxed" (default) or "host" for stdio servers; empty for http.
	Trust string `json:"trust,omitempty"`
	// TrustAnnotations lets the server's readOnlyHint lower a tool's
	// category to read. Off by default: annotations are self-reported, so
	// an untrusted server's tools are exec unless Categories says otherwise.
	TrustAnnotations bool `json:"trustAnnotations,omitempty"`
	// Categories overrides the category per tool (keyed by the server's
	// tool name or the namespaced name).
	Categories map[string]string `json:"categories,omitempty"`
	// OAuth turns on sign-in for an http server (MCP authorization).
	OAuth *OAuth `json:"oauth,omitempty"`
}

// knownCategories are the categories a Categories override may name.
var knownCategories = map[string]bool{
	tools.CategoryRead: true, tools.CategoryExec: true, tools.CategoryInstall: true, tools.CategoryWrite: true,
	tools.CategoryPush: true, tools.CategoryDelete: true, tools.CategoryPublish: true, tools.CategoryPayment: true,
	tools.CategoryNone: true,
}

var (
	nameRe   = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	scopeRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	hdrKeyRe = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
)

// ErrInvalid wraps validation failures.
var ErrInvalid = errors.New("invalid server")

func invalid(f string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(f, a...))
}

// Normalize fills defaults and validates s.
func (s Server) Normalize() (Server, error) {
	if !nameRe.MatchString(s.Name) {
		return s, invalid("name must match [a-z0-9_-]{1,32}")
	}
	if s.Scope == "" {
		s.Scope = ScopeGlobal
	}
	if s.Scope != ScopeGlobal && !scopeRe.MatchString(s.Scope) && !(strings.HasPrefix(s.Scope, TeamScope) && scopeRe.MatchString(strings.TrimPrefix(s.Scope, TeamScope))) {
		return s, invalid("scope must be %q, an agent id or team:<id>", ScopeGlobal)
	}
	for k, v := range s.Categories {
		if k == "" || !knownCategories[v] {
			return s, invalid("categories: %q → %q is not a known category", k, v)
		}
	}
	switch s.Transport {
	case TransportStdio:
		if len(s.Command) == 0 || s.Command[0] == "" {
			return s, invalid("stdio transport needs a command")
		}
		if s.URL != "" || len(s.Headers) > 0 || s.OAuth != nil {
			return s, invalid("url, headers and oauth apply to the http transport only")
		}
		switch s.Trust {
		case "":
			s.Trust = TrustSandboxed
		case TrustSandboxed, TrustHost:
		default:
			return s, invalid("trust must be %q or %q", TrustSandboxed, TrustHost)
		}
		for k := range s.Env {
			if !envKeyRe.MatchString(k) {
				return s, invalid("bad env name %q", k)
			}
		}
	case TransportHTTP:
		if len(s.Command) > 0 || len(s.Env) > 0 {
			return s, invalid("command and env apply to the stdio transport only")
		}
		if s.Trust != "" {
			return s, invalid("trust applies to the stdio transport only")
		}
		if s.OAuth != nil && s.OAuth.ClientSecret != "" && s.OAuth.ClientID == "" {
			return s, invalid("an oauth client secret needs a client id")
		}
		u, err := url.Parse(s.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return s, invalid("http transport needs an http(s) url")
		}
		for k := range s.Headers {
			if !hdrKeyRe.MatchString(k) {
				return s, invalid("bad header name %q", k)
			}
		}
	default:
		return s, invalid("transport must be %q or %q", TransportStdio, TransportHTTP)
	}
	return s, nil
}

func (s Server) clone() Server {
	s.Command = append([]string(nil), s.Command...)
	s.Env = cloneMap(s.Env)
	s.Headers = cloneMap(s.Headers)
	s.Categories = cloneMap(s.Categories)
	return s
}

func cloneMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Masked returns s with literal env/header/url secrets hidden.
func (s Server) Masked() Server {
	s = s.clone()
	s.Env, s.Headers = maskMap(s.Env), maskMap(s.Headers)
	if s.URL != "" {
		s.URL = maskURL(s.URL)
	}
	return s
}

// Status of one server's connection.
type Status struct {
	State string `json:"state"` // connecting | ready | error | disabled
	Tools int    `json:"tools"`
	Error string `json:"error,omitempty"`
	PID   int    `json:"pid,omitempty"`
	// Enforcement is the sandbox enforcement of a stdio server (full,
	// partial, none for trust=host).
	Enforcement string    `json:"enforcement,omitempty"`
	Since       time.Time `json:"since,omitzero"`
	// RefreshError is the last failed tools/list refresh (tools kept).
	RefreshError string `json:"refreshError,omitempty"`
	// LoginURL is where the person signs in (state "login", OAuth servers).
	LoginURL string `json:"loginUrl,omitempty"`
}

// ToolInfo describes one MCP tool as registered.
type ToolInfo struct {
	Name        string `json:"name"`     // namespaced, model-facing
	Original    string `json:"original"` // the server's tool name
	Description string `json:"description,omitempty"`
	Category    string `json:"category"`
}

// View is a server as shown by the API: masked config plus status.
type View struct {
	Server
	Source string `json:"source"` // config | runtime
	Status Status `json:"status"`
}

// StatusEvent is emitted on every status change.
type StatusEvent struct {
	Server string `json:"server"`
	Status Status `json:"status"`
}

// StatusChanged reports connection state changes (for the UI).
var StatusChanged = kernel.NewEmit[StatusEvent]("mcp/status")

// Errors returned by the service.
var (
	ErrNotFound = errors.New("mcp server not found")
	ErrExists   = errors.New("mcp server name already in use")
	ErrDeclared = errors.New("server is declared in the plugin config; disable it instead")
)

// Config of the mcp plugin.
type Config struct {
	Dir     string   `json:"dir"` // mcp.json and per-server dirs live here
	Servers []Server `json:"servers"`
	// Durations (Go syntax). Defaults: 30s, 5m, 10s, 500ms, 30s.
	ConnectTimeout string `json:"connectTimeout,omitempty"`
	CallTimeout    string `json:"callTimeout,omitempty"`
	ReadyWait      string `json:"readyWait,omitempty"` // agent/created bound
	BackoffMin     string `json:"backoffMin,omitempty"`
	BackoffMax     string `json:"backoffMax,omitempty"`
}

type timing struct {
	connect, call, readyWait, backoffMin, backoffMax time.Duration
}

func (cfg Config) timing() (timing, error) {
	t := timing{30 * time.Second, 5 * time.Minute, 10 * time.Second, 500 * time.Millisecond, 30 * time.Second}
	for _, x := range []struct {
		s   string
		dst *time.Duration
		key string
	}{{cfg.ConnectTimeout, &t.connect, "connectTimeout"}, {cfg.CallTimeout, &t.call, "callTimeout"},
		{cfg.ReadyWait, &t.readyWait, "readyWait"}, {cfg.BackoffMin, &t.backoffMin, "backoffMin"}, {cfg.BackoffMax, &t.backoffMax, "backoffMax"}} {
		if x.s == "" {
			continue
		}
		d, err := time.ParseDuration(x.s)
		if err != nil || d <= 0 {
			return t, fmt.Errorf("mcp: %s: invalid duration %q", x.key, x.s)
		}
		*x.dst = d
	}
	if t.backoffMax < t.backoffMin {
		t.backoffMax = t.backoffMin
	}
	return t, nil
}

type entry struct {
	srv        Server
	fromConfig bool // declared in plugin config (cannot be deleted)
	inFile     bool // persisted in mcp.json
	conn       *conn
	why        string // why there is no connection although enabled
}

// Service is the `mcp` service.
type Service struct {
	c     *kernel.Context
	dir   string
	tools *tools.Registry
	t     timing
	// HTTPClient is the base client for the http transport (tests).
	HTTPClient *http.Client

	op sync.Mutex // serializes mutations (start/stop/persist)

	mu           sync.Mutex
	entries      map[string]*entry
	closed       bool
	redirectBase string // gateway origin for OAuth callbacks
	logins       logins
}

func (s *Service) agents() *agent.Registry {
	r, _ := kernel.Lookup[*agent.Registry](s.c, agent.ServiceKey)
	return r
}

// agentReadOnly reports whether srv is scoped to an agent whose run mode is
// read-only: its sandbox must then be read-only too.
func (s *Service) agentReadOnly(srv Server) bool {
	if srv.AgentScope() == "" {
		return false
	}
	reg := s.agents()
	if reg == nil {
		return false
	}
	a, ok := reg.Get(srv.Scope)
	return ok && sandbox.EffectiveRunMode(a.Mode()) == agent.ModeReadOnly
}

// onAgentUpdated restarts the agent's sandboxed stdio servers when its run
// mode moved into or out of read-only, so the sandbox policy follows.
func (s *Service) onAgentUpdated(spec agent.Spec) {
	s.op.Lock()
	defer s.op.Unlock()
	s.mu.Lock()
	var todo []*entry
	for _, e := range s.entries {
		if e.srv.Scope == spec.ID && e.conn != nil && e.srv.Transport == TransportStdio && e.srv.Trust == TrustSandboxed {
			todo = append(todo, e)
		}
	}
	s.mu.Unlock()
	for _, e := range todo {
		s.mu.Lock()
		c := e.conn
		srv := e.srv
		s.mu.Unlock()
		if c == nil || c.readOnly == s.agentReadOnly(srv) {
			continue
		}
		s.stopLocked(e)
		s.startLocked(e, nil)
	}
}

func (s *Service) sandbox() sandbox.Sandbox {
	sb, _ := kernel.Lookup[sandbox.Sandbox](s.c, sandbox.ServiceKey)
	return sb
}

func (s *Service) resolver() Resolver {
	cr, ok := kernel.Lookup[*credentials.Service](s.c, credentials.ServiceKey)
	if !ok {
		return nil
	}
	return cr
}

func (s *Service) file() string {
	if s.dir == "" {
		return ""
	}
	return filepath.Join(s.dir, "mcp.json")
}

// serverDir is the dedicated directory of a server (cwd of global servers,
// cache root of all sandboxed ones).
func (s *Service) serverDir(name string) string {
	return filepath.Join(s.dir, "mcp", name)
}

// ServerDir is the server's own directory: writable inside its sandbox
// whatever its scope, and never an agent's workspace, so it is where a
// server should keep its sign-in state (catalog presets point it there).
func (s *Service) ServerDir(name string) string { return s.serverDir(name) }

type fileFormat struct {
	Servers []Server `json:"servers"`
}

func (s *Service) load(declared []Server) error {
	for _, d := range declared {
		n, err := d.Normalize()
		if err != nil {
			return fmt.Errorf("mcp: config server %q: %w", d.Name, err)
		}
		if _, dup := s.entries[n.Name]; dup {
			return fmt.Errorf("mcp: config server %q: %w", n.Name, ErrExists)
		}
		s.entries[n.Name] = &entry{srv: n, fromConfig: true}
	}
	f := s.file()
	if f == "" {
		return nil
	}
	b, err := os.ReadFile(f)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var ff fileFormat
	if err := json.Unmarshal(b, &ff); err != nil {
		return fmt.Errorf("mcp: %s: %w", f, err)
	}
	for _, x := range ff.Servers {
		n, err := x.Normalize()
		if err != nil {
			s.c.Log().Warn("mcp: skipping invalid server in mcp.json", "server", x.Name, "err", err)
			continue
		}
		e := s.entries[n.Name]
		if e == nil {
			e = &entry{}
			s.entries[n.Name] = e
		}
		e.srv, e.inFile = n, true // runtime edits overlay the config
	}
	return nil
}

// persist writes the runtime overlay. Callers hold s.op.
func (s *Service) persist() error {
	f := s.file()
	if f == "" {
		return nil
	}
	s.mu.Lock()
	var ff fileFormat
	for _, e := range s.entries {
		if e.inFile {
			ff.Servers = append(ff.Servers, e.srv.clone())
		}
	}
	s.mu.Unlock()
	sort.Slice(ff.Servers, func(i, j int) bool { return ff.Servers[i].Name < ff.Servers[j].Name })
	if ff.Servers == nil {
		ff.Servers = []Server{}
	}
	b, err := json.MarshalIndent(ff, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	tmp := f + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f)
}

// ---------------------------------------------------------------- queries

func (s *Service) viewLocked(e *entry) View {
	v := View{Server: e.srv.Masked(), Source: "runtime"}
	if e.fromConfig {
		v.Source = "config"
	}
	switch {
	case !e.srv.Enabled:
		v.Status = Status{State: StateDisabled}
	case e.conn != nil:
		v.Status = e.conn.status()
	default:
		v.Status = Status{State: StateError, Error: e.why}
	}
	return v
}

// List returns every server (masked) with its status, sorted by name.
func (s *Service) List() []View {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]View, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, s.viewLocked(e))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Get returns one server view.
func (s *Service) Get(name string) (View, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[name]
	if e == nil {
		return View{}, false
	}
	return s.viewLocked(e), true
}

// Status returns a server's connection status.
func (s *Service) Status(name string) (Status, bool) {
	v, ok := s.Get(name)
	return v.Status, ok
}

// Tools lists the tools currently registered for a server.
func (s *Service) Tools(name string) ([]ToolInfo, bool) {
	s.mu.Lock()
	e := s.entries[name]
	var c *conn
	if e != nil {
		c = e.conn
	}
	s.mu.Unlock()
	if e == nil {
		return nil, false
	}
	if c == nil {
		return []ToolInfo{}, true
	}
	return c.toolInfos(), true
}

// ---------------------------------------------------------------- mutations

// Add validates and adds a server, starting it if enabled.
func (s *Service) Add(srv Server) (View, error) {
	n, err := srv.Normalize()
	if err != nil {
		return View{}, err
	}
	s.op.Lock()
	defer s.op.Unlock()
	s.mu.Lock()
	if _, ok := s.entries[n.Name]; ok {
		s.mu.Unlock()
		return View{}, ErrExists
	}
	e := &entry{srv: n, inFile: true}
	s.entries[n.Name] = e
	s.mu.Unlock()
	if err := s.persist(); err != nil {
		s.mu.Lock()
		delete(s.entries, n.Name)
		s.mu.Unlock()
		return View{}, err
	}
	if n.Enabled {
		s.startLocked(e, nil)
	}
	v, _ := s.Get(n.Name)
	return v, nil
}

// Update edits a server. fn receives a copy with the real (unmasked)
// values; masked values echoed back by a client keep their current value.
// The connection restarts when the server stays enabled.
func (s *Service) Update(name string, fn func(*Server)) (View, error) {
	s.op.Lock()
	defer s.op.Unlock()
	s.mu.Lock()
	e := s.entries[name]
	if e == nil {
		s.mu.Unlock()
		return View{}, ErrNotFound
	}
	cur := e.srv.clone()
	s.mu.Unlock()

	next := cur.clone()
	fn(&next)
	next.Env = mergeMasked(cur.Env, next.Env)
	next.Headers = mergeMasked(cur.Headers, next.Headers)
	if next.URL != cur.URL && next.URL == maskURL(cur.URL) {
		next.URL = cur.URL
	}
	if next.Name != cur.Name {
		return View{}, invalid("a server cannot be renamed")
	}
	n, err := next.Normalize()
	if err != nil {
		return View{}, err
	}
	s.stopLocked(e)
	s.mu.Lock()
	prev, prevIn := e.srv, e.inFile
	e.srv, e.inFile, e.why = n, true, ""
	s.mu.Unlock()
	if err := s.persist(); err != nil {
		s.mu.Lock()
		e.srv, e.inFile = prev, prevIn
		s.mu.Unlock()
		if prev.Enabled {
			s.startLocked(e, nil)
		}
		return View{}, err
	}
	if n.Enabled {
		s.startLocked(e, nil)
	}
	s.emit(name)
	v, _ := s.Get(name)
	return v, nil
}

// SetEnabled enables or disables a server.
func (s *Service) SetEnabled(name string, on bool) (View, error) {
	return s.Update(name, func(x *Server) { x.Enabled = on })
}

// Remove stops and deletes a runtime-added server.
func (s *Service) Remove(name string) error {
	s.op.Lock()
	defer s.op.Unlock()
	s.mu.Lock()
	e := s.entries[name]
	s.mu.Unlock()
	if e == nil {
		return ErrNotFound
	}
	if e.fromConfig {
		return ErrDeclared
	}
	s.stopLocked(e)
	s.mu.Lock()
	delete(s.entries, name)
	s.mu.Unlock()
	if err := s.persist(); err != nil {
		return err
	}
	_ = s.SignOut(name) // a removed server's sign-in token goes too
	s.dropOwnSecrets(e.srv)
	s.emit(name)
	return nil
}

// SecretRef is the keychain reference of a secret that belongs to one
// server (e.g. a token pasted when installing a catalog preset). Secrets
// stored under it are deleted with the server; refs the person typed
// themselves (keychain:crewbase/github) are shared and left alone.
func SecretRef(server, key string) string {
	return "keychain:crewbase/mcp-" + server + "-" + key
}

// dropOwnSecrets deletes the SecretRef values srv's env and headers name.
func (s *Service) dropOwnSecrets(srv Server) {
	cs := s.creds()
	if cs == nil {
		return
	}
	prefix := SecretRef(srv.Name, "")
	for _, m := range []map[string]string{srv.Env, srv.Headers} {
		for _, v := range m {
			refs := inlineRef.FindAllStringSubmatch(v, -1)
			if IsRef(v) {
				refs = append(refs, []string{v, v})
			}
			for _, r := range refs {
				if strings.HasPrefix(r[1], prefix) {
					_ = cs.Delete(r[1])
				}
			}
		}
	}
}

// Probe connects to srv without registering anything and returns the
// tools it would register. srv need not be saved.
func (s *Service) Probe(ctx context.Context, srv Server) ([]ToolInfo, error) {
	n, err := srv.Normalize()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.t.connect)
	defer cancel()
	ws, err := s.workdir(n)
	if err != nil {
		return nil, err
	}
	sess, err := s.dial(ctx, n, ws, nil)
	if err != nil {
		return nil, err
	}
	defer sess.close()
	list, err := listTools(ctx, sess.cs)
	if err != nil {
		return nil, sess.scrubErr(err)
	}
	return describe(n, list), nil
}

// ProbeNamed probes a configured server.
func (s *Service) ProbeNamed(ctx context.Context, name string) ([]ToolInfo, error) {
	s.mu.Lock()
	e := s.entries[name]
	var srv Server
	if e != nil {
		srv = e.srv.clone()
	}
	s.mu.Unlock()
	if e == nil {
		return nil, ErrNotFound
	}
	return s.Probe(ctx, srv)
}

// WaitSettled waits until every enabled server scoped to agentID has made
// its first connection attempt (ready or error), or until ctx ends.
func (s *Service) WaitSettled(ctx context.Context, agentID string) {
	s.mu.Lock()
	var chans []<-chan struct{}
	for _, e := range s.entries {
		if e.srv.Scope == agentID && e.srv.Enabled && e.conn != nil {
			chans = append(chans, e.conn.settled)
		}
	}
	s.mu.Unlock()
	for _, ch := range chans {
		select {
		case <-ch:
		case <-ctx.Done():
			return
		}
	}
}

func (s *Service) emit(name string) {
	st, ok := s.Status(name)
	if !ok {
		st = Status{State: StateDisabled}
	}
	StatusChanged.Emit(s.c, StatusEvent{Server: name, Status: st})
}

// ---------------------------------------------------------------- start / stop

// startLocked starts e's connection. Callers hold s.op. For agent-scoped
// servers the connection forks the agent's scope (a may be given, else it
// is looked up); without the agent loaded the server waits for
// agent/created.
func (s *Service) startLocked(e *entry, a *agent.Agent) {
	s.mu.Lock()
	if s.closed || e.conn != nil || !e.srv.Enabled {
		s.mu.Unlock()
		return
	}
	srv := e.srv.clone()
	s.mu.Unlock()

	parent := s.c
	if srv.AgentScope() != "" {
		if a == nil {
			if reg := s.agents(); reg != nil {
				a, _ = reg.Get(srv.Scope)
			}
		}
		if a == nil || a.Ctx() == nil || a.Ctx().Disposed() {
			s.mu.Lock()
			e.why = "agent " + srv.Scope + " is not loaded"
			s.mu.Unlock()
			s.emit(srv.Name)
			return
		}
		parent = a.Ctx()
	}
	c := newConn(s, e, srv, parent.Fork(""))
	c.readOnly = s.agentReadOnly(srv)
	s.mu.Lock()
	e.conn, e.why = c, ""
	s.mu.Unlock()
	c.start()
}

// stopLocked disposes e's connection (removing its tools and killing its
// process tree). Callers hold s.op.
func (s *Service) stopLocked(e *entry) {
	s.mu.Lock()
	c := e.conn
	e.conn = nil
	s.mu.Unlock()
	if c != nil {
		c.ctx.Dispose()
	}
}

// detach is called when a connection's context is disposed from outside
// (agent deleted, agents plugin unloaded).
func (s *Service) detach(e *entry, c *conn) {
	s.mu.Lock()
	if e.conn != c {
		s.mu.Unlock()
		return
	}
	e.conn = nil
	if e.srv.AgentScope() != "" {
		e.why = "agent " + e.srv.Scope + " is not loaded"
	} else {
		e.why = "connection closed"
	}
	name := e.srv.Name
	s.mu.Unlock()
	s.emit(name)
}

func (s *Service) onAgentCreated(ctx context.Context, a *agent.Agent) {
	s.op.Lock()
	s.mu.Lock()
	var todo []*entry
	for _, e := range s.entries {
		if e.srv.Scope == a.ID() && e.srv.Enabled && e.conn == nil {
			todo = append(todo, e)
		}
	}
	s.mu.Unlock()
	for _, e := range todo {
		s.startLocked(e, a)
	}
	s.op.Unlock()
	wctx, cancel := context.WithTimeout(ctx, s.t.readyWait)
	defer cancel()
	s.WaitSettled(wctx, a.ID())
}

func (s *Service) close() {
	s.op.Lock()
	defer s.op.Unlock()
	s.mu.Lock()
	s.closed = true
	es := make([]*entry, 0, len(s.entries))
	for _, e := range s.entries {
		es = append(es, e)
	}
	s.mu.Unlock()
	for _, e := range es {
		s.stopLocked(e)
	}
}

// ---------------------------------------------------------------- plugin

// Plugin provides the `mcp` service.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/mcp",
	Inject:      []string{tools.ServiceKey},
	Optional:    []string{sandbox.ServiceKey, credentials.ServiceKey, agent.ServiceKey},
	Description: "MCP connectors: stdio (sandboxed) and Streamable HTTP servers as namespaced tools (mcp service).",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		t, err := cfg.timing()
		if err != nil {
			return err
		}
		dir := cfg.Dir
		if dir == "" {
			if dir, err = os.MkdirTemp("", "crewbase-mcp-"); err != nil {
				return err
			}
		}
		s := &Service{c: c, dir: dir, t: t, tools: kernel.Use[*tools.Registry](c, tools.ServiceKey), entries: map[string]*entry{}}
		if cfg.Dir == "" {
			c.Defer(func() { _ = os.RemoveAll(dir) })
		}
		if err := s.load(cfg.Servers); err != nil {
			return err
		}
		// Agent-scoped connections fork agent scopes, not this context:
		// stop everything explicitly when the plugin unloads.
		c.Defer(s.close)
		agent.Created.Listen(c, func(ctx context.Context, a *agent.Agent) (struct{}, error) {
			s.onAgentCreated(ctx, a)
			return struct{}{}, nil
		})
		agent.Updated.Listen(c, func(spec agent.Spec) {
			c.Go(func(context.Context) { s.onAgentUpdated(spec) })
		})
		if err := c.Provide(ServiceKey, s); err != nil {
			return err
		}
		s.op.Lock()
		s.mu.Lock()
		es := make([]*entry, 0, len(s.entries))
		for _, e := range s.entries {
			es = append(es, e)
		}
		s.mu.Unlock()
		for _, e := range es {
			s.startLocked(e, nil)
		}
		s.op.Unlock()
		return nil
	},
})

// GrantedRoots returns the server's explicit roots plus existing absolute
// directories named in its command line.
func GrantedRoots(srv Server) []string {
	seen := map[string]bool{}
	var out []string
	add := func(d string) {
		d = filepath.Clean(d)
		if !filepath.IsAbs(d) || seen[d] || d == "/" {
			return
		}
		seen[d] = true
		out = append(out, d)
	}
	for _, r := range srv.Roots {
		add(r)
	}
	if len(srv.Command) > 1 {
		for _, a := range srv.Command[1:] {
			if fi, err := os.Stat(a); err == nil && fi.IsDir() && filepath.IsAbs(a) {
				add(a)
			}
		}
	}
	return out
}
