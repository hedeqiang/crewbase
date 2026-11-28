package agent

// The registry: creating agents (slugs, reserved ids), restoring their
// queues, persisting and loading specs, deleting, and the plugin.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/mozillazg/go-pinyin"
)

// ---------------------------------------------------------------- registry

// StatusEvent reports an agent status change.
type StatusEvent struct {
	Agent  string `json:"agent"`
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
	// Session the status concerns (e.g. where an approval or takeover is
	// waiting); "" when not session-specific.
	Session string `json:"session,omitempty"`
}

var (
	// Setup runs in the setup window: the agent exists and has its scope but
	// is not published and receives no input. Register private tools here.
	Setup = kernel.NewSerial[*Agent, struct{}]("agent/setup")
	// Created runs after publication (awaited: e.g. MCP connections).
	Created       = kernel.NewSerial[*Agent, struct{}]("agent/created")
	Deleted       = kernel.NewEmit[string]("agent/deleted")
	Updated       = kernel.NewEmit[Spec]("agent/updated")
	StatusChanged = kernel.NewEmit[StatusEvent]("agent/status")
	QueueChanged  = kernel.NewEmit[string]("agent/queue-changed")
)

// Registry is the `agents` service.
type Registry struct {
	c        *kernel.Context
	root     string
	wsBase   atomic.Value // string: where new agents' folders go ("" = <root>/workspaces)
	sessions *session.Store
	defaults []llm.ModelRef

	mu     sync.Mutex
	agents map[string]*Agent
	// fallback caches availableDefaults until the adapter set changes.
	fallback   []llm.ModelRef
	fallbackAt time.Time
	// roots gives the folders besides its workspace an agent works in.
	roots atomic.Pointer[func(agent string) (rw, ro []string)]
}

// SetExtraRoots names the folders, besides its workspace, an agent works
// in: rw like its workspace, ro only to read (the git feature: the
// worktrees an agent holds; an untrusted one is read only).
func (r *Registry) SetExtraRoots(fn func(agent string) (rw, ro []string)) { r.roots.Store(&fn) }

// ExtraRoots are the folders besides its workspace the agent works in (rw)
// or may only read (ro).
func (a *Agent) ExtraRoots() (rw, ro []string) {
	if a.reg == nil {
		return nil, nil
	}
	if fn := a.reg.roots.Load(); fn != nil {
		return (*fn)(a.id)
	}
	return nil, nil
}

var (
	idRe   = regexp.MustCompile(`[^a-z0-9-]+`)
	dashRe = regexp.MustCompile(`-{2,}`)
)

// Slug makes an id from a name. Chinese characters become pinyin
// (调研员 → diao-yan-yuan) so ids and workspace folders stay readable.
func Slug(name string) string {
	var b strings.Builder
	py := pinyin.NewArgs()
	for _, r := range strings.TrimSpace(name) {
		if unicode.Is(unicode.Han, r) {
			if p := pinyin.SinglePinyin(r, py); len(p) > 0 {
				b.WriteString("-" + p[0] + "-")
				continue
			}
		}
		b.WriteRune(r)
	}
	s := dashRe.ReplaceAllString(idRe.ReplaceAllString(strings.ToLower(b.String()), "-"), "-")
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	if s == "" {
		s = "agent"
	}
	return s
}

func isASCII(s string) bool {
	for _, r := range s {
		if r >= 128 {
			return false
		}
	}
	return true
}

