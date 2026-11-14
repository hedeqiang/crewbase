package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/sandbox"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MaxTools bounds how many tools one server may register.
const MaxTools = 256

// InheritEnv is the host environment passed to stdio servers; everything
// else (API keys, tokens) stays out unless the server's env adds it.
var InheritEnv = []string{"PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "LC_CTYPE", "TERM", "TMPDIR", "SHELL", "TZ"}

// conn is one enabled server's supervised connection.
type conn struct {
	s       *Service
	e       *entry
	srv     Server
	ctx     *kernel.Context
	settled chan struct{} // closed after the first connection attempt
	settle  sync.Once
	refresh chan struct{}
	// readOnly: the scoped agent was read-only when this connection
	// started (its sandbox policy is read-only).
	readOnly bool

	mu      sync.Mutex
	st      Status
	cur     *session
	infos   []ToolInfo
	toolCtx *kernel.Context
}

func newConn(s *Service, e *entry, srv Server, ctx *kernel.Context) *conn {
	c := &conn{s: s, e: e, srv: srv, ctx: ctx, settled: make(chan struct{}), refresh: make(chan struct{}, 1)}
	c.st = Status{State: StateConnecting, Since: time.Now().UTC()}
	ctx.Defer(func() { s.detach(e, c) })
	// Runs after the supervising goroutine returned: drops the tools even
	// if they were installed after Dispose snapshotted the children.
	ctx.Defer(func() {
		c.mu.Lock()
		tc := c.toolCtx
		c.toolCtx, c.infos = nil, nil
		c.mu.Unlock()
		if tc != nil {
			tc.Dispose()
		}
	})
	return c
}

func (c *conn) start() {
	c.s.emit(c.srv.Name)
	c.ctx.Go(c.run)
}

func (c *conn) status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.st
}

func (c *conn) toolInfos() []ToolInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ToolInfo{}, c.infos...)
}

func (c *conn) session() *session {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cur
}

func (c *conn) setStatus(fn func(*Status)) {
	c.mu.Lock()
	fn(&c.st)
	c.mu.Unlock()
	c.s.emit(c.srv.Name)
}

func (c *conn) setError(err error) {
	c.setStatus(func(st *Status) {
		st.State, st.Error, st.PID, st.Since = StateError, err.Error(), 0, time.Now().UTC()
	})
}

