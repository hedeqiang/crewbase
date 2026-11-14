// Package channel is channel-local: shared channels where several agents
// and humans share context and file references, with exactly one task owner
// at a time and explicit handovers (A-3, A-4, A-5). Channel state (owner,
// lifecycle) is folded from session events.
package channel

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

const ServiceKey = "channel"

// Session event types.
const (
	TypeOwner    = "channel/owner"
	TypeHandover = "channel/handover"
	TypeState    = "channel/state"
	TypeMembers  = "channel/members"
)

// Lifecycle states.
const (
	StateActive   = "active"
	StatePaused   = "paused"
	StateArchived = "archived"
)

// OwnerData is recorded whenever the owner changes.
type OwnerData struct {
	Owner  string `json:"owner"`
	Prev   string `json:"prev,omitempty"`
	By     string `json:"by,omitempty"`     // human or agent who caused it
	Reason string `json:"reason,omitempty"` // created | assigned | handover | reassigned
}

// Handover is the payload of channel/handover (and its Waterfall).
type Handover struct {
	Channel string   `json:"channel"`
	From    string   `json:"from"`
	To      string   `json:"to"`
	Note    string   `json:"note"`
	Files   []string `json:"files,omitempty"` // references, never copies
}

// View is a channel snapshot.
type View struct {
	session.Meta
	Owner   string   `json:"owner"`
	State   string   `json:"state"`
	Members []string `json:"members"`
}

var (
	// HandoverCheck may verify artifacts / receiver state and rewrite the
	// note; returning an error rejects the handover.
	HandoverCheck = kernel.NewWaterfall[Handover, Handover]("channel/handover")
	// OwnerChanged notifies UI and notifications.
	OwnerChanged = kernel.NewEmit[OwnerEvent]("channel/owner-changed")
)

// OwnerEvent reports an owner change.
type OwnerEvent struct {
	Channel string `json:"channel"`
	OwnerData
}

// Service is the `channel` service.
type Service struct {
	c        *kernel.Context
	sessions *session.Store
	agents   *agent.Registry
	mu       sync.Mutex // serializes owner changes (CAS on the log)
}

// Create makes a channel. The first member is the initial owner.
func (s *Service) Create(name string, members []string, folder, by string) (View, error) {
	name = strings.TrimSpace(strings.TrimPrefix(name, "#"))
	if name == "" {
		return View{}, errors.New("channel name is required")
	}
	if len(members) == 0 {
		return View{}, errors.New("a channel needs at least one agent")
	}
	for _, m := range members {
		if _, ok := s.agents.Get(m); !ok {
			return View{}, fmt.Errorf("unknown agent %q", m)
		}
	}
	ss, err := s.sessions.Create(session.Meta{Kind: "channel", Title: name, Agents: members, Folder: folder})
	if err != nil {
		return View{}, err
	}
	if _, err := ss.Append(TypeState, "", map[string]string{"state": StateActive}); err != nil {
		return View{}, err
	}
	if err := s.setOwner(ss, members[0], by, "created"); err != nil {
		return View{}, err
	}
	return s.view(ss), nil
}

func (s *Service) channel(id string) (*session.Session, error) {
	ss, err := s.sessions.Get(id)
	if err != nil || ss.Meta().Kind != "channel" {
		return nil, fmt.Errorf("channel %s not found", id)
	}
	return ss, nil
}

// Owner returns the current owner of a channel session.
func Owner(ss *session.Session) string {
	if e, ok := ss.Last(TypeOwner); ok {
		var d OwnerData
		if e.Decode(&d) == nil {
			return d.Owner
		}
	}
	return ""
}

// State returns the lifecycle state.
func State(ss *session.Session) string {
	if e, ok := ss.Last(TypeState); ok {
		var d map[string]string
		if e.Decode(&d) == nil {
			return d["state"]
		}
	}
	return StateActive
}

func (s *Service) view(ss *session.Session) View {
	m := ss.Meta()
	return View{Meta: m, Owner: Owner(ss), State: State(ss), Members: m.Agents}
}

// Get returns a channel view.
func (s *Service) Get(id string) (View, error) {
	ss, err := s.channel(id)
	if err != nil {
		return View{}, err
	}
	return s.view(ss), nil
}

// List returns all channels.
func (s *Service) List() []View {
	var out []View
	for _, m := range s.sessions.List() {
		if m.Kind != "channel" {
			continue
		}
		if ss, err := s.sessions.Get(m.ID); err == nil {
			out = append(out, s.view(ss))
		}
	}
	return out
}

