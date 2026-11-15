// Package imconfig sets up IM gateways from the UI (设置 › 通知 › 即时通讯):
// pick a platform, paste the bot token(s), choose the channel and who may
// talk to Crewbase, test, enable. The tokens go to the system keychain
// (keychain:crewbase/im-<id>-bot / -app); im.json keeps references only.
// Each enabled gateway runs as its provider plugin, loaded in a context
// that is disposed when it is disabled or changed — no restart needed.
package imconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/features/im"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/im/discord"
	"github.com/hedeqiang/crewbase/providers/im/feishu"
	"github.com/hedeqiang/crewbase/providers/im/slack"
	"github.com/hedeqiang/crewbase/providers/im/telegram"
)

const ServiceKey = "im-config"

// Platform describes what each provider needs.
type Platform struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	App      bool   `json:"app"`      // needs a second (app-level) token: Slack socket mode
	AppID    bool   `json:"appId"`    // needs an app id: Feishu
	Channel  string `json:"channel"`  // what "channel" means here (for the form hint)
	TokenDoc string `json:"tokenDoc"` // where to get the token
	// Domain is the open platform of the provider (Feishu in China, Lark
	// elsewhere: the same gateway, separate tenants and endpoints).
	Domain string `json:"domain,omitempty"`
	spec   kernel.Spec
}

// Platforms supported by the setup form.
var Platforms = []Platform{
	{ID: "slack", Name: "Slack", App: true, Channel: "频道 ID（C…）", TokenDoc: "https://api.slack.com/apps", spec: slack.Plugin},
	{ID: "telegram", Name: "Telegram", Channel: "群组 / 对话 ID（-100…）", TokenDoc: "https://t.me/BotFather", spec: telegram.Plugin},
	{ID: "discord", Name: "Discord", Channel: "频道 ID", TokenDoc: "https://discord.com/developers/applications", spec: discord.Plugin},
	{ID: "feishu", Name: "飞书", AppID: true, Channel: "群 chat_id（oc_…）", TokenDoc: "https://open.feishu.cn/app", Domain: "https://open.feishu.cn", spec: feishu.Plugin},
	{ID: "lark", Name: "Lark", AppID: true, Channel: "群 chat_id（oc_…）", TokenDoc: "https://open.larksuite.com/app", Domain: "https://open.larksuite.com", spec: feishu.Plugin},
}

func platform(id string) (Platform, bool) {
	for _, p := range Platforms {
		if p.ID == id {
			return p, true
		}
	}
	return Platform{}, false
}

// Gateway is one configured gateway (no token values).
type Gateway struct {
	ID           string    `json:"id"`
	Platform     string    `json:"platform"`
	Channel      string    `json:"channel"`
	AllowUsers   []string  `json:"allowUsers"`
	AppID        string    `json:"appId,omitempty"`
	RelayReplies bool      `json:"relayReplies"`
	Approvals    bool      `json:"approvals"`
	Enabled      bool      `json:"enabled"`
	Created      time.Time `json:"created"`
}

// Input creates or edits a gateway; tokens are write-only.
type Input struct {
	Platform     string   `json:"platform"`
	Channel      string   `json:"channel"`
	AllowUsers   []string `json:"allowUsers"`
	AppID        string   `json:"appId"`
	RelayReplies *bool    `json:"relayReplies"`
	Approvals    *bool    `json:"approvals"`
	Enabled      *bool    `json:"enabled"`
	BotToken     string   `json:"botToken"`
	AppToken     string   `json:"appToken"`
}

// View is a gateway with whether its tokens are stored.
type View struct {
	Gateway
	HasToken    bool   `json:"hasToken"`
	HasAppToken bool   `json:"hasAppToken"`
	Running     bool   `json:"running"`
	Error       string `json:"error,omitempty"`
}

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

func botRef(id string) string { return "keychain:crewbase/im-" + id + "-bot" }
func appRef(id string) string { return "keychain:crewbase/im-" + id + "-app" }

// Service is the `im-config` service.
type Service struct {
	c    *kernel.Context
	path string
	// extra config merged into every provider config (tests: apiBase)
	extra map[string]any

	mu    sync.Mutex
	items map[string]Gateway
	live  map[string]string // id → loaded plugin id
	errs  map[string]string
}