func (c *conn) onListChanged() {
	select {
	case c.refresh <- struct{}{}:
	default:
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// run supervises the connection: connect, install tools, serve until the
// session ends, then reconnect with exponential backoff.
func (c *conn) run(ctx context.Context) {
	backoff := c.s.t.backoffMin
	grow := func() {
		backoff *= 2
		if backoff > c.s.t.backoffMax {
			backoff = c.s.t.backoffMax
		}
	}
	for ctx.Err() == nil {
		c.setStatus(func(st *Status) { st.State, st.Error = StateConnecting, "" })
		sess, list, err := c.connect(ctx)
		if ctx.Err() != nil {
			if sess != nil {
				sess.close()
			}
			return
		}
		if err != nil {
			c.ctx.Log().Warn("mcp: connect failed", "server", c.srv.Name, "err", err)
			c.setError(err)
			c.settle.Do(func() { close(c.settled) })
			if !sleepCtx(ctx, backoff) {
				return
			}
			grow()
			continue
		}
		c.install(list)
		c.mu.Lock()
		c.cur = sess
		c.mu.Unlock()
		c.setStatus(func(st *Status) {
			st.State, st.Error, st.RefreshError, st.PID, st.Enforcement, st.Since = StateReady, "", "", sess.pid, sess.enforcement, time.Now().UTC()
		})
		c.settle.Do(func() { close(c.settled) })
		up := time.Now()
		c.serve(ctx, sess)
		c.mu.Lock()
		c.cur = nil
		c.mu.Unlock()
		sess.close()
		if ctx.Err() != nil {
			return
		}
		// Tools stay registered (answering "not connected") until the
		// server is back: the model keeps a stable tool list.
		c.setError(fmt.Errorf("connection lost: %s", sess.exitReason()))
		if time.Since(up) > 30*time.Second {
			backoff = c.s.t.backoffMin
		}
		if !sleepCtx(ctx, backoff) {
			return
		}
		grow()
	}
}

func (c *conn) connect(ctx context.Context) (*session, []*sdk.Tool, error) {
	ws, err := c.s.workdir(c.srv)
	if err != nil {
		return nil, nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, c.s.t.connect)
	defer cancel()
	sess, err := c.s.dial(cctx, c.srv, ws, c.onListChanged)
	if err != nil {
		return nil, nil, err
	}
	list, err := listTools(cctx, sess.cs)
	if err != nil {
		sess.close()
		return nil, nil, sess.scrubErr(fmt.Errorf("tools/list: %w", err))
	}
	return sess, list, nil
}

func (c *conn) serve(ctx context.Context, sess *session) {
	done := make(chan struct{})
	go func() {
		_ = sess.cs.Wait()
		close(done)
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-sess.exited:
			return
		case <-c.refresh:
			rctx, cancel := context.WithTimeout(ctx, c.s.t.connect)
			list, err := listTools(rctx, sess.cs)
			cancel()
			if err != nil {
				// A failed refresh keeps the previous tool list.
				msg := sess.scrubErr(err).Error()
				c.ctx.Log().Warn("mcp: tools refresh failed; keeping previous tools", "server", c.srv.Name, "err", msg)
				c.setStatus(func(st *Status) { st.RefreshError = msg })
				continue
			}
			c.install(list)
			c.setStatus(func(st *Status) { st.RefreshError = "" })
		}
	}
}

// install replaces the registered tools with list.
func (c *conn) install(list []*sdk.Tool) {
	infos := describe(c.srv, list)
	// Register the new list first, then drop the old one: the scoped
	// registry lets the newest registration of a name win and disposes in
	// any order, so there is no window without the server's tools.
	tc := c.ctx.Fork("")
	for i, t := range list {
		if i >= len(infos) {
			break
		}
		info := infos[i]
		params := inputSchema(t.InputSchema)
		desc := t.Description
		if desc == "" {
			desc = t.Title
		}
		c.s.tools.Register(tc, &tools.Tool{
			Name:         info.Name,
			Description:  truncate(fmt.Sprintf("[MCP %s] %s", c.srv.Name, desc), 2000),
			Params:       params,
			Category:     info.Category,
			PathArgs:     PathArgs(params),
			AllowedRoots: GrantedRoots(c.srv),
			Execute:      c.execute(info.Original),
		})
	}
	c.mu.Lock()
	old := c.toolCtx
	c.toolCtx, c.infos = tc, infos
	c.st.Tools = len(infos)
	c.mu.Unlock()
	if old != nil {
		old.Dispose()
	}
}

func (c *conn) execute(original string) func(context.Context, tools.Call) (tools.Result, error) {
	return func(ctx context.Context, call tools.Call) (tools.Result, error) {
		sess := c.session()
		if sess == nil {
			st := c.status()
			return tools.Errorf("MCP server %q is not connected (%s); try again later", c.srv.Name, st.State), nil
		}
		args := call.Args
		if len(args) == 0 || string(args) == "null" {
			args = json.RawMessage("{}")
		}
		cctx, cancel := context.WithTimeout(ctx, c.s.t.call)
		defer cancel()
		res, err := sess.cs.CallTool(cctx, &sdk.CallToolParams{Name: original, Arguments: args})
		if err != nil {
			return tools.Errorf("MCP %s: %v", call.Name, sess.scrubErr(err)), nil
		}
		return ConvertResult(res), nil
	}
}

// describe names and categorizes a tool list, deduplicating names that
// collide after sanitizing.
func describe(srv Server, list []*sdk.Tool) []ToolInfo {
	server := srv.Name
	seen := map[string]bool{}
	out := make([]ToolInfo, 0, len(list))
	for _, t := range list {
		if len(out) >= MaxTools {
			break
		}
		name := ToolName(server, t.Name)
		for i := 2; seen[name]; i++ {
			name = ToolName(server, fmt.Sprintf("%s_%d", t.Name, i))
		}
		seen[name] = true
		cat := Category(t.Annotations, srv.TrustAnnotations)
		if v, ok := srv.Categories[t.Name]; ok {
			cat = v
		} else if v, ok := srv.Categories[name]; ok {
			cat = v
		}
		out = append(out, ToolInfo{Name: name, Original: t.Name, Description: t.Description, Category: cat})
	}
	return out
}

func listTools(ctx context.Context, cs *sdk.ClientSession) ([]*sdk.Tool, error) {
	var out []*sdk.Tool
	var seq iter.Seq2[*sdk.Tool, error] = cs.Tools(ctx, nil)
	for t, err := range seq {
		if err != nil {
			return nil, err
		}
		if t == nil {
			continue
		}
		out = append(out, t)
		if len(out) > MaxTools {
			break
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- dialing

// session is one live MCP client session (and, for stdio, its process).
type session struct {
	cs          *sdk.ClientSession
	pid         int
	enforcement string
	secrets     []string
	stderr      *tailBuf
	exited      chan struct{} // closed when the process exits (never for http)
	waitErr     error
	kill        func()
	once        sync.Once
	policy      sandbox.Policy // the sandbox policy a confined server runs under
}

func (p *session) scrubErr(err error) error {
	if err == nil {
		return nil
	}
	msg := scrub(err.Error(), p.secrets)
	if msg == err.Error() {
		return err
	}
	return &scrubbedError{msg: msg, err: err}
}

// scrubbedError hides secret values in the message while keeping the
// error chain for errors.Is (e.g. sandbox.ErrUnavailable).
type scrubbedError struct {
	msg string
	err error
}

func (e *scrubbedError) Error() string { return e.msg }
func (e *scrubbedError) Unwrap() error { return e.err }

func (p *session) exitReason() string {
	var b strings.Builder
	select {
	case <-p.exited:
		if p.waitErr != nil {
			b.WriteString("process exited: " + p.waitErr.Error())
		} else {
			b.WriteString("process exited")
		}
	default:
		b.WriteString("session closed")
	}
	if p.stderr != nil {
		if t := strings.TrimSpace(p.stderr.String()); t != "" {
			b.WriteString("; stderr: " + truncate(t, 500))
		}
	}
	return scrub(b.String(), p.secrets)
}

// close ends the session and kills the whole process tree.
func (p *session) close() {
	p.once.Do(func() {
		done := make(chan struct{})
		go func() {
			if p.cs != nil {
				_ = p.cs.Close()
			}
			close(done)
		}()
		if p.pid > 0 {
			select {
			case <-p.exited:
			case <-time.After(2 * time.Second):
				signalGroup(p.pid, syscall.SIGTERM)
				select {
				case <-p.exited:
				case <-time.After(time.Second):
				}
			}
			killGroup(p.pid) // stragglers in the process group
			<-p.exited
		}
		if p.kill != nil {
			p.kill()
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	})
}

// workdir returns (and creates) a server's working directory.
func (s *Service) workdir(srv Server) (string, error) {
	if srv.Scope != ScopeGlobal {
		reg := s.agents()
		if reg == nil {
			return "", fmt.Errorf("agents service not loaded (server is scoped to %s)", srv.Scope)
		}
		a, ok := reg.Get(srv.Scope)
		if !ok {
			return "", fmt.Errorf("agent %s not found", srv.Scope)
		}
		return a.Workspace(), nil
	}
	d := s.serverDir(srv.Name)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	return d, nil
}

func (s *Service) newClient(onChange func()) *sdk.Client {
	opts := &sdk.ClientOptions{Capabilities: &sdk.ClientCapabilities{}}
	if onChange != nil {
		opts.ToolListChangedHandler = func(context.Context, *sdk.ToolListChangedRequest) { onChange() }
	}
	return sdk.NewClient(&sdk.Implementation{Name: "crewbase", Version: "1.0.0"}, opts)
}

// dial connects and initializes a session for srv.
func (s *Service) dial(ctx context.Context, srv Server, ws string, onChange func()) (*session, error) {
	switch srv.Transport {
	case TransportStdio:
		return s.dialStdio(ctx, srv, ws, onChange)
	case TransportHTTP:
		return s.dialHTTP(ctx, srv, onChange)
	}
	return nil, invalid("unknown transport %q", srv.Transport)
}

func baseEnv() []string {
	keep := map[string]bool{}
	for _, k := range InheritEnv {
		keep[k] = true
	}
	var out []string
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok && keep[k] {
			out = append(out, kv)
		}
	}
	return out
}

func mergeEnv(base []string, extra map[string]string) []string {
	idx := map[string]int{}
	out := append([]string(nil), base...)
	for i, kv := range out {
		k, _, _ := strings.Cut(kv, "=")
		idx[k] = i
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		kv := k + "=" + extra[k]
		if i, ok := idx[k]; ok {
			out[i] = kv
			continue
		}
		idx[k] = len(out)
		out = append(out, kv)
	}
	return out
}

func (s *Service) dialStdio(ctx context.Context, srv Server, ws string, onChange func()) (*session, error) {
	sess := &session{exited: make(chan struct{})}
	env, err := resolveMap(ctx, s.resolver(), srv.Env, &sess.secrets)
	if err != nil {
		return nil, err
	}
	envList := baseEnv()
	var conf sandbox.Confined
	if srv.Trust == TrustSandboxed {
		// Package managers (npx, uvx) need a writable cache: point them at
		// the server's own directory unless the config says otherwise.
		cache := filepath.Join(s.serverDir(srv.Name), "cache")
		if err := os.MkdirAll(cache, 0o700); err != nil {
			return nil, err
		}
		defaults := map[string]string{"XDG_CACHE_HOME": cache, "npm_config_cache": filepath.Join(cache, "npm"), "UV_CACHE_DIR": filepath.Join(cache, "uv")}
		for k, v := range env {
			defaults[k] = v
		}
		env = defaults
		envList = mergeEnv(envList, env)
		sb := s.sandbox()
		if sb == nil {
			return nil, sandbox.Unavailable("no sandbox provider is loaded; refusing to start MCP server %q unconfined (trust=host opts out)", srv.Name)
		}
		roots := []string{ws}
		if sd := s.serverDir(srv.Name); sd != ws {
			roots = append(roots, sd)
		}
		roots = append(roots, GrantedRoots(srv)...)
		pol := sandbox.Policy{Mode: sandbox.ModeWorkspaceWrite, WritableRoots: roots, Network: sandbox.NetworkOpen}
		if s.agentReadOnly(srv) {
			// The agent may not write: neither may its server.
			pol = sandbox.Policy{Mode: sandbox.ModeReadOnly, Network: sandbox.NetworkOpen}
		}
		pol = pol.Normalize()
		sess.policy = pol
		conf, err = sb.Confine(ctx, srv.Command, ws, envList, pol)
		if err != nil {
			return nil, sess.scrubErr(err)
		}
		if conf.Enforcement == "" || conf.Enforcement == sandbox.EnforcementNone {
			if conf.Kill != nil {
				conf.Kill()
			}
			return nil, sandbox.Unavailable("sandbox %s gave no enforcement; refusing to start MCP server %q unconfined", sb.Name(), srv.Name)
		}
	} else {
		envList = mergeEnv(envList, env)
		conf = sandbox.Unconfined(srv.Command, envList)
	}
	sess.enforcement, sess.kill = conf.Enforcement, conf.Kill

	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, err
	}
	sess.stderr = &tailBuf{max: 4096}
	cmd := exec.Command(conf.Argv[0], conf.Argv[1:]...)
	cmd.Dir, cmd.Env = ws, conf.Env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, sess.stderr
	cmd.WaitDelay = time.Second // don't hang on stderr held by grandchildren
	setGroup(cmd)
	err = cmd.Start()
	inR.Close()
	outW.Close()
	if err != nil {
		inW.Close()
		outR.Close()
		if conf.Kill != nil {
			conf.Kill()
		}
		return nil, sess.scrubErr(fmt.Errorf("start %s: %w", srv.Command[0], err))
	}
	sess.pid = cmd.Process.Pid
	go func() {
		sess.waitErr = cmd.Wait()
		close(sess.exited)
	}()
	cs, err := s.newClient(onChange).Connect(ctx, &sdk.IOTransport{Reader: outR, Writer: inW}, nil)
	if err != nil {
		inW.Close()
		outR.Close()
		sess.close()
		return nil, errors.New(scrub(fmt.Sprintf("initialize: %v (%s)", err, sess.exitReason()), sess.secrets))
	}
	sess.cs = cs
	return sess, nil
}

// headerTransport adds resolved headers to every request.
type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (h *headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h.headers {
		r.Header.Set(k, v)
	}
	return h.base.RoundTrip(r)
}

func (s *Service) dialHTTP(ctx context.Context, srv Server, onChange func()) (*session, error) {
	sess := &session{exited: make(chan struct{})} // never closed: no process
	cr := s.resolver()
	hdrs, err := resolveMap(ctx, cr, srv.Headers, &sess.secrets)
	if err != nil {
		return nil, err
	}
	u, err := resolveValue(ctx, cr, srv.URL)
	if err != nil {
		return nil, fmt.Errorf("url: %w", err)
	}
	if u != srv.URL {
		sess.secrets = append(sess.secrets, u)
	}
	base := http.DefaultTransport
	hc := &http.Client{}
	if s.HTTPClient != nil {
		*hc = *s.HTTPClient
		if s.HTTPClient.Transport != nil {
			base = s.HTTPClient.Transport
		}
	}
	hc.Transport = &headerTransport{base: base, headers: hdrs}
	t := &sdk.StreamableClientTransport{Endpoint: u, HTTPClient: hc}
	cs, err := s.newClient(onChange).Connect(ctx, t, nil)
	if err != nil {
		return nil, sess.scrubErr(fmt.Errorf("initialize: %w", err))
	}
	sess.cs = cs
	return sess, nil
}

// tailBuf keeps the last max bytes written (stderr of a server).
type tailBuf struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tailBuf) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = append([]byte(nil), t.b[len(t.b)-t.max:]...)
	}
	return len(p), nil
}

func (t *tailBuf) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}
