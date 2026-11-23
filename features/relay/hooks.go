package relay

// Public webhook intake through the relay, for a host behind NAT.
//
// A webhook sender cannot speak the devices' end-to-end encrypted
// protocol, so unlike everything else the relay carries, a webhook's
// headers and body pass through the relay in the clear (in transit over
// TLS). That is why it is opt-in per endpoint (webhooks settings), the
// relay can switch it off (crew-relay --no-hooks), and the host still
// verifies every signature itself: the relay only forwards bytes.
//
//	sender  → relay   POST /v1/hooks/<token>      (token: 48 hex, per endpoint)
//	relay   → host    FrameHook   rid ‖ {"t": token, "h": headers, "b": body}
//	host    → relay   FrameHookOK rid ‖ {"s": status}
//	relay   → sender  that status (504 when the host does not answer in time)
//
// The host tells the relay which tokens it serves in a text message
// {"hooks": [...]} after it signed in, and again whenever they change.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/kernel"
)

// Hook frames (relay ↔ host); the stream id is the request id.
const (
	FrameHook   byte = 4
	FrameHookOK byte = 5
)

// Limits of the hook intake.
const (
	MaxHookBody     = 1 << 20
	maxHookTokens   = 200 // per host
	hookTimeout     = 15 * time.Second
	hookRatePerMin  = 120 // per token
	hookTokenMinLen = 32
)

// hookFrame is a forwarded request (relay → host).
type hookFrame struct {
	Token  string            `json:"t"`
	Header map[string]string `json:"h"`
	Body   []byte            `json:"b"`
}

type hookReply struct {
	Status int `json:"s"`
}

// hookTokensMsg announces the host's tokens (host → relay, text).
type hookTokensMsg struct {
	Hooks []string `json:"hooks"`
}

// hookState is the relay's side: tokens → host, waiting replies, rates.
type hookState struct {
	mu      sync.Mutex
	owner   map[string]*hostConn // token → host
	waiting map[uint32]chan int
	next    uint32
	rate    map[string][]time.Time
}

func (s *Server) hooks() *hookState {
	s.hookOnce.Do(func() {
		s.hk = &hookState{owner: map[string]*hostConn{}, waiting: map[uint32]chan int{}, rate: map[string][]time.Time{}}
	})
	return s.hk
}

// setHostHooks replaces the tokens h serves.
func (s *Server) setHostHooks(h *hostConn, tokens []string) {
	hs := s.hooks()
	hs.mu.Lock()
	defer hs.mu.Unlock()
	for t, o := range hs.owner {
		if o == h {
			delete(hs.owner, t)
		}
	}
	for i, t := range tokens {
		if i >= maxHookTokens {
			break
		}
		if len(t) < hookTokenMinLen || strings.ContainsAny(t, "/?# ") {
			continue
		}
		hs.owner[t] = h
	}
}

func (s *Server) dropHostHooks(h *hostConn) { s.setHostHooks(h, nil) }

// hookReply is a host's answer to a forwarded request.
func (s *Server) hookAnswered(rid uint32, payload []byte) {
	var r hookReply
	_ = json.Unmarshal(payload, &r)
	hs := s.hooks()
	hs.mu.Lock()
	ch := hs.waiting[rid]
	hs.mu.Unlock()
	if ch != nil {
		select {
		case ch <- r.Status:
		default:
		}
	}
}

// forwardedHeaders are the request headers a webhook verifier may need.
func forwardedHeaders(h http.Header) map[string]string {
	out := map[string]string{}
	for k, v := range h {
		lk := strings.ToLower(k)
		switch {
		case lk == "cookie", lk == "x-forwarded-for", lk == "x-real-ip", lk == "connection", strings.HasPrefix(lk, "sec-"):
			continue
		}
		if len(out) >= 64 || len(v) == 0 {
			continue
		}
		out[k] = v[0]
	}
	return out
}

