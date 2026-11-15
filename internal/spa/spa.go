// Package spa serves the web UI: files from an fs.FS, unknown paths fall
// back to index.html (client-side routes), hashed build assets are cached
// for a year, and text assets go out gzip-compressed (compressed once, kept
// in memory) — the UI also loads on phones over slow links.
package spa

import (
	"bytes"
	"compress/gzip"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Handler serves fsys as a single-page app.
func Handler(fsys fs.FS) http.Handler {
	files := http.FileServer(http.FS(fsys))
	var cache sync.Map // path → *packed
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		st, err := fs.Stat(fsys, p)
		if err != nil || st.IsDir() {
			p, r.URL.Path = "index.html", "/"
			st, err = fs.Stat(fsys, p)
		}
		h := w.Header()
		if strings.HasPrefix(p, "assets/") {
			h.Set("Cache-Control", "public, max-age=31536000, immutable") // names carry a content hash
		} else {
			h.Set("Cache-Control", "no-cache") // index.html: always check for a new build
		}
		if err == nil && compressible(p) && st.Size() >= 1024 && acceptsGzip(r) && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			if pk := pack(&cache, fsys, p, st); pk != nil {
				h.Set("Content-Encoding", "gzip")
				h.Add("Vary", "Accept-Encoding")
				h.Set("Content-Type", pk.mime)
				h.Set("Content-Length", strconv.Itoa(len(pk.gz)))
				h.Set("Last-Modified", st.ModTime().UTC().Format(http.TimeFormat))
				if r.Method == http.MethodHead {
					return
				}
				_, _ = w.Write(pk.gz)
				return
			}
		}
		if p == "index.html" {
			r.URL.Path = "/" // FileServer redirects /index.html to /
		}
		files.ServeHTTP(w, r)
	})
}

type packed struct {
	size int64
	mod  time.Time
	mime string
	gz   []byte
}

func pack(cache *sync.Map, fsys fs.FS, p string, st fs.FileInfo) *packed {
	if v, ok := cache.Load(p); ok {
		if pk := v.(*packed); pk.size == st.Size() && pk.mod.Equal(st.ModTime()) {
			return pk
		}
	}
	raw, err := fs.ReadFile(fsys, p)
	if err != nil {
		return nil
	}
	var b bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
	_, _ = zw.Write(raw)
	if zw.Close() != nil {
		return nil
	}
	ct := mime.TypeByExtension(path.Ext(p))
	if ct == "" {
		ct = http.DetectContentType(raw)
	}
	pk := &packed{size: st.Size(), mod: st.ModTime(), mime: ct, gz: b.Bytes()}
	cache.Store(p, pk)
	return pk
}

func compressible(p string) bool {
	switch path.Ext(p) {
	case ".js", ".mjs", ".css", ".html", ".svg", ".json", ".webmanifest", ".txt", ".map":
		return true
	}
	return false
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
			continue
		}
		if v, ok := strings.CutPrefix(strings.ReplaceAll(params, " ", ""), "q="); ok {
			q, err := strconv.ParseFloat(v, 64)
			return err == nil && q > 0
		}
		return true
	}
	return false
}
