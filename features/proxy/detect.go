package proxy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/internal/netproxy"
)

// Found is a proxy already there: macOS's system proxy, a variable in
// Crewbase's environment, or a port on this machine that answers like one.
type Found struct {
	Type   string `json:"type"`
	Host   string `json:"host"`
	Port   int    `json:"port"`
	Source string `json:"source"`         // system, env, port
	Also   string `json:"also,omitempty"` // socks5: the port speaks both (a mixed port)
}

// common local proxy ports: Clash (mixed 7890, socks 7891, Verge 7897),
// Surge (6152 http, 6153 socks), v2rayN (10808 socks, 10809 http), V2rayU
// (1087 http, 1086 socks), Shadowsocks (1080), Privoxy (8118), Qv2ray /
// v2rayA (8889, 20171, 20172)
var localPorts = []int{7890, 7897, 7891, 6152, 6153, 10809, 10808, 1087, 1086, 1080, 8118, 8889, 20171, 20172}

// Detect lists proxies found, system ones first.
func Detect(ctx context.Context) []Found {
	var out []Found
	seen := map[string]bool{}
	add := func(f Found) {
		h := f.Host
		if h == "localhost" {
			h = "127.0.0.1"
		}
		k := f.Type + "|" + h + "|" + strconv.Itoa(f.Port)
		if f.Host == "" || f.Port == 0 || seen[k] {
			return
		}
		seen[k] = true
		out = append(out, f)
	}
	for _, f := range systemProxies(ctx) {
		add(f)
	}
	for _, f := range envProxies() {
		add(f)
	}
	for _, f := range probePorts(ctx, "127.0.0.1", localPorts) {
		if !seen[netproxy.HTTP+"|127.0.0.1|"+strconv.Itoa(f.Port)] && !seen[netproxy.SOCKS5+"|127.0.0.1|"+strconv.Itoa(f.Port)] {
			add(f)
		}
	}
	return out
}

// systemProxies: macOS's (System Settings › Network › Proxies, what Clash
// and Surge set when they "set the system proxy").
func systemProxies(ctx context.Context) []Found {
	if runtime.GOOS != "darwin" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, "scutil", "--proxy").Output()
	if err != nil {
		return nil
	}
	return parseScutil(b)
}

func parseScutil(b []byte) []Found {
	kv := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), " : ")
		if ok {
			kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	var out []Found
	for _, x := range []struct{ prefix, typ string }{{"HTTP", netproxy.HTTP}, {"HTTPS", netproxy.HTTP}, {"SOCKS", netproxy.SOCKS5}} {
		if kv[x.prefix+"Enable"] != "1" {
			continue
		}
		port, _ := strconv.Atoi(kv[x.prefix+"Port"])
		out = append(out, Found{Type: x.typ, Host: kv[x.prefix+"Proxy"], Port: port, Source: "system"})
	}
	return out
}

func envProxies() []Found {
	var out []Found
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		v := os.Getenv(k)
		if v == "" {
			continue
		}
		if !strings.Contains(v, "://") {
			v = "http://" + v
		}
		t, h, p, _, err := ParseURL(v)
		if err != nil {
			continue
		}
		out = append(out, Found{Type: t, Host: h, Port: p, Source: "env"})
	}
	return out
}

// EnvProxy is the proxy Crewbase's environment names (what is used while
// 网络代理 is off), without credentials; "" when none.
func EnvProxy() string {
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		if v := os.Getenv(k); v != "" {
			if u, err := url.Parse(v); err == nil && u.User != nil {
				u.User = nil
				return u.String()
			}
			return v
		}
	}
	return ""
}

func probePorts(ctx context.Context, host string, ports []int) []Found {
	res := make([]*Found, len(ports))
	var wg sync.WaitGroup
	for i, p := range ports {
		wg.Add(1)
		go func() {
			defer wg.Done()
			addr := net.JoinHostPort(host, strconv.Itoa(p))
			socks, httpOK := speaksSOCKS5(ctx, addr, quick), speaksHTTPProxy(ctx, addr, quick)
			switch {
			case httpOK && socks:
				res[i] = &Found{Type: netproxy.HTTP, Host: host, Port: p, Source: "port", Also: netproxy.SOCKS5}
			case httpOK:
				res[i] = &Found{Type: netproxy.HTTP, Host: host, Port: p, Source: "port"}
			case socks:
				res[i] = &Found{Type: netproxy.SOCKS5, Host: host, Port: p, Source: "port"}
			}
		}()
	}
	wg.Wait()
	var out []Found
	for _, f := range res {
		if f != nil {
			out = append(out, *f)
		}
	}
	return out
}

// quick: local ports; patient: a proxy that may be far away
const (
	quick   = 800 * time.Millisecond
	patient = 3 * time.Second
)

func dialShort(ctx context.Context, addr string, wait time.Duration) (net.Conn, error) {
	d := net.Dialer{Timeout: wait / 2}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	_ = c.SetDeadline(time.Now().Add(wait))
	return c, nil
}