// serveHook is POST /v1/hooks/{token}.
func (s *Server) serveHook(w http.ResponseWriter, r *http.Request) {
	if s.cfg.NoHooks {
		http.Error(w, "webhook intake is off on this relay", http.StatusNotFound)
		return
	}
	token := r.PathValue("token")
	hs := s.hooks()
	now := time.Now()
	hs.mu.Lock()
	h := hs.owner[token]
	// a per-token rate limit, so one noisy sender cannot flood a host
	recent := hs.rate[token][:0:0]
	for _, t := range hs.rate[token] {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	limited := len(recent) >= hookRatePerMin
	if !limited && h != nil {
		hs.rate[token] = append(recent, now)
	}
	hs.next++
	rid := hs.next
	ch := make(chan int, 1)
	if h != nil && !limited {
		hs.waiting[rid] = ch
	}
	hs.mu.Unlock()
	if h == nil {
		http.Error(w, "unknown webhook (or its computer is offline)", http.StatusNotFound)
		return
	}
	if limited {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	defer func() {
		hs.mu.Lock()
		delete(hs.waiting, rid)
		hs.mu.Unlock()
	}()
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxHookBody+1))
	if err != nil {
		http.Error(w, "could not read the body", http.StatusBadRequest)
		return
	}
	if len(body) > MaxHookBody {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	payload, _ := json.Marshal(hookFrame{Token: token, Header: forwardedHeaders(r.Header), Body: body})
	ctx, cancel := context.WithTimeout(r.Context(), hookTimeout)
	defer cancel()
	if err := h.write(ctx, EncodeFrame(FrameHook, rid, payload)); err != nil {
		http.Error(w, "the computer is not reachable", http.StatusServiceUnavailable)
		return
	}
	select {
	case st := <-ch:
		if st < 100 || st > 599 {
			st = http.StatusBadGateway
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(st)
		_, _ = w.Write([]byte(`{"status":` + itoa(st) + `}`))
	case <-ctx.Done():
		http.Error(w, "the computer did not answer in time", http.StatusGatewayTimeout)
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// ---------------------------------------------------------------- host side

// HookHandler serves one forwarded webhook on the host: the endpoint's
// token, the sender's headers and body; it returns the status the sender
// gets.
type HookHandler func(ctx context.Context, token string, h http.Header, body []byte) int

// SetHooks gives the host its webhook intake: the tokens it serves (asked
// again on every connect and after Kick) and the handler.
func (h *Host) SetHooks(tokens func() []string, handle HookHandler) {
	h.mu.Lock()
	h.hookTokens, h.hookHandle = tokens, handle
	h.mu.Unlock()
	h.KickHooks()
}

// KickHooks tells the relay the tokens again (they changed).
func (h *Host) KickHooks() {
	h.mu.Lock()
	ch := h.hookKick
	h.mu.Unlock()
	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (h *Host) hookTokensNow() []string {
	h.mu.Lock()
	f := h.hookTokens
	h.mu.Unlock()
	if f == nil {
		return []string{}
	}
	ts := f()
	if ts == nil {
		ts = []string{}
	}
	return ts
}

// serveHookFrame answers one forwarded webhook.
func (h *Host) serveHookFrame(ctx context.Context, rid uint32, payload []byte, write func(byte, uint32, []byte) error) {
	h.mu.Lock()
	handle := h.hookHandle
	h.mu.Unlock()
	st := http.StatusNotFound
	var f hookFrame
	if handle != nil && json.Unmarshal(payload, &f) == nil {
		hdr := http.Header{}
		for k, v := range f.Header {
			hdr.Set(k, v)
		}
		hctx, cancel := context.WithTimeout(ctx, hookTimeout-time.Second)
		st = handle(hctx, f.Token, hdr, f.Body)
		cancel()
	}
	b, _ := json.Marshal(hookReply{Status: st})
	_ = write(FrameHookOK, rid, b)
}

// ---------------------------------------------------------------- kernel

// HookCall is one webhook that came through the relay.
type HookCall struct {
	Token  string
	Header http.Header
	Body   []byte
}

// Events the webhooks feature answers (the relay knows nothing of it).
var (
	// HookTokens: the tokens of endpoints reachable through the relay.
	HookTokens = kernel.NewBail[struct{}, []string]("relay/hook-tokens")
	// HookIntake: serve one forwarded webhook; the answer is its status.
	HookIntake = kernel.NewBail[HookCall, int]("relay/hook")
	// HooksChanged: the tokens changed (tell the relay again).
	HooksChanged = kernel.NewEmit[struct{}]("relay/hooks-changed")
)
