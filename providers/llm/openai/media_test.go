package openai

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

// a 1x1 PNG
var pngBytes, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==")

func TestModelsKind(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-6-luna","object":"model"},{"id":"gpt-image-2","object":"model"},
		 {"id":"dall-e-3","object":"model"},{"id":"sora-2","object":"model"},{"id":"flux-x","object":"model","kind":"image"}]}`)
	}))
	defer srv.Close()
	ms, err := newAdapter(t, srv.URL, nil).FetchModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"gpt-6-luna": "", "gpt-image-2": "image", "dall-e-3": "image", "sora-2": "video", "flux-x": "image"}
	for _, m := range ms {
		if m.Kind != want[m.ID] {
			t.Errorf("%s: kind %q, want %q", m.ID, m.Kind, want[m.ID])
		}
		if m.ID == "gpt-image-2" && (len(m.Capabilities) != 1 || m.Capabilities[0] != llm.CapImageEdit || m.ContextWindow != 0) {
			t.Errorf("gpt-image-2: %+v", m)
		}
	}
	// static specs: classified too, and an explicit kind wins
	st := newAdapter(t, srv.URL, func(pc *ProviderConfig) {
		pc.Models = []ModelSpec{{ID: "gpt-image-1-mini"}, {ID: "my-painter", Kind: "image"}, {ID: "gpt-image-odd", Kind: "chat"}}
	})
	ms, _ = st.Models(context.Background())
	if ms[0].Kind != "image" || ms[1].Kind != "image" || ms[2].Kind != "chat" {
		t.Fatalf("%+v", ms)
	}
}

func TestGenerateImages(t *testing.T) {
	var mu sync.Mutex
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/images/generations" {
			http.NotFound(w, r)
			return
		}
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(b, &got)
		auth = r.Header.Get("Authorization")
		mu.Unlock()
		img := base64.StdEncoding.EncodeToString(pngBytes)
		fmt.Fprintf(w, `{"created":1,"data":[{"b64_json":%q},{"b64_json":%q}],"output_format":"png","usage":{"input_tokens":12,"output_tokens":272}}`, img, img)
	}))
	defer srv.Close()
	a := newAdapter(t, srv.URL, nil)
	res, err := a.GenerateImages(context.Background(), llm.ImageRequest{Model: "gpt-image-1-mini", Prompt: "a cat", Aspect: "16:9", Quality: "low", N: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Images) != 2 || res.Images[0].Mime != "image/png" || string(res.Images[0].Data) != string(pngBytes) {
		t.Fatalf("%+v", res)
	}
	if res.Usage.OutputTokens != 272 {
		t.Fatalf("usage %+v", res.Usage)
	}
	mu.Lock()
	defer mu.Unlock()
	if got["model"] != "gpt-image-1-mini" || got["prompt"] != "a cat" || got["size"] != "1536x1024" || got["quality"] != "low" || got["n"] != float64(2) {
		t.Fatalf("body %v", got)
	}
	if _, ok := got["response_format"]; ok {
		t.Fatal("GPT image models reject response_format")
	}
	if auth != "Bearer sk-testsecretkey1234567890" {
		t.Fatal(auth)
	}
}

func TestEditImagesAndURLResult(t *testing.T) {
	var srvURL string
	var fields map[string][]string
	var files []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/images/edits":
			if err := r.ParseMultipartForm(10 << 20); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			fields = r.MultipartForm.Value
			for _, fh := range r.MultipartForm.File["image[]"] {
				files = append(files, fh.Filename+"|"+fh.Header.Get("Content-Type"))
			}
			fmt.Fprintf(w, `{"data":[{"url":%q,"revised_prompt":"a cat in a hat"}]}`, srvURL+"/dl/x.png")
		case "/dl/x.png":
			if r.Header.Get("Authorization") != "" {
				t.Error("the key must not go to result links")
			}
			w.Write(pngBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL
	a := newAdapter(t, srv.URL, nil)
	res, err := a.GenerateImages(context.Background(), llm.ImageRequest{Model: "gpt-image-2", Prompt: "add a hat",
		Inputs: []llm.InputImage{{Name: "cat.png", Mime: "image/png", Data: pngBytes}, {Name: "hat.png", Mime: "image/png", Data: pngBytes}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Images) != 1 || res.Images[0].RevisedPrompt != "a cat in a hat" || res.Images[0].Mime != "image/png" {
		t.Fatalf("%+v", res)
	}
	if fields["model"][0] != "gpt-image-2" || fields["prompt"][0] != "add a hat" || len(files) != 2 || files[0] != "cat.png|image/png" {
		t.Fatalf("fields %v files %v", fields, files)
	}
}

func TestGenerateImagesErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprint(w, `{"error":{"message":"Invalid size '1x1'.","type":"invalid_request_error"}}`)
	}))
	defer srv.Close()
	_, err := newAdapter(t, srv.URL, nil).GenerateImages(context.Background(), llm.ImageRequest{Model: "gpt-image-2", Prompt: "x", Size: "1x1"})
	if llm.KindOf(err) != llm.ErrBadRequest || !strings.Contains(err.Error(), "Invalid size") {
		t.Fatal(err)
	}
	// a link to somewhere odd is refused
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"url":"file:///etc/passwd"}]}`)
	}))
	defer srv2.Close()
	if _, err := newAdapter(t, srv2.URL, nil).GenerateImages(context.Background(), llm.ImageRequest{Model: "m", Prompt: "x"}); err == nil {
		t.Fatal("file: links must be refused")
	}
}

