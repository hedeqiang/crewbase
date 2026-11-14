// Package workflows implements Workflows (PRD R-3, R-5): reusable pipelines
// "trigger → serial / parallel multi-agent steps → approval gates". A
// workflow is a plugin-provided strategy on top of the agent loop, not part
// of it: agent steps are ordinary tagged inputs in the run's own session.
//
// Definitions are JSON files (`<dir>/workflows/<id>.json`); timed and
// webhook triggers go through the schedule service (owner "workflow";
// manual workflows have no job). Every run gets its own session (kind
// "workflow") and its state is folded from that session's workflow/run and
// workflow/step events, so it survives restarts and can be rerun from a
// failed step reusing the recorded outputs of the steps before it.
package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/features/schedule"
	"github.com/hedeqiang/crewbase/kernel"
)

const ServiceKey = "workflows"

// Owner is the schedule owner kind of workflow triggers.
const Owner = "workflow"

// SessionKind is the session kind of workflow runs.
const SessionKind = "workflow"

// Session event types.
const (
	TypeRun  = "workflow/run"
	TypeStep = "workflow/step"
)

// Step kinds.
const (
	KindAgent    = "agent"
	KindParallel = "parallel"
	KindApproval = "approval"
)

// Triggers other than a schedule spec.
const (
	TriggerManual  = "manual"
	TriggerWebhook = "webhook"
)

// Run and step statuses.
const (
	StatusRunning   = "running"
	StatusWaiting   = "waiting" // approval step waiting for a person
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusRejected  = "rejected"
	StatusCancelled = "cancelled"
)

// MaxRecordedOutput bounds outputs recorded in workflow/step events.
const MaxRecordedOutput = 16000

// Step is one pipeline step.
type Step struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	Kind string `json:"kind"` // agent | parallel | approval
	// Agent steps.
	Agent string `json:"agent,omitempty"`
	// Prompt is a template: {{input}} (trigger payload / manual input),
	// {{prev}} (previous step output), {{steps.<id>}} (a named step's output).
	Prompt string `json:"prompt,omitempty"`
	Mode   string `json:"mode,omitempty"` // read-only | sandbox (default); host is forbidden
	// Parallel steps: agent steps run concurrently, outputs joined.
	Parallel []Step `json:"parallel,omitempty"`
	// Approval steps.
	Approval *ApprovalGate `json:"approval,omitempty"`
}

// ApprovalGate configures an approval step.
type ApprovalGate struct {
	Summary string `json:"summary,omitempty"` // template, shown on the approval card
}

// Workflow is a persisted definition.
type Workflow struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Trigger     string    `json:"trigger"` // manual | schedule spec | webhook
	Steps       []Step    `json:"steps"`
	Created     time.Time `json:"created,omitzero"`
	Updated     time.Time `json:"updated,omitzero"`
}

// View is a workflow with its trigger state.
type View struct {
	Workflow
	Job     string    `json:"job,omitempty"`  // schedule job id
	Hook    string    `json:"hook,omitempty"` // webhook path
	Secret  string    `json:"secret,omitempty"`
	NextRun time.Time `json:"nextRun,omitzero"`
	LastRun time.Time `json:"lastRun,omitzero"`
}

// RunEvent is the payload of workflow/run. The first event of a run carries
// the definition snapshot; later ones carry the status changes.
type RunEvent struct {
	Run        string    `json:"run"`
	Workflow   string    `json:"workflow"`
	Status     string    `json:"status"`
	Trigger    string    `json:"trigger,omitempty"` // manual | schedule | webhook | rerun
	Input      string    `json:"input,omitempty"`
	RerunOf    string    `json:"rerunOf,omitempty"`
	FromStep   string    `json:"fromStep,omitempty"`
	Output     string    `json:"output,omitempty"`
	Error      string    `json:"error,omitempty"`
	Definition *Workflow `json:"definition,omitempty"`
}

// StepEvent is the payload of workflow/step.
type StepEvent struct {
	Step   string `json:"step"`
	Group  string `json:"group,omitempty"` // parent parallel step
	Kind   string `json:"kind"`
	Agent  string `json:"agent,omitempty"`
	Status string `json:"status"`
	Input  string `json:"input,omitempty"` // queued input id (agent steps)
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
	Reused bool   `json:"reused,omitempty"` // output reused from RerunOf
}

// StepView is the folded state of one step.
type StepView struct {
	ID      string    `json:"id"`
	Group   string    `json:"group,omitempty"`
	Kind    string    `json:"kind"`
	Agent   string    `json:"agent,omitempty"`
	Status  string    `json:"status"`
	Output  string    `json:"output,omitempty"`
	Error   string    `json:"error,omitempty"`
	Reused  bool      `json:"reused,omitempty"`
	Started time.Time `json:"started,omitzero"`
	Ended   time.Time `json:"ended,omitzero"`
}

