package netguard_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/internal/netguard"
	"github.com/hedeqiang/crewbase/internal/netproxy"
	"github.com/hedeqiang/crewbase/internal/netproxy/proxytest"
)

// With a proxy set, a guarded request (a link preview, a webhook delivery)
// still has its site checked here, then goes through the proxy; a site at
// an allowed private address is reached directly.
func TestGuardedClientThroughProxy(t *testing.T) {
	t.Cleanup(func() { netproxy.Set(nil) })
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "site") }))
	defer site.Close()
	_, port, _ := net.SplitHostPort(site.Listener.Addr().String())
	p := proxytest.HTTP(t, "", "")
	p.Route("news.example.test:"+port, site.Listener.Addr().String())
	netproxy.Set(&netproxy.Proxy{Type: netproxy.HTTP, Host: p.Host, Port: p.Port})

	names := map[string]string{"news.example.test": "93.184.216.34", "evil.example.test": "10.0.0.5"}
	g := &netguard.Guard{
		Allow: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
		Lookup: func(_ context.Context, h string) ([]netip.Addr, error) {
			if ip, ok := names[h]; ok {
				return []netip.Addr{netip.MustParseAddr(ip)}, nil
			}
			return nil, errors.New("no such host")
		},
	}
	c := g.Client(3*time.Second, 2)

	// a public site: through the proxy (whose 127.0.0.1 address is not refused)
	resp, err := c.Get("http://news.example.test:" + port + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "site" || resp.Header.Get("X-Test-Proxy") != "http" || p.Hits.Load() != 1 {
		t.Fatalf("public: %q via %q, hits %d", b, resp.Header.Get("X-Test-Proxy"), p.Hits.Load())
	}

	// a name pointing inside: refused before anything is sent to the proxy
	if _, err := c.Get("http://evil.example.test:" + port + "/"); !errors.Is(err, netguard.ErrBlocked) {
		t.Fatalf("private site: %v", err)
	}
	if p.Hits.Load() != 1 {
		t.Fatal("a refused site reached the proxy")
	}

	// an allowed private address: straight there, not through the proxy
	resp, err = c.Get(site.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("X-Test-Proxy") != "" || p.Hits.Load() != 1 {
		t.Fatal("an allowed private site went through the proxy")
	}

	// with no proxy set, as before: dialled directly after the check
	netproxy.Set(nil)
	if _, err := c.Get("http://news.example.test:" + port + "/"); err == nil {
		t.Fatal("reached a name that only the proxy knows the way to")
	}
}
