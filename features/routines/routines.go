// Package routines implements Routines (R-1) and the Morning Brief (R-2).
// A routine is a scheduled or webhook-triggered check run by one agent in
// its own session, read-only by default. When nothing is wrong the agent
// answers NO_ISSUES and the run stays silent; anything else notifies.
// The morning brief lists only what needs a person; normal runs are folded
// into a count.
package routines

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/capabilities/notify"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/schedule"
	"github.com/hedeqiang/crewbase/kernel"
)

const ServiceKey = "routines"

// Sentinel the agent replies with when everything is normal.
const Sentinel = "NO_ISSUES"

// Run statuses.
const (
	StatusRunning   = "running"
	StatusQuiet     = "quiet"
	StatusAttention = "attention"
	StatusFailed    = "failed"
)

// TypeRun is the session event recording a routine run.
const TypeRun = "routine/run"

// Routine is the payload stored on the schedule job.
type Routine struct {
	ID       string    `json:"id"` // = job id
	Name     string    `json:"name"`
	Agent    string    `json:"agent"`
	Prompt   string    `json:"prompt"`
	Schedule string    `json:"schedule"` // trigger spec or "webhook"
	Mode     string    `json:"mode"`     // read-only (default) | sandbox
	Session  string    `json:"session"`
	Enabled  bool      `json:"enabled"`
	Secret   string    `json:"secret,omitempty"` // webhook secret (shown once)
	NextRun  time.Time `json:"nextRun,omitzero"`
	LastRun  time.Time `json:"lastRun,omitzero"`
	// By: who set it up — "agent" (in a conversation, routine_create) or
	// "" (the person, in Settings). Origin: that conversation.
	By     string `json:"by,omitempty"`
	Origin string `json:"origin,omitempty"`
}

// Run is one execution.
type Run struct {
	Routine string    `json:"routine"`
	Key     string    `json:"key"`
	Input   string    `json:"input"`
	Status  string    `json:"status"`
	Summary string    `json:"summary,omitempty"`
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended,omitzero"`
}

// Brief is a morning brief.
type Brief struct {
	Time      time.Time          `json:"time"`
	NeedsYou  []approval.Request `json:"needsYou"`
	Attention []Run              `json:"attention"`
	Failed    []AgentIssue       `json:"failedAgents"`
	Folded    int                `json:"folded"` // quiet runs since last brief
}

// AgentIssue is an agent in a failed state.
type AgentIssue struct {
	Agent  string `json:"agent"`
	Name   string `json:"name"`
	Detail string `json:"detail"`
}

// Empty reports whether the brief has nothing for a person.
func (b Brief) Empty() bool { return len(b.NeedsYou)+len(b.Attention)+len(b.Failed) == 0 }

// Service is the `routines` service.
type Service struct {
	c      *kernel.Context
	sched  *schedule.Service
	agents *agent.Registry
	dir    string

	mu        sync.Mutex
	briefs    []Brief
	lastBrief time.Time
}

func (s *Service) toRoutine(j schedule.Job) Routine {
	var r Routine
	_ = json.Unmarshal(j.Payload, &r)
	r.ID, r.Name, r.Schedule, r.Enabled = j.ID, j.Name, j.Spec, j.Enabled
	r.NextRun, r.LastRun = j.NextFire, j.LastFire
	r.Secret = ""
	return r
}

// Create adds a routine.
func (s *Service) Create(r Routine) (Routine, error) {
	if strings.TrimSpace(r.Name) == "" || strings.TrimSpace(r.Prompt) == "" {
		return r, errors.New("name and prompt are required")
	}
	a, ok := s.agents.Get(r.Agent)
	if !ok {
		return r, fmt.Errorf("unknown agent %q", r.Agent)
	}
	if r.Mode == "" {
		r.Mode = agent.ModeReadOnly // routines are read-only by default
	}
	if r.Mode == agent.ModeHost {
		return r, errors.New("routines cannot run in host mode")
	}
	if _, err := schedule.Parse(r.Schedule); err != nil {
		return r, err
	}
	ss, err := s.agents.Sessions().Create(session.Meta{Kind: "routine", Title: r.Name, Agents: []string{a.ID()}, Folder: a.Spec().Folder})
	if err != nil {
		return r, err
	}
	r.Session = ss.ID()
	payload, _ := json.Marshal(r)
	j, err := s.sched.Add(schedule.Job{Owner: "routine", Name: r.Name, Spec: r.Schedule, Payload: payload})
	if err != nil {
		return r, err
	}
	out := s.toRoutine(j)
	out.Secret = j.Secret
	return out, nil
}

// List returns all routines.
func (s *Service) List() []Routine {
	var out []Routine
	for _, j := range s.sched.List("routine") {
		out = append(out, s.toRoutine(j))
	}
	return out
}

// Get returns a routine.
func (s *Service) Get(id string) (Routine, bool) {
	j, ok := s.sched.Get(id)
	if !ok || j.Owner != "routine" {
		return Routine{}, false
	}
	return s.toRoutine(j), true
}

