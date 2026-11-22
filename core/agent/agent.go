// Package agent provides the agent registry: persistent agents with their own
// workspace (`workspaces/<id>/`), INSTRUCTIONS.md, MEMORY.md, model order,
// run mode, agent scope (kernel.Context) and a resumable FIFO input queue.
package agent

import (
	"context"
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
	// ModeUntil bounds host mode (auto-revert to sandbox).
	ModeUntil time.Time `json:"modeUntil,omitzero"`
	Color     string    `json:"color,omitempty"`
	Shape     string    `json:"shape,omitempty"`
	Face      string    `json:"face,omitempty"` // avatar face (web: FACES)
	Folder    string    `json:"folder,omitempty"`
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
	in.Agent = a.id
	ss, err := a.reg.sessions.Get(in.Session)
	if err != nil {
		return in, err
	}
	if _, err := ss.Append(session.TypeInputQueued, a.id, in); err != nil {
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
	QueueChanged.Emit(a.ctx, a.id)
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
		_, _ = ss.Append(session.TypeInputCancelled, a.id, map[string]string{"id": inputID})
	}
	QueueChanged.Emit(a.ctx, a.id)
	return true
}

// Pause stops claiming new inputs (the current turn continues).
func (a *Agent) Pause() {
	a.mu.Lock()
	a.paused = true
	a.mu.Unlock()
	if ss, err := a.Session(); err == nil {
		_, _ = ss.Append(session.TypeQueuePaused, a.id, nil)
	}
	QueueChanged.Emit(a.ctx, a.id)
}

// Resume resumes claiming inputs.
func (a *Agent) Resume() {
	a.mu.Lock()
	a.paused = false
	a.mu.Unlock()
	if ss, err := a.Session(); err == nil {
		_, _ = ss.Append(session.TypeQueueResumed, a.id, nil)
	}
	a.signal()
	QueueChanged.Emit(a.ctx, a.id)
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
	a.curInput, a.curSession, a.stopCur = in.ID, in.Session, nil
	a.mu.Unlock()
	if ss, err := a.reg.sessions.Get(in.Session); err == nil {
		_, _ = ss.Append(session.TypeInputClaimed, a.id, map[string]string{"id": in.ID})
	}
	QueueChanged.Emit(a.ctx, a.id)
	return in, true
}

// Joinable reports whether next can join the turn working on cur: both
// are plain messages (from people, or results of background work: jobs,
// delegations, follow-ups — nobody waits for the turn end of those), for
// the same conversation, run mode and model. Several results arriving
// together become one turn, and one update.
func Joinable(cur, next Input) bool {
	if next.Queue || !plain(cur) || !plain(next) || cur.Session != next.Session || cur.Mode != next.Mode {
		return false
	}
	if next.Model != nil && (cur.Model == nil || *cur.Model != *next.Model) {
		return false
	}
	return true
}

// Absorb claims the inputs at the head of the queue that can join the
// turn working on cur (Joinable), in order: several messages sent in a row
// become one turn and one reply, and a message sent mid-turn is seen at
// the next step instead of waiting for the turn to end. Nothing is taken
// while the queue is paused. The claims are recorded.
func (a *Agent) Absorb(cur Input) []Input {
	a.mu.Lock()
	if a.paused {
		a.mu.Unlock()
		return nil
	}
	n := 0
	for n < len(a.queue) && Joinable(cur, a.queue[n]) {
		n++
	}
	if n == 0 {
		a.mu.Unlock()
		return nil
	}
	got := append([]Input(nil), a.queue[:n]...)
	a.queue = a.queue[n:]
	a.mu.Unlock()
	if ss, err := a.reg.sessions.Get(cur.Session); err == nil {
		for _, in := range got {
			_, _ = ss.Append(session.TypeInputClaimed, a.id, map[string]string{"id": in.ID, "joined": cur.ID})
		}
	}
	QueueChanged.Emit(a.ctx, a.id)
	return got
}

