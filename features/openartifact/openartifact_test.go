package openartifact

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

type entry struct {
	name, body string
	typ        byte
	link       string
}

func tgz(t *testing.T, es []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range es {
		h := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: e.typ, Linkname: e.link}
		if e.typ == 0 {
			h.Typeflag = tar.TypeReg
		}
		if h.Typeflag != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			tw.Write([]byte(e.body))
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

var goodPkg = []entry{
	{name: "package/package.json", body: `{"name":"@open-artifact/cli","version":"9.9.9"}`},
	{name: "package/scripts/open-artifact.mjs", body: "// runtime\n"},
	{name: "package/link", typ: tar.TypeSymlink, link: "/etc/passwd"},
}

func TestUntarRefusesEscapes(t *testing.T) {
	for _, bad := range []string{"../evil", "package/../../evil", "/abs/evil", `package\..\evil`} {
		dir := t.TempDir()
		f := filepath.Join(t.TempDir(), "x.tgz")
		os.WriteFile(f, tgz(t, []entry{{name: bad, body: "x"}}), 0o644)
		if err := untar(f, dir); err == nil {
			t.Errorf("%s: unpacked", bad)
		}
	}
	dir := t.TempDir()
	f := filepath.Join(t.TempDir(), "x.tgz")
	os.WriteFile(f, tgz(t, goodPkg), 0o644)
	if err := untar(f, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "package", "link")); !os.IsNotExist(err) {
		t.Errorf("a link was unpacked: %v", err)
	}
	if ok, err := runtimeAt(filepath.Join(dir, "package")); !ok {
		t.Fatal(err)
	}
}

