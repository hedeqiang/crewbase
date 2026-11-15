// Package catalog is Crewbase's built-in marketplace of MCP server presets
// and skills (设置 › 插件与技能). The list is curated and embedded in the
// binary: nothing is fetched to show it, and installing a preset only
// writes local config (an MCP server through the mcp service, a skill
// folder through the skills service).
//
// Every preset was checked against its vendor's documentation when it was
// added (see each preset's docs link). A preset says how it is reached
// (Streamable HTTP url or a stdio command), what it needs from the person
// (nothing, an OAuth sign-in handled by the mcp service, or values they
// paste) and where those values go: secrets are written to the system
// keychain under mcp.SecretRef and only the reference lands in mcp.json.
//
// Presets that share a Server are parts of one server (the Google
// Workspace entries are one workspace-mcp process): installing a part adds
// it to the "{parts}" argument of that server's command, removing the last
// part removes the server.
package catalog

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"

	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/features/mcp"
	"github.com/hedeqiang/crewbase/features/skills"
	"github.com/hedeqiang/crewbase/kernel"
)

// ServiceKey is the key of the catalog service.
const ServiceKey = "catalog"

//go:embed catalog.json
var catalogJSON []byte

//go:embed skills
var skillFS embed.FS

// Text is a string in both UI languages.
type Text struct {
	Zh string `json:"zh"`
	En string `json:"en"`
}

// Category groups catalog items under a heading.
type Category struct {
	ID    string `json:"id"`
	Title Text   `json:"title"`
	Desc  Text   `json:"desc"`
}

// Input is a value the person supplies when installing a preset. It
// replaces "{id}" in the preset's command, env and headers.
type Input struct {
	ID          string `json:"id"`
	Label       Text   `json:"label"`
	Help        Text   `json:"help,omitzero"`
	Placeholder string `json:"placeholder,omitempty"`
	// Secret values go to the keychain; config keeps the reference.
	Secret bool `json:"secret,omitempty"`
	// Optional inputs left empty drop the header, env entry or argument
	// they appear in.
	Optional bool `json:"optional,omitempty"`
}

// Preset is one installable MCP server (or part of one).
type Preset struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Name     string `json:"name"`
	// Icon is a brand icon key the web UI knows ("" = a letter tile).
	Icon string `json:"icon,omitempty"`
	Desc Text   `json:"desc"`
	// Auth is what the person must do: none | oauth | token | setup
	// (values to paste that are not a token, e.g. a folder).
	Auth  string `json:"auth"`
	Docs  string `json:"docs"`
	Setup Text   `json:"setup,omitzero"`

	Server    string            `json:"server"`
	Transport string            `json:"transport"`
	URL       string            `json:"url,omitempty"`
	Command   []string          `json:"command,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	OAuth     bool              `json:"oauth,omitempty"`
	Inputs    []Input           `json:"inputs,omitempty"`
	// Part is this preset's word in its server's "{parts}" argument.
	Part  string `json:"part,omitempty"`
	Group string `json:"group,omitempty"`
	// SignIn names a built-in sign-in that obtains the token input for
	// the person ("github": GitHub's OAuth device flow, see github.go).
	// Without a configured client the preset falls back to its inputs.
	SignIn string `json:"signIn,omitempty"`
}

// How a preset is installed (Preset.kind). Install, Uninstall and List
// switch on it; a new way to connect (e.g. a built-in connector) adds a
// kind here and a case in each.
const (
	kindServer = "server" // one MCP server of its own
	kindPart   = "part"   // a part of a server several presets share
)

func (p Preset) kind() string {
	if p.Part != "" {
		return kindPart
	}
	return kindServer
}

// SkillInfo is a built-in skill as listed (its text is in skills/<name>/).
type SkillInfo struct {
	Name string `json:"name"`
	Icon string `json:"icon,omitempty"`
	Desc Text   `json:"desc"`
}

type file struct {
	Categories []Category `json:"categories"`
	// Groups hold what the parts of one server share (a preset names its
	// group; its own fields win).
	Groups  map[string]Preset `json:"groups"`
	Presets []Preset          `json:"presets"`
	Skills  []SkillInfo       `json:"skills"`
}

var builtin = func() file {
	f, err := parseFile(catalogJSON)
	if err != nil {
		panic("catalog: catalog.json: " + err.Error())
	}
	return f
}()

func parseFile(b []byte) (file, error) {
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return f, err
	}
	for i, p := range f.Presets {
		if p.Group == "" {
			continue
		}
		g, ok := f.Groups[p.Group]
		if !ok {
			return f, fmt.Errorf("preset %s: unknown group %q", p.ID, p.Group)
		}
		g.ID, g.Category, g.Name, g.Icon, g.Desc, g.Part, g.Group = p.ID, p.Category, p.Name, p.Icon, p.Desc, p.Part, p.Group
		f.Presets[i] = g
	}
	return f, nil
}

// SkillCategory is the category id of the built-in skills.
const SkillCategory = "skills"

// Item is one catalog entry as the API lists it.
type Item struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"` // mcp | skill
	Category string `json:"category"`
	Name     string `json:"name"`
	Icon     string `json:"icon,omitempty"`
	Desc     Text   `json:"desc"`
	// Auth: none | oauth | token | setup, or "device" when a built-in
	// sign-in (SignIn) is available: connecting starts it and Inputs are
	// only the fallback.
	Auth   string `json:"auth"`
	SignIn string `json:"signIn,omitempty"`
	Docs   string `json:"docs,omitempty"`
	Setup  Text   `json:"setup,omitzero"`
	// Inputs still needed: empty when installing a further part of a
	// server that is already set up.
	Inputs    []Input `json:"inputs,omitempty"`
	Transport string  `json:"transport,omitempty"`
	Endpoint  string  `json:"endpoint,omitempty"` // url or command line
	// Installed: the MCP server (or skill) exists; Server names it.
	Installed bool   `json:"installed"`
	Server    string `json:"server,omitempty"`
	Scope     string `json:"scope,omitempty"` // MCP scope / skill agent
}

