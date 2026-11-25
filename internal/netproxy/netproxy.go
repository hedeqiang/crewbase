// Package netproxy is the proxy every outbound connection Crewbase makes
// goes through once one is set (Settings › 网络代理, features/proxy).
//
// Importing it points http.DefaultTransport at Func, so everything built on
// it (http.DefaultClient, &http.Client{}, the model SDKs, websocket dials)
// follows the setting with no change of its own; transports built by hand
// use Func too. With nothing set, Func is http.ProxyFromEnvironment, as
// before. Local and LAN addresses never go through it (Bypass). Commands
// Crewbase starts (agents' shell, git, MCP servers, plugins) get the same
// proxy through Env.
package netproxy

import (
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"

	"golang.org/x/net/http/httpproxy"
)

// Types of proxy.
const (
	HTTP   = "http"
	HTTPS  = "https"
	SOCKS5 = "socks5"
)

// DefaultBypass: this machine and the private networks, direct.
var DefaultBypass = []string{"localhost", "127.0.0.1", "::1", ".local", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}

// Proxy is one proxy server, with the addresses that go around it.
type Proxy struct {
	Type     string // http, https or socks5
	Host     string
	Port     int
	Username string
	Password string
	Bypass   []string // hosts, .domains (and subdomains), IPs and CIDRs
}

// URL of the proxy (with the credentials).
func (p Proxy) URL() *url.URL {
	u := &url.URL{Scheme: p.Type, Host: net.JoinHostPort(p.Host, strconv.Itoa(p.Port))}
	if p.Username != "" || p.Password != "" {
		u.User = url.UserPassword(p.Username, p.Password)
	}
	return u
}

type active struct {
	p     Proxy
	addr  string // host:port as the transport dials it
	match func(*url.URL) (*url.URL, error)
	env   []string
}

var cur atomic.Pointer[active]

func init() {
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.Proxy = Func
	}
}

// Set makes p the proxy (nil: none). Idle connections of the default
// transport are dropped so the next request takes the new way.
func Set(p *Proxy) {
	if p == nil {
		cur.Store(nil)
	} else {
		q := *p
		q.Bypass = NormalizeBypass(q.Bypass)
		cur.Store(&active{p: q, addr: q.URL().Host, match: Matcher(q), env: envOf(q, strings.Join(q.Bypass, ","))})
	}
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.CloseIdleConnections()
	}
}

// Matcher is p's choice for a URL: p, or nil (direct) for a bypassed or
// loopback address.
func Matcher(p Proxy) func(*url.URL) (*url.URL, error) {
	u := p.URL().String()
	cfg := httpproxy.Config{HTTPProxy: u, HTTPSProxy: u, NoProxy: strings.Join(NormalizeBypass(p.Bypass), ",")}
	return cfg.ProxyFunc()
}

// Current is the proxy that is set, if any.
func Current() (Proxy, bool) {
	if a := cur.Load(); a != nil {
		return a.p, true
	}
	return Proxy{}, false
}

// Func is the Proxy of every transport: the set proxy (unless the address
// is bypassed), else the environment's.
func Func(req *http.Request) (*url.URL, error) {
	if a := cur.Load(); a != nil {
		return a.match(req.URL)
	}
	return http.ProxyFromEnvironment(req)
}

// Configured is Func without the environment fallback: only the proxy set
// in Crewbase. For transports that never used the environment's.
func Configured(req *http.Request) (*url.URL, error) {
	if a := cur.Load(); a != nil {
		return a.match(req.URL)
	}
	return nil, nil
}

// IsProxyAddr reports whether addr (host:port) is the set proxy's: a
// transport dials it when the request goes through the proxy.
func IsProxyAddr(addr string) bool {
	a := cur.Load()
	return a != nil && strings.EqualFold(a.addr, addr)
}

// Env is the proxy for commands Crewbase starts (curl, git, npm, pip…),
// both spellings; nil when none is set.
func Env() []string {
	if a := cur.Load(); a != nil {
		return append([]string(nil), a.env...)
	}
	return nil
}

func envOf(p Proxy, no string) []string {
	u := p.URL()
	// remote DNS for SOCKS (curl and git read socks5h as that)
	if u.Scheme == SOCKS5 {
		u.Scheme = "socks5h"
	}
	s := u.String()
	var out []string
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"} {
		out = append(out, k+"="+s, strings.ToLower(k)+"="+s)
	}
	return append(out, "NO_PROXY="+no, "no_proxy="+no)
}

// ProxyEnvKeys are the variables Env sets.
var ProxyEnvKeys = []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"}

// ForContainer rewrites a proxy variable for a process in a container,
// where 127.0.0.1 is the container itself: this machine is
// host.docker.internal there.
func ForContainer(kv string) string {
	k, v, ok := strings.Cut(kv, "=")
	if !ok || !isProxyKey(k) || strings.EqualFold(k, "NO_PROXY") {
		return kv
	}
	u, err := url.Parse(v)
	if err != nil || u.Host == "" {
		return kv
	}
	if h := u.Hostname(); h == "localhost" || h == "::1" || strings.HasPrefix(h, "127.") {
		u.Host = net.JoinHostPort("host.docker.internal", u.Port())
		return k + "=" + u.String()
	}
	return kv
}

func isProxyKey(k string) bool {
	for _, x := range ProxyEnvKeys {
		if k == x {
			return true
		}
	}
	return false
}

// NormalizeBypass cleans a bypass list: split on commas and spaces, *.x
// written as .x (subdomains), no duplicates.
func NormalizeBypass(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' || r == ';' }) {
			f = strings.ToLower(strings.TrimSpace(f))
			if strings.HasPrefix(f, "*.") {
				f = f[1:]
			}
			if f == "" || seen[f] {
				continue
			}
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}
