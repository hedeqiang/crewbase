package linkpreview

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var lo = netip.MustParseAddr("127.0.0.1")

// testService resolves every *.test name to the httptest server and lets
// 127.0.0.1 (only) through the address check.
func testService(t *testing.T, names map[string]string) *Service {
	t.Helper()
	s := New()
	t.Cleanup(s.client.CloseIdleConnections)
	s.blocked = func(ip netip.Addr) bool { return ip != lo && Blocked(ip) }
	s.lookup = func(_ context.Context, host string) ([]netip.Addr, error) {
		if a, ok := names[host]; ok {
			return []netip.Addr{netip.MustParseAddr(a)}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return s
}

// at rewrites a server URL to the name site.test.
func at(srv *httptest.Server, name, p string) string {
	u, _ := url.Parse(srv.URL)
	return "http://" + name + ":" + u.Port() + p
}

func TestParseMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/og":
			fmt.Fprint(w, `<!doctype html><html><head>
<title>Fallback title</title>
<meta property="og:title" content="  The &amp; Title  ">
<meta property="og:description" content="What it is about">
<meta property="og:site_name" content="Example News">
<meta property="og:image" content="/img/cover.jpg">
<link rel="shortcut icon" href="static/fav.png">
</head><body><meta property="og:title" content="ignored"></body></html>`)
		case "/twitter":
			fmt.Fprint(w, `<html><head><meta name="twitter:title" content="Tw title"><meta name="twitter:description" content="Tw desc">
<meta name="twitter:image" content="https://cdn.example.com/x.png"><link rel="apple-touch-icon" href="/apple.png"></head></html>`)
		case "/plain":
			fmt.Fprint(w, `<html><head><base href="/sub/"><title>
  Just   a
  title </title><meta name="description" content="Plain description"><meta property="og:image" content="pic.png"></head><body>x</body></html>`)
		case "/lan":
			fmt.Fprint(w, `<html><head><title>T</title><meta property="og:image" content="http://192.168.1.1/x.png"><link rel="icon" href="http://localhost/i.png"></head></html>`)
		case "/gbk":
			w.Header().Set("Content-Type", "text/html; charset=gbk")
			// "中文" in GBK
			w.Write([]byte("<html><head><title>\xd6\xd0\xce\xc4</title></head></html>"))
		case "/empty":
			fmt.Fprint(w, `<html><head></head><body>nothing</body></html>`)
		case "/pdf":
			w.Header().Set("Content-Type", "application/pdf")
			fmt.Fprint(w, "%PDF-1.4")
		}
	}))
	defer srv.Close()
	s := testService(t, map[string]string{"site.test": "127.0.0.1"})
	ctx := context.Background()
	base := at(srv, "site.test", "")

	c, err := s.Unfurl(ctx, base+"/og#frag")
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != "page" || c.Title != "The & Title" || c.Description != "What it is about" || c.SiteName != "Example News" {
		t.Fatalf("og: %+v", c)
	}
	if c.Image != base+"/img/cover.jpg" || c.Icon != base+"/static/fav.png" || c.URL != base+"/og" {
		t.Fatalf("og urls: %+v", c)
	}

	c, err = s.Unfurl(ctx, base+"/twitter")
	if err != nil {
		t.Fatal(err)
	}
	if c.Title != "Tw title" || c.Description != "Tw desc" || c.Image != "https://cdn.example.com/x.png" || c.Icon != base+"/apple.png" || c.SiteName != "site.test" {
		t.Fatalf("twitter: %+v", c)
	}

	c, err = s.Unfurl(ctx, base+"/plain")
	if err != nil {
		t.Fatal(err)
	}
	if c.Title != "Just a title" || c.Description != "Plain description" || c.Image != base+"/sub/pic.png" || c.Icon != base+"/favicon.ico" {
		t.Fatalf("plain: %+v", c)
	}

	c, err = s.Unfurl(ctx, base+"/lan")
	if err != nil {
		t.Fatal(err)
	}
	if c.Image != "" || c.Icon != "" {
		t.Fatalf("LAN image / icon kept: %+v", c)
	}

	c, err = s.Unfurl(ctx, base+"/gbk")
	if err != nil || c.Title != "中文" {
		t.Fatalf("gbk: %+v %v", c, err)
	}

	if _, err := s.Unfurl(ctx, base+"/empty"); err == nil {
		t.Fatal("a page with nothing to show should fail")
	}
	if _, err := s.Unfurl(ctx, base+"/pdf"); err == nil {
		t.Fatal("pdf should not unfurl")
	}
}

