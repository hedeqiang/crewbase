// Package autodraft turns a stretch of conversation into a draft routine
// (例行巡检) or workflow (工作流): 从对话生成例行流程. A cheap routed model
// (else the agent's own) reads what the person and the agent did — the
// messages, the tools called and what they returned — and writes the goal,
// the steps, what "nothing to report" looks like and a schedule if the
// conversation names one. The draft is returned in the shapes the
// routines and workflows APIs take; nothing is created here: the person
// reviews the draft in the conversation and creates it through those APIs.
package autodraft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/features/routines"
	"github.com/hedeqiang/crewbase/features/schedule"
	"github.com/hedeqiang/crewbase/features/workflows"
	"github.com/hedeqiang/crewbase/kernel"
)

// Scopes of conversation a draft is made from.
const (
	ScopeTurn = "turn" // the turn ending at upto: its user message(s) and what answered them
	ScopeAll  = "all"  // everything up to upto (after the last compaction)
)

// Kinds of draft.
const (
	KindRoutine  = "routine"
	KindWorkflow = "workflow"
)

// MaxTranscript bounds the transcript the model reads (the tail is kept).
const MaxTranscript = 24000

// DefaultSchedule is proposed when the conversation names no time.
const DefaultSchedule = "daily 09:00"

// Request asks for a draft.
type Request struct {
	Agent string `json:"agent"` // whose turn; "" = the session's first agent
	Upto  int64  `json:"upto"`  // last event seq to read (0 = all)
	Scope string `json:"scope"` // turn | all (default all)
	Kind  string `json:"kind"`  // routine | workflow | "" (the model picks)
}

// Draft is what comes back: the routine and the workflow in the APIs'
// shapes, plus the parts the review card edits separately.
type Draft struct {
	Kind string `json:"kind"` // what the model recommends
	// Both: the conversation reads as a single check as well as a pipeline
	// of distinct steps, so offering the choice makes sense.
	Both     bool               `json:"both"`
	Routine  routines.Routine   `json:"routine"`
	Workflow workflows.Workflow `json:"workflow"`
	// Task: what each run checks or does (the routine prompt without Quiet).
	Task string `json:"task"`
	// Quiet: what counts as nothing to report (the run answers NO_ISSUES).
	Quiet string   `json:"quiet"`
	Tools []string `json:"tools,omitempty"` // tools the conversation used
	// ScheduleGuessed: the conversation named no time; Schedule is a default.
	ScheduleGuessed bool   `json:"scheduleGuessed,omitempty"`
	ScheduleWhy     string `json:"scheduleWhy,omitempty"`
	Model           string `json:"model,omitempty"` // provider/model that drafted it
	From            int64  `json:"from"`            // first event seq read
	Upto            int64  `json:"upto"`
}

const draftPrompt = `You turn a conversation between a person and an AI agent into something repeatable.
The person walked the agent through a task; they now want it to happen again without them.
Read the transcript and extract:
- the goal (one sentence) and the concrete steps the agent took, including which tools it used and on what (paths, repos, URLs, commands);
- what "done" looks like, and what counts as "nothing to report" (all normal);
- a schedule, only if the conversation says or clearly implies one ("every morning", "each Friday", "hourly").

Decide the kind:
- "routine": one agent repeats a check or a chore and reports only when something needs the person (most cases: monitoring, summaries, reports);
- "workflow": the task is a pipeline of distinct stages that hand work on, e.g. research → draft → review, possibly a person's approval in between.
Set "both" true when either would work.

Answer with ONE JSON object and nothing else (no code fences):
{
  "kind": "routine" | "workflow",
  "both": true | false,
  "name": "short name in the conversation's language, at most 16 characters, e.g. CI 失败巡检",
  "task": "instructions for the agent's future self for each run, in the conversation's language: what to check or do, step by step, naming the exact repos/paths/URLs/commands and tools from the conversation, and what to report when something needs attention. Self-contained: the future run does not see this conversation.",
  "quiet": "one sentence: when everything is normal and there is nothing to report",
  "tools": ["tool names the agent used"],
  "schedule": "daily HH:MM" | "weekly mon|tue|wed|thu|fri|sat|sun HH:MM" | "every Nh" | "" (empty when the conversation names no time),
  "scheduleWhy": "the words in the conversation the schedule comes from, or empty",
  "mode": "read-only" (only looks) | "sandbox" (must write files in its workspace),
  "steps": [ {"name": "short stage name", "prompt": "what this stage does, self-contained", "approval": false} ]
}
"steps" is for the workflow: 2 to 5 stages in order; a stage with "approval": true is the person approving before the next stage (its prompt is what they are shown). For a plain routine give a single step with the task.`

