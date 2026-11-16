package modelcatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/kernel"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestNormalize(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"claude-opus-5-5", "claude-opus-5-5"},
		{"anthropic/claude-opus-5.5", "claude-opus-5-5"},
		{"magpie/claude/claude-opus-5-5", "claude-opus-5-5"},
		{"alchemypay-anthropic/claude-opus-5-5", "claude-opus-5-5"},
		{"cursor/claude-opus-5-5@1m", "claude-opus-5-5"},
		{"cursor/grok-4.7@500k", "grok-4-7"},
		{"x-ai/grok-4.7", "grok-4-7"},
		{"anthropic/claude-opus-5.5:batch", "claude-opus-5-5"},
		{"openrouter/openai/gpt-6.1-sol", "gpt-6-1-sol"},
		{"codex/GPT-6.1-Sol", "gpt-6-1-sol"},
		{"bedrock/us.anthropic.claude-x-v1:0", "us-anthropic-claude-x-v1"},
		{"  ", ""},
	} {
		if got := Normalize(c.in); got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"openai/gpt-image-2-2026-04-21", []string{"gpt-image-2-2026-04-21", "gpt-image-2"}},
		{"claude-sonnet-4-5-20250929", []string{"claude-sonnet-4-5-20250929", "claude-sonnet-4-5"}},
		{"gpt-4-0613", []string{"gpt-4-0613", "gpt-4"}},
		{"chatgpt-4o-latest", []string{"chatgpt-4o-latest", "chatgpt-4o"}},
		{"deepseek-flash", []string{"deepseek-flash"}},
	} {
		if got := candidates(c.in); strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("candidates(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParseOpenRouter(t *testing.T) {
	format, m, err := Parse(fixture(t, "openrouter.json"))
	if err != nil || format != FormatOpenRouter {
		t.Fatal(format, err)
	}
	opus := m["claude-opus-5-5"]
	// the :batch variant (half price) must not replace the list price
	if opus != (Entry{Context: 1000000, MaxOutput: 128000, Input: 4, Output: 20, CacheRead: 0.2, CacheWrite: 5}) {
		t.Fatalf("%+v", opus)
	}
	if e := m["deepseek-v4-pro"]; e.Input != 0.9 || e.Output != 1.5 || e.CacheRead != 0.25 || e.Context != 1048576 {
		t.Fatalf("%+v", e)
	}
	if e := m["gemini-nano-banana-2-1"]; e.Kind != "image" {
		t.Fatalf("image model: %+v", e)
	}
	if e := m["grok-4-7"]; e.MaxOutput != 450000 || e.Input != 2 {
		t.Fatalf("%+v", e)
	}
}

func TestParseLiteLLM(t *testing.T) {
	format, m, err := Parse(fixture(t, "litellm.json"))
	if err != nil || format != FormatLiteLLM {
		t.Fatal(format, err)
	}
	if _, ok := m["sample-spec"]; ok {
		t.Fatal("sample_spec is documentation")
	}
	if _, ok := m["text-embedding-3-large"]; ok {
		t.Fatal("embeddings are not used")
	}
	if e := m["deepseek-flash"]; e.Input != 0.3 || e.Output != 1.2 || e.Context != 1000000 || e.MaxOutput != 393216 || e.CacheRead != 0.006 {
		t.Fatalf("%+v", e)
	}
	if e := m["gemini-3-pro-image-preview"]; e.Kind != "image" || e.Image != 0.134 {
		t.Fatalf("%+v", e)
	}
	// resellers are left out: only the vendor's own entries
	if e := m["claude-opus-5-5"]; e.Input != 4 || e.CacheWrite != 5 {
		t.Fatalf("%+v", e)
	}
	// a dated-only id is also found undated
	if e, ok := m["claude-sonnet-4-5"]; !ok || e.Input != 3 || e.Context != 200000 {
		t.Fatalf("alias: %+v %v", e, ok)
	}
}

// service builds a Service around the two fixtures served locally.
func service(t *testing.T, dir string, srv *httptest.Server, emb Snapshot) *Service {
	t.Helper()
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	srcs := []string{srv.URL + "/or", srv.URL + "/ll"}
	s, err := New(h.Root(), Config{Dir: dir, Sources: &srcs})
	if err != nil {
		t.Fatal(err)
	}
	s.embedded = emb
	s.mu.Lock()
	s.rebuildLocked()
	s.mu.Unlock()
	return s
}

func fixtureServer(t *testing.T, hits *atomic.Int32, fail *atomic.Bool) *httptest.Server {
	or, ll := fixture(t, "openrouter.json"), fixture(t, "litellm.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "crewbase/") || r.Header.Get("Authorization") != "" || r.URL.RawQuery != "" {
			http.Error(w, "unexpected request", 400)
			return
		}
		if fail.Load() {
			http.Error(w, "down", 503)
			return
		}
		switch r.URL.Path {
		case "/or":
			w.Write(or)
		case "/ll":
			w.Write(ll)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLookupMergeAndCache(t *testing.T) {
	var hits atomic.Int32
	var fail atomic.Bool
	srv := fixtureServer(t, &hits, &fail)
	dir := t.TempDir()

	// embedded snapshot only (first run, offline): one entry for the OpenRouter URL
	emb := Snapshot{V: 1, Sources: []SourceData{{URL: srv.URL + "/or", Format: FormatOpenRouter, Fetched: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		Models: map[string]Entry{"claude-opus-5-5": {Context: 900000, Input: 9, Output: 99}}}}}
	s := service(t, dir, srv, emb)
	if e, ok := s.Lookup("magpie/claude/claude-opus-5-5"); !ok || e.ContextWindow != 900000 || e.Pricing.Input != 9 {
		t.Fatalf("embedded: %+v %v", e, ok)
	}
	if st := s.Status(); st.Sources[0].Origin != "embedded" || st.Sources[1].Origin != "" || !s.due(time.Now()) {
		t.Fatalf("%+v", st)
	}

	st := s.Refresh(context.Background())
	if hits.Load() != 2 || st.Sources[0].Origin != "remote" || st.Sources[1].Origin != "remote" || st.Sources[0].Error != "" || st.Models == 0 {
		t.Fatalf("%+v", st)
	}
	if s.due(time.Now()) {
		t.Fatal("refreshed a moment ago")
	}
	if !s.due(time.Now().Add(25 * time.Hour)) {
		t.Fatal("a day later a refresh is due")
	}
	// OpenRouter first: its price; LiteLLM fills ids it lacks
	opus, _ := s.Lookup("alchemypay-anthropic/claude-opus-5-5")
	if opus.ContextWindow != 1000000 || opus.MaxOutput != 128000 || opus.Pricing.Input != 4 || opus.Pricing.Source != FormatOpenRouter {
		t.Fatalf("%+v %+v", opus, opus.Pricing)
	}
	flash, ok := s.Lookup("deepseek/deepseek-flash")
	if !ok || flash.Pricing.Source != FormatLiteLLM || flash.Pricing.Output != 1.2 {
		t.Fatalf("%+v", flash)
	}
	if img, _ := s.Lookup("gemini/gemini-3-pro-image-preview"); img.Kind != llm.KindImage || img.Pricing.Image != 0.134 {
		t.Fatalf("%+v", img)
	}
	if _, ok := s.Lookup("cursor/composer-2.5"); ok {
		t.Fatal("unknown model matched")
	}

	// the cache is used on the next start, even when the sources are down
	fail.Store(true)
	s2 := service(t, dir, srv, Snapshot{})
	if e, ok := s2.Lookup("claude-opus-5-5"); !ok || e.Pricing.Input != 4 {
		t.Fatalf("cache: %+v %v", e, ok)
	}
	st2 := s2.Refresh(context.Background())
	if st2.Sources[0].Error == "" || st2.Sources[0].Origin != "remote" {
		t.Fatalf("failed refresh keeps the cached data: %+v", st2.Sources[0])
	}
	if e, _ := s2.Lookup("claude-opus-5-5"); e.Pricing.Input != 4 {
		t.Fatal("data lost on a failed refresh")
	}

	// an unreadable cache falls back to the embedded snapshot
	os.WriteFile(filepath.Join(dir, "model-catalog.json"), []byte("{not json"), 0o600)
	s3 := service(t, dir, srv, emb)
	if e, _ := s3.Lookup("claude-opus-5-5"); e.Pricing.Input != 9 {
		t.Fatalf("fallback: %+v", e)
	}
}

func TestSettingsOffline(t *testing.T) {
	var hits atomic.Int32
	var fail atomic.Bool
	srv := fixtureServer(t, &hits, &fail)
	dir := t.TempDir()
	emb := Snapshot{V: 1, Sources: []SourceData{{URL: OpenRouterURL, Format: FormatOpenRouter, Fetched: time.Now(),
		Models: map[string]Entry{"claude-opus-5-5": {Context: 900000}}}}}
	s := service(t, dir, srv, emb)
	if _, err := s.SetSettings(Settings{Sources: []string{"http://example.com/x"}}); err == nil {
		t.Fatal("plain http to a remote host must be refused")
	}
	// no sources: nothing is fetched, the embedded snapshot still answers
	st, err := s.SetSettings(Settings{Sources: []string{}, AutoRefresh: true})
	if err != nil || len(st.Sources) != 0 || s.due(time.Now()) {
		t.Fatal(st, err)
	}
	if e, ok := s.Lookup("claude-opus-5-5"); !ok || e.ContextWindow != 900000 {
		t.Fatalf("%+v", e)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "model-catalog.settings.json"))
	var saved Settings
	if json.Unmarshal(b, &saved) != nil || saved.Sources == nil || len(saved.Sources) != 0 {
		t.Fatalf("%s", b)
	}
	// autoRefresh off: never due
	if _, err := s.SetSettings(Settings{Sources: []string{srv.URL + "/or"}, AutoRefresh: false}); err != nil {
		t.Fatal(err)
	}
	if s.due(time.Now()) {
		t.Fatal("auto refresh is off")
	}
	if hits.Load() != 0 {
		t.Fatal("fetched while offline")
	}
	// settings survive a restart
	h := kernel.NewHost(nil)
	defer h.Close()
	s2, _ := New(h.Root(), Config{Dir: dir})
	if got := s2.Settings(); got.AutoRefresh || len(got.Sources) != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestRegistryEnrich(t *testing.T) {
	var hits atomic.Int32
	var fail atomic.Bool
	srv := fixtureServer(t, &hits, &fail)
	s := service(t, t.TempDir(), srv, Snapshot{})
	s.Refresh(context.Background())
	h := kernel.NewHost(nil)
	defer h.Close()
	h.Load(llm.Plugin, kernel.LoadOptions{})
	reg := kernel.Use[*llm.Registry](h.Root(), llm.ServiceKey)
	reg.SetCatalog(h.Root(), s)
	ms := reg.Enrich("magpie", []llm.ModelInfo{
		{ID: "claude/claude-opus-5-5", ContextWindow: 777, MaxOutput: 5},               // reported: kept
		{ID: "deepseek/deepseek-flash", ContextWindow: 128000, ContextEstimated: true}, // guessed: replaced
		{ID: "x-ai/grok-4.7"},      // unknown: filled
		{ID: "routing-group-name"}, // no match: untouched
	})
	if ms[0].ContextWindow != 777 || ms[0].MaxOutput != 5 || ms[0].Pricing.Input != 4 {
		t.Fatalf("%+v", ms[0])
	}
	if ms[1].ContextWindow != 1000000 || ms[1].ContextEstimated || ms[1].Pricing.Output != 1.2 {
		t.Fatalf("%+v", ms[1])
	}
	if ms[2].ContextWindow != 500000 || ms[2].MaxOutput != 450000 {
		t.Fatalf("%+v", ms[2])
	}
	if ms[3].Pricing != nil || ms[3].ContextWindow != 0 {
		t.Fatalf("%+v", ms[3])
	}
	if p := reg.PriceOf(llm.ModelRef{Provider: "anthropic", Model: "claude-opus-5-5"}); p == nil || p.Output != 20 {
		t.Fatal(p)
	}
	u := llm.Usage{InputTokens: 1_000_000, OutputTokens: 100_000, CacheRead: 500_000}
	if got := fmt.Sprintf("%.2f", reg.PriceOf(llm.ModelRef{Provider: "x", Model: "claude-opus-5-5"}).Cost(u)); got != "4.10" { // 0.5M×4 + 0.5M×0.2 + 0.1M×20
		t.Fatal(got)
	}
}

func TestEmbeddedSnapshot(t *testing.T) {
	snap := Embedded()
	if len(snap.Sources) != 2 {
		t.Fatalf("embedded snapshot has %d sources; run go generate ./features/modelcatalog", len(snap.Sources))
	}
	for _, d := range snap.Sources {
		if len(d.Models) < 100 || d.Fetched.IsZero() {
			t.Fatalf("%s: %d models", d.URL, len(d.Models))
		}
	}
}
