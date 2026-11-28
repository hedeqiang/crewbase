// Package agent provides the agent registry: persistent agents with their own
// workspace (`workspaces/<id>/`), INSTRUCTIONS.md, MEMORY.md, model order,
// run mode, agent scope (kernel.Context) and a resumable FIFO input queue.
package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
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
	ID string `json:"id"`
	// IDHint, when set at creation, names the id instead of the name (a
	// template's key); not stored.
	IDHint       string         `json:"-"`
	Name         string         `json:"name"`
	Role         string         `json:"role,omitempty"` // one-line responsibility
	Instructions string         `json:"instructions,omitempty"`
	Models       []llm.ModelRef `json:"models"`
	Mode         string         `json:"mode"`
	// ModeUntil: a host-mode grant from before grants stopped expiring
	// (they ran 24h). A past one still reads as sandbox; a grant made now
	// has none and lasts until the mode is changed.
	ModeUntil time.Time `json:"modeUntil,omitzero"`
	Color     string    `json:"color,omitempty"`
	Shape     string    `json:"shape,omitempty"`
	Face      string    `json:"face,omitempty"` // avatar face (web: FACES)
	// Avatar is the avatar style: "" a crew character (Color, Shape,
	// Face) or AvatarVendor, a model vendor's mark on a neutral tile.
	Avatar string `json:"avatar,omitempty"`
	// Vendor is the mark a vendor avatar shows: "" follows the model in
	// use (fallbacks included), else an icon key (web: ProviderMark).
	// Color, Shape and Face are kept for when the style goes back.
	Vendor string `json:"vendor,omitempty"`
	Folder string `json:"folder,omitempty"`
	// Team the agent belongs to ("" = the default team, DefaultTeam).
	Team string `json:"team,omitempty"`
	// Owner is the person who created a blank chat ("" = the host's owner):
	// chats are personal, only their owner sees them.
	Owner   string    `json:"owner,omitempty"`
	Host    string    `json:"host,omitempty"`
	Created time.Time `json:"created"`
	// Session is the agent's direct-conversation session.
	Session string `json:"session"`
	// NativeTools enables provider-hosted tools (llm.NativeWebSearch,
	// llm.NativeReadURL) when the agent's model supports them.
	NativeTools []string `json:"nativeTools,omitempty"`
	// Connectors limits which connectors (MCP servers, built-in connectors
	// such as Gmail; by name) the agent may use. nil = every connector its
	// scope allows; a list = only those (empty = none). Built-in tools are
	// never limited by it.
	Connectors *[]string `json:"connectors,omitempty"`
	// ToolSearch overrides when the agent's deferred tools are held back
	// for tool search: "auto" (default, the loop's thresholds), "always"
	// or "never" (send every tool every step).
	ToolSearch string `json:"toolSearch,omitempty"`
	// AlwaysLoad lists deferred tools (by name) or tool groups (an MCP
	// server, "browser", "computer", …) this agent always has loaded.
	AlwaysLoad []string `json:"alwaysLoad,omitempty"`
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
	// Home is the agent's own folder when it is not <root>/workspaces/<id>
	// (it was created while Settings › 通用 › 默认工作区 pointed elsewhere).
	Home string `json:"home,omitempty"`
	// Kind is "" for an agent, KindChat for a blank chat: it works like an
	// agent (tools, run modes, approvals, workspace) but its prompt carries
	// no persona, instructions, memory or plugin fragments — only the base
	// working rules. Chats are personal: they do not join channels,
	// workflows or delegation.
	Kind string `json:"kind,omitempty"`
}

// KindChat marks a blank chat (Spec.Kind).
const KindChat = "chat"

// ChatDefaultName is a chat's name until its first message names it.
const ChatDefaultName = "新对话"

