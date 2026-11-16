package llm

import "testing"

func TestComputerTool(t *testing.T) {
	for model, want := range map[string]string{
		"claude-opus-5-5":           "computer_toolset_20260801",
		"claude-sonnet-5-5":         "computer_toolset_20260801",
		"claude-haiku-5-5":          "computer_toolset_20260801",
		"claude-fable-5-2":          "computer_toolset_20260801",
		"anthropic/claude-opus-5-5": "computer_toolset_20260801",
		"claude-opus-4-7":           "computer_20251124",
		"claude-sonnet-4-6":         "computer_20251124",
		"claude-opus-4-5-20251101":  "computer_20251124",
		"claude-3-7-sonnet":         "",
		"gpt-5.4":                   "computer",
		"gpt-6.1-sol":               "computer",
		"gpt-5.2":                   "",
		"gpt-4o":                    "",
		"gemini-3.8-flash":          "computer_use",
		"gemini-4-carbon":           "computer_use",
		"gemini-2.5-flash":          "",
		"deepseek-chat":             "",
	} {
		if got := ComputerTool(model, nil); got != want {
			t.Errorf("%s: %q, want %q", model, got, want)
		}
	}
	if ComputerTool("claude-next", []string{"computer_use"}) != "computer_toolset_20260801" {
		t.Error("capability not honoured")
	}
}

func TestComputerCallRoundTrip(t *testing.T) {
	tc := EncodeComputerCall("c1", ComputerCall{Vendor: ComputerAnthropic, Action: "left_click", Input: []byte(`{"coordinate":[1,2]}`)})
	c, ok := DecodeComputerCall(tc)
	if !ok || c.Action != "left_click" || string(c.Input) != `{"coordinate":[1,2]}` {
		t.Fatalf("%+v %v", c, ok)
	}
	if _, ok := DecodeComputerCall(ToolCall{Name: "computer_act", Args: tc.Args}); ok {
		t.Fatal("decoded a function tool")
	}
	ack, rest := Acknowledged(SafetyAcknowledged + "\nDid it.")
	if !ack || rest != "Did it." {
		t.Fatalf("%v %q", ack, rest)
	}
}
