package linkpreview

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Embed is a player the UI can put in an iframe in place of the card.
type Embed struct {
	Provider string  `json:"provider"`
	Src      string  `json:"src"`
	Aspect   float64 `json:"aspect"`
	// Thumb is a thumbnail known without fetching the page (YouTube).
	Thumb string `json:"-"`
	// id: the video id (YouTube), or bvid=… / aid=… (Bilibili)
	id string
}

const wide = 16.0 / 9.0

var (
	ytID      = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	bvID      = regexp.MustCompile(`^(?i:BV)[0-9A-Za-z]{10}$`)
	avID      = regexp.MustCompile(`^(?i:av)([0-9]{1,12})$`)
	vimeoID   = regexp.MustCompile(`^[0-9]{1,12}$`)
	vimeoHash = regexp.MustCompile(`^[0-9a-f]{6,20}$`)
	ytTime    = regexp.MustCompile(`^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s?)?$`)
)

// embedFor maps a video page URL to its player, without the network.
// Returns nil for anything else.
func embedFor(u *url.URL) *Embed {
	if u == nil {
		return nil
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	host = strings.TrimPrefix(host, "m.")
	seg := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	switch host {
	case "youtube.com", "music.youtube.com", "youtube-nocookie.com":
		var id string
		switch {
		case len(seg) == 1 && seg[0] == "watch":
			id = u.Query().Get("v")
		case len(seg) >= 2 && (seg[0] == "shorts" || seg[0] == "embed" || seg[0] == "live" || seg[0] == "v"):
			id = seg[1]
		}
		return youtube(id, u.Query())
	case "youtu.be":
		if len(seg) >= 1 {
			return youtube(seg[0], u.Query())
		}
	case "bilibili.com":
		if len(seg) >= 2 && seg[0] == "video" {
			q := url.Values{}
			if bvID.MatchString(seg[1]) {
				q.Set("bvid", seg[1])
			} else if m := avID.FindStringSubmatch(seg[1]); m != nil {
				q.Set("aid", m[1])
			} else {
				return nil
			}
			ref := q.Encode()
			if p, err := strconv.Atoi(u.Query().Get("p")); err == nil && p > 1 {
				q.Set("page", strconv.Itoa(p))
			}
			q.Set("autoplay", "0")
			return &Embed{Provider: "bilibili", Src: "https://player.bilibili.com/player.html?" + q.Encode(), Aspect: wide, id: ref}
		}
	case "vimeo.com", "player.vimeo.com":
		// vimeo.com/123, vimeo.com/123/<hash> (unlisted),
		// vimeo.com/channels/x/123, player.vimeo.com/video/123
		for i, s := range seg {
			if !vimeoID.MatchString(s) {
				continue
			}
			src := "https://player.vimeo.com/video/" + s
			if h := u.Query().Get("h"); h != "" {
				src += "?h=" + url.QueryEscape(h)
			} else if i+1 < len(seg) && vimeoHash.MatchString(seg[i+1]) {
				src += "?h=" + seg[i+1]
			}
			return &Embed{Provider: "vimeo", Src: src, Aspect: wide}
		}
	}
	return nil
}

func youtube(id string, q url.Values) *Embed {
	if !ytID.MatchString(id) {
		return nil
	}
	src := "https://www.youtube-nocookie.com/embed/" + id
	if s := ytStart(q.Get("t")); s > 0 {
		src += "?start=" + strconv.Itoa(s)
	} else if s := ytStart(q.Get("start")); s > 0 {
		src += "?start=" + strconv.Itoa(s)
	}
	return &Embed{Provider: "youtube", Src: src, Aspect: wide, id: id, Thumb: "https://i.ytimg.com/vi/" + id + "/hqdefault.jpg"}
}

// ytStart reads t=90, t=90s or t=1m30s.
func ytStart(t string) int {
	if t == "" {
		return 0
	}
	m := ytTime.FindStringSubmatch(t)
	if m == nil {
		return 0
	}
	n := func(s string) int { v, _ := strconv.Atoi(s); return v }
	return n(m[1])*3600 + n(m[2])*60 + n(m[3])
}
