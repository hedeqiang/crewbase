package api_test

import "testing"

// A vendor avatar: set at creation, switched, refused when malformed; an
// agent from before (no avatar fields) reads as it did, and going back to
// the character style keeps its shape, face and colour.
func TestVendorAvatar(t *testing.T) {
	base, _ := start(t)
	code, a := do(t, base, tok, "POST", "/api/agents", map[string]any{"name": "Old", "color": "#3987e5", "shape": "hex", "face": "dots"})
	if code != 200 {
		t.Fatal(code, a)
	}
	if _, ok := a["avatar"]; ok {
		t.Fatalf("an agent without a vendor avatar carries one: %v", a)
	}
	id := a["id"].(string)
	code, out := do(t, base, tok, "PATCH", "/api/agents/"+id, map[string]any{"avatar": "vendor", "vendor": "deepseek"})
	if code != 200 || out["avatar"] != "vendor" || out["vendor"] != "deepseek" || out["shape"] != "hex" {
		t.Fatalf("vendor avatar not set: %d %v", code, out)
	}
	// follow the model: an empty key
	code, out = do(t, base, tok, "PATCH", "/api/agents/"+id, map[string]any{"vendor": ""})
	if code != 200 || out["avatar"] != "vendor" || out["vendor"] != nil {
		t.Fatalf("follow-model not set: %d %v", code, out)
	}
	for _, bad := range []map[string]any{{"avatar": "photo"}, {"vendor": "Deep Seek"}, {"vendor": 3}, {"vendor": "-x"}} {
		if code, out := do(t, base, tok, "PATCH", "/api/agents/"+id, bad); code != 400 {
			t.Fatalf("%v accepted: %d %v", bad, code, out)
		}
	}
	code, out = do(t, base, tok, "PATCH", "/api/agents/"+id, map[string]any{"avatar": ""})
	if code != 200 || out["avatar"] != nil || out["shape"] != "hex" || out["face"] != "dots" || out["color"] != "#3987e5" {
		t.Fatalf("back to the character: %d %v", code, out)
	}
	if code, out := do(t, base, tok, "POST", "/api/agents", map[string]any{"name": "Bad", "avatar": "vendor", "vendor": "A/B"}); code != 400 {
		t.Fatalf("bad vendor key at creation accepted: %d %v", code, out)
	}
	code, out = do(t, base, tok, "POST", "/api/agents", map[string]any{"name": "Round", "avatar": "vendor", "modelRefs": []string{"mock/default"}})
	if code != 200 || out["avatar"] != "vendor" {
		t.Fatalf("vendor avatar at creation: %d %v", code, out)
	}
}
