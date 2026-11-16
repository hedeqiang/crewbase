// Package mcpserver is Crewbase's own MCP server for the coding CLIs it
// drives over ACP (providers/llm/acp). Those CLIs run their own tools, so
// without it an agent whose model is a CLI cannot use Crewbase's tools
// (computer use, Crewbase's browser). The server offers an allow-list of
// them (Config.Tools, default computer_* and browser_*), and every call
// runs through tools.Registry.Run exactly as if the agent's own model had
// made it: rules, approval cards, run-mode guards, the computer-tools
// policy, logging and session events all apply.
//
// Transport: MCP Streamable HTTP (https://modelcontextprotocol.io/specification),
// mounted on the gateway at Path (http://127.0.0.1:<port>/mcp). Only POST
// with JSON responses is served (no SSE stream: GET and DELETE answer 405,
// which the spec allows); the server is stateless (no Mcp-Session-Id).
// Methods: initialize, notifications/initialized, ping, tools/list,
// tools/call and notifications/cancelled; anything else is method not
// found.
//
// Credentials: the ACP adapter opens a Grant per ACP session (Open) bound
// to (agent, Crewbase session, workspace, own workspace); the run mode is
// set per turn (Grant.Turn). A grant carries a random bearer token (only
// its SHA-256 is kept); the token authenticates /mcp only, never another
// gateway route (and gateway tokens are not accepted here). Revoke
// invalidates it and cancels its calls. Requests carrying an Origin header
// that is not loopback are refused (DNS rebinding).
//
// Calls run only while the grant has an active turn; ending or cancelling
// the turn (or revoking) cancels calls still running.
package mcpserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// ServiceKey is the key of the MCP server service (*Service).
const ServiceKey = "mcp-server"

// Path is where the server is mounted on the gateway.
const Path = "/mcp"

// ServerName is the MCP server name given to CLIs; their tools then appear
// as e.g. mcp__crewbase__computer_act (Claude Code).
const ServerName = "crewbase"

// DefaultTools are the tool name patterns offered by default (path.Match
// globs): what CLIs cannot do themselves. Shell, file and other tools the
// CLIs have are never offered.
var DefaultTools = []string{"computer_*", "browser_*"}

// Config of the plugin.
type Config struct {
	// Tools lists the name patterns (path.Match) of the tools offered;
	// empty = DefaultTools. Only tools registered and visible to the agent
	// are listed.
	Tools []string `json:"tools,omitempty"`
}

// Binding is what a grant acts as.
type Binding struct {
	Agent   string
	Session string // Crewbase session id
	// Workspace and Mode are fallbacks used when the agent registry is
	// unavailable; normally the agent's current workspace and mode apply.
	Workspace string
	Mode      string
	// Own: Workspace is the agent's own workspace (llm.Request.OwnWorkspace).
	Own bool
}

// Service is the MCP server.
type Service struct {
	c        *kernel.Context
	reg      *tools.Registry
	patterns []string
	addr     func() string // gateway address

	mu     sync.Mutex
	grants map[string]*Grant // sha256(token) hex →
}

func newService(c *kernel.Context, reg *tools.Registry, cfg Config, addr func() string) (*Service, error) {
	pats := cfg.Tools
	if len(pats) == 0 {
		pats = DefaultTools
	}
	for _, p := range pats {
		if _, err := path.Match(p, ""); err != nil {
			return nil, fmt.Errorf("mcp-server: bad tool pattern %q: %w", p, err)
		}
	}
	return &Service{c: c, reg: reg, patterns: slices.Clone(pats), addr: addr, grants: map[string]*Grant{}}, nil
}

// Offered reports whether name matches the configured allow-list.
func (s *Service) Offered(name string) bool {
	for _, p := range s.patterns {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

// URL is the server's address for local clients ("" while the gateway is
// not listening).
func (s *Service) URL() string {
	a := ""
	if s.addr != nil {
		a = s.addr()
	}
	host, port, err := net.SplitHostPort(a)
	if err != nil || port == "" || port == "0" {
		return ""
	}
	if ip := net.ParseIP(host); host == "" || ip == nil || ip.IsUnspecified() || ip.IsLoopback() {
		host = "127.0.0.1"
	}
	return (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, port), Path: Path}).String()
}

