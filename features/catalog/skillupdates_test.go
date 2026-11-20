package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/hedeqiang/crewbase/features/skills"
	"github.com/hedeqiang/crewbase/kernel"
)

func TestSkillUpdates(t *testing.T) {
	var body atomic.Value
	body.Store(tarball(t, map[string]string{
		"skills/pdf/SKILL.md":        "---\nname: pdf\ndescription: Work with PDF files\n---\nUse pypdf.",
		"skills/pdf/scripts/fill.py": "print('fill')",
	}))
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body.Load().([]byte))
	}))
	defer gh.Close()
	old := githubWeb
	githubWeb = gh.URL
	t.Cleanup(func() { githubWeb = old })

	e := newEnv(t)
	s := kernelService(t, e)
	ctx := context.Background()
	got, err := s.Apply(ctx, ApplyRequest{Kind: "skill", Source: "https://github.com/acme/skills", Skills: []string{"skills/pdf"}})
	if err != nil {
		t.Fatal(err)
	}
	dir := got.Skills[0].Dir
	if _, err := os.Stat(filepath.Join(dir, ".crewbase-origin.json")); err != nil {
		t.Fatal("no origin recorded")
	}
	// unchanged: nothing offered
	if u, err := s.SkillUpdates(ctx); err != nil || len(u) != 0 {
		t.Fatalf("unchanged: %+v %v", u, err)
	}
	// the repository changed: offered, then updated in place
	body.Store(tarball(t, map[string]string{
		"skills/pdf/SKILL.md":        "---\nname: pdf\ndescription: Work with PDF files\n---\nUse pypdf, or pdfplumber for tables.",
		"skills/pdf/scripts/fill.py": "print('fill v2')",
	}))
	u, err := s.SkillUpdates(ctx)
	if err != nil || len(u) != 1 || u[0].Title != "pdf" || u[0].Source != "https://github.com/acme/skills" {
		t.Fatalf("changed: %+v %v", u, err)
	}
	if _, err := s.UpdateSkill(ctx, u[0].Agent, u[0].Name); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "scripts", "fill.py")); string(b) != "print('fill v2')" {
		t.Fatalf("not updated: %q", b)
	}
	if u, err := s.SkillUpdates(ctx); err != nil || len(u) != 0 {
		t.Fatalf("after update: %+v %v", u, err)
	}
	// the origin file never shows among the files an agent sees
	_, _, files, err := kernel.Use[*skills.Service](e.h.Root(), skills.ServiceKey).Load("", "pdf")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f == ".crewbase-origin.json" {
			t.Fatal("origin file shown to agents")
		}
	}
}
