// Package agent provides the agent registry: persistent agents with their own
// workspace (`workspaces/<id>/`), INSTRUCTIONS.md, MEMORY.md, model order,
// run mode, agent scope (kernel.Context) and a resumable FIFO input queue.
package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/kernel"
)

const ServiceKey = "agents"

// Run mode presets (docs/06 §2).
const (
	ModeReadOnly = "read-only"
	ModeSandbox  = "sandbox"
	ModeHost     = "host"
)

// Spec is the persisted definition of an agent.
type Spec struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Role         string         `json:"role,omitempty"` // one-line responsibility
	Instructions string         `json:"instructions,omitempty"`
	Models       []llm.ModelRef `json:"models"`
	Mode         string         `json:"mode"`
	// ModeUntil bounds host mode (auto-revert to sandbox).
	ModeUntil time.Time `json:"modeUntil,omitzero"`
	Color     string    `json:"color,omitempty"`
	Shape     string    `json:"shape,omitempty"`
	Folder    string    `json:"folder,omitempty"`
	Host      string    `json:"host,omitempty"`
	Created   time.Time `json:"created"`
	// Session is the agent's direct-conversation session.
	Session string `json:"session"`
	// NativeTools enables provider-hosted tools (llm.NativeWebSearch,
	// llm.NativeReadURL) when the agent's model supports them.
	NativeTools []string `json:"nativeTools,omitempty"`
	// Ephemeral agents (subagents) are never persisted; their workspace is
	// removed on delete and leftovers are swept at startup.
	Ephemeral bool `json:"ephemeral,omitempty"`
	// Parent is the agent that spawned an ephemeral agent (lineage data).
	Parent string `json:"parent,omitempty"`
	// SharedWorkspace makes an ephemeral agent work in another agent's
	// workspace (e.g. a CLI handed one turn of its parent's coding task).
	// Its own directory still holds its bookkeeping and is what gets
	// removed on delete; the shared workspace is never deleted.
	SharedWorkspace string `json:"sharedWorkspace,omitempty"`
}

// Status of an agent (docs/03 §7).
type Status string

const (
	StatusIdle    Status = "idle"
	StatusRunning Status = "running"
	StatusWaiting Status = "waiting"
	StatusPaused  Status = "paused"
	StatusFailed  Status = "failed"
)

// Input is one queued unit of work.
type Input struct {
	ID      string        `json:"id"`
	Session string        `json:"session"`
	Text    string        `json:"text"`
	From    string        `json:"from,omitempty"`
	Model   *llm.ModelRef `json:"model,omitempty"` // composer override for this input
	Front   bool          `json:"front,omitempty"`
	// Mode tightens the run mode for this input only (e.g. routines run
	// read-only). It can never widen the agent's mode.
	Mode string `json:"mode,omitempty"`
	// Tag lets the producer recognise its own turns (e.g. "routine:<id>").
	Tag    string    `json:"tag,omitempty"`
	Queued time.Time `json:"queued"`
}

// Agent is a live agent.
type Agent struct {
	reg *Registry
	ctx *kernel.Context // agent scope

	mu       sync.Mutex
	spec     Spec
	status   Status
	queue    []Input
	paused   bool
	wake     chan struct{}
	stopCur  func()
	curInput string
	detail   string
}

func (a *Agent) ID() string { return a.spec.ID }

// Spec returns a copy of the agent definition.
func (a *Agent) Spec() Spec {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.spec
	s.Models = append([]llm.ModelRef(nil), s.Models...)
	return s
}

// Ctx returns the agent-scope context. Registrations on it shadow globals
// for this agent only and are undone when the agent is deleted.
func (a *Agent) Ctx() *kernel.Context { return a.ctx }

// Workspace is the agent's working directory.
func (a *Agent) Workspace() string {
	if a.spec.SharedWorkspace != "" {
		return a.spec.SharedWorkspace
	}
	return a.ownDir()
}

func (a *Agent) ownDir() string { return filepath.Join(a.reg.root, "workspaces", a.spec.ID) }

// Mode returns the effective run mode (host mode expires to sandbox).
func (a *Agent) Mode() string {
	s := a.Spec()
	if s.Mode == ModeHost && !s.ModeUntil.IsZero() && time.Now().After(s.ModeUntil) {
		return ModeSandbox
	}
	if s.Mode == "" {
		return ModeSandbox
	}
	return s.Mode
}

// Instructions reads INSTRUCTIONS.md (the file is the source of truth).
func (a *Agent) Instructions() string {
	b, err := os.ReadFile(filepath.Join(a.Workspace(), "INSTRUCTIONS.md"))
	if err != nil {
		return a.Spec().Instructions
	}
	return string(b)
}

