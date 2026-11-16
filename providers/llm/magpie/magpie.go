// Package magpie connects Crewbase to a running magpie gateway
// (https://github.com/yetone/magpie): one local endpoint that serves every
// model the user set up in magpie — subscriptions, API keys, local models
// and routing groups — translating between vendor APIs.
//
// Following magpie's integration guide (docs/integrating.md):
//   - the port is read (never written) from magpie's settings.json
//     (`$XDG_CONFIG_HOME/magpie` or `~/.config/magpie`), default 3425;
//   - `GET /api/hello` must answer {"name":"magpie"};
//   - models come from `GET /v1/models?kind=chat,image,video` (ids are
//     `provider/model` or a routing group name, sent as they are); image
//     and video models are marked "kind" and served by
//     `/v1/images/generations`, `/v1/images/edits` and `/v1/videos` in
//     OpenAI's shapes;
//   - requests use OpenAI Chat Completions at `/v1`, with the key
//     `magpie-crewbase` so magpie's Usage counts them as Crewbase;
//   - subscription allowances come from `GET /v1/magpie/quotas` (cached
//     ~60s, best effort; see Adapter.Quota);
//   - magpie is one more provider beside the user's own, never a
//     replacement. If magpie is not running, nothing is registered.
//
// The adapter appears and disappears as magpie starts and stops.
package magpie

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/openai"
)

// ProviderID is the provider id models are addressed under:
// "magpie/<magpie model id>", e.g. "magpie/claude/claude-opus-5-5" or
// "magpie/opus-anywhere" (a routing group).
const ProviderID = "magpie"

// Kind is reported by the adapter: magpie fronts subscriptions, API keys
// and local models alike, so it is neither of the three.
const Kind = "gateway"

// DefaultPort is magpie's default gateway port.
const DefaultPort = 3425

// AppKey names Crewbase in magpie's Usage page.
const AppKey = "magpie-crewbase"

// SettingsPath returns magpie's settings.json location.
func SettingsPath() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "magpie", "settings.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "magpie", "settings.json")
}

// DiscoverBaseURL returns the gateway root (no /v1) from settings.json.
func DiscoverBaseURL() string {
	port := DefaultPort
	if p := SettingsPath(); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			var s struct {
				Port int `json:"port"`
			}
			if json.Unmarshal(b, &s) == nil && s.Port > 0 && s.Port < 65536 {
				port = s.Port
			}
		}
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

// modelEntry is one item of magpie's /v1/models.
type modelEntry struct {
	ID              string `json:"id"`
	DisplayName     string `json:"display_name"`
	Label           string `json:"magpie_label"`
	ToolCall        *bool  `json:"tool_call"`
	OwnedBy         string `json:"owned_by"`
	Kind            string `json:"kind"`
	Reasoning       bool   `json:"reasoning"`
	ContextWindow   int    `json:"context_window"`
	MaxOutputTokens int    `json:"max_output_tokens"`
	ReasoningLevels []struct {
		Effort string `json:"effort"`
	} `json:"supported_reasoning_levels"`
	Modalities json.RawMessage `json:"modalities"`
	// Endpoints lists what a media model serves ("image-generation",
	// "image-edit", ...).
	Endpoints []string `json:"supported_endpoint_types"`
}

// Adapter is the magpie provider. Streaming is delegated to an
// OpenAI-compatible adapter rebuilt whenever the catalog changes.
type Adapter struct {
	c      *kernel.Context
	root   string // http://127.0.0.1:<port>
	client *http.Client
	window int

	mu        sync.RWMutex
	inner     *openai.Adapter
	all       []llm.ModelInfo   // everything magpie lists
	models    []llm.ModelInfo   // the enabled ones (see Discovery.SetEnabled)
	enabled   map[string]bool   // nil = all
	providers map[string]bool   // magpie providers behind the catalog
	modelProv map[string]string // model id -> magpie provider
	version   string

	quotaMu sync.Mutex // serialises quota fetches
	quota   quotaCache
}

func (a *Adapter) ID() string   { return ProviderID }
func (a *Adapter) Kind() string { return Kind }

// Version is the magpie version reported by /api/hello.
func (a *Adapter) Version() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.version
}

// BaseURL is the gateway root.
func (a *Adapter) BaseURL() string { return a.root }

// Models returns the enabled models of the catalog fetched at the last
// scan (all of them unless the person chose; see Discovery.SetEnabled).
// Pickers and routing only see these; a model chosen earlier still
// streams when it is later disabled.
func (a *Adapter) Models(context.Context) ([]llm.ModelInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]llm.ModelInfo(nil), a.models...), nil
}

// AllModels returns everything magpie lists, enabled or not.
func (a *Adapter) AllModels() []llm.ModelInfo {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]llm.ModelInfo(nil), a.all...)
}

