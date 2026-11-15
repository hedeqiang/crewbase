// Package providers manages model providers added from the UI (settings ›
// 模型与订阅 › 添加提供方) without restarting: each one is registered on
// the llm registry under its own kernel context, so adding, editing or
// removing a provider only rebuilds that adapter.
//
// API keys never touch the config: they are written to the credentials
// service (system keychain) as keychain:crewbase/provider-<id>, and only
// that reference is persisted in ${dir}/providers.json.
package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/anthropic"
	"github.com/hedeqiang/crewbase/providers/llm/gemini"
	"github.com/hedeqiang/crewbase/providers/llm/openai"
)

const ServiceKey = "providers"

// Kinds of API the provider speaks.
const (
	APIOpenAI    = "openai"    // Chat Completions (OpenAI, DeepSeek, Qwen, …)
	APIAnthropic = "anthropic" // Messages
	APIGemini    = "gemini"
)

// Provider is one user-added provider.
type Provider struct {
	ID      string   `json:"id"`
	Name    string   `json:"name,omitempty"`
	API     string   `json:"api"`
	Preset  string   `json:"preset,omitempty"` // openai presets: deepseek, qwen, …
	BaseURL string   `json:"baseURL,omitempty"`
	KeyRef  string   `json:"keyRef,omitempty"`
	Models  []string `json:"models,omitempty"` // enabled models; empty = all listed
	// Icon is a vendor icon key, "mono" for a letter tile, "" = auto.
	Icon    string    `json:"icon,omitempty"`
	Created time.Time `json:"created"`
}

// Preset is a known provider offered in the add dialog.
type Preset struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	API     string `json:"api"`
	BaseURL string `json:"baseURL"`
	KeyURL  string `json:"keyURL,omitempty"` // where to get a key
	Icon    string `json:"icon,omitempty"`   // vendor icon key (LobeHub icons in the UI)
	// Group is "" for model vendors, "platform" for aggregators and
	// inference platforms (shown in a second section).
	Group string `json:"group,omitempty"`
	// ModelHint is shown when the provider cannot list its models and the
	// person types model ids.
	ModelHint string `json:"modelHint,omitempty"`
}