// Memory reads MEMORY.md.
func (a *Agent) Memory() string {
	b, _ := os.ReadFile(filepath.Join(a.Workspace(), "MEMORY.md"))
	return string(b)
}

// Session returns the agent's direct session.
func (a *Agent) Session() (*session.Session, error) {
	return a.reg.sessions.Get(a.Spec().Session)
}

// Status returns the current status and an optional detail.
func (a *Agent) Status() (Status, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.paused && a.status == StatusIdle {
		return StatusPaused, a.detail
	}
	return a.status, a.detail
}

// SetStatus updates status and notifies listeners.
func (a *Agent) SetStatus(s Status, detail string) {
	a.mu.Lock()
	changed := a.status != s || a.detail != detail
	a.status, a.detail = s, detail
	a.mu.Unlock()
	if changed {
		StatusChanged.Emit(a.ctx, StatusEvent{Agent: a.spec.ID, Status: s, Detail: detail})
	}
}

// Update changes the spec and persists it.
func (a *Agent) Update(fn func(*Spec)) error {
	a.mu.Lock()
	s := a.spec
	fn(&s)
	s.ID, s.Session, s.Created = a.spec.ID, a.spec.Session, a.spec.Created
	wasHost := a.spec.Mode == ModeHost && (a.spec.ModeUntil.IsZero() || time.Now().Before(a.spec.ModeUntil))
	if s.Mode == ModeHost && !wasHost {
		s.ModeUntil = time.Time{} // entering host mode restarts the 24h clock
	}
	if s.Mode == ModeHost && s.ModeUntil.IsZero() {
		s.ModeUntil = time.Now().Add(24 * time.Hour)
	}
	if s.Mode != ModeHost {
		s.ModeUntil = time.Time{}
	}
	a.spec = s
	a.mu.Unlock()
	if err := a.reg.persist(s); err != nil {
		return err
	}
	Updated.Emit(a.ctx, s)
	return nil
}

// Stricter returns the more restrictive of two run modes ("" = no limit).
func Stricter(a, b string) string {
	rank := map[string]int{ModeReadOnly: 0, ModeSandbox: 1, ModeHost: 2, "": 3}
	if rank[b] < rank[a] {
		return b
	}
	return a
}

// SetSession makes sessionID the agent's current direct conversation
// ("new conversation": same agent, workspace, memory and queue).
func (a *Agent) SetSession(sessionID string) error {
	ss, err := a.reg.sessions.Get(sessionID)
	if err != nil {
		return err
	}
	if !slices.Contains(ss.Meta().Agents, a.spec.ID) {
		return fmt.Errorf("session %s does not belong to agent %s", sessionID, a.spec.ID)
	}
	a.mu.Lock()
	a.spec.Session = sessionID
	s := a.spec
	a.mu.Unlock()
	if err := a.reg.persist(s); err != nil {
		return err
	}
	Updated.Emit(a.ctx, s)
	return nil
}

// Inject adds text to the agent's next model request in sess. It is recorded
// as a session event, never slipped into a request unlogged.
func (a *Agent) Inject(sess *session.Session, text, source string) error {
	_, err := sess.Append(session.TypeInject, a.spec.ID, session.InjectData{Text: text, Source: source})
	return err
}

// ---------------------------------------------------------------- queue

// Enqueue records and queues an input. Front=true jumps the queue.
func (a *Agent) Enqueue(in Input) (Input, error) {
	if in.ID == "" {
		in.ID = session.NewID("in")
	}
	if in.Session == "" {
		in.Session = a.Spec().Session
	}
	in.Queued = time.Now().UTC()
	ss, err := a.reg.sessions.Get(in.Session)
	if err != nil {
		return in, err
	}
	if _, err := ss.Append(session.TypeInputQueued, a.spec.ID, in); err != nil {
		return in, err
	}
	a.mu.Lock()
	if in.Front {
		a.queue = append([]Input{in}, a.queue...)
	} else {
		a.queue = append(a.queue, in)
	}
	a.mu.Unlock()
	a.signal()
	QueueChanged.Emit(a.ctx, a.spec.ID)
	return in, nil
}

// Cancel removes a queued input.
func (a *Agent) Cancel(inputID string) bool {
	a.mu.Lock()
	var found *Input
	for i, x := range a.queue {
		if x.ID == inputID {
			found = &x
			a.queue = append(a.queue[:i:i], a.queue[i+1:]...)
			break
		}
	}
	a.mu.Unlock()
	if found == nil {
		return false
	}
	if ss, err := a.reg.sessions.Get(found.Session); err == nil {
		_, _ = ss.Append(session.TypeInputCancelled, a.spec.ID, map[string]string{"id": inputID})
	}
	QueueChanged.Emit(a.ctx, a.spec.ID)
	return true
}