// IsChat reports whether the agent is a blank chat.
func (a *Agent) IsChat() bool { return a.Spec().Kind == KindChat }

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
	Agent   string        `json:"agent,omitempty"` // set by Enqueue
	Session string        `json:"session"`
	Text    string        `json:"text"`
	From    string        `json:"from,omitempty"`
	Model   *llm.ModelRef `json:"model,omitempty"` // composer override for this input
	// Attachments are files already saved in the agent workspace.
	Attachments []llm.Attachment `json:"attachments,omitempty"`
	// Reply quotes the message this one answers (回复).
	Reply *session.Quote `json:"reply,omitempty"`
	// Use: skills and tool families picked with / for this message.
	Use   []session.Invoke `json:"use,omitempty"`
	Front bool             `json:"front,omitempty"`
	// Queue: wait for the turn running now to end (the person chose 排队),
	// instead of joining it at its next step (插话, the default).
	Queue bool `json:"queue,omitempty"`
	// Mode tightens the run mode for this input only (e.g. routines run
	// read-only). It can never widen the agent's mode.
	Mode string `json:"mode,omitempty"`
	// Tag lets the producer recognise its own turns (e.g. "routine:<id>").
	Tag    string    `json:"tag,omitempty"`
	Queued time.Time `json:"queued"`
	// Resumed marks an input whose turn was cut off by a restart and is
	// picked up again (its message is already in the log).
	Resumed bool `json:"resumed,omitempty"`
	// Logged: the message was recorded when it was posted (one channel
	// message given to several agents is recorded once).
	Logged bool `json:"logged,omitempty"`
}

// background-work result tags: nobody waits for their turn end
var resultTags = []string{"job:", "delegation:", "followup:", "approval:"}

func plain(in Input) bool {
	if in.Tag == "" {
		return true
	}
	for _, p := range resultTags {
		if strings.HasPrefix(in.Tag, p) {
			return true
		}
	}
	return false
}

// resumable reports whether an input cut off by a restart is picked up
// again: conversations (people, chat, follow-ups, job reports) are;
// workflows, routines and goals are not (they record the interruption
// themselves, and goals need re-authorisation after a restart).
func resumable(in Input) bool {
	if in.Tag == "" {
		return true
	}
	for _, p := range []string{"im:", "followup:", "job:", "delegation:"} {
		if strings.HasPrefix(in.Tag, p) {
			return true
		}
	}
	return false
}

// Agent is a live agent.
type Agent struct {
	reg *Registry
	ctx *kernel.Context // agent scope

	// id and created never change (Update keeps them), so they are read
	// without mu; spec is replaced as a whole by Update
	id      string
	created time.Time

	mu            sync.Mutex
	spec          Spec
	status        Status
	queue         []Input
	paused        bool
	wake          chan struct{}
	statusSession string
	stopReq       string // stop requested for a claimed input not yet started
	stopCur       func()
	curInput      string
	curSession    string // the session of curInput
	detail        string
}

func (a *Agent) ID() string { return a.id }

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

func (a *Agent) ownDir() string {
	if a.spec.Home != "" {
		return a.spec.Home
	}
	return filepath.Join(a.reg.root, "workspaces", a.id)
}

func (a *Agent) whereLine() string {
	if p := a.ProjectDir(); p != "" {
		return "You work in the project folder " + p + " (the user chose it): relative paths and commands start there."
	}
	return "Workspace directory: " + a.Workspace()
}

// DefaultTeam holds agents and channels with no team (everything created
// before teams existed).
const DefaultTeam = "default"

// TeamOf normalizes a team id ("" = DefaultTeam).
func TeamOf(id string) string {
	if id == "" {
		return DefaultTeam
	}
	return id
}

// TeamID is the agent's team.
func (s Spec) TeamID() string { return TeamOf(s.Team) }

// ConnectorList is the agent's connector list; ok=false means no limit
// (Connectors unset).
func (s Spec) ConnectorList() (names []string, ok bool) {
	if s.Connectors == nil {
		return nil, false
	}
	return slices.Clone(*s.Connectors), true
}

