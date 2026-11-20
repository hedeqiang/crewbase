package preview

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/capabilities/browser"
	"github.com/hedeqiang/crewbase/core/tools"
)

type fakeRenderer struct{ r browser.Rendered }

func (f fakeRenderer) Render(context.Context, string, browser.RenderOptions) (browser.Rendered, error) {
	return f.r, nil
}

func TestShowPage(t *testing.T) {
	ws := t.TempDir()
	m := NewManager(0, 0)
	defer m.Close()
	call := func(args map[string]any) tools.Call {
		b, _ := json.Marshal(args)
		return tools.Call{ID: "c1", Name: "show_page", Args: b, Agent: "a1", Workspace: ws}
	}
	png := []byte("\x89PNG fake")
	bad := fakeRenderer{browser.Rendered{Errors: []string{"ReferenceError: x is not defined"},
		Shots: []browser.Shot{{Size: browser.Size{Width: 1280, Height: 800}, PNG: png, TextChars: 50, Elements: 9},
			{Size: browser.Size{Width: 390, Height: 844}, PNG: png, TextChars: 50, Elements: 9, Overflow: 300}}}}

	// problems: not shown, both found, screenshots attached
	res, _ := m.showPage(context.Background(), call(map[string]any{"title": "三季度 销售", "html": "<h1>hi</h1>"}), bad)
	if !strings.Contains(res.Content, "NOT shown") || !strings.Contains(res.Content, "x is not defined") || !strings.Contains(res.Content, "300px wider") || res.Data != nil {
		t.Fatalf("%+v", res)
	}
	if len(res.Attachments) != 2 || res.Attachments[1].Mime != "image/png" {
		t.Fatalf("attachments %+v", res.Attachments)
	}
	if _, err := os.Stat(filepath.Join(ws, "pages", "三季度-销售.html")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ws, filepath.FromSlash(res.Attachments[0].Path))); err != nil {
		t.Fatal(err)
	}
	// force: shown, the problems kept as notes
	res, _ = m.showPage(context.Background(), call(map[string]any{"title": "三季度 销售", "path": "pages/三季度-销售.html", "force": true}), bad)
	if sh, ok := res.Data.(PageShown); !ok || len(sh.Notes) != 2 || !sh.Checked {
		t.Fatalf("%+v", res)
	}
	// clean: shown
	good := fakeRenderer{browser.Rendered{Shots: []browser.Shot{{Size: browser.Size{Width: 1280, Height: 800}, PNG: png, TextChars: 50, Elements: 9}}}}
	res, _ = m.showPage(context.Background(), call(map[string]any{"title": "ok", "html": "<p>fine</p>"}), good)
	if sh, ok := res.Data.(PageShown); !ok || sh.Path != "pages/ok.html" || !strings.HasPrefix(sh.URL, "http://127.0.0.1:") {
		t.Fatalf("%+v", res)
	}
	// blank page
	blank := fakeRenderer{browser.Rendered{Shots: []browser.Shot{{Size: browser.Size{Width: 1280, Height: 800}}}}}
	if res, _ = m.showPage(context.Background(), call(map[string]any{"title": "b", "html": "<div></div>"}), blank); !strings.Contains(res.Content, "blank") {
		t.Fatal(res.Content)
	}
	// no browser: shown unchecked
	res, _ = m.showPage(context.Background(), call(map[string]any{"title": "n", "html": "<p>x</p>"}), nil)
	if sh, ok := res.Data.(PageShown); !ok || sh.Checked {
		t.Fatalf("%+v", res)
	}
	if res, _ = m.showPage(context.Background(), call(map[string]any{"title": "x", "path": "../etc/passwd.html"}), good); !res.IsError {
		t.Fatal("outside the workspace")
	}
}