// Pause stops claiming new inputs (the current turn continues).
func (a *Agent) Pause() {
	a.mu.Lock()
	a.paused = true
	a.mu.Unlock()
	if ss, err := a.Session(); err == nil {
		_, _ = ss.Append(session.TypeQueuePaused, a.spec.ID, nil)
	}
	QueueChanged.Emit(a.ctx, a.spec.ID)
}

// Resume resumes claiming inputs.
func (a *Agent) Resume() {
	a.mu.Lock()
	a.paused = false
	a.mu.Unlock()
	if ss, err := a.Session(); err == nil {
		_, _ = ss.Append(session.TypeQueueResumed, a.spec.ID, nil)
	}
	a.signal()
	QueueChanged.Emit(a.ctx, a.spec.ID)
}

// Paused reports whether the queue is paused.
func (a *Agent) Paused() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.paused
}

// Queue returns a snapshot of waiting inputs.
func (a *Agent) Queue() []Input {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Input(nil), a.queue...)
}

// Claim pops the next input unless paused. The claim is recorded.
func (a *Agent) Claim() (Input, bool) {
	a.mu.Lock()
	if a.paused || len(a.queue) == 0 {
		a.mu.Unlock()
		return Input{}, false
	}
	in := a.queue[0]
	a.queue = a.queue[1:]
	a.mu.Unlock()
	if ss, err := a.reg.sessions.Get(in.Session); err == nil {
		_, _ = ss.Append(session.TypeInputClaimed, a.spec.ID, map[string]string{"id": in.ID})
	}
	QueueChanged.Emit(a.ctx, a.spec.ID)
	return in, true
}

// Wake returns a channel signalled when inputs arrive or the queue resumes.
func (a *Agent) Wake() <-chan struct{} { return a.wake }

func (a *Agent) signal() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// SetStopper installs the cancel func of the running turn.
func (a *Agent) SetStopper(stop func()) { a.SetCurrent("", stop) }

// SetCurrent records the input being worked on and its cancel func.
func (a *Agent) SetCurrent(inputID string, stop func()) {
	a.mu.Lock()
	a.stopCur, a.curInput = stop, inputID
	a.mu.Unlock()
}

// CurrentInput returns the id of the input being worked on ("" = idle).
func (a *Agent) CurrentInput() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.curInput
}

// StopInput cancels the turn working on inputID, or removes it from the
// queue if it has not started. It never touches another input's turn.
func (a *Agent) StopInput(inputID string) bool {
	a.mu.Lock()
	stop := a.stopCur
	cur := a.curInput == inputID && inputID != ""
	a.mu.Unlock()
	if cur && stop != nil {
		stop()
		return true
	}
	return a.Cancel(inputID)
}

// Stop cancels the running turn (the queue and workspace are kept).
func (a *Agent) Stop() bool {
	a.mu.Lock()
	stop := a.stopCur
	a.mu.Unlock()
	if stop == nil {
		return false
	}
	stop()
	return true
}

// ---------------------------------------------------------------- registry

// StatusEvent reports an agent status change.
type StatusEvent struct {
	Agent  string `json:"agent"`
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
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
	sessions *session.Store
	defaults []llm.ModelRef

	mu     sync.Mutex
	agents map[string]*Agent
}

var idRe = regexp.MustCompile(`[^a-z0-9-]+`)

// Slug makes an id from a name.
func Slug(name string) string {
	s := idRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "agent"
	}
	return s
}

