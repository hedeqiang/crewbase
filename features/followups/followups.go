// Package followups lets an agent wake itself later (plugin
// @crewbase/followups): "check whether CI passed in 20 minutes", "see
// whether the vendor replied tomorrow at 9". A follow-up is a one-shot
// schedule job (owner "followup"); when it is due the note comes back as a
// new input in the conversation that set it, so the agent picks up where it
// left off instead of polling or forgetting.
//
// Tools: followup_set {in | at, note}, followup_list, followup_cancel {id}.
// API:   GET /api/agents/{id}/followups, DELETE /api/agents/{id}/followups/{fid}
//
// Limits: at least a minute and at most 90 days ahead; at most MaxPerAgent
// pending per agent. Follow-ups of a deleted agent are removed.
package followups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/features/schedule"
	"github.com/hedeqiang/crewbase/kernel"
)

// Owner is the schedule owner kind of follow-ups.
const Owner = "followup"

// Limits.
const (
	MaxPerAgent = 20
	MinDelay    = time.Minute
	MaxDelay    = 90 * 24 * time.Hour
	MaxNote     = 2000
)

// Payload is stored in the schedule job.
type Payload struct {
	Agent   string    `json:"agent"`
	Session string    `json:"session"`
	Note    string    `json:"note"`
	Set     time.Time `json:"set"`
}

// Followup is a pending follow-up as tools and the UI see it.
type Followup struct {
	ID      string    `json:"id"`
	Agent   string    `json:"agent"`
	Session string    `json:"session"`
	Note    string    `json:"note"`
	Due     time.Time `json:"due"`
	Set     time.Time `json:"set"`
}

// Service manages follow-ups.
type Service struct {
	c      *kernel.Context
	sched  *schedule.Service
	agents *agent.Registry
	now    func() time.Time
}

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

var dayRe = regexp.MustCompile(`^(\d+)d(.*)$`)

// parseIn accepts Go durations plus days: "20m", "2h30m", "1d", "3d12h".
func parseIn(v string) (time.Duration, error) {
	v = strings.TrimSpace(strings.ToLower(v))
	var d time.Duration
	if m := dayRe.FindStringSubmatch(v); m != nil {
		n, _ := strconv.Atoi(m[1])
		d = time.Duration(n) * 24 * time.Hour
		v = m[2]
	}
	if v != "" {
		x, err := time.ParseDuration(v)
		if err != nil {
			return 0, fmt.Errorf("in: want a duration such as 20m, 2h or 1d (%v)", err)
		}
		d += x
	}
	return d, nil
}

// parseAt accepts RFC 3339 or local "2006-01-02 15:04" / "15:04" (today,
// or tomorrow if already past).
func parseAt(v string, now time.Time) (time.Time, error) {
	v = strings.TrimSpace(v)
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	for _, f := range []string{"2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(f, v, now.Location()); err == nil {
			return t, nil
		}
	}
	if t, err := time.ParseInLocation("15:04", v, now.Location()); err == nil {
		d := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
		if !d.After(now) {
			d = d.AddDate(0, 0, 1)
		}
		return d, nil
	}
	return time.Time{}, fmt.Errorf("at: want a time such as 2026-11-16 09:00, 09:00 or RFC 3339")
}

// Set schedules a follow-up.
func (s *Service) Set(agentID, sessionID string, due time.Time, note string) (Followup, error) {
	note = strings.TrimSpace(note)
	if note == "" {
		return Followup{}, errors.New("note is required: say what to check and why")
	}
	if r := []rune(note); len(r) > MaxNote {
		note = string(r[:MaxNote])
	}
	now := s.clock()
	if d := due.Sub(now); d < MinDelay-time.Second {
		return Followup{}, fmt.Errorf("a follow-up must be at least a minute ahead (%s is %s)", due.Format("2006-01-02 15:04"), humanDur(d))
	} else if d > MaxDelay {
		return Followup{}, errors.New("a follow-up can be at most 90 days ahead")
	}
	if n := len(s.List(agentID)); n >= MaxPerAgent {
		return Followup{}, fmt.Errorf("this agent already has %d pending follow-ups; cancel some first", n)
	}
	p := Payload{Agent: agentID, Session: sessionID, Note: note, Set: now.UTC()}
	b, _ := json.Marshal(p)
	j, err := s.sched.Add(schedule.Job{Owner: Owner, Name: "跟进：" + clip(note, 40), Spec: "at " + due.UTC().Format(time.RFC3339), Payload: b})
	if err != nil {
		return Followup{}, err
	}
	return toFollowup(j)
}

