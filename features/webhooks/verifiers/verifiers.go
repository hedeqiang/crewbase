// Package verifiers checks that an inbound webhook really comes from the
// service it claims to, before anything else looks at it. Each verifier
// follows its provider's documented scheme, compares in constant time and,
// where the scheme signs a timestamp, refuses requests outside the window
// (replays). It also says which delivery it was (for de-duplication) and
// what kind of event (for filters).
package verifiers

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Providers.
const (
	GitHub = "github"
	GitLab = "gitlab"
	Sentry = "sentry"
	Linear = "linear"
	Stripe = "stripe"
	// HMAC is Crewbase's own scheme (the one outbound deliveries use):
	// X-Crewbase-Signature-256: t=<unix>,v1=<hex hmac-sha256(secret, t + "." + body)>
	HMAC   = "hmac-sha256"
	Bearer = "bearer" // Authorization: Bearer <secret>
)

// Names lists the providers in a stable order.
func Names() []string { return []string{GitHub, GitLab, Sentry, Linear, Stripe, HMAC, Bearer} }

// Tolerance is how old (or how far in the future) a signed timestamp may be.
const Tolerance = 5 * time.Minute

// Errors.
var (
	ErrMissing  = errors.New("signature missing")
	ErrMismatch = errors.New("signature does not match")
	ErrExpired  = errors.New("timestamp outside the allowed window (a replay?)")
	ErrProvider = errors.New("unknown provider")
	ErrNoSecret = errors.New("no secret configured")
)

// Result is what a verified request is.
type Result struct {
	Delivery string // the provider's delivery id ("" = none)
	Event    string // the event type (github: pull_request, stripe: invoice.paid…)
}

// Verify checks req against secret for provider.
func Verify(provider, secret string, h http.Header, body []byte, now time.Time) (Result, error) {
	if secret == "" {
		return Result{}, ErrNoSecret
	}
	switch provider {
	case GitHub:
		sig := h.Get("X-Hub-Signature-256")
		if sig == "" {
			return Result{}, ErrMissing
		}
		if !equalHex(strings.TrimPrefix(sig, "sha256="), mac(secret, body)) {
			return Result{}, ErrMismatch
		}
		return Result{Delivery: h.Get("X-GitHub-Delivery"), Event: h.Get("X-GitHub-Event")}, nil
	case GitLab:
		tok := h.Get("X-Gitlab-Token")
		if tok == "" {
			return Result{}, ErrMissing
		}
		if subtle.ConstantTimeCompare([]byte(tok), []byte(secret)) != 1 {
			return Result{}, ErrMismatch
		}
		d := h.Get("X-Gitlab-Event-UUID")
		if d == "" {
			d = h.Get("Idempotency-Key")
		}
		ev := h.Get("X-Gitlab-Event") // "Merge Request Hook"
		if kind := jsonString(body, "object_kind"); kind != "" {
			ev = kind // merge_request, pipeline…
		}
		return Result{Delivery: d, Event: ev}, nil
	case Sentry:
		sig := h.Get("Sentry-Hook-Signature")
		if sig == "" {
			return Result{}, ErrMissing
		}
		if !equalHex(sig, mac(secret, body)) {
			return Result{}, ErrMismatch
		}
		if ts := h.Get("Sentry-Hook-Timestamp"); ts != "" {
			if err := window(ts, now, time.Second); err != nil {
				return Result{}, err
			}
		}
		ev := h.Get("Sentry-Hook-Resource")
		if a := jsonString(body, "action"); a != "" && ev != "" {
			ev += "." + a // issue.created, event_alert.triggered…
		}
		return Result{Delivery: h.Get("Request-ID"), Event: ev}, nil
	case Linear:
		sig := h.Get("Linear-Signature")
		if sig == "" {
			return Result{}, ErrMissing
		}
		if !equalHex(sig, mac(secret, body)) {
			return Result{}, ErrMismatch
		}
		if ms := jsonNumber(body, "webhookTimestamp"); ms > 0 {
			if d := now.Sub(time.UnixMilli(ms)); d > time.Minute || d < -time.Minute {
				return Result{}, ErrExpired
			}
		}
		ev := jsonString(body, "type")
		if a := jsonString(body, "action"); a != "" && ev != "" {
			ev += "." + a // Issue.create
		}
		return Result{Delivery: h.Get("Linear-Delivery"), Event: ev}, nil
	case Stripe:
		t, sigs := parseSigHeader(h.Get("Stripe-Signature"))
		if t == "" || len(sigs) == 0 {
			return Result{}, ErrMissing
		}
		want := mac(secret, append([]byte(t+"."), body...))
		ok := false
		for _, s := range sigs {
			ok = ok || equalHex(s, want)
		}
		if !ok {
			return Result{}, ErrMismatch
		}
		if err := window(t, now, time.Second); err != nil {
			return Result{}, err
		}
		return Result{Delivery: jsonString(body, "id"), Event: jsonString(body, "type")}, nil
	case HMAC:
		t, sigs := parseSigHeader(h.Get("X-Crewbase-Signature-256"))
		if t == "" || len(sigs) == 0 {
			return Result{}, ErrMissing
		}
		want := mac(secret, append([]byte(t+"."), body...))
		ok := false
		for _, s := range sigs {
			ok = ok || equalHex(s, want)
		}
		if !ok {
			return Result{}, ErrMismatch
		}
		if err := window(t, now, time.Second); err != nil {
			return Result{}, err
		}
		d := h.Get("X-Crewbase-Delivery")
		if d == "" {
			d = h.Get("Idempotency-Key")
		}
		return Result{Delivery: d, Event: h.Get("X-Crewbase-Event")}, nil
	case Bearer:
		a := h.Get("Authorization")
		tok, ok := strings.CutPrefix(a, "Bearer ")
		if !ok || tok == "" {
			return Result{}, ErrMissing
		}
		if subtle.ConstantTimeCompare([]byte(tok), []byte(secret)) != 1 {
			return Result{}, ErrMismatch
		}
		return Result{Delivery: h.Get("Idempotency-Key"), Event: h.Get("X-Event")}, nil
	}
	return Result{}, fmt.Errorf("%w %q", ErrProvider, provider)
}

