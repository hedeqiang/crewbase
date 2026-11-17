package agent

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestCLIConfigPaths(t *testing.T) {
	if CLIConfigPaths("") != nil || CLIConfigPaths("rel") != nil {
		t.Fatal("relative dir must give no paths")
	}
	dir := filepath.Join(t.TempDir(), "a")
	ps := CLIConfigPaths(dir + "/")
	for _, name := range []string{".claude", ".codex", ".gemini", ".cursor", ".opencode", "opencode.json", ".grok", ".mcp.json"} {
		if want := filepath.Join(dir, name); !slices.Contains(ps, want) {
			t.Errorf("missing %s in %v", want, ps)
		}
	}
}

func TestIsCLIConfigPath(t *testing.T) {
	ws := t.TempDir()
	for p, want := range map[string]bool{
		ws + "/.claude":                     true,
		ws + "/.claude/settings.local.json": true,
		ws + "/.CLAUDE/settings.json":       true,
		ws + "/.gemini/settings.json":       true,
		ws + "/sub/../.codex/config.toml":   true,
		ws + "/opencode.json":               true,
		ws + "/.claudex":                    false,
		ws + "/sub/.claude/settings.json":   false,
		ws + "/README.md":                   false,
		"relative/.claude":                  false,
	} {
		if got := IsCLIConfigPath(ws, p); got != want {
			t.Errorf("%s: %v, want %v", p, got, want)
		}
	}
	// a link in the workspace to the config directory counts as it
	os.MkdirAll(filepath.Join(ws, ".cursor"), 0o755)
	if err := os.Symlink(filepath.Join(ws, ".cursor"), filepath.Join(ws, "c")); err != nil {
		t.Fatal(err)
	}
	if !IsCLIConfigPath(ws, filepath.Join(ws, "c", "cli.json")) {
		t.Error("link to .cursor not detected")
	}
}
