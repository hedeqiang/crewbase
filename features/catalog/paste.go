package catalog

// Paste to install: one box takes whatever the person copied from a
// README or a docs page, works out what it is and what installing it
// needs, and installs it once they confirm. It understands
//
//   - an MCP config: Claude Desktop / Cursor {"mcpServers": {...}}, VS Code
//     {"servers": {...}}, a bare map of servers, or one server object;
//   - a `claude mcp add …` / `codex mcp add …` line;
//   - a command that starts a local MCP server (npx …, uvx …, docker run …),
//     with leading KEY=value environment assignments;
//   - an MCP server URL (probed: it must answer the MCP handshake; 401
//     means it signs in with OAuth);
//   - a GitHub repository or folder: its SKILL.md folders are skills, a
//     crewbase-plugin.toml at its root makes it a plugin;
//   - a local folder or file: a skill folder, a folder of skills, a plugin
//     folder or a plugin tarball;
//   - an npm package (name or npmjs.com page): a Crewbase plugin when it
//     says so, otherwise an MCP server run with npx.
//
// Detect only reads (and downloads to look); Apply installs MCP servers
// and skills. Plugins go through the plugin manager's own install and
// permission approval (POST /api/plugins/install with Plan.Plugin).
//
// Secret-looking values (keys named *TOKEN*, *KEY*, *SECRET*, …, and
// Authorization headers) are stored in the vault; the config keeps the
// reference, as for catalog presets.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/features/mcp"
	"github.com/hedeqiang/crewbase/features/skills"
	"gopkg.in/yaml.v3"
)

// Plan is what pasted input installs.
type Plan struct {
	Kind string `json:"kind"` // mcp | skill | plugin
	// Source is what the plan was read from (a URL, a path, "config").
	Source  string       `json:"source"`
	Servers []PlanServer `json:"servers,omitempty"`
	Skills  []PlanSkill  `json:"skills,omitempty"`
	// Plugin is the source to hand to the plugin install (Kind "plugin").
	Plugin string `json:"plugin,omitempty"`
	// Note explains something the person should know before installing.
	Note Text `json:"note,omitzero"`
}

// PlanServer is one MCP server of a plan; the person may edit it before
// Apply.
type PlanServer struct {
	Name      string    `json:"name"`
	Transport string    `json:"transport"` // stdio | http
	Command   []string  `json:"command,omitempty"`
	URL       string    `json:"url,omitempty"`
	OAuth     bool      `json:"oauth,omitempty"`
	Env       []PlanVar `json:"env,omitempty"`
	Headers   []PlanVar `json:"headers,omitempty"`
	// Exists: a server of this name is already installed.
	Exists bool `json:"exists,omitempty"`
}

// PlanVar is an environment variable or header of a server.
type PlanVar struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	// Secret: stored in the vault, shown as a password field.
	Secret bool `json:"secret,omitempty"`
	// Missing: the pasted value is a placeholder; the person fills it in.
	Missing bool `json:"missing,omitempty"`
}

// PlanSkill is one skill folder found in the source.
type PlanSkill struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Path is the folder inside the source ("" = its root).
	Path   string `json:"path"`
	Exists bool   `json:"exists,omitempty"`
}

// ApplyRequest installs (part of) a plan.
type ApplyRequest struct {
	Kind    string       `json:"kind"`
	Source  string       `json:"source"`
	Servers []PlanServer `json:"servers,omitempty"`
	// Skills are the PlanSkill.Path values to install.
	Skills []string `json:"skills,omitempty"`
	// Scope: "global" / "" or an agent id.
	Scope string `json:"scope,omitempty"`
}

// Applied is what Apply installed.
type Applied struct {
	Servers []mcp.View     `json:"servers,omitempty"`
	Skills  []skills.Skill `json:"skills,omitempty"`
}

// ErrNotRecognized means the pasted input is none of the known shapes.
var ErrNotRecognized = errors.New("not recognized")

// Sources the detector reads (tests point them elsewhere).
var (
	githubWeb   = "https://github.com"
	npmRegistry = "https://registry.npmjs.org"
	// a skill library is a whole repository download (several MB): the
	// connection must answer quickly, the body may take a while
	pasteClient = &http.Client{Timeout: 3 * time.Minute, Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}}
)

// Limits on what is downloaded to look at a source.
const (
	maxArchiveBytes = 40 << 20
	maxSkillBytes   = 8 << 20
	maxSkillFiles   = 300
)

