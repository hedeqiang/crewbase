package browsertools_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/features/browsertools"
)

// framesSite serves a page on 127.0.0.1 that embeds a cross-site iframe
// (from localhost: another site, so Chrome runs it out of process) and a
// same-origin iframe.
func framesSite(t *testing.T) string {
	inner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>Inner</title><body><p>inside the cross-site iframe</p>
<button onclick="document.getElementById('r').textContent='inner clicked on '+location.hostname+' trusted='+event.isTrusted">Inner button</button>
<input id=q placeholder="Inner field"><input type="pass`+`word" placeholder="Card PIN"><div id=r></div></body>`)
	}))
	t.Cleanup(inner.Close)
	innerURL := strings.Replace(inner.URL, "127.0.0.1", "localhost", 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/same", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>Same</title><body><button onclick="document.body.append('same clicked')">Same button</button></body>`)
	})
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<!doctype html><title>Outer</title><body><h1>Checkout</h1>
<div style="height:900px">tall content</div>
<iframe title="Payment" src="%s/" width=500 height=220 style="border:7px solid #888;padding:5px"></iframe>
<iframe title="Local" src="/same" width=300 height=80></iframe></body>`, innerURL)
	})
	outer := httptest.NewServer(mux)
	t.Cleanup(outer.Close)
	return outer.URL
}

func refOf(t *testing.T, content, label string) int {
	t.Helper()
	for _, l := range strings.Split(content, "\n") {
		l = strings.TrimSpace(l)
		if strings.Contains(l, label) && strings.HasPrefix(l, "[") {
			if n, err := strconv.Atoi(l[1:strings.Index(l, "]")]); err == nil {
				return n
			}
		}
	}
	t.Fatalf("no ref for %q in:\n%s", label, content)
	return 0
}

// Cross-site (out-of-process) and same-origin iframes are part of the
// snapshot, and clicks / typing reach the right frame.
func TestIframesSnapshotAndActions(t *testing.T) {
	e := setup(t, nil)
	outer := framesSite(t)
	r := e.run(t, browsertools.ToolOpen, map[string]any{"url": outer + "/"})
	want(t, r, "# Checkout", `[iframe "Payment"] http://localhost:`, "inside the cross-site iframe", `button "Inner button"`,
		`input[text] "Inner field"`, `[iframe "Local"] http://127.0.0.1:`, `button "Same button"`, "Hint:")

	// the cross-site iframe really is out of process: a target of its own
	port, err := os.ReadFile(filepath.Join(e.a.Workspace(), "browser", "DevToolsActivePort"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get("http://127.0.0.1:" + strings.SplitN(string(port), "\n", 2)[0] + "/json/list")
	if err != nil {
		t.Fatal(err)
	}
	var list []struct{ Type, URL string }
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	oopif := false
	for _, x := range list {
		oopif = oopif || (x.Type == "iframe" && strings.Contains(x.URL, "localhost"))
	}
	if !oopif {
		t.Fatalf("the iframe is not out of process: %+v", list)
	}

	r = e.run(t, browsertools.ToolClick, map[string]any{"ref": refOf(t, r.Content, "Inner button")})
	want(t, r, "inner clicked on localhost trusted=true") // a real mouse click, not el.click()
	r = e.run(t, browsertools.ToolType, map[string]any{"ref": refOf(t, r.Content, "Inner field"), "text": "hello frame"})
	want(t, r, `value="hello frame"`)
	if x := e.run(t, browsertools.ToolType, map[string]any{"ref": refOf(t, r.Content, "Card PIN"), "text": "1234"}); !x.IsError || !strings.Contains(x.Content, "refused") {
		t.Fatalf("a secret field in an iframe must be refused: %+v", x)
	}
	r = e.run(t, browsertools.ToolSnapshot, nil)
	r = e.run(t, browsertools.ToolClick, map[string]any{"ref": refOf(t, r.Content, "Same button")})
	want(t, r, "same clicked")
}

// The host filter reaches into cross-site iframes: a request made by an
// out-of-process iframe to a host outside the list is blocked.
func TestBrowserAllowlistInIframe(t *testing.T) {
	var hits atomic.Int32
	outside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Access-Control-Allow-Origin", "*")
		fmt.Fprint(w, "leaked")
	}))
	defer outside.Close()
	// *.localhost resolves to loopback in Chrome; outside.localhost is not allowed
	outsideURL := strings.Replace(outside.URL, "127.0.0.1", "outside.localhost", 1)
	inner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<!doctype html><title>Inner</title><body><p id=s>waiting</p><img src="%s/pixel.png">
<script>fetch(%q).then(r=>r.text()).then(t=>s.textContent="got "+t).catch(()=>s.textContent="frame request blocked")</script></body>`, outsideURL, outsideURL+"/data")
	}))
	defer inner.Close()
	outer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<!doctype html><title>Outer</title><body><iframe src="%s/"></iframe></body>`, inner.URL)
	}))
	defer outer.Close()
	outerURL := strings.Replace(outer.URL, "127.0.0.1", "localhost", 1)
	e := setup(t, map[string]any{"allowHosts": []string{"localhost", "127.0.0.1"}})
	e.run(t, browsertools.ToolOpen, map[string]any{"url": outerURL})
	time.Sleep(800 * time.Millisecond)
	snap := e.run(t, browsertools.ToolSnapshot, nil)
	if !strings.Contains(snap.Content, "frame request blocked") || hits.Load() != 0 {
		t.Fatalf("the iframe reached the outside host (%d hits): %s", hits.Load(), snap.Content)
	}
}
