// Package linkpreview turns links in messages into cards (docs/previews.md,
// part 一): GET /api/unfurl?url= fetches the page's Open Graph data, maps
// video sites to their players, and passes media links through. Only public
// addresses are fetched: every IP is checked when it is dialled (so DNS
// rebinding and redirects to the LAN are refused too).
package linkpreview

import (
	"github.com/hedeqiang/crewbase/internal/netguard"
	"github.com/hedeqiang/crewbase/internal/netproxy"

	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// Card is what the UI draws for a link.
type Card struct {
	URL         string `json:"url"`
	Kind        string `json:"kind"` // page | video | image | audio
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	SiteName    string `json:"siteName,omitempty"`
	Image       string `json:"image,omitempty"`
	Icon        string `json:"icon,omitempty"`
	Embed       *Embed `json:"embed,omitempty"`
	Media       string `json:"media,omitempty"`
}

const (
	fetchTimeout = 5 * time.Second
	maxRedirects = 5
	maxBody      = 512 << 10
	okTTL        = time.Hour
	failTTL      = 10 * time.Minute
	maxEntries   = 500
	userAgent    = "Mozilla/5.0 (compatible; CrewbaseBot/1.0; +https://github.com/hedeqiang/crewbase)"
)

// ErrBlocked: the address is not a public one.
var ErrBlocked = errors.New("address is not public")

// Service fetches and caches cards.
type Service struct {
	client  *http.Client
	timeout time.Duration
	maxBody int64
	now     func() time.Time
	// lookup resolves a host name (tests swap it); blocked decides which
	// addresses may not be dialled.
	lookup  func(ctx context.Context, host string) ([]netip.Addr, error)
	blocked func(netip.Addr) bool

	mu     sync.Mutex
	cache  map[string]*list.Element
	order  *list.List // front = most recently used
	flight map[string]*call
}

type entry struct {
	key  string
	card *Card
	err  error
	exp  time.Time
}

type call struct {
	done chan struct{}
	card *Card
	err  error
}

// New returns a service with the default limits.
func New() *Service {
	s := &Service{
		timeout: fetchTimeout,
		maxBody: maxBody,
		now:     time.Now,
		blocked: Blocked,
		cache:   map[string]*list.Element{},
		order:   list.New(),
		flight:  map[string]*call{},
	}
	s.lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}
	dialer := &net.Dialer{Timeout: fetchTimeout, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		// with a proxy set (Settings › 网络代理) the site is checked before
		// the request goes to the proxy, whose own address is not the site's
		Proxy: func(req *http.Request) (*url.URL, error) {
			p, err := netproxy.Configured(req)
			if err != nil || p == nil {
				return nil, err
			}
			if _, err := s.resolve(req.Context(), req.URL.Hostname()); err != nil {
				return nil, err
			}
			return p, nil
		},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if netproxy.IsProxyAddr(addr) {
				return dialer.DialContext(ctx, network, addr)
			}
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := s.resolve(ctx, host)
			if err != nil {
				return nil, err
			}
			var last error
			for _, ip := range ips {
				// dial the very address that was checked
				c, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return c, nil
				}
				last = err
			}
			return nil, last
		},
		TLSHandshakeTimeout:   fetchTimeout,
		ResponseHeaderTimeout: fetchTimeout,
		MaxIdleConns:          20,
		IdleConnTimeout:       60 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	s.client = &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return fmt.Errorf("more than %d redirects", maxRedirects)
			}
			return checkURL(req.URL)
		},
	}
	return s
}

// resolve returns the host's addresses, refusing the lot when any one of
// them is not public.
func (s *Service) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if localHost(host) {
		return nil, ErrBlocked
	}
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip}
	} else {
		ips, err = s.lookup(ctx, host)
		if err != nil {
			return nil, err
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no address for %s", host)
	}
	for _, ip := range ips {
		if s.blocked(ip) {
			return nil, fmt.Errorf("%s: %w", host, ErrBlocked)
		}
	}
	return ips, nil
}

// Blocked reports whether ip is not a public unicast address (netguard).
func Blocked(ip netip.Addr) bool { return netguard.Blocked(ip) }

