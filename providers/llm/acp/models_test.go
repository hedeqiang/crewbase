package acp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
)

// The CLI's models are learned without a prompt, kept on disk, and read
// back by the next adapter (after a restart) without starting the CLI.
func TestProbeModelsAndCache(t *testing.T) {
	state := t.TempDir()
	a := newFake(t, Options{StateDir: state}, nil)
	if a.loadCachedModels() {
		t.Fatal("nothing cached yet")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := a.ProbeModels(ctx); err != nil {
		t.Fatal(err)
	}
	ms, _ := a.Models(ctx)
	if len(ms) != 2 || ms[0].ID != "fast" || ms[1].ID != "smart" {
		t.Fatalf("models %+v", ms)
	}
	b := newFake(t, Options{StateDir: state}, nil)
	if !b.loadCachedModels() {
		t.Fatal("fresh cache not used")
	}
	if b.Running() {
		t.Fatal("reading the cache must not start the CLI")
	}
	if ms, _ := b.Models(ctx); len(ms) != 2 {
		t.Fatalf("cached models %+v", ms)
	}
}

func fakeAs(t *testing.T, id string, env map[string]string) *Adapter {
	t.Helper()
	r := fakeResolved(t, env)
	r.ID = id
	r.cliPath = "" // no `codex debug models` of the test binary
	a := newAdapter("acp-"+id, r, Options{IdleTimeout: -1})
	t.Cleanup(a.Close)
	return a
}

func picks(t *testing.T, a *Adapter, req llm.Request) string {
	t.Helper()
	resp, _, err := stream(t, a, req)
	if err != nil {
		t.Fatal(err)
	}
	txt := resp.Message.Text()
	_, after, _ := strings.Cut(txt, "picks=")
	return after
}

// A folded model at a level is sent as that variant's id, and a new level
// in the same conversation switches the running session.
func TestSetModelSendsVariant(t *testing.T) {
	sess := `{"models":{"currentModelId":"auto-smart[optimize_for=balanced]","availableModels":[
		{"modelId":"auto-smart[optimize_for=balanced]","name":"Auto"},
		{"modelId":"opus[effort=low,fast=false]","name":"Opus Low"},
		{"modelId":"opus[effort=medium,fast=false]","name":"Opus"},
		{"modelId":"opus[effort=high,fast=false]","name":"Opus High"}]}}`
	a := fakeAs(t, "cursor", map[string]string{"FAKE_ACP_SESSION": sess})
	if err := a.ProbeModels(t.Context()); err != nil {
		t.Fatal(err)
	}
	ms, _ := a.Models(t.Context())
	if len(ms) != 2 || ms[1].ID != "opus" || len(ms[1].Efforts) != 3 {
		t.Fatalf("models %+v", ms)
	}
	req := llm.Request{Agent: "a1", Session: "s1", Model: "opus", Effort: "high", Messages: []llm.Message{user("/picks")}}
	if got := picks(t, a, req); got != "model:opus[effort=high,fast=false]" {
		t.Fatalf("picks %q", got)
	}
	resp, _, _ := stream(t, a, req)
	req.Messages = append(req.Messages, resp.Message, user("/picks"))
	req.Effort = "low"
	if got := picks(t, a, req); got != "model:opus[effort=high,fast=false] model:opus[effort=low,fast=false]" {
		t.Fatalf("picks after a level change %q", got)
	}
}

// codex-acp >= 0.9: no session/set_model; models come from the model
// config option and are chosen there, the level in reasoning_effort.
func TestConfigOptionModels(t *testing.T) {
	sess := `{"models":null,"configOptions":[
		{"id":"mode","name":"Approval Preset","category":"mode","type":"select","currentValue":"auto","options":[{"value":"auto","name":"Auto"}]},
		{"id":"model","name":"Model","category":"model","type":"select","currentValue":"gpt-6-astra","options":[
			{"value":"gpt-6-astra","name":"GPT-6-Astra"},{"value":"gpt-6.1-sol","name":"GPT-6.1-Sol"}]},
		{"id":"reasoning_effort","name":"Reasoning Effort","category":"thought_level","type":"select","currentValue":"medium","options":[
			{"value":"low","name":"Low"},{"value":"medium","name":"Medium"},{"value":"high","name":"High"}]}]}`
	a := fakeAs(t, "codex", map[string]string{"FAKE_ACP_SESSION": sess})
	if err := a.ProbeModels(t.Context()); err != nil {
		t.Fatal(err)
	}
	ms, _ := a.Models(t.Context())
	if len(ms) != 2 || ms[0].ID != "gpt-6-astra" || ms[1].ID != "gpt-6.1-sol" {
		t.Fatalf("models from the config option %+v", ms)
	}
	// with the CLI's own list (normally `codex debug models`)
	a.mu.Lock()
	a.native = codexFixture(t)
	a.mu.Unlock()
	a.reshape()
	req := llm.Request{Agent: "a1", Session: "s1", Model: "gpt-6.1-sol", Effort: "high", Messages: []llm.Message{user("/picks")}}
	if got := picks(t, a, req); got != "model=gpt-6.1-sol reasoning_effort=high" {
		t.Fatalf("picks %q", got)
	}
	req.Session, req.Model, req.Effort = "s2", "gpt-6-astra", ""
	if got := picks(t, a, req); got != "" {
		t.Fatalf("the current model at its default level needs no call: %q", got)
	}
}

// A list learned with another CLI or bridge version is probed again.
func TestModelCacheKeyedByVersions(t *testing.T) {
	state := t.TempDir()
	a := newFake(t, Options{StateDir: state}, nil)
	a.SetVersions("cli 1.0 | @zed-industries/codex-acp 0.8.0")
	if err := a.ProbeModels(t.Context()); err != nil {
		t.Fatal(err)
	}
	b := newFake(t, Options{StateDir: state}, nil)
	b.SetVersions("cli 1.0 | @zed-industries/codex-acp 0.8.0")
	if !b.loadCachedModels() {
		t.Fatal("same versions: fresh")
	}
	c := newFake(t, Options{StateDir: state}, nil)
	c.SetVersions("cli 1.0 | @zed-industries/codex-acp 0.16.0")
	if c.loadCachedModels() {
		t.Fatal("a new bridge must re-learn the list")
	}
	if ms, _ := c.Models(t.Context()); len(ms) != 2 {
		t.Fatalf("the old list still shows until then: %+v", ms)
	}
}

// The probe reads `codex debug models` (here a script printing the
// recorded output) and falls back to the bridge's list when it fails.
func TestProbeReadsCodexList(t *testing.T) {
	fixture, err := filepath.Abs("testdata/codex-debug-models.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		script string
		first  string
	}{
		{"#!/bin/sh\n[ \"$1 $2\" = \"debug models\" ] && exec cat " + fixture + "\nexit 2\n", "gpt-6.1-sol"},
		{"#!/bin/sh\necho 'error: unrecognized subcommand debug' >&2\nexit 2\n", "fast"},
	} {
		cli := filepath.Join(t.TempDir(), "codex")
		if err := os.WriteFile(cli, []byte(c.script), 0o755); err != nil {
			t.Fatal(err)
		}
		r := fakeResolved(t, nil)
		r.ID, r.cliPath = "codex", cli
		a := newAdapter("acp-codex", r, Options{IdleTimeout: -1})
		t.Cleanup(a.Close)
		if err := a.ProbeModels(t.Context()); err != nil {
			t.Fatal(err)
		}
		ms, _ := a.Models(t.Context())
		if len(ms) < 1 || ms[0].ID != c.first {
			t.Fatalf("%q: models %+v", c.first, ms)
		}
	}
}

// cursor-agent models leaves a worker running with its stdout: the list is
// read when the command itself exits, not after the timeout.
func TestListerIgnoresLingeringChild(t *testing.T) {
	cli := filepath.Join(t.TempDir(), "cursor-agent")
	script := "#!/bin/sh\nsleep 30 &\nprintf 'Available models\\n\\nauto - Auto (default)\\ngpt-5.2 - GPT-5.2\\n'\n"
	if err := os.WriteFile(cli, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ls := nativeLister(resolved{AgentConfig: AgentConfig{ID: "cursor"}, cliPath: cli})
	t0 := time.Now()
	ms, err := ls(t.Context())
	if err != nil || len(ms) != 2 || ms[0].Name != "Auto" || ms[1].ID != "gpt-5.2" {
		t.Fatalf("%+v %v", ms, err)
	}
	if time.Since(t0) > 10*time.Second {
		t.Fatalf("waited %s for the lingering child", time.Since(t0))
	}
}
