// Package local provides local model servers: a native Ollama client
// (/api/chat NDJSON streaming, /api/tags models with sizes) and
// OpenAI-compatible servers (LM Studio, vLLM) through providers/llm/openai.
// Servers are discovered at plugin start and on a periodic rescan; servers
// that appear register an adapter, servers that disappear unregister it.
//
// Config:
//
//	{
//	  "discover": true,              // probe the built-in defaults below (default true)
//	  "rescanInterval": "60s",       // "0" disables periodic rescans
//	  "probeTimeout": "1500ms",
//	  "dropAfter": 2,                // consecutive failed probes before unregistering
//	  "servers": [                   // extra servers / overrides by id
//	    {"id": "ollama",   "type": "ollama", "baseURL": "http://127.0.0.1:11434", "numCtx": 0},
//	    {"id": "lmstudio", "type": "openai", "baseURL": "http://127.0.0.1:1234/v1"},
//	    {"id": "vllm",     "type": "openai", "baseURL": "http://127.0.0.1:8000/v1", "apiKeyRef": "..."}
//	  ]
//	}
//
// The discovery service is provided under ServiceKey so the UI can call
// Rescan and Servers.
package local

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/openai"
)

// ServiceKey is where the *Discovery service is provided.
const ServiceKey = "llm/local"

// ServerConfig describes one local server.
type ServerConfig struct {
	ID        string             `json:"id"`
	Type      string             `json:"type"` // ollama | openai
	BaseURL   string             `json:"baseURL"`
	APIKey    string             `json:"apiKey,omitempty"`
	APIKeyRef string             `json:"apiKeyRef,omitempty"`
	NumCtx    int                `json:"numCtx,omitempty"`
	Models    []openai.ModelSpec `json:"models,omitempty"`
	Disabled  bool               `json:"disabled,omitempty"`
}

// Config is the plugin config.
type Config struct {
	Discover       *bool          `json:"discover,omitempty"`
	RescanInterval string         `json:"rescanInterval,omitempty"`
	ProbeTimeout   string         `json:"probeTimeout,omitempty"`
	DropAfter      int            `json:"dropAfter,omitempty"`
	Servers        []ServerConfig `json:"servers,omitempty"`
}

// DefaultServers are probed when discovery is on. OLLAMA_HOST overrides the
// Ollama address.
func DefaultServers() []ServerConfig {
	ollama := "http://127.0.0.1:11434"
	if h := strings.TrimSpace(os.Getenv("OLLAMA_HOST")); h != "" {
		if !strings.Contains(h, "://") {
			h = "http://" + h
		}
		ollama = h
	}
	return []ServerConfig{
		{ID: "ollama", Type: "ollama", BaseURL: ollama},
		{ID: "lmstudio", Type: "openai", BaseURL: "http://127.0.0.1:1234/v1"},
		{ID: "vllm", Type: "openai", BaseURL: "http://127.0.0.1:8000/v1"},
	}
}

// ServerStatus is a discovery snapshot for one server.
type ServerStatus struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	BaseURL   string    `json:"baseURL"`
	Up        bool      `json:"up"`
	LastError string    `json:"lastError,omitempty"`
	LastSeen  time.Time `json:"lastSeen,omitzero"`
}

type serverState struct {
	cfg      ServerConfig
	ctx      *kernel.Context // forked; disposed on disappearance
	misses   int
	lastErr  string
	lastSeen time.Time
}

// Discovery probes local servers and keeps the llm registry in sync.
type Discovery struct {
	c         *kernel.Context
	reg       *llm.Registry
	http      *http.Client
	timeout   time.Duration
	dropAfter int

	mu      sync.Mutex
	servers []*serverState
}

// NewDiscovery creates a discovery bound to c (the plugin context).
func NewDiscovery(c *kernel.Context, reg *llm.Registry, servers []ServerConfig, probeTimeout time.Duration, dropAfter int) *Discovery {
	if probeTimeout <= 0 {
		probeTimeout = 1500 * time.Millisecond
	}
	if dropAfter <= 0 {
		dropAfter = 2
	}
	d := &Discovery{c: c, reg: reg, http: &http.Client{}, timeout: probeTimeout, dropAfter: dropAfter}
	for _, s := range servers {
		s.BaseURL = trimSlash(s.BaseURL)
		if s.Type == "" {
			s.Type = "openai"
		}
		d.servers = append(d.servers, &serverState{cfg: s})
	}
	return d
}

// Servers returns a status snapshot.
func (d *Discovery) Servers() []ServerStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]ServerStatus, 0, len(d.servers))
	for _, s := range d.servers {
		out = append(out, ServerStatus{ID: s.cfg.ID, Type: s.cfg.Type, BaseURL: s.cfg.BaseURL, Up: s.ctx != nil, LastError: s.lastErr, LastSeen: s.lastSeen})
	}
	return out
}

