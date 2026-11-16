package protect

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestPlanAndRelease(t *testing.T) {
	ws := t.TempDir()
	present := filepath.Join(ws, ".claude")
	os.MkdirAll(present, 0o755)
	missing := filepath.Join(ws, ".mcp.json")
	var r Registry

	pl, rel := r.Plan([]string{present, missing, missing, "/elsewhere/.codex"}, []string{ws}, false)
	if rel != nil || !slices.Equal(pl.Present, []string{present}) || !slices.Equal(pl.Missing, []string{missing}) {
		t.Fatal(pl)
	}

	// Two commands hold the placeholder; the sandbox creates the mount point.
	_, rel1 := r.Plan([]string{missing}, []string{ws}, true)
	os.Mkdir(missing, 0o755)
	// While held, the existing mount point still counts as a placeholder,
	// so the second command shares it (and its cleanup duty).
	pl, rel2 := r.Plan([]string{missing}, []string{ws}, true)
	if !slices.Equal(pl.Missing, []string{missing}) || len(pl.Present) != 0 || r.Held(missing) != 2 {
		t.Fatal(pl, r.Held(missing))
	}
	rel1()
	rel1() // idempotent
	if _, err := os.Lstat(missing); err != nil || r.Held(missing) != 1 {
		t.Fatal("placeholder removed while another command uses it", err)
	}
	rel2()
	if _, err := os.Lstat(missing); !os.IsNotExist(err) || r.Held(missing) != 0 {
		t.Fatal("placeholder left behind", err)
	}

	// A placeholder that got real content meanwhile is kept.
	_, rel3 := r.Plan([]string{missing}, []string{ws}, true)
	os.Mkdir(missing, 0o755)
	os.WriteFile(filepath.Join(missing, "x"), []byte("x"), 0o644)
	rel3()
	if _, err := os.Lstat(filepath.Join(missing, "x")); err != nil {
		t.Fatal("non-empty directory removed", err)
	}
	// So is a file put there.
	os.RemoveAll(missing)
	_, rel4 := r.Plan([]string{missing}, []string{ws}, true)
	os.WriteFile(missing, nil, 0o644)
	rel4()
	if _, err := os.Lstat(missing); err != nil {
		t.Fatal("file removed", err)
	}
}

func TestPlanProtectedContainsWritable(t *testing.T) {
	ws := t.TempDir()
	sub := filepath.Join(ws, "sub")
	os.MkdirAll(sub, 0o755)
	pl, _ := (&Registry{}).Plan([]string{ws}, []string{sub}, false)
	if !slices.Equal(pl.Present, []string{ws}) {
		t.Fatal("a protected root that contains a writable one wins", pl)
	}
}