func TestMediaLinks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/clip.mp4":
			w.Header().Set("Content-Type", "video/mp4")
		case "/a.png":
			w.Header().Set("Content-Type", "image/png")
		case "/s.mp3":
			w.Header().Set("Content-Type", "audio/mpeg")
		}
		w.Write(make([]byte, 2<<20)) // never read
	}))
	defer srv.Close()
	s := testService(t, map[string]string{"site.test": "127.0.0.1"})
	for p, kind := range map[string]string{"/clip.mp4": "video", "/a.png": "image", "/s.mp3": "audio"} {
		c, err := s.Unfurl(context.Background(), at(srv, "site.test", p))
		if err != nil {
			t.Fatal(p, err)
		}
		if c.Kind != kind || c.Media != at(srv, "site.test", p) || c.Title != p[1:] || c.Embed != nil {
			t.Fatalf("%s: %+v", p, c)
		}
	}
}

func TestEmbedFor(t *testing.T) {
	yt := "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ"
	cases := map[string]string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ":            yt,
		"https://youtube.com/watch?v=dQw4w9WgXcQ&list=x":         yt,
		"https://m.youtube.com/watch?v=dQw4w9WgXcQ":              yt,
		"https://music.youtube.com/watch?v=dQw4w9WgXcQ":          yt,
		"https://youtu.be/dQw4w9WgXcQ":                           yt,
		"https://youtu.be/dQw4w9WgXcQ?t=90":                      yt + "?start=90",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=1m30s":    yt + "?start=90",
		"https://www.youtube.com/shorts/dQw4w9WgXcQ":             yt,
		"https://www.youtube.com/embed/dQw4w9WgXcQ":              yt,
		"https://www.youtube.com/live/dQw4w9WgXcQ":               yt,
		"https://www.youtube.com/watch?v=short":                  "",
		"https://www.youtube.com/@channel":                       "",
		"https://www.bilibili.com/video/BV1GJ411x7h7":            "https://player.bilibili.com/player.html?autoplay=0&bvid=BV1GJ411x7h7",
		"https://www.bilibili.com/video/BV1GJ411x7h7/?p=3&spm=1": "https://player.bilibili.com/player.html?autoplay=0&bvid=BV1GJ411x7h7&page=3",
		"https://m.bilibili.com/video/BV1GJ411x7h7":              "https://player.bilibili.com/player.html?autoplay=0&bvid=BV1GJ411x7h7",
		"https://www.bilibili.com/video/av170001":                "https://player.bilibili.com/player.html?aid=170001&autoplay=0",
		"https://www.bilibili.com/read/cv123":                    "",
		"https://vimeo.com/76979871":                             "https://player.vimeo.com/video/76979871",
		"https://vimeo.com/channels/staffpicks/76979871":         "https://player.vimeo.com/video/76979871",
		"https://vimeo.com/76979871/a1b2c3d4e5":                  "https://player.vimeo.com/video/76979871?h=a1b2c3d4e5",
		"https://player.vimeo.com/video/76979871":                "https://player.vimeo.com/video/76979871",
		"https://vimeo.com/about":                                "",
		"https://example.com/watch?v=dQw4w9WgXcQ":                "",
		"https://notyoutube.com/watch?v=dQw4w9WgXcQ":             "",
	}
	for raw, want := range cases {
		u, _ := url.Parse(raw)
		e := embedFor(u)
		got := ""
		if e != nil {
			got = e.Src
			if e.Aspect < 1.7 || e.Aspect > 1.8 {
				t.Errorf("%s: aspect %v", raw, e.Aspect)
			}
		}
		if got != want {
			t.Errorf("%s: got %q want %q", raw, got, want)
		}
	}
}