func (s *Service) setOwner(ss *session.Session, owner, by, reason string) error {
	prev := Owner(ss)
	if prev == owner && reason != "created" {
		return nil
	}
	d := OwnerData{Owner: owner, Prev: prev, By: by, Reason: reason}
	if _, err := ss.Append(TypeOwner, "", d); err != nil {
		return err
	}
	OwnerChanged.Emit(s.c, OwnerEvent{Channel: ss.ID(), OwnerData: d})
	return nil
}

var mentionRe = regexp.MustCompile(`@([\p{L}\p{N}_-]+)`)

// resolveMention finds a member agent mentioned by id or display name.
func (s *Service) resolveMention(ss *session.Session, text string) string {
	members := ss.Meta().Agents
	for _, m := range mentionRe.FindAllStringSubmatch(text, -1) {
		want := strings.ToLower(m[1])
		for _, id := range members {
			a, ok := s.agents.Get(id)
			if !ok {
				continue
			}
			if strings.ToLower(id) == want || strings.ToLower(a.Spec().Name) == want {
				return id
			}
		}
	}
	return ""
}

// PostResult tells the caller who got the message and their queue position.
type PostResult struct {
	Agent    string `json:"agent"`
	Input    string `json:"input"`
	Position int    `json:"position"` // 0 = being worked on next
}

// Post sends a human message. "@agent" assigns the task to that member
// (who becomes owner); otherwise the current owner receives it.
func (s *Service) Post(id, text, from string) (PostResult, error) {
	ss, err := s.channel(id)
	if err != nil {
		return PostResult{}, err
	}
	switch State(ss) {
	case StateArchived:
		return PostResult{}, errors.New("channel is archived")
	}
	s.mu.Lock()
	target := s.resolveMention(ss, text)
	if target != "" {
		if err := s.setOwner(ss, target, from, "assigned"); err != nil {
			s.mu.Unlock()
			return PostResult{}, err
		}
	} else {
		target = Owner(ss)
	}
	s.mu.Unlock()
	a, ok := s.agents.Get(target)
	if !ok {
		return PostResult{}, fmt.Errorf("owner %q is not available", target)
	}
	if State(ss) == StatePaused {
		_ = s.setState(ss, StateActive, from)
	}
	in, err := a.Enqueue(agent.Input{Session: id, Text: text, From: from})
	if err != nil {
		return PostResult{}, err
	}
	pos := 0
	for i, q := range a.Queue() {
		if q.ID == in.ID {
			pos = i
		}
	}
	if st, _ := a.Status(); st == agent.StatusRunning {
		pos++
	}
	return PostResult{Agent: target, Input: in.ID, Position: pos}, nil
}

