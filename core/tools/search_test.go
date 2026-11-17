package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// fixtureTools loads testdata/mcp_tools.json: 150 deferred tools from a
// recorded-shape Notion server (79) and GitHub server (71).
func fixtureTools(t testing.TB) []*Tool {
	t.Helper()
	b, err := os.ReadFile("testdata/mcp_tools.json")
	if err != nil {
		t.Fatal(err)
	}
	var raw []struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	out := make([]*Tool, 0, len(raw))
	for _, r := range raw {
		out = append(out, &Tool{Name: r.Name, Description: r.Description, Params: r.Parameters, Load: LoadDeferred})
	}
	return out
}

func TestSearchFindsFixtureTools(t *testing.T) {
	ts := fixtureTools(t)
	if len(ts) != 150 {
		t.Fatalf("fixture has %d tools", len(ts))
	}
	ix := NewIndex(ts)
	cases := []struct{ query, want string }{
		{"create a notion page", "notion__API_post_page"},
		{"list github pull requests", "github__list_pull_requests"},
		{"open a pull request", "github__create_pull_request"},
		{"merge PR", "github__merge_pull_request"},
		{"search notion", "notion__API_post_search"},
		{"append blocks to a notion page", "notion__API_patch_block_children"},
		{"create github issue", "github__create_issue"},
		{"comment on an issue", "github__add_issue_comment"},
		{"query notion database rows", "notion__API_query_data_source"},
		{"read file contents from a repo", "github__get_file_contents"},
		{"workflow run logs", "github__get_job_logs"},
		{"update page properties", "notion__API_patch_page"},
	}
	for _, c := range cases {
		hits := ix.Search(c.query, 3)
		var names []string
		found := false
		for _, h := range hits {
			names = append(names, h.Tool.Name)
			found = found || h.Tool.Name == c.want
		}
		if !found {
			t.Errorf("%q: want %s in top 3, got %v", c.query, c.want, names)
		}
	}
}

func TestSearchNoMatch(t *testing.T) {
	ix := NewIndex(fixtureTools(t))
	if hits := ix.Search("the a of", 5); len(hits) != 0 {
		t.Fatalf("stopwords only matched %d", len(hits))
	}
	if hits := ix.Search("zzzqqq", 5); len(hits) != 0 {
		t.Fatalf("nonsense matched %d", len(hits))
	}
}

func TestTokenize(t *testing.T) {
	got := strings.Join(Tokenize("github__list_pull_requests getPullRequest API-post-page 创建"), " ")
	want := "github list pull request getpullrequest get pull request api post page 创 建"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestOneLine(t *testing.T) {
	if got := OneLine("[MCP notion] Create a page. Long details follow here."); got != "Create a page." {
		t.Fatalf("got %q", got)
	}
	if got := OneLine("first\nsecond"); got != "first" {
		t.Fatalf("got %q", got)
	}
}

func TestGroups(t *testing.T) {
	ts := fixtureTools(t)
	ts = append(ts, &Tool{Name: "computer_click", Load: LoadDeferred}, &Tool{Name: "image_generate", Group: "media", Load: LoadDeferred})
	gs := Groups(ts)
	if len(gs) != 4 || gs[0].Name != "notion" || gs[0].Count != 79 || gs[1].Name != "github" || gs[1].Count != 71 ||
		gs[2].Name != "computer" || gs[3].Name != "media" {
		t.Fatalf("groups = %+v", gs)
	}
	if !strings.Contains(gs[1].Summary, "MCP server tools, e.g. list_pull_requests") {
		t.Fatalf("summary = %q", gs[1].Summary)
	}
}

func TestSplitAndShouldDefer(t *testing.T) {
	ts := []*Tool{{Name: "bash"}, {Name: "x__a", Load: LoadDeferred}, {Name: "tool_search", Load: LoadMeta}, {Name: "read_file", Load: LoadCore}}
	core, def := Split(ts)
	if len(core) != 2 || core[0].Name != "bash" || core[1].Name != "read_file" || len(def) != 1 {
		t.Fatalf("core=%v def=%v", core, def)
	}
	small := make([]*Tool, 30)
	for i := range small {
		small[i] = &Tool{Name: fmt.Sprintf("s__t%d", i), Description: "short", Load: LoadDeferred}
	}
	var auto DeferConfig
	if auto.ShouldDefer(nil, 200000) {
		t.Fatal("nothing to defer")
	}
	if auto.ShouldDefer(small, 200000) {
		t.Fatal("30 small tools should not defer")
	}
	if !auto.ShouldDefer(append(small, &Tool{Name: "s__x", Load: LoadDeferred}), 200000) {
		t.Fatal("31 tools should defer")
	}
	// size: 30 tools exceeding 10% of a small window
	if !auto.ShouldDefer(small, SchemaTokens(small)*9) {
		t.Fatal("schemas over 10% of the window should defer")
	}
	if auto.ShouldDefer(small, SchemaTokens(small)*11) {
		t.Fatal("schemas under 10% should not defer")
	}
	if (DeferConfig{Mode: DeferNever}).ShouldDefer(fixtureTools(t), 1000) {
		t.Fatal("never")
	}
	if !(DeferConfig{Mode: DeferAlways}).ShouldDefer(small[:1], 1<<30) {
		t.Fatal("always")
	}
	if !(DeferConfig{MaxTools: 5}).ShouldDefer(small[:6], 1<<30) {
		t.Fatal("custom max tools")
	}
}
