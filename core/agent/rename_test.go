package agent

import (
	"testing"

	"github.com/hedeqiang/crewbase/core/llm"
)

func TestRenameProviders(t *testing.T) {
	if renameProviders([]llm.ModelRef{{Provider: "acp-codex", Model: "default"}}) != nil {
		t.Fatal("unchanged list must return nil")
	}
	got := renameProviders([]llm.ModelRef{{Provider: "acp-gemini-cli", Model: "default"}, {Provider: "acp-antigravity", Model: "default"}, {Provider: "acp-codex", Model: "default"}})
	if len(got) != 2 || got[0].Provider != "acp-antigravity" || got[1].Provider != "acp-codex" {
		t.Fatalf("%v", got)
	}
}
