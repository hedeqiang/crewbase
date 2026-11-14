// Package gateway is the remote gateway: an HTTP API whose methods are an
// explicit whitelist. Every call runs authenticate → validate → resolve and
// activate; a rejected call never activates an agent nor reveals whether an
// object exists. Plugins add methods with Gateway.Handle (removed when the
// plugin unloads). Live updates are a Server-Sent Events stream.
package gateway

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/kernel"
)

const ServiceKey = "gateway"

// Request is a validated call passed to handlers.
type Request struct {
	HTTP   *http.Request
	Params map[string]string // path params
	Body   json.RawMessage
	Device string // authenticated device class: desktop | mobile | im
}

// Decode unmarshals the JSON body.
func (r *Request) Decode(v any) error {
	if len(r.Body) == 0 {
		return nil
	}
	if err := json.Unmarshal(r.Body, v); err != nil {
		return BadRequest("invalid JSON body: %v", err)
	}
	return nil
}

// Error is an API error with a status.
type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

func BadRequest(f string, a ...any) error {
	return &Error{Status: 400, Code: "bad_request", Message: fmt.Sprintf(f, a...)}
}

// NotFound is deliberately generic.
func NotFound() error { return &Error{Status: 404, Code: "not_found", Message: "not found"} }

func Conflict(f string, a ...any) error {
	return &Error{Status: 409, Code: "conflict", Message: fmt.Sprintf(f, a...)}
}

// Route is one whitelisted remote method.
type Route struct {
	Method string // GET POST PATCH DELETE
	Path   string // /api/agents/{id}
	// Devices allowed to call it ("" = desktop only). Mobile devices may
	// view, approve and send messages; not change rules or plugins.
	Devices []string
	// Public routes skip token auth; their Validate must authenticate the
	// caller itself (e.g. a webhook secret). Used only for webhooks.
	Public bool
	// Validate runs before Handle and must not touch state.
	Validate func(r *Request) error
	Handle   func(ctx context.Context, r *Request) (any, error)
	// MaxBody caps the request body (default DefaultMaxBody); larger
	// bodies are refused with 413 rather than truncated.
	MaxBody int64
}

// DefaultMaxBody is the request body cap for routes that set none.
const DefaultMaxBody = 8 << 20

// Gateway is the `gateway` service.
type Gateway struct {
	mu     sync.RWMutex
	routes []*Route
	tokens map[string]string // token → device class
	hub    *Hub
}

// Handle registers a remote method bound to ctx.
func (g *Gateway) Handle(c *kernel.Context, r Route) {
	rp := &r
	g.mu.Lock()
	g.routes = append(g.routes, rp)
	g.mu.Unlock()
	c.Defer(func() {
		g.mu.Lock()
		for i, x := range g.routes {
			if x == rp {
				g.routes = append(g.routes[:i:i], g.routes[i+1:]...)
				break
			}
		}
		g.mu.Unlock()
	})
}

// Hub returns the SSE hub.
func (g *Gateway) Hub() *Hub { return g.hub }

// AddToken authorizes a token for a device class.
func (g *Gateway) AddToken(token, device string) {
	g.mu.Lock()
	g.tokens[token] = device
	g.mu.Unlock()
}

// match matches the escaped request path against pattern, segment by
// segment, so a parameter may contain an encoded "/" (%2F), as approval
// call ids do ("acp/<session>/<tool>", "<run>/<step>").
func match(pattern, escapedPath string) (map[string]string, bool) {
	ps := strings.Split(strings.Trim(pattern, "/"), "/")
	xs := strings.Split(strings.Trim(escapedPath, "/"), "/")
	if len(ps) != len(xs) {
		return nil, false
	}
	params := map[string]string{}
	for i := range ps {
		seg, err := url.PathUnescape(xs[i])
		if err != nil {
			return nil, false
		}
		if strings.HasPrefix(ps[i], "{") && strings.HasSuffix(ps[i], "}") {
			if seg == "" {
				return nil, false
			}
			params[ps[i][1:len(ps[i])-1]] = seg
		} else if ps[i] != seg {
			return nil, false
		}
	}
	return params, true
}

func (g *Gateway) auth(r *http.Request) (string, bool) {
	tok := ""
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		tok = strings.TrimPrefix(h, "Bearer ")
	} else if q := r.URL.Query().Get("token"); q != "" {
		tok = q
	}
	if tok == "" {
		return "", false
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	for t, dev := range g.tokens {
		if subtle.ConstantTimeCompare([]byte(t), []byte(tok)) == 1 {
			return dev, true
		}
	}
	return "", false
}

