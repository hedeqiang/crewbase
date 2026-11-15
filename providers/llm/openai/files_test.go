package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hedeqiang/crewbase/core/llm"
)

func streamOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, `data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func attachMsg(ats ...llm.Attachment) []llm.Message {
	parts := []llm.Part{{Type: "text", Text: "看一下"}}
	for _, at := range ats {
		at := at
		parts = append(parts, llm.Part{Type: llm.PartAttachment, Attachment: &at})
	}
	return []llm.Message{{Role: llm.RoleUser, Parts: parts}}
}

func TestAttachmentsOpenAI(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = map[string]any{}
		json.Unmarshal(b, &body)
		streamOK(w)
	}))
	defer srv.Close()
	a, err := New(nil, ProviderConfig{ID: "gw", BaseURL: srv.URL, APIKey: "k", Models: []ModelSpec{
		{ID: "seeing", Capabilities: []string{"vision", "pdf"}}, {ID: "blind", Capabilities: []string{"tools"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !a.AcceptsFile("seeing", "image/png") || a.AcceptsFile("blind", "image/png") || !a.AcceptsFile("seeing", "application/pdf") || a.AcceptsFile("blind", "application/pdf") {
		t.Fatal("capabilities not respected")
	}
	img := llm.Attachment{Path: "uploads/d/p.png", Name: "p.png", Mime: "image/png", Data: []byte{0x89, 'P'}, Text: "[图片 p.png]"}
	pdf := llm.Attachment{Path: "uploads/d/a.pdf", Name: "a.pdf", Mime: "application/pdf", Data: []byte("%PDF"), Text: "[附件 a.pdf]"}
	txt := llm.Attachment{Path: "uploads/d/n.txt", Name: "n.txt", Mime: "text/plain", Text: "[附件 n.txt]\nhello"}
	if _, err := a.Stream(context.Background(), llm.Request{Model: "seeing", Messages: attachMsg(img, pdf, txt)}, func(llm.Chunk) error { return nil }); err != nil {
		t.Fatal(err)
	}
	msgs := body["messages"].([]any)
	content := msgs[0].(map[string]any)["content"].([]any)
	var kinds []string
	for _, c := range content {
		m := c.(map[string]any)
		kinds = append(kinds, m["type"].(string))
		switch m["type"] {
		case "image_url":
			if u := m["image_url"].(map[string]any)["url"].(string); !strings.HasPrefix(u, "data:image/png;base64,") {
				t.Fatalf("image url %s", u)
			}
		case "file":
			f := m["file"].(map[string]any)
			if f["filename"] != "a.pdf" || !strings.HasPrefix(f["file_data"].(string), "data:application/pdf;base64,") {
				t.Fatalf("file part %v", f)
			}
		}
	}
	if strings.Join(kinds, ",") != "text,image_url,text,file,text" {
		t.Fatalf("content kinds %v", kinds)
	}
	if last := content[4].(map[string]any)["text"].(string); !strings.Contains(last, "hello") {
		t.Fatalf("text attachment %q", last)
	}
}

func TestDeepSeekFileUpload(t *testing.T) {
	var uploads atomic.Int32
	var purpose, fieldOrder string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/files" {
			uploads.Add(1)
			mr, err := r.MultipartReader()
			if err != nil {
				t.Error(err)
				return
			}
			var order []string
			for {
				p, err := mr.NextPart()
				if err != nil {
					break
				}
				order = append(order, p.FormName())
				if p.FormName() == "purpose" {
					b, _ := io.ReadAll(p)
					purpose = string(b)
				}
			}
			fieldOrder = strings.Join(order, ",")
			fmt.Fprint(w, `{"id":"file-api-1","object":"file","bytes":4,"filename":"a.pdf","purpose":"text_recognition"}`)
			return
		}
		b, _ := io.ReadAll(r.Body)
		body = map[string]any{}
		json.Unmarshal(b, &body)
		streamOK(w)
	}))
	defer srv.Close()
	a, err := New(nil, ProviderConfig{ID: "deepseek", BaseURL: srv.URL, APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if !a.AcceptsFile("deepseek-flash", mimeDOCX) || !a.AcceptsFile("deepseek-flash", "image/png") {
		t.Fatal("deepseek should take documents and images")
	}
	ws := t.TempDir()
	pdf := llm.Attachment{Path: "uploads/d/a.pdf", Name: "a.pdf", Mime: "application/pdf", SHA256: "abc", Data: []byte("%PDF"), Text: "[附件 a.pdf]", Alt: "full text"}
	for i := 0; i < 2; i++ {
		req := llm.Request{Model: "deepseek-flash", Workspace: ws, Messages: attachMsg(pdf)}
		if _, err := a.Stream(context.Background(), req, func(llm.Chunk) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if uploads.Load() != 1 {
		t.Fatalf("uploaded %d times, want once (ids are cached per workspace)", uploads.Load())
	}
	if purpose != "text_recognition" || !strings.HasPrefix(fieldOrder, "purpose,") || !strings.HasSuffix(fieldOrder, ",file") {
		t.Fatalf("form purpose %q order %q", purpose, fieldOrder)
	}
	content := body["messages"].([]any)[0].(map[string]any)["content"].([]any)
	f := content[1].(map[string]any)
	if f["type"] != "file" || f["file_id"] != "file-api-1" || f["file"] != nil { // flat, as DeepSeek documents
		t.Fatalf("file ref %v", f)
	}

	// a failed upload falls back to the extracted text
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/files" {
			http.Error(w, `{"error":{"message":"busy"}}`, 503)
			return
		}
		b, _ := io.ReadAll(r.Body)
		body = map[string]any{}
		json.Unmarshal(b, &body)
		streamOK(w)
	}))
	defer bad.Close()
	b2, _ := New(nil, ProviderConfig{ID: "deepseek", BaseURL: bad.URL, APIKey: "k"})
	other := pdf
	other.SHA256 = "def"
	if _, err := b2.Stream(context.Background(), llm.Request{Model: "deepseek-flash", Workspace: t.TempDir(), Messages: attachMsg(other)}, func(llm.Chunk) error { return nil }); err != nil {
		t.Fatal(err)
	}
	content = body["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if len(content) != 1 || !strings.Contains(content[0].(map[string]any)["text"].(string), "full text") {
		t.Fatalf("fallback content %v", content)
	}
}
