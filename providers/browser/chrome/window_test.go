package chrome_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/browser"
	"github.com/hedeqiang/crewbase/providers/browser/chrome"
)

func windowServer(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		// a session cookie: Chrome drops it when it exits
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: r.URL.Query().Get("v"), Path: "/"})
		fmt.Fprint(w, "<title>login</title><script>localStorage.setItem('k', 'kept')</script>ok")
	})
	mux.HandleFunc("/who", func(w http.ResponseWriter, r *http.Request) {
		v := "anonymous"
		if c, err := r.Cookie("sid"); err == nil {
			v = c.Value
		}
		fmt.Fprintf(w, "<title>who</title><p id=who>%s</p>", v)
	})
	mux.HandleFunc("/long", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<title>long</title><div style="height:5000px">long page</div>`)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func session(t *testing.T, ch *chrome.Chrome, agent string) (*browser.Session, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	s, err := ch.Session(ctx, agent)
	if err != nil {
		t.Fatal(err)
	}
	return s, ctx
}

func waitTabs(t *testing.T, ch *chrome.Chrome, agent string, want []string) []string {
	t.Helper()
	var tabs []string
	for i := 0; i < 50; i++ {
		tabs, _ = chrome.Tabs(ch, agent)
		ok := len(tabs) == len(want)
		for j := 0; ok && j < len(want); j++ {
			ok = strings.HasSuffix(tabs[j], want[j])
		}
		if ok {
			return tabs
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("tabs %v, want %v", tabs, want)
	return nil
}

// A takeover moves to a window and back: the browser is relaunched on the
// same profile each time, with the same tabs, scroll position, session
// cookie and local storage, and the agent stays paused until hand-back.
func TestWindowTakeoverKeepsTabs(t *testing.T) {
	needChrome(t)
	srv := windowServer(t)
	ch := newChrome(t, chrome.Config{})
	chrome.FakeWindow(ch)
	s, ctx := session(t, ch, "otto")
	if err := s.Navigate(ctx, srv.URL+"/login?v=human"); err != nil {
		t.Fatal(err)
	}
	if err := s.Navigate(ctx, srv.URL+"/long"); err != nil {
		t.Fatal(err)
	}
	if err := s.Eval(ctx, `scrollTo(0, 1500)`, nil); err != nil {
		t.Fatal(err)
	}
	if err := chrome.OpenTab(ch, "otto", srv.URL+"/who?tab=2"); err != nil {
		t.Fatal(err)
	}
	waitTabs(t, ch, "otto", []string{"/long@1500", "/who?tab=2@0"})
	_, pid0 := chrome.Headed(ch, "otto")

	if err := ch.SetWindow("otto", "me", true); !errors.Is(err, browser.ErrNotTakenOver) {
		t.Fatalf("window without a takeover: %v", err)
	}
	if err := ch.Takeover("otto", "me"); err != nil {
		t.Fatal(err)
	}
	if st := ch.State("otto"); !st.CanWindow || st.View != "frames" || st.Window {
		t.Fatalf("before: %+v", st)
	}
	if err := ch.SetWindow("otto", "you", true); !errors.Is(err, browser.ErrNotController) {
		t.Fatalf("someone else: %v", err)
	}
	if err := ch.SetWindow("otto", "me", true); err != nil {
		t.Fatal(err)
	}
	headed, pid1 := chrome.Headed(ch, "otto")
	if st := ch.State("otto"); !headed || pid1 == pid0 || !st.Window || st.View != "window" || st.Headless || !st.Running || st.TakenOverBy != "me" {
		t.Fatalf("window: headed=%v pid %d→%d %+v", headed, pid0, pid1, st)
	}
	if _, err := ch.Session(context.Background(), "otto"); !errors.Is(err, browser.ErrTakenOver) {
		t.Fatalf("the agent must stay paused: %v", err)
	}
	waitTabs(t, ch, "otto", []string{"/long@1500", "/who?tab=2@0"})
	if err := ch.SetWindow("otto", "me", true); err != nil { // no-op
		t.Fatal(err)
	}
	if _, pid := chrome.Headed(ch, "otto"); pid != pid1 {
		t.Fatal("a second open must not relaunch")
	}

	if err := ch.Handback("otto"); err != nil {
		t.Fatal(err)
	}
	headed, pid2 := chrome.Headed(ch, "otto")
	if st := ch.State("otto"); headed || pid2 == pid1 || st.Window || st.View != "frames" || st.TakenOverBy != "" {
		t.Fatalf("after hand-back: headed=%v %+v", headed, st)
	}
	tabs := waitTabs(t, ch, "otto", []string{"/long@1500", "/who?tab=2@0"})
	s, ctx = session(t, ch, "otto")
	var y float64
	if err := s.Eval(ctx, `scrollY`, &y); err != nil || y != 1500 {
		t.Fatalf("the agent's tab: y=%v %v (tabs %v)", y, err, tabs)
	}
	var ls string
	if err := s.Navigate(ctx, srv.URL+"/who"); err != nil {
		t.Fatal(err)
	}
	if err := s.Eval(ctx, `document.getElementById('who').textContent + '/' + localStorage.getItem('k')`, &ls); err != nil || ls != "human/kept" {
		t.Fatalf("login state after two relaunches: %q %v", ls, err)
	}
}

// The person closes the window (or the browser crashes) during a window
// takeover: the state says so, and hand-back still brings the tabs back
// in a headless browser.
func TestWindowClosedByHand(t *testing.T) {
	needChrome(t)
	srv := windowServer(t)
	ch := newChrome(t, chrome.Config{})
	chrome.FakeWindow(ch)
	s, ctx := session(t, ch, "otto")
	if err := s.Navigate(ctx, srv.URL+"/who?first"); err != nil {
		t.Fatal(err)
	}
	if err := ch.Takeover("otto", "me"); err != nil {
		t.Fatal(err)
	}
	if err := ch.SetWindow("otto", "me", true); err != nil {
		t.Fatal(err)
	}
	if err := chrome.OpenTab(ch, "otto", srv.URL+"/long"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2500 * time.Millisecond) // the watcher notes the new tab
	chrome.KillBrowser(ch, "otto")
	if st := ch.State("otto"); st.Running || !st.Window || st.TakenOverBy != "me" {
		t.Fatalf("closed window: %+v", st)
	}
	if err := ch.Handback("otto"); err != nil {
		t.Fatal(err)
	}
	if headed, _ := chrome.Headed(ch, "otto"); headed {
		t.Fatal("must be headless after hand-back")
	}
	tabs := waitTabs(t, ch, "otto", []string{"/long@0", "/who?first@0"}) // the tab in front comes first
	_ = tabs
	if _, err := ch.Session(context.Background(), "otto"); err != nil {
		t.Fatal(err)
	}
}

// A window that fails to open (no display, Chrome refuses to start) falls
// back to the frame view with the same tabs; the takeover goes on.
func TestWindowFailsFallsBack(t *testing.T) {
	needChrome(t)
	srv := windowServer(t)
	ch := newChrome(t, chrome.Config{})
	chrome.FakeWindow(ch)
	chrome.FailWindow(ch, errors.New("no display"))
	s, ctx := session(t, ch, "otto")
	if err := s.Navigate(ctx, srv.URL+"/long"); err != nil {
		t.Fatal(err)
	}
	if err := s.Eval(ctx, `scrollTo(0, 700)`, nil); err != nil {
		t.Fatal(err)
	}
	if err := ch.Takeover("otto", "me"); err != nil {
		t.Fatal(err)
	}
	err := ch.SetWindow("otto", "me", true)
	if !errors.Is(err, browser.ErrNoWindow) || !strings.Contains(err.Error(), "no display") {
		t.Fatalf("want ErrNoWindow: %v", err)
	}
	if st := ch.State("otto"); st.Window || st.View != "frames" || !st.Running || st.TakenOverBy != "me" {
		t.Fatalf("after a failed switch: %+v", st)
	}
	waitTabs(t, ch, "otto", []string{"/long@700"})
	if _, err := ch.Screenshot("otto"); err != nil {
		t.Fatalf("the frame view goes on: %v", err)
	}
}

// Attached browsers and windowed configs cannot switch.
func TestNoWindowWhenAttachedOrHeaded(t *testing.T) {
	headless := false
	ch, err := chrome.New(chrome.Config{Attach: "http://127.0.0.1:9", Headless: &headless}, chrome.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	if st := ch.State("otto"); st.CanWindow {
		t.Fatalf("attached: %+v", st)
	}
}