// Home holds the agent's own files (INSTRUCTIONS.md, MEMORY.md, .memory/):
// its own directory, also when it works in a project folder, so they never
// land in someone's repository. A sub-agent sharing its parent's workspace
// reads the parent's (as before).
func (a *Agent) Home() string {
	if a.spec.Ephemeral {
		return a.Workspace()
	}
	return a.ownDir()
}

// ProjectDir is the project folder the agent works in, "" for its own
// workspace.
func (a *Agent) ProjectDir() string {
	if a.spec.Ephemeral {
		return ""
	}
	return a.spec.SharedWorkspace
}

// SetProjectDir points the agent at a project folder ("" = back to its own
// workspace), checked as Registry.ProjectDir does.
func (a *Agent) SetProjectDir(dir string) error {
	if a.Spec().Ephemeral {
		return errors.New("a sub-agent works where its parent does")
	}
	if dir != "" {
		d, err := a.reg.ProjectDir(dir)
		if err != nil {
			return err
		}
		dir = d
	}
	return a.Update(func(s *Spec) { s.SharedWorkspace = dir })
}

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
	b, err := os.ReadFile(filepath.Join(a.Home(), "INSTRUCTIONS.md"))
	if err != nil {
		return a.Spec().Instructions
	}
	return string(b)
}

// Memory budget in the system prompt (runes): every request carries it,
// so it stays bounded however long MEMORY.md grows. The beginning (facts
// written first, usually the lasting ones) and the most recent entries are
// kept; the middle is left to memory_search.
const (
	MemoryHead = 2500
	MemoryTail = 5500
)

// MemoryForPrompt bounds m for the system prompt, cutting at line breaks.
func MemoryForPrompt(m string) string {
	r := []rune(m)
	if len(r) <= MemoryHead+MemoryTail+200 {
		return m
	}
	head := string(r[:MemoryHead])
	if i := strings.LastIndex(head, "\n"); i > len(head)/2 {
		head = head[:i]
	}
	tail := string(r[len(r)-MemoryTail:])
	if i := strings.Index(tail, "\n"); i >= 0 && i < len(tail)/2 {
		tail = tail[i+1:]
	}
	omitted := len(r) - len([]rune(head)) - len([]rune(tail))
	return head + fmt.Sprintf("\n\n[… %d characters of older memory not shown here; memory_search finds them …]\n\n", omitted) + tail
}

// Memory reads MEMORY.md.
func (a *Agent) Memory() string {
	b, _ := os.ReadFile(filepath.Join(a.Home(), "MEMORY.md"))
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
func (a *Agent) SetStatus(s Status, detail string) { a.SetStatusIn(s, detail, "") }

// SetStatusIn updates status, recording the session it concerns.
func (a *Agent) SetStatusIn(s Status, detail, sessionID string) {
	a.mu.Lock()
	changed := a.status != s || a.detail != detail || a.statusSession != sessionID
	a.status, a.detail, a.statusSession = s, detail, sessionID
	a.mu.Unlock()
	if changed {
		StatusChanged.Emit(a.ctx, StatusEvent{Agent: a.id, Status: s, Detail: detail, Session: sessionID})
	}
}

// StatusSession returns the session the current status concerns.
func (a *Agent) StatusSession() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.statusSession
}

// Update changes the spec and persists it.
func (a *Agent) Update(fn func(*Spec)) error {
	a.mu.Lock()
	s := a.spec
	fn(&s)
	s.ID, s.Session, s.Created, s.Kind = a.id, a.spec.Session, a.spec.Created, a.spec.Kind
	if s.Kind == KindChat {
		s.Role = "" // a chat stays a chat
	}
	// host mode lasts until it is changed: no time is stamped on a grant
	// (runmode.Set and the API clear any for a new one); an old grant's
	// time is kept, so one that ran out stays out
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
	if !slices.Contains(ss.Meta().Agents, a.id) {
		return fmt.Errorf("session %s does not belong to agent %s", sessionID, a.id)
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
	_, err := sess.Append(session.TypeInject, a.id, session.InjectData{Text: text, Source: source})
	return err
}