// RunView is the folded state of one run (run id = session id).
type RunView struct {
	ID       string     `json:"id"`
	Workflow string     `json:"workflow"`
	Name     string     `json:"name"`
	Status   string     `json:"status"`
	Trigger  string     `json:"trigger,omitempty"`
	Input    string     `json:"input,omitempty"`
	RerunOf  string     `json:"rerunOf,omitempty"`
	FromStep string     `json:"fromStep,omitempty"`
	Output   string     `json:"output,omitempty"`
	Error    string     `json:"error,omitempty"`
	Started  time.Time  `json:"started"`
	Ended    time.Time  `json:"ended,omitzero"`
	Steps    []StepView `json:"steps"`
	// Definition is the snapshot the run started with.
	Definition *Workflow `json:"definition,omitempty"`
}

// Done reports whether the run reached a final status.
func (r RunView) Done() bool { return r.Status != StatusRunning && r.Status != "" }

// Step returns a step's view.
func (r RunView) Step(id string) (StepView, bool) {
	for _, s := range r.Steps {
		if s.ID == id {
			return s, true
		}
	}
	return StepView{}, false
}

// ---------------------------------------------------------------- validation

var (
	idRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	stepRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	tplRe  = regexp.MustCompile(`\{\{\s*(input|prev|steps\.([A-Za-z0-9_-]+))\s*\}\}`)
	slugRe = regexp.MustCompile(`[^a-z0-9-]+`)
)

func refs(tpl string) []string {
	var out []string
	for _, m := range tplRe.FindAllStringSubmatch(tpl, -1) {
		if m[2] != "" {
			out = append(out, m[2])
		}
	}
	return out
}

// Render expands a step template.
func Render(tpl, input, prev string, outputs map[string]string) string {
	return tplRe.ReplaceAllStringFunc(tpl, func(m string) string {
		sm := tplRe.FindStringSubmatch(m)
		switch {
		case sm[1] == "input":
			return input
		case sm[1] == "prev":
			return prev
		default:
			return outputs[sm[2]]
		}
	})
}

func checkMode(m string) error {
	switch m {
	case "", agent.ModeReadOnly, agent.ModeSandbox:
		return nil
	case agent.ModeHost:
		return errors.New("workflow steps cannot run in host mode")
	}
	return fmt.Errorf("unknown mode %q", m)
}

func (s *Service) checkAgentStep(st *Step) error {
	if st.Agent == "" {
		return fmt.Errorf("step %s: agent is required", st.ID)
	}
	if _, ok := s.agents.Get(st.Agent); !ok {
		return fmt.Errorf("step %s: unknown agent %q", st.ID, st.Agent)
	}
	if strings.TrimSpace(st.Prompt) == "" {
		return fmt.Errorf("step %s: prompt is required", st.ID)
	}
	if err := checkMode(st.Mode); err != nil {
		return fmt.Errorf("step %s: %w", st.ID, err)
	}
	if st.Mode == "" {
		st.Mode = agent.ModeSandbox
	}
	return nil
}

// Check validates and normalizes a definition without touching state:
// unique step ids, known agents, no host mode, referenced steps exist and
// come earlier, a valid trigger.
func (s *Service) Check(wf Workflow) (Workflow, error) {
	wf.Name = strings.TrimSpace(wf.Name)
	if wf.Name == "" {
		return wf, errors.New("name is required")
	}
	if wf.ID != "" && !idRe.MatchString(wf.ID) {
		return wf, fmt.Errorf("invalid workflow id %q", wf.ID)
	}
	switch wf.Trigger {
	case "", TriggerManual:
		wf.Trigger = TriggerManual
	case TriggerWebhook:
	default:
		if _, err := schedule.Parse(wf.Trigger); err != nil {
			return wf, fmt.Errorf("trigger: %w", err)
		}
	}
	if len(wf.Steps) == 0 {
		return wf, errors.New("a workflow needs at least one step")
	}
	steps := make([]Step, len(wf.Steps))
	copy(steps, wf.Steps)
	pos := map[string]int{} // step id → top-level index
	type pendingRef struct {
		at        int
		step, ref string
	}
	var pending []pendingRef
	for i := range steps {
		st := &steps[i]
		if !stepRe.MatchString(st.ID) {
			return wf, fmt.Errorf("step %d: invalid id %q", i+1, st.ID)
		}
		if _, dup := pos[st.ID]; dup {
			return wf, fmt.Errorf("duplicate step id %q", st.ID)
		}
		pos[st.ID] = i
		if st.Kind == "" {
			st.Kind = KindAgent
			if len(st.Parallel) > 0 {
				st.Kind = KindParallel
			}
		}
		var tpls []string
		switch st.Kind {
		case KindAgent:
			if len(st.Parallel) > 0 {
				return wf, fmt.Errorf("step %s: only parallel steps have sub-steps", st.ID)
			}
			if err := s.checkAgentStep(st); err != nil {
				return wf, err
			}
			tpls = append(tpls, st.Prompt)
		case KindParallel:
			if len(st.Parallel) == 0 {
				return wf, fmt.Errorf("step %s: parallel step needs sub-steps", st.ID)
			}
			subs := make([]Step, len(st.Parallel))
			copy(subs, st.Parallel)
			for j := range subs {
				sub := &subs[j]
				if !stepRe.MatchString(sub.ID) {
					return wf, fmt.Errorf("step %s: sub-step %d: invalid id %q", st.ID, j+1, sub.ID)
				}
				if _, dup := pos[sub.ID]; dup {
					return wf, fmt.Errorf("duplicate step id %q", sub.ID)
				}
				pos[sub.ID] = i
				if sub.Kind == "" {
					sub.Kind = KindAgent
				}
				if sub.Kind != KindAgent || len(sub.Parallel) > 0 {
					return wf, fmt.Errorf("step %s: sub-steps of a parallel step must be agent steps", sub.ID)
				}
				if err := s.checkAgentStep(sub); err != nil {
					return wf, err
				}
				tpls = append(tpls, sub.Prompt)
			}
			st.Parallel = subs
			st.Agent, st.Prompt, st.Mode = "", "", ""
		case KindApproval:
			if len(st.Parallel) > 0 || st.Agent != "" {
				return wf, fmt.Errorf("step %s: approval steps have no agent or sub-steps", st.ID)
			}
			if st.Approval == nil {
				st.Approval = &ApprovalGate{}
			}
			tpls = append(tpls, st.Approval.Summary)
		default:
			return wf, fmt.Errorf("step %s: unknown kind %q", st.ID, st.Kind)
		}
		for _, t := range tpls {
			for _, ref := range refs(t) {
				pending = append(pending, pendingRef{i, st.ID, ref})
			}
		}
	}
	for _, r := range pending {
		p, ok := pos[r.ref]
		if !ok {
			return wf, fmt.Errorf("step %s: {{steps.%s}} refers to an unknown step", r.step, r.ref)
		}
		if p >= r.at {
			return wf, fmt.Errorf("step %s: {{steps.%s}} must refer to an earlier step", r.step, r.ref)
		}
	}
	wf.Steps = steps
	return wf, nil
}