// localHost: names that mean this machine or the LAN, refused before any
// lookup (the 预览 panel handles localhost links).
func localHost(h string) bool { return netguard.LocalName(h) }

func checkURL(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("only http and https links")
	}
	if u.Hostname() == "" {
		return errors.New("no host")
	}
	if u.User != nil {
		return errors.New("links with credentials are not fetched")
	}
	if localHost(u.Hostname()) {
		return ErrBlocked
	}
	return nil
}

// Parse validates a link for unfurling.
func Parse(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if err := checkURL(u); err != nil {
		return nil, err
	}
	u.Fragment = ""
	u.RawFragment = ""
	return u, nil
}

// Unfurl returns the card for raw, from the cache when it is fresh. The
// same address is fetched once however many ask at the same time.
func (s *Service) Unfurl(ctx context.Context, raw string) (*Card, error) {
	u, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	key := u.String()
	s.mu.Lock()
	if el, ok := s.cache[key]; ok {
		e := el.Value.(*entry)
		if s.now().Before(e.exp) {
			s.order.MoveToFront(el)
			s.mu.Unlock()
			return e.card, e.err
		}
		s.order.Remove(el)
		delete(s.cache, key)
	}
	c, ok := s.flight[key]
	if !ok {
		c = &call{done: make(chan struct{})}
		s.flight[key] = c
		go func() {
			// not tied to the first caller: others may be waiting
			fctx, cancel := context.WithTimeout(context.Background(), s.timeout)
			card, err := s.fetch(fctx, u)
			cancel()
			c.card, c.err = card, err
			s.mu.Lock()
			delete(s.flight, key)
			s.put(key, card, err)
			s.mu.Unlock()
			close(c.done)
		}()
	}
	s.mu.Unlock()
	select {
	case <-c.done:
		// a caller that gave up is told so even when the fetch finished in
		// the meantime (select picks either when both are ready)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return c.card, c.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// put stores a result; s.mu is held.
func (s *Service) put(key string, card *Card, err error) {
	ttl := okTTL
	if err != nil {
		ttl = failTTL
	}
	s.cache[key] = s.order.PushFront(&entry{key: key, card: card, err: err, exp: s.now().Add(ttl)})
	for s.order.Len() > maxEntries {
		el := s.order.Back()
		s.order.Remove(el)
		delete(s.cache, el.Value.(*entry).key)
	}
}

// fetch builds the card. A video site's player is known from its URL, so a
// page that cannot be fetched still gets a playable card.
func (s *Service) fetch(ctx context.Context, u *url.URL) (*Card, error) {
	if emb := embedFor(u); emb != nil {
		return s.video(ctx, u, emb), nil
	}
	card, err := s.get(ctx, u)
	if err != nil {
		return nil, err
	}
	if fu, err := url.Parse(card.URL); err == nil {
		if emb := embedFor(fu); emb != nil && card.Kind == "page" {
			return s.video(ctx, fu, emb), nil // a short link (b23.tv…) that landed on one
		}
		if card.SiteName == "" {
			card.SiteName = host(fu)
		}
	}
	return card, nil
}

// Where video sites describe a video. Their pages are slow (YouTube) or
// answer robots with a captcha (Bilibili), so their APIs are asked instead.
var (
	youtubeOEmbed = "https://www.youtube.com/oembed"
	vimeoOEmbed   = "https://vimeo.com/api/oembed.json"
	bilibiliView  = "https://api.bilibili.com/x/web-interface/view"
)

// video is the card of a video site link: the player always, the title
// and thumbnail when the site's API answers.
func (s *Service) video(ctx context.Context, u *url.URL, emb *Embed) *Card {
	card := &Card{URL: u.String(), Kind: "video", SiteName: siteName(emb.Provider), Image: emb.Thumb, Embed: emb}
	var title, desc, image string
	switch emb.Provider {
	case "youtube", "vimeo":
		var o struct {
			Title       string `json:"title"`
			Author      string `json:"author_name"`
			Description string `json:"description"`
			Thumb       string `json:"thumbnail_url"`
		}
		q := youtubeOEmbed + "?format=json&url=" + url.QueryEscape("https://www.youtube.com/watch?v="+emb.id)
		if emb.Provider == "vimeo" {
			q = vimeoOEmbed + "?url=" + url.QueryEscape(u.String())
		}
		if s.getJSON(ctx, q, &o) == nil {
			title, desc, image = o.Title, o.Description, o.Thumb
			if desc == "" {
				desc = o.Author
			}
		}
	case "bilibili":
		var o struct {
			Code int `json:"code"`
			Data struct {
				Title string `json:"title"`
				Pic   string `json:"pic"`
				Desc  string `json:"desc"`
				Owner struct {
					Name string `json:"name"`
				} `json:"owner"`
			} `json:"data"`
		}
		if s.getJSON(ctx, bilibiliView+"?"+emb.id, &o) == nil && o.Code == 0 {
			title, desc, image = o.Data.Title, o.Data.Desc, o.Data.Pic
			if desc == "" || desc == "-" {
				desc = o.Data.Owner.Name
			}
			if strings.HasPrefix(image, "http://") {
				image = "https://" + image[len("http://"):]
			}
		}
	}
	card.Title, card.Description = clip(title, 300), clip(desc, 500)
	if r := resolve(u, image); r != "" {
		card.Image = r
	}
	return card
}

func (s *Service) getJSON(ctx context.Context, q string, v any) error {
	u, err := Parse(q)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	res, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return fmt.Errorf("%s: HTTP %d", u.Host, res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, s.maxBody)).Decode(v)
}

func siteName(provider string) string {
	switch provider {
	case "youtube":
		return "YouTube"
	case "bilibili":
		return "哔哩哔哩"
	case "vimeo":
		return "Vimeo"
	}
	return provider
}

func (s *Service) get(ctx context.Context, u *url.URL) (*Card, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	res, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("%s: HTTP %d", u.Host, res.StatusCode)
	}
	final := res.Request.URL
	card := &Card{URL: final.String(), Kind: "page"}
	ct := res.Header.Get("Content-Type")
	mt, _, _ := mime.ParseMediaType(ct)
	switch {
	case strings.HasPrefix(mt, "video/"), strings.HasPrefix(mt, "audio/"), strings.HasPrefix(mt, "image/"):
		card.Kind = mt[:strings.IndexByte(mt, '/')]
		card.Media = card.URL
		card.Title = path.Base(final.Path)
		if card.Title == "/" || card.Title == "." {
			card.Title = ""
		}
		card.SiteName = host(final)
		return card, nil
	case mt == "text/html" || mt == "application/xhtml+xml" || mt == "":
	default:
		return nil, fmt.Errorf("unsupported content type %q", mt)
	}
	body := io.LimitReader(res.Body, s.maxBody)
	m := parseHTML(body, ct, final)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	card.Title, card.Description, card.SiteName, card.Image, card.Icon = m.title, m.description, m.siteName, m.image, m.icon
	if card.Title == "" && card.Description == "" && card.Image == "" {
		return nil, errors.New("the page has no title")
	}
	return card, nil
}

func host(u *url.URL) string { return strings.TrimPrefix(u.Hostname(), "www.") }

// Plugin registers GET /api/unfurl.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/linkpreview",
	Inject:      []string{gateway.ServiceKey},
	Description: "Link cards: titles, thumbnails and video players for links in messages (public addresses only).",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		s := New()
		c.Defer(s.client.CloseIdleConnections)
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/unfurl", Devices: []string{"mobile"},
			Validate: func(r *gateway.Request) error {
				if _, err := Parse(r.HTTP.URL.Query().Get("url")); err != nil {
					return gateway.BadRequest("%v", err)
				}
				return nil
			},
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				card, err := s.Unfurl(ctx, r.HTTP.URL.Query().Get("url"))
				if err != nil {
					return nil, &gateway.Error{Status: http.StatusUnprocessableEntity, Code: "unfurl_failed", Message: err.Error()}
				}
				return card, nil
			}})
		return nil
	},
})
