package library_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/api"
	"github.com/hedeqiang/crewbase/features/attachments"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/features/library"
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

const tok = "test-token-library-xxxxxxxxxxxxxxxxxxxxx"

func start(t *testing.T) (string, string) {
	t.Helper()
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	var addr string
	gateway.Listening.Listen(h.Root(), func(a string) { addr = a })
	root := t.TempDir()
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, map[string]any{"dir": root + "/sessions"}},
		{prompt.Plugin, nil}, {tools.Plugin, nil}, {approval.Plugin, nil}, {approval.DesktopPlugin, nil},
		{llm.Plugin, nil}, {mock.Plugin, nil}, {router.Plugin, nil},
		{agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"mock/default"}}},
		{agentloop.Plugin, nil}, {attachments.Plugin, nil},
		{library.Plugin, map[string]any{"root": root}},
		{gateway.Plugin, map[string]any{"addr": "127.0.0.1:0", "token": tok}},
		{api.Plugin, nil}, {attachments.APIPlugin, nil}, {library.APIPlugin, nil},
	} {
		if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
			t.Fatal(err)
		}
	}
	return "http://" + addr, root
}

func call(t *testing.T, method, u string, body []byte, out any) int {
	t.Helper()
	req, _ := http.NewRequest(method, u, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil && json.Valid(body) {
		req.Header.Set("Content-Type", "application/json")
	} else {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil {
		if p, ok := out.(*[]byte); ok {
			*p = b
		} else if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s: %v: %s", method, u, err, b)
		}
	}
	return resp.StatusCode
}

type listOut struct {
	Items []library.Hit  `json:"items"`
	Stats *library.Stats `json:"stats"`
}

func TestLibraryAPI(t *testing.T) {
	base, root := start(t)
	if c := call(t, "POST", base+"/api/agents", []byte(`{"name":"Kai"}`), nil); c != 200 {
		t.Fatal(c)
	}
	// an upload into a conversation lands in the library
	if c := call(t, "POST", base+"/api/agents/kai/uploads?name=report.md", []byte("# Q3\nRevenue grew in Lisbon.\n"), nil); c != 200 {
		t.Fatal(c)
	}
	var l listOut
	deadline := time.Now().Add(5 * time.Second)
	for len(l.Items) == 0 && time.Now().Before(deadline) {
		call(t, "GET", base+"/api/library?team=default", nil, &l)
		time.Sleep(20 * time.Millisecond)
	}
	if len(l.Items) != 1 || l.Items[0].Folder != "inbox" || l.Stats == nil || l.Stats.Folders["inbox"] != 1 {
		t.Fatalf("%+v", l)
	}
	up := l.Items[0]

	// the person adds a file; replacing it keeps a version
	var it library.Item
	if c := call(t, "POST", base+"/api/library?team=default&name=plan.md&folder=docs", []byte("v1 Lisbon plan"), &it); c != 200 || it.Source != "person" {
		t.Fatalf("%d %+v", c, it)
	}
	// search
	call(t, "GET", base+"/api/library?team=default&q=Lisbon", nil, &l)
	if len(l.Items) != 2 {
		t.Fatalf("search %+v", l.Items)
	}
	call(t, "GET", base+"/api/library?team=default&q=Lisbon&folder=docs", nil, &l)
	if len(l.Items) != 1 || l.Items[0].ID != it.ID {
		t.Fatalf("search in docs %+v", l.Items)
	}
	// another team sees nothing of it
	if c := call(t, "GET", fmt.Sprintf("%s/api/library/%d?team=shop", base, up.ID), nil, nil); c != 404 {
		t.Fatalf("other team: %d", c)
	}
	// text, raw, tags
	var tx struct{ Text string }
	call(t, "GET", fmt.Sprintf("%s/api/library/%d/text?team=default", base, up.ID), nil, &tx)
	if !strings.Contains(tx.Text, "Revenue grew") {
		t.Fatalf("text %q", tx.Text)
	}
	var raw []byte
	if c := call(t, "GET", fmt.Sprintf("%s/api/library/%d/raw?team=default", base, up.ID), nil, &raw); c != 200 || !bytes.HasPrefix(raw, []byte("# Q3")) {
		t.Fatalf("raw %d %q", c, raw)
	}
	if c := call(t, "PATCH", fmt.Sprintf("%s/api/library/%d?team=default", base, up.ID), []byte(`{"tags":["lisbon"],"title":"Q3 report"}`), &it); c != 200 || it.Title != "Q3 report" {
		t.Fatalf("patch %d %+v", c, it)
	}
	call(t, "GET", base+"/api/library?team=default&tag=lisbon", nil, &l)
	if len(l.Items) != 1 || len(l.Stats.Tags) != 1 || l.Stats.Tags[0].Tag != "lisbon" {
		t.Fatalf("tag %+v", l)
	}
	// attach it to a conversation: a copy in the agent's workspace
	var at llm.Attachment
	if c := call(t, "POST", fmt.Sprintf("%s/api/library/%d/attach?team=default", base, up.ID), []byte(`{"agent":"kai"}`), &at); c != 200 || at.Path == "" {
		t.Fatalf("attach %d %+v", c, at)
	}
	if _, err := os.Stat(filepath.Join(root, "workspaces", "kai", filepath.FromSlash(at.Path))); err != nil {
		t.Fatalf("attached file: %v", err)
	}
	// delete
	if c := call(t, "DELETE", fmt.Sprintf("%s/api/library/%d?team=default", base, up.ID), nil, nil); c != 200 {
		t.Fatal(c)
	}
	if c := call(t, "GET", fmt.Sprintf("%s/api/library/%d?team=default", base, up.ID), nil, nil); c != 404 {
		t.Fatalf("deleted: %d", c)
	}
}
