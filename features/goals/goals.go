// Package goals implements persistent session goals (requirement R-4,
// docs/06 §9). A session may carry one goal; while it is active and a
// person has authorised it, the agent keeps working in rounds: when a turn
// would stop, a continuation prompt is injected (as a logged agent/inject
// event) and the turn continues, until the agent calls goal_complete or
// goal_blocked, the round cap is reached, progress stalls, an approval is
// rejected or the turn fails.
//
// The goal is a projection of goal/set, goal/status and goal/cleared
// session events. Authorisation is deliberately NOT part of the projection:
// it lives only in memory, so after a daemon restart or a fork the goal is
// restored but not authorised, and a person must re-authorise it.
package goals

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

// ServiceKey is the key of the goals service (*Service).
const ServiceKey = "goals"

// Session event types.
const (
	TypeSet     = "goal/set"
	TypeStatus  = "goal/status"
	TypeCleared = "goal/cleared"
)

// Statuses.
const (
	StatusActive   = "active"
	StatusPaused   = "paused"
	StatusBlocked  = "blocked"
	StatusComplete = "complete"
)

// Blocked reason codes.
const (
	CodeNeedsInput       = "needs_input"
	CodeApprovalRejected = "approval_rejected"
	CodeError            = "error"
	CodeMaxRounds        = "max_rounds"
	CodeNoProgress       = "no_progress"
)

// Codes lists every blocked reason code.
var Codes = []string{CodeNeedsInput, CodeApprovalRejected, CodeError, CodeMaxRounds, CodeNoProgress}

// Goal is the current goal of a session.
type Goal struct {
	Session   string `json:"session"`
	Agent     string `json:"agent"`
	Objective string `json:"objective"`
	MaxRounds int    `json:"maxRounds"`
	Status    string `json:"status"`
	Rounds    int    `json:"rounds"`
	// BlockedReason is a code (see Codes); BlockedDetail is free text.
	BlockedReason string `json:"blockedReason,omitempty"`
	BlockedDetail string `json:"blockedDetail,omitempty"`
	Summary       string `json:"summary,omitempty"` // from goal_complete
	// Authorized is live state only (never replayed from the log).
	Authorized bool      `json:"authorized"`
	SetAt      time.Time `json:"setAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// SetData is the payload of goal/set.
type SetData struct {
	Objective string `json:"objective"`
	MaxRounds int    `json:"maxRounds"`
	Agent     string `json:"agent"`
	By        string `json:"by,omitempty"`
}

// StatusData is the payload of goal/status. Authorized is recorded for the
// audit trail only; Project ignores it.
type StatusData struct {
	Status     string `json:"status"`
	Rounds     int    `json:"rounds"`
	Code       string `json:"code,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Summary    string `json:"summary,omitempty"`
	Authorized *bool  `json:"authorized,omitempty"`
	By         string `json:"by,omitempty"`
}

// Project derives the goal from session events (nil when none is set).
// The result is never authorised.
func Project(events []session.Event) *Goal {
	var g *Goal
	for _, e := range events {
		switch e.Type {
		case TypeSet:
			var d SetData
			if e.Decode(&d) != nil {
				continue
			}
			g = &Goal{Session: e.Session, Agent: d.Agent, Objective: d.Objective, MaxRounds: d.MaxRounds, Status: StatusActive, SetAt: e.Time, UpdatedAt: e.Time}
		case TypeStatus:
			var d StatusData
			if g == nil || e.Decode(&d) != nil {
				continue
			}
			if d.Status != "" {
				g.Status = d.Status
			}
			g.Rounds = d.Rounds
			g.BlockedReason, g.BlockedDetail = "", ""
			if g.Status == StatusBlocked {
				g.BlockedReason, g.BlockedDetail = d.Code, d.Reason
			}
			if d.Summary != "" {
				g.Summary = d.Summary
			}
			g.UpdatedAt = e.Time
		case TypeCleared:
			g = nil
		}
	}
	return g
}