// transcript renders events [from, upto] for agentID as plain text: who
// said what, which tools were called with what, what they returned.
func transcript(events []session.Event, agentID string, upto int64, scope string) (string, int64, int64) {
	events = session.Visible(events, agentID)
	if upto <= 0 && len(events) > 0 {
		upto = events[len(events)-1].Seq
	}
	start := session.CompactedUpto(events, agentID)
	var in []session.Event
	for _, e := range events {
		if e.Seq > start && e.Seq <= upto {
			in = append(in, e)
		}
	}
	if scope == ScopeTurn {
		// back to the user message(s) that opened the turn ending at upto
		i := len(in) - 1
		for i >= 0 && in[i].Type != session.TypeUserMessage {
			i--
		}
		for i > 0 && in[i-1].Type == session.TypeUserMessage {
			i--
		}
		if i > 0 {
			in = in[i:]
		}
	}
	var b strings.Builder
	var from int64
	for _, e := range in {
		if e.Agent != "" && e.Agent != agentID && e.Type != session.TypeUserMessage {
			continue
		}
		line := ""
		switch e.Type {
		case session.TypeUserMessage:
			var m session.UserMessage
			if e.Decode(&m) == nil && strings.TrimSpace(m.Text) != "" {
				line = "PERSON: " + clip(m.Text, 3000)
			}
		case session.TypeAssistantMessage:
			var m session.AssistantMessage
			if e.Decode(&m) != nil {
				continue
			}
			var parts []string
			if t := strings.TrimSpace(m.Message.Text()); t != "" {
				parts = append(parts, "AGENT: "+clip(t, 3000))
			}
			for _, c := range m.Message.ToolCalls() {
				parts = append(parts, "TOOL CALL "+c.Name+" "+clip(string(c.Args), 600))
			}
			line = strings.Join(parts, "\n")
		case session.TypeToolResult:
			var r llm.ToolResult
			if e.Decode(&r) == nil {
				tag := "TOOL RESULT"
				if r.IsError {
					tag = "TOOL ERROR"
				}
				if r.Name != "" {
					tag += " " + r.Name
				}
				line = tag + ": " + clip(r.Content, 800)
			}
		case session.TypeCompaction:
			var c session.CompactionData
			if e.Decode(&c) == nil {
				line = "EARLIER (summary): " + clip(c.Summary, 3000)
			}
		}
		if line == "" {
			continue
		}
		if from == 0 {
			from = e.Seq
		}
		b.WriteString(line + "\n\n")
	}
	s := b.String()
	if len(s) > MaxTranscript {
		cut := len(s) - MaxTranscript
		for cut < len(s) && !utf8.RuneStart(s[cut]) {
			cut++
		}
		s = "…(earlier part cut)\n" + s[cut:]
	}
	return s, from, upto
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// answer is the model's JSON.
type answer struct {
	Kind        string   `json:"kind"`
	Both        bool     `json:"both"`
	Name        string   `json:"name"`
	Task        string   `json:"task"`
	Quiet       string   `json:"quiet"`
	Tools       []string `json:"tools"`
	Schedule    string   `json:"schedule"`
	ScheduleWhy string   `json:"scheduleWhy"`
	Mode        string   `json:"mode"`
	Steps       []struct {
		Name     string `json:"name"`
		Prompt   string `json:"prompt"`
		Approval bool   `json:"approval"`
	} `json:"steps"`
}

var fenceRe = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)```")

// parse finds the JSON object in a model's answer.
func parse(text string) (answer, error) {
	var a answer
	text = strings.TrimSpace(text)
	if m := fenceRe.FindStringSubmatch(text); m != nil {
		text = m[1]
	}
	i, j := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if i < 0 || j <= i {
		return a, errors.New("no JSON object in the answer")
	}
	if err := json.Unmarshal([]byte(text[i:j+1]), &a); err != nil {
		return a, err
	}
	if strings.TrimSpace(a.Task) == "" && len(a.Steps) == 0 {
		return a, errors.New("the answer has no task")
	}
	return a, nil
}

// normSchedule keeps a schedule the routines take (a repeating one), else "".
func normSchedule(s string) string {
	s = strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
	if s == "" || s == "manual" || s == "webhook" || strings.HasPrefix(s, "at ") {
		return ""
	}
	sp, err := schedule.Parse(s)
	if err != nil {
		return ""
	}
	a := sp.Next(time.Now())
	if a.IsZero() || sp.Next(a).IsZero() {
		return ""
	}
	return s
}

var stepIDRe = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// build maps the model's answer onto the routine and workflow shapes.
func build(a answer, agentID string, origin string) Draft {
	d := Draft{Kind: KindRoutine, Both: a.Both, Task: strings.TrimSpace(a.Task), Quiet: strings.TrimSpace(a.Quiet), Tools: a.Tools, ScheduleWhy: strings.TrimSpace(a.ScheduleWhy)}
	if a.Kind == KindWorkflow {
		d.Kind = KindWorkflow
	}
	name := strings.TrimSpace(a.Name)
	if name == "" {
		name = "例行任务"
	}
	if utf8.RuneCountInString(name) > 40 {
		name = string([]rune(name)[:40])
	}
	sched := normSchedule(a.Schedule)
	if sched == "" {
		sched, d.ScheduleGuessed = DefaultSchedule, true
	}
	mode := agent.ModeReadOnly
	if a.Mode == agent.ModeSandbox {
		mode = agent.ModeSandbox
	}
	if d.Task == "" {
		var parts []string
		for _, s := range a.Steps {
			if p := strings.TrimSpace(s.Prompt); p != "" && !s.Approval {
				parts = append(parts, p)
			}
		}
		d.Task = strings.Join(parts, "\n")
	}
	d.Routine = routines.Routine{Name: name, Agent: agentID, Prompt: d.Task, Schedule: sched, Mode: mode, Enabled: true, Origin: origin}

	wf := workflows.Workflow{Name: name, Description: clip(d.Task, 300), Trigger: sched}
	stepMode := mode
	if stepMode == agent.ModeReadOnly && d.Kind == KindWorkflow {
		stepMode = agent.ModeSandbox // stages hand work on: drafts are written
	}
	seen := map[string]bool{}
	for i, s := range a.Steps {
		p := strings.TrimSpace(s.Prompt)
		if p == "" {
			continue
		}
		id := strings.Trim(stepIDRe.ReplaceAllString(strings.ToLower(s.Name), "-"), "-")
		if id == "" || len(id) > 40 || seen[id] {
			id = fmt.Sprintf("step%d", i+1)
		}
		seen[id] = true
		st := workflows.Step{ID: id, Name: strings.TrimSpace(s.Name)}
		if s.Approval {
			st.Kind = workflows.KindApproval
			st.Approval = &workflows.ApprovalGate{Summary: p + "\n\n{{prev}}"}
		} else {
			st.Kind, st.Agent, st.Mode = workflows.KindAgent, agentID, stepMode
			if len(wf.Steps) == 0 {
				if !strings.Contains(p, "{{input}}") {
					p += "\n\n{{input}}"
				}
			} else if !strings.Contains(p, "{{prev}}") && !strings.Contains(p, "{{steps.") {
				p += "\n\n上一步的结果：\n{{prev}}"
			}
			st.Prompt = p
		}
		wf.Steps = append(wf.Steps, st)
	}
	if len(wf.Steps) == 0 || wf.Steps[0].Kind == workflows.KindApproval {
		wf.Steps = append([]workflows.Step{{ID: "step0", Name: name, Kind: workflows.KindAgent, Agent: agentID, Mode: stepMode, Prompt: d.Task + "\n\n{{input}}"}}, wf.Steps...)
	}
	d.Workflow = wf
	return d
}

// Service drafts automations from conversations.
type Service struct {
	c      *kernel.Context
	agents *agent.Registry
	llms   *llm.Registry
}

// candidates: the cheap summarize / title routes, then the agent's own
// models (a local-only agent only gets local routes).
func (s *Service) candidates(a *agent.Agent) []llm.ModelRef {
	var out []llm.ModelRef
	add := func(r llm.ModelRef) {
		for _, x := range out {
			if x.String() == r.String() {
				return
			}
		}
		out = append(out, r)
	}
	if rt, ok := kernel.Lookup[agentloop.Router](s.c, agentloop.RouterKey); ok {
		if r, isR := rt.(*router.Router); isR {
			for _, task := range []string{router.TaskSummarize, router.TaskTitle} {
				if ref, ok := r.RouteForAgent(task, a); ok {
					add(ref)
				}
			}
		}
	}
	for _, m := range a.Spec().Models {
		add(m)
	}
	return out
}

// Make drafts from session ss.
func (s *Service) Make(ctx context.Context, ss *session.Session, req Request) (Draft, error) {
	agentID := req.Agent
	if agentID == "" {
		if ags := ss.Meta().Agents; len(ags) > 0 {
			agentID = ags[0]
		}
	}
	a, ok := s.agents.Get(agentID)
	if !ok {
		return Draft{}, fmt.Errorf("unknown agent %q", agentID)
	}
	text, from, upto := transcript(ss.Events(), a.ID(), req.Upto, req.Scope)
	if strings.TrimSpace(text) == "" {
		return Draft{}, errors.New("nothing in this stretch of conversation to draft from")
	}
	ask := "Transcript:\n\n" + text
	switch req.Kind {
	case KindRoutine:
		ask += "\n\nThe person wants a routine (kind \"routine\")."
	case KindWorkflow:
		ask += "\n\nThe person wants a workflow (kind \"workflow\") with distinct stages."
	}
	var last error
	for _, ref := range s.candidates(a) {
		ad, found := s.llms.Get(ref.Provider)
		if !found {
			continue
		}
		tctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		resp, err := ad.Stream(tctx, llm.Request{Model: ref.Model, Effort: "minimal", MaxTokens: 2000, System: draftPrompt,
			Messages: []llm.Message{llm.TextMessage(llm.RoleUser, ask)}, Agent: a.ID(), Session: ss.ID()}, func(llm.Chunk) error { return nil })
		cancel()
		if err != nil {
			last = err
			s.c.Log().Info("automation draft: model failed, trying the next", "model", ref.String(), "err", err)
			continue
		}
		ans, err := parse(resp.Message.Text())
		if err != nil {
			last = err
			s.c.Log().Info("automation draft: unusable answer, trying the next", "model", ref.String(), "err", err)
			continue
		}
		if req.Kind == KindRoutine || req.Kind == KindWorkflow {
			ans.Kind = req.Kind
		}
		d := build(ans, a.ID(), ss.ID())
		d.Model, d.From, d.Upto = ref.String(), from, upto
		return d, nil
	}
	if last == nil {
		last = errors.New("no model available to draft with")
	}
	return Draft{}, last
}

// access is the teams plugin's view of who sees what (nil without it).
type access interface {
	Session(deviceID, id string) bool
	Agents(deviceID string, ids []string, act bool) bool
}

// Plugin serves POST /api/sessions/{id}/automation-draft.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/automation-draft",
	Inject:      []string{gateway.ServiceKey, agent.ServiceKey, llm.ServiceKey},
	Description: "Drafts a routine or workflow (从对话生成例行流程) or a skill (做成技能) from a stretch of conversation.",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		s := &Service{c: c, agents: kernel.Use[*agent.Registry](c, agent.ServiceKey), llms: kernel.Use[*llm.Registry](c, llm.ServiceKey)}
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/sessions/{id}/automation-draft",
			Validate: func(r *gateway.Request) error {
				var b Request
				if err := r.Decode(&b); err != nil {
					return err
				}
				if b.Scope != "" && b.Scope != ScopeTurn && b.Scope != ScopeAll {
					return gateway.BadRequest("scope must be turn or all")
				}
				if b.Kind != "" && b.Kind != KindRoutine && b.Kind != KindWorkflow {
					return gateway.BadRequest("kind must be routine or workflow")
				}
				return nil
			},
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				var b Request
				_ = r.Decode(&b)
				id := r.Params["id"]
				acc, _ := kernel.Lookup[access](c, "access")
				if acc != nil && !acc.Session(r.DeviceID, id) {
					return nil, gateway.NotFound()
				}
				ss, err := s.agents.Sessions().Get(id)
				if err != nil {
					return nil, gateway.NotFound()
				}
				if b.Agent != "" && acc != nil && !acc.Agents(r.DeviceID, []string{b.Agent}, true) {
					return nil, &gateway.Error{Status: 403, Code: "forbidden", Message: "you cannot give this agent a routine"}
				}
				d, err := s.Make(ctx, ss, b)
				if err != nil {
					return nil, gateway.Conflict("%v", err)
				}
				return d, nil
			}})
		// 做成技能: a SKILL.md drafted from the same stretch of conversation
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/sessions/{id}/skill-draft",
			Validate: func(r *gateway.Request) error {
				var b Request
				if err := r.Decode(&b); err != nil {
					return err
				}
				if b.Scope != "" && b.Scope != ScopeTurn && b.Scope != ScopeAll {
					return gateway.BadRequest("scope must be turn or all")
				}
				return nil
			},
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				var b Request
				_ = r.Decode(&b)
				id := r.Params["id"]
				acc, _ := kernel.Lookup[access](c, "access")
				if acc != nil && !acc.Session(r.DeviceID, id) {
					return nil, gateway.NotFound()
				}
				ss, err := s.agents.Sessions().Get(id)
				if err != nil {
					return nil, gateway.NotFound()
				}
				d, err := s.MakeSkill(ctx, ss, b)
				if err != nil {
					return nil, gateway.Conflict("%v", err)
				}
				return d, nil
			}})
		return nil
	},
})
