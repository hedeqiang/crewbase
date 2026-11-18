package preview

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultIdle is how long a static server lives without a request.
const DefaultIdle = 30 * time.Minute

// Manager runs one static server per previewed root directory. Each one
// listens on its own random 127.0.0.1 port, so every preview is its own
// origin: apart from the app (127.0.0.1:7788) and from the other previews.
type Manager struct {
	idle time.Duration

	mu     sync.Mutex
	sites  map[string]*site // agent + "\x00" + real root
	recent map[string][]Opened
	closed bool
	stop   chan struct{}
}

// NewManager starts a manager whose servers close after idle without a
// request (DefaultIdle when 0); tick is how often that is checked.
func NewManager(idle, tick time.Duration) *Manager {
	if idle <= 0 {
		idle = DefaultIdle
	}
	if tick <= 0 {
		tick = min(idle/4, time.Minute)
	}
	m := &Manager{idle: idle, sites: map[string]*site{}, recent: map[string][]Opened{}, stop: make(chan struct{})}
	go m.reap(tick)
	return m
}

type site struct {
	agent string
	root  string // real path
	port  string
	base  string // http://127.0.0.1:<port>
	srv   *http.Server
	last  atomic.Int64 // unix nanos of the last request (or open)
}

func (s *site) touch() { s.last.Store(time.Now().UnixNano()) }

func (m *Manager) reap(tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
		}
		cut := time.Now().Add(-m.idle).UnixNano()
		m.mu.Lock()
		for k, s := range m.sites {
			if s.last.Load() < cut {
				delete(m.sites, k)
				go s.srv.Close()
			}
		}
		m.mu.Unlock()
	}
}

// Serve returns the base URL of the static server for root (an existing
// directory), starting one when none runs.
func (m *Manager) Serve(agent, root string) (string, error) {
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(real); err != nil || !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", root)
	}
	key := agent + "\x00" + real
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return "", errors.New("preview servers are shut down")
	}
	if s := m.sites[key]; s != nil {
		s.touch()
		return s.base, nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	s := &site{agent: agent, root: real, port: port, base: "http://127.0.0.1:" + port}
	s.srv = &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute}
	s.touch()
	m.sites[key] = s
	go func() { _ = s.srv.Serve(ln) }()
	return s.base, nil
}

// Running lists the base URLs of an agent's servers ("" = every agent).
func (m *Manager) Running(agent string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, s := range m.sites {
		if agent == "" || s.agent == agent {
			out = append(out, s.base)
		}
	}
	return out
}

// CloseAgent stops an agent's servers (the agent was deleted).
func (m *Manager) CloseAgent(agent string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, s := range m.sites {
		if s.agent == agent {
			delete(m.sites, k)
			_ = s.srv.Close()
		}
	}
	for sid, list := range m.recent {
		kept := list[:0]
		for _, o := range list {
			if o.Agent != agent {
				kept = append(kept, o)
			}
		}
		m.recent[sid] = kept
	}
}

// Close stops every server (daemon exit).
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	close(m.stop)
	for k, s := range m.sites {
		delete(m.sites, k)
		_ = s.srv.Close()
	}
}

// remember keeps a preview the person opened from the app (not recorded
// in the session) for the session's list.
func (m *Manager) remember(sid string, o Opened) {
	if sid == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	list := append(m.recent[sid], o)
	if len(list) > maxRecent {
		list = list[len(list)-maxRecent:]
	}
	m.recent[sid] = list
}

func (m *Manager) remembered(sid string) []Opened {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Opened(nil), m.recent[sid]...)
}

// ---------------------------------------------------------------- serving

func (s *site) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.touch()
	// only this server's own loopback host: a page elsewhere that rebinds
	// its name to 127.0.0.1 gets nothing
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil || port != s.port || (host != "127.0.0.1" && host != "localhost") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "read-only", http.StatusMethodNotAllowed)
		return
	}
	h := w.Header()
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")

	up := r.URL.Path
	if !strings.HasPrefix(up, "/") {
		up = "/" + up
	}
	clean := path.Clean(up)
	for _, seg := range strings.Split(clean, "/") {
		// dotfiles (.env, .git, .crewbase) are never served
		if strings.HasPrefix(seg, ".") || strings.ContainsRune(seg, 0) {
			http.NotFound(w, r)
			return
		}
	}
	full := filepath.Join(s.root, filepath.FromSlash(clean))
	if p, info, ok := s.inside(full); ok {
		if !info.IsDir() {
			s.serveFile(w, r, p)
			return
		}
		if idx, ii, ok := s.inside(filepath.Join(p, "index.html")); ok && !ii.IsDir() {
			if !strings.HasSuffix(up, "/") {
				q := ""
				if r.URL.RawQuery != "" {
					q = "?" + r.URL.RawQuery
				}
				http.Redirect(w, r, clean+"/"+q, http.StatusMovedPermanently)
				return
			}
			s.serveFile(w, r, idx)
			return
		}
		// a folder without index.html: never listed
	}
	// single-page apps: a route (no file extension) gets the root index.html
	if path.Ext(clean) == "" {
		if idx, ii, ok := s.inside(filepath.Join(s.root, "index.html")); ok && !ii.IsDir() {
			s.serveFile(w, r, idx)
			return
		}
	}
	http.NotFound(w, r)
}

// inside resolves p (following symlinks) and reports whether it exists
// within the root.
func (s *site) inside(p string) (string, os.FileInfo, bool) {
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", nil, false
	}
	if real != s.root && !strings.HasPrefix(real, s.root+string(filepath.Separator)) {
		return "", nil, false
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", nil, false
	}
	return real, info, true
}

func (s *site) serveFile(w http.ResponseWriter, r *http.Request, p string) {
	f, err := os.Open(p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, filepath.Base(p), info.ModTime(), f)
}