// HandOver moves ownership from the current owner to `to`, recording the
// note and file references, and queues the receiver to continue.
func (s *Service) HandOver(ctx context.Context, h Handover) (Handover, error) {
	ss, err := s.channel(h.Channel)
	if err != nil {
		return h, err
	}
	if State(ss) == StateArchived {
		return h, errors.New("channel is archived")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if owner := Owner(ss); owner != h.From {
		return h, fmt.Errorf("only the current owner (%s) can hand over", owner)
	}
	if h.To == h.From {
		return h, errors.New("cannot hand over to yourself")
	}
	member := false
	for _, m := range ss.Meta().Agents {
		member = member || m == h.To
	}
	if !member {
		// accept display names too
		if id := s.resolveMention(ss, "@"+h.To); id != "" {
			h.To, member = id, true
		}
	}
	if !member {
		return h, fmt.Errorf("%s is not a member of this channel", h.To)
	}
	h, err = HandoverCheck.Run(kernel.For(s.c, h.From), ctx, h, func(_ context.Context, x Handover) (Handover, error) { return x, nil })
	if err != nil {
		return h, err
	}
	if _, err := ss.Append(TypeHandover, h.From, h); err != nil {
		return h, err
	}
	if err := s.setOwner(ss, h.To, h.From, "handover"); err != nil {
		return h, err
	}
	recv, ok := s.agents.Get(h.To)
	if !ok {
		return h, fmt.Errorf("agent %s not found", h.To)
	}
	fromName := h.From
	if a, ok := s.agents.Get(h.From); ok {
		fromName = a.Spec().Name
	}
	text := fmt.Sprintf("[handover] %s handed this task to you. You are now the task owner.\n%s", fromName, h.Note)
	if len(h.Files) > 0 {
		text += "\nFiles: " + strings.Join(h.Files, ", ")
	}
	_, err = recv.Enqueue(agent.Input{Session: h.Channel, Text: text, From: fromName})
	return h, err
}

// Reassign lets a human change the owner (e.g. from the right panel).
func (s *Service) Reassign(id, to, by, note string) error {
	ss, err := s.channel(id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	prev := Owner(ss)
	if p, ok := s.agents.Get(prev); ok {
		p.Stop()
	}
	if err := s.setOwner(ss, to, by, "reassigned"); err != nil {
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()
	a, ok := s.agents.Get(to)
	if !ok {
		return fmt.Errorf("agent %s not found", to)
	}
	text := "[reassigned] " + by + " assigned this task to you. Continue from the current state."
	if note != "" {
		text += "\n" + note
	}
	_, err = a.Enqueue(agent.Input{Session: id, Text: text, From: by})
	return err
}

// Stop halts the owner's current turn and pauses the channel.
func (s *Service) Stop(id, by string) error {
	ss, err := s.channel(id)
	if err != nil {
		return err
	}
	if a, ok := s.agents.Get(Owner(ss)); ok {
		a.Stop()
		for _, q := range a.Queue() {
			if q.Session == id {
				a.Cancel(q.ID)
			}
		}
	}
	return s.setState(ss, StatePaused, by)
}

// Resume reactivates a paused channel and nudges the owner to continue.
func (s *Service) Resume(id, by string) error {
	ss, err := s.channel(id)
	if err != nil {
		return err
	}
	if State(ss) != StatePaused {
		return nil
	}
	if err := s.setState(ss, StateActive, by); err != nil {
		return err
	}
	if a, ok := s.agents.Get(Owner(ss)); ok {
		_, err = a.Enqueue(agent.Input{Session: id, Text: "[resumed] Continue the task from where you stopped.", From: by})
	}
	return err
}

// Archive closes the channel to new messages.
func (s *Service) Archive(id, by string) error {
	ss, err := s.channel(id)
	if err != nil {
		return err
	}
	_ = s.Stop(id, by)
	return s.setState(ss, StateArchived, by)
}

// AddMember adds an agent to the channel.
func (s *Service) AddMember(id, agentID string) error {
	ss, err := s.channel(id)
	if err != nil {
		return err
	}
	if _, ok := s.agents.Get(agentID); !ok {
		return fmt.Errorf("unknown agent %q", agentID)
	}
	return ss.UpdateMeta(func(m *session.Meta) {
		for _, x := range m.Agents {
			if x == agentID {
				return
			}
		}
		m.Agents = append(m.Agents, agentID)
	})
}

func (s *Service) setState(ss *session.Session, st, by string) error {
	_, err := ss.Append(TypeState, "", map[string]string{"state": st, "by": by})
	return err
}

// handoverTool lets the owner hand the task over.
func (s *Service) handoverTool() *tools.Tool {
	return &tools.Tool{
		Name: "handover",
		Description: "Hand the current channel task to another member agent when your part is done. " +
			"Only the current task owner can do this. Include a concise note (what is done, what is next) and file paths of artifacts.",
		Params: tools.Schema(map[string]any{
			"to":    tools.Prop("string", "agent id or name of the receiving member"),
			"note":  tools.Prop("string", "handover note: what was done and what the receiver should do next"),
			"files": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "artifact file paths (references)"},
		}, "to", "note"),
		Category: tools.CategoryNone,
		Execute: func(ctx context.Context, call tools.Call) (tools.Result, error) {
			if call.Session == nil || call.Session.Meta().Kind != "channel" {
				return tools.Errorf("handover is only available inside a channel"), nil
			}
			var args struct {
				To    string   `json:"to"`
				Note  string   `json:"note"`
				Files []string `json:"files"`
			}
			if err := call.Decode(&args); err != nil {
				return tools.Errorf("bad arguments: %v", err), nil
			}
			h, err := s.HandOver(ctx, Handover{Channel: call.Session.ID(), From: call.Agent, To: args.To, Note: args.Note, Files: args.Files})
			if err != nil {
				return tools.Errorf("handover failed: %v", err), nil
			}
			return tools.Result{Content: "Handed over to " + h.To + ".", Data: h, StopTurn: true}, nil
		},
	}
}

// Plugin is channel-local.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/channel-local",
	Inject:      []string{session.ServiceKey, agent.ServiceKey, tools.ServiceKey},
	Description: "Shared channels with a single task owner and explicit handover.",
	Apply: func(c *kernel.Context) error {
		s := &Service{c: c, sessions: kernel.Use[*session.Store](c, session.ServiceKey), agents: kernel.Use[*agent.Registry](c, agent.ServiceKey)}
		kernel.Use[*tools.Registry](c, tools.ServiceKey).Register(c, s.handoverTool())
		return c.Provide(ServiceKey, s)
	},
})
