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

	"github.com/hedeqiang/crewbase/internal/spa"

	"github.com/hedeqiang/crewbase/kernel"
)

const ServiceKey = "gateway"

// Request is a validated call passed to handlers.
type Request struct {
	HTTP   *http.Request
	Params map[string]string // path params
	Body   json.RawMessage
	Device string // authenticated device class: desktop | mobile | im
	// DeviceID identifies a paired device ("" for the local desktop token).
	DeviceID string
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
	// RawBody accepts a non-JSON body (file uploads); Decode is then unusable.
	RawBody bool
}

// Reply is a JSON response that also sets cookies (e.g. signing an
// embedded local app in).
type Reply struct {
	Data    any
	Cookies []*http.Cookie
}

// Blob is a non-JSON response (file downloads). Only raster images are
// served inline; anything else downloads as an attachment, so uploaded
// HTML or SVG can never run in the app's origin.
type Blob struct {
	Name string
	Mime string
	Data []byte
	// Content, when set, is served instead of Data with Range support
	// (video playback seeks); it is closed after the response.
	Content io.ReadSeeker
	ModTime time.Time
	// NoStore: never cache (live frames).
	NoStore bool
	// Meta becomes X-Crewbase-<Key> response headers (ASCII values; others
	// are dropped).
	Meta map[string]string
}

func hdrSafe(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return len(s) <= 2048
}

// inlineTypes are the only types shown in the app's origin: raster images
// and video. Everything else (HTML, SVG, PDF, …) is a download.
var inlineTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true, "video/mp4": true, "video/webm": true}

func writeBlob(w http.ResponseWriter, r *http.Request, b *Blob) {
	if c, ok := b.Content.(io.Closer); ok {
		defer c.Close()
	}
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cache-Control", "private, max-age=300")
	if b.NoStore {
		h.Set("Cache-Control", "no-store")
	}
	for k, v := range b.Meta {
		if hdrSafe(k) && hdrSafe(v) {
			h.Set("X-Crewbase-"+k, v)
		}
	}
	if inlineTypes[b.Mime] {
		h.Set("Content-Type", b.Mime)
		h.Set("Content-Disposition", "inline; filename*=UTF-8''"+url.PathEscape(b.Name))
	} else {
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(b.Name))
	}
	if b.Content != nil {
		http.ServeContent(w, r, "", b.ModTime, b.Content) // Range, If-Modified-Since
		return
	}
	w.WriteHeader(200)
	_, _ = w.Write(b.Data)
}

// DefaultMaxBody is the request body cap for routes that set none.
const DefaultMaxBody = 8 << 20

// Gateway is the `gateway` service.
type Gateway struct {
	mu        sync.RWMutex
	routes    []*Route
	tokens    map[string]string // token → device class
	verifiers []*Verifier
	hub       *Hub
	host      *kernel.Host
	addr      string // bound address (host:port)
	guard     Guard
	mounts    map[string]http.Handler // exact path → handler (see Mount)
}

// Addr is the address the gateway listens on ("" before it is bound).
func (g *Gateway) Addr() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.addr
}

// Redirect is a response that sends the browser elsewhere (only to a path
// on this same origin).
type Redirect struct {
	Path string // must start with "/" (not "//")
}

// Verifier authenticates tokens issued elsewhere (paired devices). It
// returns the device class and the device id. It must compare in
// constant time (e.g. by hashing the token first).
type Verifier func(token string) (device, id string, ok bool)

// AddVerifier registers a token verifier for ctx's lifetime.
func (g *Gateway) AddVerifier(c *kernel.Context, v Verifier) {
	p := &v
	g.mu.Lock()
	g.verifiers = append(g.verifiers, p)
	g.mu.Unlock()
	c.Defer(func() {
		g.mu.Lock()
		for i, x := range g.verifiers {
			if x == p {
				g.verifiers = append(g.verifiers[:i:i], g.verifiers[i+1:]...)
				break
			}
		}
		g.mu.Unlock()
	})
}

// AuthFailure reports a rejected token (not a missing one).
type AuthFailure struct {
	Remote string `json:"remote"`
	Path   string `json:"path"`
}

// AuthFailed fires when a request carries a token that is not accepted.
var AuthFailed = kernel.NewEmit[AuthFailure]("gateway/auth-failed")

// Mount serves h at exactly path (outside /api/, e.g. "/mcp") for ctx's
// lifetime. Mounted handlers skip the gateway entirely: no token check, no
// device classes, no guard. They must authenticate every request
// themselves, with credentials of their own: gateway tokens are not
// accepted there, and their credentials never reach the /api/ routes.
func (g *Gateway) Mount(c *kernel.Context, path string, h http.Handler) {
	g.mu.Lock()
	if g.mounts == nil {
		g.mounts = map[string]http.Handler{}
	}
	g.mounts[path] = h
	g.mu.Unlock()
	c.Defer(func() {
		g.mu.Lock()
		if g.mounts[path] == h {
			delete(g.mounts, path)
		}
		g.mu.Unlock()
	})
}

func (g *Gateway) mounted(path string) http.Handler {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.mounts[path]
}

// Kick closes the event streams of a device (after it is revoked).
func (g *Gateway) Kick(deviceID string) { g.hub.kick(deviceID) }

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

// Guard checks a request against what the person behind it may reach
// (route is the matched route path, e.g. /api/agents/{id}). Set by the
// access layer (teams); nil allows everything the device tier allows.
type Guard func(r *Request, route string) error