// Create makes, persists and publishes an agent.
func (r *Registry) Create(spec Spec) (*Agent, error) {
	switch spec.Kind {
	case "":
	case KindChat:
		// a blank chat: no role, instructions or memory file
		if strings.TrimSpace(spec.Name) == "" {
			spec.Name = ChatDefaultName
		}
		spec.Role, spec.Instructions = "", ""
		if spec.ID == "" {
			spec.ID = "chat"
		}
	default:
		return nil, fmt.Errorf("unknown agent kind %q", spec.Kind)
	}
	if strings.TrimSpace(spec.Name) == "" {
		return nil, errors.New("agent name is required")
	}
	if spec.Mode == ModeHost {
		return nil, errors.New("host mode cannot be enabled at creation")
	}
	if err := ValidAvatar(spec.Avatar, spec.Vendor); err != nil {
		return nil, err
	}
	if spec.Mode == "" {
		spec.Mode = ModeSandbox
	}
	// the agent's own folder is the registry's to choose
	spec.Home = ""
	if spec.SharedWorkspace != "" && !spec.Ephemeral {
		dir, err := r.ProjectDir(spec.SharedWorkspace)
		if err != nil {
			return nil, err
		}
		spec.SharedWorkspace = dir
	}
	if len(spec.Models) == 0 {
		spec.Models = r.availableDefaults()
	}
	r.mu.Lock()
	if spec.ID == "" || (spec.Kind == KindChat && spec.ID == "chat") {
		base := Slug(spec.Name)
		// a template's key reads better than pinyin (researcher, not diao-yan-yuan)
		if h := Slug(spec.IDHint); spec.IDHint != "" && h != "agent" && !isASCII(spec.Name) {
			base = h
		}
		if spec.Kind == KindChat {
			base = "chat"
		}
		spec.ID = base
		// in a folder of the person's choosing, never adopt one of theirs
		taken := func(id string) bool {
			if _, ok := r.agents[id]; ok {
				return true
			}
			if spec.Ephemeral {
				return false
			}
			ents, err := os.ReadDir(filepath.Join(r.base(), id))
			return err == nil && len(ents) > 0
		}
		for i := 2; taken(spec.ID); i++ {
			spec.ID = fmt.Sprintf("%s-%d", base, i)
		}
	}
	if r.agents[spec.ID] != nil {
		r.mu.Unlock()
		return nil, fmt.Errorf("agent %s exists", spec.ID)
	}
	r.agents[spec.ID] = nil // reserve
	r.mu.Unlock()

	spec.Created = time.Now().UTC()
	ss, err := r.sessions.Create(session.Meta{Kind: "agent", Title: spec.Name, Agents: []string{spec.ID}, Folder: spec.Folder})
	if err != nil {
		r.unreserve(spec.ID)
		return nil, err
	}
	spec.Session = ss.ID()
	ws := filepath.Join(r.root, "workspaces", spec.ID)
	if b := r.base(); !spec.Ephemeral && b != filepath.Join(r.root, "workspaces") {
		ws = filepath.Join(b, spec.ID)
		spec.Home = ws
	}
	if err := os.MkdirAll(ws, 0o755); err != nil {
		r.unreserve(spec.ID)
		return nil, err
	}
	if spec.Kind != KindChat {
		instr := spec.Instructions
		if instr == "" {
			instr = "# " + spec.Name + "\n\n" + spec.Role + "\n"
		}
		writeIfMissing(filepath.Join(ws, "INSTRUCTIONS.md"), instr)
		writeIfMissing(filepath.Join(ws, "MEMORY.md"), "# Memory\n\n")
	}
	if err := r.persist(spec); err != nil {
		r.unreserve(spec.ID)
		return nil, err
	}
	return r.publish(spec)
}

func (r *Registry) unreserve(id string) {
	r.mu.Lock()
	if r.agents[id] == nil {
		delete(r.agents, id)
	}
	r.mu.Unlock()
}

func (r *Registry) publish(spec Spec) (*Agent, error) {
	a := &Agent{reg: r, id: spec.ID, created: spec.Created, spec: spec, status: StatusIdle, wake: make(chan struct{}, 1)}
	a.ctx = r.c.Fork(spec.ID)
	if _, err := Setup.Run(a.ctx, a.ctx.Context(), a); err != nil {
		a.ctx.Dispose()
		r.unreserve(spec.ID)
		return nil, fmt.Errorf("agent setup: %w", err)
	}
	r.mu.Lock()
	r.agents[spec.ID] = a
	r.mu.Unlock()
	r.restoreQueue(a)
	if _, err := Created.Run(a.ctx, a.ctx.Context(), a); err != nil {
		r.c.Log().Warn("agent/created listener failed", "agent", spec.ID, "err", err)
	}
	return a, nil
}