// Create makes, persists and publishes an agent.
func (r *Registry) Create(spec Spec) (*Agent, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return nil, errors.New("agent name is required")
	}
	if spec.Mode == ModeHost {
		return nil, errors.New("host mode cannot be enabled at creation")
	}
	if spec.Mode == "" {
		spec.Mode = ModeSandbox
	}
	if len(spec.Models) == 0 {
		spec.Models = r.DefaultModels()
	}
	r.mu.Lock()
	if spec.ID == "" {
		base := Slug(spec.Name)
		spec.ID = base
		for i := 2; r.agents[spec.ID] != nil; i++ {
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
	if err := os.MkdirAll(ws, 0o755); err != nil {
		r.unreserve(spec.ID)
		return nil, err
	}
	instr := spec.Instructions
	if instr == "" {
		instr = "# " + spec.Name + "\n\n" + spec.Role + "\n"
	}
	writeIfMissing(filepath.Join(ws, "INSTRUCTIONS.md"), instr)
	writeIfMissing(filepath.Join(ws, "MEMORY.md"), "# Memory\n\n")
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
	a := &Agent{reg: r, spec: spec, status: StatusIdle, wake: make(chan struct{}, 1)}
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
			if x == a.spec.ID {
				ids[m.ID] = true
			}
		}
	}
	var pending []Input
	for id := range ids {
		ss, err := r.sessions.Get(id)
		if err != nil {
			continue
		}
		open := map[string]Input{}
		var order []string
		for _, e := range ss.Events() {
			if e.Agent != a.spec.ID {
				continue
			}
			switch e.Type {
			case session.TypeInputQueued:
				var in Input
				if e.Decode(&in) == nil {
					open[in.ID] = in
					order = append(order, in.ID)
				}
			case session.TypeInputClaimed, session.TypeInputCancelled:
				var m map[string]string
				if e.Decode(&m) == nil {
					delete(open, m["id"])
				}
			case session.TypeQueuePaused:
				a.paused = true
			case session.TypeQueueResumed:
				a.paused = false
			}
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

const ephemeralMarker = ".ephemeral"

func (r *Registry) persist(s Spec) error {
	if s.Ephemeral {
		return os.WriteFile(filepath.Join(r.root, "workspaces", s.ID, ephemeralMarker), []byte(s.Parent), 0o644)
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(filepath.Join(r.root, "workspaces", s.ID, "agent.json"), b, 0o644)
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
	sort.Slice(out, func(i, j int) bool { return out[i].spec.Created.Before(out[j].spec.Created) })
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
	} else {
		_ = os.Rename(ws, filepath.Join(r.root, "workspaces", fmt.Sprintf(".deleted-%s-%d", id, time.Now().Unix())))
	}
	Deleted.Emit(r.c, id)
	return nil
}

func (r *Registry) loadAll() {
	dirs, _ := os.ReadDir(filepath.Join(r.root, "workspaces"))
	for _, d := range dirs {
		if !d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			continue
		}
		// Ephemeral agents left behind by a crash are swept.
		if _, err := os.Stat(filepath.Join(r.root, "workspaces", d.Name(), ephemeralMarker)); err == nil {
			_ = os.RemoveAll(filepath.Join(r.root, "workspaces", d.Name()))
			continue
		}
		b, err := os.ReadFile(filepath.Join(r.root, "workspaces", d.Name(), "agent.json"))
		if err != nil {
			continue
		}
		var s Spec
		if json.Unmarshal(b, &s) != nil || s.ID == "" {
			continue
		}
		r.mu.Lock()
		r.agents[s.ID] = nil
		r.mu.Unlock()
		if _, err := r.publish(s); err != nil {
			r.c.Log().Warn("restore agent failed", "agent", s.ID, "err", err)
		}
	}
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
		for _, s := range cfg.DefaultModels {
			ref, err := llm.ParseRef(s)
			if err != nil {
				return err
			}
			r.defaults = append(r.defaults, ref)
		}
		// Instructions and memory are prompt fragments resolved per agent.
		pa := kernel.Use[*prompt.Assembler](c, prompt.ServiceKey)
		pa.Register(c, &prompt.Fragment{Name: "agent-identity", Order: 10, Render: func(id string) string {
			a, ok := r.Get(id)
			if !ok {
				return ""
			}
			s := a.Spec()
			return fmt.Sprintf("Your name is %s (agent id %s). Workspace directory: %s\n\n%s", s.Name, s.ID, a.Workspace(), a.Instructions())
		}})
		pa.Register(c, &prompt.Fragment{Name: "agent-memory", Order: 20, Render: func(id string) string {
			a, ok := r.Get(id)
			if !ok {
				return ""
			}
			m := strings.TrimSpace(a.Memory())
			if m == "" || m == "# Memory" {
				return ""
			}
			return "Long-term memory (MEMORY.md):\n" + m
		}})
		if err := c.Provide(ServiceKey, r); err != nil {
			return err
		}
		r.loadAll()
		return nil
	},
})

// DefaultModels returns the model order given to new agents.
func (r *Registry) DefaultModels() []llm.ModelRef {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]llm.ModelRef(nil), r.defaults...)
}

// SetDefaultModels changes the model order given to new agents.
func (r *Registry) SetDefaultModels(ms []llm.ModelRef) {
	r.mu.Lock()
	r.defaults = append([]llm.ModelRef(nil), ms...)
	r.mu.Unlock()
}

// Root returns the data root.
func (r *Registry) Root() string { return r.root }

// Sessions returns the session store.
func (r *Registry) Sessions() *session.Store { return r.sessions }