// filterLocked recomputes models from all and enabled.
func (a *Adapter) filterLocked() {
	if a.enabled == nil {
		a.models = a.all
		return
	}
	a.models = make([]llm.ModelInfo, 0, len(a.enabled))
	for _, m := range a.all {
		if a.enabled[m.ID] {
			a.models = append(a.models, m)
		}
	}
}

func (a *Adapter) setEnabled(ids []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.enabled = enabledSet(ids)
	a.filterLocked()
}

func enabledSet(ids []string) map[string]bool {
	if ids == nil {
		return nil
	}
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// Stream sends the request through magpie's Chat Completions endpoint.
func (a *Adapter) Stream(ctx context.Context, req llm.Request, onChunk func(llm.Chunk) error) (llm.Response, error) {
	a.mu.RLock()
	inner := a.inner
	a.mu.RUnlock()
	if inner == nil {
		return llm.Response{}, &llm.Error{Kind: llm.ErrUnavailable, Provider: ProviderID, Message: "magpie is not running"}
	}
	resp, err := inner.Stream(ctx, req, onChunk)
	if le, ok := err.(*llm.Error); ok {
		le.Provider = ProviderID
	}
	return resp, err
}

func (a *Adapter) innerOrErr() (*openai.Adapter, error) {
	a.mu.RLock()
	inner := a.inner
	a.mu.RUnlock()
	if inner == nil {
		return nil, &llm.Error{Kind: llm.ErrUnavailable, Provider: ProviderID, Message: "magpie is not running"}
	}
	return inner, nil
}

// GenerateImages goes through magpie's /v1/images/generations and
// /v1/images/edits (OpenAI's shapes).
func (a *Adapter) GenerateImages(ctx context.Context, req llm.ImageRequest) (llm.ImageResult, error) {
	inner, err := a.innerOrErr()
	if err != nil {
		return llm.ImageResult{}, err
	}
	return inner.GenerateImages(ctx, req)
}

// GenerateVideo goes through magpie's /v1/videos (OpenAI's shape).
func (a *Adapter) GenerateVideo(ctx context.Context, req llm.VideoRequest, progress func(llm.VideoProgress)) (llm.Video, error) {
	inner, err := a.innerOrErr()
	if err != nil {
		return llm.Video{}, err
	}
	return inner.GenerateVideo(ctx, req, progress)
}

// AcceptsFile follows the model's input modalities in magpie's catalog.
func (a *Adapter) AcceptsFile(model, mime string) bool {
	a.mu.RLock()
	inner := a.inner
	a.mu.RUnlock()
	return inner != nil && inner.AcceptsFile(model, mime)
}

// hello checks that root is a magpie gateway and returns its version.
func hello(ctx context.Context, client *http.Client, root string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, root+"/api/hello", nil)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("hello: %s", resp.Status)
	}
	var h struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<16)).Decode(&h); err != nil || h.Name != "magpie" {
		return "", fmt.Errorf("not a magpie gateway")
	}
	return h.Version, nil
}

