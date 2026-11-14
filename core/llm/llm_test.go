package llm

import "testing"

func TestParseRef(t *testing.T) {
	cases := map[string]ModelRef{
		"anthropic/claude-opus-5-5":            {Provider: "anthropic", Model: "claude-opus-5-5"},
		"anthropic/claude-opus-5-5@high":       {Provider: "anthropic", Model: "claude-opus-5-5", Effort: "high"},
		"magpie/cursor/grok-4.7@500k":          {Provider: "magpie", Model: "cursor/grok-4.7@500k"},
		"magpie/cursor/claude-opus-5-5@1m@low": {Provider: "magpie", Model: "cursor/claude-opus-5-5@1m", Effort: "low"},
		"magpie/auto/claude-haiku-5-5":         {Provider: "magpie", Model: "auto/claude-haiku-5-5"},
	}
	for in, want := range cases {
		got, err := ParseRef(in)
		if err != nil || got != want {
			t.Errorf("%s: got %+v %v", in, got, err)
		}
		if got.Full() != in {
			t.Errorf("round trip %s → %s", in, got.Full())
		}
	}
	for _, bad := range []string{"", "nomodel", "/x", "p/"} {
		if _, err := ParseRef(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}
