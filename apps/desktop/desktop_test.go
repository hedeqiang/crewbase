package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/capabilities/notify"
)

func newTestDaemon(t *testing.T, h http.Handler) *daemon {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &daemon{
		addr:    strings.TrimPrefix(srv.URL, "http://"),
		token:   "tok",
		changed: make(chan struct{}, 1),
		notes:   make(chan notify.Notification, 8),
	}
}

func authed(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Authorization") != "Bearer tok" {
		http.Error(w, "no", http.StatusUnauthorized)
		return false
	}
	return true
}

// Attach mode: the "needs you" list comes from /api/agents and /api/approvals.
func TestNeedsAttached(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/agents", func(w http.ResponseWriter, r *http.Request) {
		if authed(w, r) {
			fmt.Fprint(w, `{"agents":[{"id":"a1","name":"Kai","status":"waiting","detail":"approval:c1","statusSession":"s1"},
				{"id":"a2","name":"Mia","status":"waiting","detail":"takeover:登录 GitHub","statusSession":"s2"}]}`)
		}
	})
	mux.HandleFunc("/api/approvals", func(w http.ResponseWriter, r *http.Request) {
		if authed(w, r) {
			fmt.Fprint(w, `{"pending":[{"callId":"c1","session":"s1","agent":"a1","summary":"git push origin main","asked":"2026-01-01T00:00:00Z"}]}`)
		}
	})
	d := newTestDaemon(t, mux)
	ns, err := d.needs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ns) != 2 {
		t.Fatalf("needs = %+v", ns)
	}
	got := map[string]string{}
	for _, n := range ns {
		got[n.Session] = n.Label
	}
	if got["s1"] != "Kai 等你批准：git push origin main" || got["s2"] != "Mia 等你接管浏览器：登录 GitHub" {
		t.Fatalf("labels = %v", got)
	}
}

// Attach mode: SSE "notify" events become native notifications, and
// approval/status events signal a refresh.
func TestEventStream(t *testing.T) {
	d := newTestDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authed(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: hello\ndata: {}\n\n")
		fmt.Fprint(w, "event: notify\ndata: {\"id\":\"n1\",\"kind\":\"needs-you\",\"title\":\"Kai 等你批准\",\"body\":\"git push\",\"session\":\"s1\"}\n\n")
		fmt.Fprint(w, ": ping\n\n")
		fmt.Fprint(w, "event: approvals.changed\ndata: {}\n\n")
	}))
	if err := d.streamOnce(); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("streamOnce = %v", err)
	}
	select {
	case n := <-d.notes:
		if n.Title != "Kai 等你批准" || n.Session != "s1" {
			t.Fatalf("notification = %+v", n)
		}
	default:
		t.Fatal("no notification")
	}
	select {
	case <-d.changed:
	default:
		t.Fatal("no change signal")
	}
}

func TestClip(t *testing.T) {
	if got := clip("  a   b\nc ", 10); got != "a b c" {
		t.Fatalf("clip = %q", got)
	}
	if got := clip(strings.Repeat("长", 50), 5); got != "长长长长…" {
		t.Fatalf("clip = %q", got)
	}
}

func TestPreloadScript(t *testing.T) {
	js := preloadScript("http://127.0.0.1:7788", "0.1.0")
	for _, want := range []string{`location.origin !== "http://127.0.0.1:7788"`, `version: "0.1.0"`, "quit:", "openExternal:", "setBadge:", "pickFolder:", "checkForUpdates:", "keepRunning:", "setKeepRunning:", "permissions:", "requestPermission:", "openPermissionSettings:", `"Desktop." + m`} {
		if !strings.Contains(js, want) {
			t.Errorf("preload lacks %q", want)
		}
	}
}
