package agent_test

import (
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/core/agent"
)

func TestValidAvatar(t *testing.T) {
	for _, ok := range [][2]string{{"", ""}, {"vendor", ""}, {"vendor", "deepseek"}, {"vendor", "lm-studio2"}, {"", "qwen"}} {
		if err := agent.ValidAvatar(ok[0], ok[1]); err != nil {
			t.Errorf("%q/%q refused: %v", ok[0], ok[1], err)
		}
	}
	for _, bad := range [][2]string{{"photo", ""}, {"vendor", "DeepSeek"}, {"vendor", "a b"}, {"vendor", "-a"}, {"vendor", "a/b"}, {"vendor", strings.Repeat("a", 33)}} {
		if agent.ValidAvatar(bad[0], bad[1]) == nil {
			t.Errorf("%q/%q accepted", bad[0], bad[1])
		}
	}
}

// An agent's vendor avatar survives a restart; an unknown style is refused.
func TestVendorAvatarPersists(t *testing.T) {
	root := t.TempDir()
	r := startAgents(t, root)
	a, err := r.Create(agent.Spec{Name: "Ana", Shape: "hex", Avatar: agent.AvatarVendor, Vendor: "qwen"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Create(agent.Spec{Name: "Bo", Avatar: "photo"}); err == nil {
		t.Fatal("unknown avatar style accepted")
	}
	r2 := startAgents(t, root)
	b, ok := r2.Get(a.ID())
	if !ok {
		t.Fatal("agent gone after restart")
	}
	if s := b.Spec(); s.Avatar != agent.AvatarVendor || s.Vendor != "qwen" || s.Shape != "hex" {
		t.Fatalf("after restart: %+v", s)
	}
}
