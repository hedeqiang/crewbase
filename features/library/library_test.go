package library

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func open(t *testing.T) *Service {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "library"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// indexAll extracts the text of everything added (the worker does this in
// the plugin).
func indexAll(s *Service) { s.catchUp(context.Background()) }

func TestAddSearchRead(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	up, err := s.Add(ctx, AddRequest{Team: "", Name: "季度报告.md", Data: []byte("# 第三季度\n\n营收增长了 12%，主要来自海外市场。\n"), Source: SourceUpload, Agent: "kai"})
	if err != nil {
		t.Fatal(err)
	}
	if up.Folder != FolderInbox || up.Team != "default" || up.Name != "季度报告.md" {
		t.Fatalf("%+v", up)
	}
	if _, err := os.Stat(s.File(up)); err != nil {
		t.Fatal("file not in the library folder")
	}
	doc, _ := s.Add(ctx, AddRequest{Name: "plan.md", Title: "Launch plan", Data: []byte("We launch in Berlin first, then Tokyo.\n"), Source: SourceAgent, Agent: "kai", Tags: []string{"launch"}})
	img, _ := s.Add(ctx, AddRequest{Folder: FolderMedia, Name: "cat.png", Data: []byte("\x89PNG\r\n\x1a\nfake"), Source: SourceGenerated, Agent: "kai", Note: "a cat on a red sofa"})
	secret, _ := s.Add(ctx, AddRequest{Name: "notes.md", Data: []byte("only for me: the budget is tight\n"), Source: SourceAgent, Agent: "kai", Private: true})
	indexAll(s)

	team := Viewer{Team: "default", Agent: "mia"}
	for q, want := range map[string]int64{
		"海外市场":   up.ID,  // CJK, trigram
		"营收":     up.ID,  // short CJK, substring fallback
		"Tokyo":  doc.ID, // English words
		"launch": doc.ID, // tag and title
		"sofa":   img.ID, // an image by its prompt
	} {
		hits, err := s.Search(ctx, team, q, 5)
		if err != nil || len(hits) == 0 || hits[0].ID != want {
			t.Errorf("%q: %v %+v", q, err, hits)
		}
	}
	// private: only its agent (and people) see it
	if hits, _ := s.Search(ctx, team, "budget", 5); len(hits) != 0 {
		t.Fatalf("another agent sees a private item: %+v", hits)
	}
	if hits, _ := s.Search(ctx, Viewer{Team: "default", Agent: "kai"}, "budget", 5); len(hits) != 1 || hits[0].ID != secret.ID {
		t.Fatalf("its agent: %+v", hits)
	}
	if hits, _ := s.Search(ctx, Viewer{Team: "default"}, "budget", 5); len(hits) != 1 {
		t.Fatal("a person should see it")
	}
	// another team sees nothing
	if hits, _ := s.Search(ctx, Viewer{Team: "shop", Agent: "x"}, "Tokyo", 5); len(hits) != 0 {
		t.Fatalf("other team: %+v", hits)
	}
	// text of a document
	if !strings.Contains(s.Text(up.ID), "海外市场") {
		t.Fatal("no text")
	}
	if got, _ := s.Get(team, up.ID); !got.Text {
		t.Fatal("text flag")
	}
	if b := s.Body(up.ID); strings.Contains(b, "季度报告.md") || !strings.HasPrefix(b, "# 第三季度") {
		t.Fatalf("body %q", b)
	}
	// a file without readable text has no body (not its name)
	if b := s.Body(img.ID); b != "" {
		t.Fatalf("image body %q", b)
	}
	if _, err := s.Add(ctx, AddRequest{Name: "blob.bin", Data: []byte{0, 1, 2, 3}, Source: SourceUpload}); err != nil {
		t.Fatal(err)
	}
}

func TestDedupeNamesAndVersions(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	a, _ := s.Add(ctx, AddRequest{Name: "a.txt", Data: []byte("one"), Source: SourceUpload})
	// the same bytes again (another name): the same item, tags merged
	b, _ := s.Add(ctx, AddRequest{Name: "copy.txt", Data: []byte("one"), Source: SourceUpload, Tags: []string{"x"}})
	if a.ID != b.ID || len(b.Tags) != 1 {
		t.Fatalf("dedupe %+v %+v", a, b)
	}
	// other bytes, same name: a free name
	c, _ := s.Add(ctx, AddRequest{Name: "a.txt", Data: []byte("two"), Source: SourceUpload})
	if c.Name != "a (2).txt" {
		t.Fatalf("name %q", c.Name)
	}
	// replace keeps the earlier content as a version
	d1, _ := s.Add(ctx, AddRequest{Name: "plan.md", Data: []byte("v1"), Source: SourceAgent, Agent: "kai", Replace: true})
	d2, _ := s.Add(ctx, AddRequest{Name: "plan.md", Data: []byte("v2"), Source: SourceAgent, Agent: "mia", Replace: true})
	if d1.ID != d2.ID || d2.Versions != 1 {
		t.Fatalf("replace %+v", d2)
	}
	vs, _ := s.Versions(Viewer{}, d2.ID)
	if len(vs) != 1 || vs[0].Agent != "kai" {
		t.Fatalf("versions %+v", vs)
	}
	if b, _ := s.VersionData(Viewer{}, d2.ID, vs[0].ID); string(b) != "v1" {
		t.Fatalf("version data %q", b)
	}
	r, err := s.Restore(ctx, Viewer{}, d2.ID, vs[0].ID, "")
	if err != nil || r.Versions != 2 {
		t.Fatalf("restore %+v %v", r, err)
	}
	if b, _ := os.ReadFile(s.File(r)); string(b) != "v1" {
		t.Fatalf("restored %q", b)
	}
	// delete removes the file
	if err := s.Delete(Viewer{}, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.File(r)); err == nil {
		t.Fatal("file left")
	}
	if _, err := s.Get(Viewer{}, r.ID); err == nil {
		t.Fatal("item left")
	}
}

func TestFolders(t *testing.T) {
	for _, c := range []struct{ name, mime, source, want string }{
		{"x.pdf", "application/pdf", SourceUpload, FolderInbox},
		{"x.png", "image/png", SourceGenerated, FolderMedia},
		{"x.html", "text/html", SourceAgent, FolderPages},
		{"x.md", "text/markdown", SourceAgent, FolderDocs},
	} {
		if got := FolderFor(c.name, c.mime, c.source); got != c.want {
			t.Errorf("%s: %s", c.name, got)
		}
	}
	if safeName("../../etc/passwd") != "-..-etc-passwd" && strings.Contains(safeName("../../etc/passwd"), "/") {
		t.Fatal("path in a name")
	}
}

func TestHTMLText(t *testing.T) {
	got := htmlText(`<html><head><style>p{color:red}</style><script>alert(1)</script></head><body><h1>Pelican &amp; Co</h1><p>海外市场</p><table><tr><td>欧洲</td><td>1</td></tr></table></body></html>`)
	if got != "Pelican & Co\n海外市场\n欧洲 1" {
		t.Fatalf("%q", got)
	}
}

// A save that replaces by name only replaces what the saver may see, kept
// private (or not) as asked.
func TestReplaceRespectsPrivacy(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	mia, err := s.Add(ctx, AddRequest{Team: "default", Name: "plan.md", Data: []byte("A secret salary plan"), Source: SourceAgent, Agent: "mia", Private: true, Replace: true})
	if err != nil {
		t.Fatal(err)
	}
	kai, err := s.Add(ctx, AddRequest{Team: "default", Name: "plan.md", Data: []byte("B public notes"), Source: SourceAgent, Agent: "kai", Replace: true})
	if err != nil {
		t.Fatal(err)
	}
	if kai.ID == mia.ID {
		t.Fatal("kai's save overwrote mia's private document")
	}
	if got, err := s.Get(Viewer{Team: "default", Agent: "mia"}, mia.ID); err != nil || got.Agent != "mia" || got.Versions != 0 {
		t.Fatalf("mia's document changed: %+v %v", got, err)
	}
	// a private save over a shared document does not stay shared
	pub, _ := s.Add(ctx, AddRequest{Team: "default", Name: "pub.md", Data: []byte("x"), Source: SourceAgent, Agent: "kai", Replace: true})
	p, err := s.Add(ctx, AddRequest{Team: "default", Name: "pub.md", Data: []byte("my private y"), Source: SourceAgent, Agent: "kai", Private: true, Replace: true})
	if err != nil || !p.Private || p.ID == pub.ID {
		t.Fatalf("private save: %+v %v", p, err)
	}
	// replacing one's own document of the same privacy still replaces it
	again, err := s.Add(ctx, AddRequest{Team: "default", Name: "plan.md", Data: []byte("B public notes v2"), Source: SourceAgent, Agent: "kai", Replace: true})
	if err != nil || again.ID != kai.ID || again.Versions != 1 {
		t.Fatalf("own replace: %+v %v", again, err)
	}
	// the same with a Chinese name: saving again updates kai's copy
	s.Add(ctx, AddRequest{Team: "default", Name: "薪资.md", Data: []byte("secret"), Source: SourceAgent, Agent: "mia", Private: true, Replace: true})
	c1, _ := s.Add(ctx, AddRequest{Team: "default", Name: "薪资.md", Data: []byte("v1"), Source: SourceAgent, Agent: "kai", Replace: true})
	c2, err := s.Add(ctx, AddRequest{Team: "default", Name: "薪资.md", Data: []byte("v2"), Source: SourceAgent, Agent: "kai", Replace: true})
	if err != nil || c2.ID != c1.ID {
		t.Fatalf("saving again made another copy: %v %v (%s, %s)", c1.ID, c2.ID, c1.Name, c2.Name)
	}
}
