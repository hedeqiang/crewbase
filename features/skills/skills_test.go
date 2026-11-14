package skills_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/skills"
	"github.com/hedeqiang/crewbase/kernel"
)

func write(t *testing.T, path, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogRankAndLoad(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "skills")
	write(t, filepath.Join(shared, "review", "SKILL.md"), "---\nname: code-review\ndescription: Review a diff for bugs\n---\n# Review\nStep 1: read the diff.\n")
	write(t, filepath.Join(shared, "review", "checklist.md"), "- nil checks")
	write(t, filepath.Join(shared, "brief", "SKILL.md"), "# Weekly brief\nSummarise the week for the team.\n")

	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, nil}, {prompt.Plugin, nil}, {tools.Plugin, nil},
		{agent.Plugin, map[string]any{"root": root}},
		{skills.Plugin, map[string]any{"dirs": []string{shared}}},
	} {
		h.Load(x.s, kernel.LoadOptions{Config: x.cfg})
	}
	r := h.Root()
	a, _ := kernel.Use[*agent.Registry](r, agent.ServiceKey).Create(agent.Spec{Name: "Kai"})
	// workspace skill shadows the shared one with the same name
	write(t, filepath.Join(a.Workspace(), "skills", "my-review", "SKILL.md"), "---\nname: code-review\ndescription: Kai's own review style\n---\nBe strict.")

	sys := kernel.Use[*prompt.Assembler](r, prompt.ServiceKey).Assemble(a.ID())
	if !strings.Contains(sys, "code-review: Kai's own review style") || !strings.Contains(sys, "brief: Summarise the week") {
		t.Fatalf("catalog:\n%s", sys)
	}
	if strings.Contains(sys, "Be strict") || strings.Contains(sys, "Step 1") {
		t.Fatal("full skill text must not be resident in the prompt")
	}
	other := kernel.Use[*prompt.Assembler](r, prompt.ServiceKey).Assemble("")
	if !strings.Contains(other, "Review a diff for bugs") {
		t.Fatal("global view sees shared skill")
	}

	reg := kernel.Use[*tools.Registry](r, tools.ServiceKey)
	ss, _ := a.Session()
	args, _ := json.Marshal(map[string]string{"name": "brief"})
	exe := reg.Run(context.Background(), r, tools.Call{ID: "c1", Name: "skill_load", Args: args, Agent: a.ID(), Session: ss})
	if !strings.Contains(exe.Result.Content, "Summarise the week") {
		t.Fatal(exe.Result)
	}
	args, _ = json.Marshal(map[string]string{"name": "code-review"})
	exe = reg.Run(context.Background(), r, tools.Call{ID: "c2", Name: "skill_load", Args: args, Agent: "", Session: ss})
	if !strings.Contains(exe.Result.Content, "Step 1") || !strings.Contains(exe.Result.Content, "checklist.md") {
		t.Fatal(exe.Result)
	}
}