// restoreQueue rebuilds unclaimed inputs from the agent's sessions.
func (r *Registry) restoreQueue(a *Agent) {
	ids := map[string]bool{a.spec.Session: true}
	for _, m := range r.sessions.List() {
		for _, x := range m.Agents {
			if x == a.id {
				ids[m.ID] = true
			}
		}
	}
	var pending []Input
	// paused or not: the latest pause / resume in any of its conversations
	// (they are written where the agent was then; map order must not decide)
	var pausedAt time.Time
	for id := range ids {
		ss, err := r.sessions.Get(id)
		if err != nil {
			continue
		}
		open := map[string]Input{}
		all := map[string]Input{}
		var order []string
		running := "" // input whose turn started and has not ended
		for _, e := range ss.Events() {
			if e.Agent != a.id {
				continue
			}
			switch e.Type {
			case session.TypeInputQueued:
				var in Input
				if e.Decode(&in) == nil {
					open[in.ID] = in
					all[in.ID] = in
					order = append(order, in.ID)
				}
			case session.TypeInputClaimed, session.TypeInputCancelled:
				var m map[string]string
				if e.Decode(&m) == nil {
					delete(open, m["id"])
					// a resumed turn the person then removed is not resumed again
					if e.Type == session.TypeInputCancelled && m["id"] == running {
						running = ""
					}
				}
			case session.TypeTurnStart:
				var m struct {
					Input string `json:"input"`
				}
				if e.Decode(&m) == nil {
					running = m.Input
				}
			case session.TypeTurnEnd:
				var m struct {
					Status string `json:"status"`
				}
				// a turn cut off by a shutdown is still unfinished
				if e.Decode(&m) == nil && m.Status != "interrupted" {
					running = ""
				}
			case session.TypeQueuePaused, session.TypeQueueResumed:
				if !e.Time.Before(pausedAt) {
					pausedAt = e.Time
					a.paused = e.Type == session.TypeQueuePaused
				}
			}
		}
		if in, ok := all[running]; ok && running != "" && resumable(in) {
			in.Resumed, in.Front = true, true
			in.Queued = time.Time{} // first
			pending = append(pending, in)
		}
		for _, id := range order {
			if in, ok := open[id]; ok {
				pending = append(pending, in)
			}
		}
	}
	sort.SliceStable(pending, func(i, j int) bool { return pending[i].Queued.Before(pending[j].Queued) })
	a.queue = pending
}

func writeIfMissing(path, content string) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		_ = os.WriteFile(path, []byte(content), 0o644)
	}
}

// Specs live in <root>/agents, outside every workspace: an agent can write
// its own workspace, so anything there that is read back as its definition
// (run mode, models, team, project folder) would let it raise its own
// permissions. <root>/agents/<id>.json is the spec of a persisted agent,
// <root>/agents/<id>.ephemeral marks a live sub-agent (its parent id) so a
// crash leftover is swept at startup.
const (
	specDir         = "agents"
	ephemeralSuffix = ".ephemeral"
	// legacySpec and legacyEphemeral are where older versions kept them,
	// inside the workspace; loadAll migrates them.
	legacySpec      = "agent.json"
	legacyEphemeral = ".ephemeral"
)

func (r *Registry) specPath(id string) string {
	return filepath.Join(r.root, specDir, id+".json")
}

func (r *Registry) ephemeralPath(id string) string {
	return filepath.Join(r.root, specDir, id+ephemeralSuffix)
}

