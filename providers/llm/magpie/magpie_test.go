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
	"sync/atomic"
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
		if r.URL.Query().Get("kind") != "chat,image,video" {
			http.Error(w, "want every kind", 400)
			return
		}
		fmt.Fprint(w, `{"object":"list","data":[
		 {"id":"claude/claude-opus-5-5","display_name":"Claude Opus 5.5","owned_by":"claude","reasoning":true,
		  "supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}],"context_window":1000000,"modalities":{"input":["text","image"]}},
		 {"id":"opus-anywhere","display_name":"opus-anywhere","owned_by":"group"},
		 {"id":"flux/flux-2","kind":"image","supported_endpoint_types":["image-generation","image-edit"]},
		 {"id":"x/embed","kind":"embedding"}]}`)
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
	if len(ms) != 3 { // embeddings left out
		t.Fatalf("%+v", ms)
	}
	if img := ms[1]; img.ID != "flux/flux-2" || img.Kind != llm.KindImage || len(img.Capabilities) != 1 || img.Capabilities[0] != llm.CapImageEdit {
		t.Fatalf("image model: %+v", img)
	}
	ms = append(ms[:1], ms[2])
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
	if !st.Running || st.Version != "9.9.9" || st.Models != 3 {
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

func TestEnabledModels(t *testing.T) {
	f := &fakeMagpie{}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	state := t.TempDir()
	load := func() (*kernel.Host, llm.Adapter, *magpie.Discovery) {
		h := kernel.NewHost(nil)
		h.Load(llm.Plugin, kernel.LoadOptions{})
		if _, err := h.Load(magpie.Plugin, kernel.LoadOptions{Config: map[string]any{"baseURL": srv.URL + "/v1", "rescanInterval": "0", "stateDir": state}}); err != nil {
			t.Fatal(err)
		}
		ad, ok := kernel.Use[*llm.Registry](h.Root(), llm.ServiceKey).Get("magpie")
		if !ok {
			t.Fatal("not registered")
		}
		return h, ad, kernel.Use[*magpie.Discovery](h.Root(), magpie.ServiceKey)
	}
	ids := func(ad llm.Adapter) string {
		ms, _ := ad.Models(context.Background())
		var out []string
		for _, m := range ms {
			out = append(out, m.ID)
		}
		return strings.Join(out, ",")
	}
	h, ad, d := load()
	// default: everything, as before
	if got := ids(ad); got != "claude/claude-opus-5-5,flux/flux-2,opus-anywhere" {
		t.Fatal(got)
	}
	if ch := d.Choice(); !ch.Running || len(ch.Models) != 3 || ch.Enabled != nil {
		t.Fatalf("%+v", ch)
	}
	var changed atomic.Int32
	llm.AdaptersChanged.Listen(h.Root(), func(struct{}) { changed.Add(1) })
	if err := d.SetEnabled([]string{"flux/flux-2", "claude/claude-opus-5-5", "gone/model"}); err != nil {
		t.Fatal(err)
	}
	if got := ids(ad); got != "claude/claude-opus-5-5,flux/flux-2" || changed.Load() == 0 {
		t.Fatal(got, changed.Load())
	}
	if st := d.Status(); st.Models != 2 || st.Total != 3 || !st.Chosen {
		t.Fatalf("%+v", st)
	}
	// a disabled model still streams for an agent that already uses it
	if _, err := ad.Stream(context.Background(), llm.Request{Model: "opus-anywhere", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, func(llm.Chunk) error { return nil }); err != nil {
		t.Fatal(err)
	}
	// kept across restarts and rescans
	h.Close()
	h, ad, d = load()
	defer h.Close()
	d.Rescan(context.Background())
	if got := ids(ad); got != "claude/claude-opus-5-5,flux/flux-2" {
		t.Fatal(got)
	}
	// null = all again
	if err := d.SetEnabled(nil); err != nil {
		t.Fatal(err)
	}
	if got := ids(ad); got != "claude/claude-opus-5-5,flux/flux-2,opus-anywhere" {
		t.Fatal(got)
	}
}