// Steer makes queued input id join the turn running now (at its next
// step) instead of waiting for it to end; false when it is not queued.
func (a *Agent) Steer(id string) bool {
	a.mu.Lock()
	idx := -1
	for i, in := range a.queue {
		if in.ID == id {
			idx = i
		}
	}
	if idx < 0 {
		a.mu.Unlock()
		return false
	}
	in := a.queue[idx]
	in.Queue = false
	// to the head of the queue, where the running turn takes it from
	a.queue = append(a.queue[:idx], a.queue[idx+1:]...)
	a.queue = append([]Input{in}, a.queue...)
	a.mu.Unlock()
	QueueChanged.Emit(a.ctx, a.id)
	a.signal()
	return true
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

// SetCurrent records the input being worked on and its cancel func. A
// stop requested for the input between Claim and SetCurrent fires now.
func (a *Agent) SetCurrent(inputID string, stop func()) {
	a.mu.Lock()
	a.stopCur, a.curInput = stop, inputID
	if inputID == "" {
		a.curSession = ""
	}
	fire := stop != nil && inputID != "" && a.stopReq == inputID
	if fire || inputID == "" {
		a.stopReq = ""
	}
	a.mu.Unlock()
	if fire {
		stop()
	}
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
	if cur && stop == nil {
		// Claimed but the turn has not installed its stopper yet.
		a.stopReq = inputID
		a.mu.Unlock()
		return true
	}
	a.mu.Unlock()
	if cur {
		stop()
		return true
	}
	return a.Cancel(inputID)
}

// StopIn cancels the running turn only when it works in session id (an
// agent working elsewhere is left alone). A turn claimed but not started
// yet is stopped as soon as it starts.
func (a *Agent) StopIn(id string) bool {
	a.mu.Lock()
	if a.curInput == "" || a.curSession != id {
		a.mu.Unlock()
		return false
	}
	in := a.curInput
	a.mu.Unlock()
	return a.StopInput(in)
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
		r.mu.Lock()
		r.agents[s.ID] = nil
		r.mu.Unlock()
		if _, err := r.publish(s); err != nil {
			r.c.Log().Warn("restore agent failed", "agent", s.ID, "err", err)
		}
	}
}

// validID reports whether id names a single path element.
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
// grants it, for 24h, through Update), a project folder outside the checks
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

// DefaultModels returns the model order given to new agents.
func (r *Registry) DefaultModels() []llm.ModelRef {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]llm.ModelRef(nil), r.defaults...)
}

// AvailableDefaults is the model order a new agent gets now: the
// configured order with concrete models, minus providers that are gone.
func (r *Registry) AvailableDefaults() []llm.ModelRef { return r.availableDefaults() }

// availableDefaults is the default order restricted to configured
// providers; when none of them is configured (e.g. no CLI installed), the
// first model of each available provider is used instead so a new agent
// can work out of the box.
func (r *Registry) availableDefaults() []llm.ModelRef {
	defs := r.DefaultModels()
	reg, ok := kernel.Lookup[*llm.Registry](r.c, llm.ServiceKey)
	if !ok {
		return defs
	}
	r.mu.Lock()
	if r.fallback != nil && time.Since(r.fallbackAt) < 10*time.Minute {
		out := append([]llm.ModelRef(nil), r.fallback...)
		r.mu.Unlock()
		return out
	}
	r.mu.Unlock()
	out := r.computeDefaults(reg, defs)
	r.mu.Lock()
	r.fallback, r.fallbackAt = out, time.Now()
	r.mu.Unlock()
	return append([]llm.ModelRef(nil), out...)
}

func (r *Registry) computeDefaults(reg *llm.Registry, defs []llm.ModelRef) []llm.ModelRef {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var out []llm.ModelRef
	seen := map[string]bool{}
	for _, m := range defs {
		ad, ok := reg.Get(m.Provider)
		if !ok || reg.ModelOff(m) {
			continue // a provider that is gone or a switched-off model
		}
		// "default" is a placeholder for "the provider's own choice": name
		// the concrete model so people see (and the router tracks) what runs.
		if m.Model == "" || m.Model == "default" {
			if ms, err := reg.ModelsOf(ctx, ad); err == nil {
				if id := pickModel(ms); id != "" {
					m.Model = id
				}
			}
		}
		if k := m.String(); !seen[k] {
			seen[k] = true
			out = append(out, m)
		}
	}
	if len(out) > 0 {
		return out
	}
	// Nothing configured is connected: up to three strong models from what
	// is (a gateway such as magpie offers many), so a new agent has a
	// fallback when the first one is out of quota.
	type cand struct {
		ref  llm.ModelRef
		rank int
	}
	var cands []cand
	for _, ad := range reg.List() {
		if ad.ID() == "mock" {
			continue
		}
		ms, err := reg.ModelsOf(ctx, ad) // switched-off models are not enabled
		if err != nil {
			continue
		}
		best := map[int]bool{}
		for _, m := range ms {
			if !m.Enabled || m.OutputKind() != llm.KindChat {
				continue
			}
			for i, want := range preferredModels {
				if strings.Contains(m.ID, want) {
					if !best[i] { // one model per family and provider
						best[i] = true
						cands = append(cands, cand{llm.ModelRef{Provider: ad.ID(), Model: m.ID}, i})
					}
					break
				}
			}
		}
		if len(best) == 0 {
			if id := pickModel(ms); id != "" {
				cands = append(cands, cand{llm.ModelRef{Provider: ad.ID(), Model: id}, len(preferredModels)})
			}
		}
	}
	slices.SortStableFunc(cands, func(a, b cand) int { return a.rank - b.rank })
	for _, c := range cands {
		if len(out) == 3 {
			break
		}
		out = append(out, c.ref)
	}
	if len(out) == 0 {
		return defs
	}
	return out
}

