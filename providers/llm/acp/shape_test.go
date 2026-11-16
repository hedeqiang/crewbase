package acp

import (
	"bufio"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

// testdata/codex-debug-models.json is `codex debug models` of Codex CLI
// 0.167 (2026-11-17), trimmed to the fields read; testdata/agy-models.txt
// is `agy models` of the same day.

func codexFixture(t *testing.T) []nativeModel {
	t.Helper()
	b, err := os.ReadFile("testdata/codex-debug-models.json")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := parseCodexModels(b)
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

func ids(es []modelEntry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.ID
	}
	return out
}

func TestParseCodexModels(t *testing.T) {
	ms := codexFixture(t)
	var got []string
	for _, m := range ms {
		got = append(got, m.ID)
	}
	want := []string{"gpt-6.1-sol", "gpt-6-astra", "gpt-6.1-terra", "gpt-6-sol", "gpt-6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"}
	if !slices.Equal(got, want) {
		t.Fatalf("listed models %v (hidden ones must be left out)", got)
	}
	sol := ms[0]
	if sol.Name != "GPT-6.1-Sol" || sol.DefaultEffort != "medium" || sol.ContextWindow != 500000 || !sol.Vision {
		t.Fatalf("gpt-6.1-sol %+v", sol)
	}
	// Codex's "ultra" has no Crewbase level
	if !slices.Equal(sol.Efforts, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("efforts %v", sol.Efforts)
	}
	if _, err := parseCodexModels([]byte("error: unknown command")); err == nil {
		t.Fatal("garbage must fail (falls back to the bridge's list)")
	}
}

// codex-acp 0.8 reports "<slug>/<effort>" presets of the Codex it bundles
// plus the configured model; the CLI's own list wins, and the id sent for
// a model + level is what that bridge parses.
func TestCodexPrefersCLIList(t *testing.T) {
	old := []acpModel{
		{ModelID: "gpt-6-astra", Name: "gpt-6-astra"},
		{ModelID: "gpt-5.2-codex/low", Name: "gpt-5.2-codex (low)"},
		{ModelID: "gpt-5.2-codex/medium", Name: "gpt-5.2-codex (medium)"},
		{ModelID: "gpt-5.2-codex/high", Name: "gpt-5.2-codex (high)"},
		{ModelID: "gpt-5.2-codex/xhigh", Name: "gpt-5.2-codex (xhigh)"},
		{ModelID: "gpt-5.1-codex-max/low", Name: "gpt-5.1-codex-max (low)"},
		{ModelID: "gpt-5.1-codex-max/medium", Name: "gpt-5.1-codex-max (medium)"},
	}
	a := newAdapter("acp-codex", resolved{AgentConfig: AgentConfig{ID: "codex", Name: "Codex", ContextWindow: 272000}}, Options{})
	a.acpModels, a.native = old, codexFixture(t)
	a.reshape()
	es := a.entries
	if len(es) != 8 || es[0].ID != "gpt-6.1-sol" || slices.Contains(ids(es), "gpt-5.2-codex") {
		t.Fatalf("offered %v", ids(es))
	}
	for _, c := range []struct{ model, effort, want string }{
		{"gpt-6.1-sol", "high", "gpt-6.1-sol/high"},
		{"gpt-6.1-sol", "", "gpt-6.1-sol/medium"},         // the model's default level
		{"gpt-6.1-sol", "max", "gpt-6.1-sol"},             // 0.8 cannot spell max: bare slug
		{"gpt-6.1-terra", "minimal", "gpt-6.1-terra/low"}, // not a level of the model: its default
		{"gpt-5.2-codex/low", "", "gpt-5.2-codex/low"},    // an id picked earlier passes as is
	} {
		if got := a.setModelID(c.model, c.effort); got != c.want {
			t.Errorf("%s@%s -> %q, want %q", c.model, c.effort, got, c.want)
		}
	}
	ms, _ := a.Models(t.Context())
	if ms[1].ID != "gpt-6.1-sol" || ms[1].ContextWindow != 500000 || !slices.Contains(ms[1].Capabilities, "vision") || len(ms[1].Efforts) != 5 {
		t.Fatalf("model info %+v", ms[1])
	}

	// without the CLI's list the bridge's presets fold into models with levels
	es = shapeModels("codex", old, nil)
	if !slices.Equal(ids(es), []string{"gpt-6-astra", "gpt-5.2-codex", "gpt-5.1-codex-max"}) {
		t.Fatalf("folded %v", ids(es))
	}
	if e := es[1]; e.Name != "gpt-5.2-codex" || !slices.Equal(e.Efforts, []string{"low", "medium", "high", "xhigh"}) || e.wire("high") != "gpt-5.2-codex/high" || e.wire("") != "gpt-5.2-codex/medium" {
		t.Fatalf("gpt-5.2-codex %+v", e)
	}
}

func TestAntigravityFoldsLevels(t *testing.T) {
	f, err := os.Open("testdata/agy-models.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var ms []acpModel
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		id, name, ok := strings.Cut(sc.Text(), "\t")
		if ok {
			ms = append(ms, acpModel{ModelID: id, Name: name})
		}
	}
	es := shapeModels("antigravity", ms, nil)
	want := []string{"gemini-4-carbon", "gemini-4-argon", "gemini-4-hydrogen", "gemini-pro-orchestrator", "gemini-pro-orchestrator-argon",
		"gemini-3.8-flash", "claude-opus-5-5", "claude-sonnet-5-5", "gpt-oss-120b-medium"}
	if !slices.Equal(ids(es), want) {
		t.Fatalf("offered %v", ids(es))
	}
	c := es[0]
	if c.Name != "Gemini 4 Carbon" || !slices.Equal(c.Efforts, []string{"low", "medium", "high"}) || c.wire("high") != "gemini-4-carbon-high" || c.wire("") != "gemini-4-carbon-medium" || c.wire("max") != "gemini-4-carbon-medium" {
		t.Fatalf("carbon %+v", c)
	}
	if h := es[2]; !slices.Equal(h.Efforts, []string{"low", "medium", "high", "xhigh"}) || h.wire("xhigh") != "gemini-4-hydrogen-xhigh" {
		t.Fatalf("hydrogen %+v", h)
	}
	if o := es[8]; o.Name != "GPT-OSS 120B (Medium)" || o.Efforts != nil || o.wire("") != "gpt-oss-120b-medium" {
		t.Fatalf("single level model %+v", o)
	}
}

// cursor-agent's ACP ids (variant strings; format as reported on
// 2026-11-17, list made up to have several levels per model): one model
// per base + non-level parameters.
func TestCursorFoldsVariants(t *testing.T) {
	ms := []acpModel{
		{ModelID: "auto-smart[optimize_for=balanced]", Name: "Auto"},
		{ModelID: "claude-opus-5-5[context=300k,effort=low,fast=false]", Name: "Claude Opus 5.5 Low"},
		{ModelID: "claude-opus-5-5[context=300k,effort=medium,fast=false]", Name: "Claude Opus 5.5"},
		{ModelID: "claude-opus-5-5[context=300k,effort=high,fast=false]", Name: "Claude Opus 5.5 High"},
		{ModelID: "claude-opus-5-5[context=300k,effort=xhigh,fast=false]", Name: "Claude Opus 5.5 Extra High"},
		{ModelID: "claude-opus-5-5[context=300k,effort=medium,fast=true]", Name: "Claude Opus 5.5 Fast"},
		{ModelID: "claude-opus-5-5[context=300k,effort=high,fast=true]", Name: "Claude Opus 5.5 High Fast"},
		{ModelID: "composer-2.5[fast=false]", Name: "Composer 2.5"},
		{ModelID: "gpt-5.2", Name: "GPT-5.2"},
		{ModelID: "gpt-5.2", Name: "GPT-5.2"},
	}
	es := shapeModels("cursor", ms, nil)
	if !slices.Equal(ids(es), []string{"auto-smart", "claude-opus-5-5", "claude-opus-5-5-fast", "composer-2.5", "gpt-5.2"}) {
		t.Fatalf("offered %v", ids(es))
	}
	o := es[1]
	if o.Name != "Claude Opus 5.5" || !slices.Equal(o.Efforts, []string{"low", "medium", "high", "xhigh"}) ||
		o.wire("xhigh") != "claude-opus-5-5[context=300k,effort=xhigh,fast=false]" || o.wire("") != "claude-opus-5-5[context=300k,effort=medium,fast=false]" {
		t.Fatalf("opus %+v", o)
	}
	if f := es[2]; f.Name != "Claude Opus 5.5 Fast" || f.wire("high") != "claude-opus-5-5[context=300k,effort=high,fast=true]" {
		t.Fatalf("opus fast %+v", f)
	}
	if au := es[0]; au.Name != "Auto" || au.wire("") != "auto-smart[optimize_for=balanced]" {
		t.Fatalf("auto %+v", au)
	}
}

func TestClaudeCodeNames(t *testing.T) {
	ms := []acpModel{
		{ModelID: "default", Name: "Default (recommended)"},
		{ModelID: "opus", Name: "Opus"},
		{ModelID: "auto/claude-sonnet-5-5[1m]", Name: "auto/claude-sonnet-5-5[1m]"},
		{ModelID: "alchemypay-openai/gpt-6-astra", Name: ""},
		{ModelID: "opus", Name: "Opus"},
	}
	es := shapeModels(claudeCodeID, ms, nil)
	if !slices.Equal(ids(es), []string{"opus", "auto/claude-sonnet-5-5[1m]", "alchemypay-openai/gpt-6-astra"}) {
		t.Fatalf("offered %v", ids(es))
	}
	if es[1].Name != "claude-sonnet-5-5 [1m] · auto" || es[2].Name != "gpt-6-astra · alchemypay-openai" || es[0].Name != "Opus" {
		t.Fatalf("names %+v", es)
	}
	// other CLIs: as reported
	if es := shapeModels("opencode", ms, nil); len(es) != 3 || es[1].Name != "auto/claude-sonnet-5-5[1m]" {
		t.Fatalf("opencode %+v", es)
	}
}

func TestBridgeVersion(t *testing.T) {
	dir := t.TempDir()
	pkg := dir + "/lib/node_modules/@zed-industries/codex-acp"
	if err := os.MkdirAll(pkg+"/bin", 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(pkg+"/package.json", []byte(`{"name":"@zed-industries/codex-acp","version":"0.8.0"}`), 0o644)
	_ = os.WriteFile(pkg+"/bin/codex-acp.js", []byte("#!/usr/bin/env node\n"), 0o755)
	if err := os.MkdirAll(dir+"/bin", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(pkg+"/bin/codex-acp.js", dir+"/bin/codex-acp"); err != nil {
		t.Fatal(err)
	}
	b := bridgeOf(resolved{AgentConfig: AgentConfig{ID: "codex", Command: []string{dir + "/bin/codex-acp"}}})
	if b == nil || b.Version != "0.8.0" || !b.Outdated || b.UpdateCommand != "npm install -g @zed-industries/codex-acp@latest" {
		t.Fatalf("bridge %+v", b)
	}
	_ = os.WriteFile(pkg+"/package.json", []byte(`{"name":"@zed-industries/codex-acp","version":"0.16.0"}`), 0o644)
	if b := bridgeOf(resolved{AgentConfig: AgentConfig{Command: []string{dir + "/bin/codex-acp"}}}); b == nil || b.Outdated {
		t.Fatalf("current bridge %+v", b)
	}
	_ = os.WriteFile(pkg+"/package.json", []byte(`{"name":"@zed-industries/claude-code-acp","version":"0.12.6"}`), 0o644)
	if b := bridgeOf(resolved{AgentConfig: AgentConfig{Command: []string{dir + "/bin/codex-acp"}}}); b == nil || !b.Outdated || b.ReplacedBy != "@agentclientprotocol/claude-agent-acp" ||
		b.UpdateCommand != "npm install -g @agentclientprotocol/claude-agent-acp@latest" {
		t.Fatalf("renamed bridge %+v", b)
	}
	_ = os.WriteFile(pkg+"/package.json", []byte(`{"name":"left-pad","version":"1.0.0"}`), 0o644)
	if b := bridgeOf(resolved{AgentConfig: AgentConfig{Command: []string{dir + "/bin/codex-acp"}}}); b != nil {
		t.Fatalf("unknown package %+v", b)
	}
	if !versionLess("0.9.5", "0.16.0") || versionLess("0.16.0", "0.16.0") || versionLess("1.0.0-beta", "0.16.0") {
		t.Fatal("versionLess")
	}
}

// Recorded: cursor-agent 2026.09.26's session/new availableModels
// (testdata/cursor-acp-models.json, one variant per model, named by bare
// base id) and `cursor-agent models` (testdata/cursor-models.txt).
func TestCursorRecorded(t *testing.T) {
	var ms []acpModel
	b, err := os.ReadFile("testdata/cursor-acp-models.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &ms); err != nil {
		t.Fatal(err)
	}
	txt, err := os.ReadFile("testdata/cursor-models.txt")
	if err != nil {
		t.Fatal(err)
	}
	cli, err := parseCursorModels(txt)
	if err != nil || len(cli) < 200 || cli[0].ID != "auto" || cli[0].Name != "Auto" {
		t.Fatalf("cursor-agent models %v %+v", err, cli[:1])
	}
	es := shapeModels("cursor", ms, cli)
	if len(es) != len(ms) {
		t.Fatalf("%d models from %d variants", len(es), len(ms))
	}
	by := map[string]modelEntry{}
	for _, e := range es {
		if strings.ContainsAny(e.ID, "[]=") {
			t.Errorf("id not cleaned: %s", e.ID)
		}
		by[e.ID] = e
	}
	for id, want := range map[string][2]string{
		"claude-opus-5-5": {"Claude Opus 5.5 1M", "claude-opus-5-5[context=300k,effort=medium,fast=false]"},
		"composer-2.5":    {"Composer 2.5 Fast", "composer-2.5[fast=true]"},
		"gpt-5.6-sol":     {"GPT-5.6 Sol 1M", "gpt-5.6-sol[context=272k,reasoning=medium,fast=false]"},
		"gemini-3.1-pro":  {"Gemini 3.1 Pro", "gemini-3.1-pro[]"},
		"auto-smart":      {"Auto Balance", "auto-smart[optimize_for=balanced]"},
		"grok-4.6":        {"Grok 4.6 Fast", "grok-4.6[effort=high,fast=true]"},
	} {
		e, ok := by[id]
		if !ok || e.Name != want[0] || e.wire("") != want[1] || e.wire("high") != want[1] || e.Efforts != nil {
			t.Errorf("%s: %+v", id, e)
		}
	}
}
