package catalog

import (
	"testing"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/features/mcp"
)

// A connector installed from a team's (or an agent's) context is scoped to
// that team; an explicit scope, global included, wins.
func TestInstallScopeFromContext(t *testing.T) {
	mk := func(id string) Preset {
		return Preset{ID: id, Category: "dev", Name: id, Auth: "none", Docs: "https://example.com", Server: id, Transport: "http", URL: "http://127.0.0.1:1/mcp"}
	}
	withPresets(t, mk("a"), mk("b"), mk("c"), mk("d"))
	e := newEnv(t)
	if _, err := e.reg.Create(agent.Spec{Name: "Kai", Team: "red"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		id   string
		req  Request
		want string
	}{
		{"a", Request{Context: "team:blue"}, "team:blue"},
		{"b", Request{Context: "kai"}, "team:red"},
		{"c", Request{Context: "team:blue", Scope: mcp.ScopeGlobal}, mcp.ScopeGlobal},
		{"d", Request{}, mcp.ScopeGlobal},
	} {
		if code, raw := e.do(t, "POST", "/api/catalog/"+c.id+"/install", c.req); code != 200 {
			t.Fatal(c.id, code, raw)
		}
		if v, _ := e.mcp.Get(c.id); v.Scope != c.want {
			t.Errorf("%s: scope %q, want %q", c.id, v.Scope, c.want)
		}
	}
	if code, _ := e.do(t, "POST", "/api/catalog/a/install", Request{Context: "ghost"}); code != 400 {
		t.Fatal("unknown agent context accepted", code)
	}
}
