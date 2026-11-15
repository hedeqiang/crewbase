package console

// Folder browser for the composer's "choose folder" when there is no
// native picker (the UI opened in a browser or through the relay): lists
// the sub-folders of a folder on the host, starting at the home folder.
// Only folder names are returned, never files; only the host's owner may
// browse (a member's device gets 403), and only desktop-class devices.
//
//	GET /api/fs/dirs?path=<abs or ~/…>  → {path, parent, home, dirs: [{name, path}]}

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

type dirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

const maxDirs = 500

func browseDirs(home, p string) (map[string]any, error) {
	if p == "" || p == "~" {
		p = home
	} else if strings.HasPrefix(p, "~/") {
		p = filepath.Join(home, p[2:])
	}
	if !filepath.IsAbs(p) {
		return nil, gateway.BadRequest("path must be absolute")
	}
	p = filepath.Clean(p)
	es, err := os.ReadDir(p)
	if err != nil {
		return nil, gateway.NotFound()
	}
	out := []dirEntry{}
	for _, e := range es {
		n := e.Name()
		if strings.HasPrefix(n, ".") {
			continue // hidden: config and caches, not projects
		}
		full := filepath.Join(p, n)
		isDir := e.IsDir()
		if e.Type()&os.ModeSymlink != 0 {
			if st, err := os.Stat(full); err == nil && st.IsDir() {
				isDir = true
			}
		}
		if isDir {
			out = append(out, dirEntry{Name: n, Path: full})
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	if len(out) > maxDirs {
		out = out[:maxDirs]
	}
	parent := filepath.Dir(p)
	if parent == p {
		parent = ""
	}
	return map[string]any{"path": p, "parent": parent, "home": home, "dirs": out}, nil
}

// ownerOnly refuses devices that belong to a member (not the host owner).
type memberOf interface{ MemberIDOf(deviceID string) string }

func registerDirs(c *kernel.Context, g *gateway.Gateway) {
	g.Handle(c, gateway.Route{Method: "GET", Path: "/api/fs/dirs",
		Handle: func(_ context.Context, r *gateway.Request) (any, error) {
			if r.DeviceID != "" {
				if m, ok := kernel.Lookup[memberOf](c, "members"); ok && m.MemberIDOf(r.DeviceID) != "" {
					return nil, &gateway.Error{Status: 403, Code: "forbidden", Message: "only the host's owner can browse its folders"}
				}
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			return browseDirs(home, r.HTTP.URL.Query().Get("path"))
		}})
}
