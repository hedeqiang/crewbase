package preview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// SnippetDir is where HTML code blocks are written to be previewed
// (relative to the workspace).
const SnippetDir = ".crewbase/previews"

// MaxSnippet caps a previewed HTML code block.
const MaxSnippet = 2 << 20

// WriteSnippet saves html as SnippetDir/<hash>.html in the workspace and
// returns its workspace-relative path.
func WriteSnippet(workspace, html string) (string, error) {
	sum := sha256.Sum256([]byte(html))
	rel := SnippetDir + "/" + hex.EncodeToString(sum[:8]) + ".html"
	p := filepath.Join(workspace, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	if b, err := os.ReadFile(p); err == nil && string(b) == html {
		return rel, nil
	}
	if err := os.WriteFile(p, []byte(html), 0o644); err != nil {
		return "", err
	}
	return rel, nil
}

func key(o Opened) string {
	if o.Kind == "path" {
		return "p\x00" + o.Agent + "\x00" + o.Path
	}
	return "u\x00" + o.URL
}

// List returns a session's previews, newest first: what its agents
// opened (preview/opened events) and what the person opened from the
// app, one row per page.
func (m *Manager) List(ss *session.Session) []Opened {
	var all []Opened
	for _, e := range ss.Events() {
		if e.Type != EventOpened {
			continue
		}
		var o Opened
		if e.Decode(&o) == nil && o.URL != "" {
			if o.Agent == "" {
				o.Agent = e.Agent
			}
			if o.Time.IsZero() {
				o.Time = e.Time
			}
			all = append(all, o)
		}
	}
	all = append(all, m.remembered(ss.ID())...)
	sort.SliceStable(all, func(i, j int) bool { return all[i].Time.After(all[j].Time) })
	seen := map[string]bool{}
	out := []Opened{}
	for _, o := range all {
		if k := key(o); !seen[k] {
			seen[k] = true
			out = append(out, o)
		}
		if len(out) == maxRecent {
			break
		}
	}
	return out
}

type openBody struct {
	Path    string `json:"path"`
	HTML    string `json:"html"`
	Title   string `json:"title"`
	Session string `json:"session"`
}

// APIPlugin exposes previews to the desktop UI:
//
//	POST /api/agents/{id}/preview        {path, title?, session?}  serve a workspace file / folder
//	POST /api/agents/{id}/preview/html   {html, title?, session?}  an HTML code block, saved under .crewbase/previews/
//	GET  /api/sessions/{id}/previews                               the session's previews, newest first
var APIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/preview-api",
	Inject:      []string{gateway.ServiceKey, agent.ServiceKey, ServiceKey},
	Description: "Remote methods for the right panel's 预览 tab.",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		agents := kernel.Use[*agent.Registry](c, agent.ServiceKey)
		m := kernel.Use[*Manager](c, ServiceKey)
		open := func(r *gateway.Request, b openBody, source string) (any, error) {
			a, ok := agents.Get(r.Params["id"])
			if !ok {
				return nil, gateway.NotFound()
			}
			o, err := m.OpenPath(a.ID(), a.Workspace(), b.Path)
			if err != nil {
				return nil, gateway.BadRequest("%v", err)
			}
			if t := strings.TrimSpace(b.Title); t != "" {
				o.Title = t
			}
			o.Source = source
			m.remember(b.Session, o)
			return o, nil
		}
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/agents/{id}/preview",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b openBody
				if err := r.Decode(&b); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				if strings.TrimSpace(b.Path) == "" {
					return nil, gateway.BadRequest("path is required")
				}
				return open(r, b, "files")
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/agents/{id}/preview/html", MaxBody: MaxSnippet + 64<<10,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b openBody
				if err := r.Decode(&b); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				if strings.TrimSpace(b.HTML) == "" {
					return nil, gateway.BadRequest("html is required")
				}
				if len(b.HTML) > MaxSnippet {
					return nil, gateway.BadRequest("the HTML is too large to preview")
				}
				a, ok := agents.Get(r.Params["id"])
				if !ok {
					return nil, gateway.NotFound()
				}
				rel, err := WriteSnippet(a.Workspace(), b.HTML)
				if err != nil {
					return nil, err
				}
				b.Path = rel
				return open(r, b, "snippet")
			}})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/sessions/{id}/previews",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				ss, err := agents.Sessions().Get(r.Params["id"])
				if err != nil {
					return nil, gateway.NotFound()
				}
				return map[string]any{"previews": m.List(ss)}, nil
			}})
		return nil
	},
})
