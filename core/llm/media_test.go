package llm

import "testing"

func TestClassifyModel(t *testing.T) {
	cases := map[string]string{
		// OpenAI
		"gpt-image-2": KindImage, "gpt-image-1-mini": KindImage, "gpt-image-2.5-sunburst-2026-09-08": KindImage,
		"chatgpt-image-latest": KindImage, "dall-e-3": KindImage, "sora-2-pro": KindVideo,
		"gpt-6-luna": KindChat, "gpt-4o": KindChat, "o3": KindChat, "chatgpt-4o-latest": KindChat,
		// Google
		"gemini-nano-banana-2.1": KindImage, "gemini-3.1-flash-lite-image": KindImage, "gemini-3-pro-image": KindImage,
		"gemini-2.5-flash-image": KindImage, "gemini-2.5-flash-image-preview": KindImage, "imagen-4.0-generate-001": KindImage,
		"veo-3.1-generate-preview": KindVideo, "gemini-omni-1.2-flash": KindVideo,
		"gemini-3-flash": KindChat, "gemini-2.5-pro": KindChat,
		// behind gateways: the last segment counts
		"openai/gpt-image-2": KindImage, "alchemypay-openai/gpt-image-1.5": KindImage, "models/veo-3": KindVideo,
		"cursor/claude-opus-5-5@1m": KindChat, "deepseek/deepseek-flash": KindChat,
	}
	for id, want := range cases {
		if got := ClassifyModel(id); got != want {
			t.Errorf("%s: %s, want %s", id, got, want)
		}
	}
}

func TestOutputKindAndEdits(t *testing.T) {
	if (ModelInfo{}).OutputKind() != KindChat || (ModelInfo{Kind: "image"}).OutputKind() != KindImage || (ModelInfo{Kind: "bogus"}).OutputKind() != KindChat {
		t.Fatal("OutputKind")
	}
	if !EditsByID("openai/gpt-image-1-mini") || !EditsByID("gemini-3-pro-image") || EditsByID("dall-e-3") || EditsByID("gpt-6") {
		t.Fatal("EditsByID")
	}
	if m := WithKind(ModelInfo{ID: "veo-3"}); m.Kind != KindVideo {
		t.Fatal(m)
	}
	if m := WithKind(ModelInfo{ID: "gpt-image-2", Kind: KindChat}); m.Kind != KindChat {
		t.Fatal("explicit kind must win")
	}
}
