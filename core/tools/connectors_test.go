package tools_test

import (
	"context"
	"slices"
	"testing"

	"github.com/hedeqiang/crewbase/core/tools"
)

func names(ts []*tools.Tool) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Name)
	}
	slices.Sort(out)
	return out
}

func noop(context.Context, tools.Call) (tools.Result, error) { return tools.Text("ok"), nil }

// An agent's connector list limits connector tools only: nil = every
// connector, empty = none, a list = those; built-ins always stay; team
// scoping still applies on top.
func TestConnectorScoping(t *testing.T) {
	h, reg, ss := setup(t)
	reg.Register(h.Root(), &tools.Tool{Name: "bash", Execute: noop})
	reg.Register(h.Root(), &tools.Tool{Name: "notion__search", Source: "notion", Execute: noop})
	reg.Register(h.Root(), &tools.Tool{Name: "github__issue", Source: "github", Execute: noop})
	reg.Register(h.Root(), &tools.Tool{Name: "team__x", Source: "teamsrv", Teams: []string{"red"}, Execute: noop})

	lists := map[string]*[]string{}
	teams := map[string]string{"nil": "red", "empty": "red", "gh": "red", "blue": "blue"}
	empty, gh := []string{}, []string{"github", "teamsrv"}
	lists["empty"], lists["gh"], lists["blue"] = &empty, &gh, &gh
	reg.SetTeamOf(func(a string) string { return teams[a] })
	reg.SetConnectorsOf(func(a string) ([]string, bool) {
		l := lists[a]
		if l == nil {
			return nil, false
		}
		return *l, true
	})

	for agent, want := range map[string][]string{
		"nil":   {"bash", "github__issue", "notion__search", "team__x"},
		"empty": {"bash"},
		"gh":    {"bash", "github__issue", "team__x"},
		"blue":  {"bash", "github__issue"}, // listed, but not its team
		"":      {"bash", "github__issue", "notion__search"},
	} {
		if got := names(reg.Visible(agent)); !slices.Equal(got, want) {
			t.Errorf("%q sees %v, want %v", agent, got, want)
		}
		if n := len(reg.SchemasFor(agent, "")); n != len(want) {
			t.Errorf("%q: %d schemas, want %d", agent, n, len(want))
		}
	}
	// a hidden connector tool cannot be called either
	if exe := reg.Run(context.Background(), h.Root(), call(ss, "gh", "notion__search", map[string]any{})); !exe.Result.IsError {
		t.Fatalf("hidden tool ran: %+v", exe.Result)
	}
	if exe := reg.Run(context.Background(), h.Root(), call(ss, "gh", "github__issue", map[string]any{})); exe.Result.IsError {
		t.Fatalf("listed tool refused: %+v", exe.Result)
	}
}

func TestConnectorListing(t *testing.T) {
	h, reg, _ := setup(t)
	sub := h.Root().Fork("")
	reg.AddConnectors(sub, func() []tools.ConnectorInfo {
		return []tools.ConnectorInfo{{Name: "notion", Kind: "mcp", Tools: 79}, {Name: "deepwiki", Kind: "mcp"}}
	})
	reg.AddConnectors(h.Root(), func() []tools.ConnectorInfo { return []tools.ConnectorInfo{{Name: "gmail", Kind: "builtin"}} })
	got := reg.Connectors()
	if len(got) != 3 || got[0].Name != "deepwiki" || got[2].Name != "notion" || !reg.HasConnector("gmail") {
		t.Fatalf("%+v", got)
	}
	sub.Dispose()
	if reg.HasConnector("notion") || len(reg.Connectors()) != 1 {
		t.Fatalf("lister kept after dispose: %+v", reg.Connectors())
	}
}