// Detect works out what input installs.
func (s *Service) Detect(ctx context.Context, input string) (Plan, error) {
	in := strings.TrimSpace(input)
	if in == "" {
		return Plan{}, fmt.Errorf("%w: paste a link, a command or a config", ErrNotRecognized)
	}
	var p Plan
	var err error
	switch {
	case strings.HasPrefix(in, "{") || strings.HasPrefix(in, `"`):
		p, err = detectConfig(in)
	case addLine.MatchString(in):
		p, err = detectAddLine(in)
	case isLocalPath(in):
		p, err = s.detectLocal(expandHome(in))
	case strings.HasPrefix(in, "http://") || strings.HasPrefix(in, "https://"):
		p, err = s.detectURL(ctx, in)
	case looksLikeCommand(in):
		p, err = detectCommand(in)
	case npmName.MatchString(in):
		p, err = detectNpm(ctx, in)
	default:
		err = fmt.Errorf("%w: paste an MCP config, a command, a server URL, a GitHub link, a folder or an npm package", ErrNotRecognized)
	}
	if err != nil {
		return Plan{}, err
	}
	s.markExisting(&p)
	return p, nil
}

// Apply installs the servers or skills of a confirmed plan.
func (s *Service) Apply(ctx context.Context, req ApplyRequest) (Applied, error) {
	scope := strings.TrimSpace(req.Scope)
	if scope == "" {
		scope = mcp.ScopeGlobal
	}
	switch req.Kind {
	case "mcp":
		return s.applyServers(req.Servers, scope)
	case "skill":
		return s.applySkills(ctx, req.Source, req.Skills, scope)
	case "plugin":
		return Applied{}, fmt.Errorf("%w: plugins are installed with POST /api/plugins/install", ErrInput)
	}
	return Applied{}, fmt.Errorf("%w: unknown kind %q", ErrInput, req.Kind)
}

// ---------------------------------------------------------------- MCP configs

func detectConfig(in string) (Plan, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(in), &raw); err != nil {
		// a fragment: "name": {...}
		if err2 := json.Unmarshal([]byte("{"+strings.TrimSuffix(strings.TrimSpace(in), ",")+"}"), &raw); err2 != nil {
			return Plan{}, fmt.Errorf("%w: this is not valid JSON (%v)", ErrNotRecognized, err)
		}
	}
	for _, k := range []string{"mcpServers", "servers", "mcp_servers", "context_servers"} {
		if v, ok := raw[k]; ok {
			var m map[string]json.RawMessage
			if err := json.Unmarshal(v, &m); err != nil {
				return Plan{}, fmt.Errorf("%w: %s is not an object of servers", ErrNotRecognized, k)
			}
			raw = m
			break
		}
	}
	// one server object on its own
	if isServerObject(raw) {
		b, _ := json.Marshal(raw)
		raw = map[string]json.RawMessage{"": b}
	}
	p := Plan{Kind: "mcp", Source: "config"}
	names := make([]string, 0, len(raw))
	for k := range raw {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		var o map[string]json.RawMessage
		if json.Unmarshal(raw[name], &o) != nil || !isServerObject(o) {
			continue
		}
		srv, err := serverFromObject(name, o)
		if err != nil {
			return Plan{}, err
		}
		p.Servers = append(p.Servers, srv)
	}
	if len(p.Servers) == 0 {
		return Plan{}, fmt.Errorf("%w: no MCP server in this config (a server has a command or a url)", ErrNotRecognized)
	}
	return p, nil
}

func isServerObject(o map[string]json.RawMessage) bool {
	_, c := o["command"]
	_, u := o["url"]
	_, su := o["serverUrl"]
	return c || u || su
}

func serverFromObject(name string, o map[string]json.RawMessage) (PlanServer, error) {
	str := func(k string) string {
		var v string
		_ = json.Unmarshal(o[k], &v)
		return strings.TrimSpace(v)
	}
	strs := func(k string) []string {
		var v []string
		_ = json.Unmarshal(o[k], &v)
		return v
	}
	vars := func(k string) map[string]string {
		var v map[string]string
		_ = json.Unmarshal(o[k], &v)
		return v
	}
	if c := str("command"); c != "" {
		cmd := append(splitCommand(c), strs("args")...)
		srv := PlanServer{Name: name, Transport: "stdio", Command: cmd, Env: planVars(vars("env"), false)}
		if srv.Name == "" {
			srv.Name = nameFromCommand(cmd)
		}
		srv.Name = cleanName(srv.Name)
		return srv, nil
	}
	u := str("url")
	if u == "" {
		u = str("serverUrl")
	}
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		return PlanServer{}, fmt.Errorf("%w: %q is not an http(s) URL", ErrNotRecognized, u)
	}
	srv := PlanServer{Name: name, Transport: "http", URL: u, Headers: planVars(vars("headers"), true)}
	if srv.Name == "" {
		srv.Name = nameFromURL(u)
	}
	srv.Name = cleanName(srv.Name)
	// with no credentials given, a remote server usually signs in itself
	srv.OAuth = len(srv.Headers) == 0
	return srv, nil
}

