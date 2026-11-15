package console

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBrowseDirs(t *testing.T) {
	home := t.TempDir()
	for _, d := range []string{"code/app", "Docs", ".config"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(home, "notes.txt"), []byte("x"), 0o600)
	_ = os.Symlink(filepath.Join(home, "code"), filepath.Join(home, "link"))

	r, err := browseDirs(home, "")
	if err != nil {
		t.Fatal(err)
	}
	ds := r["dirs"].([]dirEntry)
	var names []string
	for _, d := range ds {
		names = append(names, d.Name)
	}
	if got := len(names); got != 3 || names[0] != "code" || names[1] != "Docs" || names[2] != "link" {
		t.Fatalf("dirs = %v (no files, no hidden, symlinked dirs kept)", names)
	}
	r, _ = browseDirs(home, "~/code")
	if r["path"] != filepath.Join(home, "code") || r["parent"] != home {
		t.Fatalf("~/code: %v", r)
	}
	if _, err := browseDirs(home, "relative"); err == nil {
		t.Fatal("relative path accepted")
	}
	if _, err := browseDirs(home, filepath.Join(home, "missing")); err == nil {
		t.Fatal("missing folder")
	}
}
