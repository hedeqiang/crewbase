package magpie_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/magpie"
)

type fakeMagpie struct {
	mu       sync.Mutex
	gotModel string
	gotAuth  string
	gotUA    string
	effort   any
}

func (f *fakeMagpie) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/hello", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"name":"magpie","version":"9.9.9"}`)
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"object":"list","data":[
		 {"id":"claude/claude-opus-5-5","display_name":"Claude Opus 5.5","owned_by":"claude","reasoning":true,
		  "supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}],"context_window":1000000,"modalities":{"input":["text","image"]}},
		 {"id":"opus-anywhere","display_name":"opus-anywhere","owned_by":"group"},
		 {"id":"flux/flux-2","kind":"image"}]}`)
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(b, &body)
		f.mu.Lock()
		f.gotModel, _ = body["model"].(string)
		f.gotAuth, f.gotUA, f.effort = r.Header.Get("Authorization"), r.Header.Get("User-Agent"), body["reasoning_effort"]
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		for _, s := range []string{
			`{"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"hi "}}]}`,
			`{"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"from magpie"},"finish_reason":"stop"}]}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", s)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	return mux
}

func TestDiscoveryModelsStreamAndDisappear(t *testing.T) {
	f := &fakeMagpie{}
	srv := httptest.NewServer(f.handler())
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	os.MkdirAll(filepath.Join(cfgHome, "magpie"), 0o755)
	os.WriteFile(filepath.Join(cfgHome, "magpie", "settings.json"), []byte(fmt.Sprintf(`{"port":%d,"theme":"dark"}`, port)), 0o644)

	h := kernel.NewHost(nil)
	defer h.Close()
	h.Load(llm.Plugin, kernel.LoadOptions{})
	if _, err := h.Load(magpie.Plugin, kernel.LoadOptions{Config: map[string]any{"rescanInterval": "0"}}); err != nil {
		t.Fatal(err)
	}
	reg := kernel.Use[*llm.Registry](h.Root(), llm.ServiceKey)
	ad, ok := reg.Get("magpie")
	if !ok {
		t.Fatal("magpie adapter not registered")
	}
	if ad.Kind() != magpie.Kind {
		t.Fatal(ad.Kind())
	}
	ms, _ := ad.Models(context.Background())
	if len(ms) != 2 { // image model filtered out
		t.Fatalf("%+v", ms)
	}
	opus := ms[0]
	if opus.ID != "claude/claude-opus-5-5" || opus.ContextWindow != 1000000 || !strings.Contains(strings.Join(opus.Capabilities, ","), "reasoning") || !strings.Contains(strings.Join(opus.Capabilities, ","), "vision") {
		t.Fatalf("%+v", opus)
	}
	if ms[1].ID != "opus-anywhere" || ms[1].ContextWindow != 128000 {
		t.Fatalf("routing group: %+v", ms[1])
	}

	// full ref "magpie/claude/claude-opus-5-5" parses to provider magpie
	ref, err := llm.ParseRef("magpie/claude/claude-opus-5-5@high")
	if err != nil || ref.Provider != "magpie" || ref.Model != "claude/claude-opus-5-5" {
		t.Fatal(ref, err)
	}
	var text strings.Builder
	resp, err := ad.Stream(context.Background(), llm.Request{Model: ref.Model, Effort: ref.Effort, Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}},
		func(c llm.Chunk) error { text.WriteString(c.Text); return nil })
	if err != nil || resp.Message.Text() != "hi from magpie" || text.String() != "hi from magpie" {
		t.Fatalf("%v %q %q", err, resp.Message.Text(), text.String())
	}
	f.mu.Lock()
	if f.gotModel != "claude/claude-opus-5-5" || f.gotAuth != "Bearer "+magpie.AppKey || !strings.HasPrefix(f.gotUA, "Crewbase") || f.effort != "high" {
		t.Fatalf("model=%q auth=%q ua=%q effort=%v", f.gotModel, f.gotAuth, f.gotUA, f.effort)
	}
	f.mu.Unlock()

	st := kernel.Use[*magpie.Discovery](h.Root(), magpie.ServiceKey).Status()
	if !st.Running || st.Version != "9.9.9" || st.Models != 2 {
		t.Fatalf("%+v", st)
	}

	// magpie stops → adapter unregistered; a non-magpie server is ignored
	srv.Close()
	kernel.Use[*magpie.Discovery](h.Root(), magpie.ServiceKey).Rescan(context.Background())
	if _, ok := reg.Get("magpie"); ok {
		t.Fatal("adapter should be removed when magpie stops")
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"name":"other"}`) }))
	defer other.Close()
	h2 := kernel.NewHost(nil)
	defer h2.Close()
	h2.Load(llm.Plugin, kernel.LoadOptions{})
	h2.Load(magpie.Plugin, kernel.LoadOptions{Config: map[string]any{"baseURL": other.URL + "/v1", "rescanInterval": "0"}})
	if _, ok := kernel.Use[*llm.Registry](h2.Root(), llm.ServiceKey).Get("magpie"); ok {
		t.Fatal("non-magpie server must not register")
	}
}