// addLine is `claude mcp add …` / `codex mcp add …`.
var addLine = regexp.MustCompile(`^(claude|codex|gemini)\s+mcp\s+add\s`)

func detectAddLine(in string) (Plan, error) {
	args := splitCommand(in)[3:]
	var name, transport string
	env := map[string]string{}
	headers := map[string]string{}
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		val := func() string {
			if k, v, ok := strings.Cut(a, "="); ok && strings.HasPrefix(k, "--") {
				return v
			}
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch {
		case a == "--":
			rest = append(rest, args[i+1:]...)
			i = len(args)
		case a == "-t" || strings.HasPrefix(a, "--transport"):
			transport = val()
		case a == "-e" || strings.HasPrefix(a, "--env"):
			if k, v, ok := strings.Cut(val(), "="); ok {
				env[k] = v
			}
		case a == "-H" || strings.HasPrefix(a, "--header"):
			if k, v, ok := strings.Cut(val(), ":"); ok {
				headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		case a == "-s" || strings.HasPrefix(a, "--scope"), strings.HasPrefix(a, "--url"):
			if strings.HasPrefix(a, "--url") {
				rest = append(rest, val())
			} else {
				_ = val()
			}
		case strings.HasPrefix(a, "-"):
			// an option we do not need
		case name == "":
			name = a
		default:
			rest = append(rest, args[i:]...)
			i = len(args)
		}
	}
	if len(rest) == 0 {
		return Plan{}, fmt.Errorf("%w: the command names no server URL or command", ErrNotRecognized)
	}
	if transport == "http" || transport == "sse" || strings.HasPrefix(rest[0], "http://") || strings.HasPrefix(rest[0], "https://") {
		srv := PlanServer{Name: cleanName(firstNonEmpty(name, nameFromURL(rest[0]))), Transport: "http", URL: rest[0], Headers: planVars(headers, true)}
		srv.OAuth = len(headers) == 0
		return Plan{Kind: "mcp", Source: "command", Servers: []PlanServer{srv}}, nil
	}
	srv := PlanServer{Name: cleanName(firstNonEmpty(name, nameFromCommand(rest))), Transport: "stdio", Command: rest, Env: planVars(env, false)}
	return Plan{Kind: "mcp", Source: "command", Servers: []PlanServer{srv}}, nil
}

// runners start local MCP servers.
var runners = []string{"npx", "uvx", "uv", "bunx", "pnpm", "pnpx", "yarn", "docker", "podman", "node", "deno", "bun", "python", "python3", "pipx", "go", "java", "dotnet"}

var envAssign = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

func looksLikeCommand(in string) bool {
	f := splitCommand(in)
	for len(f) > 0 && envAssign.MatchString(f[0]) {
		f = f[1:]
	}
	if len(f) == 0 {
		return false
	}
	return slices.Contains(runners, path.Base(f[0])) || strings.HasPrefix(f[0], "/")
}

func detectCommand(in string) (Plan, error) {
	f := splitCommand(in)
	env := map[string]string{}
	for len(f) > 0 && envAssign.MatchString(f[0]) {
		k, v, _ := strings.Cut(f[0], "=")
		env[k] = v
		f = f[1:]
	}
	srv := PlanServer{Name: cleanName(nameFromCommand(f)), Transport: "stdio", Command: f, Env: planVars(env, false)}
	return Plan{Kind: "mcp", Source: "command", Servers: []PlanServer{srv}}, nil
}

// ---------------------------------------------------------------- URLs

func (s *Service) detectURL(ctx context.Context, raw string) (Plan, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return Plan{}, fmt.Errorf("%w: %q is not a URL", ErrNotRecognized, raw)
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case host == "github.com" || host == "www.github.com":
		return s.detectGitHub(ctx, u)
	case host == "www.npmjs.com" || host == "npmjs.com":
		if name, ok := strings.CutPrefix(u.Path, "/package/"); ok && name != "" {
			return detectNpm(ctx, strings.Trim(name, "/"))
		}
	case strings.HasSuffix(u.Path, ".tgz") || strings.HasSuffix(u.Path, ".tar.gz"):
		return Plan{Kind: "plugin", Source: raw, Plugin: raw}, nil
	}
	// an MCP server: it answers the handshake, or asks to sign in
	tryURL := raw
	st, err := probeMCP(ctx, tryURL)
	if (err != nil || st == 0) && strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/sse") {
		// a legacy SSE address: most such servers also serve /mcp
		alt := *u
		alt.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/sse") + "/mcp"
		if st2, err2 := probeMCP(ctx, alt.String()); err2 == nil && st2 != 0 {
			tryURL, st, err = alt.String(), st2, nil
		}
	}
	if err != nil || st == 0 {
		return Plan{}, fmt.Errorf("%w: %s does not answer as an MCP server, and is not a GitHub or npm link", ErrNotRecognized, raw)
	}
	srv := PlanServer{Name: cleanName(nameFromURL(tryURL)), Transport: "http", URL: tryURL, OAuth: st == http.StatusUnauthorized}
	p := Plan{Kind: "mcp", Source: raw, Servers: []PlanServer{srv}}
	if tryURL != raw {
		p.Note = Text{Zh: "这是旧版 SSE 地址，已改用同一服务器的 " + tryURL, En: "That is a legacy SSE address; using the same server's " + tryURL}
	}
	return p, nil
}

