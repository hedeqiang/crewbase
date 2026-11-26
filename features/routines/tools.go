package routines

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/schedule"
	"github.com/hedeqiang/crewbase/kernel"
)

// Limits on routines an agent sets up itself (the person can set up
// anything in 设置 › 自动化).
const (
	MaxAgentRoutines = 10
	MinAgentInterval = time.Hour
)

// Tool names.
const (
	ToolCreate = "routine_create"
	ToolList   = "routine_list"
	ToolUpdate = "routine_update"
	ToolCancel = "routine_cancel"
)

type routineArgs struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Prompt   string `json:"prompt"`
	Schedule string `json:"schedule"`
	Mode     string `json:"mode"`
	Enabled  *bool  `json:"enabled"`
}

// checkAgentSchedule: a real schedule (no webhook), at most hourly.
func checkAgentSchedule(spec string) error {
	spec = strings.TrimSpace(spec)
	if spec == "" || schedule.IsWebhookSpec(spec) || spec == "manual" {
		return fmt.Errorf("give a schedule: daily HH:MM, weekly mon HH:MM, every 2h, or a 5-field cron expression (webhooks are set up by the person in Settings)")
	}
	sp, err := schedule.Parse(spec)
	if err != nil {
		return fmt.Errorf("schedule %q: %v", spec, err)
	}
	if strings.HasPrefix(strings.ToLower(spec), "at ") {
		return fmt.Errorf("a routine repeats; for a one-time check use followup_set")
	}
	now := time.Now()
	a := sp.Next(now)
	b := sp.Next(a)
	c := sp.Next(b)
	if a.IsZero() || b.IsZero() {
		return fmt.Errorf("schedule %q never repeats", spec)
	}
	if b.Sub(a) < MinAgentInterval-time.Second || (!c.IsZero() && c.Sub(b) < MinAgentInterval-time.Second) {
		return fmt.Errorf("a routine you set up may run at most once an hour")
	}
	return nil
}

func modeOf(m string) (string, error) {
	switch m {
	case "", agent.ModeReadOnly:
		return agent.ModeReadOnly, nil
	case agent.ModeSandbox:
		return agent.ModeSandbox, nil
	}
	return "", fmt.Errorf("mode must be read-only or sandbox")
}

// own returns routine id when it belongs to agentID.
func (s *Service) own(agentID, id string) (Routine, error) {
	r, ok := s.Get(strings.TrimSpace(id))
	if !ok || r.Agent != agentID {
		return Routine{}, fmt.Errorf("you have no routine %q (routine_list shows yours)", id)
	}
	return r, nil
}

func describeRoutine(r Routine) string {
	state := "on"
	if !r.Enabled {
		state = "off"
	}
	next := ""
	if r.Enabled && !r.NextRun.IsZero() {
		next = ", next " + r.NextRun.Local().Format("Mon 2006-01-02 15:04")
	}
	return fmt.Sprintf("%s  %s  [%s, %s, %s%s]\n    %s", r.ID, r.Name, r.Schedule, r.Mode, state, next, clipRunes(r.Prompt, 200))
}

func clipRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

// scheduleWords is a schedule in plain words for the approval card.
func scheduleWords(spec string) string {
	f := strings.Fields(strings.ToLower(strings.TrimSpace(spec)))
	days := map[string]string{"mon": "Monday", "tue": "Tuesday", "wed": "Wednesday", "thu": "Thursday", "fri": "Friday", "sat": "Saturday", "sun": "Sunday"}
	switch {
	case len(f) == 2 && f[0] == "daily":
		return "every day at " + f[1]
	case len(f) == 3 && f[0] == "weekly" && days[f[1][:min(3, len(f[1]))]] != "":
		return "every " + days[f[1][:3]] + " at " + f[2]
	case len(f) == 2 && f[0] == "every":
		d := strings.NewReplacer("h", " h", "m", " min").Replace(f[1])
		return "every " + d
	}
	return spec
}

func modeWords(m string) string {
	if m == agent.ModeSandbox {
		return "sandbox"
	}
	return "read-only"
}

