package proxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/internal/netproxy"
	"github.com/hedeqiang/crewbase/internal/netproxy/proxytest"
)

func vault() func() (*credentials.Service, bool) {
	cs := credentials.New()
	cs.SetBackend("vault", credentials.NewMemory())
	return func() (*credentials.Service, bool) { return cs, true }
}

func TestSetSavesAndRoutes(t *testing.T) {
	t.Cleanup(func() { netproxy.Set(nil) })
	dir, creds := t.TempDir(), vault()
	s, err := Open(dir, creds)
	if err != nil {
		t.Fatal(err)
	}
	if st := s.Settings(); st.Enabled || st.Type != "http" || st.Port != 7890 || len(st.Bypass) == 0 {
		t.Fatalf("defaults %+v", st)
	}
	if _, on := netproxy.Current(); on {
		t.Fatal("on by default")
	}
	ctx := context.Background()
	// a whole URL pasted as the host is taken apart
	st, err := s.Set(ctx, Input{Settings: Settings{Enabled: true, Host: "socks5://bob@127.0.0.1:7891", Bypass: []string{"localhost, *.corp.example"}}, Password: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Type != "socks5" || st.Host != "127.0.0.1" || st.Port != 7891 || st.Username != "bob" || !st.HasPassword ||
		strings.Join(st.Bypass, ",") != "localhost,.corp.example" {
		t.Fatalf("saved %+v", st)
	}
	p, on := netproxy.Current()
	if !on || p.Password != "s3cret" || p.URL().String() != (&url.URL{Scheme: "socks5", User: url.UserPassword("bob", "s3cret"), Host: "127.0.0.1:7891"}).String() {
		t.Fatalf("routing %+v %v", p, on)
	}
	// the file keeps no password; the vault does
	b, _ := os.ReadFile(filepath.Join(dir, "proxy.json"))
	if strings.Contains(string(b), "s3cret") {
		t.Fatalf("password in the file: %s", b)
	}
	cs, _ := creds()
	if v, _ := cs.Resolve(ctx, PasswordRef); v != "s3cret" {
		t.Fatalf("vault %q", v)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "proxy.json")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}

	// a restart routes the same way, with the password from the vault
	netproxy.Set(nil)
	if _, err := Open(dir, creds); err != nil {
		t.Fatal(err)
	}
	if p, on := netproxy.Current(); !on || p.Password != "s3cret" {
		t.Fatalf("after reopen %+v", p)
	}

	// an empty password keeps it; clearing removes it
	st, _ = s.Set(ctx, Input{Settings: st})
	if !st.HasPassword {
		t.Fatal("empty password dropped the saved one")
	}
	st, err = s.Set(ctx, Input{Settings: Settings{Enabled: true, Type: "http", Host: "127.0.0.1", Port: 7890}, ClearPassword: true})
	if err != nil || st.HasPassword {
		t.Fatalf("clear %+v %v", st, err)
	}
	if _, err := cs.Resolve(ctx, PasswordRef); err == nil {
		t.Fatal("password still in the vault")
	}

	// off: no proxy, the setting kept for next time
	st, _ = s.Set(ctx, Input{Settings: Settings{Enabled: false, Type: "http", Host: "127.0.0.1", Port: 7890}})
	if _, on := netproxy.Current(); on || st.Host != "127.0.0.1" {
		t.Fatalf("off %+v", st)
	}
}

func TestRejectsBadSettings(t *testing.T) {
	t.Cleanup(func() { netproxy.Set(nil) })
	s, _ := Open(t.TempDir(), vault())
	for _, in := range []Input{
		{Settings: Settings{Enabled: true, Type: "ftp", Host: "127.0.0.1", Port: 21}},
		{Settings: Settings{Enabled: true, Type: "http", Host: "", Port: 7890}},
		{Settings: Settings{Enabled: true, Type: "http", Host: "127.0.0.1", Port: 0}},
		{Settings: Settings{Enabled: true, Type: "http", Host: "127.0.0.1", Port: 70000}},
		{Settings: Settings{Enabled: true, Type: "http", Host: "a/b", Port: 80}},
		{Settings: Settings{Enabled: true, Type: "http", Host: "socks5://nohostport"}},
		{Settings: Settings{Enabled: true, Type: "http", Host: "127.0.0.1", Port: 7890}, Password: "x"}, // a password without a user
	} {
		if _, err := s.Set(context.Background(), in); !errors.Is(err, ErrInput) {
			t.Errorf("%+v: %v", in, err)
		}
	}
	if _, on := netproxy.Current(); on {
		t.Fatal("a refused setting was applied")
	}
}

func TestParseScutil(t *testing.T) {
	out := []byte(`<dictionary> {
  ExceptionsList : <array> {
    0 : localhost
  }
  HTTPEnable : 1
  HTTPPort : 7890
  HTTPProxy : 127.0.0.1
  HTTPSEnable : 1
  HTTPSPort : 7890
  HTTPSProxy : 127.0.0.1
  SOCKSEnable : 1
  SOCKSPort : 7891
  SOCKSProxy : 127.0.0.1
  ProxyAutoConfigEnable : 0
}`)
	got := parseScutil(out)
	if len(got) != 3 || got[0] != (Found{Type: "http", Host: "127.0.0.1", Port: 7890, Source: "system"}) ||
		got[2] != (Found{Type: "socks5", Host: "127.0.0.1", Port: 7891, Source: "system"}) {
		t.Fatalf("%+v", got)
	}
	if parseScutil([]byte("<dictionary> {\n  HTTPEnable : 0\n}")) != nil {
		t.Fatal("disabled proxy found")
	}
}

// The test button: through a real proxy of each kind, with the right and
// the wrong login, to a closed port, and with the wrong type.
func TestProbe(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer site.Close()
	_, port, _ := net.SplitHostPort(site.Listener.Addr().String())
	target := "http://www.example.test:" + port + "/generate_204"
	hp, sp := proxytest.HTTP(t, "bob", "s3cret"), proxytest.SOCKS5(t, "bob", "s3cret")
	for _, p := range []*proxytest.Proxy{hp, sp} {
		p.Route("www.example.test:"+port, site.Listener.Addr().String())
	}
	ctx := context.Background()
	px := func(typ string, p *proxytest.Proxy, pass string) netproxy.Proxy {
		return netproxy.Proxy{Type: typ, Host: p.Host, Port: p.Port, Username: "bob", Password: pass}
	}
	for name, tc := range map[string]struct {
		p    netproxy.Proxy
		ok   bool
		code string
	}{
		"http":           {px("http", hp, "s3cret"), true, ""},
		"socks5":         {px("socks5", sp, "s3cret"), true, ""},
		"http bad login": {px("http", hp, "nope"), false, "auth"},
		"socks bad":      {px("socks5", sp, "nope"), false, "auth"},
		"closed port":    {netproxy.Proxy{Type: "http", Host: "127.0.0.1", Port: closedPort(t)}, false, "unreachable"},
		"socks as http":  {px("http", sp, "s3cret"), false, "type"},
		"http as socks5": {px("socks5", hp, "s3cret"), false, "type"},
		"http as https":  {px("https", hp, "s3cret"), false, "tls"},
	} {
		c := probe(ctx, tc.p, target)
		if c.OK != tc.ok || c.Code != tc.code {
			t.Errorf("%s: %+v", name, c)
		}
	}
	if c := probe(ctx, px("socks5", sp, "s3cret"), target); c.Status != 204 || c.Target != "www.example.test:"+port {
		t.Fatalf("%+v", c)
	}
}

func TestDetectsLocalProxies(t *testing.T) {
	hp, sp := proxytest.HTTP(t, "", ""), proxytest.SOCKS5(t, "", "")
	got := probePorts(context.Background(), "127.0.0.1", []int{hp.Port, sp.Port, closedPort(t)})
	if len(got) != 2 || got[0].Type != "http" || got[0].Port != hp.Port || got[1].Type != "socks5" || got[1].Port != sp.Port {
		t.Fatalf("%+v", got)
	}
}

func TestEnvProxyHidesLogin(t *testing.T) {
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		t.Setenv(k, "")
	}
	t.Setenv("HTTPS_PROXY", (&url.URL{Scheme: "http", User: url.UserPassword("bob", "s3cret"), Host: "proxy.corp:3128"}).String())
	if got := EnvProxy(); got != "http://proxy.corp:3128" {
		t.Fatalf("%q", got)
	}
}

func closedPort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}
