// Package preview shows what an agent built (an HTML file, a static site,
// a dev server) in the right panel of the desktop UI (docs/previews.md).
//
// Agent-written HTML never runs in the app's origin: workspace files are
// served by per-root static servers on random 127.0.0.1 ports (Manager),
// and dev servers are opened at their own address. The UI frames either
// in a sandboxed iframe.
package preview

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

// ServiceKey is the context key of the *Manager.
const ServiceKey = "preview"

// EventOpened is the session event an agent's preview_open leaves.
const EventOpened = "preview/opened"

// maxRecent caps the previews listed per session.
const maxRecent = 12

// Opened describes one preview (the preview/opened event data and a row
// of the session's preview list).
type Opened struct {
	Title string `json:"title"`
	// URL is the address framed; for kind "path" it may be stale (a
	// stopped server) and the UI reopens the path to get a fresh one.
	URL  string `json:"url"`
	Kind string `json:"kind"` // "path" | "url"
	// Path is the workspace-relative file or folder (kind "path").
	Path   string    `json:"path,omitempty"`
	Agent  string    `json:"agent,omitempty"`
	Source string    `json:"source"` // tool | files | snippet
	Call   string    `json:"call,omitempty"`
	Time   time.Time `json:"time"`
}

// Target is a workspace path resolved for previewing.
type Target struct {
	Root string // real directory served
	Page string // URL path of the page in it ("/" or "/page.html")
	Rel  string // workspace-relative path asked for (slash-separated)
}

// Resolve checks rel (relative to the workspace, or absolute inside it)
// and returns what to serve: a folder needs an index.html; a file is
// served from its folder.
func Resolve(workspace, rel string) (Target, error) {
	if workspace == "" || !filepath.IsAbs(workspace) {
		return Target{}, errors.New("no workspace is set for this agent")
	}
	if strings.ContainsRune(rel, 0) {
		return Target{}, fmt.Errorf("invalid path %q", rel)
	}
	ws, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return Target{}, fmt.Errorf("the workspace is not available: %v", err)
	}
	p := strings.TrimSpace(rel)
	if p == "" {
		p = "."
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(workspace, p)
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(p))
	if err != nil {
		if os.IsNotExist(err) {
			return Target{}, fmt.Errorf("%s does not exist", rel)
		}
		return Target{}, err
	}
	if real != ws && !strings.HasPrefix(real, ws+string(filepath.Separator)) {
		return Target{}, fmt.Errorf("%s is outside the workspace; only workspace files can be previewed", rel)
	}
	info, err := os.Stat(real)
	if err != nil {
		return Target{}, err
	}
	out := Target{}
	if r, err := filepath.Rel(ws, real); err == nil {
		out.Rel = filepath.ToSlash(r)
	}
	if info.IsDir() {
		if fi, err := os.Stat(filepath.Join(real, "index.html")); err != nil || fi.IsDir() {
			return Target{}, fmt.Errorf("%s has no index.html; pass an .html file, or build the project first (e.g. npm run build → dist/)", rel)
		}
		out.Root, out.Page = real, "/"
		if out.Rel != "." {
			out.Rel += "/"
		}
		return out, nil
	}
	out.Root, out.Page = filepath.Dir(real), "/"+url.PathEscape(filepath.Base(real))
	return out, nil
}

// OpenPath serves a workspace path and returns its preview.
func (m *Manager) OpenPath(agentID, workspace, rel string) (Opened, error) {
	tg, err := Resolve(workspace, rel)
	if err != nil {
		return Opened{}, err
	}
	base, err := m.Serve(agentID, tg.Root)
	if err != nil {
		return Opened{}, err
	}
	return Opened{URL: base + tg.Page, Kind: "path", Path: tg.Rel, Agent: agentID, Title: tg.Rel, Time: time.Now().UTC()}, nil
}

// NormalizeURL accepts http(s) URLs only; 0.0.0.0 / [::] (what a dev
// server binds) become 127.0.0.1.
func NormalizeURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("%q is not an http(s) URL", raw)
	}
	if u.User != nil {
		return "", errors.New("URLs with a user name or password are not opened")
	}
	switch u.Hostname() {
	case "0.0.0.0", "::":
		p := u.Port()
		u.Host = "127.0.0.1"
		if p != "" {
			u.Host += ":" + p
		}
	}
	return u.String(), nil
}

// IsLoopback reports whether the URL's host is this machine.
func IsLoopback(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	h := u.Hostname()
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// URLWait is how long preview_open waits for a local dev server that is
// still starting.
var URLWait = 8 * time.Second

// answers waits up to wait for something to answer HTTP at u.
func answers(ctx context.Context, u string, wait time.Duration) bool {
	cl := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	deadline := time.Now().Add(wait)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return false
		}
		if res, err := cl.Do(req); err == nil {
			res.Body.Close()
			return true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(300 * time.Millisecond):
		}
	}
}