func (s *Service) creds() (*credentials.Service, error) {
	cs, ok := kernel.Lookup[*credentials.Service](s.c, credentials.ServiceKey)
	if !ok {
		return nil, errors.New("the credentials service is not available")
	}
	return cs, nil
}

func (s *Service) load() error {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var list []Gateway
	if err := json.Unmarshal(b, &list); err != nil {
		return fmt.Errorf("im.json: %w", err)
	}
	for _, g := range list {
		s.items[g.ID] = g
	}
	return nil
}

func (s *Service) saveLocked() error {
	list := make([]Gateway, 0, len(s.items))
	for _, g := range s.items {
		list = append(list, g)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Created.Before(list[j].Created) })
	b, _ := json.MarshalIndent(list, "", "  ")
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func cleanUsers(xs []string) []string {
	var out []string
	for _, x := range xs {
		for _, p := range strings.FieldsFunc(x, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '，' }) {
			if p != "" && !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	if out == nil {
		out = []string{}
	}
	return out
}

// pluginID of a gateway's provider plugin instance.
func pluginID(id string) string { return "im-ui-" + id }

// mount (re)loads the provider plugin for g.
func (s *Service) mount(g Gateway) {
	s.unmount(g.ID)
	if !g.Enabled {
		return
	}
	p, ok := platform(g.Platform)
	if !ok {
		return
	}
	cfg := map[string]any{
		"name": g.ID, "channel": g.Channel, "allowUsers": g.AllowUsers, "relayReplies": g.RelayReplies,
		"approvals": g.Approvals, "botTokenRef": botRef(g.ID), "stateDir": filepath.Dir(s.path),
	}
	if p.App {
		cfg["appTokenRef"] = appRef(g.ID)
	}
	if p.AppID {
		cfg["appId"] = g.AppID
	}
	if p.Domain != "" {
		cfg["domain"] = p.Domain
	}
	for k, v := range s.extra {
		cfg[k] = v
	}
	id := pluginID(g.ID)
	_, err := s.c.Host().Load(p.spec, kernel.LoadOptions{ID: id, Config: cfg})
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.errs[g.ID] = err.Error()
		return
	}
	if info, _ := s.c.Host().Plugin(id); info.State == kernel.StateFailed {
		s.errs[g.ID] = info.Error
	} else {
		delete(s.errs, g.ID)
	}
	s.live[g.ID] = id
}

func (s *Service) unmount(id string) {
	s.mu.Lock()
	pid, ok := s.live[id]
	delete(s.live, id)
	s.mu.Unlock()
	if ok {
		s.c.Host().Unload(pid)
	}
}

// Save creates or updates a gateway.
func (s *Service) Save(id string, in Input) (View, error) {
	s.mu.Lock()
	prev, exists := s.items[id]
	s.mu.Unlock()
	if id == "" {
		id = in.Platform
	}
	id = strings.ToLower(strings.TrimSpace(id))
	if !idRe.MatchString(id) {
		return View{}, errors.New("id must be lower-case letters, digits and dashes")
	}
	g := prev
	if !exists {
		p, ok := platform(in.Platform)
		if !ok {
			return View{}, fmt.Errorf("unknown platform %q", in.Platform)
		}
		g = Gateway{ID: id, Platform: p.ID, Created: time.Now().UTC(), RelayReplies: true, Approvals: true, Enabled: true}
	}
	p, _ := platform(g.Platform)
	if in.Channel != "" || !exists {
		g.Channel = strings.TrimSpace(in.Channel)
	}
	if in.AllowUsers != nil {
		g.AllowUsers = cleanUsers(in.AllowUsers)
	}
	if g.AllowUsers == nil {
		g.AllowUsers = []string{}
	}
	if p.AppID && (in.AppID != "" || !exists) {
		g.AppID = strings.TrimSpace(in.AppID)
	}
	if in.RelayReplies != nil {
		g.RelayReplies = *in.RelayReplies
	}
	if in.Approvals != nil {
		g.Approvals = *in.Approvals
	}
	if in.Enabled != nil {
		g.Enabled = *in.Enabled
	}
	if g.Channel == "" {
		return View{}, errors.New("a channel is required")
	}
	if p.AppID && g.AppID == "" {
		return View{}, errors.New("an app id is required")
	}
	cs, err := s.creds()
	if err != nil {
		return View{}, err
	}
	if in.BotToken != "" {
		if err := cs.Set(botRef(id), strings.TrimSpace(in.BotToken)); err != nil {
			return View{}, err
		}
	}
	if in.AppToken != "" {
		if err := cs.Set(appRef(id), strings.TrimSpace(in.AppToken)); err != nil {
			return View{}, err
		}
	}
	if !s.hasRef(botRef(id)) {
		return View{}, errors.New("a bot token is required")
	}
	if p.App && !s.hasRef(appRef(id)) {
		return View{}, errors.New("an app-level token is required")
	}
	s.mu.Lock()
	s.items[id] = g
	err = s.saveLocked()
	s.mu.Unlock()
	if err != nil {
		return View{}, err
	}
	s.mount(g)
	return s.view(g), nil
}

