package providers_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/features/providers"
	"github.com/hedeqiang/crewbase/kernel"
)

func ptr[T any](v T) *T { return &v }

// Editing an added provider: a blank key keeps the stored secret, the
// model list changes and the adapter is swapped without a restart.
func TestUpdate(t *testing.T) {
	srv := fakeOpenAI(t)
	defer srv.Close()
	h, s, reg, dir := setup(t)
	ctx := context.Background()
	if _, err := s.Add(providers.Input{ID: "ds", Name: "DeepSeek", BaseURL: srv.URL, APIKey: secret,
		Models: []string{"deepseek-flash", "acme-painter"}, Kinds: map[string]string{"acme-painter": "image"}}); err != nil {
		t.Fatal(err)
	}
	before, _ := reg.Get("ds")
	var changed atomic.Int32
	llm.AdaptersChanged.Listen(h.Root(), func(struct{}) { changed.Add(1) })
	ping := func(a llm.Adapter) error {
		_, err := a.Stream(ctx, llm.Request{Model: "deepseek-flash", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "ping")}}, func(llm.Chunk) error { return nil })
		return err
	}

	// re-fetch the listing with the stored key (no key sent)
	if r := s.Test(ctx, providers.Input{ID: "ds", BaseURL: srv.URL}); !r.OK || len(r.Models) != 2 {
		t.Fatalf("re-test with the stored key: %+v", r)
	}
	p, err := s.Update("ds", providers.Patch{Name: ptr("DS 直连"), Icon: ptr("deepseek"), Models: &[]string{"deepseek-v4-pro", "deepseek-flash", "my-manual-id"}})
	if err != nil {
		t.Fatal(err)
	}
	// the removed model's kind mark goes with it
	if p.Name != "DS 直连" || p.Icon != "deepseek" || p.Kinds != nil || p.KeyRef != "keychain:crewbase/provider-ds" {
		t.Fatalf("%+v", p)
	}
	after, ok := reg.Get("ds")
	if !ok || after == before || changed.Load() == 0 {
		t.Fatal("not re-mounted")
	}
	ms, _ := after.Models(ctx)
	if len(ms) != 3 || ms[0].ID != "deepseek-v4-pro" {
		t.Fatalf("%+v", ms)
	}
	if err := ping(after); err != nil {
		t.Fatal("the stored key must keep working:", err)
	}

	// a new key replaces the stored one
	if _, err := s.Update("ds", providers.Patch{APIKey: "sk-other-key-123"}); err != nil {
		t.Fatal(err)
	}
	cs := kernel.Use[*credentials.Service](h.Root(), credentials.ServiceKey)
	if v, _ := cs.Resolve(context.Background(), "keychain:crewbase/provider-ds"); v != "sk-other-key-123" {
		t.Fatalf("key not replaced: %q", v)
	}
	ad, _ := reg.Get("ds")
	if err := ping(ad); err == nil {
		t.Fatal("the new key should be used")
	}

	// kinds alone; invalid edits are refused
	if _, err := s.Update("ds", providers.Patch{Kinds: &map[string]string{"my-manual-id": "video"}}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []providers.Patch{
		{BaseURL: ptr("http://evil.example.com")},
		{Models: &[]string{"bad id"}},
		{Kinds: &map[string]string{"nope": "image"}},
		{Icon: ptr("<svg>")},
	} {
		if _, err := s.Update("ds", bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	if _, err := s.Update("missing", providers.Patch{}); err == nil {
		t.Fatal("unknown provider")
	}
	b, _ := os.ReadFile(filepath.Join(dir, "providers.json"))
	if !strings.Contains(string(b), `"my-manual-id": "video"`) || strings.Contains(string(b), "sk-other") {
		t.Fatalf("%s", b)
	}
}

// The context window a provider's listing reports for a chosen model is
// recorded when it is added, and is what the model reports afterwards
// (the catalog only fills in models the provider says nothing about).
func TestProviderWindowsWin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/models") {
			fmt.Fprint(w, `{"object":"list","data":[{"id":"gw/fast","object":"model","context_length":272000},{"id":"gw/plain","object":"model"}]}`)
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	_, s, reg, _ := setup(t)
	if _, err := s.Add(providers.Input{ID: "gw", API: "openai", BaseURL: srv.URL, APIKey: secret, Models: []string{"gw/fast", "gw/plain"}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		p, _ := s.Get("gw")
		if p.Windows["gw/fast"] == 272000 {
			if _, ok := p.Windows["gw/plain"]; ok {
				t.Fatalf("a window the provider did not report was recorded: %v", p.Windows)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("windows not recorded: %+v", p.Windows)
		}
		time.Sleep(20 * time.Millisecond)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		ad, ok := reg.Get("gw")
		if ok {
			ms, _ := ad.Models(context.Background())
			for _, m := range ms {
				if m.ID == "gw/fast" && m.ContextWindow == 272000 && !m.ContextEstimated {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the adapter does not report the provider's window")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