// Update changes name/prompt/schedule/enabled.
func (s *Service) Update(id string, fn func(*Routine)) (Routine, error) {
	cur, ok := s.Get(id)
	if !ok {
		return cur, fmt.Errorf("routine %s not found", id)
	}
	fn(&cur)
	if cur.Mode == agent.ModeHost {
		return cur, errors.New("routines cannot run in host mode")
	}
	payload, _ := json.Marshal(cur)
	j, err := s.sched.Update(id, func(j *schedule.Job) {
		j.Name, j.Spec, j.Enabled, j.Payload = cur.Name, cur.Schedule, cur.Enabled, payload
	})
	if err != nil {
		return cur, err
	}
	return s.toRoutine(j), nil
}

// Delete removes a routine (its session stays for history).
func (s *Service) Delete(id string) error {
	if _, ok := s.Get(id); !ok {
		return fmt.Errorf("routine %s not found", id)
	}
	return s.sched.Remove(id)
}

// RunNow triggers a routine immediately.
func (s *Service) RunNow(ctx context.Context, id string) error {
	if _, ok := s.Get(id); !ok {
		return fmt.Errorf("routine %s not found", id)
	}
	return s.sched.Trigger(ctx, id, "", nil)
}

// RunWith runs a routine now with a payload (a webhook event); key makes
// a repeated delivery a no-op.
func (s *Service) RunWith(ctx context.Context, id, key string, body []byte) error {
	if _, ok := s.Get(id); !ok {
		return fmt.Errorf("routine %s not found", id)
	}
	return s.sched.Trigger(ctx, id, key, body)
}

// RunDone is a finished routine run (RunFinished).
type RunDone struct {
	Run
	Name    string `json:"name"`
	Agent   string `json:"agent"`
	Session string `json:"session"`
}

// RunFinished is emitted when a routine run ends (quiet, attention, failed).
var RunFinished = kernel.NewEmit[RunDone]("routine/run-finished")

// Runs lists a routine's runs, newest first.
func (s *Service) Runs(id string) ([]Run, error) {
	r, ok := s.Get(id)
	if !ok {
		return nil, fmt.Errorf("routine %s not found", id)
	}
	ss, err := s.agents.Sessions().Get(r.Session)
	if err != nil {
		return nil, err
	}
	byKey := map[string]Run{}
	var order []string
	for _, e := range ss.Events() {
		if e.Type != TypeRun {
			continue
		}
		var run Run
		if e.Decode(&run) != nil {
			continue
		}
		if _, ok := byKey[run.Key]; !ok {
			order = append(order, run.Key)
		}
		byKey[run.Key] = run // latest state wins
	}
	out := make([]Run, 0, len(order))
	for i := len(order) - 1; i >= 0; i-- {
		out = append(out, byKey[order[i]])
	}
	return out, nil
}

func (s *Service) fire(_ context.Context, f schedule.Firing) error {
	r := s.toRoutine(f.Job)
	a, ok := s.agents.Get(r.Agent)
	if !ok {
		return fmt.Errorf("agent %s not found", r.Agent)
	}
	ss, err := s.agents.Sessions().Get(r.Session)
	if err != nil {
		return err
	}
	text := r.Prompt + "\n\n" +
		"This is a routine check (" + r.Name + "). If everything is normal, reply with exactly " + Sentinel +
		" and nothing else. Otherwise, explain concisely what needs attention and why."
	if len(f.Body) > 0 {
		text += "\n\nTrigger payload:\n" + truncate(string(f.Body), 4000)
	}
	in, err := a.Enqueue(agent.Input{Session: r.Session, Text: text, From: "routine", Mode: r.Mode, Tag: "routine:" + r.ID + ":" + f.Key})
	if err != nil {
		return err
	}
	_, err = ss.Append(TypeRun, a.ID(), Run{Routine: r.ID, Key: f.Key, Input: in.ID, Status: StatusRunning, Started: time.Now().UTC()})
	return err
}

func (s *Service) onTurnEnd(e agentloop.TurnEndEvent) {
	tag := e.Input.Tag
	if !strings.HasPrefix(tag, "routine:") {
		return
	}
	rest := strings.TrimPrefix(tag, "routine:")
	id, key, _ := strings.Cut(rest, ":")
	ss, err := s.agents.Sessions().Get(e.Session)
	if err != nil {
		return
	}
	run := Run{Routine: id, Key: key, Input: e.Input.ID, Ended: time.Now().UTC()}
	switch {
	case e.Status != "completed":
		run.Status, run.Summary = StatusFailed, e.Error
	case strings.TrimSpace(strings.Trim(e.Text, ".` \n")) == Sentinel:
		run.Status = StatusQuiet
	default:
		run.Status, run.Summary = StatusAttention, truncate(e.Text, 2000)
	}
	if prev, err := s.Runs(id); err == nil {
		for _, p := range prev {
			if p.Key == key {
				run.Started = p.Started
			}
		}
	}
	_, _ = ss.Append(TypeRun, e.Agent, run)
	if r, ok := s.Get(id); ok {
		RunFinished.Emit(s.c, RunDone{Run: run, Name: r.Name, Agent: e.Agent, Session: e.Session})
	}
	if run.Status == StatusQuiet {
		return // silent when nothing is wrong
	}
	if n, ok := kernel.Lookup[*notify.Service](s.c, notify.ServiceKey); ok {
		r, _ := s.Get(id)
		kind := notify.KindRoutine
		if run.Status == StatusFailed {
			kind = notify.KindFailed
		}
		n.Send(context.Background(), notify.Notification{Kind: kind, Title: r.Name, Body: truncate(run.Summary, 300), Agent: e.Agent, Session: e.Session})
	}
}

