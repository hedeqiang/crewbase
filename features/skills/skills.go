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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
	"gopkg.in/yaml.v3"
)

const ServiceKey = "skills"

// Skill is one discovered skill.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version,omitempty"`
	Dir         string `json:"dir"`
	Source      string `json:"source"` // workspace | <configured dir>
	// Agent is the agent whose workspace holds the skill (All only).
	Agent string `json:"agent,omitempty"`
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
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") { // .install-*, .old-* while being swapped
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
		out = append(out, Skill{Name: meta.Name, Description: meta.Description, Version: meta.version(), Dir: sd, Source: source})
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
	// Version: optional; also accepted under metadata (as Anthropic's
	// skills put it).
	Version  string            `yaml:"version"`
	Metadata map[string]string `yaml:"metadata"`
}

func (f frontMatter) version() string {
	if v := strings.TrimSpace(f.Version); v != "" {
		return v
	}
	return strings.TrimSpace(f.Metadata["version"])
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
			if rel, _ := filepath.Rel(sk.Dir, p); rel != "SKILL.md" && rel != OriginFile {
				files = append(files, rel)
			}
			return nil
		})
		return sk, body, files, nil
	}
	return Skill{}, "", nil, fmt.Errorf("unknown skill %q", name)
}

// All returns every installed skill: the configured directories (shared by
// all agents) and each agent's workspace skills (Agent set). Unlike List it
// does not resolve name clashes, so a settings page can show both copies.
func (s *Service) All() []Skill {
	var out []Skill
	for _, d := range s.dirs {
		out = append(out, s.scan(d, d)...)
	}
	for _, a := range s.agents.List() {
		for _, sk := range s.scan(filepath.Join(a.Workspace(), "skills"), "workspace") {
			sk.Agent = a.ID()
			out = append(out, sk)
		}
	}
	return out
}

// Errors of Install and Remove.
var (
	ErrExists   = errors.New("skill already installed")
	ErrNotFound = errors.New("skill not found")
)

// installDir is where Install writes and Remove deletes: the agent's
// workspace skills/ for an agent, else the first configured directory
// (${root}/skills). Other configured directories are the person's own and
// are never written to.
func (s *Service) installDir(agentID string) (dir, source string, err error) {
	if agentID != "" {
		a, ok := s.agents.Get(agentID)
		if !ok {
			return "", "", fmt.Errorf("unknown agent %q", agentID)
		}
		return filepath.Join(a.Workspace(), "skills"), "workspace", nil
	}
	if len(s.dirs) == 0 {
		return "", "", errors.New("no skills directory is configured")
	}
	return s.dirs[0], s.dirs[0], nil
}

var folderRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// Install writes a skill folder <name>/ holding files (slash-separated
// relative paths, at least SKILL.md) for agentID ("" = every agent). It
// never overwrites: a skill of that name already there is ErrExists.
func (s *Service) Install(agentID, name string, files map[string][]byte) (Skill, error) {
	if !folderRe.MatchString(name) {
		return Skill{}, fmt.Errorf("bad skill name %q", name)
	}
	if _, ok := files["SKILL.md"]; !ok {
		return Skill{}, errors.New("a skill needs a SKILL.md")
	}
	root, source, err := s.installDir(agentID)
	if err != nil {
		return Skill{}, err
	}
	for _, sk := range s.scan(root, source) {
		if sk.Name == name {
			return Skill{}, ErrExists
		}
	}
	dir := filepath.Join(root, name)
	if _, err := os.Stat(dir); err == nil {
		return Skill{}, ErrExists
	}
	// Write a temp folder and rename it, so the poll (and the prompt
	// catalog) never sees a half-written skill.
	if err := os.MkdirAll(root, 0o755); err != nil {
		return Skill{}, err
	}
	tmp, err := os.MkdirTemp(root, ".install-")
	if err != nil {
		return Skill{}, err
	}
	defer os.RemoveAll(tmp)
	for rel, b := range files {
		p := filepath.Join(tmp, filepath.FromSlash(rel))
		if !strings.HasPrefix(p, tmp+string(filepath.Separator)) {
			return Skill{}, fmt.Errorf("bad file path %q", rel)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return Skill{}, err
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return Skill{}, err
		}
	}
	if err := os.Rename(tmp, dir); err != nil {
		return Skill{}, err
	}
	Changed.Emit(s.c, struct{}{})
	meta, _, err := parse(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return Skill{}, err
	}
	if meta.Name == "" {
		meta.Name = name
	}
	return Skill{Name: meta.Name, Description: meta.Description, Version: meta.version(), Dir: dir, Source: source, Agent: agentID}, nil
}