// probeMCP sends the MCP initialize request. It returns 200 when the
// server answered, 401 when it wants a sign-in, 0 when it is not an MCP
// server.
func probeMCP(ctx context.Context, u string) (int, error) {
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"crewbase","version":"1"}}}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := pasteClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode == http.StatusUnauthorized && strings.Contains(strings.ToLower(resp.Header.Get("WWW-Authenticate")), "bearer"):
		return http.StatusUnauthorized, nil
	case resp.StatusCode == http.StatusOK && (bytes.Contains(b, []byte(`"protocolVersion"`)) || bytes.Contains(b, []byte(`"serverInfo"`))):
		return http.StatusOK, nil
	}
	return 0, nil
}

// ---------------------------------------------------------------- GitHub

// ghTarget is a repository and the folder of it a link points at.
type ghTarget struct{ owner, repo, ref, dir string }

func parseGitHub(u *url.URL) (ghTarget, bool) {
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return ghTarget{}, false
	}
	t := ghTarget{owner: parts[0], repo: strings.TrimSuffix(parts[1], ".git"), ref: "HEAD"}
	if len(parts) >= 4 && (parts[2] == "tree" || parts[2] == "blob") {
		t.ref = parts[3]
		t.dir = strings.Join(parts[4:], "/")
		if parts[2] == "blob" { // a file: its folder (a SKILL.md link)
			t.dir = path.Dir(t.dir)
			if t.dir == "." {
				t.dir = ""
			}
		}
	}
	return t, true
}

func (t ghTarget) archive() string {
	return githubWeb + "/" + t.owner + "/" + t.repo + "/archive/" + url.PathEscape(t.ref) + ".tar.gz"
}

func (s *Service) detectGitHub(ctx context.Context, u *url.URL) (Plan, error) {
	t, ok := parseGitHub(u)
	if !ok {
		return Plan{}, fmt.Errorf("%w: link to a repository or a folder of one", ErrNotRecognized)
	}
	files, err := fetchArchive(ctx, t.archive(), t.dir, true)
	if err != nil {
		return Plan{}, err
	}
	if _, ok := files[pluginManifest]; ok && t.dir == "" {
		return Plan{Kind: "plugin", Source: u.String(), Plugin: t.archive()}, nil
	}
	if sk := skillsIn(files); len(sk) > 0 {
		return Plan{Kind: "skill", Source: u.String(), Skills: sk}, nil
	}
	if _, ok := files[pluginManifest]; ok {
		return Plan{}, fmt.Errorf("%w: a plugin is installed from its repository's root, not a folder of it", ErrNotRecognized)
	}
	return Plan{}, fmt.Errorf("%w: this repository has no SKILL.md and no crewbase-plugin.toml; if it is an MCP server, paste the config or command from its README", ErrNotRecognized)
}

const pluginManifest = "crewbase-plugin.toml"

