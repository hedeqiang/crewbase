// Package roles imports role templates from other agent products (C-5):
// a Hermes agent folder (SOUL.md + skills/), a Markdown role file with
// front matter, a JSON role template, or pasted text. Only instructions
// and flows (skills) are migrated; conversations, memory, credentials and
// the other product's settings are never imported, and the preview lists
// every file left out and why.
package roles

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
	"gopkg.in/yaml.v3"
)

// Limits on what an import may carry.
const (
	MaxTotalBytes = 8 << 20
	MaxFileBytes  = 1 << 20
	MaxFiles      = 400
	maxInstrBytes = 256 << 10
)

// Formats detected.
const (
	FormatHermes   = "hermes"
	FormatMarkdown = "markdown"
	FormatJSON     = "json"
	FormatText     = "text"
)

// Reasons a file is left out.
const (
	SkipMemory       = "memory"       // long-term memory, user profile
	SkipConversation = "conversation" // chats, sessions, logs
	SkipSecret       = "secret"       // keys, tokens, auth files
	SkipSettings     = "settings"     // the other product's model / tool config
	SkipUnsupported  = "unsupported"
	SkipTooLarge     = "too-large"
)

// File is one uploaded file (Data is base64 in JSON).
type File struct {
	Path string `json:"path"`
	Data []byte `json:"data"`

	tooLarge bool
}

// Source is what the person uploaded or pasted.
type Source struct {
	Files []File `json:"files,omitempty"`
	Zip   []byte `json:"zip,omitempty"`  // a .zip of the folder
	Text  string `json:"text,omitempty"` // pasted instructions
}

// Skill is a flow carried over as a SKILL.md folder.
type Skill struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Files       map[string][]byte `json:"-"`
	FileNames   []string          `json:"files"`
}

// Skipped is a file left out of the import.
type Skipped struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Template is the parsed role, shown as the import preview.
type Template struct {
	Format       string    `json:"format"`
	Name         string    `json:"name"`
	Role         string    `json:"role,omitempty"`
	Instructions string    `json:"instructions"`
	Skills       []Skill   `json:"skills"`
	Skipped      []Skipped `json:"skipped"`
}

var (
	skillNameRe = regexp.MustCompile(`[^a-z0-9._-]+`)
	headingRe   = regexp.MustCompile(`(?m)^#\s+(.+?)\s*$`)
)

func skillSlug(s string) string {
	v := strings.Trim(skillNameRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-"), "-.")
	if len(v) > 60 {
		v = v[:60]
	}
	return v
}

// clean normalises an uploaded path; ok=false for anything unsafe.
func clean(p string) (string, bool) {
	p = strings.ReplaceAll(p, "\\", "/")
	if strings.HasPrefix(p, "/") || strings.Contains(p, "\x00") {
		return "", false
	}
	c := path.Clean(p)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", false
	}
	return c, true
}

// stripRoot removes a folder every file shares (the uploaded folder name,
// e.g. ".hermes/"), so layouts are matched from their root.
func stripRoot(files []File) []File {
	if len(files) == 0 {
		return files
	}
	first, _, ok := strings.Cut(files[0].Path, "/")
	if !ok {
		return files
	}
	for _, f := range files {
		if d, _, ok := strings.Cut(f.Path, "/"); !ok || d != first {
			return files
		}
	}
	out := make([]File, len(files))
	for i, f := range files {
		out[i] = File{Path: strings.TrimPrefix(f.Path, first+"/"), Data: f.Data, tooLarge: f.tooLarge}
	}
	return stripRoot(out)
}

func unzip(b []byte) ([]File, error) {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, fmt.Errorf("not a zip file: %w", err)
	}
	var out []File
	var total int64
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if len(out) >= MaxFiles {
			return nil, fmt.Errorf("too many files (max %d)", MaxFiles)
		}
		if f.UncompressedSize64 > MaxFileBytes {
			out = append(out, File{Path: f.Name, tooLarge: true})
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(rc, MaxFileBytes+1))
		rc.Close()
		if err != nil {
			return nil, err
		}
		total += int64(len(data))
		if total > MaxTotalBytes {
			return nil, fmt.Errorf("the template is too large (max %d MB)", MaxTotalBytes>>20)
		}
		out = append(out, File{Path: f.Name, Data: data})
	}
	return out, nil
}