func (s *Service) registerTools(c *kernel.Context) {
	reg, ok := kernel.Lookup[*tools.Registry](c, tools.ServiceKey)
	if !ok {
		return
	}
	reg.Register(c, &tools.Tool{
		Name: ToolCreate,
		Description: "Set up a routine: a task you run again and again on a schedule, in its own conversation (e.g. \"every morning at 8 " +
			"check the competitors' pricing pages\", \"every Friday 17:00 write the weekly report\"). Use it when the person asks for something " +
			"recurring. The person confirms it before it is saved. For a one-time check later, use followup_set instead. " +
			"Each run, the prompt is sent to you; when nothing needs attention you answer NO_ISSUES and the run stays quiet, otherwise the person is notified.",
		Params: tools.Schema(map[string]any{
			"name":     tools.Prop("string", "Short name, e.g. 竞品定价巡检."),
			"prompt":   tools.Prop("string", "What to do each run, written for your future self: what to check, how, and what counts as worth reporting."),
			"schedule": tools.Prop("string", "When: daily 08:30, weekly fri 17:00, every 2h, or a 5-field cron expression (local time; at most hourly)."),
			"mode":     tools.Prop("string", "read-only (default: look, do not change anything) or sandbox (may write in your workspace)."),
		}, "name", "prompt", "schedule"),
		Category: tools.CategoryNone,
		Group:    "routines",
		Execute: func(_ context.Context, call tools.Call) (tools.Result, error) {
			var a routineArgs
			if err := call.Decode(&a); err != nil {
				return tools.Errorf("invalid arguments: %v", err), nil
			}
			if strings.TrimSpace(a.Name) == "" || strings.TrimSpace(a.Prompt) == "" {
				return tools.Errorf("name and prompt are required"), nil
			}
			if err := checkAgentSchedule(a.Schedule); err != nil {
				return tools.Errorf("%v", err), nil
			}
			mode, err := modeOf(a.Mode)
			if err != nil {
				return tools.Errorf("%v", err), nil
			}
			n := 0
			for _, r := range s.List() {
				if r.Agent == call.Agent {
					n++
				}
			}
			if n >= MaxAgentRoutines {
				return tools.Errorf("you already have %d routines; cancel one first (routine_list, routine_cancel)", n), nil
			}
			origin := ""
			if call.Session != nil {
				origin = call.Session.ID()
			}
			r, err := s.Create(Routine{Name: strings.TrimSpace(a.Name), Agent: call.Agent, Prompt: strings.TrimSpace(a.Prompt),
				Schedule: strings.TrimSpace(a.Schedule), Mode: mode, Enabled: true, By: "agent", Origin: origin})
			if err != nil {
				return tools.Errorf("%v", err), nil
			}
			return tools.Text(fmt.Sprintf("Routine %s created: %s, %s. Next run %s. The person can see, edit and turn it off in Settings › Automation.",
				r.ID, r.Name, r.Schedule, r.NextRun.Local().Format("Mon 2006-01-02 15:04"))), nil
		},
	})
	reg.Register(c, &tools.Tool{
		Name:        ToolList,
		Load:        tools.LoadDeferred,
		Description: "List your routines (scheduled recurring tasks): id, name, schedule, mode, on/off, next run, prompt.",
		Params:      tools.Schema(map[string]any{}),
		Category:    tools.CategoryNone,
		Group:       "routines",
		Execute: func(_ context.Context, call tools.Call) (tools.Result, error) {
			var b strings.Builder
			for _, r := range s.List() {
				if r.Agent == call.Agent {
					b.WriteString(describeRoutine(r) + "\n")
				}
			}
			if b.Len() == 0 {
				return tools.Text("You have no routines."), nil
			}
			return tools.Text(b.String()), nil
		},
	})
	reg.Register(c, &tools.Tool{
		Name: ToolUpdate,
		Load: tools.LoadDeferred,
		Description: "Change one of your routines: its name, prompt, schedule or mode, or turn it off/on (enabled). Give only what changes. " +
			"Changes other than turning it off are confirmed by the person.",
		Params: tools.Schema(map[string]any{
			"id":       tools.Prop("string", "Routine id (routine_list)."),
			"name":     tools.Prop("string", "New name."),
			"prompt":   tools.Prop("string", "New prompt."),
			"schedule": tools.Prop("string", "New schedule: daily 08:30, weekly fri 17:00, every 2h, or cron."),
			"mode":     tools.Prop("string", "read-only or sandbox."),
			"enabled":  tools.Prop("boolean", "false pauses it, true resumes it."),
		}, "id"),
		Category: tools.CategoryNone,
		Group:    "routines",
		Execute: func(_ context.Context, call tools.Call) (tools.Result, error) {
			var a routineArgs
			if err := call.Decode(&a); err != nil {
				return tools.Errorf("invalid arguments: %v", err), nil
			}
			if _, err := s.own(call.Agent, a.ID); err != nil {
				return tools.Errorf("%v", err), nil
			}
			if a.Schedule != "" {
				if err := checkAgentSchedule(a.Schedule); err != nil {
					return tools.Errorf("%v", err), nil
				}
			}
			mode := ""
			if a.Mode != "" {
				m, err := modeOf(a.Mode)
				if err != nil {
					return tools.Errorf("%v", err), nil
				}
				mode = m
			}
			r, err := s.Update(strings.TrimSpace(a.ID), func(r *Routine) {
				if v := strings.TrimSpace(a.Name); v != "" {
					r.Name = v
				}
				if v := strings.TrimSpace(a.Prompt); v != "" {
					r.Prompt = v
				}
				if v := strings.TrimSpace(a.Schedule); v != "" {
					r.Schedule = v
				}
				if mode != "" {
					r.Mode = mode
				}
				if a.Enabled != nil {
					r.Enabled = *a.Enabled
				}
			})
			if err != nil {
				return tools.Errorf("%v", err), nil
			}
			return tools.Text("Updated:\n" + describeRoutine(r)), nil
		},
	})
	reg.Register(c, &tools.Tool{
		Name:        ToolCancel,
		Load:        tools.LoadDeferred,
		Description: "Delete one of your routines for good (when the person no longer wants it). To pause it instead, use routine_update with enabled: false.",
		Params:      tools.Schema(map[string]any{"id": tools.Prop("string", "Routine id (routine_list).")}, "id"),
		Category:    tools.CategoryNone,
		Group:       "routines",
		Execute: func(_ context.Context, call tools.Call) (tools.Result, error) {
			r, err := s.own(call.Agent, call.String("id"))
			if err != nil {
				return tools.Errorf("%v", err), nil
			}
			if err := s.Delete(r.ID); err != nil {
				return tools.Errorf("%v", err), nil
			}
			return tools.Text(fmt.Sprintf("Routine %s (%s) deleted; its earlier runs stay in its conversation.", r.ID, r.Name)), nil
		},
	})

	// Setting up or changing a routine is confirmed by the person every
	// time, with what it will do in plain words; pausing is not.
	tools.PreExecute.Use(c, func(ctx context.Context, d *tools.Decision, next kernel.Next[*tools.Decision, *tools.Decision]) (*tools.Decision, error) {
		name := ""
		if d.Call != nil {
			name = d.Call.Name
		}
		if name != ToolCreate && name != ToolUpdate {
			return next(ctx, d)
		}
		out, err := next(ctx, d)
		if err != nil || out == nil || out.Verdict == tools.Deny {
			return out, err
		}
		var a routineArgs
		_ = json.Unmarshal(d.Call.Args, &a)
		if name == ToolUpdate {
			r, err := s.own(d.Call.Agent, a.ID)
			if err != nil {
				return out, nil // the tool answers
			}
			pauseOnly := a.Name == "" && a.Prompt == "" && a.Schedule == "" && a.Mode == "" && a.Enabled != nil && !*a.Enabled
			if pauseOnly {
				return out, nil
			}
			if a.Schedule != "" && checkAgentSchedule(a.Schedule) != nil {
				return out, nil
			}
			if _, err := modeOf(a.Mode); err != nil {
				return out, nil
			}
			merged := r
			if a.Name != "" {
				merged.Name = a.Name
			}
			if a.Prompt != "" {
				merged.Prompt = a.Prompt
			}
			if a.Schedule != "" {
				merged.Schedule = a.Schedule
			}
			if a.Mode != "" {
				merged.Mode = a.Mode
			}
			out.Summary = fmt.Sprintf("Change routine “%s”: %s · %s", merged.Name, scheduleWords(merged.Schedule), modeWords(merged.Mode))
			out.Preview = "**What it does**\n\n" + merged.Prompt
		} else {
			// what the tool would refuse anyway is not asked about
			mode, err := modeOf(a.Mode)
			if err != nil || strings.TrimSpace(a.Name) == "" || strings.TrimSpace(a.Prompt) == "" || checkAgentSchedule(a.Schedule) != nil {
				return out, nil
			}
			out.Summary = fmt.Sprintf("Set up routine “%s”: %s · %s", strings.TrimSpace(a.Name), scheduleWords(a.Schedule), modeWords(mode))
			out.Preview = "**What each run does**\n\n" + strings.TrimSpace(a.Prompt)
		}
		out.RequireApproval("routines: the person confirms every routine an agent sets up or changes")
		out.Rememberable, out.ConfirmRemember = false, false
		return out, nil
	})
}