// fetchArchive downloads a .tar.gz and returns the files under dir of its
// single top-level folder (paths relative to dir). With headOnly only the
// files a detector looks at are kept: SKILL.md and plugin manifests.
// Otherwise only the files in the folders keep names ("" = all of dir),
// and each of those folders is held to a skill's size.
func fetchArchive(ctx context.Context, src, dir string, headOnly bool, keep ...string) (map[string][]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, err
	}
	resp, err := pasteClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: not found (a private repository, or a wrong branch or folder)", ErrNotRecognized)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	zr, err := gzip.NewReader(io.LimitReader(resp.Body, maxArchiveBytes))
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	tr := tar.NewReader(zr)
	files := map[string][]byte{}
	size := map[string]int{}  // bytes per kept folder
	count := map[string]int{} // files per kept folder
	prefix := strings.Trim(dir, "/")
	// keptIn: the kept folder name lies in ("", true for "all of dir")
	keptIn := func(name string) (string, bool) {
		if len(keep) == 0 {
			return "", true
		}
		for _, k := range keep {
			if k == "" || name == k || strings.HasPrefix(name, k+"/") {
				return k, true
			}
		}
		return "", false
	}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("download: %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		name := h.Name
		if i := strings.IndexByte(name, '/'); i >= 0 {
			name = name[i+1:] // the archive's top-level folder
		}
		if prefix != "" {
			rel, ok := strings.CutPrefix(name, prefix+"/")
			if !ok {
				continue
			}
			name = rel
		}
		if name == "" || strings.Contains(name, "..") {
			continue
		}
		base := path.Base(name)
		if headOnly && base != "SKILL.md" && base != pluginManifest {
			continue
		}
		if !headOnly {
			if strings.HasPrefix(name, ".git/") || base == ".DS_Store" {
				continue
			}
			k, ok := keptIn(name)
			if !ok {
				continue
			}
			size[k] += int(h.Size)
			count[k]++
			if size[k] > maxSkillBytes || count[k] > maxSkillFiles {
				what := firstNonEmpty(k, "the folder")
				return nil, fmt.Errorf("%w: %s is larger than a skill should be (%d MB, %d files at most)", ErrInput, what, maxSkillBytes>>20, maxSkillFiles)
			}
		}
		b, err := io.ReadAll(io.LimitReader(tr, maxSkillBytes))
		if err != nil {
			return nil, err
		}
		files[name] = b
	}
	return files, nil
}

// skillsIn lists the skill folders in files (any SKILL.md, at any depth).
func skillsIn(files map[string][]byte) []PlanSkill {
	var out []PlanSkill
	for name, b := range files {
		if path.Base(name) != "SKILL.md" {
			continue
		}
		dir := path.Dir(name)
		if dir == "." {
			dir = ""
		}
		m := frontmatter(b)
		n := m["name"]
		if n == "" {
			n = path.Base(dir)
		}
		out = append(out, PlanSkill{Name: n, Description: m["description"], Path: dir})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// frontmatter reads the simple key: value lines of a SKILL.md header.
func frontmatter(b []byte) map[string]string {
	out := map[string]string{}
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return out
	}
	end := strings.Index(s[4:], "\n---")
	if end < 0 {
		return out
	}
	// real YAML first: block scalars ("description: >-" and the indented
	// lines under it) are common in SKILL.md headers
	var y map[string]any
	if yaml.Unmarshal([]byte(s[4:4+end]), &y) == nil && len(y) > 0 {
		for k, v := range y {
			if str, ok := v.(string); ok {
				out[k] = strings.Join(strings.Fields(str), " ")
			}
		}
		return out
	}
	for _, line := range strings.Split(s[4:4+end], "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok || strings.HasPrefix(line, " ") {
			continue
		}
		out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return out
}

// ---------------------------------------------------------------- local

func isLocalPath(in string) bool {
	return strings.HasPrefix(in, "/") || strings.HasPrefix(in, "~/") || strings.HasPrefix(in, "./") || strings.HasPrefix(in, "file://") ||
		(len(in) > 2 && in[1] == ':' && (in[2] == '\\' || in[2] == '/'))
}

func expandHome(p string) string {
	p = strings.TrimPrefix(p, "file://")
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, rest)
		}
	}
	return p
}

func (s *Service) detectLocal(p string) (Plan, error) {
	st, err := os.Stat(p)
	if err != nil {
		return Plan{}, fmt.Errorf("%w: %s does not exist", ErrNotRecognized, p)
	}
	if !st.IsDir() {
		switch {
		case filepath.Base(p) == "SKILL.md":
			return s.detectLocal(filepath.Dir(p))
		case strings.HasSuffix(p, ".tgz") || strings.HasSuffix(p, ".tar.gz"):
			return Plan{Kind: "plugin", Source: p, Plugin: p}, nil
		}
		return Plan{}, fmt.Errorf("%w: %s is not a skill folder, a plugin folder or a plugin tarball", ErrNotRecognized, p)
	}
	if _, err := os.Stat(filepath.Join(p, pluginManifest)); err == nil {
		return Plan{Kind: "plugin", Source: p, Plugin: p}, nil
	}
	files, err := readDir(p, true)
	if err != nil {
		return Plan{}, err
	}
	if sk := skillsIn(files); len(sk) > 0 {
		return Plan{Kind: "skill", Source: p, Skills: sk}, nil
	}
	return Plan{}, fmt.Errorf("%w: %s has no SKILL.md and no %s", ErrNotRecognized, p, pluginManifest)
}

