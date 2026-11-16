package acp

import (
	"context"
	"testing"
	"time"
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
	if len(ms) != 3 || ms[1].ID != "fast" || ms[2].ID != "smart" {
		t.Fatalf("models %+v", ms)
	}
	b := newFake(t, Options{StateDir: state}, nil)
	if !b.loadCachedModels() {
		t.Fatal("fresh cache not used")
	}
	if b.Running() {
		t.Fatal("reading the cache must not start the CLI")
	}
	if ms, _ := b.Models(ctx); len(ms) != 3 {
		t.Fatalf("cached models %+v", ms)
	}
}
