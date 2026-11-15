package deepseek

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hedeqiang/crewbase/core/tools"
)

const testKey = "sk-test-0000"

// fake implements the five endpoints as documented.
func fake(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":{"message":"Authentication Fails","type":"authentication_error","param":null,"code":"invalid_api_key"}}`)
			return
		}
		switch r.URL.Path {
		case "/v1/tools/search":
			var in map[string]any
			json.NewDecoder(r.Body).Decode(&in)
			if in["query"] == "" {
				w.WriteHeader(400)
				return
			}
			if in["query"] == "nothing" {
				io.WriteString(w, `{"results":[]}`)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{
				{"rerank_score": 2.1, "title": "Go 1.27 released", "url": "https://go.dev/blog/go1.27", "snippet": "s", "summary": "Go 1.27 adds …", "content": "", "site_name": "go.dev", "site_icon": nil, "date": "2026-08-12T00:00:00Z", "authority": 4},
				{"rerank_score": 1.1, "title": "Other", "url": "https://example.com", "snippet": "only snippet", "summary": "", "content": "", "site_name": nil, "site_icon": nil, "date": nil, "authority": -1},
			}})
		case "/v1/tools/fetch":
			var in map[string]string
			json.NewDecoder(r.Body).Decode(&in)
			if strings.Contains(in["url"], "blocked") {
				w.WriteHeader(403)
				io.WriteString(w, `{"error":{"message":"blocked","type":"invalid_request_error","param":null,"code":"blocked"}}`)
				return
			}
			io.WriteString(w, `{"title":"Example Domain","content":"# Example\n\nThis domain is for use in examples."}`)
		case "/v1/tools/ocr":
			if err := r.ParseMultipartForm(32 << 20); err != nil || r.FormValue("purpose") == "" {
				w.WriteHeader(400)
				return
			}
			f, _, err := r.FormFile("file")
			if err != nil {
				w.WriteHeader(400)
				return
			}
			b, _ := io.ReadAll(f)
			// keep-alive newlines before the body, as the service does
			io.WriteString(w, "\n\n\n")
			if string(b) == "broken" {
				io.WriteString(w, `{"error":{"message":"file is encrypted","type":"invalid_request_error","param":null,"code":"encrypted"}}`)
				return
			}
			io.WriteString(w, `{"id":"x","object":"ocr.result","created":1,"purpose":"text_recognition","page_count":2,"pages":[`+
				`{"index":0,"text":"<｜ref｜>发票<｜/ref｜><｜box｜>[[10, 20, 300, 40]]<｜/box｜>\n金额 100 元","finish_reason":"stop"},`+
				`{"index":1,"text":"第二页","finish_reason":"length"}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`)
		case "/v1/tools/asr":
			if err := r.ParseMultipartForm(16 << 20); err != nil {
				w.WriteHeader(400)
				return
			}
			if _, _, err := r.FormFile("file"); err != nil {
				w.WriteHeader(400)
				return
			}
			io.WriteString(w, `{"request_id":"r1","text":"上海今天天气怎么样？","duration_ms":3200}`)
		case "/v1/tools/tts":
			var in map[string]any
			json.NewDecoder(r.Body).Decode(&in)
			if in["voice_id"] != "stella" && in["voice_id"] != "tide" || in["format"] != "wav" {
				w.WriteHeader(400)
				return
			}
			w.Header().Set("Content-Type", "audio/wav")
			w.Write(wav(16, 100))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func wav(bits uint16, samples int) []byte {
	data := samples * int(bits) / 8
	b := make([]byte, 44+data)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+data))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], 16000)
	binary.LittleEndian.PutUint32(b[28:], 16000*uint32(bits)/8)
	binary.LittleEndian.PutUint16(b[32:], bits/8)
	binary.LittleEndian.PutUint16(b[34:], bits)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(data))
	return b
}

func client(url, key string) *Client {
	return &Client{BaseURL: url, Key: func(context.Context) (string, error) {
		if key == "" {
			return "", ErrNoKey
		}
		return key, nil
	}}
}