// readDir reads root's files (relative slash paths); headOnly keeps only
// SKILL.md files, at most four levels down.
func readDir(root string, headOnly bool) (map[string][]byte, error) {
	files := map[string][]byte{}
	total := 0
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" || (headOnly && strings.Count(rel, "/") >= 4)) {
				return filepath.SkipDir
			}
			return nil
		}
		if headOnly && d.Name() != "SKILL.md" {
			return nil
		}
		if d.Name() == ".DS_Store" || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += int(info.Size())
		if !headOnly && (total > maxSkillBytes || len(files) >= maxSkillFiles) {
			return fmt.Errorf("%w: the folder is larger than a skill should be (%d MB, %d files at most)", ErrInput, maxSkillBytes>>20, maxSkillFiles)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[rel] = b
		return nil
	})
	return files, err
}

// ---------------------------------------------------------------- npm

var npmName = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._~-]*/)?[a-z0-9][a-z0-9._~-]*$`)

func detectNpm(ctx context.Context, name string) (Plan, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, npmRegistry+"/"+strings.Replace(name, "/", "%2f", 1)+"/latest", nil)
	if err != nil {
		return Plan{}, err
	}
	resp, err := pasteClient.Do(req)
	if err != nil {
		return Plan{}, fmt.Errorf("npm: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Plan{}, fmt.Errorf("%w: there is no npm package %q", ErrNotRecognized, name)
	}
	if resp.StatusCode != http.StatusOK {
		return Plan{}, fmt.Errorf("npm: HTTP %d", resp.StatusCode)
	}
	var pkg struct {
		Keywords []string        `json:"keywords"`
		Crewbase json.RawMessage `json:"crewbase"`
		Bin      json.RawMessage `json:"bin"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&pkg); err != nil {
		return Plan{}, fmt.Errorf("npm: %w", err)
	}
	if len(pkg.Crewbase) > 0 || slices.Contains(pkg.Keywords, "crewbase-plugin") {
		return Plan{Kind: "plugin", Source: name, Plugin: name}, nil
	}
	srv := PlanServer{Name: cleanName(nameFromCommand([]string{"npx", "-y", name})), Transport: "stdio", Command: []string{"npx", "-y", name}}
	p := Plan{Kind: "mcp", Source: name, Servers: []PlanServer{srv}}
	if !slices.ContainsFunc(pkg.Keywords, func(k string) bool { return strings.Contains(strings.ToLower(k), "mcp") }) && !strings.Contains(name, "mcp") {
		p.Note = Text{Zh: "没法确定这是 MCP 服务器；装好后如果连不上，请对照它的说明检查命令和参数。", En: "Could not tell for sure that this is an MCP server; if it does not connect, check the command against its docs."}
	}
	return p, nil
}

// ---------------------------------------------------------------- apply

func (s *Service) applyServers(list []PlanServer, scope string) (Applied, error) {
	svc := s.mcp()
	if svc == nil {
		return Applied{}, errors.New("the mcp service is not running")
	}
	if len(list) == 0 {
		return Applied{}, fmt.Errorf("%w: nothing to install", ErrInput)
	}
	// check everything first: nothing is half-installed
	built := make([]mcp.Server, 0, len(list))
	secrets := map[string]string{}
	for _, ps := range list {
		name := cleanName(ps.Name)
		srv := mcp.Server{Name: name, Transport: ps.Transport, Command: ps.Command, URL: ps.URL, Scope: scope, Enabled: true}
		if ps.OAuth && ps.Transport == "http" {
			srv.OAuth = &mcp.OAuth{}
		}
		fill := func(vs []PlanVar, header bool) (map[string]string, error) {
			if len(vs) == 0 {
				return nil, nil
			}
			out := map[string]string{}
			for _, v := range vs {
				k, val := strings.TrimSpace(v.Key), strings.TrimSpace(v.Value)
				if k == "" {
					continue
				}
				if val == "" || placeholder(val) {
					return nil, fmt.Errorf("%w: fill in %s for %s", ErrInput, k, name)
				}
				if v.Secret {
					ref := mcp.SecretRef(name, strings.ToLower(regexp.MustCompile(`[^A-Za-z0-9]+`).ReplaceAllString(k, "-")))
					if header {
						// keep a scheme word outside the secret: "Bearer ${ref}"
						if scheme, tok, ok := strings.Cut(val, " "); ok && strings.EqualFold(scheme, "bearer") {
							secrets[ref] = strings.TrimSpace(tok)
							out[k] = scheme + " ${" + ref + "}"
							continue
						}
					}
					secrets[ref] = val
					out[k] = ref
					continue
				}
				out[k] = val
			}
			return out, nil
		}
		var err error
		if srv.Env, err = fill(ps.Env, false); err != nil {
			return Applied{}, err
		}
		if srv.Headers, err = fill(ps.Headers, true); err != nil {
			return Applied{}, err
		}
		if _, err := srv.Normalize(); err != nil {
			return Applied{}, err
		}
		built = append(built, srv)
	}
	if len(secrets) > 0 {
		cs := s.creds()
		if cs == nil {
			return Applied{}, errors.New("no credential store is available to keep the secrets")
		}
		for ref, v := range secrets {
			if err := cs.Set(ref, v); err != nil {
				s.dropSecrets(secrets)
				return Applied{}, fmt.Errorf("store %s: %w", ref, err)
			}
		}
	}
	var out Applied
	for _, srv := range built {
		v, err := svc.Add(srv)
		if err != nil {
			// the ones added stay; say which one failed
			return out, fmt.Errorf("%s: %w", srv.Name, err)
		}
		out.Servers = append(out.Servers, v)
	}
	return out, nil
}