func TestGenerateVideoCreatePollDownload(t *testing.T) {
	var mu sync.Mutex
	polls := 0
	var created map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "POST" && r.URL.Path == "/videos":
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &created)
			fmt.Fprint(w, `{"id":"video_123","object":"video","status":"queued","progress":0}`)
		case r.Method == "GET" && r.URL.Path == "/videos/video_123":
			polls++
			if polls < 3 {
				fmt.Fprintf(w, `{"id":"video_123","status":"in_progress","progress":%d}`, polls*30)
				return
			}
			fmt.Fprint(w, `{"id":"video_123","status":"completed","progress":100}`)
		case r.Method == "GET" && r.URL.Path == "/videos/video_123/content":
			w.Header().Set("Content-Type", "video/mp4")
			w.Write([]byte("\x00\x00\x00\x18ftypmp42fake-video"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	a := newAdapter(t, srv.URL, nil)
	a.SetPollInterval(time.Millisecond)
	var seen []int
	v, err := a.GenerateVideo(context.Background(), llm.VideoRequest{Model: "sora-2", Prompt: "a cat", Seconds: 4, Aspect: "9:16", Resolution: "720p"},
		func(p llm.VideoProgress) { seen = append(seen, p.Percent) })
	if err != nil {
		t.Fatal(err)
	}
	defer v.Body.Close()
	b, _ := io.ReadAll(v.Body)
	if v.Mime != "video/mp4" || !strings.Contains(string(b), "fake-video") {
		t.Fatalf("%q %q", v.Mime, b)
	}
	if created["seconds"] != "4" || created["size"] != "720x1280" || created["model"] != "sora-2" {
		t.Fatalf("create body %v", created)
	}
	if len(seen) < 3 || seen[len(seen)-1] != 100 {
		t.Fatalf("progress %v", seen)
	}
}

func TestGenerateVideoFailedAndTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/videos":
			fmt.Fprint(w, `{"id":"v1","status":"queued"}`)
		case "/videos/v1":
			fmt.Fprint(w, `{"id":"v1","status":"failed","error":{"code":"moderation_blocked","message":"blocked by moderation"}}`)
		case "/videos/v2":
			fmt.Fprint(w, `{"id":"v2","status":"in_progress","progress":5}`)
		}
	}))
	defer srv.Close()
	a := newAdapter(t, srv.URL, nil)
	a.SetPollInterval(time.Millisecond)
	_, err := a.GenerateVideo(context.Background(), llm.VideoRequest{Model: "m", Prompt: "x"}, nil)
	if err == nil || !strings.Contains(err.Error(), "blocked by moderation") {
		t.Fatal(err)
	}
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/videos" {
			fmt.Fprint(w, `{"id":"v2","status":"queued"}`)
			return
		}
		fmt.Fprint(w, `{"id":"v2","status":"in_progress","progress":5}`)
	}))
	defer srv2.Close()
	b := newAdapter(t, srv2.URL, nil)
	b.SetPollInterval(time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := b.GenerateVideo(ctx, llm.VideoRequest{Model: "m", Prompt: "x"}, nil); err != context.DeadlineExceeded {
		t.Fatal(err)
	}
}
