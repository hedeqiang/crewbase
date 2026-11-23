// Package netguard keeps outbound HTTP requests Crewbase makes on someone
// else's say-so (link previews, webhook deliveries) away from this machine,
// its network and cloud metadata services: names are resolved first, every
// address is checked, and the connection goes to the very address that was
// checked (no second lookup an attacker could answer differently).
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// ErrBlocked: the address is not a public one (and not allowed).
var ErrBlocked = errors.New("address is not public")

// nonPublic: ranges beyond what netip already knows. 198.18.0.0/15 is
// left out on purpose: proxies in fake-IP / TUN mode (Clash, Surge…)
// answer every name with an address there and resolve the name themselves.
var nonPublic = func() []netip.Prefix {
	var out []netip.Prefix
	for _, p := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
		"172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16",
		"198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
		"::/128", "::1/128", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001:db8::/32",
		"fc00::/7", "fe80::/10", "fec0::/10", "ff00::/8",
	} {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}()

// Blocked reports whether ip is loopback, private, link-local (cloud
// metadata lives there), multicast, unspecified or otherwise not a public
// unicast address.
func Blocked(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, p := range nonPublic {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// LocalName reports names that mean this machine or the LAN, refused
// before any lookup.
func LocalName(h string) bool {
	h = strings.TrimSuffix(strings.ToLower(h), ".")
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".internal") {
		return true
	}
	if ip, err := netip.ParseAddr(strings.Trim(h, "[]")); err == nil {
		return Blocked(ip)
	}
	return false
}

// Guard resolves and checks addresses. Allow lets chosen ranges through
// (an admin's private network); loopback and link-local are never allowed
// unless listed exactly.
type Guard struct {
	Allow  []netip.Prefix
	Lookup func(ctx context.Context, host string) ([]netip.Addr, error)
}

func (g *Guard) allowed(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, p := range g.Allow {
		if p.Contains(ip) {
			return true
		}
	}
	return !Blocked(ip)
}

// Resolve returns host's addresses, refusing them all when any one is not
// allowed (a name with a public and a private address is refused).
func (g *Guard) Resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	h := strings.Trim(host, "[]")
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(h); err == nil {
		ips = []netip.Addr{ip}
	} else {
		lower := strings.TrimSuffix(strings.ToLower(h), ".")
		if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
			return nil, fmt.Errorf("%s: %w", host, ErrBlocked)
		}
		look := g.Lookup
		if look == nil {
			look = func(ctx context.Context, host string) ([]netip.Addr, error) {
				return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			}
		}
		ips, err = look(ctx, h)
		if err != nil {
			return nil, err
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no address for %s", host)
	}
	for _, ip := range ips {
		if !g.allowed(ip) {
			return nil, fmt.Errorf("%s (%s): %w", host, ip, ErrBlocked)
		}
	}
	return ips, nil
}

// Client is an HTTP client whose connections only reach allowed addresses,
// never through a proxy (a proxy's address is not the site's).
func (g *Guard) Client(timeout time.Duration, maxRedirects int) *http.Client {
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := g.Resolve(ctx, host)
			if err != nil {
				return nil, err
			}
			var last error
			for _, ip := range ips {
				c, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return c, nil
				}
				last = err
			}
			return nil, last
		},
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		MaxIdleConns:          20,
		IdleConnTimeout:       60 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   timeout * 2,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return fmt.Errorf("more than %d redirects", maxRedirects)
			}
			if req.URL.Scheme != "https" && req.URL.Scheme != "http" {
				return fmt.Errorf("redirect to %s", req.URL.Scheme)
			}
			return nil
		},
	}
}

// ParsePrefixes reads "10.0.0.0/8, 192.168.1.5" (a bare address is /32 or /128).
func ParsePrefixes(xs []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, x := range xs {
		x = strings.TrimSpace(x)
		if x == "" {
			continue
		}
		if !strings.Contains(x, "/") {
			ip, err := netip.ParseAddr(x)
			if err != nil {
				return nil, fmt.Errorf("%q is not an address or range", x)
			}
			out = append(out, netip.PrefixFrom(ip, ip.BitLen()))
			continue
		}
		p, err := netip.ParsePrefix(x)
		if err != nil {
			return nil, fmt.Errorf("%q is not an address or range", x)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}