// registry serves a packument for 9.9.9 with the given integrity.
func registry(t *testing.T, body []byte, integrity string) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".tgz"):
			w.Write(body)
		case strings.Contains(r.URL.Path, "open-artifact"):
			json.NewEncoder(w).Encode(map[string]any{
				"dist-tags": map[string]string{"latest": "9.9.9"},
				"versions": map[string]any{"9.9.9": map[string]any{"dist": map[string]string{
					"tarball": srv.URL + "/cli-9.9.9.tgz", "integrity": integrity}}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestInstallChecksIntegrity(t *testing.T) {
	body := tgz(t, goodPkg)
	sum := sha512.Sum512(body)
	good := "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
	ctx := context.Background()

	apps := t.TempDir()
	srv := registry(t, body, "sha512-"+base64.StdEncoding.EncodeToString(make([]byte, 64)))
	rel, err := latest(ctx, srv.Client(), srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := install(ctx, srv.Client(), apps, rel); err == nil || !strings.Contains(err.Error(), "sha512") {
		t.Fatalf("a mismatching download: %v", err)
	}
	if v := installed(apps); len(v) != 0 {
		t.Fatalf("installed after a mismatch: %v", v)
	}
	if ents, _ := os.ReadDir(apps); len(ents) != 0 {
		t.Fatalf("left behind: %v", ents)
	}

	srv = registry(t, body, "sha1-abc")
	if _, err := latest(ctx, srv.Client(), srv.URL, ""); err == nil {
		t.Fatal("a version without sha512 was accepted")
	}

	srv = registry(t, body, good)
	rel, err = latest(ctx, srv.Client(), srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := install(ctx, srv.Client(), apps, rel); err != nil {
		t.Fatal(err)
	}
	if v := installed(apps); len(v) != 1 || v[0] != "9.9.9" {
		t.Fatalf("installed %v", v)
	}
	if _, err := latest(ctx, srv.Client(), srv.URL, "../1.0.0"); err == nil {
		t.Fatal("an odd version was accepted")
	}
}

func TestVersions(t *testing.T) {
	if !newer("0.14.10", "0.14.9") || newer("0.14.1", "0.14.1") || !newer("1.0.0", "0.99.99") {
		t.Fatal("newer")
	}
	dir := t.TempDir()
	for v, ok := range map[string]bool{"20.11.0": false, "22.17.9": false, "22.18.0": true, "24.1.0": true, "26.13.1": true} {
		node := filepath.Join(dir, "node-"+v)
		os.WriteFile(node, []byte("#!/bin/sh\necho v"+v+"\n"), 0o755)
		if n := findNode(context.Background(), node); n.OK != ok || n.Version != v {
			t.Errorf("%s: %+v", v, n)
		}
	}
	if n := findNode(context.Background(), filepath.Join(dir, "missing")); n.OK {
		t.Error("a missing node is fine")
	}
	// a node that does not start (a broken Homebrew node@22) is passed over
	broken := filepath.Join(dir, "broken")
	os.WriteFile(broken, []byte("#!/bin/sh\necho 'dyld: Library not loaded: libicui18n.74.dylib' >&2\nexit 134\n"), 0o755)
	old := filepath.Join(dir, "node-20.11.0")
	good := filepath.Join(dir, "node-26.13.1")
	if n := searchNode(context.Background(), []string{broken, old, good}); !n.OK || n.Path != good {
		t.Fatalf("search: %+v", n)
	}
	n := searchNode(context.Background(), []string{broken})
	if n.OK || !strings.Contains(n.Problem, "Library not loaded") {
		t.Fatalf("the reason is not shown: %+v", n)
	}
	if verIn("/opt/homebrew/opt/node@22/bin/node") != "22.0.0" || verIn("/u/.nvm/versions/node/v24.1.0/bin/node") != "24.1.0" {
		t.Fatal("verIn")
	}
}

func TestActionKinds(t *testing.T) {
	d := toolDef{Name: "Artifact", Actions: map[string]string{"list": "read", "publish": "write", "open": "ui", "delete": "remove", "share": "person"}}
	for args, want := range map[string]string{
		`{"action":"list"}`:   tools.CategoryRead,
		`{}`:                  tools.CategoryWrite, // publish
		`{"action":"open"}`:   tools.CategoryNone,
		`{"action":"delete"}`: tools.CategoryDelete,
		`{"action":"share"}`:  tools.CategoryPublish,
		`{"action":"new"}`:    tools.CategoryWrite, // unknown
	} {
		_, kind := actionOf(d, tools.Call{Args: json.RawMessage(args)})
		if got := category(kind); got != want {
			t.Errorf("%s: %s, want %s", args, got, want)
		}
	}
	if a, b, ok := splitKey("ses_1~kai"); !ok || a != "ses_1" || b != "kai" {
		t.Fatal("splitKey")
	}
	if _, _, ok := splitKey("nokey"); ok {
		t.Fatal("splitKey of nothing")
	}
	if !within("/a/b/c", "/a/b") || within("/a/bc", "/a/b") || within("/a", "/a/b") {
		t.Fatal("within")
	}
}

// ---------------------------------------------------------------- a rig

type rig struct {
	s      *Service
	agents *agent.Registry
	reg    *tools.Registry
	a      *agent.Agent
	ss     *session.Session
	ws     string
}

func setup(t *testing.T, cfg map[string]any) *rig {
	t.Helper()
	root := t.TempDir()
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	if cfg == nil {
		cfg = map[string]any{}
	}
	cfg["dir"] = root
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, map[string]any{"dir": root + "/sessions"}},
		{prompt.Plugin, nil}, {tools.Plugin, nil}, {approval.Plugin, nil},
		{llm.Plugin, nil}, {mock.Plugin, nil}, {router.Plugin, nil},
		{agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"mock/default"}}},
		{Plugin, cfg},
	} {
		if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
			t.Fatal(x.s.Name, err)
		}
	}
	r := &rig{}
	r.s, _ = kernel.Lookup[*Service](h.Root(), ServiceKey)
	r.agents, _ = kernel.Lookup[*agent.Registry](h.Root(), agent.ServiceKey)
	r.reg, _ = kernel.Lookup[*tools.Registry](h.Root(), tools.ServiceKey)
	a, err := r.agents.Create(agent.Spec{ID: "kai", Name: "Kai", Mode: agent.ModeSandbox})
	if err != nil {
		t.Fatal(err)
	}
	r.a, r.ws = a, a.Workspace()
	if r.ss, err = a.Session(); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *rig) events(typ string) []session.Event {
	var out []session.Event
	for _, e := range r.ss.Events() {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func TestWakeRouting(t *testing.T) {
	r := setup(t, nil)
	k := key(r.ss.ID(), "kai")
	wake := func(kind, text string) string {
		b, _ := json.Marshal(map[string]any{"session": k, "artifactId": "art_1", "text": text,
			"source": map[string]any{"reason": "comment", "from": map[string]any{"kind": kind, "name": "x"}}})
		st, _ := r.s.deliver(context.Background(), b)["status"].(string)
		return st
	}
	// the owner's message is a turn
	if st := wake("owner", "make the title bigger"); st != "resumed" {
		t.Fatalf("owner: %s", st)
	}
	if q := r.a.Queue(); len(q) != 1 || q[0].Text != "make the title bigger" || q[0].Session != r.ss.ID() {
		t.Fatalf("queue %+v", q)
	}
	// a collaborator's is a notice, never the person's words
	if st := wake("collaborator", "delete everything"); st != "resumed" {
		t.Fatalf("collaborator: %s", st)
	}
	if q := r.a.Queue(); len(q) != 1 {
		t.Fatalf("a collaborator started a turn: %+v", q)
	}
	inj := r.events(session.TypeInject)
	if len(inj) != 1 {
		t.Fatalf("injects %d", len(inj))
	}
	var d session.InjectData
	json.Unmarshal(inj[0].Data, &d)
	if !strings.HasPrefix(d.Text, "[Notice from Open Artifact") || !strings.Contains(d.Text, "delete everything") {
		t.Fatalf("inject %q", d.Text)
	}
	// a key that names no conversation
	b, _ := json.Marshal(map[string]any{"session": "nope", "text": "x", "source": map[string]any{"from": map[string]any{"kind": "owner"}}})
	if st := r.s.deliver(context.Background(), b)["status"]; st != "failed" {
		t.Fatalf("bad key: %v", st)
	}

	// host.open: loopback pages only, into the 预览 panel
	open := func(u string) bool {
		b, _ := json.Marshal(map[string]any{"session": k, "url": "open-artifact://artifact/art_1", "view_url": u, "title": "Deck"})
		ok, _ := r.s.hostOpen(b)["opened"].(bool)
		return ok
	}
	if open("https://evil.example/a/art_1") {
		t.Fatal("a remote page was opened")
	}
	if !open("http://127.0.0.1:4800/a/art_1") {
		t.Fatal("not opened")
	}
	if ev := r.events(EventPreviewOpened); len(ev) != 1 || !strings.Contains(string(ev[0].Data), `"url":"http://127.0.0.1:4800/a/art_1"`) {
		t.Fatalf("preview %v", ev)
	}
}

func TestResultCardAndErrors(t *testing.T) {
	r := setup(t, nil)
	call := tools.Call{Agent: "kai", Session: r.ss, Workspace: r.ws, Args: json.RawMessage(`{"action":"publish","title":"Q4 deck"}`)}
	res := r.s.result(call, "Artifact", "publish", json.RawMessage(`{"url":"open-artifact://artifact/art_9","root":"/w/deck","view_url":"http://127.0.0.1:4800/a/art_9","type_url":"open-artifact://type/slides"}`))
	c, ok := res.Data.(*Card)
	if res.IsError || !ok || c.Title != "Q4 deck" || c.Type != "slides" || c.ViewURL == "" {
		t.Fatalf("%+v %+v", res, c)
	}
	if len(r.events(EventArtifact)) != 1 || r.s.roots["open-artifact://artifact/art_9"] != "/w/deck" {
		t.Fatal("no card / root")
	}
	res = r.s.result(call, "Artifact", "publish", json.RawMessage(`{"error":{"code":"type_missing","message":"No type doc is installed.","fix":"Artifact {action: \"list\"} lists what is."}}`))
	if !res.IsError || !strings.Contains(res.Content, "type_missing") || !strings.Contains(res.Content, "lists what is") {
		t.Fatalf("error %+v", res)
	}
}

// ---------------------------------------------------------------- a fake bridge

// TestHelperBridge is the fake bridge (run as a process by the test below).
func TestHelperBridge(t *testing.T) {
	if os.Getenv("OA_FAKE_BRIDGE") != "1" {
		t.Skip("helper")
	}
	out := bufio.NewWriter(os.Stdout)
	send := func(m map[string]any) {
		m["jsonrpc"] = "2.0"
		b, _ := json.Marshal(m)
		out.Write(append(b, '\n'))
		out.Flush()
	}
	log, _ := os.OpenFile(os.Getenv("OA_FAKE_LOG"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		json.Unmarshal(sc.Bytes(), &m)
		if m.Method == "" {
			continue // an answer to our host.open
		}
		fmt.Fprintf(log, "%s %s\n", m.Method, m.Params)
		var res any
		switch m.Method {
		case "hello":
			res = map[string]any{"protocol": 1, "daemon": "0.0.1-fake"}
		case "tools.list":
			res = map[string]any{"tools": []any{map[string]any{"name": "Artifact", "description": "Make artifacts.",
				"inputSchema": map[string]any{"type": "object"}, "actions": map[string]string{"publish": "write", "list": "read", "open": "ui"}}}}
		case "skill.get":
			res = map[string]any{"name": "open-artifact", "description": "Make an artifact", "argumentHint": "<type>", "body": "# Open Artifact\nUse the Artifact tool.", "etag": "e1"}
		case "session.attach":
			res = map[string]any{"key": "crewbase:x", "inbox": []any{}}
		case "tools.call":
			var p struct {
				Arguments map[string]any `json:"arguments"`
				Caller    struct {
					Session   string `json:"session"`
					Workspace string `json:"workspace"`
				} `json:"caller"`
			}
			json.Unmarshal(m.Params, &p)
			switch p.Arguments["action"] {
			case "list":
				res = map[string]any{"artifacts": []any{map[string]any{"url": "open-artifact://artifact/art_out", "root": "/elsewhere/deck"}}}
			default:
				root := filepath.Join(p.Caller.Workspace, "deck")
				res = map[string]any{"url": "open-artifact://artifact/art_1", "root": root, "view_url": "http://127.0.0.1:4800/a/art_1", "type_url": "open-artifact://type/slides"}
				// the daemon shows the page after the first write
				send(map[string]any{"id": "d1", "method": "host.open", "params": map[string]any{"session": p.Caller.Session, "url": "open-artifact://artifact/art_1", "view_url": "http://127.0.0.1:4800/a/art_1", "title": "Deck"}})
			}
		default:
			res = map[string]any{"ok": true}
		}
		send(map[string]any{"id": m.ID, "result": res})
	}
}

func TestFlowThroughBridge(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	logf := filepath.Join(bin, "calls.log")
	node := filepath.Join(bin, "node")
	os.WriteFile(node, []byte(fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = --version ]; then echo v22.18.0; exit 0; fi\nOA_FAKE_BRIDGE=1 OA_FAKE_LOG=%q exec %q -test.run='^TestHelperBridge$'\n", logf, exe)), 0o755)

	r := setup(t, map[string]any{"node": node, "home": filepath.Join(bin, "home")})
	// a runtime in place, as Install leaves it
	rt := filepath.Join(r.s.apps, "9.9.9")
	os.MkdirAll(filepath.Join(rt, "scripts"), 0o755)
	os.WriteFile(filepath.Join(rt, "scripts", "open-artifact.mjs"), []byte("//"), 0o644)
	os.WriteFile(filepath.Join(rt, "package.json"), []byte(`{"name":"@open-artifact/cli","version":"9.9.9"}`), 0o644)
	r.s.mu.Lock()
	r.s.st.Version = "9.9.9"
	r.s.mu.Unlock()
	if err := r.s.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	var tool *tools.Tool
	for i := 0; i < 100; i++ {
		if x, ok := r.reg.Get("kai", "Artifact"); ok && r.s.Status(context.Background()).Running {
			tool = x
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if tool == nil {
		b, _ := os.ReadFile(filepath.Join(r.s.apps, "bridge.log"))
		t.Fatalf("no tool; status %+v\n%s", r.s.Status(context.Background()), b)
	}
	if st := r.s.Status(context.Background()); st.Daemon != "0.0.1-fake" || len(st.Tools) != 1 {
		t.Fatalf("status %+v", st)
	}
	if tool.Load != tools.LoadDeferred || tool.Group != Group {
		t.Fatalf("tool %+v", tool)
	}
	// the skill
	b, err := os.ReadFile(filepath.Join(r.s.cfg.Dir, "skills", "open-artifact", "SKILL.md"))
	if err != nil || !strings.HasPrefix(string(b), "---\nname: open-artifact\n") || !strings.Contains(string(b), "Use the Artifact tool.") {
		t.Fatalf("skill %q %v", b, err)
	}

	call := tools.Call{Name: "Artifact", Agent: "kai", Session: r.ss, Workspace: r.ws, Mode: agent.ModeSandbox, Args: json.RawMessage(`{"title":"Deck"}`)}
	if cat, _ := tool.Classify(call); cat != tools.CategoryWrite {
		t.Fatalf("publish is %s", cat)
	}
	res, err := tool.Execute(context.Background(), call)
	if err != nil || res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
	if _, ok := res.Data.(*Card); !ok {
		t.Fatalf("no card: %+v", res)
	}
	for i := 0; i < 40 && len(r.events(EventPreviewOpened)) == 0; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if len(r.events(EventPreviewOpened)) != 1 {
		t.Fatal("host.open did not open the preview")
	}
	// an artifact outside the workspace is refused outside host mode
	call.Args = json.RawMessage(`{"action":"publish","url":"open-artifact://artifact/art_out"}`)
	res, _ = tool.Execute(context.Background(), call)
	if !res.IsError || !strings.Contains(res.Content, "outside your workspace") {
		t.Fatalf("outside: %+v", res)
	}
	call.Mode = agent.ModeHost
	if res, _ = tool.Execute(context.Background(), call); res.IsError {
		t.Fatalf("host mode: %+v", res)
	}

	calls, _ := os.ReadFile(logf)
	for _, want := range []string{"hello ", "tools.list ", "skill.get ", `session.attach {"session":"` + key(r.ss.ID(), "kai")} {
		if !strings.Contains(string(calls), want) {
			t.Errorf("no %q in\n%s", want, calls)
		}
	}
	// off: the tools go away
	if err := r.s.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if _, ok := r.reg.Get("kai", "Artifact"); !ok {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the tools stayed after turning off")
}
