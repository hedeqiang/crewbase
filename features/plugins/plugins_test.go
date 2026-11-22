package plugins_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"github.com/hedeqiang/crewbase/capabilities/sandbox"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/capabilities/notify"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/bridge/process"
	"github.com/hedeqiang/crewbase/features/plugins"
	"github.com/hedeqiang/crewbase/kernel"
)

const tok = "test-token-0123456789abcdef0123456789"

type env struct {
	t    *testing.T
	h    *kernel.Host
	base string
}

func start(t *testing.T, dir string, cfg map[string]any) *env {
	t.Helper()
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	var addr string
	gateway.Listening.Listen(h.Root(), func(a string) { addr = a })
	mcfg := map[string]any{"dir": dir, "process": map[string]any{"backoffMs": 100}}
	for k, v := range cfg {
		mcfg[k] = v
	}
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{tools.Plugin, nil}, {prompt.Plugin, nil}, {notify.Plugin, nil}, {credentials.Plugin, nil},
		{passSandbox, nil},
		{gateway.Plugin, map[string]any{"addr": "127.0.0.1:0", "token": tok}},
		{plugins.Plugin, mcfg}, {plugins.APIPlugin, nil},
	} {
		if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range h.Plugins() {
		if p.State != kernel.StateActive {
			t.Fatalf("%s: %s %s %v", p.ID, p.State, p.Error, p.Missing)
		}
	}
	return &env{t: t, h: h, base: "http://" + addr}
}

func (e *env) do(method, path string, body any) (int, map[string]any) {
	e.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, e.base+path, rd)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (e *env) must(method, path string, body any) map[string]any {
	e.t.Helper()
	code, out := e.do(method, path, body)
	if code != 200 {
		e.t.Fatalf("%s %s: %d %v", method, path, code, out)
	}
	return out
}

func (e *env) hasTool(name string) bool {
	_, ok := kernel.Use[*tools.Registry](e.h.Root(), tools.ServiceKey).Get("", name)
	return ok
}

func (e *env) run(name, args string) tools.Execution {
	reg := kernel.Use[*tools.Registry](e.h.Root(), tools.ServiceKey)
	return reg.Run(context.Background(), e.h.Root(), tools.Call{ID: "c1", Name: name, Args: json.RawMessage(args)})
}

func state(v map[string]any) string { s, _ := v["state"].(string); return s }

func pid(v map[string]any) int {
	p, _ := v["process"].(map[string]any)
	f, _ := p["pid"].(float64)
	return int(f)
}

func needs(v map[string]any) string {
	b, _ := json.Marshal(v["needsApproval"])
	return string(b)
}

func needPython(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
}