func agentIDs(wf Workflow) []string {
	var out []string
	seen := map[string]bool{}
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, st := range wf.Steps {
		add(st.Agent)
		for _, sub := range st.Parallel {
			add(sub.Agent)
		}
	}
	return out
}

// ---------------------------------------------------------------- service

type liveRun struct {
	id     string
	wf     Workflow
	ss     *session.Session
	input  string
	cancel context.CancelFunc
	done   chan struct{}
}

// Service is the `workflows` service.
type Service struct {
	c        *kernel.Context
	sched    *schedule.Service
	agents   *agent.Registry
	sessions *session.Store
	dir      string // "" = definitions in memory only
	w        *waiter

	mu    sync.Mutex
	defs  map[string]Workflow
	index map[string][]string // workflow id → run ids
	live  map[string]*liveRun
}

func jobID(wfID string) string { return "wf-" + wfID }

func (s *Service) path(id string) string { return filepath.Join(s.dir, id+".json") }

func (s *Service) persist(wf Workflow) error {
	if s.dir == "" {
		return nil
	}
	b, _ := json.MarshalIndent(wf, "", "  ")
	tmp := s.path(wf.ID) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(wf.ID))
}

func (s *Service) load() error {
	if s.dir == "" {
		return nil
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var wf Workflow
		if json.Unmarshal(b, &wf) != nil || wf.ID == "" {
			s.c.Log().Warn("skipping invalid workflow file", "file", e.Name())
			continue
		}
		s.defs[wf.ID] = wf
	}
	return nil
}

func (s *Service) view(wf Workflow) View {
	v := View{Workflow: wf}
	if j, ok := s.sched.Get(jobID(wf.ID)); ok && j.Owner == Owner {
		v.Job, v.NextRun, v.LastRun = j.ID, j.NextFire, j.LastFire
		if j.IsWebhook() {
			v.Hook = "/api/hooks/" + j.ID
		}
	}
	return v
}

// syncJob makes the schedule job match wf.Trigger. It returns the webhook
// secret when a webhook job was (re)created.
func (s *Service) syncJob(wf Workflow) (string, error) {
	id := jobID(wf.ID)
	j, exists := s.sched.Get(id)
	if wf.Trigger == TriggerManual {
		if exists {
			return "", s.sched.Remove(id)
		}
		return "", nil
	}
	if exists && j.Owner == Owner && j.Spec == wf.Trigger {
		if j.Name != wf.Name {
			_, err := s.sched.Update(id, func(x *schedule.Job) { x.Name = wf.Name })
			return "", err
		}
		return "", nil
	}
	if exists {
		if err := s.sched.Remove(id); err != nil {
			return "", err
		}
	}
	payload, _ := json.Marshal(map[string]string{"workflow": wf.ID})
	nj, err := s.sched.Add(schedule.Job{ID: id, Owner: Owner, Name: wf.Name, Spec: wf.Trigger, Payload: payload})
	if err != nil {
		return "", err
	}
	return nj.Secret, nil
}

// Create validates, persists and schedules a workflow.
func (s *Service) Create(wf Workflow) (View, error) {
	wf, err := s.Check(wf)
	if err != nil {
		return View{}, err
	}
	s.mu.Lock()
	if wf.ID == "" {
		base := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(wf.Name), "-"), "-")
		if base == "" || !idRe.MatchString(base) {
			base = "workflow"
		}
		if len(base) > 50 {
			base = strings.TrimRight(base[:50], "-")
		}
		wf.ID = base
		for i := 2; s.defs[wf.ID].ID != ""; i++ {
			wf.ID = fmt.Sprintf("%s-%d", base, i)
		}
	} else if _, ok := s.defs[wf.ID]; ok {
		s.mu.Unlock()
		return View{}, fmt.Errorf("workflow %s exists", wf.ID)
	}
	now := time.Now().UTC()
	wf.Created, wf.Updated = now, now
	s.defs[wf.ID] = wf
	s.mu.Unlock()
	if err := s.persist(wf); err != nil {
		s.mu.Lock()
		delete(s.defs, wf.ID)
		s.mu.Unlock()
		return View{}, err
	}
	secret, err := s.syncJob(wf)
	if err != nil {
		_ = s.Delete(wf.ID)
		return View{}, err
	}
	v := s.view(wf)
	v.Secret = secret
	return v, nil
}

