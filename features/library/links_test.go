package library

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPublishSyncPull(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	ws := t.TempDir()
	os.MkdirAll(filepath.Join(ws, "docs"), 0o755)
	file := filepath.Join(ws, "docs", "plan.md")
	os.WriteFile(file, []byte("v1\n"), 0o644)
	// a document of the same bytes already: publishing still gets its own
	other, _ := s.Add(ctx, AddRequest{Name: "other.md", Data: []byte("v1\n"), Source: SourcePerson})
	dirOf := func(string) (string, bool) { return ws, true }
	v := Viewer{}

	lv, err := s.Publish(ctx, PublishRequest{Agent: "kai", Dir: ws, Path: "docs/plan.md", Session: "s1", Seq: 7})
	if err != nil {
		t.Fatal(err)
	}
	if lv.Item == other.ID || lv.State != SyncSame || lv.Path != "docs/plan.md" || lv.Session != "s1" || lv.Seq != 7 {
		t.Fatalf("%+v", lv)
	}
	// the workspace moves on
	os.WriteFile(file, []byte("v2\n"), 0o644)
	if l, _ := s.LinkOf(v, lv.Item, dirOf); l.State != SyncFileAhead {
		t.Fatalf("want file ahead, got %s", l.State)
	}
	it, err := s.Push(v, lv.Item, dirOf, "")
	if err != nil || it.Versions != 1 {
		t.Fatalf("%v %+v", err, it)
	}
	if b, _ := os.ReadFile(s.File(it)); string(b) != "v2\n" {
		t.Fatalf("library has %q", b)
	}
	// the document moves on (a version restored)
	vs, _ := s.Versions(v, it.ID)
	if _, err := s.Restore(ctx, v, it.ID, vs[0].ID, ""); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.LinkOf(v, lv.Item, dirOf); l.State != SyncDocAhead {
		t.Fatalf("want doc ahead, got %s", l.State)
	}
	if _, err := s.Pull(v, lv.Item, dirOf); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(file); string(b) != "v1\n" {
		t.Fatalf("workspace has %q", b)
	}
	if l, _ := s.LinkOf(v, lv.Item, dirOf); l.State != SyncSame {
		t.Fatalf("want same, got %s", l.State)
	}
	// both move: diverged; publishing again syncs into the same document
	os.WriteFile(file, []byte("mine\n"), 0o644)
	cur, _ := s.Get(v, it.ID)
	s.replaceContent(cur, []byte("theirs\n"), "")
	if l, _ := s.LinkOf(v, lv.Item, dirOf); l.State != SyncDiverged {
		t.Fatalf("want both, got %s", l.State)
	}
	again, err := s.Publish(ctx, PublishRequest{Agent: "kai", Dir: ws, Path: "docs/plan.md"})
	if err != nil || again.Item != lv.Item || again.State != SyncSame {
		t.Fatalf("%v %+v", err, again)
	}
	ls, _ := s.AgentLinks(v, "kai", ws)
	if len(ls) != 1 || ls[0].Name != "plan.md" {
		t.Fatalf("%+v", ls)
	}
	// gone file; deleting the document forgets the link
	os.Remove(file)
	if l, _ := s.LinkOf(v, lv.Item, dirOf); l.State != SyncFileGone {
		t.Fatalf("want gone, got %s", l.State)
	}
	s.Delete(v, lv.Item)
	if ls, _ := s.AgentLinks(v, "kai", ws); len(ls) != 0 {
		t.Fatalf("%+v", ls)
	}
}

func TestPublishStaysInWorkspace(t *testing.T) {
	s := open(t)
	ws := t.TempDir()
	out := t.TempDir()
	os.WriteFile(filepath.Join(out, "secret.txt"), []byte("x"), 0o644)
	os.Symlink(filepath.Join(out, "secret.txt"), filepath.Join(ws, "link.txt"))
	for _, p := range []string{"../secret.txt", "/etc/hosts", "link.txt", ""} {
		if _, err := s.Publish(context.Background(), PublishRequest{Agent: "kai", Dir: ws, Path: p}); err == nil {
			t.Fatalf("%q published", p)
		}
	}
}