// Open mints a grant for b. The caller must Revoke it when the ACP session
// (or the adapter) ends.
func (s *Service) Open(b Binding) (*Grant, error) {
	if b.Agent == "" || b.Session == "" {
		return nil, errors.New("mcp-server: a grant needs an agent and a session")
	}
	u := s.URL()
	if u == "" {
		return nil, errors.New("mcp-server: the gateway is not listening")
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	tok := hex.EncodeToString(buf)
	g := &Grant{s: s, b: b, url: u, token: tok, hash: hashToken(tok), mode: b.Mode, inflight: map[string]context.CancelFunc{}}
	s.mu.Lock()
	s.grants[g.hash] = g
	s.mu.Unlock()
	return g, nil
}

// Grants is the number of live grants.
func (s *Service) Grants() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.grants)
}

func hashToken(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

// lookup finds the live grant of a bearer token.
func (s *Service) lookup(tok string) *Grant {
	if tok == "" {
		return nil
	}
	h := hashToken(tok)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.grants[h]
}

// revokeAll ends every grant (plugin unload).
func (s *Service) revokeAll() {
	s.mu.Lock()
	gs := make([]*Grant, 0, len(s.grants))
	for _, g := range s.grants {
		gs = append(gs, g)
	}
	s.mu.Unlock()
	for _, g := range gs {
		g.Revoke()
	}
}

// visible lists the offered tools agent may call in a session of kind.
func (s *Service) visible(agentID, kind string) []*tools.Tool {
	var out []*tools.Tool
	for _, t := range s.reg.Visible(agentID) {
		if s.Offered(t.Name) && availableIn(t, kind) {
			out = append(out, t)
		}
	}
	slices.SortFunc(out, func(a, b *tools.Tool) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func availableIn(t *tools.Tool, kind string) bool {
	return kind == "" || len(t.SessionKinds) == 0 || slices.Contains(t.SessionKinds, kind)
}

// Grant is one ACP session's access to the server.
type Grant struct {
	s     *Service
	b     Binding
	url   string
	token string
	hash  string

	mu       sync.Mutex
	mode     string
	turn     context.Context // nil between turns
	gen      int
	inflight map[string]context.CancelFunc // JSON-RPC id → cancel
	seq      int
	listed   []string // tool names the client was last given
	revoked  bool
}

// Server returns the name, URL and bearer token to give the CLI. The token
// must never be logged.
func (g *Grant) Server() (name, url, token string) { return ServerName, g.url, g.token }

// Binding returns what the grant acts as.
func (g *Grant) Binding() Binding { return g.b }

// Turn makes calls possible while ctx lives, as run mode mode ("" keeps the
// previous). Cancelling ctx, or calling end, cancels calls still running;
// after end calls are refused until the next Turn.
func (g *Grant) Turn(ctx context.Context, mode string) (end func()) {
	g.mu.Lock()
	g.gen++
	gen := g.gen
	g.turn = ctx
	if mode != "" {
		g.mode = mode
	}
	g.mu.Unlock()
	return func() {
		g.mu.Lock()
		if g.gen == gen {
			g.turn = nil
		}
		cancels := g.cancelsLocked()
		g.mu.Unlock()
		for _, c := range cancels {
			c()
		}
	}
}

func (g *Grant) cancelsLocked() []context.CancelFunc {
	out := make([]context.CancelFunc, 0, len(g.inflight))
	for _, c := range g.inflight {
		out = append(out, c)
	}
	return out
}

// Revoke invalidates the token and cancels running calls. Idempotent.
func (g *Grant) Revoke() {
	g.s.mu.Lock()
	if g.s.grants[g.hash] == g {
		delete(g.s.grants, g.hash)
	}
	g.s.mu.Unlock()
	g.mu.Lock()
	g.revoked = true
	g.turn = nil
	cancels := g.cancelsLocked()
	g.mu.Unlock()
	for _, c := range cancels {
		c()
	}
}

// Allows reports whether tool is one this grant offers now (allow-listed,
// registered and visible to the agent).
func (g *Grant) Allows(tool string) bool {
	if !g.s.Offered(tool) {
		return false
	}
	t, ok := g.s.reg.Get(g.b.Agent, tool)
	return ok && availableIn(t, g.kind())
}

// Stale reports whether the tools offered now differ from those the client
// was last given (e.g. computer use was switched on): the client should
// start a new session to see them. False before the client listed tools.
func (g *Grant) Stale() bool {
	g.mu.Lock()
	listed := g.listed
	g.mu.Unlock()
	if listed == nil {
		return false
	}
	return !slices.Equal(listed, g.names())
}

func (g *Grant) names() []string {
	ts := g.s.visible(g.b.Agent, g.kind())
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name
	}
	return out
}

func (g *Grant) kind() string {
	if ss := g.session(); ss != nil {
		return ss.Meta().Kind
	}
	return ""
}

func (g *Grant) session() *session.Session {
	store, ok := kernel.Lookup[*session.Store](g.s.c, session.ServiceKey)
	if !ok {
		return nil
	}
	ss, err := store.Get(g.b.Session)
	if err != nil {
		return nil
	}
	return ss
}

// effective returns the workspace and run mode a call runs with: the
// agent's current ones (a host-mode grant that expired applies at once),
// never looser than the turn's mode.
func (g *Grant) effective() (workspace, mode string, err error) {
	g.mu.Lock()
	mode = g.mode
	g.mu.Unlock()
	workspace = g.b.Workspace
	if agents, ok := kernel.Lookup[*agent.Registry](g.s.c, agent.ServiceKey); ok {
		a, ok := agents.Get(g.b.Agent)
		if !ok {
			return "", "", errors.New("the agent no longer exists")
		}
		workspace = a.Workspace()
		mode = agent.Stricter(a.Mode(), mode)
	}
	if mode == "" {
		mode = agent.ModeSandbox
	}
	return workspace, mode, nil
}

// begin registers a call under the active turn; ctx is cancelled when the
// HTTP request, the turn or the grant ends.
func (g *Grant) begin(parent context.Context, id string) (context.Context, func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.revoked {
		return nil, nil, errRevoked
	}
	if g.turn == nil || g.turn.Err() != nil {
		return nil, nil, errNoTurn
	}
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(g.turn, cancel)
	g.seq++
	key := id
	if key == "" {
		key = fmt.Sprintf("#%d", g.seq)
	}
	if _, dup := g.inflight[key]; dup {
		key = fmt.Sprintf("%s#%d", key, g.seq)
	}
	g.inflight[key] = cancel
	return ctx, func() {
		stop()
		cancel()
		g.mu.Lock()
		delete(g.inflight, key)
		g.mu.Unlock()
	}, nil
}

// cancel cancels the running call with JSON-RPC id (notifications/cancelled).
func (g *Grant) cancel(id string) {
	g.mu.Lock()
	c := g.inflight[id]
	g.mu.Unlock()
	if c != nil {
		c()
	}
}

var (
	errRevoked = errors.New("this Crewbase session has ended")
	errNoTurn  = errors.New("Crewbase tools can only be used while Crewbase is running a turn for this agent")
)

// ---------------------------------------------------------------- plugin

// Plugin mounts the server on the gateway and provides *Service.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/mcp-server",
	Inject:      []string{tools.ServiceKey, gateway.ServiceKey, session.ServiceKey},
	Description: "Crewbase's computer_* / browser_* tools as an MCP server for ACP coding CLIs.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		gw := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		s, err := newService(c, kernel.Use[*tools.Registry](c, tools.ServiceKey), cfg, gw.Addr)
		if err != nil {
			return err
		}
		c.Defer(s.revokeAll)
		gw.Mount(c, Path, &handler{s: s, host: c.Host()})
		return c.Provide(ServiceKey, s)
	},
})