func (s *Service) hasRef(ref string) bool {
	cs, err := s.creds()
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	v, err := cs.Resolve(ctx, ref)
	return err == nil && v != ""
}

// Remove deletes a gateway and its tokens.
func (s *Service) Remove(id string) error {
	s.mu.Lock()
	_, ok := s.items[id]
	if ok {
		delete(s.items, id)
	}
	err := s.saveLocked()
	s.mu.Unlock()
	if !ok {
		return os.ErrNotExist
	}
	s.unmount(id)
	if cs, cerr := s.creds(); cerr == nil {
		_ = cs.Delete(botRef(id))
		_ = cs.Delete(appRef(id))
	}
	return err
}

func (s *Service) view(g Gateway) View {
	v := View{Gateway: g, HasToken: s.hasRef(botRef(g.ID))}
	if p, _ := platform(g.Platform); p.App {
		v.HasAppToken = s.hasRef(appRef(g.ID))
	}
	s.mu.Lock()
	_, v.Running = s.live[g.ID]
	v.Error = s.errs[g.ID]
	s.mu.Unlock()
	return v
}

// List returns the configured gateways.
func (s *Service) List() []View {
	s.mu.Lock()
	list := make([]Gateway, 0, len(s.items))
	for _, g := range s.items {
		list = append(list, g)
	}
	s.mu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].Created.Before(list[j].Created) })
	out := make([]View, 0, len(list))
	for _, g := range list {
		out = append(out, s.view(g))
	}
	return out
}

// Config of the plugin.
type Config struct {
	Dir string `json:"dir"`
	// Provider is merged into each gateway's provider config (e.g. a test
	// apiBase); not exposed in the UI.
	Provider map[string]any `json:"provider,omitempty"`
}

// Plugin manages UI-configured IM gateways and exposes:
//
//	GET    /api/im/config               → {platforms, gateways}
//	PUT    /api/im/config/{id}  Input   create / update (tokens write-only)
//	DELETE /api/im/config/{id}          remove (and its tokens)
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/im-config",
	Inject:      []string{gateway.ServiceKey, credentials.ServiceKey},
	Description: "Set up IM gateways (Slack, Telegram, Discord, 飞书, Lark) from the UI; tokens in the keychain.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		if cfg.Dir == "" {
			return errors.New("im-config needs a dir")
		}
		s := &Service{c: c, path: filepath.Join(cfg.Dir, "im.json"), extra: cfg.Provider, items: map[string]Gateway{}, live: map[string]string{}, errs: map[string]string{}}
		if err := s.load(); err != nil {
			return err
		}
		c.Defer(func() {
			s.mu.Lock()
			ids := make([]string, 0, len(s.live))
			for id := range s.live {
				ids = append(ids, id)
			}
			s.mu.Unlock()
			for _, id := range ids {
				s.unmount(id)
			}
		})
		for _, g := range s.List() {
			s.mount(g.Gateway)
		}
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/im/config",
			Handle: func(context.Context, *gateway.Request) (any, error) {
				return map[string]any{"platforms": Platforms, "gateways": s.List()}, nil
			}})
		g.Handle(c, gateway.Route{Method: "PUT", Path: "/api/im/config/{id}",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var in Input
				if err := r.Decode(&in); err != nil {
					return nil, err
				}
				v, err := s.Save(r.Params["id"], in)
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return v, nil
			}})
		g.Handle(c, gateway.Route{Method: "DELETE", Path: "/api/im/config/{id}",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if err := s.Remove(r.Params["id"]); err != nil {
					if errors.Is(err, os.ErrNotExist) {
						return nil, gateway.NotFound()
					}
					return nil, err
				}
				return nil, nil
			}})
		return c.Provide(ServiceKey, s)
	},
})

var _ = im.ServicePrefix
