package netproxy_test

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/internal/netproxy"
	"github.com/hedeqiang/crewbase/internal/netproxy/proxytest"
)

func route(t *testing.T, raw string) string {
	t.Helper()
	u, _ := url.Parse(raw)
	p, err := netproxy.Func(&http.Request{URL: u})
	if err != nil {
		t.Fatal(err)
	}
	if p == nil {
		return "direct"
	}
	return p.String()
}

func TestRoutesBySetting(t *testing.T) {
	t.Cleanup(func() { netproxy.Set(nil) })
	netproxy.Set(&netproxy.Proxy{Type: netproxy.SOCKS5, Host: "127.0.0.1", Port: 7891, Username: "bob", Password: "p@ss",
		Bypass: append(slices.Clone(netproxy.DefaultBypass), "*.corp.example", "git.internal:8443")})
	via := (&url.URL{Scheme: "socks5", User: url.UserPassword("bob", "p@ss"), Host: "127.0.0.1:7891"}).String()
	for raw, want := range map[string]string{
		"https://api.anthropic.com/v1/messages": via,
		"http://registry.npmjs.org/":            via,
		"http://localhost:11434/api/tags":       "direct", // Ollama
		"http://127.0.0.1:7788/":                "direct",
		"http://[::1]:8080/":                    "direct",
		"http://192.168.1.20/":                  "direct",
		"http://10.2.3.4:9000/":                 "direct",
		"https://printer.local/":                "direct",
		"https://gitlab.corp.example/":          "direct", // *.corp.example → .corp.example
		"https://corp.example/":                 via,      // only subdomains
		"https://git.internal:8443/":            "direct",
		"https://git.internal/":                 via,
	} {
		if got := route(t, raw); got != want {
			t.Errorf("%s: %s, want %s", raw, got, want)
		}
	}
	// the default transport follows it too
	u, _ := url.Parse("https://api.openai.com/v1")
	if p, _ := http.DefaultTransport.(*http.Transport).Proxy(&http.Request{URL: u}); p == nil || p.String() != via {
		t.Fatalf("default transport: %v", p)
	}
	if !netproxy.IsProxyAddr("127.0.0.1:7891") || netproxy.IsProxyAddr("127.0.0.1:7890") {
		t.Fatal("IsProxyAddr")
	}

	netproxy.Set(nil)
	if p, _ := netproxy.Configured(&http.Request{URL: u}); p != nil {
		t.Fatalf("still proxied after off: %v", p)
	}
	if netproxy.Env() != nil || netproxy.IsProxyAddr("127.0.0.1:7891") {
		t.Fatal("env or address survived off")
	}
}

func TestEnvForCommands(t *testing.T) {
	t.Cleanup(func() { netproxy.Set(nil) })
	netproxy.Set(&netproxy.Proxy{Type: netproxy.SOCKS5, Host: "127.0.0.1", Port: 1080, Bypass: []string{"localhost", ".local"}})
	env := strings.Join(netproxy.Env(), "\n")
	for _, want := range []string{"HTTPS_PROXY=socks5h://127.0.0.1:1080", "https_proxy=socks5h://127.0.0.1:1080", "ALL_PROXY=socks5h://127.0.0.1:1080",
		"HTTP_PROXY=socks5h://127.0.0.1:1080", "NO_PROXY=localhost,.local", "no_proxy=localhost,.local"} {
		if !strings.Contains(env, want+"\n") && !strings.HasSuffix(env, want) {
			t.Errorf("missing %s in\n%s", want, env)
		}
	}
	netproxy.Set(&netproxy.Proxy{Type: netproxy.HTTP, Host: "proxy.example", Port: 3128})
	if e := netproxy.Env(); !slices.Contains(e, "HTTPS_PROXY=http://proxy.example:3128") {
		t.Fatalf("http env %v", e)
	}
}

func TestForContainer(t *testing.T) {
	for in, want := range map[string]string{
		"HTTPS_PROXY=http://127.0.0.1:7890":    "HTTPS_PROXY=http://host.docker.internal:7890",
		"all_proxy=socks5h://localhost:7891":   "all_proxy=socks5h://host.docker.internal:7891",
		"HTTP_PROXY=http://u:p@127.0.0.1:7890": "HTTP_PROXY=http://u:p@host.docker.internal:7890",
		"HTTPS_PROXY=http://proxy.corp:3128":   "HTTPS_PROXY=http://proxy.corp:3128",
		"NO_PROXY=localhost,127.0.0.1":         "NO_PROXY=localhost,127.0.0.1",
		"API_BASE=http://127.0.0.1:7890":       "API_BASE=http://127.0.0.1:7890",
	} {
		if got := netproxy.ForContainer(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

func TestNormalizeBypass(t *testing.T) {
	got := netproxy.NormalizeBypass([]string{" *.Corp.Example, localhost\n10.0.0.0/8 ;localhost", ""})
	if want := []string{".corp.example", "localhost", "10.0.0.0/8"}; !slices.Equal(got, want) {
		t.Fatalf("%v, want %v", got, want)
	}
}

// Real requests through each kind of proxy, with a login, on the default
// client (as the model SDKs and everything else use it).
func TestRequestsGoThroughTheProxy(t *testing.T) {
	t.Cleanup(func() { netproxy.Set(nil) })
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "hello from "+r.Host) }))
	defer site.Close()
	_, port, _ := net.SplitHostPort(site.Listener.Addr().String())
	// a name that only the proxy knows the way to
	target := "api.example.test:" + port
	for _, tc := range []struct {
		typ string
		p   *proxytest.Proxy
	}{{netproxy.HTTP, proxytest.HTTP(t, "bob", "s3cret")}, {netproxy.SOCKS5, proxytest.SOCKS5(t, "bob", "s3cret")}} {
		tc.p.Route(target, site.Listener.Addr().String())
		netproxy.Set(&netproxy.Proxy{Type: tc.typ, Host: tc.p.Host, Port: tc.p.Port, Username: "bob", Password: "s3cret"})
		resp, err := http.Get("http://" + target + "/")
		if err != nil {
			t.Fatalf("%s: %v", tc.typ, err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(b) != "hello from "+target || tc.p.Hits.Load() != 1 {
			t.Fatalf("%s: %q, hits %d", tc.typ, b, tc.p.Hits.Load())
		}
		// a wrong password is refused by the proxy
		netproxy.Set(&netproxy.Proxy{Type: tc.typ, Host: tc.p.Host, Port: tc.p.Port, Username: "bob", Password: "nope"})
		resp, err = http.Get("http://" + target + "/")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode != http.StatusProxyAuthRequired {
				t.Fatalf("%s: wrong password got %d", tc.typ, resp.StatusCode)
			}
		}
	}
	// local addresses never go through it
	p := proxytest.HTTP(t, "", "")
	netproxy.Set(&netproxy.Proxy{Type: netproxy.HTTP, Host: p.Host, Port: p.Port, Bypass: netproxy.DefaultBypass})
	resp, err := http.Get(site.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if p.Hits.Load() != 0 {
		t.Fatal("a loopback request went through the proxy")
	}
}