// Presets are offered in the UI; anything else is "自定义（OpenAI 兼容）".
var Presets = []Preset{
	// official model vendors
	{ID: "anthropic", Name: "Anthropic", API: APIAnthropic, BaseURL: "https://api.anthropic.com", KeyURL: "https://console.anthropic.com/settings/keys", Icon: "anthropic"},
	{ID: "openai", Name: "OpenAI", API: APIOpenAI, BaseURL: "https://api.openai.com/v1", KeyURL: "https://platform.openai.com/api-keys", Icon: "openai"},
	{ID: "gemini", Name: "Google Gemini", API: APIGemini, BaseURL: "https://generativelanguage.googleapis.com", KeyURL: "https://aistudio.google.com/apikey", Icon: "gemini"},
	{ID: "deepseek", Name: "DeepSeek", API: APIOpenAI, BaseURL: "https://api.deepseek.com", KeyURL: "https://platform.deepseek.com/api_keys", Icon: "deepseek"},
	{ID: "xai", Name: "xAI", API: APIOpenAI, BaseURL: "https://api.x.ai/v1", KeyURL: "https://console.x.ai/", Icon: "xai"},
	{ID: "moonshot", Name: "Kimi（月之暗面）", API: APIOpenAI, BaseURL: "https://api.moonshot.cn/v1", KeyURL: "https://platform.moonshot.cn/console/api-keys", Icon: "kimi"},
	{ID: "zhipu", Name: "智谱 GLM", API: APIOpenAI, BaseURL: "https://open.bigmodel.cn/api/paas/v4", KeyURL: "https://open.bigmodel.cn/usercenter/apikeys", Icon: "zhipu"},
	{ID: "minimax", Name: "MiniMax", API: APIOpenAI, BaseURL: "https://api.minimaxi.com/v1", KeyURL: "https://platform.minimaxi.com/user-center/basic-information/interface-key", Icon: "minimax"},
	{ID: "qwen", Name: "通义千问（阿里云百炼）", API: APIOpenAI, BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", KeyURL: "https://bailian.console.aliyun.com/", Icon: "qwen"},
	{ID: "volcengine", Name: "火山引擎（豆包）", API: APIOpenAI, BaseURL: "https://ark.cn-beijing.volces.com/api/v3", KeyURL: "https://console.volcengine.com/ark/region:ark+cn-beijing/apiKey", Icon: "volcengine",
		ModelHint: "模型 ID（如 doubao-seed-…）或推理接入点 ID（ep-…）"},
	// aggregators and inference platforms
	{ID: "openrouter", Name: "OpenRouter", API: APIOpenAI, BaseURL: "https://openrouter.ai/api/v1", KeyURL: "https://openrouter.ai/keys", Icon: "openrouter", Group: "platform"},
	{ID: "siliconflow", Name: "SiliconFlow（硅基流动）", API: APIOpenAI, BaseURL: "https://api.siliconflow.cn/v1", KeyURL: "https://cloud.siliconflow.cn/account/ak", Icon: "siliconcloud", Group: "platform"},
	{ID: "groq", Name: "Groq", API: APIOpenAI, BaseURL: "https://api.groq.com/openai/v1", KeyURL: "https://console.groq.com/keys", Icon: "groq", Group: "platform"},
	{ID: "mistral", Name: "Mistral", API: APIOpenAI, BaseURL: "https://api.mistral.ai/v1", KeyURL: "https://console.mistral.ai/api-keys", Icon: "mistral"},
}

func presetByID(id string) (Preset, bool) {
	for _, p := range Presets {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}

var (
	idRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	iconRe  = regexp.MustCompile(`^([a-z0-9-]{1,40})?$`)
	modelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,159}$`)
)

// Service is the `providers` service.
type Service struct {
	c    *kernel.Context
	path string
	llms *llm.Registry

	mu    sync.Mutex
	items map[string]Provider
	live  map[string]*kernel.Context
	errs  map[string]string
}

func (s *Service) creds() (*credentials.Service, error) {
	cs, ok := kernel.Lookup[*credentials.Service](s.c, credentials.ServiceKey)
	if !ok {
		return nil, errors.New("the credentials service is not available; cannot store API keys")
	}
	return cs, nil
}

func keyRef(id string) string { return "keychain:crewbase/provider-" + id }

// adapter builds the llm adapter for p (not registered).
func (s *Service) adapter(ctx *kernel.Context, p Provider) (llm.Adapter, error) {
	switch p.API {
	case APIAnthropic:
		return anthropic.New(ctx, anthropic.ProviderConfig{ID: p.ID, BaseURL: p.BaseURL, APIKeyRef: p.KeyRef, Models: specsAnthropic(p.Models)}), nil
	case APIGemini:
		return gemini.New(ctx, gemini.ProviderConfig{ID: p.ID, BaseURL: p.BaseURL, APIKeyRef: p.KeyRef, Models: specsGemini(p.Models)}), nil
	default:
		pc := openai.ProviderConfig{ID: p.ID, BaseURL: p.BaseURL, APIKeyRef: p.KeyRef}
		if _, ok := openai.Presets[p.Preset]; ok {
			pc.Preset = p.Preset
		}
		for _, m := range p.Models {
			pc.Models = append(pc.Models, openai.ModelSpec{ID: m})
		}
		return openai.New(ctx, pc)
	}
}

// mount (re)registers p under a fresh context.
func (s *Service) mount(p Provider) error {
	s.unmount(p.ID)
	if _, taken := s.llms.Get(p.ID); taken {
		return fmt.Errorf("a provider %q is already configured elsewhere (e.g. in a patch file)", p.ID)
	}
	ctx := s.c.Fork("")
	a, err := s.adapter(ctx, p)
	if err == nil {
		err = s.llms.Register(ctx, a)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		ctx.Dispose()
		s.errs[p.ID] = err.Error()
		return err
	}
	s.live[p.ID] = ctx
	delete(s.errs, p.ID)
	return nil
}

func (s *Service) unmount(id string) {
	s.mu.Lock()
	ctx := s.live[id]
	delete(s.live, id)
	s.mu.Unlock()
	if ctx != nil {
		ctx.Dispose()
	}
}

func (s *Service) save() error {
	if s.path == "" {
		return nil
	}
	s.mu.Lock()
	list := make([]Provider, 0, len(s.items))
	for _, p := range s.items {
		list = append(list, p)
	}
	s.mu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].Created.Before(list[j].Created) })
	b, _ := json.MarshalIndent(list, "", "  ")
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Input of Test / Add.
type Input struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Preset  string   `json:"preset"` // a Presets id, or "" for custom
	API     string   `json:"api"`
	BaseURL string   `json:"baseURL"`
	APIKey  string   `json:"apiKey"` // write-only; never echoed or stored in config
	Models  []string `json:"models"`
	Icon    string   `json:"icon"`
}

func (in *Input) normalize() error {
	if pr, ok := presetByID(in.Preset); ok {
		if in.API == "" {
			in.API = pr.API
		}
		if in.BaseURL == "" {
			in.BaseURL = pr.BaseURL
		}
		if in.ID == "" {
			in.ID = pr.ID
		}
		if in.Name == "" {
			in.Name = pr.Name
		}
		if in.Icon == "" {
			in.Icon = pr.Icon
		}
	}
	if !iconRe.MatchString(in.Icon) {
		return errors.New("icon must be an icon key")
	}
	if len(in.Models) > 200 {
		return errors.New("too many models")
	}
	for i, m := range in.Models {
		in.Models[i] = strings.TrimSpace(m)
		if !modelRe.MatchString(in.Models[i]) {
			return fmt.Errorf("invalid model id %q", m)
		}
	}
	if in.API == "" {
		in.API = APIOpenAI
	}
	switch in.API {
	case APIOpenAI, APIAnthropic, APIGemini:
	default:
		return fmt.Errorf("unknown api %q", in.API)
	}
	in.ID = strings.ToLower(strings.TrimSpace(in.ID))
	if !idRe.MatchString(in.ID) {
		return errors.New("id must be lower-case letters, digits and dashes (max 32)")
	}
	in.BaseURL = strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	if in.BaseURL != "" && !strings.HasPrefix(in.BaseURL, "https://") && !strings.HasPrefix(in.BaseURL, "http://127.0.0.1") && !strings.HasPrefix(in.BaseURL, "http://localhost") {
		return errors.New("base URL must use https (http is only allowed for localhost)")
	}
	return nil
}

// TestResult reports a connection test.
type TestResult struct {
	OK        bool            `json:"ok"`
	LatencyMs int64           `json:"latencyMs"`
	Models    []llm.ModelInfo `json:"models"`
	Error     string          `json:"error,omitempty"`
	// ManualModels: the key was not refused but the model list is not
	// available; model ids may be entered by hand.
	ManualModels bool `json:"manualModels,omitempty"`
}

// Test builds a temporary adapter with the given key and lists models.
// Nothing is stored; the key is held in a throwaway in-memory credential.
func (s *Service) Test(ctx context.Context, in Input) TestResult {
	if err := in.normalize(); err != nil {
		return TestResult{Error: err.Error(), Models: []llm.ModelInfo{}}
	}
	tctx := s.c.Fork("")
	defer tctx.Dispose()
	p := Provider{ID: in.ID, API: in.API, Preset: in.Preset, BaseURL: in.BaseURL}
	var temp string
	if in.APIKey != "" {
		cs, err := s.creds()
		if err != nil {
			return TestResult{Error: err.Error(), Models: []llm.ModelInfo{}}
		}
		temp = fmt.Sprintf("mem:provider-test-%s-%d", in.ID, time.Now().UnixNano())
		if err := cs.Set(temp, in.APIKey); err != nil {
			return TestResult{Error: err.Error(), Models: []llm.ModelInfo{}}
		}
		defer cs.Delete(temp)
		p.KeyRef = temp
	} else if cur, ok := s.Get(in.ID); ok {
		p.KeyRef = cur.KeyRef // re-test an existing provider with its stored key
	}
	a, err := s.adapter(tctx, p)
	if err != nil {
		return TestResult{Error: err.Error(), Models: []llm.ModelInfo{}}
	}
	t0 := time.Now()
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ms, err := a.Models(cctx)
	res := TestResult{LatencyMs: time.Since(t0).Milliseconds(), Models: []llm.ModelInfo{}}
	if err != nil {
		res.Error = scrub(err.Error(), in.APIKey)
		// Some endpoints (Volcengine Ark, many gateways) cannot list
		// models; anything but a refused key lets the person type ids.
		res.ManualModels = llm.KindOf(err) != llm.ErrAuth && ctx.Err() == nil
		return res
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].ID < ms[j].ID })
	res.OK, res.Models = true, ms
	return res
}

func scrub(msg, secret string) string {
	if len(secret) >= 6 {
		msg = strings.ReplaceAll(msg, secret, "[redacted]")
	}
	return msg
}

// Add stores the key in the keychain, persists the provider and registers
// it. Adding an existing id updates it.
func (s *Service) Add(in Input) (Provider, error) {
	if err := in.normalize(); err != nil {
		return Provider{}, err
	}
	prev, exists := s.Get(in.ID)
	p := Provider{ID: in.ID, Name: in.Name, API: in.API, Preset: in.Preset, BaseURL: in.BaseURL, Models: in.Models, Icon: in.Icon, Created: time.Now().UTC()}
	if exists {
		p.Created, p.KeyRef = prev.Created, prev.KeyRef
	}
	if in.APIKey != "" {
		cs, err := s.creds()
		if err != nil {
			return Provider{}, err
		}
		p.KeyRef = keyRef(p.ID)
		if err := cs.Set(p.KeyRef, in.APIKey); err != nil {
			return Provider{}, fmt.Errorf("store API key: %w", err)
		}
	}
	if p.KeyRef == "" {
		return Provider{}, errors.New("an API key is required")
	}
	if !exists {
		if _, taken := s.llms.Get(p.ID); taken {
			return Provider{}, fmt.Errorf("a provider %q already exists", p.ID)
		}
	}
	s.mu.Lock()
	s.items[p.ID] = p
	s.mu.Unlock()
	if err := s.save(); err != nil {
		return p, err
	}
	return p, s.mount(p)
}

// Remove unregisters a provider and deletes its stored key.
func (s *Service) Remove(id string) error {
	p, ok := s.Get(id)
	if !ok {
		return fmt.Errorf("provider %s not found", id)
	}
	s.unmount(id)
	s.mu.Lock()
	delete(s.items, id)
	delete(s.errs, id)
	s.mu.Unlock()
	if cs, err := s.creds(); err == nil && strings.HasPrefix(p.KeyRef, "keychain:crewbase/provider-") {
		_ = cs.Delete(p.KeyRef)
	}
	return s.save()
}

// Get returns a user-added provider.
func (s *Service) Get(id string) (Provider, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.items[id]
	return p, ok
}

// View is a provider as the UI sees it (no secrets).
type View struct {
	Provider
	Masked string `json:"masked,omitempty"`
	Active bool   `json:"active"`
	Error  string `json:"error,omitempty"`
}

// List returns user-added providers.
func (s *Service) List() []View {
	cs, _ := s.creds()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []View{}
	for _, p := range s.items {
		v := View{Provider: p, Active: s.live[p.ID] != nil, Error: s.errs[p.ID]}
		if cs != nil && p.KeyRef != "" {
			v.Masked = cs.Describe(p.KeyRef).Masked
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

func specsAnthropic(ids []string) []anthropic.ModelSpec {
	var out []anthropic.ModelSpec
	for _, m := range ids {
		out = append(out, anthropic.ModelSpec{ID: m})
	}
	return out
}

func specsGemini(ids []string) []gemini.ModelSpec {
	var out []gemini.ModelSpec
	for _, m := range ids {
		out = append(out, gemini.ModelSpec{ID: m})
	}
	return out
}

// Config of the plugin.
type Config struct {
	Dir string `json:"dir"`
}

// Plugin manages user-added providers.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/providers",
	Inject:      []string{llm.ServiceKey},
	Optional:    []string{credentials.ServiceKey},
	Description: "Model providers added from the UI, applied without a restart.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		s := &Service{c: c, llms: kernel.Use[*llm.Registry](c, llm.ServiceKey), items: map[string]Provider{}, live: map[string]*kernel.Context{}, errs: map[string]string{}}
		if cfg.Dir != "" {
			if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
				return err
			}
			s.path = filepath.Join(cfg.Dir, "providers.json")
			if b, err := os.ReadFile(s.path); err == nil {
				var list []Provider
				if err := json.Unmarshal(b, &list); err != nil {
					return fmt.Errorf("providers.json: %w", err)
				}
				for _, p := range list {
					s.items[p.ID] = p
				}
			}
		}
		for _, p := range s.List() {
			if err := s.mount(p.Provider); err != nil {
				c.Log().Warn("provider not mounted", "provider", p.ID, "err", err)
			}
		}
		return c.Provide(ServiceKey, s)
	},
})

// APIPlugin exposes the service on the gateway (a separate plugin so it
// waits for the gateway, which loads after the base bundle).
var APIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/providers-api",
	Inject:      []string{gateway.ServiceKey, ServiceKey},
	Description: "Remote methods for user-added model providers.",
	Apply: func(c *kernel.Context) error {
		registerAPI(c, kernel.Use[*gateway.Gateway](c, gateway.ServiceKey), kernel.Use[*Service](c, ServiceKey))
		return nil
	},
})

func registerAPI(c *kernel.Context, g *gateway.Gateway, s *Service) {
	g.Handle(c, gateway.Route{Method: "GET", Path: "/api/providers",
		Handle: func(context.Context, *gateway.Request) (any, error) {
			return map[string]any{"providers": s.List(), "presets": Presets}, nil
		}})
	g.Handle(c, gateway.Route{Method: "POST", Path: "/api/providers/test",
		Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
			var in Input
			if err := r.Decode(&in); err != nil {
				return nil, err
			}
			return s.Test(ctx, in), nil
		}})
	g.Handle(c, gateway.Route{Method: "POST", Path: "/api/providers",
		Handle: func(_ context.Context, r *gateway.Request) (any, error) {
			var in Input
			if err := r.Decode(&in); err != nil {
				return nil, err
			}
			p, err := s.Add(in)
			if err != nil {
				return nil, gateway.BadRequest("%s", scrub(err.Error(), in.APIKey))
			}
			for _, v := range s.List() {
				if v.ID == p.ID {
					return v, nil
				}
			}
			return p, nil
		}})
	g.Handle(c, gateway.Route{Method: "DELETE", Path: "/api/providers/{id}",
		Handle: func(_ context.Context, r *gateway.Request) (any, error) {
			if err := s.Remove(r.Params["id"]); err != nil {
				return nil, gateway.NotFound()
			}
			return nil, nil
		}})
}

// KeyRefFor returns the key reference of the first provider made from
// preset (e.g. "deepseek", or a custom provider on host) that has a key,
// for features that call the vendor's other APIs with the same key.
func (s *Service) KeyRefFor(preset, host string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *Provider
	for _, p := range s.items {
		match := p.Preset == preset || (host != "" && strings.Contains(strings.ToLower(p.BaseURL), "://"+host))
		if match && p.KeyRef != "" && (best == nil || p.Created.Before(best.Created)) {
			p := p
			best = &p
		}
	}
	if best == nil {
		return "", false
	}
	return best.KeyRef, true
}