// Update replaces a definition (id, created are kept).
func (s *Service) Update(id string, wf Workflow) (View, error) {
	s.mu.Lock()
	cur, ok := s.defs[id]
	s.mu.Unlock()
	if !ok {
		return View{}, fmt.Errorf("workflow %s not found", id)
	}
	wf.ID = id
	wf, err := s.Check(wf)
	if err != nil {
		return View{}, err
	}
	wf.Created, wf.Updated = cur.Created, time.Now().UTC()
	if err := s.persist(wf); err != nil {
		return View{}, err
	}
	s.mu.Lock()
	s.defs[id] = wf
	s.mu.Unlock()
	secret, err := s.syncJob(wf)
	if err != nil {
		return View{}, err
	}
	v := s.view(wf)
	v.Secret = secret
	return v, nil
}

// Delete removes a definition and its trigger (run sessions stay).
func (s *Service) Delete(id string) error {
	s.mu.Lock()
	_, ok := s.defs[id]
	delete(s.defs, id)
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("workflow %s not found", id)
	}
	if _, exists := s.sched.Get(jobID(id)); exists {
		_ = s.sched.Remove(jobID(id))
	}
	if s.dir != "" {
		if err := os.Remove(s.path(id)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// Get returns a workflow.
func (s *Service) Get(id string) (View, bool) {
	s.mu.Lock()
	wf, ok := s.defs[id]
	s.mu.Unlock()
	if !ok {
		return View{}, false
	}
	return s.view(wf), true
}

// List returns all workflows ordered by creation.
func (s *Service) List() []View {
	s.mu.Lock()
	wfs := make([]Workflow, 0, len(s.defs))
	for _, wf := range s.defs {
		wfs = append(wfs, wf)
	}
	s.mu.Unlock()
	sort.Slice(wfs, func(i, j int) bool {
		if wfs[i].Created.Equal(wfs[j].Created) {
			return wfs[i].ID < wfs[j].ID
		}
		return wfs[i].Created.Before(wfs[j].Created)
	})
	out := make([]View, 0, len(wfs))
	for _, wf := range wfs {
		out = append(out, s.view(wf))
	}
	return out
}

// ---------------------------------------------------------------- runs

// Start runs a workflow now with input (manual run).
func (s *Service) Start(id, input string) (RunView, error) {
	return s.startTrigger(id, input, TriggerManual)
}

func (s *Service) startTrigger(id, input, trigger string) (RunView, error) {
	s.mu.Lock()
	wf, ok := s.defs[id]
	s.mu.Unlock()
	if !ok {
		return RunView{}, fmt.Errorf("workflow %s not found", id)
	}
	return s.start(wf, input, RunEvent{Trigger: trigger}, 0, nil, nil)
}

// fire is the schedule handler. It runs under the schedule lock, so it
// only starts the run.
func (s *Service) fire(_ context.Context, f schedule.Firing) error {
	var p struct {
		Workflow string `json:"workflow"`
	}
	_ = json.Unmarshal(f.Job.Payload, &p)
	trig := "schedule"
	if f.Job.IsWebhook() {
		trig = TriggerWebhook
	}
	_, err := s.startTrigger(p.Workflow, inputFromBody(f.Body), trig)
	return err
}

// inputFromBody turns a trigger payload into {{input}}: a JSON string is
// unquoted, {"input": "..."} is unwrapped, anything else is used verbatim.
func inputFromBody(b json.RawMessage) string {
	if len(b) == 0 {
		return ""
	}
	var str string
	if json.Unmarshal(b, &str) == nil {
		return str
	}
	var obj struct {
		Input *string `json:"input"`
	}
	if json.Unmarshal(b, &obj) == nil && obj.Input != nil {
		return *obj.Input
	}
	return string(b)
}

// start creates the run session and launches execution from step index
// from, with reused outputs prefilled (rerun).
func (s *Service) start(wf Workflow, input string, head RunEvent, from int, reused []StepEvent, outputs map[string]string) (RunView, error) {
	if s.c.Disposed() {
		return RunView{}, errors.New("workflows service stopped")
	}
	ss, err := s.sessions.Create(session.Meta{Kind: SessionKind, Title: wf.Name, Agents: agentIDs(wf)})
	if err != nil {
		return RunView{}, err
	}
	head.Run, head.Workflow, head.Status, head.Input = ss.ID(), wf.ID, StatusRunning, input
	def := wf
	head.Definition = &def
	if _, err := ss.Append(TypeRun, "", head); err != nil {
		return RunView{}, err
	}
	for _, r := range reused {
		if _, err := ss.Append(TypeStep, r.Agent, r); err != nil {
			return RunView{}, err
		}
	}
	if outputs == nil {
		outputs = map[string]string{}
	}
	ctx, cancel := context.WithCancel(s.c.Context())
	lr := &liveRun{id: ss.ID(), wf: wf, ss: ss, input: input, cancel: cancel, done: make(chan struct{})}
	s.mu.Lock()
	s.live[lr.id] = lr
	s.index[wf.ID] = append(s.index[wf.ID], lr.id)
	s.mu.Unlock()
	view := buildRun(ss)
	s.c.Go(func(context.Context) {
		defer func() {
			cancel()
			s.mu.Lock()
			delete(s.live, lr.id)
			s.mu.Unlock()
			close(lr.done)
		}()
		s.execute(ctx, lr, from, outputs)
	})
	return view, nil
}

type stepErr struct {
	status string
	msg    string
}

func (e *stepErr) Error() string { return e.msg }

func failWith(status, format string, a ...any) error {
	return &stepErr{status: status, msg: fmt.Sprintf(format, a...)}
}

func statusOf(ctx context.Context, err error) (string, string) {
	if err == nil {
		return StatusCompleted, ""
	}
	var se *stepErr
	if errors.As(err, &se) {
		return se.status, se.msg
	}
	if ctx.Err() != nil {
		return StatusCancelled, "cancelled"
	}
	return StatusFailed, err.Error()
}

func (s *Service) execute(ctx context.Context, lr *liveRun, from int, outputs map[string]string) {
	prev := lr.input
	if from > 0 {
		prev = outputs[lr.wf.Steps[from-1].ID]
	}
	var mu sync.Mutex // guards outputs during parallel steps
	for i := from; i < len(lr.wf.Steps); i++ {
		st := lr.wf.Steps[i]
		if ctx.Err() != nil {
			s.finish(lr, StatusCancelled, "", "cancelled")
			return
		}
		out, err := s.runStep(ctx, lr, st, prev, outputs, &mu)
		if err != nil {
			status, msg := statusOf(ctx, err)
			s.finish(lr, status, "", fmt.Sprintf("step %s: %s", st.ID, msg))
			return
		}
		mu.Lock()
		outputs[st.ID] = out
		mu.Unlock()
		prev = out
	}
	s.finish(lr, StatusCompleted, prev, "")
}

func (s *Service) finish(lr *liveRun, status, output, msg string) {
	_, _ = lr.ss.Append(TypeRun, "", RunEvent{Run: lr.id, Workflow: lr.wf.ID, Status: status, Output: truncate(output, MaxRecordedOutput), Error: msg})
}

func (s *Service) record(lr *liveRun, ev StepEvent) {
	ev.Output = truncate(ev.Output, MaxRecordedOutput)
	_, _ = lr.ss.Append(TypeStep, ev.Agent, ev)
}

func (s *Service) runStep(ctx context.Context, lr *liveRun, st Step, prev string, outputs map[string]string, mu *sync.Mutex) (string, error) {
	snapshot := func() map[string]string {
		mu.Lock()
		defer mu.Unlock()
		m := make(map[string]string, len(outputs))
		for k, v := range outputs {
			m[k] = v
		}
		return m
	}
	switch st.Kind {
	case KindAgent:
		return s.runAgentStep(ctx, lr, st, "", Render(st.Prompt, lr.input, prev, snapshot()))
	case KindApproval:
		return s.runApproval(ctx, lr, st, prev, snapshot())
	case KindParallel:
		s.record(lr, StepEvent{Step: st.ID, Kind: KindParallel, Status: StatusRunning})
		outs := snapshot()
		gctx, cancel := context.WithCancel(ctx)
		defer cancel()
		results := make([]string, len(st.Parallel))
		errs := make([]error, len(st.Parallel))
		var wg sync.WaitGroup
		for j, sub := range st.Parallel {
			wg.Add(1)
			go func() {
				defer wg.Done()
				out, err := s.runAgentStep(gctx, lr, sub, st.ID, Render(sub.Prompt, lr.input, prev, outs))
				results[j], errs[j] = out, err
				if err != nil {
					cancel() // first failure stops the siblings
					return
				}
				mu.Lock()
				outputs[sub.ID] = out
				mu.Unlock()
			}()
		}
		wg.Wait()
		// Report the root cause, not the siblings cancelled because of it.
		var first error
		for j, err := range errs {
			if err == nil {
				continue
			}
			wrapped := fmt.Errorf("%s: %w", st.Parallel[j].ID, err)
			if first == nil {
				first = wrapped
			}
			if status, _ := statusOf(gctx, err); status != StatusCancelled {
				first = wrapped
				break
			}
		}
		if first != nil {
			status, _ := statusOf(ctx, first)
			msg := first.Error()
			if ctx.Err() == nil && status == StatusCancelled {
				status = StatusFailed
			}
			s.record(lr, StepEvent{Step: st.ID, Kind: KindParallel, Status: status, Error: msg})
			return "", &stepErr{status: status, msg: msg}
		}
		var b strings.Builder
		for j, sub := range st.Parallel {
			if j > 0 {
				b.WriteString("\n\n")
			}
			title := sub.Name
			if title == "" {
				title = sub.ID
			}
			b.WriteString("## " + title + "\n" + results[j])
		}
		s.record(lr, StepEvent{Step: st.ID, Kind: KindParallel, Status: StatusCompleted, Output: b.String()})
		return b.String(), nil
	}
	return "", failWith(StatusFailed, "unknown step kind %q", st.Kind)
}

func (s *Service) runApproval(ctx context.Context, lr *liveRun, st Step, prev string, outputs map[string]string) (string, error) {
	summary := ""
	if st.Approval != nil {
		summary = Render(st.Approval.Summary, lr.input, prev, outputs)
	}
	if strings.TrimSpace(summary) == "" {
		name := st.Name
		if name == "" {
			name = st.ID
		}
		summary = fmt.Sprintf("Workflow %q: continue past %q?", lr.wf.Name, name)
	}
	s.record(lr, StepEvent{Step: st.ID, Kind: KindApproval, Status: StatusWaiting})
	d := approval.Decision{Outcome: approval.OutcomeUnavailable, Message: "approval service unavailable"}
	if svc, ok := kernel.Lookup[*approval.Service](s.c, approval.ServiceKey); ok {
		d = svc.Ask(ctx, lr.ss, approval.Request{
			CallID: lr.id + "/" + st.ID, Session: lr.id, Tool: "workflow", Category: "workflow",
			Summary: oneLine(summary, 160), Preview: truncateRunes(prev, 4000), Reason: "workflow approval gate", Rememberable: false,
		})
	}
	if d.Outcome == approval.OutcomeAllowedOnce {
		s.record(lr, StepEvent{Step: st.ID, Kind: KindApproval, Status: StatusCompleted, Output: prev})
		return prev, nil // the gate passes the previous output through
	}
	status := StatusRejected
	if ctx.Err() != nil {
		status = StatusCancelled
	}
	msg := string(d.Outcome)
	if d.Message != "" {
		msg += ": " + d.Message
	}
	s.record(lr, StepEvent{Step: st.ID, Kind: KindApproval, Status: status, Error: msg})
	return "", &stepErr{status: status, msg: msg}
}

func (s *Service) runAgentStep(ctx context.Context, lr *liveRun, st Step, group, text string) (string, error) {
	a, ok := s.agents.Get(st.Agent)
	if !ok {
		msg := fmt.Sprintf("agent %s not found", st.Agent)
		s.record(lr, StepEvent{Step: st.ID, Group: group, Kind: KindAgent, Agent: st.Agent, Status: StatusFailed, Error: msg})
		return "", failWith(StatusFailed, "%s", msg)
	}
	mode := st.Mode
	if mode == "" || mode == agent.ModeHost {
		mode = agent.ModeSandbox
	}
	tag := "workflow:" + lr.id + ":" + st.ID
	ch := s.w.expect(tag)
	defer s.w.done(tag)
	in, err := a.Enqueue(agent.Input{Session: lr.id, Text: text, From: "workflow", Mode: mode, Tag: tag})
	if err != nil {
		s.record(lr, StepEvent{Step: st.ID, Group: group, Kind: KindAgent, Agent: a.ID(), Status: StatusFailed, Error: err.Error()})
		return "", failWith(StatusFailed, "enqueue: %v", err)
	}
	s.record(lr, StepEvent{Step: st.ID, Group: group, Kind: KindAgent, Agent: a.ID(), Status: StatusRunning, Input: in.ID})
	select {
	case e := <-ch:
		out := turnOutput(lr.ss, a.ID(), in.ID)
		if out == "" {
			out = e.Text
		}
		switch e.Status {
		case "completed":
			s.record(lr, StepEvent{Step: st.ID, Group: group, Kind: KindAgent, Agent: a.ID(), Status: StatusCompleted, Input: in.ID, Output: out})
			return out, nil
		case "cancelled":
			status := StatusCancelled
			if ctx.Err() == nil {
				status = StatusFailed // stopped from outside the workflow
			}
			s.record(lr, StepEvent{Step: st.ID, Group: group, Kind: KindAgent, Agent: a.ID(), Status: status, Input: in.ID, Error: "agent turn cancelled"})
			return "", failWith(status, "agent turn cancelled")
		default:
			msg := e.Error
			if msg == "" {
				msg = "agent turn " + e.Status
			}
			s.record(lr, StepEvent{Step: st.ID, Group: group, Kind: KindAgent, Agent: a.ID(), Status: StatusFailed, Input: in.ID, Error: msg})
			return "", failWith(StatusFailed, "%s", msg)
		}
	case <-ctx.Done():
		if !a.Cancel(in.ID) && s.w.isRunning(tag) {
			a.StopInput(in.ID)
			select {
			case <-ch:
			case <-time.After(5 * time.Second):
			}
		}
		s.record(lr, StepEvent{Step: st.ID, Group: group, Kind: KindAgent, Agent: a.ID(), Status: StatusCancelled, Input: in.ID, Error: "cancelled"})
		return "", failWith(StatusCancelled, "cancelled")
	}
}

// Cancel stops a running run.
func (s *Service) Cancel(runID string) error {
	s.mu.Lock()
	lr, ok := s.live[runID]
	s.mu.Unlock()
	if ok {
		lr.cancel()
		return nil
	}
	rv, err := s.Run(runID)
	if err != nil {
		return err
	}
	if rv.Done() {
		return fmt.Errorf("run %s is already %s", runID, rv.Status)
	}
	ss, err := s.sessions.Get(runID)
	if err != nil {
		return err
	}
	_, err = ss.Append(TypeRun, "", RunEvent{Run: runID, Workflow: rv.Workflow, Status: StatusCancelled, Error: "cancelled"})
	return err
}

// Wait blocks until a live run finishes (or ctx ends) and returns its state.
func (s *Service) Wait(ctx context.Context, runID string) (RunView, error) {
	s.mu.Lock()
	lr, ok := s.live[runID]
	s.mu.Unlock()
	if ok {
		select {
		case <-lr.done:
		case <-ctx.Done():
			return RunView{}, ctx.Err()
		}
	}
	return s.Run(runID)
}

// Run returns a run's state, folded from its session events.
func (s *Service) Run(runID string) (RunView, error) {
	ss, err := s.sessions.Get(runID)
	if err != nil || ss.Meta().Kind != SessionKind {
		return RunView{}, fmt.Errorf("run %s not found", runID)
	}
	rv := buildRun(ss)
	if rv.Workflow == "" {
		return RunView{}, fmt.Errorf("run %s not found", runID)
	}
	return rv, nil
}

// Runs lists a workflow's runs, newest first.
func (s *Service) Runs(id string) ([]RunView, error) {
	s.mu.Lock()
	_, ok := s.defs[id]
	ids := append([]string(nil), s.index[id]...)
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("workflow %s not found", id)
	}
	out := make([]RunView, 0, len(ids))
	for _, rid := range ids {
		if rv, err := s.Run(rid); err == nil {
			out = append(out, rv)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	return out, nil
}

// Rerun starts a new run of the run's workflow (current definition) from
// step fromStep ("" = the first step that did not complete), reusing the
// recorded outputs of every step before it.
func (s *Service) Rerun(runID, fromStep string) (RunView, error) {
	src, err := s.Run(runID)
	if err != nil {
		return RunView{}, err
	}
	if !src.Done() {
		return RunView{}, fmt.Errorf("run %s is still running", runID)
	}
	s.mu.Lock()
	wf, ok := s.defs[src.Workflow]
	s.mu.Unlock()
	if !ok {
		return RunView{}, fmt.Errorf("workflow %s no longer exists", src.Workflow)
	}
	if fromStep == "" {
		for _, st := range wf.Steps {
			if sv, ok := src.Step(st.ID); !ok || sv.Status != StatusCompleted {
				fromStep = st.ID
				break
			}
		}
		if fromStep == "" {
			return RunView{}, errors.New("every step completed; name the step to rerun from")
		}
	}
	from := -1
	for i, st := range wf.Steps {
		if st.ID == fromStep {
			from = i
		}
	}
	if from < 0 {
		return RunView{}, fmt.Errorf("unknown top-level step %q", fromStep)
	}
	outputs := map[string]string{}
	var reused []StepEvent
	take := func(st Step, group string) error {
		sv, ok := src.Step(st.ID)
		if !ok || sv.Status != StatusCompleted {
			return fmt.Errorf("step %s has no completed output in run %s", st.ID, runID)
		}
		outputs[st.ID] = sv.Output
		reused = append(reused, StepEvent{Step: st.ID, Group: group, Kind: st.Kind, Agent: sv.Agent, Status: StatusCompleted, Output: sv.Output, Reused: true})
		return nil
	}
	for _, st := range wf.Steps[:from] {
		for _, sub := range st.Parallel {
			if err := take(sub, st.ID); err != nil {
				return RunView{}, err
			}
		}
		if err := take(st, ""); err != nil {
			return RunView{}, err
		}
	}
	return s.start(wf, src.Input, RunEvent{Trigger: "rerun", RerunOf: runID, FromStep: fromStep}, from, reused, outputs)
}

// buildRun folds a run session's events.
func buildRun(ss *session.Session) RunView {
	rv := RunView{ID: ss.ID(), Name: ss.Meta().Title, Steps: []StepView{}}
	idx := map[string]int{}
	for _, e := range ss.Events() {
		switch e.Type {
		case TypeRun:
			var r RunEvent
			if e.Decode(&r) != nil {
				continue
			}
			if rv.Workflow == "" {
				rv.Workflow, rv.Trigger, rv.Input = r.Workflow, r.Trigger, r.Input
				rv.RerunOf, rv.FromStep, rv.Definition = r.RerunOf, r.FromStep, r.Definition
				rv.Started = e.Time
			}
			rv.Status = r.Status
			if r.Status != StatusRunning {
				rv.Output, rv.Error, rv.Ended = r.Output, r.Error, e.Time
			}
		case TypeStep:
			var st StepEvent
			if e.Decode(&st) != nil {
				continue
			}
			i, ok := idx[st.Step]
			if !ok {
				i = len(rv.Steps)
				idx[st.Step] = i
				rv.Steps = append(rv.Steps, StepView{ID: st.Step, Started: e.Time})
			}
			v := &rv.Steps[i]
			v.Group, v.Kind, v.Status, v.Reused = st.Group, st.Kind, st.Status, st.Reused
			if st.Agent != "" {
				v.Agent = st.Agent
			}
			v.Output, v.Error = st.Output, st.Error
			if st.Status != StatusRunning && st.Status != StatusWaiting {
				v.Ended = e.Time
			}
		}
	}
	return rv
}

// turnOutput returns the final assistant text agentID produced for input
// inputID in ss (robust when parallel agents share the run session).
func turnOutput(ss *session.Session, agentID, inputID string) string {
	in := false
	text := ""
	for _, e := range ss.Events() {
		if e.Agent != agentID {
			continue
		}
		switch e.Type {
		case session.TypeTurnStart:
			var ts agentloop.TurnStart
			if e.Decode(&ts) == nil {
				in = ts.Input == inputID
			}
		case session.TypeAssistantMessage:
			if !in {
				continue
			}
			var m session.AssistantMessage
			if e.Decode(&m) == nil {
				if t := m.Message.Text(); t != "" {
					text = t
				}
			}
		case session.TypeTurnEnd:
			if in {
				return text
			}
		}
	}
	return text
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// recoverRuns folds every existing run session into the index and closes runs
// left "running" by a previous process.
func (s *Service) recoverRuns() {
	for _, m := range s.sessions.List() {
		if m.Kind != SessionKind {
			continue
		}
		ss, err := s.sessions.Get(m.ID)
		if err != nil {
			continue
		}
		rv := buildRun(ss)
		if rv.Workflow == "" {
			continue
		}
		s.index[rv.Workflow] = append(s.index[rv.Workflow], rv.ID)
		if !rv.Done() {
			_, _ = ss.Append(TypeRun, "", RunEvent{Run: rv.ID, Workflow: rv.Workflow, Status: StatusFailed, Error: "interrupted: the process stopped during the run; rerun from the failed step"})
		}
	}
}

// reconcileJobs makes schedule jobs match the loaded definitions.
func (s *Service) reconcileJobs() {
	for _, j := range s.sched.List(Owner) {
		id := strings.TrimPrefix(j.ID, "wf-")
		if _, ok := s.Get(id); !ok {
			_ = s.sched.Remove(j.ID)
		}
	}
	for _, v := range s.List() {
		if _, err := s.syncJob(v.Workflow); err != nil {
			s.c.Log().Warn("workflow trigger", "workflow", v.ID, "err", err)
		}
	}
}

// ---------------------------------------------------------------- waiter

type waiter struct {
	mu      sync.Mutex
	ends    map[string]chan agentloop.TurnEndEvent
	running map[string]bool
}

func newWaiter(c *kernel.Context) *waiter {
	w := &waiter{ends: map[string]chan agentloop.TurnEndEvent{}, running: map[string]bool{}}
	agentloop.TurnStarted.Listen(c, func(e agentloop.TurnStartEvent) {
		w.mu.Lock()
		if _, ok := w.ends[e.Input.Tag]; ok {
			w.running[e.Input.Tag] = true
		}
		w.mu.Unlock()
	})
	agentloop.TurnEnded.Listen(c, func(e agentloop.TurnEndEvent) {
		w.mu.Lock()
		ch, ok := w.ends[e.Input.Tag]
		delete(w.running, e.Input.Tag)
		w.mu.Unlock()
		if ok {
			select {
			case ch <- e:
			default:
			}
		}
	})
	return w
}

func (w *waiter) expect(tag string) <-chan agentloop.TurnEndEvent {
	ch := make(chan agentloop.TurnEndEvent, 1)
	w.mu.Lock()
	w.ends[tag] = ch
	w.mu.Unlock()
	return ch
}

func (w *waiter) done(tag string) {
	w.mu.Lock()
	delete(w.ends, tag)
	delete(w.running, tag)
	w.mu.Unlock()
}

func (w *waiter) isRunning(tag string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running[tag]
}

// ---------------------------------------------------------------- plugin

// Config of the workflows plugin: {"dir": "${root}"} stores definitions in
// ${root}/workflows/<id>.json ("" keeps them in memory).
type Config struct {
	Dir string `json:"dir"`
}

// Plugin provides the `workflows` service.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/workflows",
	Inject:      []string{schedule.ServiceKey, agent.ServiceKey, session.ServiceKey},
	Optional:    []string{approval.ServiceKey},
	Description: "Workflows: trigger → serial/parallel agent steps → approval gates, rerun from a failed step.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		s := &Service{c: c,
			sched:    kernel.Use[*schedule.Service](c, schedule.ServiceKey),
			agents:   kernel.Use[*agent.Registry](c, agent.ServiceKey),
			sessions: kernel.Use[*session.Store](c, session.ServiceKey),
			defs:     map[string]Workflow{}, index: map[string][]string{}, live: map[string]*liveRun{},
		}
		if cfg.Dir != "" {
			s.dir = filepath.Join(cfg.Dir, "workflows")
		}
		if err := s.load(); err != nil {
			return err
		}
		s.w = newWaiter(c)
		s.recoverRuns()
		s.sched.Handle(c, Owner, s.fire)
		s.reconcileJobs()
		return c.Provide(ServiceKey, s)
	},
})

// oneLine squashes s to its first line, at most n runes.
func oneLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i]) + " …"
	}
	return truncateRunes(s, n)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
