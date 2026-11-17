package api_test

import (
	"testing"

	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

// PATCH connectors: a list of known connectors, null clears the limit,
// unknown names are refused unless already on the agent's list.
func TestPatchConnectors(t *testing.T) {
	base, h := start(t)
	reg := kernel.Use[*tools.Registry](h.Root(), tools.ServiceKey)
	known := []tools.ConnectorInfo{{Name: "notion", Kind: "mcp"}, {Name: "deepwiki", Kind: "mcp"}}
	reg.AddConnectors(h.Root(), func() []tools.ConnectorInfo { return known })
	code, out := do(t, base, tok, "POST", "/api/agents", map[string]any{"name": "Dev"})
	if code != 200 {
		t.Fatal(code, out)
	}
	id := out["id"].(string)
	if _, ok := out["connectors"]; ok {
		t.Fatal("new agent has a connector list:", out["connectors"])
	}
	code, out = do(t, base, tok, "GET", "/api/connectors", nil)
	if cs, _ := out["connectors"].([]any); code != 200 || len(cs) != 2 {
		t.Fatal(code, out)
	}
	if code, out := do(t, base, tok, "PATCH", "/api/agents/"+id, map[string]any{"connectors": []string{"nope"}}); code != 400 {
		t.Fatal("unknown connector accepted", code, out)
	}
	code, out = do(t, base, tok, "PATCH", "/api/agents/"+id, map[string]any{"connectors": []string{"deepwiki", "deepwiki"}})
	if l, _ := out["connectors"].([]any); code != 200 || len(l) != 1 || l[0] != "deepwiki" {
		t.Fatal(code, out)
	}
	// deepwiki goes away: keeping it on the list still saves
	known = known[:1]
	code, out = do(t, base, tok, "PATCH", "/api/agents/"+id, map[string]any{"connectors": []string{"deepwiki", "notion"}})
	if l, _ := out["connectors"].([]any); code != 200 || len(l) != 2 {
		t.Fatal(code, out)
	}
	// other fields leave it alone
	code, out = do(t, base, tok, "PATCH", "/api/agents/"+id, map[string]any{"role": "x"})
	if l, _ := out["connectors"].([]any); code != 200 || len(l) != 2 {
		t.Fatal(code, out)
	}
	code, out = do(t, base, tok, "PATCH", "/api/agents/"+id, map[string]any{"connectors": []string{}})
	if l, ok := out["connectors"].([]any); code != 200 || !ok || len(l) != 0 {
		t.Fatal("empty list must stay a list (no connectors):", code, out)
	}
	code, out = do(t, base, tok, "PATCH", "/api/agents/"+id, map[string]any{"connectors": nil})
	if _, ok := out["connectors"]; code != 200 || ok {
		t.Fatal("null must clear the limit:", code, out)
	}
}