func repo(t *testing.T, rel string) string {
	p, err := filepath.Abs(filepath.Join("../..", rel))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestInstallApproveEnableDisable(t *testing.T) {
	needPython(t)
	dir := t.TempDir()
	e := start(t, dir, nil)
	src := repo(t, "examples/plugins/hello-py")

	v := e.must("POST", "/api/plugins/install", map[string]any{"source": src})
	if state(v) != plugins.StatePendingApproval || needs(v) != `["service:tools"]` {
		t.Fatalf("install: %v", v)
	}
	if e.hasTool("py_hello") {
		t.Fatal("unapproved plugin registered a tool")
	}
	if _, err := os.Stat(filepath.Join(dir, "hello-py", "0.1.0", "crewbase-plugin.toml")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(src, ".crewbase-sdk")); err == nil {
		t.Fatal("install wrote into the source directory")
	}
	list := e.must("GET", "/api/plugins/installed", nil)
	if ps, _ := list["plugins"].([]any); len(ps) != 1 {
		t.Fatalf("installed: %v", list)
	}

	v = e.must("POST", "/api/plugins/hello-py/approve", map[string]any{})
	if state(v) != "ACTIVE" || v["approved"] != true {
		t.Fatalf("approve: %v", v)
	}
	if got := e.run("py_hello", `{"name":"api"}`).Result.Content; !strings.Contains(got, "Hello, api!") {
		t.Fatalf("tool: %q", got)
	}
	p1 := pid(v)

	v = e.must("POST", "/api/plugins/hello-py/disable", nil)
	if state(v) != plugins.StateDisabled || e.hasTool("py_hello") {
		t.Fatalf("disable: %v", v)
	}
	if p1 == 0 || syscall.Kill(p1, 0) == nil {
		time.Sleep(200 * time.Millisecond)
		if syscall.Kill(p1, 0) == nil {
			t.Fatalf("process %d still alive after disable", p1)
		}
	}
	v = e.must("POST", "/api/plugins/hello-py/enable", nil)
	if state(v) != "ACTIVE" || !e.hasTool("py_hello") {
		t.Fatalf("enable: %v", v)
	}

	// enabled + approved plugins start with the host
	e.h.Close()
	e2 := start(t, dir, nil)
	deadline := time.Now().Add(5 * time.Second)
	for !e2.hasTool("py_hello") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !e2.hasTool("py_hello") {
		t.Fatalf("not restored at startup: %v", e2.must("GET", "/api/plugins/installed", nil))
	}

	e2.must("DELETE", "/api/plugins/hello-py", nil)
	if e2.hasTool("py_hello") {
		t.Fatal("tool survived uninstall")
	}
	if _, err := os.Stat(filepath.Join(dir, "hello-py")); !os.IsNotExist(err) {
		t.Fatal("files survived uninstall")
	}
	if code, _ := e2.do("POST", "/api/plugins/hello-py/approve", nil); code != 404 {
		t.Fatalf("approve after uninstall: %d", code)
	}
	if code, _ := e2.do("POST", "/api/plugins/install", map[string]any{"source": "/does/not/exist"}); code != 400 {
		t.Fatalf("bad source: %d", code)
	}
}

const verMain = `
from crewbase_plugin import Plugin
p = Plugin("versioned")
@p.tool("ver_tool")
def t(a, c): return "VERSION"
p.run()
`

func writeVersion(t *testing.T, dir, version, extra string) {
	t.Helper()
	m := "name = \"versioned\"\nversion = \"" + version + "\"\nentry = [\"python3\", \"main.py\"]\n[inject]\nservices = [\"tools\"" + extra + "]\n"
	if err := os.WriteFile(filepath.Join(dir, "crewbase-plugin.toml"), []byte(m), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte(strings.Replace(verMain, "VERSION", version, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNewVersionNeedsReapproval(t *testing.T) {
	needPython(t)
	e := start(t, t.TempDir(), nil)
	src := t.TempDir()
	writeVersion(t, src, "0.1.0", "")
	e.must("POST", "/api/plugins/install", map[string]any{"source": src})
	if v := e.must("POST", "/api/plugins/versioned/approve", map[string]any{"trustFutureVersions": false}); state(v) != "ACTIVE" {
		t.Fatalf("approve: %v", v)
	}

	writeVersion(t, src, "0.2.0", "")
	v := e.must("POST", "/api/plugins/versioned/reload", nil)
	if state(v) != plugins.StatePendingApproval || !strings.Contains(needs(v), "version 0.2.0 (approved 0.1.0)") || e.hasTool("ver_tool") {
		t.Fatalf("new version without trust: %v", v)
	}
	if v := e.must("POST", "/api/plugins/versioned/approve", map[string]any{"trustFutureVersions": true}); state(v) != "ACTIVE" {
		t.Fatalf("approve 0.2.0: %v", v)
	}
	if got := e.run("ver_tool", `{}`).Result.Content; got != "0.2.0" {
		t.Fatalf("ver_tool: %q", got)
	}

	// trusted: same permissions, new version starts without asking
	writeVersion(t, src, "0.3.0", "")
	if v := e.must("POST", "/api/plugins/versioned/reload", nil); state(v) != "ACTIVE" {
		t.Fatalf("trusted upgrade: %v", v)
	}
	if got := e.run("ver_tool", `{}`).Result.Content; got != "0.3.0" {
		t.Fatalf("ver_tool: %q", got)
	}

	// trust never covers new permissions
	writeVersion(t, src, "0.4.0", `, "notify"`)
	v = e.must("POST", "/api/plugins/versioned/reload", nil)
	if state(v) != plugins.StatePendingApproval || needs(v) != `["service:notify"]` {
		t.Fatalf("grown permissions: %v", v)
	}
	// a partial grant keeps it pending
	v = e.must("POST", "/api/plugins/versioned/approve", map[string]any{"permissions": map[string]any{"services": []string{"tools"}}})
	if state(v) != plugins.StatePendingApproval {
		t.Fatalf("partial grant: %v", v)
	}
	if v := e.must("POST", "/api/plugins/versioned/approve", map[string]any{}); state(v) != "ACTIVE" {
		t.Fatalf("full grant: %v", v)
	}
}

func tarball(t *testing.T, src, prefix string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		b, _ := os.ReadFile(p)
		_ = tw.WriteHeader(&tar.Header{Name: prefix + filepath.ToSlash(rel), Mode: 0o644, Size: int64(len(b)), Typeflag: tar.TypeReg})
		_, _ = tw.Write(b)
		return nil
	})
	_ = tw.Close()
	_ = gz.Close()
	out := filepath.Join(t.TempDir(), "plugin.tgz")
	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestInstallTarball(t *testing.T) {
	needPython(t)
	e := start(t, t.TempDir(), nil)
	tgz := tarball(t, repo(t, "examples/plugins/hello-py"), "package/")
	v := e.must("POST", "/api/plugins/install", map[string]any{"source": tgz})
	if v["name"] != "hello-py" || v["sourceKind"] != "tarball" || state(v) != plugins.StatePendingApproval {
		t.Fatalf("tarball: %v", v)
	}
	e.must("POST", "/api/plugins/hello-py/approve", nil)
	if !e.hasTool("py_hello") {
		t.Fatal("tool missing")
	}
	// path traversal is refused
	evil := t.TempDir()
	_ = os.WriteFile(filepath.Join(evil, "x"), []byte("x"), 0o644)
	if code, out := e.do("POST", "/api/plugins/install", map[string]any{"source": tarball(t, evil, "../../")}); code != 400 {
		t.Fatalf("traversal: %d %v", code, out)
	}
}

func TestDefaultMarket(t *testing.T) {
	e := start(t, t.TempDir(), map[string]any{"marketBase": repo(t, "")})
	out := e.must("GET", "/api/market", nil)
	ps, _ := out["plugins"].([]any)
	found := false
	for _, x := range ps {
		p := x.(map[string]any)
		src, _ := p["source"].(string)
		if _, err := os.Stat(filepath.Join(src, "crewbase-plugin.toml")); err != nil {
			t.Fatalf("market source %q: %v", src, err)
		}
		if p["name"] == "word-count" {
			found = true
		}
	}
	if !found {
		t.Fatalf("market: %v", out)
	}
}

// Phase 2 exit criterion: a third-party TypeScript plugin is installed from
// the marketplace, authorized per its manifest and hot-reloaded.
func TestPhase2MarketInstallApproveReload(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available")
	}
	// a local marketplace with a working copy of the example as its source
	mk := t.TempDir()
	src := filepath.Join(mk, "word-count")
	copyDir(t, repo(t, "examples/plugins/word-count"), src)
	idx := `[{"name":"word-count","description":"test","version":"0.1.0","source":"./word-count",` +
		`"permissions":{"services":["tools"],"events":["tools/result"]}}]`
	if err := os.WriteFile(filepath.Join(mk, "market.json"), []byte(idx), 0o644); err != nil {
		t.Fatal(err)
	}
	e := start(t, t.TempDir(), map[string]any{"marketURL": filepath.Join(mk, "market.json")})

	m := e.must("GET", "/api/market", nil)
	if ps := m["plugins"].([]any); len(ps) != 1 || ps[0].(map[string]any)["source"] != src {
		t.Fatalf("market: %v", m)
	}
	v := e.must("POST", "/api/plugins/install", map[string]any{"market": "word-count"})
	if state(v) != plugins.StatePendingApproval || needs(v) != `["service:tools","event:tools/result"]` {
		t.Fatalf("install: %v", v)
	}
	v = e.must("POST", "/api/plugins/word-count/approve", map[string]any{})
	if state(v) != "ACTIVE" {
		t.Fatalf("approve: %v", v)
	}
	if got := e.run("word_count", `{"text":"one two three"}`).Result.Content; !strings.HasPrefix(got, "words: 3,") {
		t.Fatalf("before reload: %q", got)
	}
	p1 := pid(v)

	// edit the source, reload: old registrations revoked, new code registered
	js := filepath.Join(src, "index.js")
	b, _ := os.ReadFile(js)
	b = bytes.Replace(b, []byte(`const LABEL = "words";`), []byte(`const LABEL = "WORDS";`), 1)
	if err := os.WriteFile(js, b, 0o644); err != nil {
		t.Fatal(err)
	}
	v = e.must("POST", "/api/plugins/word-count/reload", nil)
	if state(v) != "ACTIVE" || pid(v) == p1 {
		t.Fatalf("reload: %v", v)
	}
	if got := e.run("word_count", `{"text":"one two three"}`).Result.Content; !strings.HasPrefix(got, "WORDS: 3,") {
		t.Fatalf("after reload: %q", got)
	}
	if out := e.must("GET", "/api/market", nil); out["plugins"].([]any)[0].(map[string]any)["installed"] != "0.1.0" {
		t.Fatalf("market installed flag: %v", out)
	}
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			if d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Marketplace packages can be pinned and signed by a trusted publisher.
func TestSignedPackages(t *testing.T) {
	needPython(t)
	tgz := tarball(t, repo(t, "examples/plugins/hello-py"), "package/")
	pub, priv, err := plugins.Keygen()
	if err != nil {
		t.Fatal(err)
	}
	sig, integ, err := plugins.Sign(priv, tgz, "hello-py", "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	otherPub, otherPriv, _ := plugins.Keygen()
	badSig, _, _ := plugins.Sign(otherPriv, tgz, "hello-py", "0.1.0")
	wrongVer, _, _ := plugins.Sign(priv, tgz, "hello-py", "9.9.9")
	_ = otherPub

	install := func(entry map[string]any, cfg map[string]any) (int, map[string]any) {
		dir := t.TempDir()
		idx := filepath.Join(dir, "market.json")
		entry["name"], entry["source"], entry["version"] = "hello-py", tgz, "0.1.0"
		b, _ := json.Marshal([]any{entry})
		os.WriteFile(idx, b, 0o644)
		c := map[string]any{"marketURL": idx, "publishers": []map[string]any{{"id": "acme", "name": "Acme 工作室", "key": pub}}}
		for k, v := range cfg {
			c[k] = v
		}
		e := start(t, t.TempDir(), c)
		return e.do("POST", "/api/plugins/install", map[string]any{"market": "hello-py"})
	}
	// pinned + signed by a trusted publisher → verified
	code, v := install(map[string]any{"integrity": integ, "signed": map[string]any{"publisher": "acme", "signature": sig}}, nil)
	p, _ := v["publisher"].(map[string]any)
	if code != 200 || p["name"] != "Acme 工作室" || p["key"] != nil || v["integrity"] != integ {
		t.Fatalf("verified install: %d %v", code, v)
	}
	// integrity mismatch
	if code, v := install(map[string]any{"integrity": "sha256-" + strings.Repeat("A", 43) + "="}, nil); code == 200 || !strings.Contains(fmt.Sprint(v), "pinned integrity") {
		t.Fatalf("integrity: %d %v", code, v)
	}
	// signature by a key that is not the publisher's
	if code, v := install(map[string]any{"signed": map[string]any{"publisher": "acme", "signature": badSig}}, nil); code == 200 || !strings.Contains(fmt.Sprint(v), "does not match") {
		t.Fatalf("bad signature: %d %v", code, v)
	}
	// a signature for another version does not carry over
	if code, _ := install(map[string]any{"signed": map[string]any{"publisher": "acme", "signature": wrongVer}}, nil); code == 200 {
		t.Fatal("signature for another version accepted")
	}
	// unknown publisher
	if code, v := install(map[string]any{"signed": map[string]any{"publisher": "mallory", "signature": sig}}, nil); code == 200 || !strings.Contains(fmt.Sprint(v), "not trusted") {
		t.Fatalf("unknown publisher: %d %v", code, v)
	}
	// unsigned: fine by default (no publisher), refused with requireSigned
	if code, v := install(map[string]any{}, nil); code != 200 || v["publisher"] != nil {
		t.Fatalf("unsigned: %d %v", code, v)
	}
	if code, v := install(map[string]any{}, map[string]any{"requireSigned": true}); code == 200 || !strings.Contains(fmt.Sprint(v), "requireSigned") {
		t.Fatalf("requireSigned: %d %v", code, v)
	}
}

// A wasm plugin installs like any other, asks to approve its hooks, and
// then runs in the host (here: masking ticket ids in tool results).
func TestWasmPlugin(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}
	src := t.TempDir()
	ex := repo(t, "examples/plugins/wasm-redact")
	for _, f := range []string{"crewbase-plugin.toml", "main.go", "go.mod"} {
		b, _ := os.ReadFile(filepath.Join(ex, f))
		os.WriteFile(filepath.Join(src, f), b, 0o644)
	}
	cmd := exec.Command(goBin, "build", "-buildmode=c-shared", "-o", "plugin.wasm", ".")
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "GOWORK=off")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot build the wasm example: %v %s", err, b)
	}
	e := start(t, t.TempDir(), nil)
	v := e.must("POST", "/api/plugins/install", map[string]any{"source": src})
	if state(v) != plugins.StatePendingApproval || !strings.Contains(needs(v), "hook:tool_result") {
		t.Fatalf("install: %v", v)
	}
	v = e.must("POST", "/api/plugins/wasm-redact/approve", nil)
	if v["error"] != nil && v["error"] != "" {
		t.Fatalf("approve: %v", v)
	}
	reg := kernel.Use[*tools.Registry](e.h.Root(), tools.ServiceKey)
	reg.Register(e.h.Root(), &tools.Tool{Name: "logs", Category: tools.CategoryRead, Description: "x",
		Execute: func(context.Context, tools.Call) (tools.Result, error) { return tools.Text("see INC-31415"), nil }})
	if x := e.run("logs", `{}`); x.Result.Content != "see INC-•••" {
		t.Fatalf("not applied: %+v", x.Result)
	}
	// disabling removes the hooks at once
	e.must("POST", "/api/plugins/wasm-redact/disable", nil)
	if x := e.run("logs", `{}`); x.Result.Content != "see INC-31415" {
		t.Fatalf("still applied after disable: %+v", x.Result)
	}
}

// passSandbox provides a sandbox that runs commands as they are (plugins
// fail closed without one).
var passSandbox = kernel.Spec{Name: "pass-sandbox", Apply: func(c *kernel.Context) error {
	return c.Provide(sandbox.ServiceKey, sandbox.Sandbox(passBox{}))
}}

type passBox struct{}

func (passBox) Name() string { return "pass" }
func (passBox) Check(context.Context, sandbox.Policy) (string, error) {
	return sandbox.EnforcementPartial, nil
}
func (passBox) Confine(_ context.Context, argv []string, _ string, env []string, _ sandbox.Policy) (sandbox.Confined, error) {
	return sandbox.Confined{Argv: argv, Env: env, Enforcement: sandbox.EnforcementPartial}, nil
}

// A newer marketplace version is offered and installed; new permissions
// keep it stopped until approved; the person's own copy is never offered.
func TestMarketUpdates(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available")
	}
	mk := t.TempDir()
	v1, v2 := filepath.Join(mk, "wc-1"), filepath.Join(mk, "wc-2")
	copyDir(t, repo(t, "examples/plugins/word-count"), v1)
	copyDir(t, repo(t, "examples/plugins/word-count"), v2)
	man := filepath.Join(v2, "crewbase-plugin.toml")
	b, _ := os.ReadFile(man)
	b = bytes.Replace(b, []byte(`version = "0.1.0"`), []byte(`version = "0.2.0"`), 1)
	if err := os.WriteFile(man, b, 0o644); err != nil {
		t.Fatal(err)
	}
	idx := filepath.Join(mk, "market.json")
	write := func(src, ver, perms string) {
		s := `[{"name":"word-count","description":"test","version":"` + ver + `","source":"./` + src + `","permissions":` + perms + `}]`
		if err := os.WriteFile(idx, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	perms := `{"services":["tools"],"events":["tools/result"]}`
	write("wc-1", "0.1.0", perms)
	e := start(t, t.TempDir(), map[string]any{"marketURL": idx})
	e.must("POST", "/api/plugins/install", map[string]any{"market": "word-count"})
	e.must("POST", "/api/plugins/word-count/approve", map[string]any{})
	if u := e.must("GET", "/api/plugins/updates", nil)["updates"].([]any); len(u) != 0 {
		t.Fatalf("no update yet: %v", u)
	}
	// 0.2.0 in the marketplace, same permissions: offered, updated, still running
	write("wc-2", "0.2.0", perms)
	u := e.must("GET", "/api/plugins/updates", nil)["updates"].([]any)
	if len(u) != 1 || u[0].(map[string]any)["latest"] != "0.2.0" || u[0].(map[string]any)["newPermissions"] != nil {
		t.Fatalf("updates: %v", u)
	}
	v := e.must("POST", "/api/plugins/word-count/update", nil)
	if v["version"] != "0.2.0" || state(v) != "ACTIVE" {
		t.Fatalf("update: %v", v)
	}
	if u := e.must("GET", "/api/plugins/updates", nil)["updates"].([]any); len(u) != 0 {
		t.Fatalf("after update: %v", u)
	}
	// a newer version asking for more is flagged
	man2 := filepath.Join(v1, "crewbase-plugin.toml")
	b, _ = os.ReadFile(man2)
	b = bytes.Replace(b, []byte(`version = "0.1.0"`), []byte(`version = "0.3.0"`), 1)
	_ = os.WriteFile(man2, b, 0o644)
	write("wc-1", "0.3.0", `{"services":["tools","notify"],"events":["tools/result"]}`)
	u = e.must("GET", "/api/plugins/updates", nil)["updates"].([]any)
	if len(u) != 1 || fmt.Sprint(u[0].(map[string]any)["newPermissions"]) != "[service:notify]" {
		t.Fatalf("new permissions: %v", u)
	}
}

func TestVersionNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"0.2.0", "0.1.9", true}, {"1.0.0", "1.0.0", false}, {"1.10.0", "1.9.0", true}, {"1.0.0", "1.0.0-rc.1", true}, {"1.0.0-rc.1", "1.0.0", false}, {"v2", "1.9.9", true}} {
		if got := plugins.VersionNewer(c.a, c.b); got != c.want {
			t.Errorf("%s > %s = %v", c.a, c.b, got)
		}
	}
}

// An approval vouches for a package from where it came: the same name and
// version from another source is asked about again.
func TestAnotherSourceNeedsReapproval(t *testing.T) {
	approved := &plugins.Grant{Version: "1.0.0", Origin: "publisher:acme"}
	man := process.Manifest{Name: "foo", Version: "1.0.0"}
	ok, needs := plugins.ApprovalFor(man, plugins.Record{Publisher: "acme"}, approved)
	if !ok {
		t.Fatalf("same publisher: %v", needs)
	}
	ok, needs = plugins.ApprovalFor(man, plugins.Record{SourceKind: "tarball", Source: "https://example.com/foo.tgz"}, approved)
	if ok || len(needs) == 0 {
		t.Fatal("an unsigned package from elsewhere kept the publisher's approval")
	}
	// approvals made before origins were recorded still hold
	if ok, _ := plugins.ApprovalFor(man, plugins.Record{SourceKind: "local", Source: "/x"}, &plugins.Grant{Version: "1.0.0"}); !ok {
		t.Fatal("an older approval was dropped")
	}
}
