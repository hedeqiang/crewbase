package linkpreview

import (
	"io"
	"net/url"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

// meta is what a page says about itself.
type meta struct {
	title, description, siteName, image, icon, video string
}

// parseHTML reads the <head> of a page (body is already size-limited):
// og:*, twitter:*, <title>, <meta name=description>, <link rel=icon>.
// Relative URLs resolve against base (and <base href>).
func parseHTML(body io.Reader, contentType string, base *url.URL) meta {
	if r, err := charset.NewReader(body, contentType); err == nil {
		body = r
	}
	z := html.NewTokenizer(body)
	props := map[string]string{}
	var title, iconHref, touchHref string
	inTitle := false
loop:
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			break loop
		case html.TextToken:
			if inTitle && title == "" {
				title = string(z.Text())
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "title":
				inTitle = false
			case "head":
				break loop
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			attrs := map[string]string{}
			for hasAttr {
				var k, v []byte
				k, v, hasAttr = z.TagAttr()
				attrs[string(k)] = string(v)
			}
			switch string(name) {
			case "title":
				inTitle = tt == html.StartTagToken
			case "body":
				break loop
			case "base":
				if h := attrs["href"]; h != "" {
					if b, err := base.Parse(strings.TrimSpace(h)); err == nil {
						base = b
					}
				}
			case "meta":
				key := strings.ToLower(strings.TrimSpace(attrs["property"]))
				if key == "" {
					key = strings.ToLower(strings.TrimSpace(attrs["name"]))
				}
				if key == "" {
					key = strings.ToLower(strings.TrimSpace(attrs["itemprop"]))
				}
				if v := strings.TrimSpace(attrs["content"]); key != "" && v != "" {
					if _, ok := props[key]; !ok {
						props[key] = v
					}
				}
			case "link":
				rel := " " + strings.ToLower(attrs["rel"]) + " "
				h := strings.TrimSpace(attrs["href"])
				if h == "" {
					break
				}
				if strings.Contains(rel, " icon ") && iconHref == "" {
					iconHref = h
				} else if strings.Contains(rel, " apple-touch-icon ") && touchHref == "" {
					touchHref = h
				}
			}
		}
	}
	first := func(keys ...string) string {
		for _, k := range keys {
			if v := props[k]; v != "" {
				return v
			}
		}
		return ""
	}
	m := meta{
		title:       clip(first("og:title", "twitter:title"), 300),
		description: clip(first("og:description", "twitter:description", "description"), 500),
		siteName:    clip(first("og:site_name", "application-name"), 100),
		image:       resolve(base, first("og:image:secure_url", "og:image", "og:image:url", "twitter:image", "twitter:image:src", "image")),
		video:       resolve(base, first("og:video:secure_url", "og:video:url", "og:video")),
	}
	if m.title == "" {
		m.title = clip(title, 300)
	}
	if iconHref == "" {
		iconHref = touchHref
	}
	if iconHref == "" {
		iconHref = "/favicon.ico"
	}
	m.icon = resolve(base, iconHref)
	return m
}

// resolve makes ref absolute; only http(s) URLs to public-looking hosts
// survive (the browser loads them, so no LAN addresses).
func resolve(base *url.URL, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	u, err := base.Parse(ref)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || localHost(u.Hostname()) {
		return ""
	}
	return u.String()
}

// clip collapses whitespace and cuts to n runes.
func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n-1])) + "…"
}
