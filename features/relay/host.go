package relay

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Host keeps an outbound link to a relay and serves devices through it.
type Host struct {
	id      *Identity
	relay   string // https://relay.example.org (ws(s) derived)
	handler http.Handler
	log     *slog.Logger

	mu      sync.Mutex
	online  bool
	lastErr string
	devices int

	hookTokens func() []string
	hookHandle HookHandler
	hookKick   chan struct{}
}

// Status of the link.
type Status struct {
	Relay   string `json:"relay"`
	HostID  string `json:"hostId"`
	Online  bool   `json:"online"`
	Devices int    `json:"devices"` // devices connected right now
	Error   string `json:"error,omitempty"`
}

// NewHost makes a host link; handler serves the tunnelled requests (the
// gateway), and must only see paths under /api/.
func NewHost(id *Identity, relayURL string, handler http.Handler, log *slog.Logger) (*Host, error) {
	u, err := url.Parse(strings.TrimRight(relayURL, "/"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("relay must be an http(s) URL, got %q", relayURL)
	}
	if u.Scheme == "http" && !isLocal(u.Hostname()) {
		return nil, errors.New("relay must use https (http only for localhost)")
	}
	if log == nil {
		log = slog.Default()
	}
	return &Host{id: id, relay: u.String(), handler: handler, log: log}, nil
}

func isLocal(h string) bool { return h == "localhost" || h == "127.0.0.1" || h == "::1" }

// Status reports the link.
func (h *Host) Status() Status {
	h.mu.Lock()
	defer h.mu.Unlock()
	return Status{Relay: h.relay, HostID: h.id.HostID(), Online: h.online, Devices: h.devices, Error: h.lastErr}
}

// PairURL is the link a device opens to pair through the relay: the
// relay's web app, with the host id, its public key and the code in the
// fragment (fragments are never sent to the relay).
func (h *Host) PairURL(code string) string {
	return fmt.Sprintf("%s/#/pair?host=%s&key=%s&code=%s", h.relay, h.id.HostID(), h.id.PublicB64(), url.QueryEscape(code))
}

func (h *Host) wsURL(path string) string {
	return "ws" + strings.TrimPrefix(h.relay, "http") + path
}

func (h *Host) set(online bool, err error) {
	h.mu.Lock()
	h.online = online
	if err != nil {
		h.lastErr = err.Error()
	} else if online {
		h.lastErr = ""
	}
	h.mu.Unlock()
}

// Run keeps the link up until ctx ends, reconnecting with backoff.
func (h *Host) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := h.session(ctx)
		h.set(false, err)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		h.log.Warn("relay link down", "relay", h.relay, "err", err, "retry", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
}

func (h *Host) session(ctx context.Context) error {
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	c, _, err := websocket.Dial(dctx, h.wsURL("/v1/host?pub="+h.id.PublicB64()), nil)
	cancel()
	if err != nil {
		return err
	}
	defer c.CloseNow()
	c.SetReadLimit(MaxFrame + 64)
	var ch authMsg
	if err := readJSON(ctx, c, &ch); err != nil {
		return err
	}
	sig, err := h.id.Sign(ch.Challenge)
	if err != nil {
		return err
	}
	if err := writeJSON(ctx, c, authMsg{Sig: sig}); err != nil {
		return err
	}
	var ok authMsg
	if err := readJSON(ctx, c, &ok); err != nil {
		return err
	}
	if !ok.OK {
		return fmt.Errorf("relay refused: %s", ok.Error)
	}
	h.set(true, nil)
	h.log.Info("relay link up", "relay", h.relay, "host", ok.ID)

	sctx, scancel := context.WithCancel(ctx)
	defer scancel()
	go keepalive(sctx, c)
	// the webhook tokens this host serves: now, when they change, and
	// once a minute in case a change was missed
	kick := make(chan struct{}, 1)
	h.mu.Lock()
	h.hookKick = kick
	h.mu.Unlock()
	go func() {
		last := ""
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			ts := h.hookTokensNow()
			if key := strings.Join(ts, ","); key != last {
				if writeJSON(sctx, c, hookTokensMsg{Hooks: ts}) == nil {
					last = key
				}
			}
			select {
			case <-sctx.Done():
				return
			case <-kick:
			case <-t.C:
			}
		}
	}()
	var wmu sync.Mutex
	write := func(typ byte, sid uint32, p []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		wctx, wc := context.WithTimeout(sctx, 60*time.Second)
		defer wc()
		return c.Write(wctx, websocket.MessageBinary, EncodeFrame(typ, sid, p))
	}
	streams := map[uint32]*stream{}
	defer func() {
		for _, st := range streams {
			st.stop()
		}
		h.mu.Lock()
		h.devices = 0
		h.mu.Unlock()
	}()
	for {
		typ, b, err := c.Read(sctx)
		if err != nil {
			return err
		}
		if typ != websocket.MessageBinary {
			continue
		}
		ft, sid, payload, err := DecodeFrame(b)
		if err != nil {
			continue
		}
		switch ft {
		case FrameOpen:
			if old := streams[sid]; old != nil {
				old.stop()
			}
			streams[sid] = newStream(sctx, h, sid, write)
			h.count(len(streams))
		case FrameData:
			if st := streams[sid]; st != nil {
				st.in(payload)
			}
		case FrameHook:
			go h.serveHookFrame(sctx, sid, payload, write)
		case FrameClose:
			if st := streams[sid]; st != nil {
				st.stop()
				delete(streams, sid)
				h.count(len(streams))
			}
		}
	}
}

func (h *Host) count(n int) {
	h.mu.Lock()
	h.devices = n
	h.mu.Unlock()
}