// adoptDefaults moves agents that still have the built-in model order,
// none of it configured (made before any model was connected, e.g. the
// first chat of a fresh install), onto the models that are available now.
// An order someone chose is never touched; failover handles those.
func (r *Registry) adoptDefaults() {
	reg, ok := kernel.Lookup[*llm.Registry](r.c, llm.ServiceKey)
	if !ok {
		return
	}
	defs := r.DefaultModels()
	var now []llm.ModelRef
	for _, a := range r.List() {
		ms := a.Spec().Models
		if len(ms) > 0 && (!sameRefs(ms, defs) || anyConfigured(reg, ms)) {
			continue
		}
		if now == nil {
			now = r.availableDefaults()
			if !anyConfigured(reg, now) {
				return // still nothing to move to
			}
		}
		_ = a.Update(func(s *Spec) {
			if len(s.Models) == 0 || sameRefs(s.Models, defs) {
				s.Models = append([]llm.ModelRef(nil), now...)
			}
		})
	}
}

func anyConfigured(reg *llm.Registry, ms []llm.ModelRef) bool {
	for _, m := range ms {
		if _, ok := reg.Get(m.Provider); ok {
			return true
		}
	}
	return false
}

func sameRefs(a, b []llm.ModelRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Provider != b[i].Provider || a[i].Model != b[i].Model {
			return false
		}
	}
	return true
}

// preferredModels ranks well-known strong models when a provider (such as
// a gateway) offers many; the first enabled match wins.
var preferredModels = []string{"claude-opus-5-5", "gpt-6.2", "gpt-6.1", "claude-sonnet-5-5", "gpt-6", "deepseek-v4-pro", "gemini", "deepseek-flash"}

func pickModel(ms []llm.ModelInfo) string {
	// a CLI's "default" entry only stands in when it lists nothing else
	real := ms[:0:0]
	for _, m := range ms {
		if m.ID != "default" {
			real = append(real, m)
		}
	}
	if len(real) > 0 {
		ms = real
	}
	for _, want := range preferredModels {
		for _, m := range ms {
			if m.Enabled && strings.Contains(m.ID, want) {
				return m.ID
			}
		}
	}
	for _, m := range ms {
		if m.Enabled {
			return m.ID
		}
	}
	return ""
}

// SetDefaultModels changes the model order given to new agents.
func (r *Registry) SetDefaultModels(ms []llm.ModelRef) {
	r.mu.Lock()
	r.defaults = append([]llm.ModelRef(nil), ms...)
	r.fallback = nil
	r.mu.Unlock()
}

// Root returns the data root.
func (r *Registry) Root() string { return r.root }

// Sessions returns the session store.
func (r *Registry) Sessions() *session.Store { return r.sessions }

// ChatNameFrom is the name a blank chat gets from its first message, until
// a generated topic replaces it (an agent named this way still has an
// automatic name, which the topic may replace; a name the person typed
// never matches it).
func ChatNameFrom(text string) string {
	t := strings.Join(strings.Fields(text), " ")
	if r := []rune(t); len(r) > 24 {
		t = string(r[:23]) + "…"
	}
	return t
}

// renamedProviders maps providers that were replaced to their successors:
// Gemini CLI became Antigravity (agy).
// retiredPrefix marks providers Crewbase no longer has: the subscription
// coding CLIs it drove over ACP (acp-claude-code, acp-codex, …), removed in
// 0.5.12 because reusing a CLI login risks the account.
const retiredPrefix = "acp-"

// dropRetired returns ms without models of retired providers, or nil when
// nothing changed. An order left empty is filled by adoptDefaults.
func dropRetired(ms []llm.ModelRef) []llm.ModelRef {
	if !slices.ContainsFunc(ms, func(m llm.ModelRef) bool { return strings.HasPrefix(m.Provider, retiredPrefix) }) {
		return nil
	}
	return slices.DeleteFunc(slices.Clone(ms), func(m llm.ModelRef) bool { return strings.HasPrefix(m.Provider, retiredPrefix) })
}