// A video site that cannot be fetched (blocked here, offline) still gets
// its player, from the URL alone.
func TestVideoWithoutFetch(t *testing.T) {
	s := testService(t, nil) // every lookup fails
	c, err := s.Unfurl(context.Background(), "https://www.youtube.com/watch?v=dQw4w9WgXcQ")
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != "video" || c.Embed == nil || c.Embed.Provider != "youtube" || c.Image != "https://i.ytimg.com/vi/dQw4w9WgXcQ/hqdefault.jpg" || c.SiteName != "YouTube" {
		t.Fatalf("%+v", c)
	}
}

// Video sites are described by their APIs (oEmbed, Bilibili's view API),
// and a short link that lands on a video page gets the player too.
func TestVideoMetadata(t *testing.T) {
	var port string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oembed":
			if r.URL.Query().Get("url") != "https://www.youtube.com/watch?v=dQw4w9WgXcQ" {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, `{"title":"Never Gonna","author_name":"Rick","thumbnail_url":"https://i.ytimg.com/vi/dQw4w9WgXcQ/hqdefault.jpg"}`)
		case "/vimeo":
			fmt.Fprint(w, `{"title":"Player","description":"About it","thumbnail_url":"https://i.vimeocdn.com/x.jpg"}`)
		case "/view":
			if r.URL.Query().Get("bvid") != "BV1GJ411x7h7" {
				fmt.Fprint(w, `{"code":-400}`)
				return
			}
			fmt.Fprint(w, `{"code":0,"data":{"title":"官方 MV","pic":"http://i1.hdslb.com/a.jpg","desc":"-","owner":{"name":"UP"}}}`)
		case "/short":
			// a b23.tv-style short link
			http.Redirect(w, r, "http://www.bilibili.com:"+port+"/video/BV1GJ411x7h7?p=1", http.StatusFound)
		case "/video/BV1GJ411x7h7":
			fmt.Fprint(w, `<title>验证码_哔哩哔哩</title>`) // what robots get
		}
	}))
	defer srv.Close()
	port = fmt.Sprint(srv.Listener.Addr().(*net.TCPAddr).Port)
	defer func(a, b, c string) { youtubeOEmbed, vimeoOEmbed, bilibiliView = a, b, c }(youtubeOEmbed, vimeoOEmbed, bilibiliView)
	api := "http://api.test:" + port
	youtubeOEmbed, vimeoOEmbed, bilibiliView = api+"/oembed", api+"/vimeo", api+"/view"
	s := testService(t, map[string]string{"api.test": "127.0.0.1", "b23.test": "127.0.0.1", "www.bilibili.com": "127.0.0.1"})
	ctx := context.Background()

	c, err := s.Unfurl(ctx, "https://youtu.be/dQw4w9WgXcQ")
	if err != nil || c.Kind != "video" || c.Title != "Never Gonna" || c.Description != "Rick" || c.SiteName != "YouTube" || c.Embed.Provider != "youtube" {
		t.Fatalf("youtube: %+v %v", c, err)
	}
	c, err = s.Unfurl(ctx, "https://vimeo.com/76979871")
	if err != nil || c.Title != "Player" || c.Description != "About it" || c.Image != "https://i.vimeocdn.com/x.jpg" || c.Embed.Provider != "vimeo" {
		t.Fatalf("vimeo: %+v %v", c, err)
	}
	c, err = s.Unfurl(ctx, "https://www.bilibili.com/video/BV1GJ411x7h7")
	if err != nil || c.Title != "官方 MV" || c.Description != "UP" || c.Image != "https://i1.hdslb.com/a.jpg" || c.SiteName != "哔哩哔哩" {
		t.Fatalf("bilibili: %+v %v", c, err)
	}
	// the API refuses: still a player
	c, err = s.Unfurl(ctx, "https://www.bilibili.com/video/av170001")
	if err != nil || c.Title != "" || c.Embed == nil || c.Embed.Src != "https://player.bilibili.com/player.html?aid=170001&autoplay=0" {
		t.Fatalf("bilibili av: %+v %v", c, err)
	}
	c, err = s.Unfurl(ctx, "http://b23.test:"+port+"/short")
	if err != nil || c.Kind != "video" || c.Title != "官方 MV" || c.Embed == nil || c.Embed.Provider != "bilibili" {
		t.Fatalf("short link: %+v %v", c, err)
	}
}