func (r *Registry) persist(s Spec) error {
	if err := os.MkdirAll(filepath.Join(r.root, specDir), 0o700); err != nil {
		return err
	}
	if s.Ephemeral {
		return os.WriteFile(r.ephemeralPath(s.ID), []byte(s.Parent), 0o600)
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	return writeAtomic(r.specPath(s.ID), b)
}

func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Get returns a published agent.
func (r *Registry) Get(id string) (*Agent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.agents[id]
	return a, a != nil
}

// List returns published agents ordered by creation.
func (r *Registry) List() []*Agent {
	r.mu.Lock()
	out := make([]*Agent, 0, len(r.agents))
	for _, a := range r.agents {
		if a != nil {
			out = append(out, a)
		}
	}
	r.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].created.Before(out[j].created) })
	return out
}

// Names maps agent ids to display names.
func (r *Registry) Names() map[string]string {
	m := map[string]string{}
	for _, a := range r.List() {
		m[a.ID()] = a.Spec().Name
	}
	return m
}

// ProjectDir checks a folder an agent is to work in directly (an editor's
// project, `crew acp`): an absolute, existing directory that is neither the
// file-system root, the home directory itself, nor inside Crewbase's data
// directory. It returns the cleaned path with symlinks resolved.
func (r *Registry) ProjectDir(dir string) (string, error) {
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, strings.TrimPrefix(dir, "~"))
		}
	}
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("project folder %q is not an absolute path", dir)
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(dir))
	if err != nil {
		return "", fmt.Errorf("project folder: %w", err)
	}
	st, err := os.Stat(real)
	if err != nil || !st.IsDir() {
		return "", fmt.Errorf("project folder %q is not a directory", dir)
	}
	if filepath.Dir(real) == real {
		return "", errors.New("the file-system root cannot be a project folder")
	}
	if home, err := os.UserHomeDir(); err == nil {
		if h, err := filepath.EvalSymlinks(home); err == nil && h == real {
			return "", errors.New("the home folder itself cannot be a project folder; open a project inside it")
		}
	}
	if root, err := filepath.EvalSymlinks(r.root); err == nil {
		if rel, err := filepath.Rel(root, real); err == nil && (rel == "." || !strings.HasPrefix(rel, "..")) {
			return "", errors.New("a project folder cannot be inside Crewbase's data directory")
		}
		if rel, err := filepath.Rel(real, root); err == nil && !strings.HasPrefix(rel, "..") {
			return "", errors.New("a project folder cannot contain Crewbase's data directory")
		}
	}
	return real, nil
}

// Delete unpublishes an agent and disposes its scope. Its workspace stays
// on disk (archived as workspaces/.deleted-<id>-<ts>).
func (r *Registry) Delete(id string) error {
	r.mu.Lock()
	a := r.agents[id]
	delete(r.agents, id)
	r.mu.Unlock()
	if a == nil {
		return fmt.Errorf("agent %s not found", id)
	}
	a.Stop()
	a.ctx.Dispose()
	ws := a.ownDir()
	if a.Spec().Ephemeral {
		_ = os.RemoveAll(ws)
		_ = os.Remove(r.ephemeralPath(id))
	} else {
		// the spec is archived with the workspace (nothing loads it there)
		archive := filepath.Join(filepath.Dir(ws), fmt.Sprintf(".deleted-%s-%d", id, time.Now().Unix()))
		if os.Rename(ws, archive) == nil {
			_ = os.Rename(r.specPath(id), filepath.Join(archive, legacySpec))
		}
		_ = os.Remove(r.specPath(id))
	}
	Deleted.Emit(r.c, id)
	return nil
}

