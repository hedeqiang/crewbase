package acp

import (
	"context"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
)

// A model the account cannot use (Codex with a ChatGPT plan) is listed
// switched off after the CLI refuses it, also after a restart.
func TestUnsupportedModelIsSwitchedOff(t *testing.T) {
	state := t.TempDir()
	a := newFake(t, Options{StateDir: state}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := a.ProbeModels(ctx); err != nil {
		t.Fatal(err)
	}
	a.noteQuota(&llm.Error{Kind: llm.ErrModelUnavailable, Message: "The 'smart' model is not supported when using Codex with a ChatGPT account."}, "smart")
	check := func(ad *Adapter, when string) {
		ms, _ := ad.Models(ctx)
		on := map[string]bool{}
		for _, m := range ms {
			on[m.ID] = m.Enabled
		}
		if !on["fast"] || on["smart"] {
			t.Fatalf("%s: %+v", when, ms)
		}
	}
	check(a, "now")
	b := newFake(t, Options{StateDir: state}, nil)
	b.loadCachedModels()
	check(b, "after a restart")
}

func TestAccountBanner(t *testing.T) {
	for _, s := range []string{
		"\n⚠ This service has been disabled in this account for violation of Terms of Service. Please submit an appeal to continue using this product.",
		"Your account has been suspended.",
	} {
		if !reAccountBanner.MatchString(s) {
			t.Errorf("not matched: %q", s)
		}
	}
	if reAccountBanner.MatchString("I disabled the service in this account config as you asked.") {
		t.Error("ordinary reply matched")
	}
}
