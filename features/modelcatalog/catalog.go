// Package modelcatalog knows model metadata the providers' own listings
// often leave out — context window, max output tokens, list prices —
// from two public sources: OpenRouter's model list and LiteLLM's
// model_prices_and_context_window.json.
//
// A compact snapshot of both is embedded in the binary (go generate;
// see gen/), so lookups work offline from the first run. At runtime a
// refreshed copy is kept in <dir>/model-catalog.json and fetched again
// in the background at most once a day (and on demand). The fetch is a
// plain GET with User-Agent "crewbase/<version>": no keys, nothing about
// the person. An empty source list or autoRefresh=false keeps it offline.
//
// The catalog is installed on the llm registry (llm.Registry.SetCatalog),
// which fills in ModelInfo fields a provider did not report; what a
// provider reports always wins.
//
// Precedence: sources are merged in the configured order, field by
// field, the first source that knows a field winning (a price is taken
// whole from one source). The default order is OpenRouter, then LiteLLM:
// OpenRouter has exactly one entry per model at the vendor's list price
// and is updated within hours of a release, while LiteLLM lists the same
// id many times (resellers, clouds, regions) at different prices; LiteLLM
// fills what OpenRouter lacks — vendor-only ids (e.g. deepseek-flash),
// image models with per-image prices, cache prices.
package modelcatalog

