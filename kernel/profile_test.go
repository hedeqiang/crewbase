package kernel

import "testing"

func TestPatchMergesConfig(t *testing.T) {
	base := []Entry{{ID: "relay", Name: "@x/relay", Config: map[string]any{"dir": "/data", "a": 1}, Disabled: true}}
	out := ApplyPatch(base, []Entry{{ID: "relay", Config: map[string]any{"url": "https://r", "a": 2}}})
	if out[0].Name != "@x/relay" || out[0].Disabled || out[0].Config["dir"] != "/data" || out[0].Config["url"] != "https://r" || out[0].Config["a"] != 2 {
		t.Fatalf("merge: %+v", out[0])
	}
	if base[0].Config["url"] != nil {
		t.Fatal("base mutated")
	}
	// a name replaces the whole entry
	out = ApplyPatch(base, []Entry{{ID: "relay", Name: "@y/relay", Config: map[string]any{"url": "u"}}})
	if out[0].Name != "@y/relay" || out[0].Config["dir"] != nil {
		t.Fatalf("replace: %+v", out[0])
	}
	// only disabled toggles
	out = ApplyPatch(base, []Entry{{ID: "relay"}})
	if out[0].Disabled || out[0].Config["dir"] != "/data" {
		t.Fatalf("toggle: %+v", out[0])
	}
}