// classify says what a path is in a role folder.
func classify(p string) (kind, reason string) {
	low := strings.ToLower(p)
	base := path.Base(low)
	segs := strings.Split(low, "/")
	for _, s := range segs[:len(segs)-1] {
		switch s {
		case "memories", "memory":
			return "", SkipMemory
		case "sessions", "conversations", "history", "chats", "logs", "transcripts":
			return "", SkipConversation
		case ".git", "node_modules", "__pycache__", "cache", ".cache":
			return "", SkipUnsupported
		}
	}
	switch {
	case base == ".env" || strings.HasPrefix(base, ".env.") || base == "auth.json" || strings.HasPrefix(base, "credentials") ||
		strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") || base == "token" || strings.HasSuffix(base, ".token") ||
		strings.HasPrefix(base, "token.") || strings.HasPrefix(base, "tokens."):
		return "", SkipSecret
	case base == "memory.md" || base == "user.md":
		return "", SkipMemory
	case strings.HasSuffix(base, ".jsonl") || strings.HasSuffix(base, ".db") || strings.HasSuffix(base, ".sqlite") ||
		strings.HasSuffix(base, ".sqlite3") || strings.HasSuffix(base, ".log"):
		return "", SkipConversation
	case base == "config.yaml" || base == "config.yml" || base == "config.toml" || base == "settings.json" || base == "cli-config.yaml":
		return "", SkipSettings
	}
	if len(segs) >= 3 && segs[0] == "skills" {
		return "skill", ""
	}
	if len(segs) >= 4 && segs[1] == "skills" { // skills nested one level, e.g. hermes/skills/<cat>/<name>
		return "skill", ""
	}
	if len(segs) == 1 {
		switch base {
		case "soul.md":
			return "soul", ""
		case "agents.md", "instructions.md", "claude.md", "system_prompt.md", "system-prompt.md", "prompt.md", "role.md", "persona.md":
			return "instructions", ""
		}
		if strings.HasSuffix(base, ".json") {
			return "json", ""
		}
		if strings.HasSuffix(base, ".md") || strings.HasSuffix(base, ".txt") {
			return "markdown", ""
		}
	}
	return "", SkipUnsupported
}

type frontMatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Role        string `yaml:"role"`
}

// splitFront returns YAML front matter and the body.
func splitFront(text string) (frontMatter, string) {
	var fm frontMatter
	t := strings.TrimLeft(strings.TrimPrefix(text, "\uFEFF"), " \n")
	if !strings.HasPrefix(t, "---\n") {
		return fm, strings.TrimSpace(text)
	}
	rest := t[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return fm, strings.TrimSpace(text)
	}
	_ = yaml.Unmarshal([]byte(rest[:end]), &fm)
	body := rest[end+4:]
	body = strings.TrimPrefix(body, "\n")
	return fm, strings.TrimSpace(body)
}

