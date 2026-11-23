package verifiers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func h(kv ...string) http.Header {
	x := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		x.Set(kv[i], kv[i+1])
	}
	return x
}

func sig(secret string, b []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(b)
	return hex.EncodeToString(m.Sum(nil))
}

func TestProviders(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	body := []byte(`{"action":"opened","object_kind":"merge_request","type":"invoice.paid","id":"evt_1","webhookTimestamp":1800000000000}`)
	ts := strconv.FormatInt(now.Unix(), 10)
	cases := []struct {
		p    string
		head http.Header
		ev   string
		del  string
	}{
		{GitHub, h("X-Hub-Signature-256", "sha256="+sig("s", body), "X-GitHub-Delivery", "d1", "X-GitHub-Event", "pull_request"), "pull_request", "d1"},
		{GitLab, h("X-Gitlab-Token", "s", "X-Gitlab-Event-UUID", "u1", "X-Gitlab-Event", "Merge Request Hook"), "merge_request", "u1"},
		{Sentry, h("Sentry-Hook-Signature", sig("s", body), "Sentry-Hook-Timestamp", ts, "Sentry-Hook-Resource", "issue", "Request-ID", "r1"), "issue.opened", "r1"},
		{Linear, h("Linear-Signature", sig("s", body), "Linear-Delivery", "l1"), "", "l1"},
		{Stripe, h("Stripe-Signature", "t="+ts+",v1="+sig("s", append([]byte(ts+"."), body...))), "invoice.paid", "evt_1"},
		{HMAC, h("X-Crewbase-Signature-256", Sign("s", now, body), "X-Crewbase-Delivery", "c1", "X-Crewbase-Event", "workflow.finished"), "workflow.finished", "c1"},
		{Bearer, h("Authorization", "Bearer s", "Idempotency-Key", "k1"), "", "k1"},
	}
	for _, c := range cases {
		t.Run(c.p, func(t *testing.T) {
			r, err := Verify(c.p, "s", c.head, body, now)
			if err != nil {
				t.Fatal(err)
			}
			if c.ev != "" && r.Event != c.ev || r.Delivery != c.del {
				t.Fatalf("%+v", r)
			}
			// a wrong secret
			if _, err := Verify(c.p, "other", c.head, body, now); !errors.Is(err, ErrMismatch) {
				t.Fatalf("wrong secret: %v", err)
			}
			// a changed body (the token schemes do not sign the body)
			if c.p != GitLab && c.p != Bearer {
				if _, err := Verify(c.p, "s", c.head, append(body, ' '), now); err == nil {
					t.Fatal("tampered body accepted")
				}
			}
			// no signature at all
			if _, err := Verify(c.p, "s", http.Header{}, body, now); !errors.Is(err, ErrMissing) {
				t.Fatalf("no signature: %v", err)
			}
		})
	}
}

func TestReplayWindow(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	body := []byte(`{}`)
	old := now.Add(-10 * time.Minute)
	if _, err := Verify(HMAC, "s", h("X-Crewbase-Signature-256", Sign("s", old, body)), body, now); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
	ts := fmt.Sprint(old.Unix())
	if _, err := Verify(Stripe, "s", h("Stripe-Signature", "t="+ts+",v1="+sig("s", append([]byte(ts+"."), body...))), body, now); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
	lin := []byte(fmt.Sprintf(`{"webhookTimestamp":%d}`, old.UnixMilli()))
	if _, err := Verify(Linear, "s", h("Linear-Signature", sig("s", lin)), lin, now); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
	if _, err := Verify("nope", "s", nil, body, now); !errors.Is(err, ErrProvider) {
		t.Fatal(err)
	}
	if _, err := Verify(GitHub, "", nil, body, now); !errors.Is(err, ErrNoSecret) {
		t.Fatal(err)
	}
}