func (r *Registry) loadAll() {
	_ = os.MkdirAll(filepath.Join(r.root, specDir), 0o700)
	// Ephemeral agents left behind by a crash are swept.
	files, _ := os.ReadDir(filepath.Join(r.root, specDir))
	for _, f := range files {
		if id, ok := strings.CutSuffix(f.Name(), ephemeralSuffix); ok && validID(id) {
			_ = os.RemoveAll(filepath.Join(r.root, "workspaces", id))
			_ = os.Remove(filepath.Join(r.root, specDir, f.Name()))
		}
	}
	// every saved spec (an agent's folder may be anywhere), and folders in
	// workspaces/ that still hold a spec from older versions
	var ids []string
	seen := map[string]bool{}
	for _, f := range files {
		if id, ok := strings.CutSuffix(f.Name(), ".json"); ok && validID(id) && !strings.HasSuffix(f.Name(), ephemeralSuffix) && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	dirs, _ := os.ReadDir(filepath.Join(r.root, "workspaces"))
	for _, d := range dirs {
		if d.IsDir() && !strings.HasPrefix(d.Name(), ".") && !seen[d.Name()] {
			seen[d.Name()] = true
			ids = append(ids, d.Name())
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		s, ok := r.loadSpec(id)
		if !ok {
			continue
		}
		if ms := dropRetired(s.Models); ms != nil {
			s.Models = ms
			_ = r.persist(s)
		}
		// a host grant from when grants ran 24h, still running: it lasts
		// until the mode is changed now (one that ran out stays out)
		if s.Mode == ModeHost && !s.ModeUntil.IsZero() && time.Now().Before(s.ModeUntil) {
			s.ModeUntil = time.Time{}
			_ = r.persist(s)
		}
		r.mu.Lock()
		r.agents[s.ID] = nil
		r.mu.Unlock()
		if _, err := r.publish(s); err != nil {
			r.c.Log().Warn("restore agent failed", "agent", s.ID, "err", err)
		}
	}
}

// validID reports whether id names a single path element.
// AvatarVendor is the Spec.Avatar style that shows a model vendor's mark.
const AvatarVendor = "vendor"

// ValidAvatar checks an avatar style and vendor key: the style is "" or
// AvatarVendor; the key is "" (follow the model) or a short lower-case
// icon key such as "deepseek".
func ValidAvatar(avatar, vendor string) error {
	if avatar != "" && avatar != AvatarVendor {
		return fmt.Errorf("unknown avatar style %q", avatar)
	}
	if len(vendor) > 32 {
		return errors.New("vendor key is too long")
	}
	for i, c := range vendor {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' && i > 0) {
			return fmt.Errorf("bad vendor key %q", vendor)
		}
	}
	return nil
}

func validID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, `/\`)
}

// loadSpec reads the spec of the agent whose workspace is
// workspaces/<id>, migrating one kept in the workspace by older versions.
func (r *Registry) loadSpec(id string) (Spec, bool) {
	var s Spec
	if b, err := os.ReadFile(r.specPath(id)); err == nil {
		if json.Unmarshal(b, &s) != nil || s.ID != id {
			r.c.Log().Warn("agent spec unreadable", "agent", id)
			return Spec{}, false
		}
		s.Ephemeral = false // ephemeral agents are never persisted
		return s, true
	}
	ws := filepath.Join(r.root, "workspaces", id)
	legacy := filepath.Join(ws, legacySpec)
	b, err := os.ReadFile(legacy)
	if err != nil {
		// a sub-agent workspace left by an older version's crash
		if _, err := os.Stat(filepath.Join(ws, legacyEphemeral)); err == nil {
			_ = os.RemoveAll(ws)
		}
		return Spec{}, false
	}
	if json.Unmarshal(b, &s) != nil || s.ID == "" {
		return Spec{}, false
	}
	s = r.distrustLegacy(id, s)
	if err := r.persist(s); err != nil {
		r.c.Log().Warn("agent spec migration failed", "agent", id, "err", err)
		return s, true // keep the old file; try again next start
	}
	_ = os.Remove(legacy)
	_ = os.Remove(filepath.Join(ws, legacyEphemeral)) // stray marker of a persisted agent
	return s, true
}

// distrustLegacy resets what an agent could have granted itself by editing
// a spec kept in its own (writable) workspace: host mode (only a person
// grants it, through Update), a project folder outside the checks
// of ProjectDir, the ephemeral flag (lets SharedWorkspace skip them) and
// its id.
func (r *Registry) distrustLegacy(id string, s Spec) Spec {
	s.ID = id
	s.Ephemeral, s.Parent = false, ""
	if s.Mode == ModeHost {
		r.c.Log().Warn("agent spec migrated: host mode reset to sandbox, grant it again if needed", "agent", id)
	}
	s.ModeUntil = time.Time{}
	if s.Mode != ModeReadOnly {
		s.Mode = ModeSandbox
	}
	if s.SharedWorkspace != "" {
		dir, err := r.ProjectDir(s.SharedWorkspace)
		if err != nil {
			r.c.Log().Warn("agent spec migrated: project folder dropped", "agent", id, "err", err)
			dir = ""
		}
		s.SharedWorkspace = dir
	}
	return s
}

// Config of the agents plugin.
type Config struct {
	Root          string   `json:"root"`          // data root (~/.crewbase)
	DefaultModels []string `json:"defaultModels"` // provider/model[@effort]
}

// Plugin provides the `agents` service.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/agents",
	Inject:      []string{session.ServiceKey, prompt.ServiceKey},
	Description: "Agent registry: workspaces, instructions, memory, queues, agent scopes.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		if cfg.Root == "" {
			cfg.Root, err = os.MkdirTemp("", "crewbase-")
			if err != nil {
				return err
			}
		}
		if err := os.MkdirAll(filepath.Join(cfg.Root, "workspaces"), 0o755); err != nil {
			return err
		}
		r := &Registry{c: c, root: cfg.Root, sessions: kernel.Use[*session.Store](c, session.ServiceKey), agents: map[string]*Agent{}}
		r.loadWorkspaceBase()
		for _, s := range cfg.DefaultModels {
			ref, err := llm.ParseRef(s)
			if err != nil {
				return err
			}
			r.defaults = append(r.defaults, ref)
		}
		if ms := dropRetired(r.defaults); ms != nil {
			r.defaults = ms
		}
		// team-scoped tools (a team's shared MCP servers) need the agent's team
		if tr, ok := kernel.Lookup[*tools.Registry](c, tools.ServiceKey); ok {
			tr.SetTeamOf(func(id string) string {
				if a, ok := r.Get(id); ok {
					return a.Spec().TeamID()
				}
				return ""
			})
			// an agent's connector list limits the connector tools it sees
			tr.SetConnectorsOf(func(id string) ([]string, bool) {
				if a, ok := r.Get(id); ok {
					return a.Spec().ConnectorList()
				}
				return nil, false
			})
			Updated.Listen(c, func(Spec) { tr.Touch() })
		}
		// Instructions and memory are prompt fragments resolved per agent.
		pa := kernel.Use[*prompt.Assembler](c, prompt.ServiceKey)
		pa.Register(c, &prompt.Fragment{Name: "agent-identity", Order: 10, Render: func(id string) string {
			a, ok := r.Get(id)
			if !ok {
				return ""
			}
			s := a.Spec()
			if s.Kind == KindChat {
				return a.whereLine() // no persona, no instructions
			}
			return fmt.Sprintf("Your name is %s (agent id %s). %s\n\n%s", s.Name, s.ID, a.whereLine(), a.Instructions())
		}})
		pa.Register(c, &prompt.Fragment{Name: "agent-memory", Order: 20, Render: func(id string) string {
			a, ok := r.Get(id)
			if !ok || a.IsChat() {
				return ""
			}
			m := strings.TrimSpace(a.Memory())
			if m == "" || m == "# Memory" {
				return ""
			}
			return "Long-term memory (MEMORY.md):\n" + MemoryForPrompt(m)
		}})
		llm.AdaptersChanged.Listen(c, func(struct{}) {
			r.mu.Lock()
			r.fallback = nil
			r.mu.Unlock()
			go r.adoptDefaults()
		})
		if err := c.Provide(ServiceKey, r); err != nil {
			return err
		}
		r.loadAll()
		go r.adoptDefaults() // providers registered before this plugin loaded
		return nil
	},
})