// Sign makes Crewbase's signature header value for body at t.
func Sign(secret string, t time.Time, body []byte) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	return "t=" + ts + ",v1=" + mac(secret, append([]byte(ts+"."), body...))
}

func mac(secret string, b []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(b)
	return hex.EncodeToString(m.Sum(nil))
}

func equalHex(got, want string) bool {
	g, err1 := hex.DecodeString(strings.TrimSpace(strings.ToLower(got)))
	w, err2 := hex.DecodeString(want)
	return err1 == nil && err2 == nil && hmac.Equal(g, w)
}

// parseSigHeader reads "t=123,v1=abc,v1=def" (Stripe and Crewbase).
func parseSigHeader(v string) (string, []string) {
	var t string
	var sigs []string
	for _, part := range strings.Split(v, ",") {
		k, val, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			t = val
		case "v1":
			sigs = append(sigs, val)
		}
	}
	return t, sigs
}

func window(ts string, now time.Time, unit time.Duration) error {
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return ErrExpired
	}
	at := time.Unix(0, n*int64(unit))
	if d := now.Sub(at); d > Tolerance || d < -Tolerance {
		return ErrExpired
	}
	return nil
}

func jsonString(b []byte, key string) string {
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

func jsonNumber(b []byte, key string) int64 {
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return 0
	}
	f, _ := m[key].(float64)
	return int64(f)
}

// Headers a provider's verifier reads (shown in the settings page).
func Headers(provider string) []string {
	out := map[string][]string{
		GitHub: {"X-Hub-Signature-256", "X-GitHub-Delivery", "X-GitHub-Event"},
		GitLab: {"X-Gitlab-Token", "X-Gitlab-Event-UUID", "X-Gitlab-Event"},
		Sentry: {"Sentry-Hook-Signature", "Sentry-Hook-Timestamp", "Sentry-Hook-Resource"},
		Linear: {"Linear-Signature", "Linear-Delivery"},
		Stripe: {"Stripe-Signature"},
		HMAC:   {"X-Crewbase-Signature-256", "X-Crewbase-Delivery", "X-Crewbase-Event"},
		Bearer: {"Authorization"},
	}[provider]
	sort.Strings(out)
	return out
}
