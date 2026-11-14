package telemetry_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/telemetry"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

type receiver struct {
	mu      sync.Mutex
	traces  [][]byte
	metrics [][]byte
	auth    []string
}

func (rc *receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	rc.mu.Lock()
	rc.auth = append(rc.auth, r.Header.Get("Authorization"))
	switch r.URL.Path {
	case "/v1/traces":
		rc.traces = append(rc.traces, b)
	case "/v1/metrics":
		rc.metrics = append(rc.metrics, b)
	}
	rc.mu.Unlock()
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(200)
}

func run(t *testing.T, includeContent bool) (*receiver, []string) {
	t.Helper()
	rc := &receiver{}
	srv := httptest.NewServer(rc)
	t.Cleanup(srv.Close)
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	root := t.TempDir()
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, nil}, {prompt.Plugin, nil}, {tools.Plugin, nil}, {approval.Plugin, nil},
		{llm.Plugin, nil}, {mock.Plugin, nil}, {credentials.Plugin, nil},
		{agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"mock/default"}}},
		{agentloop.Plugin, nil},
		{telemetry.Plugin, map[string]any{"endpoint": srv.URL, "includeContent": includeContent,
			"headers": map[string]any{"Authorization": "Bearer ${mem:otel}", "X-Plain": "v"}}},
	} {
		p, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg})
		if err != nil {
			t.Fatal(err)
		}
		if info, _ := h.Plugin(p.ID()); info.State != kernel.StateActive {
			t.Fatalf("%+v", info)
		}
	}
	r := h.Root()
	if err := kernel.Use[*credentials.Service](r, credentials.ServiceKey).Set("mem:otel", "otel-token"); err != nil {
		t.Fatal(err)
	}
	kernel.Use[*tools.Registry](r, tools.ServiceKey).Register(r, &tools.Tool{Name: "noop", Execute: func(context.Context, tools.Call) (tools.Result, error) {
		return tools.Text("SECRET-RESULT"), nil
	}})
	a, _ := kernel.Use[*agent.Registry](r, agent.ServiceKey).Create(agent.Spec{Name: "Tel"})
	if _, err := a.Enqueue(agent.Input{Text: `/tool noop {"q":"SECRET-ARG"}`}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if !agentloop.WaitIdle(a, 3*time.Second) {
		t.Fatal("agent did not finish")
	}
	svc := kernel.Use[*telemetry.Service](r, telemetry.ServiceKey)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if len(rc.traces) == 0 || len(rc.metrics) == 0 {
		t.Fatalf("traces=%d metrics=%d", len(rc.traces), len(rc.metrics))
	}
	var names []string
	for _, b := range rc.traces {
		var req coltracepb.ExportTraceServiceRequest
		if err := proto.Unmarshal(b, &req); err != nil {
			t.Fatal(err)
		}
		for _, rs := range req.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				for _, sp := range ss.Spans {
					names = append(names, sp.Name)
				}
			}
		}
	}
	for _, a := range rc.auth {
		if a != "Bearer otel-token" {
			t.Fatalf("auth header %q", a)
		}
	}
	return rc, names
}

func TestSpansWithoutContent(t *testing.T) {
	rc, names := run(t, false)
	got := strings.Join(names, ",")
	for _, want := range []string{"crewbase.turn", "crewbase.step", "crewbase.tool noop"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing span %s in %s", want, got)
		}
	}
	steps := strings.Count(got, "crewbase.step")
	if steps != 2 {
		t.Errorf("want 2 steps (tool call + reply), got %d", steps)
	}
	all := string(append(join(rc.traces), join(rc.metrics)...))
	for _, secret := range []string{"SECRET-ARG", "SECRET-RESULT", "/tool noop"} {
		if strings.Contains(all, secret) {
			t.Errorf("content %q exported without includeContent", secret)
		}
	}
	if !strings.Contains(string(join(rc.metrics)), "crewbase.llm.tokens") || !strings.Contains(string(join(rc.metrics)), "crewbase.turns") {
		t.Error("token / turn metrics missing")
	}
}

func TestIncludeContent(t *testing.T) {
	rc, _ := run(t, true)
	all := string(join(rc.traces))
	for _, want := range []string{"SECRET-ARG", "SECRET-RESULT", "/tool noop"} {
		if !strings.Contains(all, want) {
			t.Errorf("content %q missing with includeContent", want)
		}
	}
}

func TestConfigValidation(t *testing.T) {
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	for i, cfg := range []map[string]any{nil, {"endpoint": "collector:4318"}} {
		p, _ := h.Load(telemetry.Plugin, kernel.LoadOptions{ID: string(rune('a' + i)), Config: cfg})
		if info, _ := h.Plugin(p.ID()); info.State != kernel.StateFailed {
			t.Fatalf("%v: %+v", cfg, info)
		}
	}
}

func join(bs [][]byte) []byte {
	var out []byte
	for _, b := range bs {
		out = append(out, b...)
	}
	return out
}
