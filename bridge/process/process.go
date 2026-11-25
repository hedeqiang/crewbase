// Package process is the out-of-process plugin runtime (docs/05 §7,
// docs/06 §7, ADR-0002). It registers the kernel runtime "process": an entry
//
//	{id: word-count, name: word-count, runtime: process,
//	 permissions: [service:tools, event:tools/result],
//	 config: {dir: /path/to/plugin}}
//
// becomes a proxy plugin that starts the manifest's entry as a subprocess
// (own process group), speaks JSON-RPC 2.0 over stdio and registers the
// plugin's tools, prompt fragments, notify channels and whitelisted event
// listeners on its kernel context on the plugin's behalf. Every host call
// from the plugin is checked against the granted permissions.
//
// When the process exits, the generation context is disposed (every
// registration is revoked) and the process is restarted with backoff; more
// than MaxRestarts crashes inside RestartWindow marks the plugin FAILED.
// Unloading the plugin kills the whole process tree.
//
// Protocol (version 1), newline-delimited JSON-RPC 2.0:
//
//	host → plugin
//	  initialize          {protocolVersion, plugin{name,version,dir,dataDir}, permissions, config}
//	                      → {protocolVersion, tools[], prompts[], events[], notify[]}
//	  initialized         notification
//	  tool/execute        {tool, call{id,name,args,agent,mode,workspace}} → {content, isError?, data?}
//	  tools/pre-execute   {call, tool, category, summary, verdict, reason} → null | {verdict:"deny", reason} | {note}
//	  tools/post-execute  {call, category, result{content,isError}, denied} → null | {content?, isError?}
//	  tools/result        notification {call, category, result, denied}
//	  notify/send         {channel, notification} → null
//	  shutdown            notification
//	plugin → host
//	  log                 {level, message}                 (request or notification; always allowed)
//	  notify.send         {kind, title, body, urgent}      needs service notify
//	  credentials.resolve {ref} → {value}                  needs service credentials + ref in permissions.credentials
package process

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hedeqiang/crewbase/bridge/jsonrpc"
	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/capabilities/notify"
	"github.com/hedeqiang/crewbase/capabilities/sandbox"
	"github.com/hedeqiang/crewbase/capabilities/ui"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/internal/netproxy"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/sdk"
)

// RuntimeName is the kernel runtime handled by this package.
const RuntimeName = "process"

// ProtocolVersion is the bridge protocol version.
const ProtocolVersion = sdk.ProtocolVersion

// JSON-RPC error codes used by the host.
const (
	CodePermissionDenied = -32001
	CodeUnavailable      = -32002
)

// ErrNotApproved means the manifest requests permissions that were not granted.
var ErrNotApproved = errors.New("plugin permissions not approved")

const maxDiag = 4 << 10 // diagnostics are capped at 4 KB (docs/06 §5)

func init() { kernel.RegisterRuntime(RuntimeName, Loader) }

// Config is the entry config of a process plugin.
type Config struct {
	Dir     string         `json:"dir"`              // plugin directory (holds crewbase-plugin.toml)
	DataDir string         `json:"dataDir"`          // writable data dir (sandbox writable root)
	Config  map[string]any `json:"config,omitempty"` // passed to the plugin in initialize

	HookTimeoutMs   int `json:"hookTimeoutMs,omitempty"`   // waterfall hooks, default 2000
	ToolTimeoutMs   int `json:"toolTimeoutMs,omitempty"`   // default 60000
	InitTimeoutMs   int `json:"initTimeoutMs,omitempty"`   // handshake, default 10000
	MaxRestarts     int `json:"maxRestarts,omitempty"`     // default 3
	RestartWindowMs int `json:"restartWindowMs,omitempty"` // default 60000
	BackoffMs       int `json:"backoffMs,omitempty"`       // first restart delay, default 250
}

func (c *Config) defaults() {
	def := func(v *int, d int) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&c.HookTimeoutMs, 2000)
	def(&c.ToolTimeoutMs, 60000)
	def(&c.InitTimeoutMs, 10000)
	def(&c.MaxRestarts, 3)
	def(&c.RestartWindowMs, 60000)
	def(&c.BackoffMs, 250)
}

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func decodeConfig(raw map[string]any) (Config, error) {
	var cfg Config
	b, err := json.Marshal(raw)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, err
	}
	if cfg.Dir == "" {
		return cfg, errors.New("process plugin: config.dir is required")
	}
	if !filepath.IsAbs(cfg.Dir) {
		abs, err := filepath.Abs(cfg.Dir)
		if err != nil {
			return cfg, err
		}
		cfg.Dir = abs
	}
	cfg.defaults()
	return cfg, nil
}