// firstParagraph is a one-line role from the body.
func firstParagraph(body string) string {
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") || strings.HasPrefix(l, "---") || strings.HasPrefix(l, "```") {
			continue
		}
		l = strings.TrimLeft(l, "-*> ")
		// a tag, not a paragraph: the first sentence, at most 40 runes
		if i := strings.IndexAny(l, "。！？!?；;"); i > 0 {
			l = l[:i]
		} else if i := strings.Index(l, ". "); i > 0 {
			l = l[:i]
		}
		r := []rune(l)
		if len(r) > 40 {
			l = string(r[:39]) + "…"
		}
		return l
	}
	return ""
}

// jsonTemplate is the JSON role shape (also what a Grok Bot export or a
// hand-written template maps to): name, description, instructions and
// taught tasks / workflows.
type jsonTemplate struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Role         string   `json:"role"`
	Instructions string   `json:"instructions"`
	SystemPrompt string   `json:"system_prompt"`
	Prompt       string   `json:"prompt"`
	Tasks        []jsonFl `json:"tasks"`
	Workflows    []jsonFl `json:"workflows"`
	Skills       []jsonFl `json:"skills"`
}

type jsonFl struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Instructions string   `json:"instructions"`
	Prompt       string   `json:"prompt"`
	Steps        []string `json:"steps"`
}

func (f jsonFl) skill() (Skill, bool) {
	name := skillSlug(f.Name)
	if name == "" {
		return Skill{}, false
	}
	var b strings.Builder
	fmt.Fprintf(&b, "---\nname: %s\ndescription: %s\n---\n\n# %s\n\n", name, yamlLine(firstNonEmpty(f.Description, f.Name)), f.Name)
	if body := firstNonEmpty(f.Instructions, f.Prompt); body != "" {
		b.WriteString(strings.TrimSpace(body) + "\n")
	}
	if len(f.Steps) > 0 {
		b.WriteString("\n## Steps\n\n")
		for i, s := range f.Steps {
			fmt.Fprintf(&b, "%d. %s\n", i+1, strings.TrimSpace(s))
		}
	}
	return Skill{Name: name, Description: firstNonEmpty(f.Description, f.Name), Files: map[string][]byte{"SKILL.md": []byte(b.String())}}, true
}

func yamlLine(s string) string {
	b, _ := json.Marshal(strings.ReplaceAll(strings.TrimSpace(s), "\n", " "))
	return string(b) // a JSON string is a valid YAML scalar
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return strings.TrimSpace(x)
		}
	}
	return ""
}

// Parse reads a role template without changing anything.
func Parse(src Source) (Template, error) {
	files := src.Files
	if len(src.Zip) > 0 {
		zf, err := unzip(src.Zip)
		if err != nil {
			return Template{}, err
		}
		files = append(files, zf...)
	}
	if len(files) == 0 && strings.TrimSpace(src.Text) != "" {
		files = []File{{Path: "pasted.md", Data: []byte(src.Text)}}
	}
	if len(files) == 0 {
		return Template{}, errors.New("nothing to import: choose a folder, a file or paste the instructions")
	}
	if len(files) > MaxFiles {
		return Template{}, fmt.Errorf("too many files (max %d)", MaxFiles)
	}
	var safe []File
	t := Template{Skills: []Skill{}, Skipped: []Skipped{}}
	total := 0
	for _, f := range files {
		p, ok := clean(f.Path)
		if !ok {
			t.Skipped = append(t.Skipped, Skipped{Path: f.Path, Reason: SkipUnsupported})
			continue
		}
		total += len(f.Data)
		if total > MaxTotalBytes {
			return Template{}, fmt.Errorf("the template is too large (max %d MB)", MaxTotalBytes>>20)
		}
		safe = append(safe, File{Path: p, Data: f.Data, tooLarge: f.tooLarge})
	}
	safe = stripRoot(safe)
	sort.Slice(safe, func(i, j int) bool { return safe[i].Path < safe[j].Path })

	var soul, instr []string
	var fm frontMatter
	var jt *jsonTemplate
	skills := map[string]*Skill{}
	// Skill folders are the directories that contain a SKILL.md.
	skillDirs := map[string]bool{}
	for _, f := range safe {
		if path.Base(f.Path) == "SKILL.md" || strings.EqualFold(path.Base(f.Path), "skill.md") {
			if k, _ := classify(f.Path); k == "skill" {
				skillDirs[path.Dir(f.Path)] = true
			}
		}
	}
	dirOf := func(p string) string {
		for d := path.Dir(p); d != "." && d != "/"; d = path.Dir(d) {
			if skillDirs[d] {
				return d
			}
		}
		return ""
	}
	for _, f := range safe {
		kind, reason := classify(f.Path)
		if f.tooLarge || len(f.Data) > MaxFileBytes {
			t.Skipped = append(t.Skipped, Skipped{Path: f.Path, Reason: SkipTooLarge})
			continue
		}
		if reason != "" {
			t.Skipped = append(t.Skipped, Skipped{Path: f.Path, Reason: reason})
			continue
		}
		switch kind {
		case "skill":
			d := dirOf(f.Path)
			if d == "" {
				t.Skipped = append(t.Skipped, Skipped{Path: f.Path, Reason: SkipUnsupported})
				continue
			}
			sk := skills[d]
			if sk == nil {
				sk = &Skill{Name: skillSlug(path.Base(d)), Files: map[string][]byte{}}
				skills[d] = sk
			}
			rel := strings.TrimPrefix(f.Path, d+"/")
			if strings.EqualFold(rel, "skill.md") {
				rel = "SKILL.md"
				sfm, _ := splitFront(string(f.Data))
				if n := skillSlug(sfm.Name); n != "" {
					sk.Name = n
				}
				sk.Description = sfm.Description
			}
			sk.Files[rel] = f.Data
		case "soul":
			soul = append(soul, strings.TrimSpace(string(f.Data)))
		case "instructions", "markdown":
			ffm, body := splitFront(string(f.Data))
			if fm.Name == "" {
				fm.Name = ffm.Name
			}
			if fm.Description == "" {
				fm.Description = firstNonEmpty(ffm.Description, ffm.Role)
			}
			if body != "" {
				instr = append(instr, body)
			}
		case "json":
			var x jsonTemplate
			if err := json.Unmarshal(f.Data, &x); err != nil || firstNonEmpty(x.Instructions, x.SystemPrompt, x.Prompt) == "" && len(x.Tasks)+len(x.Workflows)+len(x.Skills) == 0 {
				t.Skipped = append(t.Skipped, Skipped{Path: f.Path, Reason: SkipUnsupported})
				continue
			}
			jt = &x
		}
	}

	switch {
	case len(soul) > 0:
		t.Format = FormatHermes
	case jt != nil:
		t.Format = FormatJSON
	case len(src.Files) == 0 && len(src.Zip) == 0:
		t.Format = FormatText
	default:
		t.Format = FormatMarkdown
	}
	body := strings.Join(append(soul, instr...), "\n\n")
	if jt != nil {
		body = strings.TrimSpace(strings.Join([]string{firstNonEmpty(jt.Instructions, jt.SystemPrompt, jt.Prompt), body}, "\n\n"))
		fm.Name = firstNonEmpty(fm.Name, jt.Name)
		fm.Description = firstNonEmpty(fm.Description, jt.Description, jt.Role)
		for _, fl := range append(append(jt.Tasks, jt.Workflows...), jt.Skills...) {
			if sk, ok := fl.skill(); ok {
				s := sk
				skills["json:"+sk.Name] = &s
			}
		}
	}
	if len(body) > maxInstrBytes {
		return Template{}, fmt.Errorf("the instructions are too long (max %d KB)", maxInstrBytes>>10)
	}
	if strings.TrimSpace(body) == "" && len(skills) == 0 {
		return Template{}, errors.New("no instructions or skills found (expected SOUL.md, a role .md / .json file, or skills/<name>/SKILL.md)")
	}

	t.Name = fm.Name
	if t.Name == "" {
		if m := headingRe.FindStringSubmatch(body); m != nil {
			t.Name = strings.TrimSpace(m[1])
		}
	}
	if r := []rune(t.Name); len(r) > 40 {
		t.Name = string(r[:40])
	}
	t.Role = firstNonEmpty(fm.Description, firstParagraph(body))
	t.Instructions = body

	seen := map[string]bool{}
	keys := make([]string, 0, len(skills))
	for k := range skills {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sk := skills[k]
		if _, ok := sk.Files["SKILL.md"]; !ok || sk.Name == "" || seen[sk.Name] {
			for p := range sk.Files {
				t.Skipped = append(t.Skipped, Skipped{Path: path.Join(strings.TrimPrefix(k, "json:"), p), Reason: SkipUnsupported})
			}
			continue
		}
		seen[sk.Name] = true
		for p := range sk.Files {
			sk.FileNames = append(sk.FileNames, p)
		}
		sort.Strings(sk.FileNames)
		t.Skills = append(t.Skills, *sk)
	}
	sort.Slice(t.Skipped, func(i, j int) bool { return t.Skipped[i].Path < t.Skipped[j].Path })
	return t, nil
}

// Options are the person's choices for the new agent.
type Options struct {
	Name   string   `json:"name"`
	Role   string   `json:"role"`
	Folder string   `json:"folder"`
	Mode   string   `json:"mode"`
	Color  string   `json:"color"`
	Shape  string   `json:"shape"`
	Face   string   `json:"face"`
	Models []string `json:"modelRefs"`
}

// Result of an import.
type Result struct {
	Agent   string    `json:"agent"`
	Session string    `json:"session"`
	Name    string    `json:"name"`
	Skills  int       `json:"skills"`
	Skipped []Skipped `json:"skipped"`
}

// Import creates a new agent from t: its INSTRUCTIONS.md and skills.
func Import(agents *agent.Registry, t Template, o Options) (Result, error) {
	name := firstNonEmpty(o.Name, t.Name)
	if name == "" {
		return Result{}, errors.New("a name is required")
	}
	if o.Mode == agent.ModeHost {
		return Result{}, errors.New("host mode cannot be enabled at creation")
	}
	spec := agent.Spec{Name: name, Role: firstNonEmpty(o.Role, t.Role), Folder: strings.TrimSpace(o.Folder), Mode: o.Mode, Color: o.Color, Shape: o.Shape, Face: o.Face}
	for _, m := range o.Models {
		ref, err := llm.ParseRef(m)
		if err != nil {
			return Result{}, err
		}
		spec.Models = append(spec.Models, ref)
	}
	instr := strings.TrimSpace(t.Instructions)
	if instr == "" {
		instr = "# " + name + "\n\n" + spec.Role
	} else if !strings.HasPrefix(instr, "#") {
		instr = "# " + name + "\n\n" + instr
	}
	spec.Instructions = instr + "\n"
	a, err := agents.Create(spec)
	if err != nil {
		return Result{}, err
	}
	root := filepath.Join(a.Workspace(), "skills")
	for _, sk := range t.Skills {
		dir := filepath.Join(root, sk.Name)
		for rel, data := range sk.Files {
			p := filepath.Join(dir, filepath.FromSlash(rel))
			if !strings.HasPrefix(p, dir+string(filepath.Separator)) {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return Result{}, err
			}
			// imported files are data, never executable
			if err := os.WriteFile(p, data, 0o644); err != nil {
				return Result{}, err
			}
		}
	}
	return Result{Agent: a.ID(), Session: a.Spec().Session, Name: name, Skills: len(t.Skills), Skipped: t.Skipped}, nil
}

// Plugin exposes role template import on the gateway.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/roles",
	Inject:      []string{gateway.ServiceKey, agent.ServiceKey},
	Description: "Import role templates (Hermes, Markdown, JSON): instructions and skills only.",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		agents := kernel.Use[*agent.Registry](c, agent.ServiceKey)
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/agents/import/preview", MaxBody: MaxTotalBytes*4/3 + 1<<20,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var src Source
				if err := r.Decode(&src); err != nil {
					return nil, err
				}
				t, err := Parse(src)
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return t, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/agents/import", MaxBody: MaxTotalBytes*4/3 + 1<<20,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b struct {
					Source
					Options Options `json:"options"`
				}
				if err := r.Decode(&b); err != nil {
					return nil, err
				}
				t, err := Parse(b.Source)
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				res, err := Import(agents, t, b.Options)
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return res, nil
			}})
		return nil
	},
})
