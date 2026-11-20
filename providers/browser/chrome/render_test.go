package chrome

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/browser"
)

func TestRender(t *testing.T) {
	exe := FindExecutable()
	if exe == "" || testing.Short() {
		t.Skip("no Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><meta name=viewport content="width=device-width"><title>报告</title>
<h1>季度报告</h1><p>营收增长。</p><img src="/missing.png"><div style="width:900px;height:20px;background:red"></div>
<script>console.error("chart failed"); undefinedFn();</script>`))
	}))
	defer srv.Close()
	c := &Chrome{exe: exe}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, err := c.Render(ctx, srv.URL+"/", browser.RenderOptions{Sizes: []browser.Size{{Width: 1280, Height: 800}, {Width: 390, Height: 844}}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Title != "报告" || len(r.Shots) != 2 {
		t.Fatalf("%q %d shots", r.Title, len(r.Shots))
	}
	joined := strings.Join(r.Errors, "\n")
	if !strings.Contains(joined, "chart failed") || !strings.Contains(joined, "undefinedFn") {
		t.Fatalf("errors %q", r.Errors)
	}
	if !strings.Contains(strings.Join(r.Failed, "\n"), "missing.png") {
		t.Fatalf("failed %q", r.Failed)
	}
	if r.Shots[0].Overflow != 0 || r.Shots[1].Overflow < 400 || r.Shots[0].TextChars == 0 {
		t.Fatalf("shots %+v %+v", r.Shots[0].Overflow, r.Shots[1].Overflow)
	}
	if !bytes.HasPrefix(r.Shots[1].PNG, []byte("\x89PNG")) {
		t.Fatal("not a png")
	}
}