// Loader is the kernel RuntimeLoader for runtime "process". The entry's
// Permissions are the user-approved grants; a manifest asking for more is
// refused with ErrNotApproved.
func Loader(e kernel.Entry) (kernel.Spec, error) {
	cfg, err := decodeConfig(e.Config)
	if err != nil {
		return kernel.Spec{}, err
	}
	m, err := ReadManifest(cfg.Dir)
	if err != nil {
		return kernel.Spec{}, err
	}
	if e.Name != "" && e.Name != m.Name {
		return kernel.Spec{}, fmt.Errorf("process plugin: entry name %q does not match manifest name %q", e.Name, m.Name)
	}
	granted, err := ParsePermissions(e.Permissions)
	if err != nil {
		return kernel.Spec{}, err
	}
	want := m.Requested()
	if missing := granted.Missing(want); len(missing) > 0 {
		return kernel.Spec{}, fmt.Errorf("%w: %s", ErrNotApproved, strings.Join(missing, ", "))
	}
	use := granted.Intersect(want)
	// running without a sandbox can be a person's call about this computer
	// (no backend works here), not only something the manifest asked for
	use.Unsandboxed = granted.Unsandboxed
	return Spec(m, cfg, use), nil
}

// Spec builds the proxy plugin spec for an already-checked manifest.
func Spec(m Manifest, cfg Config, granted Permissions) kernel.Spec {
	cfg.defaults()
	return kernel.Spec{
		Name:        m.Name,
		Inject:      append([]string(nil), granted.Services...),
		Optional:    []string{sandbox.ServiceKey},
		Description: fmt.Sprintf("Process plugin %s@%s: %s", m.Name, m.Version, m.Description),
		Apply: func(c *kernel.Context) error {
			p := &proxy{c: c, m: m, cfg: cfg, granted: granted, log: c.Log()}
			return p.start()
		},
	}
}

// ---------------------------------------------------------------- status

// Status is a snapshot of a running process plugin.
type Status struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Version  string    `json:"version"`
	State    string    `json:"state"` // starting | running | restarting
	PID      int       `json:"pid,omitempty"`
	Restarts int       `json:"restarts"`
	Tools    []string  `json:"tools,omitempty"`
	Prompts  []string  `json:"prompts,omitempty"`
	Events   []string  `json:"events,omitempty"`
	Notify   []string  `json:"notify,omitempty"`
	UI       []string  `json:"ui,omitempty"` // slot:key
	Started  time.Time `json:"started,omitempty"`
	// Enforcement tells honestly how network/files/subprocess limits are
	// enforced: "none" without a sandbox, otherwise the sandbox's level.
	Enforcement string      `json:"enforcement"`
	Limits      []string    `json:"limits,omitempty"`
	Permissions Permissions `json:"permissions"`
	LastError   string      `json:"lastError,omitempty"`
}

type statusKey struct {
	h  *kernel.Host
	id string
}

var (
	statusMu sync.Mutex
	statuses = map[statusKey]*proxy{}
)

// Lookup returns the status of the process plugin loaded under id.
func Lookup(h *kernel.Host, id string) (Status, bool) {
	statusMu.Lock()
	p, ok := statuses[statusKey{h, id}]
	statusMu.Unlock()
	if !ok {
		return Status{}, false
	}
	return p.snapshot(), true
}

// ---------------------------------------------------------------- proxy

type proxy struct {
	c       *kernel.Context
	m       Manifest
	cfg     Config
	granted Permissions
	log     *slog.Logger

	mu      sync.Mutex
	st      Status
	gen     *generation
	crashes []time.Time
}

func (p *proxy) snapshot() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.st
	s.Tools = slices.Clone(s.Tools)
	s.Events = slices.Clone(s.Events)
	return s
}

func (p *proxy) setState(fn func(s *Status)) {
	p.mu.Lock()
	fn(&p.st)
	p.mu.Unlock()
}

func (p *proxy) start() error {
	p.st = Status{ID: p.c.PluginID(), Name: p.m.Name, Version: p.m.Version, State: "starting",
		Enforcement: sandbox.EnforcementNone, Permissions: p.granted}
	key := statusKey{p.c.Host(), p.c.PluginID()}
	g, err := p.spawn()
	if err != nil {
		return err
	}
	statusMu.Lock()
	statuses[key] = p
	statusMu.Unlock()
	p.c.Defer(func() {
		statusMu.Lock()
		if statuses[key] == p {
			delete(statuses, key)
		}
		statusMu.Unlock()
	})
	p.c.Go(func(ctx context.Context) { p.supervise(ctx, g) })
	return nil
}