func (a *Adapter) fetchModels(ctx context.Context) ([]modelEntry, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, a.root+"/v1/models?kind=chat,image,video", nil)
	req.Header.Set("Authorization", "Bearer "+AppKey)
	req.Header.Set("User-Agent", "Crewbase")
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("models: %s", resp.Status)
	}
	var body struct {
		Data []modelEntry `json:"data"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 8<<20)).Decode(&body); err != nil {
		return nil, err
	}
	var out []modelEntry
	for _, m := range body.Data {
		switch m.Kind {
		case "", llm.KindChat, llm.KindImage, llm.KindVideo:
		default:
			continue // embeddings, audio, ... are not used
		}
		if m.ID == "" {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// refresh probes the gateway and rebuilds the catalog. It returns false
// when magpie is not reachable.
func (a *Adapter) refresh(ctx context.Context) (bool, error) {
	pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ver, err := hello(pctx, a.client, a.root)
	if err != nil {
		return false, nil
	}
	mctx, cancel2 := context.WithTimeout(ctx, 5*time.Second)
	defer cancel2()
	entries, err := a.fetchModels(mctx)
	if err != nil {
		return true, err
	}
	var infos []llm.ModelInfo
	var specs []openai.ModelSpec
	provs, modelProv := map[string]bool{}, map[string]string{}
	for _, m := range entries {
		if pv := modelProvider(m); pv != "" {
			provs[pv] = true
			modelProv[m.ID] = pv
		}
		name := m.Label
		if name == "" {
			name = m.DisplayName
		}
		if name == "" {
			name = m.ID
		}
		if m.Kind == llm.KindImage || m.Kind == llm.KindVideo {
			var caps []string
			if slices.Contains(m.Endpoints, "image-edit") {
				caps = append(caps, llm.CapImageEdit)
			}
			infos = append(infos, llm.ModelInfo{ID: m.ID, Provider: ProviderID, Name: name, Kind: m.Kind, Capabilities: caps, Enabled: true})
			specs = append(specs, openai.ModelSpec{ID: m.ID, Name: name, Kind: m.Kind, Capabilities: caps})
			continue
		}
		w, guessed := m.ContextWindow, false
		if w == 0 {
			w, guessed = a.window, true // the model catalog may know better
		}
		var caps []string
		if m.ToolCall == nil || *m.ToolCall {
			caps = append(caps, "tools")
		}
		if m.Reasoning || len(m.ReasoningLevels) > 0 {
			caps = append(caps, "reasoning")
		}
		if strings.Contains(string(m.Modalities), "image") {
			caps = append(caps, "vision")
		}
		// "pdf" input is listed for some models, but magpie's Chat
		// Completions bridge drops `file` parts (checked 2026-11-15:
		// 26 prompt tokens for a PDF request), so PDFs go as extracted text.
		if w >= 500000 {
			caps = append(caps, "long-context")
		}
		var efforts []string // nil: let the OpenAI adapter decide
		if len(m.ReasoningLevels) > 0 {
			efforts = []string{}
			for _, l := range m.ReasoningLevels {
				if e := strings.ToLower(l.Effort); slices.Contains(llm.Efforts, e) && !slices.Contains(efforts, e) {
					efforts = append(efforts, e)
				}
			}
			slices.SortFunc(efforts, func(x, y string) int { return slices.Index(llm.Efforts, x) - slices.Index(llm.Efforts, y) })
		} else if !m.Reasoning {
			efforts = llm.NoEfforts
		}
		infos = append(infos, llm.ModelInfo{ID: m.ID, Provider: ProviderID, Name: name, ContextWindow: w, ContextEstimated: guessed, MaxOutput: m.MaxOutputTokens, Capabilities: caps, Efforts: efforts, Enabled: true})
		specs = append(specs, openai.ModelSpec{ID: m.ID, Name: name, ContextWindow: w, Capabilities: caps, Kind: llm.KindChat, Efforts: efforts})
	}
	inner, err := openai.New(a.c, openai.ProviderConfig{
		ID: ProviderID, BaseURL: a.root + "/v1", APIKey: AppKey, Kind: Kind,
		Models: specs, EffortStyle: "auto",
		Headers: map[string]string{"User-Agent": "Crewbase"},
	})
	if err != nil {
		return true, err
	}
	inner.SetHTTPClient(a.client)
	a.mu.Lock()
	a.inner, a.all, a.version = inner, infos, ver
	a.filterLocked()
	a.providers, a.modelProv = provs, modelProv
	a.mu.Unlock()
	return true, nil
}

// Discovery keeps the magpie adapter registered while the gateway runs.
type Discovery struct {
	c       *kernel.Context
	reg     *llm.Registry
	baseURL string // fixed by config; "" = from settings.json each scan
	client  *http.Client
	window  int

	statePath string // ${stateDir}/magpie.json; "" = not persisted

	mu      sync.Mutex
	adapter *Adapter
	regCtx  *kernel.Context
	lastErr string
	enabled []string // nil = every model magpie lists
}

// Status describes the gateway for the settings UI.
type Status struct {
	Running bool   `json:"running"`
	BaseURL string `json:"baseURL"`
	Version string `json:"version,omitempty"`
	Models  int    `json:"models"` // enabled
	Total   int    `json:"total"`  // listed by magpie
	// Chosen: the person picked the enabled models (otherwise all are).
	Chosen   bool   `json:"chosen"`
	Error    string `json:"error,omitempty"`
	Settings string `json:"settings"`
}

// Status returns the current state.
func (d *Discovery) Status() Status {
	d.mu.Lock()
	defer d.mu.Unlock()
	st := Status{BaseURL: d.root(), Settings: SettingsPath(), Error: d.lastErr, Chosen: d.enabled != nil}
	if d.adapter != nil {
		st.Running, st.Version = true, d.adapter.Version()
		ms, _ := d.adapter.Models(context.Background())
		st.Models, st.Total = len(ms), len(d.adapter.AllModels())
	}
	return st
}

// Choice is magpie's model list with what is enabled.
type Choice struct {
	Running bool            `json:"running"`
	Models  []llm.ModelInfo `json:"models"`  // everything magpie lists
	Enabled []string        `json:"enabled"` // null = all
}

// Choice returns every model magpie lists and the enabled ids.
func (d *Discovery) Choice() Choice {
	d.mu.Lock()
	defer d.mu.Unlock()
	ch := Choice{Models: []llm.ModelInfo{}, Enabled: d.enabled}
	if d.adapter != nil {
		ch.Running, ch.Models = true, d.adapter.AllModels()
	}
	return ch
}

// SetEnabled chooses which magpie models Crewbase offers (nil = all,
// the default). It applies at once and is kept in magpie.json. Ids
// magpie does not list right now are kept, so a model that is briefly
// gone stays chosen.
func (d *Discovery) SetEnabled(ids []string) error {
	if ids != nil {
		seen := map[string]bool{}
		clean := make([]string, 0, len(ids))
		for _, id := range ids {
			id = strings.TrimSpace(id)
			if id != "" && !seen[id] && len(id) <= 200 {
				seen[id] = true
				clean = append(clean, id)
			}
		}
		sort.Strings(clean)
		ids = clean
	}
	if d.statePath != "" {
		b, _ := json.MarshalIndent(state{Enabled: ids}, "", "  ")
		tmp := d.statePath + ".tmp"
		if err := os.WriteFile(tmp, b, 0o600); err != nil {
			return err
		}
		if err := os.Rename(tmp, d.statePath); err != nil {
			return err
		}
	}
	d.mu.Lock()
	d.enabled = ids
	a := d.adapter
	d.mu.Unlock()
	if a != nil {
		a.setEnabled(ids)
		llm.AdaptersChanged.Emit(d.c.Host(), struct{}{})
	}
	return nil
}

// state is magpie.json.
type state struct {
	// Enabled models; absent / null = all.
	Enabled []string `json:"enabled"`
}

func (d *Discovery) root() string {
	if d.baseURL != "" {
		return strings.TrimSuffix(strings.TrimSuffix(d.baseURL, "/"), "/v1")
	}
	return DiscoverBaseURL()
}

// Rescan probes magpie now, registering or unregistering the adapter.
func (d *Discovery) Rescan(ctx context.Context) {
	root := d.root()
	d.mu.Lock()
	a := d.adapter
	if a != nil && a.root != root { // port changed in magpie's settings
		d.dropLocked()
		a = nil
	}
	d.mu.Unlock()
	if a == nil {
		d.mu.Lock()
		en := enabledSet(d.enabled)
		d.mu.Unlock()
		a = &Adapter{c: d.c, root: root, client: d.client, window: d.window, enabled: en}
	}
	up, err := a.refresh(ctx)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastErr = ""
	if err != nil {
		d.lastErr = err.Error()
	}
	switch {
	case up && d.adapter == nil && err == nil:
		rc := d.c.Fork("")
		if rerr := d.reg.Register(rc, a); rerr != nil {
			rc.Dispose()
			d.lastErr = rerr.Error()
			return
		}
		d.adapter, d.regCtx = a, rc
	case !up && d.adapter != nil:
		d.dropLocked()
	}
}

func (d *Discovery) dropLocked() {
	if d.regCtx != nil {
		d.regCtx.Dispose()
	}
	d.adapter, d.regCtx = nil, nil
}

// Config of the plugin.
type Config struct {
	// BaseURL overrides discovery (e.g. a magpie shared on the LAN; then
	// a gateway key is required — set it in magpie, not here).
	BaseURL        string `json:"baseURL"`
	RescanInterval string `json:"rescanInterval"` // default 30s; "0" disables
	// DefaultContextWindow for models magpie reports without one (and
	// the model catalog does not know).
	DefaultContextWindow int `json:"defaultContextWindow"`
	// StateDir keeps magpie.json (the models the person enabled).
	StateDir string `json:"stateDir"`
}

// ServiceKey exposes the discovery (status for the settings UI).
const ServiceKey = "llm/magpie"

// Plugin registers the magpie provider while the gateway is running.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/llm-magpie",
	Inject:      []string{llm.ServiceKey},
	Description: "magpie gateway (github.com/yetone/magpie) as a model provider.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		interval := 30 * time.Second
		if cfg.RescanInterval != "" {
			if cfg.RescanInterval == "0" {
				interval = 0
			} else if interval, err = time.ParseDuration(cfg.RescanInterval); err != nil {
				return err
			}
		}
		if cfg.DefaultContextWindow == 0 {
			cfg.DefaultContextWindow = 128000
		}
		d := &Discovery{c: c, reg: kernel.Use[*llm.Registry](c, llm.ServiceKey), baseURL: cfg.BaseURL,
			client: &http.Client{Timeout: 0}, window: cfg.DefaultContextWindow}
		if cfg.StateDir != "" {
			d.statePath = filepath.Join(cfg.StateDir, "magpie.json")
			if b, err := os.ReadFile(d.statePath); err == nil {
				var st state
				if err := json.Unmarshal(b, &st); err != nil {
					return fmt.Errorf("magpie.json: %w", err)
				}
				d.enabled = st.Enabled
			}
		}
		if err := c.Provide(ServiceKey, d); err != nil {
			return err
		}
		d.Rescan(c.Context())
		if interval > 0 {
			c.Go(func(ctx context.Context) {
				t := time.NewTicker(interval)
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