// stream is one device connection: handshake, then tunnelled requests.
type stream struct {
	h      *Host
	sid    uint32
	write  func(byte, uint32, []byte) error
	ctx    context.Context
	cancel context.CancelFunc

	mu   sync.Mutex // guards ch (Seal / Open counters) and reqs
	ch   *Channel
	reqs map[int64]context.CancelFunc
	sem  chan struct{}
}

const maxInflight = 16

func newStream(ctx context.Context, h *Host, sid uint32, write func(byte, uint32, []byte) error) *stream {
	c, cancel := context.WithCancel(ctx)
	return &stream{h: h, sid: sid, write: write, ctx: c, cancel: cancel, reqs: map[int64]context.CancelFunc{}, sem: make(chan struct{}, maxInflight)}
}

func (s *stream) stop() { s.cancel() }

func (s *stream) fail() {
	s.cancel()
	_ = s.write(FrameClose, s.sid, nil)
}

func (s *stream) in(frame []byte) {
	s.mu.Lock()
	if s.ch == nil {
		reply, ch, err := s.h.id.Accept(frame)
		if err != nil {
			s.mu.Unlock()
			s.fail()
			return
		}
		s.ch = ch
		s.mu.Unlock()
		_ = s.write(FrameData, s.sid, reply)
		return
	}
	plain, err := s.ch.Open(frame)
	s.mu.Unlock()
	if err != nil {
		s.fail() // tampered or out of order: drop the connection
		return
	}
	var r tReq
	if json.Unmarshal(plain, &r) != nil {
		s.fail()
		return
	}
	if r.Cancel {
		s.mu.Lock()
		if c := s.reqs[r.ID]; c != nil {
			c()
		}
		s.mu.Unlock()
		return
	}
	select {
	case s.sem <- struct{}{}:
	default:
		s.send(tResp{ID: r.ID, S: 429, B: []byte(`{"error":{"code":"busy","message":"too many requests in flight"}}`), End: true})
		return
	}
	rctx, rc := context.WithCancel(s.ctx)
	s.mu.Lock()
	s.reqs[r.ID] = rc
	s.mu.Unlock()
	go func() {
		defer func() {
			<-s.sem
			rc()
			s.mu.Lock()
			delete(s.reqs, r.ID)
			s.mu.Unlock()
		}()
		s.serve(rctx, r)
	}()
}

func (s *stream) send(r tResp) {
	b, _ := json.Marshal(r)
	s.mu.Lock()
	if s.ch == nil {
		s.mu.Unlock()
		return
	}
	frame := s.ch.Seal(b)
	// write under the lock so frames leave in counter order
	err := s.write(FrameData, s.sid, frame)
	s.mu.Unlock()
	if err != nil {
		s.cancel()
	}
}

// allowed headers a device may set on tunnelled requests.
var reqHeaders = map[string]bool{"Authorization": true, "Content-Type": true, "Accept": true}

func (s *stream) serve(ctx context.Context, r tReq) {
	if !strings.HasPrefix(r.P, "/api/") || strings.Contains(r.P, "..") {
		s.send(tResp{ID: r.ID, S: 404, B: []byte(`{"error":{"code":"not_found","message":"not found"}}`), End: true})
		return
	}
	switch r.M {
	case "GET", "POST", "PATCH", "PUT", "DELETE":
	default:
		s.send(tResp{ID: r.ID, S: 405, End: true})
		return
	}
	req, err := http.NewRequestWithContext(ctx, r.M, "http://relay.local"+r.P, bytes.NewReader(r.B))
	if err != nil {
		s.send(tResp{ID: r.ID, S: 400, End: true})
		return
	}
	for k, v := range r.H {
		if ck := http.CanonicalHeaderKey(k); reqHeaders[ck] {
			req.Header.Set(ck, v)
		}
	}
	req.Header.Set("X-Crewbase-Via", "relay")
	req.RemoteAddr = "relay:" + s.h.id.HostID()
	w := &tunnelWriter{s: s, id: r.ID, h: http.Header{}}
	s.h.handler.ServeHTTP(w, req)
	w.finish()
}

// tunnelWriter streams an http response over the channel. Writes are
// buffered and sent on Flush (SSE) or at the end, in chunks.
type tunnelWriter struct {
	s       *stream
	id      int64
	h       http.Header
	status  int
	headed  bool
	buf     bytes.Buffer
	flushed bool
}

func (w *tunnelWriter) Header() http.Header { return w.h }

func (w *tunnelWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}

func (w *tunnelWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	if w.s.ctx.Err() != nil {
		return 0, w.s.ctx.Err()
	}
	w.buf.Write(b)
	if w.buf.Len() >= 1<<20 {
		w.Flush()
	}
	return len(b), nil
}

const chunk = 512 << 10

func (w *tunnelWriter) Flush() {
	if w.status == 0 {
		w.status = 200
	}
	r := tResp{ID: w.id}
	if !w.headed {
		w.headed = true
		r.S = w.status
		r.H = map[string]string{}
		for _, k := range []string{"Content-Type", "Content-Disposition", "Cache-Control"} {
			if v := w.h.Get(k); v != "" {
				r.H[k] = v
			}
		}
	}
	data := w.buf.Bytes()
	for {
		n := min(len(data), chunk)
		r.B = data[:n]
		w.s.send(r)
		data = data[n:]
		r = tResp{ID: w.id}
		if len(data) == 0 {
			break
		}
	}
	w.buf.Reset()
	w.flushed = true
}

func (w *tunnelWriter) finish() {
	if !w.headed || w.buf.Len() > 0 {
		w.Flush()
	}
	w.s.send(tResp{ID: w.id, End: true})
}

// keyFromB64 parses a base64url public key.
func keyFromB64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