// supervise restarts the process after it exits until the plugin unloads
// or crashes too often.
func (p *proxy) supervise(ctx context.Context, g *generation) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-g.exited:
		}
		if ctx.Err() != nil {
			return
		}
		why := g.exitReason()
		p.log.Warn("plugin process exited; revoking registrations", "err", why)
		g.ctx.Dispose()
		for {
			now := time.Now()
			p.mu.Lock()
			window := ms(p.cfg.RestartWindowMs)
			kept := p.crashes[:0]
			for _, t := range p.crashes {
				if now.Sub(t) < window {
					kept = append(kept, t)
				}
			}
			p.crashes = append(kept, now)
			n := len(p.crashes)
			p.st.State, p.st.PID, p.st.LastError = "restarting", 0, why
			p.st.Tools, p.st.Prompts, p.st.Events, p.st.Notify = nil, nil, nil, nil
			p.mu.Unlock()
			if n > p.cfg.MaxRestarts {
				p.fail(fmt.Errorf("plugin process crashed %d times within %s; last: %s", n, window, why))
				return
			}
			delay := ms(p.cfg.BackoffMs) << (n - 1)
			if delay > 5*time.Second {
				delay = 5 * time.Second
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			ng, err := p.spawn()
			if err == nil {
				p.setState(func(s *Status) { s.Restarts++ })
				g = ng
				break
			}
			why = err.Error()
			p.log.Warn("plugin restart failed", "err", err)
			if ctx.Err() != nil {
				return
			}
		}
	}
}

// fail replaces the plugin with a FAILED entry so `crew plugins` shows it.
// It runs detached: unloading waits for the supervisor to return.
func (p *proxy) fail(err error) {
	h, id, cfg := p.c.Host(), p.c.PluginID(), p.c.Config()
	name, desc := p.m.Name, "Process plugin "+p.m.Name+" (failed)"
	p.log.Error("plugin failed", "err", err)
	go func() {
		if p.c.Disposed() {
			return
		}
		statusMu.Lock()
		cur := statuses[statusKey{h, id}]
		statusMu.Unlock()
		if cur != p {
			return // replaced meanwhile (reload, disable)
		}
		h.Unload(id)
		_, _ = h.Load(kernel.Spec{Name: name, Description: desc, Apply: func(*kernel.Context) error { return err }},
			kernel.LoadOptions{ID: id, Config: cfg})
	}()
}

// ---------------------------------------------------------------- generation

// generation is one run of the plugin process. Its context is a child of
// the proxy context; disposing it revokes everything the run registered and
// stops the process tree.
type generation struct {
	p      *proxy
	ctx    *kernel.Context
	cmd    *exec.Cmd
	conn   *jsonrpc.Conn
	exited chan struct{}
	events chan any // tools/result notifications, fire and forget

	stopping atomic.Bool
	errMu    sync.Mutex
	waitErr  error
	tail     []string // last stderr lines (diagnostics)
	killSB   func()
}

func (g *generation) exitReason() string {
	g.errMu.Lock()
	defer g.errMu.Unlock()
	msg := "process exited"
	if g.waitErr != nil {
		msg = "process exited: " + g.waitErr.Error()
	}
	if len(g.tail) > 0 {
		msg += "; stderr: " + strings.Join(g.tail, " | ")
	}
	return capDiag(msg)
}

func capDiag(s string) string {
	if len(s) > maxDiag {
		return s[:maxDiag] + "…"
	}
	return s
}

