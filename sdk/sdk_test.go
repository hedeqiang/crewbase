package sdk

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScaffoldAndMaterialize(t *testing.T) {
	for _, lang := range []string{"ts", "py", "go"} {
		dir := filepath.Join(t.TempDir(), "p")
		files, err := Scaffold("my-plugin", lang, dir)
		if err != nil || len(files) < 2 {
			t.Fatalf("%s: %v %v", lang, files, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "crewbase-plugin.toml")); err != nil {
			t.Fatal(err)
		}
		if _, err := Scaffold("my-plugin", lang, dir); err == nil {
			t.Fatal("scaffold into non-empty dir")
		}
	}
	if _, err := Scaffold("Bad Name", "ts", t.TempDir()); err == nil {
		t.Fatal("bad name accepted")
	}
	dir := t.TempDir()
	if err := MaterializeNode(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "node_modules/@crewbase/plugin-sdk/index.d.ts")); err != nil {
		t.Fatal(err)
	}
	pp, err := MaterializePython(filepath.Join(dir, "py"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(pp, "crewbase_plugin", "__init__.py")); err != nil {
		t.Fatal(err)
	}
}
