package chrome_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/browser"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/browser/chrome"
)

func needChrome(t *testing.T) {
	t.Helper()
	if chrome.FindExecutable() == "" {
		t.Skip("no Chrome/Chromium executable found")
	}
}

func server(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/set", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: r.URL.Query().Get("v"), Path: "/", MaxAge: 3600})
		fmt.Fprint(w, "<title>set</title>ok")
	})
	mux.HandleFunc("/who", func(w http.ResponseWriter, r *http.Request) {
		v := "anonymous"
		if c, err := r.Cookie("sid"); err == nil {
			v = c.Value
		}
		fmt.Fprintf(w, "<title>who</title><p id=who>%s</p>", v)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<title>home</title><input id=q>`)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func newChrome(t *testing.T, cfg chrome.Config) *chrome.Chrome {
	t.Helper()
	if cfg.ProfileRoot == "" {
		cfg.ProfileRoot = t.TempDir()
	}
	ch, err := chrome.New(cfg, chrome.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ch.Close)
	return ch
}

func who(t *testing.T, ch *chrome.Chrome, agent, base string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := ch.Session(ctx, agent)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Navigate(ctx, base+"/who"); err != nil {
		t.Fatal(err)
	}
	var v string
	if err := s.Eval(ctx, `document.getElementById('who').textContent`, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func setCookie(t *testing.T, ch *chrome.Chrome, agent, base, v string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := ch.Session(ctx, agent)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Navigate(ctx, base+"/set?v="+v); err != nil {
		t.Fatal(err)
	}
}

func pgrep(dir string) string {
	out, _ := exec.Command("pgrep", "-f", dir).Output()
	return strings.TrimSpace(string(out))
}

func TestNoExecutableFailsClearly(t *testing.T) {
	_, err := chrome.New(chrome.Config{Executable: "/nonexistent/chrome"}, chrome.Options{})
	if err == nil || !strings.Contains(err.Error(), "executable") {
		t.Fatalf("err = %v", err)
	}
	if _, err := chrome.New(chrome.Config{Attach: "ftp://x"}, chrome.Options{}); err == nil {
		t.Fatal("bad attach URL must fail")
	}
}

func TestProfilePersistsAcrossIdleRestart(t *testing.T) {
	needChrome(t)
	srv := server(t)
	ch := newChrome(t, chrome.Config{IdleTimeout: "200ms"})
	setCookie(t, ch, "otto", srv.URL, "otto-login")
	if got := who(t, ch, "otto", srv.URL); got != "otto-login" {
		t.Fatalf("who = %q", got)
	}
	dir, _ := ch.ProfileDir("otto")
	time.Sleep(300 * time.Millisecond)
	ch.Reap()
	if st := ch.State("otto"); st.Running {
		t.Fatal("idle browser must be reclaimed")
	}
	if p := pgrep(dir); p != "" {
		t.Fatalf("leftover processes after idle close: %s", p)
	}
	// Restarted on demand with the same profile: still logged in.
	if got := who(t, ch, "otto", srv.URL); got != "otto-login" {
		t.Fatalf("after restart who = %q", got)
	}
	// Separate agent → separate profile.
	if got := who(t, ch, "kai", srv.URL); got != "anonymous" {
		t.Fatalf("kai must not share otto's cookies, got %q", got)
	}
	kdir, _ := ch.ProfileDir("kai")
	if kdir == dir {
		t.Fatal("profiles must differ")
	}
}

func TestTakeoverGatesSession(t *testing.T) {
	needChrome(t)
	srv := server(t)
	ch := newChrome(t, chrome.Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := ch.Session(ctx, "otto")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Navigate(ctx, srv.URL+"/"); err != nil {
		t.Fatal(err)
	}
	if err := ch.Input("otto", "me", browser.Input{Type: "click"}); !errors.Is(err, browser.ErrNotTakenOver) {
		t.Fatalf("input without takeover: %v", err)
	}
	if err := ch.Takeover("otto", "me"); err != nil {
		t.Fatal(err)
	}
	if err := ch.Takeover("otto", "someone-else"); !errors.Is(err, browser.ErrNotController) {
		t.Fatalf("second taker: %v", err)
	}
	st := ch.State("otto")
	if st.TakenOverBy != "me" || !st.Running || st.View != "frames" || !strings.HasSuffix(st.URL, "/") {
		t.Fatalf("state %+v", st)
	}
	if _, err := ch.Session(ctx, "otto"); !errors.Is(err, browser.ErrTakenOver) {
		t.Fatalf("Session during takeover: %v", err)
	}
	var v any
	if err := s.Eval(ctx, `1`, &v); !errors.Is(err, browser.ErrTakenOver) {
		t.Fatalf("existing session during takeover: %v", err)
	}
	png, err := ch.Screenshot("otto")
	if err != nil || !bytes.HasPrefix(png, []byte("\x89PNG")) {
		t.Fatalf("screenshot: %v", err)
	}
	// Human types into the field.
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(ch.Input("otto", "me", browser.Input{Type: "navigate", URL: srv.URL + "/"}))
	if err := ch.Input("otto", "other", browser.Input{Type: "key", Text: "x"}); !errors.Is(err, browser.ErrNotController) {
		t.Fatalf("other caller input: %v", err)
	}
	if err := ch.Input("otto", "me", browser.Input{Type: "navigate", URL: "file:///etc/passwd"}); err == nil {
		t.Fatal("file URLs must be refused")
	}
	done := make(chan error, 1)
	go func() { _, err := ch.AwaitHandback(ctx, "otto"); done <- err }()
	time.Sleep(50 * time.Millisecond) // let the waiter register
	must(ch.Handback("otto"))
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := s.Eval(ctx, `1`, &v); err != nil {
		t.Fatalf("after handback: %v", err)
	}
}

func TestUnloadKillsChrome(t *testing.T) {
	needChrome(t)
	srv := server(t)
	root := t.TempDir()
	h := kernel.NewHost(nil)
	defer h.Close()
	if _, err := h.Load(chrome.Plugin, kernel.LoadOptions{Config: map[string]any{"profileRoot": root}}); err != nil {
		t.Fatal(err)
	}
	b, ok := kernel.Lookup[browser.Browser](h.Root(), browser.ServiceKey)
	if !ok {
		t.Fatalf("browser service missing: %+v", h.Plugins())
	}
	var events []string
	browser.Changed.Listen(h.Root(), func(s browser.State) { events = append(events, fmt.Sprint(s.Running)) })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := b.Session(ctx, "otto")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Navigate(ctx, srv.URL); err != nil {
		t.Fatal(err)
	}
	dir := root + "/otto"
	if pgrep(dir) == "" {
		t.Fatal("chrome should be running with the agent profile")
	}
	h.Unload(chrome.Plugin.Name)
	deadline := time.Now().Add(5 * time.Second)
	for pgrep(dir) != "" && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if p := pgrep(dir); p != "" {
		t.Fatalf("leftover chrome processes after unload: %s", p)
	}
	if len(events) < 2 || events[0] != "true" || events[len(events)-1] != "false" {
		t.Fatalf("changed events %v", events)
	}
}
