// Package skills loads skills on demand (C-4). A skill is a folder with a
// SKILL.md (YAML front matter: name, description). Agents only see the
// catalog (name + description) in their system prompt; the full text is
// loaded with the skill_load tool when needed, so skills take no resident
// context. Sources by rank (highest wins on name clash): the agent's
// workspace `skills/`, then configured directories in order.
package skills

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
	"gopkg.in/yaml.v3"
)

const ServiceKey = "skills"

// Skill is one discovered skill.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Dir         string `json:"dir"`
	Source      string `json:"source"` // workspace | <configured dir>
}

// Changed notifies that the catalog changed (no delta).
var Changed = kernel.NewEmit[struct{}]("skills/changed")

// Service is the `skills` service.
type Service struct {
	c      *kernel.Context
	dirs   []string
	agents *agent.Registry

	mu    sync.Mutex
	cache map[string]cached // dir → scan result
}

type cached struct {
	stamp  string
	skills []Skill
}

// stamp is a cheap change detector: names + mtimes of SKILL.md files.
func stamp(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if fi, err := os.Stat(filepath.Join(dir, e.Name(), "SKILL.md")); err == nil {
			fmt.Fprintf(&b, "%s:%d;", e.Name(), fi.ModTime().UnixNano())
		}
	}
	return b.String()
}

func (s *Service) scan(dir, source string) []Skill {
	st := stamp(dir)
	s.mu.Lock()
	if c, ok := s.cache[dir]; ok && c.stamp == st {
		s.mu.Unlock()
		return c.skills
	}
	s.mu.Unlock()
	var out []Skill
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sd := filepath.Join(dir, e.Name())
		meta, _, err := parse(filepath.Join(sd, "SKILL.md"))
		if err != nil {
			continue
		}
		if meta.Name == "" {
			meta.Name = e.Name()
		}
		out = append(out, Skill{Name: meta.Name, Description: meta.Description, Dir: sd, Source: source})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	s.mu.Lock()
	prev, had := s.cache[dir]
	s.cache[dir] = cached{stamp: st, skills: out}
	s.mu.Unlock()
	if had && prev.stamp != st {
		Changed.Emit(s.c, struct{}{})
	}
	return out
}

type frontMatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// parse reads SKILL.md: optional `---` YAML front matter, then the body.
func parse(path string) (frontMatter, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return frontMatter{}, "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 4<<20)
	var fm frontMatter
	var head, body []string
	state := 0 // 0 start, 1 in front matter, 2 body
	for sc.Scan() {
		line := sc.Text()
		switch {
		case state == 0 && strings.TrimSpace(line) == "---":
			state = 1
		case state == 0:
			state = 2
			body = append(body, line)
		case state == 1 && strings.TrimSpace(line) == "---":
			state = 2
		case state == 1:
			head = append(head, line)
		default:
			body = append(body, line)
		}
	}
	if len(head) > 0 {
		if err := yaml.Unmarshal([]byte(strings.Join(head, "\n")), &fm); err != nil {
			return fm, "", fmt.Errorf("%s: front matter: %w", path, err)
		}
	}
	if fm.Description == "" {
		// fall back to the first non-heading line
		for _, l := range body {
			if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "#") {
				fm.Description = t
				break
			}
		}
	}
	return fm, strings.TrimSpace(strings.Join(body, "\n")), sc.Err()
}

// List returns the skills visible to agent ("" = global only), ranked.
func (s *Service) List(agentID string) []Skill {
	var sources [][]Skill
	if a, ok := s.agents.Get(agentID); ok {
		sources = append(sources, s.scan(filepath.Join(a.Workspace(), "skills"), "workspace"))
	}
	for _, d := range s.dirs {
		sources = append(sources, s.scan(d, d))
	}
	seen := map[string]bool{}
	var out []Skill
	for _, src := range sources {
		for _, sk := range src {
			if !seen[sk.Name] {
				seen[sk.Name] = true
				out = append(out, sk)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Load returns a skill's full text and its other files.
func (s *Service) Load(agentID, name string) (Skill, string, []string, error) {
	for _, sk := range s.List(agentID) {
		if sk.Name != name {
			continue
		}
		_, body, err := parse(filepath.Join(sk.Dir, "SKILL.md"))
		if err != nil {
			return sk, "", nil, err
		}
		var files []string
		_ = filepath.WalkDir(sk.Dir, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || len(files) >= 100 {
				return nil
			}
			if rel, _ := filepath.Rel(sk.Dir, p); rel != "SKILL.md" {
				files = append(files, rel)
			}
			return nil
		})
		return sk, body, files, nil
	}
	return Skill{}, "", nil, fmt.Errorf("unknown skill %q", name)
}

func catalog(list []Skill) string {
	if len(list) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Skills available (load the full instructions with skill_load before using one):\n")
	for _, sk := range list {
		fmt.Fprintf(&b, "- %s: %s\n", sk.Name, sk.Description)
	}
	return b.String()
}

// Config of the skills plugin.
type Config struct {
	Dirs []string `json:"dirs"` // e.g. ["${root}/skills"]
}

// Plugin provides on-demand skills.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/skills",
	Inject:      []string{agent.ServiceKey, prompt.ServiceKey, tools.ServiceKey},
	Description: "Skills (SKILL.md folders) loaded on demand; only the catalog is resident.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		for _, d := range cfg.Dirs {
			_ = os.MkdirAll(d, 0o755)
		}
		s := &Service{c: c, dirs: cfg.Dirs, agents: kernel.Use[*agent.Registry](c, agent.ServiceKey), cache: map[string]cached{}}
		kernel.Use[*prompt.Assembler](c, prompt.ServiceKey).Register(c, &prompt.Fragment{
			Name: "skills-catalog", Order: 30, Render: func(id string) string { return catalog(s.List(id)) },
		})
		kernel.Use[*tools.Registry](c, tools.ServiceKey).Register(c, &tools.Tool{
			Name:        "skill_load",
			Description: "Load the full instructions of a skill from the skills catalog.",
			Params:      tools.Schema(map[string]any{"name": tools.Prop("string", "skill name from the catalog")}, "name"),
			Category:    tools.CategoryRead,
			Execute: func(_ context.Context, call tools.Call) (tools.Result, error) {
				sk, body, files, err := s.Load(call.Agent, call.String("name"))
				if err != nil {
					return tools.Errorf("%v", err), nil
				}
				out := fmt.Sprintf("# Skill: %s\nFolder: %s\n\n%s", sk.Name, sk.Dir, body)
				if len(files) > 0 {
					out += "\n\nOther files in this skill (read them with read_file if needed):\n- " + strings.Join(files, "\n- ")
				}
				return tools.Result{Content: out, Data: sk}, nil
			},
		})
		// Notify changes without a watcher dependency: a light poll.
		c.Go(func(ctx context.Context) {
			t := time.NewTicker(10 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					for _, d := range s.dirs {
						s.scan(d, d)
					}
				}
			}
		})
		return c.Provide(ServiceKey, s)
	},
})
