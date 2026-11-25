// Package proxy is Settings › 网络代理: an HTTP, HTTPS or SOCKS5 proxy that
// every outbound connection of Crewbase goes through once it is on (models,
// MCP, plugin and skill downloads, GitHub, link previews, webhook
// deliveries, the relay, and the commands agents run). The routing itself
// is internal/netproxy; this package keeps the setting (the password in the
// vault), finds proxies already running on this machine and tests one.
package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/internal/netproxy"
)

// ServiceKey is the key of the proxy service.
const ServiceKey = "proxy"

// PasswordRef is where the proxy's password is kept.
const PasswordRef = "vault:crewbase/proxy"

// Settings as stored (proxy.json) and shown. The password is never here:
// HasPassword says whether the vault has one.
type Settings struct {
	Enabled     bool     `json:"enabled"`
	Type        string   `json:"type"` // http, https, socks5
	Host        string   `json:"host"`
	Port        int      `json:"port"`
	Username    string   `json:"username,omitempty"`
	HasPassword bool     `json:"hasPassword"`
	Bypass      []string `json:"bypass"`
}

// Input is a change from the page. Password: "" keeps the saved one,
// ClearPassword removes it.
type Input struct {
	Settings
	Password      string `json:"password,omitempty"`
	ClearPassword bool   `json:"clearPassword,omitempty"`
}

// Defaults: off, an HTTP proxy on this machine, local addresses direct.
func Defaults() Settings {
	return Settings{Type: netproxy.HTTP, Host: "127.0.0.1", Port: 7890, Bypass: append([]string(nil), netproxy.DefaultBypass...)}
}

// ErrInput: the setting cannot be used as given.
var ErrInput = errors.New("invalid proxy setting")

// Service keeps the setting and applies it.
type Service struct {
	file  string
	creds func() (*credentials.Service, bool)

	mu sync.Mutex
	st Settings
}

// Open reads <dir>/proxy.json (defaults when absent) and applies it.
func Open(dir string, creds func() (*credentials.Service, bool)) (*Service, error) {
	s := &Service{file: filepath.Join(dir, "proxy.json"), creds: creds, st: Defaults()}
	b, err := os.ReadFile(s.file)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &s.st); err != nil {
			return nil, fmt.Errorf("%s: %w", s.file, err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	if s.st.Bypass == nil {
		s.st.Bypass = append([]string(nil), netproxy.DefaultBypass...)
	}
	s.apply(context.Background())
	return s, nil
}

// Settings now.
func (s *Service) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	x := s.st
	x.Bypass = append([]string(nil), s.st.Bypass...)
	return x
}

// Set checks and saves a change, then routes by it.
func (s *Service) Set(ctx context.Context, in Input) (Settings, error) {
	next, err := clean(in.Settings)
	if err != nil {
		return Settings{}, err
	}
	cs, haveVault := s.creds()
	s.mu.Lock()
	defer s.mu.Unlock()
	next.HasPassword = s.st.HasPassword
	switch {
	case in.ClearPassword:
		if haveVault {
			_ = cs.Delete(PasswordRef)
		}
		next.HasPassword = false
	case in.Password != "":
		if !haveVault {
			return Settings{}, fmt.Errorf("the vault is not available to keep the password")
		}
		if err := cs.Set(PasswordRef, in.Password); err != nil {
			return Settings{}, err
		}
		next.HasPassword = true
	}
	if next.Enabled && next.HasPassword && next.Username == "" {
		return Settings{}, fmt.Errorf("%w: a password needs a username", ErrInput)
	}
	b, _ := json.MarshalIndent(next, "", "  ")
	if err := writeFile(s.file, b); err != nil {
		return Settings{}, err
	}
	s.st = next
	s.applyLocked(ctx)
	return s.settingsLocked(), nil
}

func (s *Service) settingsLocked() Settings {
	x := s.st
	x.Bypass = append([]string(nil), s.st.Bypass...)
	return x
}

func (s *Service) apply(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applyLocked(ctx)
}

func (s *Service) applyLocked(ctx context.Context) {
	if !s.st.Enabled {
		netproxy.Set(nil)
		return
	}
	p := s.proxyOf(ctx, s.st, "")
	netproxy.Set(&p)
}

// proxyOf is the proxy of a setting; password "" takes the saved one.
func (s *Service) proxyOf(ctx context.Context, st Settings, password string) netproxy.Proxy {
	if password == "" && st.HasPassword {
		if cs, ok := s.creds(); ok {
			password, _ = cs.Resolve(ctx, PasswordRef)
		}
	}
	return netproxy.Proxy{Type: st.Type, Host: st.Host, Port: st.Port, Username: st.Username, Password: password, Bypass: st.Bypass}
}

// clean checks a setting. A whole proxy URL pasted as the host
// ("socks5://127.0.0.1:7890") is taken apart.
func clean(in Settings) (Settings, error) {
	in.Host = strings.TrimSpace(in.Host)
	if strings.Contains(in.Host, "://") {
		t, h, p, user, err := ParseURL(in.Host)
		if err != nil {
			return Settings{}, err
		}
		in.Type, in.Host, in.Port = t, h, p
		if user != "" {
			in.Username = user
		}
	}
	in.Type = strings.ToLower(strings.TrimSpace(in.Type))
	if in.Type == "socks" || in.Type == "socks5h" {
		in.Type = netproxy.SOCKS5
	}
	in.Username = strings.TrimSpace(in.Username)
	in.Bypass = netproxy.NormalizeBypass(in.Bypass)
	if in.Bypass == nil {
		in.Bypass = []string{}
	}
	switch in.Type {
	case netproxy.HTTP, netproxy.HTTPS, netproxy.SOCKS5:
	default:
		return Settings{}, fmt.Errorf("%w: type %q (http, https or socks5)", ErrInput, in.Type)
	}
	if in.Enabled || in.Host != "" {
		if in.Host == "" || strings.ContainsAny(in.Host, "/ @?#") {
			return Settings{}, fmt.Errorf("%w: host %q", ErrInput, in.Host)
		}
		if in.Port < 1 || in.Port > 65535 {
			return Settings{}, fmt.Errorf("%w: port %d", ErrInput, in.Port)
		}
	}
	return in, nil
}

// ParseURL takes apart "socks5://user@host:1080".
func ParseURL(raw string) (typ, host string, port int, user string, err error) {
	scheme, rest, _ := strings.Cut(strings.TrimSpace(raw), "://")
	typ = strings.ToLower(scheme)
	if typ == "socks" || typ == "socks5h" {
		typ = netproxy.SOCKS5
	}
	rest, _, _ = strings.Cut(rest, "/")
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		user, _, _ = strings.Cut(rest[:at], ":")
		rest = rest[at+1:]
	}
	h, ps, e := net.SplitHostPort(rest)
	if e != nil {
		return "", "", 0, "", fmt.Errorf("%w: %q needs host:port", ErrInput, raw)
	}
	port, e = strconv.Atoi(ps)
	if e != nil {
		return "", "", 0, "", fmt.Errorf("%w: port %q", ErrInput, ps)
	}
	return typ, h, port, user, nil
}

func writeFile(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