import (
	"bytes"
	"compress/gzip"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

//go:generate go run ./gen -o snapshot.json.gz

//go:embed snapshot.json.gz
var embedded []byte

const ServiceKey = "model-catalog"

// Default sources.
const (
	OpenRouterURL = "https://openrouter.ai/api/v1/models"
	LiteLLMURL    = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"
)

// DefaultSources in precedence order (see the package doc).
var DefaultSources = []string{OpenRouterURL, LiteLLMURL}

// SourceData is one source's parsed models.
type SourceData struct {
	URL     string           `json:"url"`
	Format  string           `json:"format"`
	Fetched time.Time        `json:"fetched"`
	Models  map[string]Entry `json:"models"`
}

// Snapshot is the embedded snapshot and the cache file.
type Snapshot struct {
	V       int          `json:"v"`
	Sources []SourceData `json:"sources"`
}

// UserAgent is sent with every fetch (nothing about the person).
func UserAgent() string { return "crewbase/" + kernel.Version }

const maxSourceBytes = 64 << 20

// Fetch GETs one source and parses it.
func Fetch(ctx context.Context, client *http.Client, url string) (SourceData, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return SourceData{}, err
	}
	req.Header.Set("User-Agent", UserAgent())
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return SourceData{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return SourceData{}, fmt.Errorf("%s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxSourceBytes+1))
	if err != nil {
		return SourceData{}, err
	}
	if len(b) > maxSourceBytes {
		return SourceData{}, errors.New("response too large")
	}
	format, models, err := Parse(b)
	if err != nil {
		return SourceData{}, err
	}
	return SourceData{URL: url, Format: format, Fetched: time.Now().UTC().Truncate(time.Second), Models: models}, nil
}

// EncodeGzip writes a snapshot as gzip'd JSON (the embedded form).
func EncodeGzip(s Snapshot) ([]byte, error) {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err := json.NewEncoder(zw).Encode(s); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Embedded returns the snapshot built into the binary.
func Embedded() Snapshot {
	var s Snapshot
	zr, err := gzip.NewReader(bytes.NewReader(embedded))
	if err != nil {
		return s
	}
	_ = json.NewDecoder(zr).Decode(&s)
	return s
}

// Settings are what the person can change in Settings.
type Settings struct {
	// Sources in precedence order; empty = never fetch (offline).
	Sources []string `json:"sources"`
	// AutoRefresh fetches in the background at most once a day; when off,
	// only 立即更新 fetches.
	AutoRefresh bool `json:"autoRefresh"`
}

func (st *Settings) validate() error {
	if len(st.Sources) > 6 {
		return errors.New("at most 6 sources")
	}
	seen := map[string]bool{}
	clean := []string{}
	for _, u := range st.Sources {
		u = strings.TrimSpace(u)
		if u == "" || seen[u] {
			continue
		}
		if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://127.0.0.1") && !strings.HasPrefix(u, "http://localhost") {
			return fmt.Errorf("source %q must use https", u)
		}
		if len(u) > 500 {
			return errors.New("source URL too long")
		}
		seen[u] = true
		clean = append(clean, u)
	}
	st.Sources = clean
	return nil
}

// SourceStatus describes one configured source.
type SourceStatus struct {
	URL     string    `json:"url"`
	Format  string    `json:"format,omitempty"`
	Fetched time.Time `json:"fetched,omitzero"`
	Models  int       `json:"models"`
	// Origin of the data in use: "remote" (fetched and cached), "embedded"
	// (the snapshot in the binary) or "" (none yet).
	Origin string `json:"origin"`
	Error  string `json:"error,omitempty"`
}

// Status is the catalog state for the settings page.
type Status struct {
	Settings    Settings       `json:"settings"`
	Sources     []SourceStatus `json:"sources"`
	Models      int            `json:"models"`  // distinct model keys after merging
	Updated     time.Time      `json:"updated"` // newest data in use
	Refreshing  bool           `json:"refreshing"`
	LastAttempt time.Time      `json:"lastAttempt,omitzero"`
	EmbeddedAt  time.Time      `json:"embeddedAt,omitzero"`
}

// merged is a lookup entry after merging the sources.
type merged struct {
	Entry
	priceSrc string
}

// Service is the model catalog.
type Service struct {
	c            *kernel.Context
	dir          string // "" = no cache / settings files
	client       *http.Client
	embedded     Snapshot
	refreshEvery time.Duration

	mu          sync.RWMutex
	settings    Settings
	cache       map[string]SourceData // url → last good fetch
	errs        map[string]string
	index       map[string]merged
	refreshing  bool
	lastAttempt time.Time
	refreshMu   sync.Mutex // one refresh at a time
}

func (s *Service) cachePath() string    { return filepath.Join(s.dir, "model-catalog.json") }
func (s *Service) settingsPath() string { return filepath.Join(s.dir, "model-catalog.settings.json") }

// active returns the data in use for each configured source: the cached
// fetch, else the embedded snapshot of the same URL.
func (s *Service) activeLocked() []SourceData {
	var out []SourceData
	for _, u := range s.settings.Sources {
		if d, ok := s.cache[u]; ok {
			out = append(out, d)
			continue
		}
		for _, d := range s.embedded.Sources {
			if d.URL == u {
				out = append(out, d)
			}
		}
	}
	return out
}

// rebuildLocked merges the active sources in order.
func (s *Service) rebuildLocked() {
	idx := map[string]merged{}
	src := s.activeLocked()
	if len(src) == 0 && len(s.settings.Sources) == 0 {
		// remote fetching is off: the embedded snapshot still answers
		src = s.embedded.Sources
	}
	for _, d := range src {
		for k, e := range d.Models {
			m, ok := idx[k]
			if !ok {
				m = merged{Entry: Entry{Kind: e.Kind}}
			}
			if m.Context == 0 {
				m.Context = e.Context
			}
			if m.MaxOutput == 0 {
				m.MaxOutput = e.MaxOutput
			}
			if m.priceSrc == "" && e.priced() {
				m.Input, m.Output, m.CacheRead, m.CacheWrite, m.Image = e.Input, e.Output, e.CacheRead, e.CacheWrite, e.Image
				m.priceSrc = d.Format
			}
			idx[k] = m
		}
	}
	s.index = idx
}

// generic ids name a routing choice, not a model.
var generic = map[string]bool{"auto": true, "default": true, "latest": true, "best": true, "fast": true, "router": true}

// Lookup implements llm.Catalog.
func (s *Service) Lookup(id string) (llm.CatalogEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, k := range candidates(id) {
		if generic[k] || len(k) < 3 {
			break // "cursor/auto", ACP "default": not a model name
		}
		m, ok := s.index[k]
		if !ok {
			continue
		}
		ce := llm.CatalogEntry{ContextWindow: m.Context, MaxOutput: m.MaxOutput, Kind: m.Kind}
		if m.priceSrc != "" {
			ce.Pricing = &llm.Pricing{Input: m.Input, Output: m.Output, CacheRead: m.CacheRead, CacheWrite: m.CacheWrite, Image: m.Image, Source: m.priceSrc}
		}
		if ce.Kind == "" {
			ce.Kind = llm.KindChat
		}
		return ce, true
	}
	return llm.CatalogEntry{}, false
}

// Status reports the catalog state.
func (s *Service) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := Status{Settings: s.settings, Sources: []SourceStatus{}, Models: len(s.index), Refreshing: s.refreshing, LastAttempt: s.lastAttempt}
	if st.Settings.Sources == nil {
		st.Settings.Sources = []string{}
	}
	for _, d := range s.embedded.Sources {
		if d.Fetched.After(st.EmbeddedAt) {
			st.EmbeddedAt = d.Fetched
		}
	}
	for _, u := range s.settings.Sources {
		ss := SourceStatus{URL: u, Error: s.errs[u]}
		if d, ok := s.cache[u]; ok {
			ss.Format, ss.Fetched, ss.Models, ss.Origin = d.Format, d.Fetched, len(d.Models), "remote"
		} else {
			for _, d := range s.embedded.Sources {
				if d.URL == u {
					ss.Format, ss.Fetched, ss.Models, ss.Origin = d.Format, d.Fetched, len(d.Models), "embedded"
				}
			}
		}
		if ss.Fetched.After(st.Updated) {
			st.Updated = ss.Fetched
		}
		st.Sources = append(st.Sources, ss)
	}
	if len(s.settings.Sources) == 0 {
		st.Updated = st.EmbeddedAt
	}
	return st
}

// Settings returns the current settings.
func (s *Service) Settings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

// SetSettings changes the sources / auto refresh, applies them at once
// and keeps them in model-catalog.settings.json.
func (s *Service) SetSettings(st Settings) (Status, error) {
	if err := st.validate(); err != nil {
		return Status{}, err
	}
	if s.dir != "" {
		b, _ := json.MarshalIndent(st, "", "  ")
		if err := writeFile(s.settingsPath(), b); err != nil {
			return Status{}, err
		}
	}
	s.mu.Lock()
	s.settings = st
	for u := range s.errs {
		if !contains(st.Sources, u) {
			delete(s.errs, u)
		}
	}
	s.rebuildLocked()
	s.mu.Unlock()
	llm.AdaptersChanged.Emit(s.c.Host(), struct{}{}) // model lists show new metadata
	s.kick()
	return s.Status(), nil
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func writeFile(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Refresh fetches every configured source now. A source that fails
// keeps its previous data (cached, else embedded); the error is shown.
func (s *Service) Refresh(ctx context.Context) Status {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	s.mu.Lock()
	urls := append([]string(nil), s.settings.Sources...)
	s.refreshing, s.lastAttempt = true, time.Now().UTC()
	s.mu.Unlock()
	type res struct {
		d   SourceData
		err error
	}
	results := make([]res, len(urls))
	var wg sync.WaitGroup
	for i, u := range urls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fctx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			d, err := Fetch(fctx, s.client, u)
			results[i] = res{d, err}
		}()
	}
	wg.Wait()
	s.mu.Lock()
	changed := false
	for i, u := range urls {
		if r := results[i]; r.err != nil {
			s.errs[u] = r.err.Error()
		} else {
			delete(s.errs, u)
			s.cache[u] = r.d
			changed = true
		}
	}
	if changed {
		s.rebuildLocked()
	}
	s.refreshing = false
	snap := Snapshot{V: 1}
	for _, u := range s.settings.Sources {
		if d, ok := s.cache[u]; ok {
			snap.Sources = append(snap.Sources, d)
		}
	}
	s.mu.Unlock()
	if changed && s.dir != "" {
		if b, err := json.Marshal(snap); err == nil {
			if err := writeFile(s.cachePath(), b); err != nil {
				s.c.Log().Warn("model catalog: cache not written", "err", err)
			}
		}
	}
	if changed {
		llm.AdaptersChanged.Emit(s.c.Host(), struct{}{})
	}
	return s.Status()
}

// due reports whether a background refresh should run now: remote
// fetching is on, the newest data in use is a day old (or a source has
// none) and the last attempt was over an hour ago.
func (s *Service) due(now time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.settings.AutoRefresh || len(s.settings.Sources) == 0 || s.refreshing {
		return false
	}
	if now.Sub(s.lastAttempt) < time.Hour {
		return false
	}
	for _, u := range s.settings.Sources {
		d, ok := s.cache[u]
		if !ok {
			for _, e := range s.embedded.Sources {
				if e.URL == u { // a fresh build needs no fetch on its first run
					d, ok = e, true
				}
			}
		}
		if !ok || now.Sub(d.Fetched) >= s.refreshEvery {
			return true
		}
	}
	return false
}

// kick runs a refresh in the background when one is due.
func (s *Service) kick() {
	if !s.due(time.Now()) {
		return
	}
	s.c.Go(func(ctx context.Context) { s.Refresh(ctx) })
}

// Config of the plugin.
type Config struct {
	Dir string `json:"dir"`
	// Sources / AutoRefresh are the defaults until the person changes
	// them in Settings (then model-catalog.settings.json wins).
	Sources     *[]string `json:"sources"`
	AutoRefresh *bool     `json:"autoRefresh"`
	// CheckEvery is how often to check whether a refresh is due (1h).
	CheckEvery string `json:"checkEvery"`
	// StartDelay before the first check (10s), so start-up is not slowed.
	StartDelay string `json:"startDelay"`
}

func dur(s string, def time.Duration) (time.Duration, error) {
	if s == "" {
		return def, nil
	}
	return time.ParseDuration(s)
}

// New builds the service (Plugin does this from config).
func New(c *kernel.Context, cfg Config) (*Service, error) {
	s := &Service{c: c, dir: cfg.Dir, client: &http.Client{}, embedded: Embedded(), refreshEvery: 24 * time.Hour,
		cache: map[string]SourceData{}, errs: map[string]string{}}
	s.settings = Settings{Sources: append([]string(nil), DefaultSources...), AutoRefresh: true}
	if cfg.Sources != nil {
		s.settings.Sources = append([]string{}, (*cfg.Sources)...)
	}
	if cfg.AutoRefresh != nil {
		s.settings.AutoRefresh = *cfg.AutoRefresh
	}
	if s.dir != "" {
		if err := os.MkdirAll(s.dir, 0o700); err != nil {
			return nil, err
		}
		if b, err := os.ReadFile(s.settingsPath()); err == nil {
			var st Settings
			if json.Unmarshal(b, &st) == nil && st.validate() == nil {
				s.settings = st
			}
		}
		if b, err := os.ReadFile(s.cachePath()); err == nil {
			var snap Snapshot
			if json.Unmarshal(b, &snap) == nil {
				for _, d := range snap.Sources {
					if d.URL != "" && len(d.Models) > 0 {
						s.cache[d.URL] = d
					}
				}
			} else {
				c.Log().Warn("model catalog: cache unreadable, using the embedded snapshot")
			}
		}
	}
	if err := s.settings.validate(); err != nil {
		return nil, err
	}
	s.rebuildLocked()
	return s, nil
}

// Plugin provides the catalog and installs it on the llm registry.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/model-catalog",
	Inject:      []string{llm.ServiceKey},
	Description: "Model metadata (context window, max output, prices) from OpenRouter and LiteLLM, cached; embedded snapshot offline.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		every, err := dur(cfg.CheckEvery, time.Hour)
		if err != nil {
			return err
		}
		delay, err := dur(cfg.StartDelay, 10*time.Second)
		if err != nil {
			return err
		}
		s, err := New(c, cfg)
		if err != nil {
			return err
		}
		if err := c.Provide(ServiceKey, s); err != nil {
			return err
		}
		kernel.Use[*llm.Registry](c, llm.ServiceKey).SetCatalog(c, s)
		c.Go(func(ctx context.Context) {
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			s.kick()
			t := time.NewTicker(every)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					s.kick()
				}
			}
		})
		return nil
	},
})

// APIPlugin: GET /api/model-catalog, POST /api/model-catalog/refresh,
// PUT /api/model-catalog/settings.
var APIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/model-catalog-api",
	Inject:      []string{gateway.ServiceKey, ServiceKey},
	Description: "Remote methods for the model catalog.",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		s := kernel.Use[*Service](c, ServiceKey)
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/model-catalog",
			Handle: func(context.Context, *gateway.Request) (any, error) { return s.Status(), nil }})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/model-catalog/refresh",
			Handle: func(ctx context.Context, _ *gateway.Request) (any, error) {
				if len(s.Settings().Sources) == 0 {
					return nil, gateway.BadRequest("no sources configured")
				}
				return s.Refresh(ctx), nil
			}})
		g.Handle(c, gateway.Route{Method: "PUT", Path: "/api/model-catalog/settings",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var st Settings
				if err := r.Decode(&st); err != nil {
					return nil, err
				}
				out, err := s.SetSettings(st)
				if err != nil {
					return nil, gateway.BadRequest("%s", err.Error())
				}
				return out, nil
			}})
		return nil
	},
})