// Config of the plugin.
type Config struct {
	DefaultMaxRounds int `json:"defaultMaxRounds"` // default 10
	MaxRoundsLimit   int `json:"maxRoundsLimit"`   // default 100
}

// Errors returned by the service.
var (
	ErrNoGoal   = errors.New("goals: session has no goal")
	ErrComplete = errors.New("goals: goal is complete")
	ErrInvalid  = errors.New("goals: invalid goal")
)

// Service is the `goals` service.
type Service struct {
	agents *agent.Registry
	cfg    Config

	mu       sync.Mutex
	auth     map[string]bool   // session → authorised (memory only)
	turns    map[string]string // agent → session of its running turn
	lastText map[string]string // session → assistant text at the last round
	calls    map[string]string // approval call id → session
	// opMu serialises read-modify-append per session.
	opMu sync.Mutex
}

func (s *Service) session(id string) (*session.Session, error) {
	return s.agents.Sessions().Get(id)
}

// Get returns the session's goal (nil, nil when none).
func (s *Service) Get(sessionID string) (*Goal, error) {
	ss, err := s.session(sessionID)
	if err != nil {
		return nil, err
	}
	return s.get(ss), nil
}

func (s *Service) get(ss *session.Session) *Goal {
	g := Project(ss.Events())
	if g == nil {
		return nil
	}
	g.Session = ss.ID()
	s.mu.Lock()
	g.Authorized = s.auth[ss.ID()]
	s.mu.Unlock()
	return g
}

func (s *Service) setAuth(sid string, v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v {
		s.auth[sid] = true
	} else {
		delete(s.auth, sid)
	}
	delete(s.lastText, sid)
}

func bptr(b bool) *bool { return &b }

func (s *Service) status(ss *session.Session, g *Goal, d StatusData) error {
	if d.Rounds == 0 {
		d.Rounds = g.Rounds
	}
	_, err := ss.Append(TypeStatus, g.Agent, d)
	return err
}

// SetOptions configure a new goal.
type SetOptions struct {
	Objective string
	MaxRounds int
	Agent     string // default: the session's first agent
	Authorize bool
	By        string
}

// Set replaces the session's goal.
func (s *Service) Set(sessionID string, o SetOptions) (*Goal, error) {
	ss, err := s.session(sessionID)
	if err != nil {
		return nil, err
	}
	o.Objective = strings.TrimSpace(o.Objective)
	if o.Objective == "" {
		return nil, fmt.Errorf("%w: objective is required", ErrInvalid)
	}
	if o.MaxRounds == 0 {
		o.MaxRounds = s.cfg.DefaultMaxRounds
	}
	if o.MaxRounds < 1 || o.MaxRounds > s.cfg.MaxRoundsLimit {
		return nil, fmt.Errorf("%w: maxRounds must be between 1 and %d", ErrInvalid, s.cfg.MaxRoundsLimit)
	}
	members := ss.Meta().Agents
	if o.Agent == "" && len(members) > 0 {
		o.Agent = members[0]
	}
	if o.Agent == "" || !slices.Contains(members, o.Agent) {
		return nil, fmt.Errorf("%w: agent must be a member of the session", ErrInvalid)
	}
	s.opMu.Lock()
	if _, err := ss.Append(TypeSet, o.Agent, SetData{Objective: o.Objective, MaxRounds: o.MaxRounds, Agent: o.Agent, By: o.By}); err != nil {
		s.opMu.Unlock()
		return nil, err
	}
	s.setAuth(ss.ID(), false)
	s.opMu.Unlock()
	if o.Authorize {
		return s.Authorize(sessionID, o.By)
	}
	return s.get(ss), nil
}

