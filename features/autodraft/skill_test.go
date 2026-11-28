package autodraft

import (
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

func TestSkillName(t *testing.T) {
	for in, want := range map[string]string{
		"Release Notes":         "release-notes",
		"  ci_failure  triage ": "ci-failure-triage",
		"日报":                    "",
		"weekly-report-v2":      "weekly-report-v2",
		"--x--":                 "x",
		strings.Repeat("a", 60): strings.Repeat("a", 40),
	} {
		if got := skillName(in); got != want {
			t.Errorf("skillName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseSkill(t *testing.T) {
	a, err := parseSkill("```json\n{\"name\":\"x\",\"description\":\"做 X\",\"body\":\"## 步骤\\n1. a\\n```sh\\nls\\n```\"}\n```")
	if err != nil || a.Description != "做 X" || !strings.Contains(a.Body, "```sh") {
		t.Fatalf("%+v %v", a, err)
	}
	if _, err := parseSkill(`{"name":"x","description":"","body":""}`); err == nil {
		t.Fatal("an empty skill is refused")
	}
}

// What skillFile writes installs and reads back as the skill it describes,
// a description with a colon and quotes included.
func TestSkillFileInstalls(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "skills")
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
	svc := kernel.Use[*skills.Service](h.Root(), skills.ServiceKey)
	desc := `整理发布说明: 读 "git log" 并按类型分组`
	content := skillFile("release-notes", desc, "## 步骤\n1. git log v1..v2\n")
	sk, err := svc.Install("", "release-notes", map[string][]byte{"SKILL.md": []byte(content)})
	if err != nil {
		t.Fatal(err)
	}
	if sk.Name != "release-notes" || sk.Description != desc {
		t.Fatalf("installed %+v", sk)
	}
	b, _ := os.ReadFile(filepath.Join(shared, "release-notes", "SKILL.md"))
	if !strings.Contains(string(b), "git log v1..v2") {
		t.Fatal(string(b))
	}
	if _, err := svc.Install("", "release-notes", map[string][]byte{"SKILL.md": []byte(content)}); err == nil {
		t.Fatal("a skill of the same name is not overwritten")
	}
}