// Listing is GET /api/catalog.
type Listing struct {
	Categories []Category `json:"categories"`
	Items      []Item     `json:"items"`
}

// Errors.
var (
	ErrUnknown   = errors.New("unknown catalog item")
	ErrInstalled = errors.New("already installed")
	ErrMissing   = errors.New("not installed")
	ErrInput     = errors.New("missing input")
)

// Service is the `catalog` service.
type Service struct {
	c  *kernel.Context
	gh *githubSignIn
}

func (s *Service) mcp() *mcp.Service {
	v, _ := kernel.Lookup[*mcp.Service](s.c, mcp.ServiceKey)
	return v
}

func (s *Service) skills() *skills.Service {
	v, _ := kernel.Lookup[*skills.Service](s.c, skills.ServiceKey)
	return v
}

func (s *Service) creds() *credentials.Service {
	v, _ := kernel.Lookup[*credentials.Service](s.c, credentials.ServiceKey)
	return v
}

func preset(id string) (Preset, bool) {
	for _, p := range builtin.Presets {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}

// skillID is the catalog id of a built-in skill.
func skillID(name string) string { return "skill-" + name }

func skillInfo(id string) (SkillInfo, bool) {
	for _, sk := range builtin.Skills {
		if skillID(sk.Name) == id {
			return sk, true
		}
	}
	return SkillInfo{}, false
}

// List returns the catalog with installed state.
func (s *Service) List() Listing {
	out := Listing{Categories: append([]Category(nil), builtin.Categories...)}
	if svc := s.mcp(); svc != nil {
		servers := svc.List()
		for _, p := range builtin.Presets {
			it := Item{ID: p.ID, Kind: "mcp", Category: p.Category, Name: p.Name, Icon: p.Icon, Desc: p.Desc,
				Auth: p.Auth, Docs: p.Docs, Setup: p.Setup, Inputs: p.Inputs, Transport: p.Transport, Endpoint: p.endpoint()}
			if p.SignIn != "" && s.signInReady(p.SignIn) {
				it.Auth, it.SignIn = "device", p.SignIn
			}
			for _, v := range servers {
				if v.Name == p.Server || (p.Part == "" && p.URL != "" && v.URL == p.URL) {
					switch p.kind() {
					case kindPart: // the shared server already holds the inputs
						it.Inputs = nil
						it.Installed = slices.Contains(v.Command, p.Part)
					default:
						it.Installed = true
					}
					if it.Installed || p.Part != "" {
						it.Server, it.Scope = v.Name, v.Scope
					}
					break
				}
			}
			out.Items = append(out.Items, it)
		}
	}
	if sk := s.skills(); sk != nil {
		out.Categories = append(out.Categories, Category{ID: SkillCategory,
			Title: Text{Zh: "技能", En: "Skills"},
			Desc:  Text{Zh: "写好的做事方法，Agent 需要时才加载全文", En: "Written-down ways of working; agents load the full text only when needed"}})
		installed := map[string]skills.Skill{}
		for _, x := range sk.All() {
			if _, ok := installed[x.Name]; !ok || x.Agent == "" {
				installed[x.Name] = x
			}
		}
		for _, b := range builtin.Skills {
			x, ok := installed[b.Name]
			it := Item{ID: skillID(b.Name), Kind: "skill", Category: SkillCategory, Name: b.Name, Icon: b.Icon, Desc: b.Desc, Auth: "none", Installed: ok}
			if ok {
				it.Server, it.Scope = x.Name, x.Agent
			}
			out.Items = append(out.Items, it)
		}
	}
	return out
}

func (p Preset) endpoint() string {
	if p.Transport == mcp.TransportHTTP {
		return p.URL
	}
	var parts []string
	for _, a := range p.Command {
		if a == "{parts}" {
			a = p.Part
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

// Request is the POST /api/catalog/{id}/install body.
type Request struct {
	// Inputs by Input.ID. Secret values are written to the keychain and
	// never stored or echoed.
	Inputs map[string]string `json:"inputs,omitempty"`
	// Scope: "global" (default), an agent id or team:<id> for MCP
	// presets; "" or an agent id for skills.
	Scope string `json:"scope,omitempty"`
}

// Result of an install.
type Result struct {
	Kind   string        `json:"kind"`
	Server *mcp.View     `json:"server,omitempty"`
	Skill  *skills.Skill `json:"skill,omitempty"`
}

// Install installs a catalog item.
func (s *Service) Install(id string, req Request) (Result, error) {
	if sk, ok := skillInfo(id); ok {
		return s.installSkill(sk, req.Scope)
	}
	p, ok := preset(id)
	if !ok {
		return Result{}, ErrUnknown
	}
	svc := s.mcp()
	if svc == nil {
		return Result{}, errors.New("the mcp service is not running")
	}
	switch p.kind() {
	case kindPart:
		if v, ok := svc.Get(p.Server); ok {
			if slices.Contains(v.Command, p.Part) {
				return Result{}, ErrInstalled
			}
			nv, err := svc.Update(p.Server, func(x *mcp.Server) { x.Command = append(x.Command, p.Part) })
			if err != nil {
				return Result{}, err
			}
			return Result{Kind: "mcp", Server: &nv}, nil
		}
	default:
		if _, ok := svc.Get(p.Server); ok {
			return Result{}, ErrInstalled
		}
	}
	return s.addServer(svc, p, req)
}

// addServer creates p's server from the person's inputs, storing secret
// inputs in the keychain first.
func (s *Service) addServer(svc *mcp.Service, p Preset, req Request) (Result, error) {
	srv, secrets, err := p.build(req.Inputs, svc.ServerDir(p.Server))
	if err != nil {
		return Result{}, err
	}
	srv.Scope = req.Scope
	if len(secrets) > 0 {
		cs := s.creds()
		if cs == nil {
			return Result{}, errors.New("no credential store is available to keep the token")
		}
		for ref, v := range secrets {
			if err := cs.Set(ref, v); err != nil {
				s.dropSecrets(secrets)
				return Result{}, fmt.Errorf("store %s: %w", ref, err)
			}
		}
	}
	v, err := svc.Add(srv)
	if err != nil {
		s.dropSecrets(secrets)
		return Result{}, err
	}
	return Result{Kind: "mcp", Server: &v}, nil
}

func (s *Service) dropSecrets(m map[string]string) {
	if cs := s.creds(); cs != nil {
		for ref := range m {
			_ = cs.Delete(ref)
		}
	}
}

// build turns p and the person's inputs into a server config. secrets maps
// each keychain ref to its value (to be stored before the server is added).
// "{serverDir}" is the server's own folder (mcp.Service.ServerDir).
func (p Preset) build(in map[string]string, serverDir string) (mcp.Server, map[string]string, error) {
	vals := map[string]string{} // input id → value as written into config
	secrets := map[string]string{}
	for _, x := range p.Inputs {
		v := strings.TrimSpace(in[x.ID])
		if v == "" {
			if !x.Optional {
				return mcp.Server{}, nil, fmt.Errorf("%w: %s", ErrInput, x.ID)
			}
			continue
		}
		if x.Secret {
			ref := mcp.SecretRef(p.Server, strings.ToLower(x.ID))
			secrets[ref] = v
			v = "${" + ref + "}"
		}
		vals[x.ID] = v
	}
	// fill replaces placeholders; ok=false when one names an empty input.
	fill := func(t string) (string, bool) {
		ok := true
		t = strings.ReplaceAll(t, "{serverDir}", serverDir)
		for _, x := range p.Inputs {
			ph := "{" + x.ID + "}"
			if !strings.Contains(t, ph) {
				continue
			}
			v, set := vals[x.ID]
			if !set {
				ok = false
				continue
			}
			t = strings.ReplaceAll(t, ph, v)
		}
		// a whole-value reference is stored bare ("keychain:…")
		if strings.HasPrefix(t, "${") && strings.HasSuffix(t, "}") && strings.Count(t, "${") == 1 {
			t = t[2 : len(t)-1]
		}
		return t, ok
	}
	srv := mcp.Server{Name: p.Server, Transport: p.Transport, URL: p.URL, Enabled: true}
	if p.OAuth {
		srv.OAuth = &mcp.OAuth{}
	}
	for _, a := range p.Command {
		if a == "{parts}" {
			srv.Command = append(srv.Command, p.Part)
			continue
		}
		if v, ok := fill(a); ok {
			srv.Command = append(srv.Command, v)
		}
	}
	fillMap := func(m map[string]string) map[string]string {
		if len(m) == 0 {
			return nil
		}
		out := map[string]string{}
		for k, t := range m {
			if v, ok := fill(t); ok {
				out[k] = v
			}
		}
		return out
	}
	srv.Env, srv.Headers = fillMap(p.Env), fillMap(p.Headers)
	return srv, secrets, nil
}

func (s *Service) installSkill(sk SkillInfo, agentID string) (Result, error) {
	ss := s.skills()
	if ss == nil {
		return Result{}, errors.New("the skills service is not running")
	}
	files, err := skillFiles(sk.Name)
	if err != nil {
		return Result{}, err
	}
	if agentID == mcp.ScopeGlobal {
		agentID = ""
	}
	v, err := ss.Install(agentID, sk.Name, files)
	if errors.Is(err, skills.ErrExists) {
		return Result{}, ErrInstalled
	}
	if err != nil {
		return Result{}, err
	}
	return Result{Kind: "skill", Skill: &v}, nil
}

// skillFiles reads a built-in skill's folder from the embedded tree.
func skillFiles(name string) (map[string][]byte, error) {
	root := path.Join("skills", name)
	files := map[string][]byte{}
	err := fs.WalkDir(skillFS, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := skillFS.ReadFile(p)
		if err != nil {
			return err
		}
		files[strings.TrimPrefix(p, root+"/")] = b
		return nil
	})
	return files, err
}

// Uninstall removes a catalog item: a part from its server (the server
// when it was the last part), the whole server, or a skill (from agentID's
// workspace, "" = the shared skills directory).
func (s *Service) Uninstall(id, agentID string) error {
	if sk, ok := skillInfo(id); ok {
		ss := s.skills()
		if ss == nil {
			return ErrMissing
		}
		err := ss.Remove(agentID, sk.Name)
		if errors.Is(err, skills.ErrNotFound) {
			return ErrMissing
		}
		return err
	}
	p, ok := preset(id)
	if !ok {
		return ErrUnknown
	}
	svc := s.mcp()
	if svc == nil {
		return ErrMissing
	}
	v, ok := svc.Get(p.Server)
	if !ok {
		return ErrMissing
	}
	if p.kind() == kindPart {
		if !slices.Contains(v.Command, p.Part) {
			return ErrMissing
		}
		left := 0
		for _, q := range builtin.Presets {
			if q.Server == p.Server && q.Part != p.Part && slices.Contains(v.Command, q.Part) {
				left++
			}
		}
		if left > 0 {
			_, err := svc.Update(p.Server, func(x *mcp.Server) {
				x.Command = slices.DeleteFunc(x.Command, func(a string) bool { return a == p.Part })
			})
			return err
		}
	}
	return svc.Remove(p.Server)
}

// Plugin provides the `catalog` service.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/catalog",
	Optional:    []string{mcp.ServiceKey, skills.ServiceKey, credentials.ServiceKey},
	Description: "Built-in catalog of MCP server presets and skills, installable in one step.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		s := &Service{c: c}
		s.gh = newGitHubSignIn(s, cfg)
		c.Go(s.gh.refreshLoop)
		return c.Provide(ServiceKey, s)
	},
})