// OriginFile records where a skill was installed from (written by the
// catalog, which checks it for updates); it is not shown to agents.
const OriginFile = ".crewbase-origin.json"

// Replace swaps the files of an installed skill (an update): the new
// files are written aside and renamed in, so the skill is never half there.
func (s *Service) Replace(agentID, name string, files map[string][]byte) (Skill, error) {
	if _, ok := files["SKILL.md"]; !ok {
		return Skill{}, errors.New("a skill needs a SKILL.md")
	}
	root, source, err := s.installDir(agentID)
	if err != nil {
		return Skill{}, err
	}
	dir := filepath.Join(root, name)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() || filepath.Dir(dir) != filepath.Clean(root) {
		return Skill{}, ErrNotFound
	}
	tmp, err := os.MkdirTemp(root, ".install-")
	if err != nil {
		return Skill{}, err
	}
	defer os.RemoveAll(tmp)
	for rel, b := range files {
		p := filepath.Join(tmp, filepath.FromSlash(rel))
		if !strings.HasPrefix(p, tmp+string(filepath.Separator)) {
			return Skill{}, fmt.Errorf("bad file path %q", rel)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return Skill{}, err
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return Skill{}, err
		}
	}
	old := filepath.Join(root, ".old-"+name)
	_ = os.RemoveAll(old)
	if err := os.Rename(dir, old); err != nil {
		return Skill{}, err
	}
	if err := os.Rename(tmp, dir); err != nil {
		_ = os.Rename(old, dir)
		return Skill{}, err
	}
	_ = os.RemoveAll(old)
	Changed.Emit(s.c, struct{}{})
	meta, _, err := parse(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return Skill{}, err
	}
	if meta.Name == "" {
		meta.Name = name
	}
	return Skill{Name: meta.Name, Description: meta.Description, Version: meta.version(), Dir: dir, Source: source, Agent: agentID}, nil
}

// Remove deletes the skill named name from agentID's workspace or, for "",
// from the first configured directory.
func (s *Service) Remove(agentID, name string) error {
	root, source, err := s.installDir(agentID)
	if err != nil {
		return err
	}
	for _, sk := range s.scan(root, source) {
		if sk.Name != name {
			continue
		}
		if filepath.Dir(sk.Dir) != filepath.Clean(root) {
			return ErrNotFound
		}
		if err := os.RemoveAll(sk.Dir); err != nil {
			return err
		}
		Changed.Emit(s.c, struct{}{})
		return nil
	}
	return ErrNotFound
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

// maxInvokeBytes caps the instructions a / pick puts into one message.
const maxInvokeBytes = 64 << 10

// slashRe is "/name" opening a message (how people pick a skill where
// there is no menu, such as Telegram or Feishu).
var slashRe = regexp.MustCompile(`^/([A-Za-z0-9][A-Za-z0-9._-]{0,127})(?:\s+|$)`)

// Resolve fills in the instructions of the skills picked for in, and turns
// a message that opens with "/<skill>" into that pick. A skill the agent
// cannot see is an error: the person asked for it by name.
func (s *Service) Resolve(in *agent.Input, agentID string) error {
	if len(in.Use) == 0 {
		if m := slashRe.FindStringSubmatch(in.Text); m != nil {
			for _, sk := range s.List(agentID) {
				if sk.Name == m[1] {
					in.Use = []session.Invoke{{Kind: session.InvokeSkill, Name: sk.Name}}
					in.Text = strings.TrimSpace(in.Text[len(m[0]):])
					break
				}
			}
		}
	}
	for i, u := range in.Use {
		if u.Kind != session.InvokeSkill || u.Text != "" {
			continue
		}
		sk, body, files, err := s.Load(agentID, u.Name)
		if err != nil {
			return fmt.Errorf("skill %q is not available to this agent", u.Name)
		}
		text := fmt.Sprintf("Folder: %s\n\n%s", sk.Dir, body)
		if len(files) > 0 {
			text += "\n\nOther files in this skill (read them with read_file if needed):\n- " + strings.Join(files, "\n- ")
		}
		if len(text) > maxInvokeBytes {
			text = text[:maxInvokeBytes] + "\n… [truncated; read SKILL.md in the folder for the rest]"
		}
		in.Use[i].Text = text
	}
	return nil
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
		// a skill picked with / is loaded when its turn starts (and recorded
		// with the message, see session.Invoke)
		agentloop.PreStep.Use(c, func(ctx context.Context, in *agent.Input, next kernel.Next[*agent.Input, *agent.Input]) (*agent.Input, error) {
			if in != nil && !in.Logged && !in.Resumed {
				if err := s.Resolve(in, in.Agent); err != nil {
					return nil, err
				}
			}
			return next(ctx, in)
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