// Clear removes the goal.
func (s *Service) Clear(sessionID, by string) error {
	ss, err := s.session(sessionID)
	if err != nil {
		return err
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	g := s.get(ss)
	if g == nil {
		return ErrNoGoal
	}
	s.setAuth(ss.ID(), false)
	_, err = ss.Append(TypeCleared, g.Agent, map[string]string{"by": by})
	return err
}

// Authorize lets the goal continue automatically (a person's decision; it
// is required again after every restart or fork). An active goal starts
// working right away.
func (s *Service) Authorize(sessionID, by string) (*Goal, error) {
	return s.transition(sessionID, by, func(g *Goal) (StatusData, error) {
		if g.Status == StatusComplete {
			return StatusData{}, ErrComplete
		}
		d := StatusData{Status: g.Status, Code: g.BlockedReason, Reason: g.BlockedDetail}
		return d, nil
	}, true)
}

// Resume reactivates a paused or blocked goal and authorises it.
func (s *Service) Resume(sessionID, by string) (*Goal, error) {
	return s.transition(sessionID, by, func(g *Goal) (StatusData, error) {
		if g.Status == StatusComplete {
			return StatusData{}, ErrComplete
		}
		return StatusData{Status: StatusActive}, nil
	}, true)
}

// Pause stops automatic continuation (the running step finishes).
func (s *Service) Pause(sessionID, by string) (*Goal, error) {
	return s.transition(sessionID, by, func(g *Goal) (StatusData, error) {
		if g.Status == StatusComplete {
			return StatusData{}, ErrComplete
		}
		return StatusData{Status: StatusPaused}, nil
	}, false)
}

func (s *Service) transition(sessionID, by string, fn func(*Goal) (StatusData, error), authorize bool) (*Goal, error) {
	ss, err := s.session(sessionID)
	if err != nil {
		return nil, err
	}
	s.opMu.Lock()
	g := s.get(ss)
	if g == nil {
		s.opMu.Unlock()
		return nil, ErrNoGoal
	}
	d, err := fn(g)
	if err != nil {
		s.opMu.Unlock()
		return nil, err
	}
	d.By = by
	if authorize {
		d.Authorized = bptr(true)
	}
	if err := s.status(ss, g, d); err != nil {
		s.opMu.Unlock()
		return nil, err
	}
	if authorize {
		s.setAuth(ss.ID(), true)
	}
	s.opMu.Unlock()
	g = s.get(ss)
	if authorize && g.Status == StatusActive {
		s.kickoff(ss, g)
	}
	return g, nil
}

func (s *Service) prompt(g *Goal, round int) string {
	return fmt.Sprintf("Continue working toward the goal: %s\n(Round %d of %d.) If the goal is complete, call goal_complete with a short summary. If you are blocked, call goal_blocked with a reason and a code (%s, %s, %s or %s).",
		g.Objective, round, g.MaxRounds, CodeNeedsInput, CodeApprovalRejected, CodeError, CodeNoProgress)
}

// kickoff queues a first input for the goal's agent unless it is already
// working on (or has queued work for) this session.
func (s *Service) kickoff(ss *session.Session, g *Goal) {
	a, ok := s.agents.Get(g.Agent)
	if !ok {
		return
	}
	s.mu.Lock()
	busy := s.turns[a.ID()] == ss.ID()
	s.mu.Unlock()
	if busy {
		return
	}
	for _, in := range a.Queue() {
		if in.Session == ss.ID() {
			return
		}
	}
	text := fmt.Sprintf("Work toward the goal: %s\nIf the goal is complete, call goal_complete with a short summary. If you are blocked, call goal_blocked with a reason and a code.", g.Objective)
	_, _ = a.Enqueue(agent.Input{Session: ss.ID(), Text: text, From: "goal", Tag: "goal:" + ss.ID()})
}

func (s *Service) block(ss *session.Session, g *Goal, code, reason string) {
	_ = s.status(ss, g, StatusData{Status: StatusBlocked, Code: code, Reason: reason, By: "system"})
}

// onStopping decides whether the turn continues for another round.
func (s *Service) onStopping(a *agent.Agent) bool {
	s.mu.Lock()
	sid := s.turns[a.ID()]
	s.mu.Unlock()
	if sid == "" {
		return false
	}
	ss, err := s.session(sid)
	if err != nil {
		return false
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	g := s.get(ss)
	if g == nil || g.Status != StatusActive || !g.Authorized || g.Agent != a.ID() {
		return false
	}
	if g.Rounds >= g.MaxRounds {
		s.block(ss, g, CodeMaxRounds, fmt.Sprintf("reached the round limit (%d)", g.MaxRounds))
		return false
	}
	text := ""
	if e, ok := ss.Last(session.TypeAssistantMessage); ok && e.Agent == a.ID() {
		var m session.AssistantMessage
		if e.Decode(&m) == nil {
			text = strings.TrimSpace(m.Message.Text())
		}
	}
	s.mu.Lock()
	prev, seen := s.lastText[sid]
	s.lastText[sid] = text
	s.mu.Unlock()
	if seen && prev == text {
		s.block(ss, g, CodeNoProgress, "the last round produced the same reply as the one before")
		return false
	}
	round := g.Rounds + 1
	if err := s.status(ss, g, StatusData{Status: StatusActive, Rounds: round}); err != nil {
		return false
	}
	if err := a.Inject(ss, s.prompt(g, round), "goal"); err != nil {
		s.block(ss, g, CodeError, "could not record the continuation: "+err.Error())
		return false
	}
	return true
}

func (s *Service) onTurnEnd(e agentloop.TurnEndEvent) {
	s.mu.Lock()
	if s.turns[e.Agent] == e.Session {
		delete(s.turns, e.Agent)
	}
	s.mu.Unlock()
	ss, err := s.session(e.Session)
	if err != nil {
		return
	}
	s.opMu.Lock()
	g := s.get(ss)
	if g == nil || g.Agent != e.Agent || g.Status != StatusActive {
		s.opMu.Unlock()
		return
	}
	switch {
	case e.Status == "failed":
		s.block(ss, g, CodeError, e.Error)
	case e.Status == "cancelled":
		_ = s.status(ss, g, StatusData{Status: StatusPaused, By: "stop"})
	case e.Status == "completed" && e.Error != "" && g.Authorized:
		// The loop's step cap ended the turn while the goal is still open:
		// continue in a new turn, counting it as a round.
		if g.Rounds >= g.MaxRounds {
			s.block(ss, g, CodeMaxRounds, fmt.Sprintf("reached the round limit (%d)", g.MaxRounds))
			break
		}
		round := g.Rounds + 1
		if s.status(ss, g, StatusData{Status: StatusActive, Rounds: round}) == nil {
			if a, ok := s.agents.Get(g.Agent); ok {
				_, _ = a.Enqueue(agent.Input{Session: ss.ID(), Text: s.prompt(g, round), From: "goal", Tag: "goal:" + ss.ID()})
			}
		}
	}
	s.opMu.Unlock()
}

func (s *Service) onDecided(d approval.DecidedData) {
	s.mu.Lock()
	sid := s.calls[d.CallID]
	delete(s.calls, d.CallID)
	s.mu.Unlock()
	if sid == "" || d.Outcome != approval.OutcomeRejected {
		return
	}
	ss, err := s.session(sid)
	if err != nil {
		return
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if g := s.get(ss); g != nil && g.Status == StatusActive {
		reason := "an approval was rejected"
		if d.Message != "" {
			reason += ": " + d.Message
		}
		s.block(ss, g, CodeApprovalRejected, reason)
	}
}

func (s *Service) tools() []*tools.Tool {
	kinds := []string{"agent", "channel"}
	return []*tools.Tool{
		{
			Name:         "goal_complete",
			Description:  "Mark this conversation's goal as complete. Call it only when the objective is fully achieved.",
			Params:       tools.Schema(map[string]any{"summary": tools.Prop("string", "What was achieved")}, "summary"),
			Category:     tools.CategoryNone,
			SessionKinds: kinds,
			Execute: func(_ context.Context, call tools.Call) (tools.Result, error) {
				var a struct {
					Summary string `json:"summary"`
				}
				_ = call.Decode(&a)
				return s.fromTool(call, StatusData{Status: StatusComplete, Summary: a.Summary, By: call.Agent}, "Goal marked complete.")
			},
		},
		{
			Name:        "goal_blocked",
			Description: "Report that this conversation's goal cannot progress without a person. Automatic continuation stops until someone resumes it.",
			Params: tools.Schema(map[string]any{
				"reason": tools.Prop("string", "What is blocking progress and what is needed"),
				"code":   map[string]any{"type": "string", "enum": []string{CodeNeedsInput, CodeApprovalRejected, CodeError, CodeNoProgress}, "description": "Reason code"},
			}, "reason"),
			Category:     tools.CategoryNone,
			SessionKinds: kinds,
			Execute: func(_ context.Context, call tools.Call) (tools.Result, error) {
				var a struct {
					Reason string `json:"reason"`
					Code   string `json:"code"`
				}
				_ = call.Decode(&a)
				if a.Code == "" {
					a.Code = CodeNeedsInput
				}
				if !slices.Contains(Codes, a.Code) || a.Code == CodeMaxRounds {
					return tools.Errorf("unknown code %q", a.Code), nil
				}
				return s.fromTool(call, StatusData{Status: StatusBlocked, Code: a.Code, Reason: a.Reason, By: call.Agent}, "Goal marked blocked; a person has been asked to help.")
			},
		},
	}
}

func (s *Service) fromTool(call tools.Call, d StatusData, ok string) (tools.Result, error) {
	ss := call.Session
	if ss == nil {
		return tools.Errorf("no session"), nil
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	g := s.get(ss)
	if g == nil {
		return tools.Errorf("this conversation has no goal"), nil
	}
	if g.Status == StatusComplete {
		return tools.Errorf("the goal is already complete"), nil
	}
	if err := s.status(ss, g, d); err != nil {
		return tools.Errorf("could not record: %v", err), nil
	}
	return tools.Result{Content: ok, StopTurn: true}, nil
}

// Plugin provides the goals service, the continuation listener and the
// goal_complete / goal_blocked tools.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/goals",
	Inject:      []string{agent.ServiceKey, tools.ServiceKey},
	Description: "Persistent session goals: round-capped continuation, blocked reason codes, re-authorisation after restart/fork.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		if cfg.MaxRoundsLimit <= 0 {
			cfg.MaxRoundsLimit = 100
		}
		if cfg.DefaultMaxRounds <= 0 {
			cfg.DefaultMaxRounds = 10
		}
		if cfg.DefaultMaxRounds > cfg.MaxRoundsLimit {
			cfg.DefaultMaxRounds = cfg.MaxRoundsLimit
		}
		s := &Service{agents: kernel.Use[*agent.Registry](c, agent.ServiceKey), cfg: cfg,
			auth: map[string]bool{}, turns: map[string]string{}, lastText: map[string]string{}, calls: map[string]string{}}
		reg := kernel.Use[*tools.Registry](c, tools.ServiceKey)
		for _, t := range s.tools() {
			reg.Register(c, t)
		}
		agentloop.TurnStarted.Listen(c, func(e agentloop.TurnStartEvent) {
			s.mu.Lock()
			s.turns[e.Agent.ID()] = e.Session.ID()
			s.mu.Unlock()
		})
		agentloop.TurnEnded.Listen(c, s.onTurnEnd)
		agentloop.TurnStopping.Listen(c, func(_ context.Context, a *agent.Agent) (bool, error) {
			return s.onStopping(a), nil
		})
		approval.Asked.Listen(c, func(r approval.Request) {
			s.mu.Lock()
			s.calls[r.CallID] = r.Session
			s.mu.Unlock()
		})
		approval.Decided.Listen(c, s.onDecided)
		return c.Provide(ServiceKey, s)
	},
})
