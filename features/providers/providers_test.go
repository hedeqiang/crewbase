package providers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/features/providers"
	"github.com/hedeqiang/crewbase/kernel"
)

const secret = "{{SECRET_wjjsydbi}}"

func fakeOpenAI(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":{"message":"invalid api key `+r.Header.Get("Authorization")+`"}}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"object":"list","data":[{"id":"deepseek-flash","object":"model"},{"id":"deepseek-v4-pro","object":"model"}]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"pong\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
}

func setup(t *testing.T) (*kernel.Host, *providers.Service, *llm.Registry, string) {
	t.Helper()
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	dir := t.TempDir()
	h.Load(llm.Plugin, kernel.LoadOptions{})
	h.Load(credentials.Plugin, kernel.LoadOptions{})
	cs := kernel.Use[*credentials.Service](h.Root(), credentials.ServiceKey)
	cs.SetBackend("keychain", credentials.NewMemory()) // never touch the real keychain in tests
	if _, err := h.Load(providers.Plugin, kernel.LoadOptions{Config: map[string]any{"dir": dir}}); err != nil {
		t.Fatal(err)
	}
	return h, kernel.Use[*providers.Service](h.Root(), providers.ServiceKey), kernel.Use[*llm.Registry](h.Root(), llm.ServiceKey), dir
}

func TestTestAddUseRemove(t *testing.T) {
	srv := fakeOpenAI(t)
	defer srv.Close()
	h, s, reg, dir := setup(t)

	// wrong key: error, key never echoed
	bad := s.Test(context.Background(), providers.Input{ID: "ds", API: "openai", BaseURL: srv.URL, APIKey: "sk-wrong-key-123456"})
	if bad.OK || strings.Contains(bad.Error, "sk-wrong-key-123456") {
		t.Fatalf("%+v", bad)
	}
	in := providers.Input{ID: "ds", Name: "DeepSeek", API: "openai", BaseURL: srv.URL, APIKey: secret}
	res := s.Test(context.Background(), in)
	if !res.OK || len(res.Models) != 2 {
		t.Fatalf("%+v", res)
	}
	if _, ok := reg.Get("ds"); ok {
		t.Fatal("test must not register the provider")
	}

	if _, err := s.Add(in); err != nil {
		t.Fatal(err)
	}
	ad, ok := reg.Get("ds")
	if !ok {
		t.Fatal("added provider not registered (hot apply)")
	}
	resp, err := ad.Stream(context.Background(), llm.Request{Model: "deepseek-flash", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "ping")}}, func(llm.Chunk) error { return nil })
	if err != nil || resp.Message.Text() != "pong" {
		t.Fatal(resp, err)
	}
	// the key is in the credential store, never in providers.json
	b, _ := os.ReadFile(filepath.Join(dir, "providers.json"))
	if bytes.Contains(b, []byte(secret)) || !bytes.Contains(b, []byte("keychain:crewbase/provider-ds")) {
		t.Fatalf("providers.json: %s", b)
	}
	if v := s.List()[0]; !v.Active || v.Masked == "" || strings.Contains(v.Masked, secret) {
		t.Fatalf("%+v", v)
	}

	// reload from disk: provider comes back
	h.Unload("@crewbase/providers")
	if _, ok := reg.Get("ds"); ok {
		t.Fatal("unload must unregister")
	}
	h.Load(providers.Plugin, kernel.LoadOptions{Config: map[string]any{"dir": dir}})
	if _, ok := reg.Get("ds"); !ok {
		t.Fatal("provider not restored")
	}
	s = kernel.Use[*providers.Service](h.Root(), providers.ServiceKey)

	if err := s.Remove("ds"); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Get("ds"); ok {
		t.Fatal("removed provider still registered")
	}
	cs := kernel.Use[*credentials.Service](h.Root(), credentials.ServiceKey)
	if cs.Describe("keychain:crewbase/provider-ds").Configured {
		t.Fatal("stored key not deleted on remove")
	}
}

func TestValidation(t *testing.T) {
	_, s, _, _ := setup(t)
	for _, in := range []providers.Input{
		{ID: "Bad ID", APIKey: "k"},
		{ID: "x", BaseURL: "http://evil.example.com", APIKey: "k"},
		{ID: "x", API: "nope", APIKey: "k"},
	} {
		if _, err := s.Add(in); err == nil {
			t.Errorf("%+v should fail", in)
		}
	}
	if _, err := s.Add(providers.Input{ID: "nokey", BaseURL: "https://example.com"}); err == nil {
		t.Error("missing key must fail")
	}
	// preset fills api / base URL / name
	res := s.Test(context.Background(), providers.Input{Preset: "deepseek", APIKey: "k"})
	_ = res
	b, _ := json.Marshal(providers.Presets)
	if !bytes.Contains(b, []byte("api.deepseek.com")) {
		t.Fatal("presets")
	}
}

// An endpoint that cannot list models (Volcengine Ark style) still lets
// the person type model ids; a refused key does not.
func TestManualModelsAndIcon(t *testing.T) {
	noList := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":{"message":"bad key"}}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":{"message":"not found"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"pong\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer noList.Close()
	_, s, reg, dir := setup(t)
	ctx := context.Background()
	in := providers.Input{ID: "ark", Name: "方舟", BaseURL: noList.URL, APIKey: secret}
	if r := s.Test(ctx, in); r.OK || !r.ManualModels {
		t.Fatalf("listing unsupported: %+v", r)
	}
	bad := in
	bad.APIKey = "wrong-key-0123456789"
	if r := s.Test(ctx, bad); r.ManualModels {
		t.Fatalf("a refused key must not offer manual models: %+v", r)
	}
	in.Models, in.Icon = []string{"ep-20261115-abc", "doubao-seed-1-6"}, "volcengine"
	if _, err := s.Add(in); err != nil {
		t.Fatal(err)
	}
	a, ok := reg.Get("ark")
	if !ok {
		t.Fatal("not registered")
	}
	ms, _ := a.Models(ctx)
	if len(ms) != 2 || ms[0].ID != "ep-20261115-abc" {
		t.Fatalf("manual models %+v", ms)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "providers.json"))
	if !strings.Contains(string(b), `"icon": "volcengine"`) {
		t.Fatalf("icon not saved: %s", b)
	}
	for _, x := range []providers.Input{
		{ID: "x1", BaseURL: noList.URL, Icon: "<svg>"},
		{ID: "x2", BaseURL: noList.URL, Models: []string{"bad model id"}},
	} {
		if _, err := s.Add(x); err == nil {
			t.Fatalf("accepted %+v", x)
		}
	}
	// presets carry their vendor icon
	p, _ := s.Add(providers.Input{ID: "ds", Preset: "deepseek", BaseURL: noList.URL, APIKey: secret, Models: []string{"deepseek-flash"}})
	if p.Icon != "deepseek" {
		t.Fatalf("preset icon %q", p.Icon)
	}
}
