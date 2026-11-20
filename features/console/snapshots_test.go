package console

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTurnSnapshots(t *testing.T) {
	ws, root := t.TempDir(), t.TempDir()
	s := &snapshots{root: root}
	write := func(rel, text string) {
		p := filepath.Join(ws, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("keep.txt", "same\n")
	write("edit.txt", "one\ntwo\n")
	write("gone.txt", "bye\n")
	write("node_modules/x.js", "skipped\n")
	write("MEMORY.md", "# Memory\n")
	write("browser/Default/Cookies", "profile")

	if err := s.take("a1", "s1", "t1", ws); err != nil {
		t.Fatal(err)
	}
	// the turn: edit one, delete one, add one (as bash would: no file tools)
	time.Sleep(10 * time.Millisecond)
	write("edit.txt", "one\nTWO\nthree\n")
	_ = os.Remove(filepath.Join(ws, "gone.txt"))
	write("sub/new.txt", "hello\n")
	write("node_modules/x.js", "changed but skipped\n")
	write("MEMORY.md", "# Memory\n\nlearned\n")
	write("browser/Default/Cookies", "profile changed")
	if err := s.finish("a1", "t1"); err != nil {
		t.Fatal(err)
	}

	tc, err := s.turnChanges("a1", "t1")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range tc.Files {
		got[f.Path] = f.Status
	}
	want := map[string]string{"edit.txt": StatusModified, "gone.txt": StatusDeleted, "sub/new.txt": StatusAdded}
	if len(got) != len(want) {
		t.Fatalf("files %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: %q, want %q (%v)", k, got[k], v, got)
		}
	}
	if !tc.Restorable || tc.Added != 3 || tc.Removed != 2 {
		t.Fatalf("%+v", tc)
	}

	// a later turn changes the file again: the first turn's diff is unchanged
	if err := s.take("a1", "s1", "t2", ws); err != nil {
		t.Fatal(err)
	}
	write("edit.txt", "rewritten\n")
	_ = s.finish("a1", "t2")
	d, err := s.turnDiff("a1", "t1", "edit.txt")
	if err != nil || d.Added != 2 || d.Removed != 1 {
		t.Fatalf("diff %+v %v", d, err)
	}
	if tc, _ := s.turnChanges("a1", "t1"); len(tc.Later) != 1 || tc.Later[0] != "t2" {
		t.Fatalf("later %v", tc.Later)
	}
	if sums := s.summaries("a1", "s1"); len(sums) != 2 || sums[0].Turn != "t2" {
		t.Fatalf("summaries %+v", sums)
	}

	// restore to before the first turn
	sn, _ := s.get("a1", "t1")
	if _, err := s.restore("a1", sn); err != nil {
		t.Fatal(err)
	}
	// both turns are marked undone (the second's change to edit.txt is gone too)
	for _, x := range s.summaries("a1", "s1") {
		if !x.Undone {
			t.Fatalf("%s not marked undone", x.Turn)
		}
	}
	for rel, text := range map[string]string{"keep.txt": "same\n", "edit.txt": "one\ntwo\n", "gone.txt": "bye\n"} {
		if b, _ := os.ReadFile(filepath.Join(ws, rel)); string(b) != text {
			t.Fatalf("%s = %q", rel, b)
		}
	}
	if _, err := os.Stat(filepath.Join(ws, "sub/new.txt")); !os.IsNotExist(err) {
		t.Fatal("added file still there")
	}
	if b, _ := os.ReadFile(filepath.Join(ws, "node_modules/x.js")); string(b) != "changed but skipped\n" {
		t.Fatal("touched a skipped folder")
	}
}
