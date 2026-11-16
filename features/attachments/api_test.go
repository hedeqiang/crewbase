package attachments_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
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
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

const tok = "test-token-attachments-xxxxxxxxxxxxxxxx"

func start(t *testing.T) (string, *kernel.Host) {
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
		{gateway.Plugin, map[string]any{"addr": "127.0.0.1:0", "token": tok}},
		{api.Plugin, nil}, {attachments.APIPlugin, nil},
	} {
		if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
			t.Fatal(err)
		}
	}
	return "http://" + addr, h
}

func call(t *testing.T, method, u string, body io.Reader, ctype string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, u, body)
	req.Header.Set("Authorization", "Bearer "+tok)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func TestUploadSendDownload(t *testing.T) {
	base, h := start(t)
	b, _ := json.Marshal(map[string]any{"name": "Kai"})
	if r, out := call(t, "POST", base+"/api/agents", bytes.NewReader(b), "application/json"); r.StatusCode != 200 {
		t.Fatal(string(out))
	}

	// upload a raw (non-JSON) body
	r, out := call(t, "POST", base+"/api/agents/kai/uploads?name="+url.QueryEscape("会议记录.md"), strings.NewReader("# 周会\n决定：周五发布"), "text/markdown")
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode, string(out))
	}
	var at llm.Attachment
	json.Unmarshal(out, &at)
	if !strings.HasPrefix(at.Path, "uploads/") || at.Mime != "text/markdown" {
		t.Fatalf("upload %+v", at)
	}

	// send with only an attachment (no text)
	b, _ = json.Marshal(map[string]any{"attachments": []map[string]string{{"path": at.Path, "name": at.Name}}})
	if r, out := call(t, "POST", base+"/api/agents/kai/messages", bytes.NewReader(b), "application/json"); r.StatusCode != 200 {
		t.Fatal(string(out))
	}
	// a path outside uploads/ is refused
	b, _ = json.Marshal(map[string]any{"text": "x", "attachments": []map[string]string{{"path": "agent.json"}}})
	if r, _ := call(t, "POST", base+"/api/agents/kai/messages", bytes.NewReader(b), "application/json"); r.StatusCode != 400 {
		t.Fatalf("non-upload path: %d", r.StatusCode)
	}

	// the mock model saw the hydrated attachment; the log holds only the reference
	reg := kernel.Use[*agent.Registry](h.Root(), agent.ServiceKey)
	a, _ := reg.Get("kai")
	ss := kernel.Use[*session.Store](h.Root(), session.ServiceKey)
	deadline := time.Now().Add(3 * time.Second)
	for {
		s, err := ss.Get(a.Spec().Session)
		if err != nil {
			t.Fatal(err)
		}
		var reply string
		var logged string
		for _, e := range s.Events() {
			if e.Type == session.TypeAssistantMessage {
				var m session.AssistantMessage
				e.Decode(&m)
				reply = m.Message.Text()
			}
			if e.Type == session.TypeUserMessage {
				logged = string(e.Data)
			}
		}
		if reply != "" {
			if !strings.Contains(reply, "会议记录.md (text/markdown") {
				t.Fatalf("reply %q", reply)
			}
			if strings.Contains(logged, "周五发布") || !strings.Contains(logged, at.Path) {
				t.Fatalf("log must hold the reference only: %s", logged)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no reply")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// download: text is served as a download, never inline
	r, out = call(t, "GET", base+"/api/agents/kai/raw?path="+url.QueryEscape(at.Path), nil, "")
	if r.StatusCode != 200 || string(out) != "# 周会\n决定：周五发布" || r.Header.Get("Content-Type") != "application/octet-stream" ||
		!strings.HasPrefix(r.Header.Get("Content-Disposition"), "attachment") || r.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("download %d %v", r.StatusCode, r.Header)
	}
	if r, _ := call(t, "GET", base+"/api/agents/kai/raw?path=../../etc/passwd", nil, ""); r.StatusCode != 404 {
		t.Fatalf("escape: %d", r.StatusCode)
	}
	// video plays inline with Range; HTML renamed to .mp4 stays a download
	ws := a.Workspace()
	os.MkdirAll(filepath.Join(ws, "generated"), 0o755)
	mp4 := append([]byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"), bytes.Repeat([]byte{7}, 1000)...)
	os.WriteFile(filepath.Join(ws, "generated", "clip.mp4"), mp4, 0o644)
	os.WriteFile(filepath.Join(ws, "generated", "evil.mp4"), []byte("<!doctype html><script>alert(1)</script>"), 0o644)
	req, _ := http.NewRequest("GET", base+"/api/agents/kai/raw?path=generated/clip.mp4", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Range", "bytes=0-99")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	part, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 206 || len(part) != 100 || resp.Header.Get("Content-Type") != "video/mp4" || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "inline") ||
		resp.Header.Get("Content-Range") != "bytes 0-99/1024" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("video range: %d %d %v", resp.StatusCode, len(part), resp.Header)
	}
	if r, _ := call(t, "GET", base+"/api/agents/kai/raw?path=generated/evil.mp4", nil, ""); r.Header.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(r.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("disguised html: %v", r.Header)
	}

	// oversized uploads are refused with 413
	big := bytes.Repeat([]byte("x"), attachments.MaxFileBytes+1)
	if r, _ := call(t, "POST", base+"/api/agents/kai/uploads?name=big.txt", bytes.NewReader(big), "text/plain"); r.StatusCode != 413 {
		t.Fatalf("big upload: %d", r.StatusCode)
	}
}
