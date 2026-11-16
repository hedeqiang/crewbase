package gemini

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
)

var pngBytes, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==")

func TestGeminiModelsKind(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"models":[
		 {"name":"models/gemini-3-flash","supportedGenerationMethods":["generateContent"],"inputTokenLimit":1048576},
		 {"name":"models/gemini-3.1-flash-image","supportedGenerationMethods":["generateContent"]},
		 {"name":"models/gemini-nano-banana-2.1","supportedGenerationMethods":["generateContent"]},
		 {"name":"models/gemini-omni-1.2-flash","supportedGenerationMethods":["generateContent"]},
		 {"name":"models/veo-3.1-generate-preview","supportedGenerationMethods":["predictLongRunning"]},
		 {"name":"models/text-embedding-004","supportedGenerationMethods":["embedContent"]}]}`)
	}))
	defer srv.Close()
	ms, err := newAdapter(srv.URL).Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, m := range ms {
		got[m.ID] = m.Kind
		if m.Kind != "" && (m.ContextWindow != 0 || strings.Contains(strings.Join(m.Capabilities, ","), "tools")) {
			t.Errorf("media model with chat caps: %+v", m)
		}
	}
	want := map[string]string{"gemini-3-flash": "", "gemini-3.1-flash-image": "image", "gemini-nano-banana-2.1": "image",
		"gemini-omni-1.2-flash": "video", "veo-3.1-generate-preview": "video", "text-embedding-004": ""}
	for id, k := range want {
		if g, ok := got[id]; !ok || g != k {
			t.Errorf("%s: %q (listed %v), want %q", id, g, ok, k)
		}
	}
}

func TestGeminiGenerateImages(t *testing.T) {
	var mu sync.Mutex
	var body map[string]any
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1beta/interactions" {
			http.NotFound(w, r)
			return
		}
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(b, &body)
		hdr = r.Header.Clone()
		mu.Unlock()
		fmt.Fprintf(w, `{"id":"v1_x","status":"completed","object":"interaction","steps":[
		 {"type":"user_input","content":[{"type":"text","text":"..."}]},
		 {"type":"model_output","content":[{"type":"text","text":"Here you go"},{"type":"image","mime_type":"image/png","data":%q}]}]}`,
			base64.StdEncoding.EncodeToString(pngBytes))
	}))
	defer srv.Close()
	a := newAdapter(srv.URL)
	res, err := a.GenerateImages(context.Background(), llm.ImageRequest{Model: "gemini-nano-banana-2.1", Prompt: "a cat", Size: "1536x1024",
		Inputs: []llm.InputImage{{Mime: "image/png", Data: pngBytes}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Images) != 1 || res.Images[0].Mime != "image/png" {
		t.Fatalf("%+v", res)
	}
	mu.Lock()
	defer mu.Unlock()
	if hdr.Get("x-goog-api-key") != "sk-testsecretkey1234567890" || hdr.Get("Api-Revision") != APIRevision {
		t.Fatalf("headers %v", hdr)
	}
	rf, _ := body["response_format"].(map[string]any)
	in, _ := body["input"].([]any)
	if body["model"] != "gemini-nano-banana-2.1" || rf["type"] != "image" || rf["aspect_ratio"] != "3:2" || len(in) != 2 || body["store"] != false {
		t.Fatalf("body %v", body)
	}
	if img, _ := in[1].(map[string]any); img["type"] != "image" || img["mime_type"] != "image/png" || img["data"] == "" {
		t.Fatalf("input image %v", in[1])
	}
}

func TestGeminiImageRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"I can't draw that."}]}]}`)
	}))
	defer srv.Close()
	_, err := newAdapter(srv.URL).GenerateImages(context.Background(), llm.ImageRequest{Model: "gemini-3.1-flash-image", Prompt: "x"})
	if err == nil || !strings.Contains(err.Error(), "no image") {
		t.Fatal(err)
	}
}

func TestGeminiVideoInline(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		fmt.Fprintf(w, `{"id":"v1_y","status":"completed","steps":[{"type":"model_output","content":[{"type":"video","mime_type":"video/mp4","data":%q}]}]}`,
			base64.StdEncoding.EncodeToString([]byte("\x00\x00\x00\x18ftypisomfake")))
	}))
	defer srv.Close()
	v, err := newAdapter(srv.URL).GenerateVideo(context.Background(), llm.VideoRequest{Model: "gemini-omni-1.2-flash", Prompt: "a marble", Seconds: 8, Aspect: "9:16", Resolution: "720p"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(v.Body)
	if v.Mime != "video/mp4" || !strings.HasSuffix(string(b), "fake") {
		t.Fatalf("%q %q", v.Mime, b)
	}
	rf, _ := body["response_format"].(map[string]any)
	if body["input"] != "a marble" || rf["type"] != "video" || rf["duration"] != "8s" || rf["aspect_ratio"] != "9:16" || rf["resolution"] != "720p" || rf["delivery"] != nil {
		t.Fatalf("body %v", body)
	}
}

func TestGeminiVideoByURI(t *testing.T) {
	var mu sync.Mutex
	checks := 0
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/v1beta/interactions":
			fmt.Fprintf(w, `{"status":"completed","steps":[{"type":"model_output","content":[{"type":"video","mime_type":"video/mp4","uri":%q}]}]}`,
				srvURL+"/v1beta/files/abc123:download?alt=media")
		case r.URL.Path == "/v1beta/files/abc123":
			checks++
			if checks < 2 {
				fmt.Fprint(w, `{"name":"files/abc123","state":"PROCESSING"}`)
				return
			}
			fmt.Fprint(w, `{"name":"files/abc123","state":"ACTIVE"}`)
		case r.URL.Path == "/v1beta/files/abc123:download":
			if r.Header.Get("x-goog-api-key") == "" {
				http.Error(w, "no key", 401)
				return
			}
			w.Write([]byte("big-video"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL
	a := newAdapter(srv.URL)
	a.pollEvery = time.Millisecond
	v, err := a.GenerateVideo(context.Background(), llm.VideoRequest{Model: "gemini-omni-1.2-flash", Prompt: "x", Resolution: "1080p"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(v.Body)
	v.Body.Close()
	if string(b) != "big-video" || checks != 2 {
		t.Fatalf("%q after %d checks", b, checks)
	}
}

func TestGeminiVideoURIOtherHostRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"completed","steps":[{"type":"model_output","content":[{"type":"video","uri":"https://evil.example/v1beta/files/x:download"}]}]}`)
	}))
	defer srv.Close()
	_, err := newAdapter(srv.URL).GenerateVideo(context.Background(), llm.VideoRequest{Model: "gemini-omni-1.2-flash", Prompt: "x", Resolution: "4k"}, nil)
	if err == nil || !strings.Contains(err.Error(), "not on the API host") {
		t.Fatal(err)
	}
}