func TestBlocked(t *testing.T) {
	for _, a := range []string{"127.0.0.1", "127.8.9.10", "10.1.2.3", "172.16.0.1", "172.31.255.255", "192.168.1.1", "169.254.169.254", "0.0.0.0",
		"100.64.0.1", "224.0.0.1", "255.255.255.255", "::1", "::", "fc00::1", "fd12:3456::1", "fe80::1", "ff02::1", "::ffff:127.0.0.1", "::ffff:10.0.0.1", "64:ff9b::a00:1"} {
		if !Blocked(netip.MustParseAddr(a)) {
			t.Errorf("%s should be blocked", a)
		}
	}
	// 198.18.x: a fake-IP proxy's answers (see nonPublic)
	for _, a := range []string{"8.8.8.8", "1.1.1.1", "172.32.0.1", "198.18.0.183", "2606:4700:4700::1111", "::ffff:8.8.8.8"} {
		if Blocked(netip.MustParseAddr(a)) {
			t.Errorf("%s should be allowed", a)
		}
	}
}

func TestRefusedURLs(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:7788/api/agents", "http://localhost:5173/", "http://app.localhost/", "http://[::1]/", "http://10.0.0.1/",
		"http://169.254.169.254/latest/meta-data/", "http://192.168.0.1/", "http://[fe80::1]/", "http://[fc00::1]/", "http://[::ffff:127.0.0.1]/",
		"http://0.0.0.0/", "http://printer.local/", "file:///etc/passwd", "ftp://example.com/", "javascript:alert(1)", "http://user:pw@example.com/", "https:///x"} {
		if _, err := Parse(raw); err == nil {
			t.Errorf("%s accepted", raw)
		}
	}
	if _, err := Parse("https://example.com/a?b=1#c"); err != nil {
		t.Fatal(err)
	}
}

// A public-looking name that resolves to 127.0.0.1 is refused when dialled,
// before any request reaches the server.
func TestDNSToLoopback(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, "<title>secret</title>")
	}))
	defer srv.Close()
	s := testService(t, map[string]string{"evil.test": "127.0.0.1", "evil6.test": "::1", "meta.test": "169.254.169.254"})
	s.blocked = Blocked // the real check
	for _, name := range []string{"evil.test", "evil6.test", "meta.test"} {
		_, err := s.Unfurl(context.Background(), at(srv, name, "/"))
		if !errors.Is(err, ErrBlocked) {
			t.Fatalf("%s: got %v, want blocked", name, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatal("the server was reached")
	}
}

func TestRedirectToPrivate(t *testing.T) {
	var inner atomic.Int32
	var port string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Host[:strings.IndexByte(r.Host, ':')] {
		case "pub.test":
			switch r.URL.Path {
			case "/literal":
				http.Redirect(w, r, "http://10.0.0.7/admin", http.StatusFound)
			case "/localhost":
				http.Redirect(w, r, "http://localhost/admin", http.StatusFound)
			case "/name":
				http.Redirect(w, r, "http://internal.test:"+port+"/admin", http.StatusFound)
			case "/file":
				http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
			case "/loop":
				http.Redirect(w, r, "/loop", http.StatusFound)
			}
		default:
			inner.Add(1)
			fmt.Fprint(w, "<title>internal</title>")
		}
	}))
	defer srv.Close()
	port = fmt.Sprint(srv.Listener.Addr().(*net.TCPAddr).Port)
	// internal.test is a private address (10.0.0.9); were it dialled, the
	// error would be a connection failure, not ErrBlocked
	s := testService(t, map[string]string{"pub.test": "127.0.0.1", "internal.test": "10.0.0.9"})
	for _, p := range []string{"/literal", "/localhost", "/name", "/file", "/loop"} {
		c, err := s.Unfurl(context.Background(), at(srv, "pub.test", p))
		if err == nil {
			t.Errorf("%s: followed to %+v", p, c)
		}
		if p != "/file" && p != "/loop" && !errors.Is(err, ErrBlocked) {
			t.Errorf("%s: got %v, want blocked", p, err)
		}
	}
	if inner.Load() != 0 {
		t.Fatal("a private address was reached")
	}
}