// speaksSOCKS5: it answers a SOCKS5 greeting (no authentication offered).
func speaksSOCKS5(ctx context.Context, addr string, wait time.Duration) bool {
	c, err := dialShort(ctx, addr, wait)
	if err != nil {
		return false
	}
	defer c.Close()
	if _, err := c.Write([]byte{5, 1, 0}); err != nil {
		return false
	}
	b := make([]byte, 2)
	n, _ := c.Read(b)
	return n == 2 && b[0] == 5
}

// speaksHTTPProxy: it answers a CONNECT (to a closed port on this machine,
// so nothing is reached) with an HTTP status line.
func speaksHTTPProxy(ctx context.Context, addr string, wait time.Duration) bool {
	c, err := dialShort(ctx, addr, wait)
	if err != nil {
		return false
	}
	defer c.Close()
	if _, err := c.Write([]byte("CONNECT 127.0.0.1:9 HTTP/1.1\r\nHost: 127.0.0.1:9\r\n\r\n")); err != nil {
		return false
	}
	b := make([]byte, 5)
	n, _ := c.Read(b)
	return n == 5 && string(b) == "HTTP/"
}

// TestTarget is what a test reaches through the proxy: a tiny answer
// (204), and a site that typically needs one where a proxy is needed.
const TestTarget = "https://www.google.com/generate_204"

// Check is the result of a test.
type Check struct {
	OK     bool   `json:"ok"`
	MS     int    `json:"ms"`
	Status int    `json:"status,omitempty"`
	Code   string `json:"code,omitempty"` // unreachable, auth, timeout, tls, type, status, other
	Error  string `json:"error,omitempty"`
	Target string `json:"target"`
}

// Test reaches TestTarget through the proxy described by in (not saved;
// an empty password uses the saved one).
func (s *Service) Test(ctx context.Context, in Input) (Check, error) {
	in.Enabled = true
	st, err := clean(in.Settings)
	if err != nil {
		return Check{}, err
	}
	if in.Password == "" && !in.ClearPassword {
		st.HasPassword = s.Settings().HasPassword
	}
	p := s.proxyOf(ctx, st, in.Password)
	return probe(ctx, p, TestTarget), nil
}

func probe(ctx context.Context, p netproxy.Proxy, target string) Check {
	host := target
	if u, err := url.Parse(target); err == nil {
		host = u.Host
	}
	// an HTTP proxy given as SOCKS5 waits for a request line that never
	// comes: say so now rather than after the timeout
	// (and the other way round)
	switch addr := p.URL().Host; p.Type {
	case netproxy.SOCKS5:
		if !speaksSOCKS5(ctx, addr, patient) && speaksHTTPProxy(ctx, addr, patient) {
			return Check{Target: host, Code: "type", Error: "this is an HTTP proxy, not SOCKS5"}
		}
	case netproxy.HTTP:
		if !speaksHTTPProxy(ctx, addr, patient) && speaksSOCKS5(ctx, addr, patient) {
			return Check{Target: host, Code: "type", Error: "this is a SOCKS5 proxy, not HTTP"}
		}
	}
	tr := &http.Transport{
		Proxy:                 http.ProxyURL(p.URL()),
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		DisableKeepAlives:     true,
	}
	defer tr.CloseIdleConnections()
	cl := &http.Client{Transport: tr, Timeout: 12 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	out := Check{Target: host}
	start := time.Now()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	resp, err := cl.Do(req)
	out.MS = int(time.Since(start) / time.Millisecond)
	if err != nil {
		out.Code, out.Error = classify(err), err.Error()
		return out
	}
	resp.Body.Close()
	out.Status = resp.StatusCode
	switch {
	case resp.StatusCode == http.StatusProxyAuthRequired:
		out.Code = "auth"
	case resp.StatusCode < 400:
		out.OK = true
	default:
		out.Code = "status"
	}
	return out
}

func classify(err error) string {
	var ne net.Error
	msg := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return "timeout"
	case strings.Contains(msg, "407") || strings.Contains(msg, "proxy authentication") || strings.Contains(msg, "authentication failed") ||
		strings.Contains(msg, "username/password") || strings.Contains(msg, "no acceptable authentication"):
		return "auth"
	case strings.Contains(msg, "connection refused") || strings.Contains(msg, "no such host") || strings.Contains(msg, "network is unreachable") ||
		strings.Contains(msg, "no route to host"):
		return "unreachable"
	case strings.Contains(msg, "first record does not look like a tls handshake") || strings.Contains(msg, "x509") || strings.Contains(msg, "tls:"):
		return "tls"
	case strings.Contains(msg, "malformed http") || strings.Contains(msg, "protocol version") || strings.Contains(msg, "unexpected eof") ||
		strings.Contains(msg, "connection reset") || strings.Contains(msg, "eof"):
		return "type"
	}
	return "other"
}