// ComposeBrief builds a brief of everything needing a person since the
// previous brief.
func (s *Service) ComposeBrief() Brief {
	s.mu.Lock()
	since := s.lastBrief
	s.mu.Unlock()
	b := Brief{Time: time.Now().UTC(), NeedsYou: []approval.Request{}, Attention: []Run{}, Failed: []AgentIssue{}}
	if in, ok := kernel.Lookup[*approval.Inbox](s.c, approval.InboxKey); ok {
		b.NeedsYou = append(b.NeedsYou, in.Pending()...)
	}
	for _, r := range s.List() {
		runs, _ := s.Runs(r.ID)
		for _, run := range runs {
			if !run.Ended.IsZero() && run.Ended.Before(since) {
				continue
			}
			switch run.Status {
			case StatusAttention, StatusFailed:
				b.Attention = append(b.Attention, run)
			case StatusQuiet:
				b.Folded++
			}
		}
	}
	for _, a := range s.agents.List() {
		if st, d := a.Status(); st == agent.StatusFailed {
			b.Failed = append(b.Failed, AgentIssue{Agent: a.ID(), Name: a.Spec().Name, Detail: d})
		}
	}
	sort.Slice(b.Attention, func(i, j int) bool { return b.Attention[i].Ended.After(b.Attention[j].Ended) })
	return b
}

func (s *Service) deliverBrief(context.Context, schedule.Firing) error {
	b := s.ComposeBrief()
	s.mu.Lock()
	s.lastBrief = b.Time
	s.briefs = append(s.briefs, b)
	if len(s.briefs) > 30 {
		s.briefs = s.briefs[len(s.briefs)-30:]
	}
	s.saveBriefsLocked()
	s.mu.Unlock()
	if b.Empty() {
		return nil // nothing for a person: no notification
	}
	if n, ok := kernel.Lookup[*notify.Service](s.c, notify.ServiceKey); ok {
		body := fmt.Sprintf("%d 项待批准 · %d 项巡检需关注 · %d 个 Agent 失败 · %d 项正常已折叠",
			len(b.NeedsYou), len(b.Attention), len(b.Failed), b.Folded)
		n.Send(context.Background(), notify.Notification{Kind: notify.KindBrief, Title: "晨报", Body: body})
	}
	return nil
}

// Briefs returns stored briefs, newest last.
func (s *Service) Briefs() []Brief {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Brief(nil), s.briefs...)
}

func (s *Service) saveBriefsLocked() {
	if s.dir == "" {
		return
	}
	b, _ := json.Marshal(s.briefs)
	_ = os.WriteFile(filepath.Join(s.dir, "briefs.json"), b, 0o600)
}

func (s *Service) loadBriefs() {
	if s.dir == "" {
		return
	}
	b, err := os.ReadFile(filepath.Join(s.dir, "briefs.json"))
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &s.briefs)
	if n := len(s.briefs); n > 0 {
		s.lastBrief = s.briefs[n-1].Time
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Config of the routines plugin.
type Config struct {
	Dir   string `json:"dir"`
	Brief string `json:"brief"` // morning brief trigger, default "daily 08:30"; "off" disables
}

// Plugin provides routines and the morning brief.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/routines",
	Inject:      []string{schedule.ServiceKey, agent.ServiceKey},
	Optional:    []string{tools.ServiceKey},
	Description: "Routines (quiet unless something needs attention) and the morning brief.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		if cfg.Brief == "" {
			cfg.Brief = "daily 08:30"
		}
		s := &Service{c: c, sched: kernel.Use[*schedule.Service](c, schedule.ServiceKey), agents: kernel.Use[*agent.Registry](c, agent.ServiceKey), dir: cfg.Dir}
		s.loadBriefs()
		s.sched.Handle(c, "routine", s.fire)
		s.sched.Handle(c, "brief", s.deliverBrief)
		agentloop.TurnEnded.Listen(c, s.onTurnEnd)
		s.registerTools(c)
		if cfg.Brief != "off" {
			found := false
			for _, j := range s.sched.List("brief") {
				found = true
				if j.Spec != cfg.Brief {
					_, _ = s.sched.Update(j.ID, func(x *schedule.Job) { x.Spec = cfg.Brief })
				}
			}
			if !found {
				if _, err := s.sched.Add(schedule.Job{ID: "morning-brief", Owner: "brief", Name: "晨报", Spec: cfg.Brief}); err != nil {
					return err
				}
			}
		}
		return c.Provide(ServiceKey, s)
	},
})
