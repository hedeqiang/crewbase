package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSkillVersion(t *testing.T) {
	dir := t.TempDir()
	for name, head := range map[string]string{
		"top":  "---\nname: top\nversion: 1.2.0\n---\nx",
		"meta": "---\nname: meta\nmetadata:\n  version: \"2.0\"\n---\nx",
		"none": "---\nname: none\n---\nx",
	} {
		_ = os.MkdirAll(filepath.Join(dir, name), 0o755)
		_ = os.WriteFile(filepath.Join(dir, name, "SKILL.md"), []byte(head), 0o644)
	}
	got := map[string]string{}
	for _, sk := range (&Service{cache: map[string]cached{}}).scan(dir, "test") {
		got[sk.Name] = sk.Version
	}
	if got["top"] != "1.2.0" || got["meta"] != "2.0" || got["none"] != "" {
		t.Fatalf("%v", got)
	}
}