// safeEnv is the environment a plugin process gets: no inherited secrets
// (API keys in the daemon's environment stay in the daemon).
func safeEnv() []string {
	var env []string
	for _, k := range []string{"PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "LC_CTYPE", "TMPDIR", "TEMP", "TMP", "SYSTEMROOT", "TZ"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	// Settings › 网络代理
	return append(env, netproxy.Env()...)
}

func (p *proxy) command() (argv, env []string, err error) {
	argv = slices.Clone(p.m.Entry)
	if strings.ContainsRune(argv[0], '/') || strings.ContainsRune(argv[0], filepath.Separator) {
		if !filepath.IsAbs(argv[0]) {
			argv[0] = filepath.Join(p.cfg.Dir, argv[0])
		}
	} else if lp, lerr := exec.LookPath(argv[0]); lerr == nil {
		argv[0] = lp
	} else {
		return nil, nil, fmt.Errorf("entry %q: %w", argv[0], lerr)
	}
	env = safeEnv()
	env = append(env,
		"CREWBASE_PROTOCOL=1",
		"CREWBASE_PLUGIN_NAME="+p.m.Name,
		"CREWBASE_PLUGIN_VERSION="+p.m.Version,
		"CREWBASE_PLUGIN_DIR="+p.cfg.Dir,
		"CREWBASE_PLUGIN_DATA="+p.cfg.DataDir,
		"PYTHONUNBUFFERED=1",
		"PYTHONDONTWRITEBYTECODE=1",
	)
	base := strings.ToLower(filepath.Base(argv[0]))
	switch {
	case strings.HasPrefix(base, "node"):
		if err := sdk.MaterializeNode(p.cfg.Dir); err != nil {
			return nil, nil, fmt.Errorf("provide TypeScript SDK: %w", err)
		}
	case strings.HasPrefix(base, "python"):
		pp, err := sdk.MaterializePython(filepath.Join(p.cfg.Dir, ".crewbase-sdk", "python"))
		if err != nil {
			return nil, nil, fmt.Errorf("provide Python SDK: %w", err)
		}
		env = append(env, "PYTHONPATH="+pp)
	}
	return argv, env, nil
}

// confine wraps argv in the sandbox. Every process plugin runs in it
// unless it holds the "unsandboxed" permission: its manifest opted out (and
// a person approved that), or a person let it run unconfined on a computer
// where no sandbox backend works. Otherwise it fails closed: no sandbox, no
// start.
func (p *proxy) confine(argv, env []string) ([]string, []string, func(), error) {
	unconfined := func(why string) ([]string, []string, func(), error) {
		p.setState(func(s *Status) {
			s.Enforcement = sandbox.EnforcementNone
			s.Limits = []string{why}
		})
		return argv, env, nil, nil
	}
	if !p.m.Sandbox {
		if !p.granted.Unsandboxed {
			return nil, nil, nil, errors.New("the plugin asks to run without the sandbox, which has not been approved")
		}
		return unconfined("runs without the sandbox, as its manifest asks (approved): network, files and subprocess permissions are not enforced")
	}
	noSandbox := func(reason error) ([]string, []string, func(), error) {
		if p.granted.Unsandboxed {
			return unconfined("no sandbox works on this computer, so it runs without one (approved): network, files and subprocess permissions are not enforced")
		}
		return nil, nil, nil, sandbox.Unavailable("no sandbox works on this computer (%v); a person can allow this plugin to run without one", reason)
	}
	sb, ok := kernel.Lookup[sandbox.Sandbox](p.c, sandbox.ServiceKey)
	if !ok {
		return noSandbox(errors.New("no sandbox service"))
	}
	pol := sandbox.Policy{Mode: sandbox.ModeReadOnly, Network: sandbox.NetworkNone}
	var roots []string
	if p.cfg.DataDir != "" {
		if err := os.MkdirAll(p.cfg.DataDir, 0o700); err != nil {
			return nil, nil, nil, err
		}
		roots = append(roots, p.cfg.DataDir)
	}
	roots = append(roots, p.granted.Files...)
	if len(roots) > 0 {
		pol.Mode, pol.WritableRoots = sandbox.ModeWorkspaceWrite, roots
	}
	if len(p.granted.Network) > 0 {
		pol.Network, pol.AllowHosts = sandbox.NetworkAllowlist, p.granted.Network
	}
	pol = pol.Normalize()
	if err := sandbox.Validate(argv, pol); err != nil {
		return nil, nil, nil, err
	}
	if _, err := sb.Check(p.c.Context(), pol); err != nil {
		if errors.Is(err, sandbox.ErrUnavailable) {
			return noSandbox(err)
		}
		return nil, nil, nil, err
	}
	cf, err := sb.Confine(p.c.Context(), argv, p.cfg.Dir, env, pol)
	if err != nil {
		if errors.Is(err, sandbox.ErrUnavailable) {
			return noSandbox(err)
		}
		return nil, nil, nil, err
	}
	p.setState(func(s *Status) {
		s.Enforcement = cf.Enforcement
		s.Limits = append(slices.Clone(cf.Limits), "subprocess permission is not enforced by the sandbox")
	})
	return cf.Argv, cf.Env, cf.Cleanup(), nil
}

func (p *proxy) spawn() (*generation, error) {
	argv, env, err := p.command()
	if err != nil {
		return nil, err
	}
	argv, env, killSB, err := p.confine(argv, env)
	if err != nil {
		return nil, err
	}
	g := &generation{p: p, ctx: p.c.Fork(""), exited: make(chan struct{}), events: make(chan any, 256), killSB: killSB}
	// the sandbox set up for it is cleaned up when the process never starts
	started := false
	defer func() {
		if !started && killSB != nil {
			killSB()
		}
	}()
	inR, inW, err := os.Pipe()
	if err != nil {
		g.ctx.Dispose()
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		g.ctx.Dispose()
		return nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		g.ctx.Dispose()
		return nil, err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = p.cfg.Dir, env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errW
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		for _, f := range []*os.File{inR, inW, outR, outW, errR, errW} {
			f.Close()
		}
		g.ctx.Dispose()
		return nil, fmt.Errorf("start %s: %w", strings.Join(argv, " "), err)
	}
	started = true
	afterStart(cmd)
	inR.Close()
	outW.Close()
	errW.Close()
	g.cmd = cmd

	g.conn = jsonrpc.NewConn(outR, inW, jsonrpc.Options{
		OnRequest: g.hostRequest,
		OnNotification: func(method string, params json.RawMessage) {
			if method == "log" {
				_, _ = g.hostRequest(context.Background(), method, params)
			}
		},
		MaxMessageBytes: 16 << 20,
	})
	go func() { _ = g.conn.Serve(context.Background()) }()
	go g.readStderr(errR)
	go func() {
		err := cmd.Wait()
		killTree(cmd) // reap anything left in the process group
		g.errMu.Lock()
		g.waitErr = err
		g.errMu.Unlock()
		_ = g.conn.Close()
		close(g.exited)
	}()
	go g.sendEvents()

	// Stop the process when the generation disposes. Registered first so it
	// runs last: registrations are revoked before the process goes away.
	g.ctx.Defer(g.stop)

	if err := g.handshake(); err != nil {
		g.ctx.Dispose()
		return nil, fmt.Errorf("plugin %s: %w", p.m.Name, err)
	}
	return g, nil
}

func (g *generation) readStderr(r io.ReadCloser) {
	defer r.Close()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	for sc.Scan() {
		line := capDiag(sc.Text())
		g.p.log.Info("plugin stderr", "line", line)
		g.errMu.Lock()
		g.tail = append(g.tail, line)
		if len(g.tail) > 5 {
			g.tail = g.tail[len(g.tail)-5:]
		}
		g.errMu.Unlock()
	}
}

func (g *generation) stop() {
	g.stopping.Store(true)
	select {
	case <-g.exited:
	default:
		_ = g.conn.Notify("shutdown", nil)
		_ = g.conn.Close() // closes stdin: SDKs exit on EOF
		select {
		case <-g.exited:
		case <-time.After(1500 * time.Millisecond):
			terminateTree(g.cmd)
			select {
			case <-g.exited:
			case <-time.After(1500 * time.Millisecond):
				killTree(g.cmd)
				<-g.exited
			}
		}
	}
	killTree(g.cmd)
	if g.killSB != nil {
		g.killSB()
	}
}

func (g *generation) sendEvents() {
	for {
		select {
		case <-g.exited:
			return
		case ev := <-g.events:
			if err := g.conn.Notify("tools/result", ev); err != nil {
				return
			}
		}
	}
}

// ---------------------------------------------------------------- handshake

// Registrations is the plugin's answer to initialize.
type Registrations struct {
	ProtocolVersion int    `json:"protocolVersion"`
	Name            string `json:"name,omitempty"`
	Version         string `json:"version,omitempty"`
	Tools           []struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Params      map[string]any `json:"params"`
		Category    string         `json:"category"`
		TimeoutMs   int            `json:"timeoutMs"`
	} `json:"tools"`
	Prompts []struct {
		Name  string `json:"name"`
		Text  string `json:"text"`
		Order int    `json:"order"`
	} `json:"prompts"`
	Events []string `json:"events"`
	Notify []struct {
		Name string `json:"name"`
	} `json:"notify"`
	// UI contributions (declarative; panels and commands call back into
	// the plugin with ui/panel and ui/run).
	UI []struct {
		Slot    string   `json:"slot"`
		Key     string   `json:"key"`
		Title   string   `json:"title"`
		Desc    string   `json:"desc"`
		Icon    string   `json:"icon"`
		Order   int      `json:"order"`
		Card    *ui.Card `json:"card"`
		Confirm string   `json:"confirm"`
	} `json:"ui"`
}

