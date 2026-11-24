// Package providers manages model providers added from the UI (settings ›
// 模型与订阅 › 添加提供方) without restarting: each one is registered on
// the llm registry under its own kernel context, so adding, editing or
// removing a provider only rebuilds that adapter.
//
// API keys never touch the config: they are written to the credentials
// service (the vault) as vault:crewbase/provider-<id>, and only
// that reference is persisted in ${dir}/providers.json.
package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
	"github.com/hedeqiang/crewbase/providers/llm/magpie"
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
	// Kinds marks models by hand as chat | image | video, for ids the
	// built-in rules (llm.ClassifyModel) do not recognise. Only models in
	// Models can be marked.
	Kinds map[string]string `json:"kinds,omitempty"`
	// Icon is a vendor icon key, "mono" for a letter tile, "" = auto.
	Icon    string    `json:"icon,omitempty"`
	Created time.Time `json:"created"`
	// Windows are the context windows the provider's own listing reported
	// for its chosen models (recorded when it is added or edited): what the
	// provider says wins over the model catalog, which only fills gaps.
	Windows map[string]int `json:"windows,omitempty"`
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
	// Desc says in a few words what it offers (shown under its name).
	Desc string `json:"desc,omitempty"`
}

// Presets are offered in the UI; anything else is "自定义（OpenAI 兼容）".
var Presets = []Preset{
	// official model vendors
	{ID: "anthropic", Name: "Anthropic", API: APIAnthropic, BaseURL: "https://api.anthropic.com", KeyURL: "https://console.anthropic.com/settings/keys", Icon: "anthropic", Desc: "Claude 系列，官方 API"},
	{ID: "openai", Name: "OpenAI", API: APIOpenAI, BaseURL: "https://api.openai.com/v1", KeyURL: "https://platform.openai.com/api-keys", Icon: "openai", Desc: "GPT 与 o 系列，官方 API"},
	{ID: "gemini", Name: "Google Gemini", API: APIGemini, BaseURL: "https://generativelanguage.googleapis.com", KeyURL: "https://aistudio.google.com/apikey", Icon: "gemini", Desc: "Gemini 系列，Google AI Studio"},
	{ID: "deepseek", Name: "DeepSeek", API: APIOpenAI, BaseURL: "https://api.deepseek.com", KeyURL: "https://platform.deepseek.com/api_keys", Icon: "deepseek", Desc: "DeepSeek V 与 R 系列，价格低"},
	{ID: "xai", Name: "xAI", API: APIOpenAI, BaseURL: "https://api.x.ai/v1", KeyURL: "https://console.x.ai/", Icon: "xai", Desc: "Grok 系列"},
	{ID: "moonshot", Name: "Kimi（月之暗面）", API: APIOpenAI, BaseURL: "https://api.moonshot.cn/v1", KeyURL: "https://platform.moonshot.cn/console/api-keys", Icon: "kimi", Desc: "Kimi 系列，长上下文"},
	{ID: "zhipu", Name: "智谱 GLM", API: APIOpenAI, BaseURL: "https://open.bigmodel.cn/api/paas/v4", KeyURL: "https://open.bigmodel.cn/usercenter/apikeys", Icon: "zhipu", Desc: "GLM 系列"},
	{ID: "minimax", Name: "MiniMax", API: APIOpenAI, BaseURL: "https://api.minimaxi.com/v1", KeyURL: "https://platform.minimaxi.com/user-center/basic-information/interface-key", Icon: "minimax", Desc: "MiniMax 系列"},
	{ID: "qwen", Name: "通义千问（阿里云百炼）", API: APIOpenAI, BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", KeyURL: "https://bailian.console.aliyun.com/", Icon: "qwen", Desc: "Qwen 系列，阿里云百炼"},
	{ID: "volcengine", Name: "火山引擎（豆包）", API: APIOpenAI, BaseURL: "https://ark.cn-beijing.volces.com/api/v3", KeyURL: "https://console.volcengine.com/ark/region:ark+cn-beijing/apiKey", Icon: "volcengine", Desc: "豆包与托管的开源模型",
		ModelHint: "模型 ID（如 doubao-seed-…）或推理接入点 ID（ep-…）"},
	// aggregators and inference platforms
	{ID: "linkyrouter", Name: "LinkyRouter", API: APIOpenAI, BaseURL: "https://api.linkyrouter.ai/v1", KeyURL: "https://console.linkyrouter.ai", Icon: "linkyrouter", Group: "platform", Desc: "一个 Key 用 OpenAI、Anthropic、Google 等 50+ 模型"},
	{ID: "openrouter", Name: "OpenRouter", API: APIOpenAI, BaseURL: "https://openrouter.ai/api/v1", KeyURL: "https://openrouter.ai/keys", Icon: "openrouter", Group: "platform", Desc: "一个 Key 用数百个模型"},
	{ID: "siliconflow", Name: "SiliconFlow（硅基流动）", API: APIOpenAI, BaseURL: "https://api.siliconflow.cn/v1", KeyURL: "https://cloud.siliconflow.cn/account/ak", Icon: "siliconcloud", Group: "platform", Desc: "开源模型推理，国内访问快"},
	{ID: "groq", Name: "Groq", API: APIOpenAI, BaseURL: "https://api.groq.com/openai/v1", KeyURL: "https://console.groq.com/keys", Icon: "groq", Group: "platform", Desc: "推理极快的开源模型"},
	{ID: "mistral", Name: "Mistral", API: APIOpenAI, BaseURL: "https://api.mistral.ai/v1", KeyURL: "https://console.mistral.ai/api-keys", Icon: "mistral", Desc: "Mistral 系列"},
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

	switches *switchStore // see switches.go
}

func (s *Service) creds() (*credentials.Service, error) {
	cs, ok := kernel.Lookup[*credentials.Service](s.c, credentials.ServiceKey)
	if !ok {
		return nil, errors.New("the credentials service is not available; cannot store API keys")
	}
	return cs, nil
}

func keyRef(id string) string { return "vault:crewbase/provider-" + id }

// adapter builds the llm adapter for p (not registered).
func (s *Service) adapter(ctx *kernel.Context, p Provider) (llm.Adapter, error) {
	switch p.API {
	case APIAnthropic:
		return anthropic.New(ctx, anthropic.ProviderConfig{ID: p.ID, BaseURL: p.BaseURL, APIKeyRef: p.KeyRef, Models: specsAnthropic(p.Models)}), nil
	case APIGemini:
		return gemini.New(ctx, gemini.ProviderConfig{ID: p.ID, BaseURL: p.BaseURL, APIKeyRef: p.KeyRef, Models: specsGemini(p.Models, p.Kinds)}), nil
	default:
		pc := openai.ProviderConfig{ID: p.ID, BaseURL: p.BaseURL, APIKeyRef: p.KeyRef}
		if _, ok := openai.Presets[p.Preset]; ok {
			pc.Preset = p.Preset
		}
		for _, m := range p.Models {
			pc.Models = append(pc.Models, openai.ModelSpec{ID: m, Kind: p.Kinds[m], ContextWindow: p.Windows[m]})
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
	// Kinds: see Provider.Kinds.
	Kinds map[string]string `json:"kinds,omitempty"`
	Icon  string            `json:"icon"`
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
	for m, k := range in.Kinds {
		switch k {
		case llm.KindChat, llm.KindImage, llm.KindVideo:
		default:
			return fmt.Errorf("model kind must be chat, image or video (got %q)", k)
		}
		if !slices.Contains(in.Models, m) {
			return fmt.Errorf("model %q is marked %s but not in the model list", m, k)
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
	res.OK, res.Models = true, s.describe(in.ID, ms)
	return res
}

// describe fills in catalog metadata (context, price) for the model
// checklist, and the catalog's kind for ids the listing does not classify,
// so image / video models are pre-marked when added.
func (s *Service) describe(provider string, ms []llm.ModelInfo) []llm.ModelInfo {
	for i, m := range ms {
		if m.Kind != "" {
			continue
		}
		if e, ok := s.llms.Lookup(m.ID); ok && (e.Kind == llm.KindImage || e.Kind == llm.KindVideo) {
			ms[i].Kind, ms[i].ContextWindow, ms[i].ContextEstimated = e.Kind, 0, false
		}
	}
	return s.llms.Enrich(provider, ms)
}

func scrub(msg, secret string) string {
	if len(secret) >= 6 {
		msg = strings.ReplaceAll(msg, secret, "[redacted]")
	}
	return msg
}

// Add stores the key in the vault, persists the provider and registers
// it. Adding an existing id updates it.
func (s *Service) Add(in Input) (Provider, error) {
	if err := in.normalize(); err != nil {
		return Provider{}, err
	}
	prev, exists := s.Get(in.ID)
	p := Provider{ID: in.ID, Name: in.Name, API: in.API, Preset: in.Preset, BaseURL: in.BaseURL, Models: in.Models, Kinds: in.Kinds, Icon: in.Icon, Created: time.Now().UTC()}
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
	if err := s.mount(p); err != nil {
		return p, err
	}
	s.learnWindows(p.ID)
	return p, nil
}

// learnWindows records, in the background, the context windows the
// provider's listing reports for its chosen models (OpenAI-compatible APIs
// report them; a guessed or missing window is not recorded, the catalog
// fills it). A failure changes nothing.
func (s *Service) learnWindows(id string) {
	s.c.Go(func(ctx context.Context) {
		p, ok := s.Get(id)
		if !ok || p.API == APIAnthropic || p.API == APIGemini || len(p.Models) == 0 {
			return
		}
		tctx := s.c.Fork("")
		defer tctx.Dispose()
		ad, err := s.adapter(tctx, Provider{ID: p.ID, API: p.API, Preset: p.Preset, BaseURL: p.BaseURL, KeyRef: p.KeyRef})
		if err != nil {
			return
		}
		f, ok := ad.(interface {
			FetchModels(context.Context) ([]llm.ModelInfo, error)
		})
		if !ok {
			return
		}
		lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		ms, err := f.FetchModels(lctx)
		if err != nil {
			return
		}
		got := map[string]int{}
		for _, m := range ms {
			if m.ContextWindow > 0 && !m.ContextEstimated && slices.Contains(p.Models, m.ID) {
				got[m.ID] = m.ContextWindow
			}
		}
		s.mu.Lock()
		cur, ok := s.items[id]
		if !ok || maps.Equal(cur.Windows, got) || (len(got) == 0 && len(cur.Windows) == 0) {
			s.mu.Unlock()
			return
		}
		if len(got) == 0 {
			got = nil
		}
		cur.Windows = got
		s.items[id] = cur
		s.mu.Unlock()
		if s.save() == nil {
			_ = s.mount(cur)
		}
	})
}

// Patch edits an added provider; nil fields are left as they are.
type Patch struct {
	Name    *string `json:"name"`
	Icon    *string `json:"icon"`
	BaseURL *string `json:"baseURL"`
	// APIKey replaces the stored key; "" keeps the one in the vault.
	APIKey string    `json:"apiKey"`
	Models *[]string `json:"models"`
	// Kinds replaces the per-model kind marks (with Models or alone).
	Kinds *map[string]string `json:"kinds"`
}

// Update edits provider id (name, icon, base URL, key, models, kinds)
// and re-mounts it at once. Its id, API and preset do not change.
func (s *Service) Update(id string, pt Patch) (Provider, error) {
	cur, ok := s.Get(id)
	if !ok {
		return Provider{}, errNotFound
	}
	in := Input{ID: cur.ID, Name: cur.Name, Preset: cur.Preset, API: cur.API, BaseURL: cur.BaseURL,
		Models: append([]string(nil), cur.Models...), Kinds: map[string]string{}, Icon: cur.Icon}
	for m, k := range cur.Kinds {
		in.Kinds[m] = k
	}
	if pt.Name != nil {
		in.Name = strings.TrimSpace(*pt.Name)
	}
	if pt.Icon != nil {
		in.Icon = *pt.Icon
	}
	if pt.BaseURL != nil {
		in.BaseURL = *pt.BaseURL
		if strings.TrimSpace(in.BaseURL) == "" {
			if pr, ok := presetByID(cur.Preset); ok {
				in.BaseURL = pr.BaseURL
			} else {
				return Provider{}, errors.New("a base URL is required")
			}
		}
	}
	if pt.Models != nil {
		in.Models = append([]string{}, (*pt.Models)...)
		for m := range in.Kinds { // marks of removed models go
			if !slices.Contains(in.Models, strings.TrimSpace(m)) {
				delete(in.Kinds, m)
			}
		}
	}
	if pt.Kinds != nil {
		in.Kinds = *pt.Kinds
	}
	if len(in.Name) > 80 {
		return Provider{}, errors.New("name too long")
	}
	if err := in.normalize(); err != nil {
		return Provider{}, err
	}
	if len(in.Kinds) == 0 {
		in.Kinds = nil
	}
	p := cur
	p.Name, p.Icon, p.BaseURL, p.Models, p.Kinds = in.Name, in.Icon, in.BaseURL, in.Models, in.Kinds
	if pt.APIKey != "" {
		cs, err := s.creds()
		if err != nil {
			return Provider{}, err
		}
		p.KeyRef = keyRef(p.ID)
		if err := cs.Set(p.KeyRef, pt.APIKey); err != nil {
			return Provider{}, fmt.Errorf("store API key: %w", err)
		}
	}
	s.mu.Lock()
	s.items[p.ID] = p
	s.mu.Unlock()
	if err := s.save(); err != nil {
		return p, err
	}
	if err := s.mount(p); err != nil {
		return p, err
	}
	s.learnWindows(p.ID)
	return p, nil
}

var errNotFound = errors.New("provider not found")

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
	if cs, err := s.creds(); err == nil && (strings.HasPrefix(p.KeyRef, "vault:crewbase/provider-") || strings.HasPrefix(p.KeyRef, "vault:crewbase/provider-")) {
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

func specsGemini(ids []string, kinds map[string]string) []gemini.ModelSpec {
	var out []gemini.ModelSpec
	for _, m := range ids {
		out = append(out, gemini.ModelSpec{ID: m, Kind: kinds[m]})
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
		swPath := ""
		if cfg.Dir != "" {
			swPath = filepath.Join(cfg.Dir, switchesFile)
		}
		if s.switches, err = loadSwitches(swPath); err != nil {
			c.Log().Warn("model switches not read; all models are on", "err", err)
			s.switches = &switchStore{path: swPath, off: map[string][]string{}}
		}
		for _, p := range s.List() {
			if err := s.mount(p.Provider); err != nil {
				c.Log().Warn("provider not mounted", "provider", p.ID, "err", err)
			}
		}
		s.llms.SetSwitches(c, s)
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
	g.Handle(c, gateway.Route{Method: "PATCH", Path: "/api/providers/{id}",
		Handle: func(_ context.Context, r *gateway.Request) (any, error) {
			var pt Patch
			if err := r.Decode(&pt); err != nil {
				return nil, err
			}
			p, err := s.Update(r.Params["id"], pt)
			if errors.Is(err, errNotFound) {
				return nil, gateway.NotFound()
			}
			if err != nil {
				return nil, gateway.BadRequest("%s", scrub(err.Error(), pt.APIKey))
			}
			for _, v := range s.List() {
				if v.ID == p.ID {
					return v, nil
				}
			}
			return p, nil
		}})
	// magpie (auto-detected): every model it lists, and which are enabled.
	magpieD := func() (*magpie.Discovery, error) {
		d, ok := kernel.Lookup[*magpie.Discovery](c, magpie.ServiceKey)
		if !ok {
			return nil, gateway.NotFound()
		}
		return d, nil
	}
	g.Handle(c, gateway.Route{Method: "GET", Path: "/api/providers/magpie/models",
		Handle: func(context.Context, *gateway.Request) (any, error) {
			d, err := magpieD()
			if err != nil {
				return nil, err
			}
			ch := d.Choice()
			ch.Models = s.llms.Enrich(magpie.ProviderID, ch.Models)
			return ch, nil
		}})
	g.Handle(c, gateway.Route{Method: "PUT", Path: "/api/providers/magpie/models",
		Handle: func(_ context.Context, r *gateway.Request) (any, error) {
			d, err := magpieD()
			if err != nil {
				return nil, err
			}
			var body struct {
				Enabled *[]string `json:"enabled"` // null = all
			}
			if err := r.Decode(&body); err != nil {
				return nil, err
			}
			var ids []string
			if body.Enabled != nil {
				ids = append([]string{}, (*body.Enabled)...)
				if len(ids) > 2000 {
					return nil, gateway.BadRequest("too many models")
				}
			}
			if err := d.SetEnabled(ids); err != nil {
				return nil, err
			}
			ch := d.Choice()
			ch.Models = s.llms.Enrich(magpie.ProviderID, ch.Models)
			return ch, nil
		}})
	// per-model switches of any provider (see switches.go)
	switches := func(ctx context.Context, provider string) (any, error) {
		ms, err := s.ModelSwitches(ctx, provider)
		if errors.Is(err, errNotFound) {
			return nil, gateway.NotFound()
		}
		if err != nil {
			return nil, err
		}
		if ms == nil {
			ms = []llm.ModelInfo{}
		}
		return map[string]any{"provider": provider, "models": ms}, nil
	}
	g.Handle(c, gateway.Route{Method: "GET", Path: "/api/models/{provider}/switches",
		Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
			return switches(ctx, r.Params["provider"])
		}})
	g.Handle(c, gateway.Route{Method: "PUT", Path: "/api/models/{provider}/switches",
		Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
			var body struct {
				Model   string `json:"model"`
				Enabled bool   `json:"enabled"`
			}
			if err := r.Decode(&body); err != nil {
				return nil, err
			}
			err := s.SetModelEnabled(ctx, r.Params["provider"], body.Model, body.Enabled)
			switch {
			case errors.Is(err, errNotFound):
				return nil, gateway.NotFound()
			case errors.Is(err, errLastModel):
				return nil, gateway.BadRequest("%s", err.Error())
			case err != nil:
				return nil, err
			}
			return switches(ctx, r.Params["provider"])
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
