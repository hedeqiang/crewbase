package agent_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/core/agent"
)

// Settings › 默认工作区: new agents' folders go to the chosen folder; agents
// made before stay where they are, and both survive a restart.
func TestWorkspaceBase(t *testing.T) {
	root := t.TempDir()
	reg := startAgents(t, root)
	old, err := reg.Create(agent.Spec{Name: "Old"})
	if err != nil {
		t.Fatal(err)
	}
	if dir, def := reg.WorkspaceBase(); !def || dir != filepath.Join(root, "workspaces") {
		t.Fatalf("default %s %v", dir, def)
	}

	// refused: relative, inside the data folder, the data folder's parent
	for _, bad := range []string{"work", filepath.Join(root, "inside"), filepath.Dir(root), "/"} {
		if _, err := reg.SetWorkspaceBase(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}

	base := filepath.Join(t.TempDir(), "my work") // created when missing
	got, err := reg.SetWorkspaceBase(base)
	if err != nil || got != base {
		t.Fatalf("%s %v", got, err)
	}
	// a folder of the person's already there under the agent's name is not adopted
	if err := os.MkdirAll(filepath.Join(base, "kai"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(base, "kai", "notes.txt"), []byte("mine"), 0o644)
	a, err := reg.Create(agent.Spec{Name: "Kai"})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID() == "kai" || !strings.HasPrefix(a.Workspace(), base+string(filepath.Separator)) {
		t.Fatalf("id %s at %s", a.ID(), a.Workspace())
	}
	if !exists(filepath.Join(a.Home(), "INSTRUCTIONS.md")) {
		t.Fatal("instructions not in the new folder")
	}
	if !strings.HasPrefix(old.Workspace(), filepath.Join(root, "workspaces")) {
		t.Fatalf("old agent moved: %s", old.Workspace())
	}

	// a restart keeps the setting and finds both agents
	reg2 := startAgents(t, root)
	if dir, def := reg2.WorkspaceBase(); def || dir != base {
		t.Fatalf("after restart %s %v", dir, def)
	}
	a2, ok := reg2.Get(a.ID())
	if !ok || a2.Workspace() != a.Workspace() {
		t.Fatalf("new agent lost on restart")
	}
	if _, ok := reg2.Get(old.ID()); !ok {
		t.Fatal("old agent lost on restart")
	}

	// deleting archives it next to its folder, not in the data folder
	if err := reg2.Delete(a.ID()); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(base)
	archived := false
	for _, e := range ents {
		archived = archived || strings.HasPrefix(e.Name(), ".deleted-"+a.ID()+"-")
	}
	if !archived {
		t.Fatal("not archived in its base")
	}

	// back to the default
	if got, err := reg2.SetWorkspaceBase(""); err != nil || got != filepath.Join(root, "workspaces") {
		t.Fatalf("reset %s %v", got, err)
	}
	b, err := reg2.Create(agent.Spec{Name: "Back"})
	if err != nil || !strings.HasPrefix(b.Workspace(), filepath.Join(root, "workspaces")) {
		t.Fatalf("%v %s", err, b.Workspace())
	}
}
