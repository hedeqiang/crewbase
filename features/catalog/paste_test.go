package catalog

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/kernel"
)

func TestDetectConfigs(t *testing.T) {
	s := &Service{}
	cases := []struct {
		name, in string
		check    func(t *testing.T, p Plan)
	}{
		{"claude desktop", `{"mcpServers": {
			"github": {"command": "npx", "args": ["-y", "@modelcontextprotocol/server-github"], "env": {"GITHUB_PERSONAL_ACCESS_TOKEN": "<YOUR_TOKEN>"}},
			"linear": {"url": "https://mcp.linear.app/mcp"}
		}}`, func(t *testing.T, p Plan) {
			if p.Kind != "mcp" || len(p.Servers) != 2 {
				t.Fatalf("%+v", p)
			}
			gh, ln := p.Servers[0], p.Servers[1]
			if gh.Name != "github" || gh.Transport != "stdio" || strings.Join(gh.Command, " ") != "npx -y @modelcontextprotocol/server-github" {
				t.Fatalf("github %+v", gh)
			}
			if len(gh.Env) != 1 || !gh.Env[0].Secret || !gh.Env[0].Missing || gh.Env[0].Value != "" {
				t.Fatalf("env %+v", gh.Env)
			}
			if ln.Transport != "http" || !ln.OAuth || ln.URL != "https://mcp.linear.app/mcp" {
				t.Fatalf("linear %+v", ln)
			}
		}},
		{"vs code servers", `{"servers": {"Fetch Tool": {"type": "stdio", "command": "uvx", "args": ["mcp-server-fetch"]}}}`, func(t *testing.T, p Plan) {
			if len(p.Servers) != 1 || p.Servers[0].Name != "fetch-tool" || p.Servers[0].Command[0] != "uvx" {
				t.Fatalf("%+v", p.Servers)
			}
		}},
		{"fragment", `"context7": {"url": "https://mcp.context7.com/mcp", "headers": {"CONTEXT7_API_KEY": "ctx7-abc123"}}`, func(t *testing.T, p Plan) {
			srv := p.Servers[0]
			if srv.Name != "context7" || srv.OAuth || len(srv.Headers) != 1 || !srv.Headers[0].Secret || srv.Headers[0].Missing || srv.Headers[0].Value != "ctx7-abc123" {
				t.Fatalf("%+v", srv)
			}
		}},
		{"one server", `{"command": "docker", "args": ["run", "-i", "--rm", "-e", "GITHUB_TOKEN", "ghcr.io/github/github-mcp-server"]}`, func(t *testing.T, p Plan) {
			if p.Servers[0].Name != "github" {
				t.Fatalf("name %q", p.Servers[0].Name)
			}
		}},
		{"claude mcp add http", `claude mcp add --transport http notion https://mcp.notion.com/mcp`, func(t *testing.T, p Plan) {
			srv := p.Servers[0]
			if srv.Name != "notion" || srv.Transport != "http" || srv.URL != "https://mcp.notion.com/mcp" || !srv.OAuth {
				t.Fatalf("%+v", srv)
			}
		}},
		{"claude mcp add stdio", `claude mcp add airtable --env AIRTABLE_API_KEY=YOUR_KEY -- npx -y airtable-mcp-server`, func(t *testing.T, p Plan) {
			srv := p.Servers[0]
			if srv.Name != "airtable" || strings.Join(srv.Command, " ") != "npx -y airtable-mcp-server" || !srv.Env[0].Missing || !srv.Env[0].Secret {
				t.Fatalf("%+v", srv)
			}
		}},
		{"command with env", `BRAVE_API_KEY=abc npx -y @modelcontextprotocol/server-brave-search`, func(t *testing.T, p Plan) {
			srv := p.Servers[0]
			if srv.Name != "brave-search" || srv.Env[0].Key != "BRAVE_API_KEY" || srv.Env[0].Value != "abc" || !srv.Env[0].Secret {
				t.Fatalf("%+v", srv)
			}
		}},
		{"uvx", `uvx mcp-server-git --repository "/tmp/my repo"`, func(t *testing.T, p Plan) {
			srv := p.Servers[0]
			if srv.Name != "git" || srv.Command[3] != "/tmp/my repo" {
				t.Fatalf("%+v", srv)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := s.Detect(context.Background(), c.in)
			if err != nil {
				t.Fatal(err)
			}
			c.check(t, p)
		})
	}
	for _, bad := range []string{"", "hello there", `{"foo": 1}`, "{not json"} {
		if _, err := s.Detect(context.Background(), bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

func TestPlaceholdersAndNames(t *testing.T) {
	for _, v := range []string{"<your-token>", "YOUR_API_KEY", "your-key-here", "xxxx", "${env:TOKEN}", "Bearer <token>", "", "..."} {
		if !placeholder(v) {
			t.Errorf("%q should be a placeholder", v)
		}
	}
	for _, v := range []string{"ghp_abc123", "Bearer sk-1", "/Users/me/notes", "true"} {
		if placeholder(v) {
			t.Errorf("%q is a value", v)
		}
	}
	for in, want := range map[string]string{
		"https://mcp.notion.com/mcp":          "notion",
		"https://docs.mcp.cloudflare.com/mcp": "cloudflare-docs",
		"https://huggingface.co/mcp":          "huggingface",
		"https://api.githubcopilot.com/mcp/":  "githubcopilot",
	} {
		if got := nameFromURL(in); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}

func TestDetectMCPURL(t *testing.T) {
	open := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","serverInfo":{"name":"x"}}}`))
	}))
	defer open.Close()
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="x"`)
		w.WriteHeader(401)
	}))
	defer login.Close()
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html>docs</html>")) }))
	defer page.Close()
	s := &Service{}
	p, err := s.Detect(context.Background(), open.URL+"/mcp")
	if err != nil || p.Servers[0].OAuth || p.Servers[0].Transport != "http" {
		t.Fatalf("open: %+v %v", p, err)
	}
	p, err = s.Detect(context.Background(), login.URL+"/mcp")
	if err != nil || !p.Servers[0].OAuth {
		t.Fatalf("login: %+v %v", p, err)
	}
	if _, err := s.Detect(context.Background(), page.URL); err == nil {
		t.Fatal("a web page detected as an MCP server")
	}
}

// tarball builds a GitHub-style archive (one top folder) of files.
func tarball(t *testing.T, files map[string]string) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, body := range files {
		_ = tw.WriteHeader(&tar.Header{Name: "repo-main/" + name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(body))
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

func TestDetectAndInstallGitHubSkills(t *testing.T) {
	arc := tarball(t, map[string]string{
		"README.md":                  "# skills",
		"skills/pdf/SKILL.md":        "---\nname: pdf\ndescription: Work with PDF files\n---\nUse pypdf.",
		"skills/pdf/scripts/fill.py": "print('fill')",
		"skills/docx/SKILL.md":       "---\nname: docx\ndescription: Word documents\n---\n...",
	})
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/acme/skills/archive/HEAD.tar.gz" && r.URL.Path != "/acme/skills/archive/main.tar.gz" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(arc)
	}))
	defer gh.Close()
	old := githubWeb
	githubWeb = gh.URL
	t.Cleanup(func() { githubWeb = old })

	e := newEnv(t)
	s := kernelService(t, e)
	p, err := s.Detect(context.Background(), "https://github.com/acme/skills")
	if err != nil || p.Kind != "skill" || len(p.Skills) != 2 || p.Skills[0].Name != "docx" || p.Skills[1].Path != "skills/pdf" {
		t.Fatalf("%+v %v", p, err)
	}
	// a folder link narrows it to that skill
	p, err = s.Detect(context.Background(), "https://github.com/acme/skills/tree/main/skills/pdf")
	if err != nil || len(p.Skills) != 1 || p.Skills[0].Path != "" || p.Skills[0].Description != "Work with PDF files" {
		t.Fatalf("folder: %+v %v", p, err)
	}
	got, err := s.Apply(context.Background(), ApplyRequest{Kind: "skill", Source: "https://github.com/acme/skills", Skills: []string{"skills/pdf"}})
	if err != nil || len(got.Skills) != 1 {
		t.Fatalf("apply %+v %v", got, err)
	}
	b, err := os.ReadFile(filepath.Join(got.Skills[0].Dir, "scripts", "fill.py"))
	if err != nil || string(b) != "print('fill')" {
		t.Fatalf("script not installed: %v", err)
	}
	// installed now: shown as such, and a second install says so
	p, _ = s.Detect(context.Background(), "https://github.com/acme/skills")
	if !p.Skills[1].Exists {
		t.Fatalf("%+v", p.Skills)
	}
	if _, err := s.Apply(context.Background(), ApplyRequest{Kind: "skill", Source: "https://github.com/acme/skills", Skills: []string{"skills/pdf"}}); err == nil {
		t.Fatal("installed twice")
	}
}

func TestDetectLocalAndPlugin(t *testing.T) {
	s := &Service{}
	dir := t.TempDir()
	sk := filepath.Join(dir, "my-skill")
	_ = os.MkdirAll(sk, 0o755)
	_ = os.WriteFile(filepath.Join(sk, "SKILL.md"), []byte("---\nname: my-skill\ndescription: d\n---\nx"), 0o644)
	p, err := s.Detect(context.Background(), sk)
	if err != nil || p.Kind != "skill" || p.Skills[0].Name != "my-skill" {
		t.Fatalf("%+v %v", p, err)
	}
	p, err = s.Detect(context.Background(), dir) // a folder of skills
	if err != nil || len(p.Skills) != 1 || p.Skills[0].Path != "my-skill" {
		t.Fatalf("%+v %v", p, err)
	}
	pl := filepath.Join(dir, "plug")
	_ = os.MkdirAll(pl, 0o755)
	_ = os.WriteFile(filepath.Join(pl, pluginManifest), []byte("name = \"p\""), 0o644)
	if p, err := s.Detect(context.Background(), pl); err != nil || p.Kind != "plugin" || p.Plugin != pl {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := s.Detect(context.Background(), filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing path detected")
	}
}

func TestDetectNpm(t *testing.T) {
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/@acme%2fcrew-plugin/latest", "/@acme/crew-plugin/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{"keywords": []string{"crewbase-plugin"}})
		case "/@acme%2fmcp-server-notes/latest", "/@acme/mcp-server-notes/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{"keywords": []string{"mcp"}, "bin": "x.js"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer reg.Close()
	old := npmRegistry
	npmRegistry = reg.URL
	t.Cleanup(func() { npmRegistry = old })
	s := &Service{}
	if p, err := s.Detect(context.Background(), "@acme/crew-plugin"); err != nil || p.Kind != "plugin" {
		t.Fatalf("%+v %v", p, err)
	}
	p, err := s.Detect(context.Background(), "https://www.npmjs.com/package/@acme/mcp-server-notes")
	if err != nil || p.Kind != "mcp" || p.Servers[0].Name != "notes" || strings.Join(p.Servers[0].Command, " ") != "npx -y @acme/mcp-server-notes" {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := s.Detect(context.Background(), "no-such-package-zz"); err == nil {
		t.Fatal("unknown package detected")
	}
}

func TestApplyServersKeepsSecretsInTheVault(t *testing.T) {
	e := newEnv(t)
	s := kernelService(t, e)
	p, err := s.Detect(context.Background(), `{"mcpServers": {"notes": {"command": "/nonexistent/notes-mcp", "env": {"NOTES_TOKEN": "<token>", "NOTES_DIR": "/tmp"}},
		"remote": {"url": "https://example.invalid/mcp", "headers": {"Authorization": "Bearer abc-123"}}}}`)
	if err != nil {
		t.Fatal(err)
	}
	// a placeholder left unfilled is refused, and nothing is added
	if _, err := s.Apply(context.Background(), ApplyRequest{Kind: "mcp", Servers: p.Servers}); err == nil || len(e.mcp.List()) != 0 {
		t.Fatalf("applied with a missing value: %v", err)
	}
	for i := range p.Servers[0].Env {
		if p.Servers[0].Env[i].Key == "NOTES_TOKEN" {
			p.Servers[0].Env[i].Value = "tok-secret-1"
		}
	}
	got, err := s.Apply(context.Background(), ApplyRequest{Kind: "mcp", Servers: p.Servers})
	if err != nil || len(got.Servers) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	raw, _ := os.ReadFile(filepath.Join(e.dir, "mcp.json"))
	if bytes.Contains(raw, []byte("tok-secret-1")) || bytes.Contains(raw, []byte("abc-123")) {
		t.Fatalf("a secret in mcp.json: %s", raw)
	}
	if !bytes.Contains(raw, []byte("vault:crewbase/mcp-notes-notes-token")) || !bytes.Contains(raw, []byte(`Bearer ${vault:crewbase/mcp-remote-authorization}`)) {
		t.Fatalf("refs missing: %s", raw)
	}
	if v, _ := e.creds.Resolve(context.Background(), "vault:crewbase/mcp-remote-authorization"); v != "abc-123" {
		t.Fatalf("header secret %q", v)
	}
	if !bytes.Contains(raw, []byte(`"NOTES_DIR": "/tmp"`)) {
		t.Fatalf("plain value lost: %s", raw)
	}
}

func kernelService(t *testing.T, e *env) *Service {
	t.Helper()
	return kernel.Use[*Service](e.h.Root(), ServiceKey)
}
