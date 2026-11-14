package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hedeqiang/crewbase/apps/launch"
	"github.com/hedeqiang/crewbase/apps/web"
	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/capabilities/notify"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// defaultAddr is where the desktop profile's gateway listens; CREWBASE_ADDR
// overrides it (development, tests, a second profile).
func defaultAddr() string {
	if v := os.Getenv("CREWBASE_ADDR"); v != "" {
		return v
	}
	return "127.0.0.1:7788"
}

// daemon is the crewd the window talks to: one started in this process
// (host != nil), or one already running that the shell attached to.
type daemon struct {
	root  string // ${CREWBASE_HOME:-~/.crewbase}
	addr  string // host:port of the gateway
	token string // desktop token (${root}/token)

	host      *kernel.Host // in-process mode only
	closeOnce sync.Once
	closed    atomic.Bool
	stopping  atomic.Bool // a quit is closing the host

	// signals to the shell; never block the sender (kernel goroutines must
	// not wait on the UI thread, see closeHost).
	changed chan struct{}            // the "needs you" set may have changed
	notes   chan notify.Notification // native notifications to show
}

func (d *daemon) attached() bool { return d.host == nil }

func (d *daemon) baseURL() string { return "http://" + d.addr }

func (d *daemon) poke() {
	select {
	case d.changed <- struct{}{}:
	default:
	}
}

func (d *daemon) post(n notify.Notification) {
	select {
	case d.notes <- n:
	default: // the shell is behind: drop rather than block a plugin
	}
}

// startDaemon attaches to a running crewd that accepts our token on
// 127.0.0.1:7788 (CREWBASE_ADDR), or starts the host in this process: on 7788 when the port
// is free, else on a free port (the address comes from gateway.Listening).
func startDaemon() (*daemon, error) {
	d := &daemon{
		root:    launch.DefaultRoot(),
		changed: make(chan struct{}, 1),
		notes:   make(chan notify.Notification, 64),
	}
	if b, err := os.ReadFile(filepath.Join(d.root, "token")); err == nil {
		d.token = strings.TrimSpace(string(b))
	}
	want := defaultAddr()
	if d.token != "" && healthy(want, d.token) {
		d.addr = want
		slog.Info("desktop: attached to running crewd", "addr", d.addr, "root", d.root)
		go d.watchEvents()
		return d, nil
	}
	addr := want
	if ln, err := net.Listen("tcp", want); err != nil {
		slog.Warn("desktop: the port is taken by something that does not accept our token; using a free port", "addr", want, "err", err)
		addr = "127.0.0.1:0"
	} else {
		ln.Close()
	}
	if err := d.startHost(addr); err != nil {
		return nil, err
	}
	slog.Info("desktop: started crewd in-process", "addr", d.addr, "root", d.root)
	return d, nil
}

// healthy reports whether a crewd answers /api/health at addr with token.
func healthy(addr, token string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/api/health", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// startHost is launch.Start with two hooks it has no room for: listeners on
// the host before the entries apply (gateway.Listening is emitted while the
// gateway plugin applies, so it must be observed from the start), and the
// desktop-native notify channel, loaded as a plugin so it follows the
// notify service through reloads.
func (d *daemon) startHost(addr string) error {
	if web.Available() {
		gateway.Static = web.Dist
	} else {
		slog.Warn("desktop: built without the web UI (run `make web`); the window will show the API only")
	}
	o := launch.Options{Profile: "desktop", Root: d.root, Addr: addr}
	if err := os.MkdirAll(o.Root, 0o700); err != nil {
		return err
	}
	entries, err := launch.Resolve(o)
	if err != nil {
		return err
	}
	h := kernel.NewHost(nil)
	var listening atomic.Value
	gateway.Listening.Listen(h.Root(), func(a string) { listening.Store(a) })
	approval.Changed.Listen(h.Root(), func(struct{}) { d.poke() })
	agent.StatusChanged.Listen(h.Root(), func(agent.StatusEvent) { d.poke() })
	if _, err := h.Load(kernel.Spec{
		Name:        "@crewbase/desktop-native",
		Inject:      []string{notify.ServiceKey},
		Description: "Native desktop notifications (Crewbase.app).",
		Apply: func(c *kernel.Context) error {
			kernel.Use[*notify.Service](c, notify.ServiceKey).AddChannel(c, nativeChannel{d})
			return nil
		},
	}, kernel.LoadOptions{ID: "desktop-native"}); err != nil {
		return err
	}
	if err := h.ApplyEntries(entries); err != nil {
		h.Log().Warn("some plugins failed", "err", err)
	}
	for _, p := range h.Plugins() {
		if p.State != kernel.StateActive {
			slog.Warn("plugin not active", "id", p.ID, "state", p.State, "missing", p.Missing, "err", p.Error)
		}
	}
	a, _ := listening.Load().(string)
	if a == "" {
		h.Close()
		return errors.New("the gateway did not start (see the log)")
	}
	tok, err := gateway.LoadOrCreateToken(filepath.Join(d.root, "token"))
	if err != nil {
		h.Close()
		return fmt.Errorf("token: %w", err)
	}
	d.host, d.addr, d.token = h, a, tok
	return nil
}

// closeHost stops the in-process host: plugins unload in reverse order,
// subprocess trees (codex, claude, MCP servers) are killed and sessions
// flushed. Safe to call more than once and in attach mode (no-op).
func (d *daemon) closeHost() {
	if d.host == nil {
		return
	}
	d.closeOnce.Do(func() {
		start := time.Now()
		d.host.Close()
		d.closed.Store(true)
		slog.Info("desktop: host closed", "took", time.Since(start).Round(time.Millisecond))
	})
}

// nativeChannel is the notify channel "desktop-native": it hands the
// notification to the shell, which shows it with the system.
type nativeChannel struct{ d *daemon }

func (nativeChannel) Name() string { return "desktop-native" }
func (c nativeChannel) Send(_ context.Context, n notify.Notification) error {
	c.d.post(n)
	return nil
}