var (
	toolNameRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`)
	categories = []string{tools.CategoryRead, tools.CategoryExec, tools.CategoryInstall, tools.CategoryWrite,
		tools.CategoryPush, tools.CategoryDelete, tools.CategoryPublish, tools.CategorySpend, tools.CategoryPayment, tools.CategoryNone}
)

func (g *generation) handshake() error {
	p := g.p
	ctx, cancel := context.WithTimeout(p.c.Context(), ms(p.cfg.InitTimeoutMs))
	defer cancel()
	cfg := p.cfg.Config
	if cfg == nil {
		cfg = map[string]any{}
	}
	var reg Registrations
	err := g.conn.Call(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"plugin":          map[string]any{"name": p.m.Name, "version": p.m.Version, "dir": p.cfg.Dir, "dataDir": p.cfg.DataDir},
		"permissions":     p.granted,
		"config":          cfg,
	}, &reg)
	if err != nil {
		select {
		case <-g.exited:
			return fmt.Errorf("initialize: %s", g.exitReason())
		case <-time.After(50 * time.Millisecond):
		}
		return fmt.Errorf("initialize: %w", err)
	}
	if reg.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("plugin speaks protocol %d, host speaks %d", reg.ProtocolVersion, ProtocolVersion)
	}
	if err := g.register(reg); err != nil {
		return err
	}
	_ = g.conn.Notify("initialized", nil)
	p.setState(func(s *Status) {
		s.State, s.PID, s.Started, s.LastError = "running", g.cmd.Process.Pid, time.Now(), ""
	})
	return nil
}

func denied(format string, a ...any) error {
	return jsonrpc.NewError(CodePermissionDenied, "permission denied: "+fmt.Sprintf(format, a...), nil)
}

// register validates every registration against the granted permissions
// before registering anything, then registers on the generation context.
func (g *generation) register(reg Registrations) error {
	p := g.p
	need := func(svc, what string) error {
		if !p.granted.HasService(svc) {
			return fmt.Errorf("plugin registers %s but service %q is not declared/approved", what, svc)
		}
		return nil
	}
	var treg *tools.Registry
	if len(reg.Tools) > 0 {
		if err := need(tools.ServiceKey, "tools"); err != nil {
			return err
		}
		var ok bool
		if treg, ok = kernel.Lookup[*tools.Registry](g.ctx, tools.ServiceKey); !ok {
			return errors.New("tools service unavailable")
		}
	}
	seen := map[string]bool{}
	for _, t := range reg.Tools {
		if !toolNameRe.MatchString(t.Name) {
			return fmt.Errorf("invalid tool name %q", t.Name)
		}
		if seen[t.Name] {
			return fmt.Errorf("duplicate tool %q", t.Name)
		}
		seen[t.Name] = true
		if t.Category != "" && !slices.Contains(categories, t.Category) {
			return fmt.Errorf("tool %s: unknown category %q", t.Name, t.Category)
		}
		// A third-party plugin never shadows an existing tool (e.g. bash).
		if _, taken := treg.Get("", t.Name); taken {
			return fmt.Errorf("tool name %q is already registered", t.Name)
		}
	}
	var asm *prompt.Assembler
	if len(reg.Prompts) > 0 {
		if err := need(prompt.ServiceKey, "prompt fragments"); err != nil {
			return err
		}
		var ok bool
		if asm, ok = kernel.Lookup[*prompt.Assembler](g.ctx, prompt.ServiceKey); !ok {
			return errors.New("prompt service unavailable")
		}
	}
	var nsvc *notify.Service
	if len(reg.Notify) > 0 {
		if err := need(notify.ServiceKey, "notify channels"); err != nil {
			return err
		}
		var ok bool
		if nsvc, ok = kernel.Lookup[*notify.Service](g.ctx, notify.ServiceKey); !ok {
			return errors.New("notify service unavailable")
		}
	}
	var usvc *ui.Service
	if len(reg.UI) > 0 {
		if err := need(ui.ServiceKey, "interface contributions"); err != nil {
			return err
		}
		var ok bool
		if usvc, ok = kernel.Lookup[*ui.Service](g.ctx, ui.ServiceKey); !ok {
			return errors.New("ui service unavailable")
		}
	}
	for _, e := range reg.Events {
		if !slices.Contains(ExposedEvents, e) {
			return fmt.Errorf("event %q is not exposed to external plugins", e)
		}
		if !p.granted.HasEvent(e) {
			return fmt.Errorf("plugin listens to %q but the event is not declared/approved", e)
		}
	}

	var st Status
	for _, t := range reg.Tools {
		tt := t
		params := tt.Params
		if params == nil {
			params = tools.Schema(map[string]any{})
		}
		treg.Register(g.ctx, &tools.Tool{Name: tt.Name, Description: tt.Description, Params: params, Category: tt.Category, Group: p.m.Name, Plugin: p.m.Name,
			Execute: func(ctx context.Context, call tools.Call) (tools.Result, error) {
				return g.executeTool(ctx, tt.Name, tt.TimeoutMs, call)
			}})
		st.Tools = append(st.Tools, tt.Name)
	}
	for _, f := range reg.Prompts {
		name := "plugin:" + p.m.Name + "/" + f.Name
		asm.Register(g.ctx, prompt.Static(name, f.Order, capText(f.Text, 8<<10)))
		st.Prompts = append(st.Prompts, name)
	}
	for _, n := range reg.Notify {
		nsvc.AddChannel(g.ctx, channel{g: g, name: n.Name})
		st.Notify = append(st.Notify, n.Name)
	}
	for _, u := range reg.UI {
		x := ui.Contribution{Slot: u.Slot, Key: u.Key, Title: capText(u.Title, 80), Desc: capText(u.Desc, 200), Icon: u.Icon, Order: u.Order,
			Card: u.Card, Confirm: capText(u.Confirm, 300), Owner: p.m.Name}
		slot, key := u.Slot, u.Key
		switch slot {
		case ui.SlotSidebarTab, ui.SlotSettingsCard:
			x.Panel = func(ctx context.Context, session string) (ui.Panel, error) {
				var out ui.Panel
				cctx, cancel := g.callCtx(ctx, 10*time.Second)
				defer cancel()
				if err := g.conn.Call(cctx, "ui/panel", map[string]any{"slot": slot, "key": key, "session": session}, &out); err != nil {
					return ui.Panel{}, fmt.Errorf("plugin %s: %s", p.m.Name, capDiag(err.Error()))
				}
				return out, nil
			}
		case ui.SlotPaletteCommand, ui.SlotComposerAction:
			x.Run = func(ctx context.Context, session string) (ui.Result, error) {
				var out ui.Result
				cctx, cancel := g.callCtx(ctx, 60*time.Second)
				defer cancel()
				if err := g.conn.Call(cctx, "ui/run", map[string]any{"slot": slot, "key": key, "session": session}, &out); err != nil {
					return ui.Result{}, fmt.Errorf("plugin %s: %s", p.m.Name, capDiag(err.Error()))
				}
				return out, nil
			}
		}
		if err := usvc.Register(g.ctx, x); err != nil {
			return err
		}
		st.UI = append(st.UI, slot+":"+key)
	}
	for _, e := range uniq(reg.Events) {
		switch e {
		case tools.OnResult.Name():
			tools.OnResult.Listen(g.ctx, func(exe tools.Execution) {
				select {
				case g.events <- executionView(exe, exe.Result):
				default:
					p.log.Warn("plugin event queue full; dropping tools/result")
				}
			})
		case tools.PreExecute.Name():
			tools.PreExecute.Use(g.ctx, g.preExecute)
		case tools.PostExecute.Name():
			tools.PostExecute.Use(g.ctx, g.postExecute)
		}
		st.Events = append(st.Events, e)
	}
	p.setState(func(s *Status) {
		s.Tools, s.Prompts, s.Notify, s.Events, s.UI = st.Tools, st.Prompts, st.Notify, st.Events, st.UI
	})
	return nil
}

func capText(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ---------------------------------------------------------------- host → plugin

func callView(c tools.Call) map[string]any {
	args := c.Args
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	return map[string]any{"id": c.ID, "name": c.Name, "args": args, "agent": c.Agent, "mode": c.Mode, "workspace": c.Workspace}
}

func executionView(exe tools.Execution, res tools.Result) map[string]any {
	return map[string]any{
		"call":     callView(exe.Call),
		"category": exe.Category,
		"result":   map[string]any{"content": res.Content, "isError": res.IsError},
		"denied":   exe.Denied,
	}
}

// callCtx bounds ctx by timeout and by the generation's lifetime.
func (g *generation) callCtx(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	cctx, cancel := context.WithTimeout(ctx, d)
	stop := context.AfterFunc(g.ctx.Context(), cancel)
	return cctx, func() { stop(); cancel() }
}

func (g *generation) executeTool(ctx context.Context, name string, timeoutMs int, call tools.Call) (tools.Result, error) {
	if g.ctx.Disposed() {
		return tools.Errorf("plugin %s is not running", g.p.m.Name), nil
	}
	if timeoutMs <= 0 {
		timeoutMs = g.p.cfg.ToolTimeoutMs
	}
	cctx, cancel := g.callCtx(ctx, ms(timeoutMs))
	defer cancel()
	var out struct {
		Content string `json:"content"`
		IsError bool   `json:"isError"`
		Data    any    `json:"data"`
	}
	if err := g.conn.Call(cctx, "tool/execute", map[string]any{"tool": name, "call": callView(call)}, &out); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return tools.Errorf("plugin %s: tool %s timed out", g.p.m.Name, name), nil
		}
		return tools.Errorf("plugin %s: %s", g.p.m.Name, capDiag(err.Error())), nil
	}
	return tools.Result{Content: out.Content, IsError: out.IsError, Data: out.Data}, nil
}

// preExecute lets the plugin deny or annotate a call. A timeout or error is
// "next() unchanged". A plugin can never allow: other verdicts are ignored,
// so a deny stays a deny and an ask stays an ask.
func (g *generation) preExecute(ctx context.Context, d *tools.Decision, next kernel.Next[*tools.Decision, *tools.Decision]) (*tools.Decision, error) {
	cctx, cancel := g.callCtx(ctx, ms(g.p.cfg.HookTimeoutMs))
	view := map[string]any{"call": callView(*d.Call), "category": d.Category, "summary": d.Summary,
		"verdict": d.Verdict, "reason": d.Reason}
	if d.Tool != nil {
		view["tool"] = d.Tool.Name
	}
	var out *struct {
		Verdict string `json:"verdict"`
		Reason  string `json:"reason"`
		Note    string `json:"note"`
	}
	err := g.conn.Call(cctx, "tools/pre-execute", view, &out)
	cancel()
	if err != nil {
		g.p.log.Warn("plugin pre-execute hook failed or timed out; continuing unchanged", "err", err)
		return next(ctx, d)
	}
	if out != nil {
		if n := strings.TrimSpace(out.Note); n != "" {
			d.Summary += " [" + g.p.m.Name + ": " + capText(n, 200) + "]"
		}
		if out.Verdict == string(tools.Deny) {
			reason := capText(strings.TrimSpace(out.Reason), 500)
			if reason == "" {
				reason = "denied"
			}
			d.DenyWith("plugin " + g.p.m.Name + ": " + reason)
			return d, nil
		}
	}
	return next(ctx, d)
}

// scrub removes known credential values before a payload leaves the host.
func (g *generation) scrub(s string) string {
	cs, ok := kernel.Lookup[*credentials.Service](g.ctx, credentials.ServiceKey)
	if !ok {
		return s
	}
	for _, v := range cs.Secrets() {
		if len(v) >= 4 {
			s = strings.ReplaceAll(s, v, "[REDACTED]")
		}
	}
	return s
}

func (g *generation) postExecute(ctx context.Context, exe tools.Execution, next kernel.Next[tools.Execution, tools.Result]) (tools.Result, error) {
	res, err := next(ctx, exe)
	if err != nil {
		return res, err
	}
	cctx, cancel := g.callCtx(ctx, ms(g.p.cfg.HookTimeoutMs))
	defer cancel()
	shown := res
	shown.Content = g.scrub(shown.Content)
	var out *struct {
		Content *string `json:"content"`
		IsError *bool   `json:"isError"`
	}
	if err := g.conn.Call(cctx, "tools/post-execute", executionView(exe, shown), &out); err != nil {
		g.p.log.Warn("plugin post-execute hook failed or timed out; result unchanged", "err", err)
		return res, nil
	}
	if out != nil {
		if out.Content != nil {
			res.Content = *out.Content
		}
		if out.IsError != nil {
			res.IsError = *out.IsError
		}
	}
	return res, nil
}

type channel struct {
	g    *generation
	name string
}

func (c channel) Name() string { return c.name }

func (c channel) Send(ctx context.Context, n notify.Notification) error {
	cctx, cancel := c.g.callCtx(ctx, 10*time.Second)
	defer cancel()
	return c.g.conn.Call(cctx, "notify/send", map[string]any{"channel": c.name, "notification": n}, nil)
}

// ---------------------------------------------------------------- plugin → host

// hostRequest serves host services to the plugin. Every method is checked
// against the granted permissions; undeclared access is an error.
func (g *generation) hostRequest(ctx context.Context, method string, params json.RawMessage) (any, error) {
	p := g.p
	switch method {
	case "log":
		var in struct {
			Level   string `json:"level"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(params, &in)
		msg := capDiag(in.Message)
		switch in.Level {
		case "error":
			p.log.Error("plugin log", "msg", msg)
		case "warn", "warning":
			p.log.Warn("plugin log", "msg", msg)
		case "debug":
			p.log.Debug("plugin log", "msg", msg)
		default:
			p.log.Info("plugin log", "msg", msg)
		}
		return nil, nil
	case "notify.send":
		if !p.granted.HasService(notify.ServiceKey) {
			return nil, denied("service %q not declared/approved", notify.ServiceKey)
		}
		svc, ok := kernel.Lookup[*notify.Service](g.ctx, notify.ServiceKey)
		if !ok {
			return nil, jsonrpc.NewError(CodeUnavailable, "notify service unavailable", nil)
		}
		var n notify.Notification
		if err := json.Unmarshal(params, &n); err != nil || strings.TrimSpace(n.Title) == "" {
			return nil, jsonrpc.NewError(jsonrpc.CodeInvalidParams, "title is required", nil)
		}
		if n.Kind == "" {
			n.Kind = "plugin"
		}
		n.ID, n.Title, n.Body = "", capText(n.Title, 200), capText(n.Body, maxDiag)
		errs := svc.Send(ctx, n)
		var es []string
		for _, e := range errs {
			es = append(es, capDiag(e.Error()))
		}
		return map[string]any{"errors": es}, nil
	case "credentials.resolve":
		var in struct {
			Ref string `json:"ref"`
		}
		_ = json.Unmarshal(params, &in)
		if !p.granted.HasService(credentials.ServiceKey) {
			return nil, denied("service %q not declared/approved", credentials.ServiceKey)
		}
		if in.Ref == "" || !slices.Contains(p.granted.Credentials, in.Ref) {
			return nil, denied("credential %q not declared/approved", in.Ref)
		}
		svc, ok := kernel.Lookup[*credentials.Service](g.ctx, credentials.ServiceKey)
		if !ok {
			return nil, jsonrpc.NewError(CodeUnavailable, "credentials service unavailable", nil)
		}
		ref := in.Ref
		if !strings.Contains(ref, ":") {
			ref = "vault:crewbase/" + ref // bare names live in the Crewbase vault
		}
		v, err := svc.Resolve(ctx, ref)
		if err != nil {
			if errors.Is(err, credentials.ErrNotFound) {
				return nil, jsonrpc.NewError(CodeUnavailable, "credential not configured", nil)
			}
			return nil, jsonrpc.NewError(CodeUnavailable, "credential unavailable", nil)
		}
		return map[string]string{"value": v}, nil
	}
	return nil, jsonrpc.ErrMethodNotFound
}
