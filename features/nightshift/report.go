package nightshift

// 值班报告: what the agent did (the routine's runs during the shift and
// what started each), what it changed (its git events and the commits on
// its folder), what failed, and what waits for the person's approval. It
// is built only from what happened; a shift with nothing in it says so.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/notify"
	"github.com/hedeqiang/crewbase/features/routines"
)

// Handled is one run of the agent and what started it.
type Handled struct {
	Run     string    `json:"run"`
	Events  []Event   `json:"events"`
	Status  string    `json:"status"` // running | quiet | attention | failed | "" (did not run)
	Summary string    `json:"summary,omitempty"`
	Started time.Time `json:"started,omitzero"`
	Ended   time.Time `json:"ended,omitzero"`
}

// Waiting is an approval still waiting for the person (需要你).
type Waiting struct {
	CallID   string    `json:"callId"`
	Session  string    `json:"session"`
	Tool     string    `json:"tool"`
	Category string    `json:"category"`
	Summary  string    `json:"summary"`
	Asked    time.Time `json:"asked"`
}

// Report is one shift's report.
type Report struct {
	ID      string    `json:"id"`
	Agent   string    `json:"agent"`
	Name    string    `json:"name"`
	Session string    `json:"session"` // the agent's conversation (the card is on top of it)
	Routine string    `json:"routine"`
	Work    string    `json:"work"` // the routine's conversation, where the night's work is
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
	Early   bool      `json:"early,omitempty"`
	Source  string    `json:"source,omitempty"`
	Polls   int       `json:"polls"`
	Handled []Handled `json:"handled"`
	Changes []Change  `json:"changes"`
	Failed  []Problem `json:"failed"`
	Waiting []Waiting `json:"waiting"`
	Seen    bool      `json:"seen,omitempty"`
}

// Empty: nothing came in, nothing changed, nothing waits.
func (r Report) Empty() bool {
	return len(r.Handled)+len(r.Changes)+len(r.Failed)+len(r.Waiting) == 0
}

func (s *Service) compose(ctx context.Context, c Config, sh *Shift, end time.Time, early bool) Report {
	r := Report{ID: newID("nr_"), Agent: sh.Agent, Routine: c.Routine, Start: sh.Start, End: end, Early: early, Source: c.source(), Polls: sh.Polls,
		Handled: []Handled{}, Changes: []Change{}, Failed: []Problem{}, Waiting: []Waiting{}}
	a, ok := s.agents.Get(sh.Agent)
	if ok {
		r.Name, r.Session = a.Spec().Name, a.Spec().Session
	}
	// the runs: by key, those of the shift
	byKey := map[string]routines.Run{}
	var order []string
	if rt, ok := s.rts.Get(c.Routine); ok {
		r.Work = rt.Session
		runs, _ := s.rts.Runs(c.Routine)
		for i := len(runs) - 1; i >= 0; i-- { // oldest first
			x := runs[i]
			if x.Started.Before(sh.Start) || x.Started.After(end) {
				continue
			}
			byKey[x.Key] = x
			order = append(order, x.Key)
		}
	}
	// what came in, grouped by the run it started
	idx := map[string]int{}
	for _, e := range sh.Events {
		i, ok := idx[e.Run]
		if !ok || e.Run == "" {
			h := Handled{Run: e.Run}
			if x, ok := byKey[e.Run]; ok {
				h.Status, h.Summary, h.Started, h.Ended = x.Status, x.Summary, x.Started, x.Ended
			}
			r.Handled = append(r.Handled, h)
			i = len(r.Handled) - 1
			idx[e.Run] = i
		}
		r.Handled[i].Events = append(r.Handled[i].Events, e)
	}
	// runs nothing recorded started (someone pressed 立即运行)
	for _, k := range order {
		if _, ok := idx[k]; !ok {
			x := byKey[k]
			r.Handled = append(r.Handled, Handled{Run: k, Events: []Event{}, Status: x.Status, Summary: x.Summary, Started: x.Started, Ended: x.Ended})
		}
	}
	for _, h := range r.Handled {
		if h.Status == routines.StatusFailed {
			r.Failed = append(r.Failed, Problem{At: h.Ended, What: "run", Detail: clip(h.Summary, 300), Session: r.Work})
		}
	}
	// what it changed: its git events, and commits on its folder since the
	// shift began that no event told of (a commit made with the shell)
	r.Changes = append(r.Changes, sh.Changes...)
	if ok {
		r.Changes = append(r.Changes, s.commits(ctx, a.Workspace(), sh, end)...)
	}
	sort.SliceStable(r.Changes, func(i, j int) bool { return r.Changes[i].At.Before(r.Changes[j].At) })
	for _, ch := range r.Changes {
		if ch.Failed {
			r.Failed = append(r.Failed, Problem{At: ch.At, What: ch.Kind, Detail: strings.TrimSpace(ch.Title + " " + ch.Ref), Session: ch.Session})
		}
	}
	for _, p := range sh.Errors {
		r.Failed = append(r.Failed, p)
	}
	// what waits for the person
	if in := s.inbox(); in != nil {
		for _, q := range in.Pending() {
			if q.Agent == sh.Agent {
				r.Waiting = append(r.Waiting, Waiting{CallID: q.CallID, Session: q.Session, Tool: q.Tool, Category: q.Category, Summary: q.Summary, Asked: q.Asked})
			}
		}
	}
	return r
}

// commits lists the commits on dir's branch made during the shift that
// were not told as git events.
func (s *Service) commits(ctx context.Context, dir string, sh *Shift, end time.Time) []Change {
	p := s.vcsp()
	if p == nil {
		return nil
	}
	info, err := p.Probe(ctx, dir)
	if err != nil {
		return nil
	}
	known := map[string]bool{}
	for _, c := range sh.Changes {
		if c.Kind == "commit" {
			known[c.Ref] = true
		}
	}
	log, err := p.Log(ctx, dir, "HEAD", 30)
	if err != nil {
		return nil
	}
	var out []Change
	for _, c := range log {
		if c.Hash == sh.Head || (sh.Head != "" && strings.HasPrefix(sh.Head, c.Hash)) {
			break // where the shift began
		}
		if c.When.Before(sh.Start) || c.When.After(end.Add(time.Minute)) || known[c.Short] {
			continue
		}
		out = append(out, Change{At: c.When.UTC(), Kind: "commit", Title: c.Subject, Ref: c.Short, Branch: info.Branch})
	}
	return out
}

// send tells the person (notify, as the morning brief).
func (s *Service) send(r Report) {
	n := s.notifier()
	if n == nil {
		return
	}
	body := fmt.Sprintf("处理 %d 件 · 改动 %d 处 · 失败 %d 项 · 待你批准 %d 项", len(r.Handled), len(r.Changes), len(r.Failed), len(r.Waiting))
	if r.Empty() {
		body = "一夜无事"
	}
	title := "值班报告"
	if r.Name != "" {
		title += " · " + r.Name
	}
	go n.Send(context.Background(), notify.Notification{Kind: notify.KindBrief, Title: title, Body: body, Agent: r.Agent, Session: r.Session})
}
