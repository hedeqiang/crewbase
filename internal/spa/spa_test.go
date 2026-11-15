package spa

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestHandler(t *testing.T) {
	js := strings.Repeat("console.log('crewbase');\n", 200)
	h := Handler(fstest.MapFS{
		"index.html":       {Data: []byte("<!doctype html><title>Crewbase</title>")},
		"assets/app-1a.js": {Data: []byte(js)},
		"icon.png":         {Data: bytes.Repeat([]byte{0x89}, 4096)},
	})
	get := func(p, enc string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", p, nil)
		if enc != "" {
			r.Header.Set("Accept-Encoding", enc)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	w := get("/assets/app-1a.js", "gzip, br")
	if w.Code != 200 || w.Header().Get("Content-Encoding") != "gzip" || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset: %d %v", w.Code, w.Header())
	}
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("type %q", w.Header().Get("Content-Type"))
	}
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(zr); string(b) != js {
		t.Fatal("body differs after gunzip")
	}
	if w.Body.Len() >= len(js) {
		t.Fatal("not smaller")
	}

	if w := get("/assets/app-1a.js", ""); w.Header().Get("Content-Encoding") != "" || w.Body.String() != js {
		t.Fatal("plain request should get plain bytes")
	}
	if w := get("/assets/app-1a.js", "gzip;q=0"); w.Header().Get("Content-Encoding") != "" {
		t.Fatal("q=0 refuses gzip")
	}
	if w := get("/assets/app-1a.js", "gzip; q=0.0"); w.Header().Get("Content-Encoding") != "" {
		t.Fatal("q=0.0 refuses gzip")
	}
	if w := get("/assets/app-1a.js", "br, gzip;q=0.5"); w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("q=0.5 accepts gzip")
	}
	if w := get("/icon.png", "gzip"); w.Header().Get("Content-Encoding") != "" {
		t.Fatal("images are not compressed")
	}
	// client routes fall back to the app, which is never cached for long
	for _, p := range []string{"/", "/session/abc", "/index.html"} {
		w := get(p, "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), "<title>Crewbase") || w.Header().Get("Cache-Control") != "no-cache" {
			t.Fatalf("%s: %d %q %v", p, w.Code, w.Body.String(), w.Header())
		}
	}
	if w := get("/../../etc/passwd", ""); !strings.Contains(w.Body.String(), "Crewbase") {
		t.Fatal("traversal must fall back to the app")
	}
}