func (s *Service) applySkills(ctx context.Context, source string, paths []string, scope string) (Applied, error) {
	ss := s.skills()
	if ss == nil {
		return Applied{}, errors.New("the skills service is not running")
	}
	if len(paths) == 0 {
		return Applied{}, fmt.Errorf("%w: pick at least one skill", ErrInput)
	}
	agentID := scope
	if agentID == mcp.ScopeGlobal {
		agentID = ""
	}
	var all map[string][]byte
	var err error
	if u, perr := url.Parse(source); perr == nil && (u.Scheme == "https" || u.Scheme == "http") {
		t, ok := parseGitHub(u)
		if !ok {
			return Applied{}, fmt.Errorf("%w: skills come from GitHub links or local folders", ErrInput)
		}
		all, err = fetchArchive(ctx, t.archive(), t.dir, false, paths...)
	} else {
		// only the picked folders: a big library as a whole is larger than one skill may be
		root := expandHome(source)
		all = map[string][]byte{}
		for _, dir := range paths {
			if slices.Contains(strings.Split(dir, "/"), "..") {
				return Applied{}, fmt.Errorf("%w: %s is outside the folder", ErrInput, dir)
			}
			var files map[string][]byte
			if files, err = readDir(filepath.Join(root, filepath.FromSlash(dir)), false); err != nil {
				break
			}
			for name, b := range files {
				all[path.Join(dir, name)] = b
			}
		}
	}
	if err != nil {
		return Applied{}, err
	}
	var out Applied
	for _, dir := range paths {
		files := map[string][]byte{}
		for name, b := range all {
			rel := name
			if dir != "" {
				r, ok := strings.CutPrefix(name, dir+"/")
				if !ok {
					continue
				}
				rel = r
			}
			files[rel] = b
		}
		if _, ok := files["SKILL.md"]; !ok {
			return out, fmt.Errorf("%w: %s has no SKILL.md", ErrInput, firstNonEmpty(dir, "the folder"))
		}
		name := skillFolder(frontmatter(files["SKILL.md"])["name"], dir, source)
		if isGitHub(source) {
			// remembered so the skill can be checked for updates
			files[skills.OriginFile] = originJSON(source, dir, files)
		}
		v, err := ss.Install(agentID, name, files)
		if errors.Is(err, skills.ErrExists) {
			return out, fmt.Errorf("%w: a skill named %s is already installed", ErrInstalled, name)
		}
		if err != nil {
			return out, err
		}
		out.Skills = append(out.Skills, v)
	}
	return out, nil
}

// skillFolder is the folder name a skill is installed under.
func skillFolder(name, dir, source string) string {
	for _, c := range []string{name, path.Base(dir), path.Base(strings.TrimRight(filepath.ToSlash(source), "/"))} {
		n := strings.Trim(regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(c), "-"), "-")
		if len(n) > 64 {
			n = strings.Trim(n[:64], "-")
		}
		if n != "" && n != "." {
			return n
		}
	}
	return "skill"
}

// markExisting flags servers and skills that are already installed, and
// gives a colliding server a free name.
func (s *Service) markExisting(p *Plan) {
	if s.c == nil {
		return // detection only (no host)
	}
	if svc := s.mcp(); svc != nil {
		have := map[string]bool{}
		for _, v := range svc.List() {
			have[v.Name] = true
		}
		for i := range p.Servers {
			if have[p.Servers[i].Name] {
				p.Servers[i].Exists = true
			}
		}
	}
	if ss := s.skills(); ss != nil {
		have := map[string]bool{}
		for _, sk := range ss.List("") {
			have[sk.Name] = true
		}
		for i := range p.Skills {
			p.Skills[i].Exists = have[p.Skills[i].Name]
		}
	}
}

// ---------------------------------------------------------------- helpers

var secretKey = regexp.MustCompile(`(?i)(api[_-]?key|apikey|token|secret|passw|private[_-]?key|credential|^auth|authorization|bearer|\bpat\b|_pat$|access[_-]?key)`)

