package skills_test

import (
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

func TestResolvePicksAndSlash(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "skills")
	write(t, filepath.Join(shared, "notes", "SKILL.md"), "---\nname: release-notes\ndescription: Write release notes\n---\nGroup changes by area.\n")
	write(t, filepath.Join(shared, "notes", "template.md"), "# vX")
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
	s := kernel.Use[*skills.Service](r, skills.ServiceKey)

	// a pick from the / menu gets the instructions and the skill's files
	in := agent.Input{Text: "for 0.9.7", Use: []session.Invoke{{Kind: session.InvokeSkill, Name: "release-notes"}, {Kind: session.InvokeTools, Name: "github"}}}
	if err := s.Resolve(&in, a.ID()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(in.Use[0].Text, "Group changes by area.") || !strings.Contains(in.Use[0].Text, "template.md") || in.Use[1].Text != "" || in.Text != "for 0.9.7" {
		t.Fatalf("pick: %+v", in)
	}

	// "/name" typed where there is no menu (IM) becomes the same pick
	in = agent.Input{Text: "/release-notes for 0.9.7"}
	if err := s.Resolve(&in, a.ID()); err != nil {
		t.Fatal(err)
	}
	if len(in.Use) != 1 || in.Use[0].Name != "release-notes" || in.Text != "for 0.9.7" {
		t.Fatalf("slash: %+v", in)
	}

	// text that only looks like one is left alone
	for _, txt := range []string{"/tmp/x is full", "/unknown do it", "see /release-notes"} {
		in = agent.Input{Text: txt}
		if err := s.Resolve(&in, a.ID()); err != nil || len(in.Use) != 0 || in.Text != txt {
			t.Fatalf("%q: %+v %v", txt, in, err)
		}
	}

	// a picked skill the agent cannot see is refused, by name
	in = agent.Input{Use: []session.Invoke{{Kind: session.InvokeSkill, Name: "nope"}}}
	if err := s.Resolve(&in, a.ID()); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("unknown skill: %v", err)
	}
}