// Rescan probes every server once and registers / unregisters adapters.
func (d *Discovery) Rescan(ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	errs := make([]error, len(d.servers))
	var wg sync.WaitGroup
	for i, s := range d.servers {
		wg.Add(1)
		go func(i int, cfg ServerConfig) {
			defer wg.Done()
			pctx, cancel := context.WithTimeout(ctx, d.timeout)
			defer cancel()
			errs[i] = d.probe(pctx, cfg)
		}(i, s.cfg)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return
	}
	for i, s := range d.servers {
		if errs[i] == nil {
			s.misses, s.lastErr, s.lastSeen = 0, "", time.Now()
			if s.ctx == nil {
				d.up(s)
			}
			continue
		}
		s.lastErr = errs[i].Error()
		s.misses++
		if s.ctx != nil && s.misses >= d.dropAfter {
			d.c.Log().Info("local model server disappeared", "server", s.cfg.ID, "url", s.cfg.BaseURL)
			s.ctx.Dispose()
			s.ctx = nil
		}
	}
}

func (d *Discovery) up(s *serverState) {
	a, err := d.adapter(s.cfg)
	if err != nil {
		s.lastErr = err.Error()
		return
	}
	fc := d.c.Fork("")
	if err := d.reg.Register(fc, a); err != nil {
		fc.Dispose()
		s.lastErr = err.Error()
		return
	}
	// The plugin context may have begun disposing after Fork; its children
	// snapshot could miss fc, so clean up ourselves.
	if d.c.Context().Err() != nil {
		fc.Dispose()
		return
	}
	s.ctx = fc
	d.c.Log().Info("local model server discovered", "server", s.cfg.ID, "url", s.cfg.BaseURL)
}

func (d *Discovery) adapter(cfg ServerConfig) (llm.Adapter, error) {
	switch cfg.Type {
	case "ollama":
		return NewOllama(d.c, cfg), nil
	case "openai":
		a, err := openai.New(d.c, openai.ProviderConfig{ID: cfg.ID, BaseURL: cfg.BaseURL, APIKey: cfg.APIKey, APIKeyRef: cfg.APIKeyRef, Models: cfg.Models, Kind: "local"})
		if err != nil {
			return nil, err
		}
		a.SetHTTPClient(d.http)
		return a, nil
	}
	return nil, fmt.Errorf("unknown server type %q", cfg.Type)
}

// probe checks that a server is up and speaks the expected protocol (a
// random service on :8000 must not be mistaken for vLLM).
func (d *Discovery) probe(ctx context.Context, cfg ServerConfig) error {
	if cfg.Disabled {
		return fmt.Errorf("disabled")
	}
	path := "/models"
	if cfg.Type == "ollama" {
		path = "/api/version"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.BaseURL+path, nil)
	if err != nil {
		return fmt.Errorf("bad base URL")
	}
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	resp, err := d.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("not responding")
		}
		return fmt.Errorf("not running")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized && cfg.Type == "openai" && cfg.APIKeyRef != "" {
		return nil // key resolved at request time
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP %d", resp.StatusCode)
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("not a model server")
	}
	key := "data"
	if cfg.Type == "ollama" {
		key = "version"
	}
	if _, ok := body[key]; !ok {
		return fmt.Errorf("not a model server")
	}
	return nil
}

func parseDuration(s string, def time.Duration) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	if s == "0" || s == "off" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}

// mergeServers overlays user servers onto defaults by id.
func mergeServers(defaults, user []ServerConfig) []ServerConfig {
	idx := map[string]int{}
	out := append([]ServerConfig(nil), defaults...)
	for i, s := range out {
		idx[s.ID] = i
	}
	for _, s := range user {
		if i, ok := idx[s.ID]; ok {
			base := out[i]
			if s.Type == "" {
				s.Type = base.Type
			}
			if s.BaseURL == "" {
				s.BaseURL = base.BaseURL
			}
			out[i] = s
			continue
		}
		idx[s.ID] = len(out)
		out = append(out, s)
	}
	return out
}

// Plugin discovers local model servers.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/llm-local",
	Inject:      []string{llm.ServiceKey},
	Optional:    []string{CredentialsKey},
	Description: "Local models: Ollama (native), LM Studio and vLLM (OpenAI-compatible), auto-discovered.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		var servers []ServerConfig
		if cfg.Discover == nil || *cfg.Discover {
			servers = DefaultServers()
		}
		servers = mergeServers(servers, cfg.Servers)
		reg := kernel.Use[*llm.Registry](c, llm.ServiceKey)
		d := NewDiscovery(c, reg, servers, parseDuration(cfg.ProbeTimeout, 1500*time.Millisecond), cfg.DropAfter)
		if err := c.Provide(ServiceKey, d); err != nil {
			return err
		}
		d.Rescan(c.Context()) // first scan synchronously so adapters exist right after start
		if every := parseDuration(cfg.RescanInterval, 60*time.Second); every > 0 {
			c.Go(func(ctx context.Context) {
				t := time.NewTicker(every)
				defer t.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-t.C:
						d.Rescan(ctx)
					}
				}
			})
		}
		return nil
	},
})