func TestSizeLimit(t *testing.T) {
	pad := strings.Repeat("<meta name=x content=y>", (maxBody/23)+100)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/late" {
			fmt.Fprint(w, "<html><head>"+pad+"<title>Too late</title></head></html>")
			return
		}
		fmt.Fprint(w, "<html><head><title>Early</title>"+pad+"</head></html>")
	}))
	defer srv.Close()
	s := testService(t, map[string]string{"site.test": "127.0.0.1"})
	if c, err := s.Unfurl(context.Background(), at(srv, "site.test", "/late")); err == nil {
		t.Fatalf("read past the limit: %+v", c)
	}
	c, err := s.Unfurl(context.Background(), at(srv, "site.test", "/early"))
	if err != nil || c.Title != "Early" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	s := testService(t, map[string]string{"site.test": "127.0.0.1"})
	s.timeout = 150 * time.Millisecond
	start := time.Now()
	if _, err := s.Unfurl(context.Background(), at(srv, "site.test", "/")); err == nil {
		t.Fatal("no timeout")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v", d)
	}
}

func TestCache(t *testing.T) {
	var hits atomic.Int32
	gate := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/slow" {
			<-gate
		}
		if r.URL.Path == "/fail" {
			http.Error(w, "no", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, "<title>ok</title>")
	}))
	defer srv.Close()
	s := testService(t, map[string]string{"site.test": "127.0.0.1"})
	var mu sync.Mutex
	now := time.Unix(1e9, 0)
	s.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	ctx := context.Background()

	u := at(srv, "site.test", "/page")
	for range 3 {
		if _, err := s.Unfurl(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("hits %d", hits.Load())
	}
	advance(59 * time.Minute)
	s.Unfurl(ctx, u)
	if hits.Load() != 1 {
		t.Fatal("refetched within the hour")
	}
	advance(2 * time.Minute)
	s.Unfurl(ctx, u)
	if hits.Load() != 2 {
		t.Fatal("not refetched after the hour")
	}

	// failures are kept for 10 minutes
	hits.Store(0)
	f := at(srv, "site.test", "/fail")
	for range 3 {
		if _, err := s.Unfurl(ctx, f); err == nil {
			t.Fatal("want error")
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("failure hits %d", hits.Load())
	}
	advance(11 * time.Minute)
	s.Unfurl(ctx, f)
	if hits.Load() != 2 {
		t.Fatal("failure kept past 10 minutes")
	}

	// the same address at the same time: one fetch
	hits.Store(0)
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for range 20 {
		wg.Go(func() {
			_, err := s.Unfurl(ctx, at(srv, "site.test", "/slow"))
			errs <- err
		})
	}
	time.Sleep(100 * time.Millisecond)
	close(gate)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("concurrent hits %d", hits.Load())
	}

	// a caller that gives up does not spoil it for the others
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Unfurl(cctx, at(srv, "site.test", "/other")); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestCacheBound(t *testing.T) {
	s := New()
	for i := range maxEntries + 50 {
		s.mu.Lock()
		s.put(fmt.Sprint("k", i), &Card{}, nil)
		s.mu.Unlock()
	}
	if len(s.cache) != maxEntries || s.order.Len() != maxEntries {
		t.Fatalf("%d entries", len(s.cache))
	}
	if _, ok := s.cache["k0"]; ok {
		t.Fatal("oldest kept")
	}
	if _, ok := s.cache[fmt.Sprint("k", maxEntries+49)]; !ok {
		t.Fatal("newest dropped")
	}
}
