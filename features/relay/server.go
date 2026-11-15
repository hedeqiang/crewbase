package relay

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hedeqiang/crewbase/internal/spa"

	"github.com/coder/websocket"
)

// ServerConfig configures a relay server.
type ServerConfig struct {
	// MaxHosts caps hosts online at once (0 = 1000).
	MaxHosts int
	// MaxClients caps device connections per host (0 = 32).
	MaxClients int
	// Web is the web app served at / (optional).
	Web fs.FS
	// Origins devices may connect from ("" = same host only).
	Origins []string
	Log     *slog.Logger
}

// Server forwards frames between hosts and their devices. It keeps no
// state beyond the open connections and never decrypts anything.
type Server struct {
	cfg ServerConfig

	mu    sync.Mutex
	hosts map[string]*hostConn
}

type hostConn struct {
	id      string
	c       *websocket.Conn
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	clients map[uint32]*websocket.Conn
	nextSID atomic.Uint32
}

// NewServer makes a relay server.
func NewServer(cfg ServerConfig) *Server {
	if cfg.MaxHosts == 0 {
		cfg.MaxHosts = 1000
	}
	if cfg.MaxClients == 0 {
		cfg.MaxClients = 32
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Server{cfg: cfg, hosts: map[string]*hostConn{}}
}

// Online reports whether a host is connected.
func (s *Server) Online(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hosts[id] != nil
}

// Handler serves /v1/host, /v1/client, /healthz and the web app.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/host", s.serveHost)
	mux.HandleFunc("GET /v1/client", s.serveClient)
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"online": s.Online(r.URL.Query().Get("host"))})
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	if s.cfg.Web != nil {
		mux.Handle("/", spa.Handler(s.cfg.Web))
	}
	return securityHeaders(mux)
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer") // pairing links carry secrets in the fragment
		w.Header().Set("X-Frame-Options", "DENY")
		h.ServeHTTP(w, r)
	})
}

func (s *Server) accept(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.cfg.Origins})
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(MaxFrame + 64)
	return c, nil
}

func (s *Server) serveHost(w http.ResponseWriter, r *http.Request) {
	pub, err := base64.RawURLEncoding.DecodeString(r.URL.Query().Get("pub"))
	if err != nil || len(pub) != 65 {
		http.Error(w, "pub must be a base64url P-256 public key", http.StatusBadRequest)
		return
	}
	c, err := s.accept(w, r)
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	// prove possession of the key behind the id
	ch := make([]byte, 32)
	_, _ = rand.Read(ch)
	if err := writeJSON(ctx, c, authMsg{Challenge: ch}); err != nil {
		return
	}
	actx, acancel := context.WithTimeout(ctx, 10*time.Second)
	var ans authMsg
	err = readJSON(actx, c, &ans)
	acancel()
	if err != nil || !VerifyHost(pub, ch, ans.Sig) {
		_ = writeJSON(ctx, c, authMsg{Error: "host authentication failed"})
		c.Close(websocket.StatusPolicyViolation, "auth failed")
		return
	}
	id := HostIDOf(pub)
	h := &hostConn{id: id, c: c, ctx: ctx, cancel: cancel, clients: map[uint32]*websocket.Conn{}}
	s.mu.Lock()
	if len(s.hosts) >= s.cfg.MaxHosts && s.hosts[id] == nil {
		s.mu.Unlock()
		_ = writeJSON(ctx, c, authMsg{Error: "relay is full"})
		c.Close(websocket.StatusTryAgainLater, "full")
		return
	}
	old := s.hosts[id]
	s.hosts[id] = h
	s.mu.Unlock()
	if old != nil {
		old.cancel() // the same host reconnected; drop the stale link
		old.c.Close(websocket.StatusGoingAway, "replaced")
	}
	if err := writeJSON(ctx, c, authMsg{OK: true, ID: id}); err != nil {
		return
	}
	s.cfg.Log.Info("host online", "host", id)
	defer func() {
		s.mu.Lock()
		if s.hosts[id] == h {
			delete(s.hosts, id)
		}
		s.mu.Unlock()
		h.mu.Lock()
		for _, cc := range h.clients {
			cc.Close(websocket.StatusGoingAway, "host offline")
		}
		h.mu.Unlock()
		s.cfg.Log.Info("host offline", "host", id)
	}()
	go keepalive(ctx, c)
	for {
		typ, b, err := c.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageBinary {
			continue
		}
		ft, sid, payload, err := DecodeFrame(b)
		if err != nil {
			continue
		}
		h.mu.Lock()
		cc := h.clients[sid]
		h.mu.Unlock()
		if cc == nil {
			continue
		}
		switch ft {
		case FrameData:
			wctx, wc := context.WithTimeout(ctx, 30*time.Second)
			if cc.Write(wctx, websocket.MessageBinary, payload) != nil {
				cc.Close(websocket.StatusGoingAway, "write failed")
			}
			wc()
		case FrameClose:
			cc.Close(websocket.StatusNormalClosure, "closed by host")
		}
	}
}

func (s *Server) serveClient(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("host")
	s.mu.Lock()
	h := s.hosts[id]
	s.mu.Unlock()
	c, err := s.accept(w, r)
	if err != nil {
		return
	}
	if h == nil {
		c.Close(4404, "host offline")
		return
	}
	h.mu.Lock()
	if len(h.clients) >= s.cfg.MaxClients {
		h.mu.Unlock()
		c.Close(websocket.StatusTryAgainLater, "too many devices")
		return
	}
	sid := h.nextSID.Add(1)
	h.clients[sid] = c
	h.mu.Unlock()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(h.ctx, cancel) // host gone → drop the device
	defer stop()
	defer func() {
		h.mu.Lock()
		delete(h.clients, sid)
		h.mu.Unlock()
		wctx, wc := context.WithTimeout(context.Background(), 5*time.Second)
		_ = h.c.Write(wctx, websocket.MessageBinary, EncodeFrame(FrameClose, sid, nil))
		wc()
		c.CloseNow()
	}()
	if err := h.c.Write(ctx, websocket.MessageBinary, EncodeFrame(FrameOpen, sid, nil)); err != nil {
		return
	}
	go keepalive(ctx, c)
	for {
		typ, b, err := c.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageBinary {
			continue
		}
		if err := h.c.Write(ctx, websocket.MessageBinary, EncodeFrame(FrameData, sid, b)); err != nil {
			return
		}
	}
}

// keepalive pings so idle links survive proxies and dead peers are noticed.
func keepalive(ctx context.Context, c *websocket.Conn) {
	t := time.NewTicker(25 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := c.Ping(pctx)
			cancel()
			if err != nil {
				c.Close(websocket.StatusGoingAway, "ping timeout")
				return
			}
		}
	}
}

func writeJSON(ctx context.Context, c *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.Write(ctx, websocket.MessageText, b)
}

func readJSON(ctx context.Context, c *websocket.Conn, v any) error {
	typ, b, err := c.Read(ctx)
	if err != nil {
		return err
	}
	if typ != websocket.MessageText {
		return errors.New("expected a text message")
	}
	return json.Unmarshal(b, v)
}