func (g *Gateway) isPublic(r *http.Request) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, rt := range g.routes {
		if rt.Public && rt.Method == r.Method {
			if _, ok := match(rt.Path, r.URL.EscapedPath()); ok {
				return true
			}
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	var e *Error
	if !errors.As(err, &e) {
		e = &Error{Status: 500, Code: "internal", Message: err.Error()}
	}
	writeJSON(w, e.Status, map[string]any{"error": e})
}

// ServeHTTP implements the API.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. authenticate (public webhook routes authenticate in Validate)
	dev, ok := g.auth(r)
	if !ok && !g.isPublic(r) {
		writeErr(w, &Error{Status: 401, Code: "unauthorized", Message: "missing or invalid token"})
		return
	}
	if r.URL.Path == "/api/events" && r.Method == http.MethodGet {
		g.hub.serve(w, r)
		return
	}
	g.mu.RLock()
	var route *Route
	var params map[string]string
	methodSeen := false
	for _, rt := range g.routes {
		if p, ok := match(rt.Path, r.URL.EscapedPath()); ok {
			methodSeen = true
			if rt.Method == r.Method {
				route, params = rt, p
				break
			}
		}
	}
	g.mu.RUnlock()
	if route == nil {
		if methodSeen {
			writeErr(w, &Error{Status: 405, Code: "method_not_allowed", Message: "method not allowed"})
		} else {
			writeErr(w, NotFound())
		}
		return
	}
	if !ok && !route.Public {
		writeErr(w, &Error{Status: 401, Code: "unauthorized", Message: "missing or invalid token"})
		return
	}
	allowed := dev == "desktop" || route.Public
	for _, d := range route.Devices {
		if d == dev {
			allowed = true
		}
	}
	if !allowed {
		writeErr(w, &Error{Status: 403, Code: "forbidden", Message: "not allowed from this device"})
		return
	}
	// 2. validate
	limit := route.MaxBody
	if limit <= 0 {
		limit = DefaultMaxBody
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		writeErr(w, BadRequest("read body: %v", err))
		return
	}
	if int64(len(body)) > limit {
		writeErr(w, &Error{Status: 413, Code: "too_large", Message: fmt.Sprintf("request body is larger than %d MB", limit>>20)})
		return
	}
	req := &Request{HTTP: r, Params: params, Body: body, Device: dev}
	if len(body) > 0 && !json.Valid(body) {
		writeErr(w, BadRequest("body must be JSON"))
		return
	}
	if route.Validate != nil {
		if err := route.Validate(req); err != nil {
			writeErr(w, err)
			return
		}
	}
	// 3. resolve + activate
	out, err := route.Handle(r.Context(), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	if out == nil {
		out = map[string]bool{"ok": true}
	}
	writeJSON(w, 200, out)
}

// ---------------------------------------------------------------- SSE hub

// Message is one pushed update.
type Message struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// Hub fans messages out to SSE subscribers.
type Hub struct {
	mu   sync.Mutex
	subs map[chan Message]struct{}
}

func NewHub() *Hub { return &Hub{subs: map[chan Message]struct{}{}} }

// Publish sends to every subscriber; slow subscribers drop messages and
// are expected to resync via the REST API.
func (h *Hub) Publish(typ string, data any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- Message{Type: typ, Data: data}:
		default:
		}
	}
}

func (h *Hub) subscribe() chan Message {
	ch := make(chan Message, 256)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *Hub) unsubscribe(ch chan Message) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

func (h *Hub) serve(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	ch := h.subscribe()
	defer h.unsubscribe(ch)
	fmt.Fprintf(w, "event: hello\ndata: {}\n\n")
	fl.Flush()
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			fmt.Fprintf(w, ": ping\n\n")
			fl.Flush()
		case m := <-ch:
			b, err := json.Marshal(m.Data)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", m.Type, b)
			fl.Flush()
		}
	}
}

// ---------------------------------------------------------------- plugin

// Config of the gateway.
type Config struct {
	Addr      string `json:"addr"`      // default 127.0.0.1:7788
	TokenFile string `json:"tokenFile"` // created if missing (0600)
	Token     string `json:"token"`     // fixed token (tests)
	WebDir    string `json:"webDir"`    // static UI directory (optional)
}

// Static is an optional embedded UI filesystem set by the binary.
var Static fs.FS

// LoadOrCreateToken reads or creates the desktop token.
func LoadOrCreateToken(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) >= 32 {
		return strings.TrimSpace(string(b)), nil
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(buf)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	return tok, os.WriteFile(path, []byte(tok+"\n"), 0o600)
}

// Plugin serves the API (and UI) on a local address.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/gateway",
	Description: "HTTP API with remote-method whitelist and SSE events.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		if cfg.Addr == "" {
			cfg.Addr = "127.0.0.1:7788"
		}
		g := &Gateway{tokens: map[string]string{}, hub: NewHub()}
		tok := cfg.Token
		if tok == "" && cfg.TokenFile != "" {
			if tok, err = LoadOrCreateToken(cfg.TokenFile); err != nil {
				return err
			}
		}
		if tok != "" {
			g.AddToken(tok, "desktop")
		}
		if err := c.Provide(ServiceKey, g); err != nil {
			return err
		}
		mux := http.NewServeMux()
		mux.Handle("/api/", g)
		var static fs.FS = Static
		if cfg.WebDir != "" {
			static = os.DirFS(cfg.WebDir)
		}
		if static != nil {
			mux.Handle("/", spaHandler(static))
		}
		ln, err := net.Listen("tcp", cfg.Addr)
		if err != nil {
			return err
		}
		srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		c.Go(func(ctx context.Context) {
			go func() {
				<-ctx.Done()
				sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = srv.Shutdown(sctx)
			}()
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				c.Log().Error("gateway serve", "err", err)
			}
		})
		c.Log().Info("gateway listening", "addr", ln.Addr().String())
		Listening.Emit(c.Host(), ln.Addr().String())
		return nil
	},
})

// Listening reports the bound address.
var Listening = kernel.NewEmit[string]("gateway/listening")

func spaHandler(fsys fs.FS) http.Handler {
	files := http.FileServer(http.FS(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(fsys, p); err != nil {
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}