// ---------------------------------------------------------------- 默认工作区

// workspaceFile keeps Settings › 通用 › 默认工作区 (where new agents' own
// folders go). Agents already made stay where they are.
const workspaceFile = "workspace.json"

// base is the folder new agents' own folders go in.
func (r *Registry) base() string {
	if b, _ := r.wsBase.Load().(string); b != "" {
		return b
	}
	return filepath.Join(r.root, "workspaces")
}

// WorkspaceBase reports where new agents' folders go and whether that is
// the default (<root>/workspaces).
func (r *Registry) WorkspaceBase() (dir string, isDefault bool) {
	b, _ := r.wsBase.Load().(string)
	return r.base(), b == ""
}

// DefaultWorkspaceBase is <root>/workspaces.
func (r *Registry) DefaultWorkspaceBase() string { return filepath.Join(r.root, "workspaces") }

// Why a workspace folder was refused (WorkspaceError.Code).
const (
	WsNotAbsolute = "not_absolute"
	WsTooBroad    = "too_broad"
	WsDataFolder  = "data_folder"
	WsNotWritable = "not_writable"
)

// WorkspaceError explains a refused workspace folder.
type WorkspaceError struct {
	Code string
	Dir  string
	Msg  string
}

func (e *WorkspaceError) Error() string { return e.Msg }

// SetWorkspaceBase makes dir ("" = the default) where new agents' folders
// go. dir must be an absolute folder (~ is expanded) outside the data
// root, which holds the vault and other private state; it is created when
// missing and must be writable.
func (r *Registry) SetWorkspaceBase(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if rest, ok := strings.CutPrefix(dir, "~"); ok && (rest == "" || rest[0] == '/' || rest[0] == filepath.Separator) {
		if h, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(h, rest)
		}
	}
	def := filepath.Join(r.root, "workspaces")
	if dir != "" {
		if !filepath.IsAbs(dir) {
			return "", &WorkspaceError{Code: WsNotAbsolute, Dir: dir, Msg: "the workspace folder must be a full path, e.g. ~/Crewbase"}
		}
		dir = filepath.Clean(dir)
	}
	if dir == "" || dir == def {
		dir = ""
	} else {
		if dir == "/" || dir == filepath.Clean(os.Getenv("HOME")) {
			return "", &WorkspaceError{Code: WsTooBroad, Dir: dir, Msg: "pick a folder of its own, not " + dir}
		}
		rootAbs, _ := filepath.Abs(r.root)
		if within(dir, rootAbs) || within(rootAbs, dir) {
			return "", &WorkspaceError{Code: WsDataFolder, Dir: rootAbs, Msg: "the workspace folder cannot be inside Crewbase's data folder " + rootAbs + ", nor contain it"}
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", &WorkspaceError{Code: WsNotWritable, Dir: dir, Msg: fmt.Sprintf("cannot create %s: %v", dir, err)}
		}
		probe, err := os.CreateTemp(dir, ".crewbase-write-*")
		if err != nil {
			return "", &WorkspaceError{Code: WsNotWritable, Dir: dir, Msg: fmt.Sprintf("cannot write in %s: %v", dir, err)}
		}
		probe.Close()
		_ = os.Remove(probe.Name())
	}
	b, _ := json.Marshal(map[string]string{"dir": dir})
	if err := os.WriteFile(filepath.Join(r.root, workspaceFile), b, 0o600); err != nil {
		return "", err
	}
	r.wsBase.Store(dir)
	return r.base(), nil
}

func (r *Registry) loadWorkspaceBase() {
	b, err := os.ReadFile(filepath.Join(r.root, workspaceFile))
	if err != nil {
		return
	}
	var v struct {
		Dir string `json:"dir"`
	}
	if json.Unmarshal(b, &v) == nil && v.Dir != "" && filepath.IsAbs(v.Dir) {
		if st, err := os.Stat(v.Dir); err == nil && st.IsDir() {
			r.wsBase.Store(v.Dir)
		} else {
			// e.g. an external disk not mounted: never recreate it on the
			// startup disk; new agents go to the data folder this run
			r.c.Log().Warn("default workspace folder unavailable; new agents use the data folder", "dir", v.Dir)
		}
	}
}

// within reports whether p is base or inside it.
func within(p, base string) bool {
	rel, err := filepath.Rel(base, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