func TestClient(t *testing.T) {
	srv, _ := fake(t)
	ctx := context.Background()
	c := client(srv.URL, testKey)

	res, err := c.Search(ctx, SearchRequest{Query: strings.Repeat("长", 200), Count: 99})
	if err != nil || len(res) != 2 || res[0].SiteName == nil || *res[0].SiteName != "go.dev" || res[1].Date != nil {
		t.Fatalf("search: %v %+v", err, res)
	}
	p, err := c.Fetch(ctx, "https://example.com")
	if err != nil || p.Title != "Example Domain" {
		t.Fatalf("fetch: %v %+v", err, p)
	}
	var ae *APIError
	if _, err := c.Fetch(ctx, "https://blocked.example"); !errors.As(err, &ae) || ae.Status != 403 || ae.Retryable() {
		t.Fatalf("fetch blocked: %v", err)
	}
	r, err := c.OCR(ctx, "a.pdf", []byte("%PDF"), "")
	if err != nil || r.PageCount != 2 {
		t.Fatalf("ocr: %v %+v", err, r)
	}
	txt := r.Text()
	if !strings.Contains(txt, "## 第 1 页") || !strings.Contains(txt, "发票\n金额 100 元") || strings.Contains(txt, "box") || strings.Contains(txt, "[[") || !strings.Contains(txt, "不完整") {
		t.Fatalf("ocr text: %q", txt)
	}
	if _, err := c.OCR(ctx, "a.pdf", []byte("broken"), ""); err == nil || !strings.Contains(err.Error(), "encrypted") {
		t.Fatalf("ocr error after 200: %v", err)
	}
	tr, err := c.Transcribe(ctx, "s.wav", wav(16, 10))
	if err != nil || tr.Text != "上海今天天气怎么样？" || tr.DurationMS != 3200 {
		t.Fatalf("asr: %v %+v", err, tr)
	}
	audio, err := c.Speak(ctx, "你好", "unknown-voice")
	if err != nil || string(audio[:4]) != "RIFF" {
		t.Fatalf("tts: %v", err)
	}

	// errors: bad key (never echoed), no key
	if _, err := client(srv.URL, "sk-wrong").Search(ctx, SearchRequest{Query: "x"}); err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "sk-wrong") {
		t.Fatalf("401: %v", err)
	}
	if _, err := client(srv.URL, "").Search(ctx, SearchRequest{Query: "x"}); !errors.Is(err, ErrNoKey) {
		t.Fatalf("no key: %v", err)
	}
}

func TestHelpers(t *testing.T) {
	if got := ClipBillable("中文abc", 5); got != "中文a" {
		t.Fatalf("clip: %q", got)
	}
	if got := CleanOCR("<｜ref｜>标题<｜/ref｜><｜det｜>[[1, 2, 3, 4], [5, 6, 7, 8]]<｜/det｜>"); got != "标题" {
		t.Fatalf("clean: %q", got)
	}
	if err := checkAudio(wav(16, 4)); err != nil {
		t.Fatal(err)
	}
	if err := checkAudio(wav(8, 4)); err == nil {
		t.Fatal("8-bit accepted")
	}
	if err := checkAudio([]byte("OggS....")); err != nil {
		t.Fatal(err)
	}
	if err := checkAudio([]byte("ID3 mp3")); err == nil {
		t.Fatal("mp3 accepted")
	}
}

func TestTools(t *testing.T) {
	srv, calls := fake(t)
	s := &Service{cfg: Config{AllowHosts: []string{"example.com"}}}
	s.Client = client(srv.URL, testKey)
	byName := map[string]*tools.Tool{}
	for _, tl := range s.tools() {
		byName[tl.Name] = tl
	}
	run := func(name, args, mode string) tools.Result {
		r, err := byName[name].Execute(context.Background(), tools.Call{Name: name, Args: json.RawMessage(args), Mode: mode, Workspace: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := run("web_search", `{"query":"go 1.27"}`, "")
	if r.IsError || !strings.Contains(r.Content, "[1] Go 1.27 released\nhttps://go.dev/blog/go1.27\ngo.dev · 2026-08-12 · authoritative") || !strings.Contains(r.Content, "only snippet") {
		t.Fatalf("search: %+v", r)
	}
	if r := run("web_search", `{"query":"nothing"}`, ""); r.IsError || !strings.Contains(r.Content, "No results") {
		t.Fatalf("empty: %+v", r)
	}
	if r := run("web_fetch", `{"url":"https://example.com/a"}`, "sandbox"); r.IsError || !strings.HasPrefix(r.Content, "# Example Domain\nhttps://example.com/a") {
		t.Fatalf("fetch: %+v", r)
	}
	n := calls.Load()
	if r := run("web_fetch", `{"url":"https://evil.test/?q=secret"}`, "sandbox"); !r.IsError || !strings.Contains(r.Content, "allowlist") || calls.Load() != n {
		t.Fatalf("allowlist not applied: %+v", r)
	}
	if r := run("web_fetch", `{"url":"https://evil.test/"}`, "host"); r.IsError {
		t.Fatalf("host mode limited: %+v", r)
	}
	if r := run("web_fetch", `{"url":"file:///etc/passwd"}`, "host"); !r.IsError {
		t.Fatal("file URL accepted")
	}
	if r := run("web_fetch", `{"url":"https://u:p@example.com/"}`, "host"); !r.IsError {
		t.Fatal("credentials in URL accepted")
	}
	if r := run("web_fetch", `{"url":"https://blocked.example.com/"}`, "host"); !r.IsError || !strings.Contains(r.Content, "browser") {
		t.Fatalf("403: %+v", r)
	}
}