func toFollowup(j schedule.Job) (Followup, error) {
	var p Payload
	if err := json.Unmarshal(j.Payload, &p); err != nil {
		return Followup{}, err
	}
	return Followup{ID: j.ID, Agent: p.Agent, Session: p.Session, Note: p.Note, Due: j.NextFire, Set: p.Set}, nil
}

// List returns the agent's pending follow-ups, soonest first ("" = all).
func (s *Service) List(agentID string) []Followup {
	var out []Followup
	for _, j := range s.sched.List(Owner) {
		if j.NextFire.IsZero() {
			continue // fired
		}
		f, err := toFollowup(j)
		if err != nil || (agentID != "" && f.Agent != agentID) {
			continue
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Due.Before(out[k].Due) })
	return out
}

// Cancel removes a pending follow-up of agentID.
func (s *Service) Cancel(agentID, id string) error {
	j, ok := s.sched.Get(id)
	if !ok || j.Owner != Owner {
		return fmt.Errorf("no follow-up %s", id)
	}
	if f, err := toFollowup(j); err != nil || f.Agent != agentID {
		return fmt.Errorf("no follow-up %s", id)
	}
	return s.sched.Remove(id)
}

// fire delivers a due follow-up. It runs under the scheduler's lock: the
// fired job is removed afterwards, outside it.
func (s *Service) fire(_ context.Context, f schedule.Firing) error {
	var p Payload
	if err := json.Unmarshal(f.Job.Payload, &p); err != nil {
		return err
	}
	id := f.Job.ID
	defer s.c.Go(func(context.Context) { _ = s.sched.Remove(id) })
	a, ok := s.agents.Get(p.Agent)
	if !ok {
		return nil // the agent is gone
	}
	if ss, err := s.agents.Sessions().Get(p.Session); err != nil || ss == nil {
		p.Session = "" // its conversation is gone: the agent's own
	}
	text := fmt.Sprintf("[跟进提醒] 这是你在 %s 设置的跟进，现在到点了：\n\n%s\n\n"+
		"只检查这件事现在的状态，不要顺带做别的。需要用户知道或决定的，简短告诉用户；还没结果的，可以再设一个跟进；不需要做什么就一句话说明，不要编造进展。",
		p.Set.In(s.clock().Location()).Format("01-02 15:04"), p.Note)
	_, err := a.Enqueue(agent.Input{Session: p.Session, Text: text, From: "followup", Tag: "followup:" + id})
	return err
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func humanDur(d time.Duration) string {
	if d < 0 {
		return "in the past"
	}
	return "only " + d.Round(time.Second).String() + " away"
}

// ---------------------------------------------------------------- tools

func (s *Service) tools() []*tools.Tool {
	return []*tools.Tool{
		{
			Name: "followup_set",
			Description: "Wake yourself later in this conversation. Use it when you are waiting on something you cannot speed up " +
				"(CI, a deploy, a reply, a long job) or the user asks you to check back: instead of polling or forgetting, " +
				"set a follow-up and end your turn. When it is due, the note comes back to you as a new message. " +
				"Give either `in` (20m, 2h, 1d) or `at` (2026-11-16 09:00 local time, or 09:00). " +
				"Write the note for your future self: what to check, how, and why.",
			Params: tools.Schema(map[string]any{
				"in":   tools.Prop("string", "Delay from now: 20m, 2h, 1d, 1d12h."),
				"at":   tools.Prop("string", "Local time: 2026-11-16 09:00, or 09:00 (today, or tomorrow if past)."),
				"note": tools.Prop("string", "What to check when it is due, and why."),
			}, "note"),
			Category: tools.CategoryNone,
			Classify: func(call tools.Call) (string, string) {
				return tools.CategoryNone, "followup: " + clip(call.String("note"), 80)
			},
			Execute: func(_ context.Context, call tools.Call) (tools.Result, error) {
				var a struct{ In, At, Note string }
				if err := call.Decode(&a); err != nil {
					return tools.Errorf("invalid arguments: %v", err), nil
				}
				now := s.clock()
				var due time.Time
				switch {
				case a.In != "" && a.At != "":
					return tools.Errorf("give either in or at, not both"), nil
				case a.In != "":
					d, err := parseIn(a.In)
					if err != nil {
						return tools.Errorf("%v", err), nil
					}
					due = now.Add(d)
				case a.At != "":
					t, err := parseAt(a.At, now)
					if err != nil {
						return tools.Errorf("%v", err), nil
					}
					due = t
				default:
					return tools.Errorf("give in (20m, 2h, 1d) or at (2026-11-16 09:00)"), nil
				}
				sess := ""
				if call.Session != nil {
					sess = call.Session.ID()
				}
				f, err := s.Set(call.Agent, sess, due, a.Note)
				if err != nil {
					return tools.Errorf("%v", err), nil
				}
				return tools.Text(fmt.Sprintf("Follow-up %s set for %s. You can end your turn now; the note will come back to you then.",
					f.ID, f.Due.In(now.Location()).Format("Mon 2006-01-02 15:04"))), nil
			},
		},
		{
			Name:        "followup_list",
			Description: "List your pending follow-ups.",
			Params:      tools.Schema(map[string]any{}),
			Category:    tools.CategoryNone,
			Execute: func(_ context.Context, call tools.Call) (tools.Result, error) {
				fs := s.List(call.Agent)
				if len(fs) == 0 {
					return tools.Text("No pending follow-ups."), nil
				}
				var b strings.Builder
				loc := s.clock().Location()
				for _, f := range fs {
					fmt.Fprintf(&b, "%s  %s  %s\n", f.ID, f.Due.In(loc).Format("2006-01-02 15:04"), clip(f.Note, 120))
				}
				return tools.Text(b.String()), nil
			},
		},
		{
			Name:        "followup_cancel",
			Description: "Cancel one of your pending follow-ups (when it is no longer needed).",
			Params:      tools.Schema(map[string]any{"id": tools.Prop("string", "Follow-up id from followup_set or followup_list.")}, "id"),
			Category:    tools.CategoryNone,
			Execute: func(_ context.Context, call tools.Call) (tools.Result, error) {
				if err := s.Cancel(call.Agent, call.String("id")); err != nil {
					return tools.Errorf("%v", err), nil
				}
				return tools.Text("Cancelled."), nil
			},
		},
	}
}

// ---------------------------------------------------------------- plugins

// ServiceKey of the *Service.
const ServiceKey = "followups"

// Plugin is @crewbase/followups.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/followups",
	Inject:      []string{schedule.ServiceKey, agent.ServiceKey, tools.ServiceKey},
	Description: "Agents wake themselves later: followup_set / list / cancel.",
	Apply: func(c *kernel.Context) error {
		s := &Service{c: c, sched: kernel.Use[*schedule.Service](c, schedule.ServiceKey), agents: kernel.Use[*agent.Registry](c, agent.ServiceKey)}
		s.sched.Handle(c, Owner, s.fire)
		reg := kernel.Use[*tools.Registry](c, tools.ServiceKey)
		for _, t := range s.tools() {
			t.Load, t.Group = tools.LoadDeferred, "followups"
			reg.Register(c, t)
		}
		agent.Deleted.Listen(c, func(id string) {
			for _, f := range s.List(id) {
				_ = s.sched.Remove(f.ID)
			}
		})
		// fired jobs left by a crash before their removal
		for _, j := range s.sched.List(Owner) {
			if j.NextFire.IsZero() && j.Fires > 0 {
				_ = s.sched.Remove(j.ID)
			}
		}
		return c.Provide(ServiceKey, s)
	},
})

// APIPlugin is @crewbase/followups-api.
var APIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/followups-api",
	Inject:      []string{gateway.ServiceKey, ServiceKey},
	Description: "List and cancel an agent's follow-ups.",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		s := kernel.Use[*Service](c, ServiceKey)
		mobile := []string{"mobile"}
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/agents/{id}/followups", Devices: mobile,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if _, ok := s.agents.Get(r.Params["id"]); !ok {
					return nil, gateway.NotFound()
				}
				fs := s.List(r.Params["id"])
				if fs == nil {
					fs = []Followup{}
				}
				return map[string]any{"followups": fs}, nil
			}})
		g.Handle(c, gateway.Route{Method: "DELETE", Path: "/api/agents/{id}/followups/{fid}", Devices: mobile,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if err := s.Cancel(r.Params["id"], r.Params["fid"]); err != nil {
					return nil, gateway.NotFound()
				}
				return nil, nil
			}})
		return nil
	},
})