// SetGuard installs the access guard.
func (g *Gateway) SetGuard(f Guard) {
	g.mu.Lock()
	g.guard = f
	g.mu.Unlock()
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

func bearer(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return r.URL.Query().Get("token")
}

// auth returns the device class and id of r's token.
func (g *Gateway) auth(r *http.Request) (dev, id string, ok bool) {
	return g.Verify(bearer(r))
}

// Verify checks a token (the local tokens, then registered verifiers).
func (g *Gateway) Verify(tok string) (dev, id string, ok bool) {
	if tok == "" {
		return "", "", false
	}
	g.mu.RLock()
	vs := append([]*Verifier(nil), g.verifiers...)
	for t, d := range g.tokens {
		if subtle.ConstantTimeCompare([]byte(t), []byte(tok)) == 1 {
			g.mu.RUnlock()
			return d, "", true
		}
	}
	g.mu.RUnlock()
	for _, v := range vs {
		if d, i, ok := (*v)(tok); ok {
			return d, i, true
		}
	}
	return "", "", false
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
	dev, devID, ok := g.auth(r)
	if !ok && bearer(r) != "" && g.host != nil {
		AuthFailed.Emit(g.host, AuthFailure{Remote: r.RemoteAddr, Path: r.URL.Path})
	}
	if !ok && !g.isPublic(r) {
		writeErr(w, &Error{Status: 401, Code: "unauthorized", Message: "missing or invalid token"})
		return
	}
	if r.URL.Path == "/api/events" && r.Method == http.MethodGet {
		g.hub.serve(w, r, devID)
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
		// viewers read whatever phones may read, and change nothing
		if d == dev || (dev == "viewer" && d == "mobile" && r.Method == http.MethodGet) {
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
	req := &Request{HTTP: r, Params: params, Body: body, Device: dev, DeviceID: devID}
	if len(body) > 0 && !route.RawBody && !json.Valid(body) {
		writeErr(w, BadRequest("body must be JSON"))
		return
	}
	g.mu.RLock()
	guard := g.guard
	g.mu.RUnlock()
	if guard != nil && !route.Public {
		if err := guard(req, route.Path); err != nil {
			writeErr(w, err)
			return
		}
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
	if b, ok := out.(*Blob); ok {
		writeBlob(w, r, b)
		return
	}
	if rp, ok := out.(*Reply); ok {
		for _, ck := range rp.Cookies {
			http.SetCookie(w, ck)
		}
		out = rp.Data
		if out == nil {
			out = map[string]bool{"ok": true}
		}
	}
	if rd, ok := out.(*Redirect); ok {
		p := rd.Path
		if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\r\n\\") {
			p = "/"
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		http.Redirect(w, r, p, http.StatusFound)
		return
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
	mu     sync.Mutex
	subs   map[chan Message]string // → device id ("" = local)
	gone   map[chan Message]chan struct{}
	filter func(deviceID string, m Message) bool
}

// SetFilter decides per subscriber which messages it receives (e.g. only
// what the person behind the device may see). nil = everything.
func (h *Hub) SetFilter(f func(deviceID string, m Message) bool) {
	h.mu.Lock()
	h.filter = f
	h.mu.Unlock()
}

func NewHub() *Hub {
	return &Hub{subs: map[chan Message]string{}, gone: map[chan Message]chan struct{}{}}
}

func (h *Hub) kick(deviceID string) {
	if deviceID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch, id := range h.subs {
		if id == deviceID {
			close(h.gone[ch])
			delete(h.gone, ch)
			delete(h.subs, ch)
		}
	}
}

// Publish sends to every subscriber; slow subscribers drop messages and
// are expected to resync via the REST API.
func (h *Hub) Publish(typ string, data any) {
	m := Message{Type: typ, Data: data}
	h.mu.Lock()
	subs := make(map[chan Message]string, len(h.subs))
	for ch, id := range h.subs {
		subs[ch] = id
	}
	filter := h.filter
	h.mu.Unlock()
	// filtered outside the lock: the filter may look up agents and sessions
	for ch, id := range subs {
		if filter != nil && !filter(id, m) {
			continue
		}
		select {
		case ch <- m:
		default:
		}
	}
}

func (h *Hub) subscribe(deviceID string) (chan Message, chan struct{}) {
	ch := make(chan Message, 256)
	gone := make(chan struct{})
	h.mu.Lock()
	h.subs[ch] = deviceID
	h.gone[ch] = gone
	h.mu.Unlock()
	return ch, gone
}

func (h *Hub) unsubscribe(ch chan Message) {
	h.mu.Lock()
	delete(h.subs, ch)
	delete(h.gone, ch)
	h.mu.Unlock()
}

func (h *Hub) serve(w http.ResponseWriter, r *http.Request, deviceID string) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	ch, gone := h.subscribe(deviceID)
	defer h.unsubscribe(ch)
	fmt.Fprintf(w, "event: hello\ndata: {}\n\n")
	fl.Flush()
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-gone:
			return // the device was revoked
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
		g := &Gateway{tokens: map[string]string{}, hub: NewHub(), host: c.Host()}
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
		var ui http.Handler = http.NotFoundHandler()
		if static != nil {
			ui = spa.Handler(static)
		}
		mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if h := g.mounted(r.URL.Path); h != nil {
				h.ServeHTTP(w, r)
				return
			}
			ui.ServeHTTP(w, r)
		}))
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
		g.mu.Lock()
		g.addr = ln.Addr().String()
		g.mu.Unlock()
		c.Log().Info("gateway listening", "addr", ln.Addr().String())
		Listening.Emit(c.Host(), ln.Addr().String())
		return nil
	},
})

// Listening reports the bound address.
var Listening = kernel.NewEmit[string]("gateway/listening")