const toolDescription = "Show a web page to the user in the preview panel beside the conversation (it opens there by itself). " +
	"Use it whenever you made something the user should look at: an HTML page, a static site, a Vue / React / Vite app.\n" +
	"Pass exactly one of:\n" +
	"- path: an .html file, or a folder with an index.html, in the workspace. It is served read-only by a local static server on its own origin with that folder as the site root " +
	"(so absolute /assets/... links work; unknown routes fall back to index.html for single-page apps; dotfiles are not served). The user's reload shows later edits.\n" +
	"- url: an http(s) address, normally a dev server you started, e.g. http://127.0.0.1:5173. The result says if nothing answers there.\n" +
	"A single page: write it (e.g. site/index.html, CSS and JS inline or next to it), then preview_open {path: \"site/index.html\"}.\n" +
	"Vue / React (Vite):\n" +
	"1. Create and install with bash: `npm create vite@latest my-app -- --template react` (or vue, react-ts, vue-ts; add --no-interactive if it asks questions), then `npm install` with cwd my-app (give installs timeout_seconds 600).\n" +
	"2. Start the dev server in the background: bash {command: \"npm run dev -- --host 127.0.0.1 --port 5173 --strictPort\", cwd: \"my-app\", background: true}. " +
	"Bind 127.0.0.1; if the port is taken use another one. Check job_output if it does not come up.\n" +
	"3. preview_open {url: \"http://127.0.0.1:5173\", title: \"My app\"}. Edits hot-reload in the preview; leave the server running while the user looks (job_stop ends it).\n" +
	"Or build a static copy instead: `npm run build`, then preview_open {path: \"my-app/dist\"} (nothing to keep running).\n" +
	"Never pass file:// URLs or the Crewbase app's own address."

func (m *Manager) tool() *tools.Tool {
	target := func(call tools.Call) string {
		if u := call.String("url"); u != "" {
			return u
		}
		return call.String("path")
	}
	return &tools.Tool{
		Name:        "preview_open",
		Description: toolDescription,
		Params: tools.Schema(map[string]any{
			"path":  tools.Prop("string", "Workspace .html file or folder with index.html (relative to the workspace)."),
			"url":   tools.Prop("string", "http(s) URL to show, e.g. a dev server at http://127.0.0.1:5173."),
			"title": tools.Prop("string", "Short title for the preview tab (default: the path or host)."),
		}),
		// it only reads the workspace; the page runs in the user's sandboxed frame
		Category: tools.CategoryRead,
		PathArgs: []string{"path"},
		Classify: func(call tools.Call) (string, string) { return tools.CategoryRead, "preview " + target(call) },
		Execute:  m.execute,
	}
}

func (m *Manager) execute(ctx context.Context, call tools.Call) (tools.Result, error) {
	var args struct {
		Path  string `json:"path"`
		URL   string `json:"url"`
		Title string `json:"title"`
	}
	if err := call.Decode(&args); err != nil {
		return tools.Errorf("invalid arguments: %v", err), nil
	}
	args.Path, args.URL, args.Title = strings.TrimSpace(args.Path), strings.TrimSpace(args.URL), strings.TrimSpace(args.Title)
	if (args.Path == "") == (args.URL == "") {
		return tools.Errorf("pass exactly one of path (a workspace file or folder) or url"), nil
	}
	var o Opened
	var note string
	if args.URL != "" {
		u, err := NormalizeURL(args.URL)
		if err != nil {
			return tools.Errorf("%v", err), nil
		}
		if IsLoopback(u) && !answers(ctx, u, URLWait) {
			return tools.Errorf("Nothing answers at %s, so the preview was not opened. Check that the dev server is running (job_output) and listening on that host and port, then call preview_open again.", u), nil
		}
		host := u
		if pu, err := url.Parse(u); err == nil {
			host = pu.Host
		}
		o = Opened{URL: u, Kind: "url", Agent: call.Agent, Title: host, Time: time.Now().UTC()}
	} else {
		var err error
		if o, err = m.OpenPath(call.Agent, call.Workspace, args.Path); err != nil {
			return tools.Errorf("preview_open: %v", err), nil
		}
		note = fmt.Sprintf(" It is served read-only from %s by a local static server; reloading shows your later edits.", o.Path)
	}
	if args.Title != "" {
		o.Title = args.Title
	}
	o.Source, o.Call = "tool", call.ID
	if call.Session != nil {
		if _, err := call.Session.Append(EventOpened, call.Agent, o); err != nil {
			return tools.Errorf("the preview could not be recorded: %v", err), nil
		}
	}
	return tools.Result{Content: fmt.Sprintf("Opened %q in the preview panel on the user's screen: %s.%s", o.Title, o.URL, note), Data: o}, nil
}

// Plugin provides the Manager and the preview_open tool.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/preview",
	Inject:      []string{tools.ServiceKey, agent.ServiceKey},
	Description: "preview_open: show an agent's HTML, static site or dev server in the right panel (isolated per-root static servers on 127.0.0.1); show_page: a checked page as the answer, in the conversation.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[struct {
			// Idle closes a static server after this long without a request (default 30m).
			Idle string `json:"idle,omitempty"`
		}](c)
		if err != nil {
			return err
		}
		idle := DefaultIdle
		if cfg.Idle != "" {
			if idle, err = time.ParseDuration(cfg.Idle); err != nil {
				return fmt.Errorf("preview: idle: %w", err)
			}
		}
		m := NewManager(idle, 0)
		c.Defer(m.Close)
		agent.Deleted.Listen(c, m.CloseAgent)
		reg := kernel.Use[*tools.Registry](c, tools.ServiceKey)
		reg.Register(c, m.tool())
		reg.Register(c, m.pageTool(c))
		return c.Provide(ServiceKey, m)
	},
})