// planVars turns env / header values into plan variables.
func planVars(m map[string]string, header bool) []PlanVar {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]PlanVar, 0, len(keys))
	for _, k := range keys {
		v := strings.TrimSpace(m[k])
		pv := PlanVar{Key: k, Value: v, Secret: secretKey.MatchString(k) || (header && strings.EqualFold(k, "Authorization"))}
		if v == "" || placeholder(v) {
			pv.Missing, pv.Value = true, ""
		}
		out = append(out, pv)
	}
	return out
}

var placeholderRe = regexp.MustCompile(`(?i)^(<[^>]*>|\[[^\]]*\]|\$\{[^}]*\}|\{\{[^}]*\}\}|your[-_ ].*|.*your[-_]?(api|token|key|secret).*|x{3,}|\.\.\.|…|changeme|replace[-_ ]?me|todo|tbd)$`)

// placeholder reports whether v is a stand-in for a value the person must
// give (bearer prefix ignored).
func placeholder(v string) bool {
	v = strings.TrimSpace(v)
	if scheme, rest, ok := strings.Cut(v, " "); ok && strings.EqualFold(scheme, "bearer") {
		v = strings.TrimSpace(rest)
	}
	return v == "" || placeholderRe.MatchString(v) || strings.Contains(v, "<") && strings.Contains(v, ">")
}

// splitCommand splits a shell-style command line (quotes and backslash
// escapes, line continuations).
func splitCommand(s string) []string {
	s = strings.ReplaceAll(s, "\\\n", " ")
	var out []string
	var cur strings.Builder
	in := rune(0)
	have := false
	esc := false
	for _, r := range s {
		switch {
		case esc:
			cur.WriteRune(r)
			esc = false
		case r == '\\' && in != '\'':
			esc = true
		case in != 0:
			if r == in {
				in = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			in, have = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if have || cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteRune(r)
		}
	}
	if have || cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// nameFromCommand picks a server name from the package or image a
// command runs.
func nameFromCommand(cmd []string) string {
	skip := map[string]bool{"npx": true, "uvx": true, "uv": true, "run": true, "tool": true, "bunx": true, "pnpm": true, "pnpx": true, "dlx": true, "yarn": true, "exec": true,
		"docker": true, "podman": true, "node": true, "deno": true, "bun": true, "python": true, "python3": true, "pipx": true, "-m": true, "go": true, "java": true, "dotnet": true}
	for i := 0; i < len(cmd); i++ {
		a := cmd[i]
		if skip[path.Base(a)] || strings.HasPrefix(a, "-") {
			// docker run -i --rm -e X image: options with a value
			if a == "-e" || a == "--env" || a == "-v" || a == "--volume" || a == "--name" || a == "-p" {
				i++
			}
			continue
		}
		n := a
		if at := strings.LastIndex(n, "@"); at > 0 {
			n = n[:at] // a version
		}
		if strings.Contains(n, ":") && !strings.Contains(n, "/") {
			n = strings.SplitN(n, ":", 2)[0]
		}
		n = path.Base(strings.TrimSuffix(n, ".js"))
		n = strings.TrimSuffix(strings.TrimSuffix(n, ".py"), "-server")
		for _, pre := range []string{"mcp-server-", "server-", "mcp-"} {
			n = strings.TrimPrefix(n, pre)
		}
		for _, suf := range []string{"-mcp-server", "-mcp", "_mcp"} {
			n = strings.TrimSuffix(n, suf)
		}
		if n != "" && n != "." {
			return n
		}
	}
	return "server"
}

// nameFromURL picks a server name from a URL's host (and path, for hosts
// that serve many).
func nameFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "server"
	}
	common := map[string]bool{"mcp": true, "www": true, "api": true, "app": true, "server": true, "remote": true,
		"com": true, "dev": true, "ai": true, "io": true, "co": true, "org": true, "net": true, "sh": true, "app.": true, "cloud": true, "run": true, "so": true, "xyz": true}
	var keep []string
	for _, l := range strings.Split(strings.ToLower(u.Hostname()), ".") {
		if !common[l] && l != "" {
			keep = append(keep, l)
		}
	}
	switch {
	case len(keep) == 0:
		return "server"
	case len(keep) >= 2 && keep[0] == "docs":
		return keep[1] + "-docs"
	}
	return keep[len(keep)-1]
}

var nameRe = regexp.MustCompile(`[^a-z0-9_-]+`)

// cleanName makes a valid server name ([a-z0-9_-], at most 32).
func cleanName(n string) string {
	n = strings.Trim(nameRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(n)), "-"), "-_")
	if len(n) > 32 {
		n = strings.Trim(n[:32], "-_")
	}
	if n == "" {
		n = "server"
	}
	return n
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}
