package wasm_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/bridge/wasm"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

var (
	buildOnce sync.Once
	built     string
	buildErr  error
)

// example builds examples/plugins/wasm-redact into a temp dir (once).
func example(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "wasm-example-")
		if err != nil {
			buildErr = err
			return
		}
		src, _ := filepath.Abs("../../examples/plugins/wasm-redact")
		out := filepath.Join(dir, "plugin.wasm")
		goBin, err := exec.LookPath("go")
		if err != nil {
			buildErr = err
			return
		}
		cmd := exec.Command(goBin, "build", "-buildmode=c-shared", "-o", out, ".")
		cmd.Dir = src
		cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "GOWORK=off")
		if b, err := cmd.CombinedOutput(); err != nil {
			buildErr = err
			t.Logf("%s", b)
			return
		}
		built = out
	})
	if buildErr != nil {
		t.Skipf("cannot build the example module: %v", buildErr)
	}
	return built
}

func TestHooksRedactAndRule(t *testing.T) {
	path := example(t)
	m, err := wasm.Load(context.Background(), "wasm-redact", path, []string{"stream_chunk", "tool_result", "rule"}, time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(context.Background())

	var out struct{ Text *string }
	if err := m.Call(context.Background(), "stream_chunk", map[string]string{"type": "text", "text": "see INC-12345 now"}, &out); err != nil || out.Text == nil || *out.Text != "see INC-••• now" {
		t.Fatalf("stream: %v %v", err, out.Text)
	}
	out.Text = nil
	if err := m.Call(context.Background(), "stream_chunk", map[string]string{"type": "text", "text": "nothing here"}, &out); err != nil || out.Text != nil {
		t.Fatalf("unchanged chunk rewritten: %v %v", err, out.Text)
	}

	// wired into the pipelines
	h := kernel.NewHost(nil)
	defer h.Close()
	if _, err := h.Load(tools.Plugin, kernel.LoadOptions{}); err != nil {
		t.Fatal(err)
	}
	c := h.Root().Fork("")
	wasm.Attach(c, m)
	ch, err := llm.StreamChunk.Run(c, context.Background(), llm.Chunk{Type: "text", Text: "ticket INC-999"}, func(_ context.Context, x llm.Chunk) (llm.Chunk, error) { return x, nil })
	if err != nil || ch.Text != "ticket INC-•••" {
		t.Fatalf("stream pipeline: %v %q", err, ch.Text)
	}
	reg := kernel.Use[*tools.Registry](h.Root(), tools.ServiceKey)
	reg.Register(c, &tools.Tool{Name: "lookup", Category: tools.CategoryRead, Description: "x", PathArgs: []string{"path"},
		Execute: func(context.Context, tools.Call) (tools.Result, error) {
			return tools.Text("found INC-4242 in the log"), nil
		}})
	exe := reg.Run(context.Background(), c, tools.Call{ID: "1", Name: "lookup", Args: json.RawMessage(`{"path":"logs/a.txt"}`)})
	if exe.Result.Content != "found INC-••• in the log" {
		t.Fatalf("tool result: %+v", exe.Result)
	}
	// the rule asks before a tool touches prod/ (no approver here → not run)
	exe = reg.Run(context.Background(), c, tools.Call{ID: "2", Name: "lookup", Args: json.RawMessage(`{"path":"prod/db.txt"}`)})
	if !exe.Denied && !exe.Result.IsError {
		t.Fatalf("prod/ access not gated: %+v", exe)
	}
	if !strings.Contains(exe.Result.Content, "prod/") && !strings.Contains(exe.Result.Content, "approv") {
		t.Fatalf("gate reason: %q", exe.Result.Content)
	}
}

// A hook that never returns is cut off by the budget, passes through, and
// the next call works on a fresh instance.
func TestBudgetAndRecovery(t *testing.T) {
	path := example(t)
	m, err := wasm.Load(context.Background(), "spin", path, []string{"stream_chunk", "spin"}, 100*time.Millisecond, nil)
	if err == nil {
		defer m.Close(context.Background())
	}
	// "spin" is not a known hook name for manifests, but Load accepts any
	// exported hook (the manifest layer restricts names)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	var out struct{ Text *string }
	if err := m.Call(context.Background(), "spin", map[string]string{}, &out); err != wasm.ErrBudget {
		t.Fatalf("spin: %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("budget not enforced: %v", d)
	}
	if err := m.Call(context.Background(), "stream_chunk", map[string]string{"type": "text", "text": "INC-111"}, &out); err != nil || out.Text == nil || *out.Text != "INC-•••" {
		t.Fatalf("after a timeout: %v %v", err, out.Text)
	}
}

func TestRefusesForeignImportsAndMissingHooks(t *testing.T) {
	path := example(t)
	if _, err := wasm.Load(context.Background(), "x", path, []string{"nope"}, 0, nil); err == nil || !strings.Contains(err.Error(), "hook_nope") {
		t.Fatalf("missing hook: %v", err)
	}
	// a module importing something other than WASI (env.danger)
	dir := t.TempDir()
	mod := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00, // magic + version
		0x01, 0x04, 0x01, 0x60, 0x00, 0x00, // type: () -> ()
		0x02, 0x0e, 0x01, 0x03, 'e', 'n', 'v', 0x06, 'd', 'a', 'n', 'g', 'e', 'r', 0x00, 0x00, // import env.danger
	}
	p := filepath.Join(dir, "bad.wasm")
	os.WriteFile(p, mod, 0o644)
	if _, err := wasm.Load(context.Background(), "bad", p, nil, 0, nil); err == nil || !strings.Contains(err.Error(), "only WASI") {
		t.Fatalf("foreign import: %v", err)
	}
}
