package usage

// Agent profiles (plugin @crewbase/agent-profile): each agent's track
// record — turns done, tools used, approvals asked / approved / rejected,
// models and what they cost, active days, the kinds of work it does most,
// and a few badges earned from that record.
//
// Counters are kept incrementally: one pass over the session logs at start
// (in the background), then every appended event updates them through a
// bounded queue (session appends never wait). A view reads the counters;
// nothing is re-scanned. Spend is valued exactly as /api/usage values it.
//
//	GET /api/agents/{id}/profile → Profile

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// Profile is an agent's track record.
type Profile struct {
	Agent      string       `json:"agent"`
	Ready      bool         `json:"ready"` // the first pass over the logs is done
	Turns      int          `json:"turns"` // completed turns
	Failed     int          `json:"failed"`
	ToolCalls  int          `json:"toolCalls"`
	ToolErrors int          `json:"toolErrors"`
	Tools      []Count      `json:"tools"` // most used first (top 8)
	Asked      int          `json:"asked"` // approvals asked
	Approved   int          `json:"approved"`
	Rejected   int          `json:"rejected"`
	Tokens     int          `json:"tokens"`
	SpentUSD   float64      `json:"spentUsd"`
	SavedUSD   float64      `json:"savedUsd"`
	Models     []ModelShare `json:"models"`
	ActiveDays int          `json:"activeDays"`
	Streak     int          `json:"streak"`     // consecutive active days up to today (or yesterday)
	BestStreak int          `json:"bestStreak"` // longest run of active days
	First      string       `json:"first,omitempty"`
	Last       string       `json:"last,omitempty"`
	// Recent is turns per day over the last 28 days, oldest first.
	Recent    []int      `json:"recent"`
	Strengths []Strength `json:"strengths"`
	Badges    []Badge    `json:"badges"`
}

// Count is a named count.
type Count struct {
	Name string `json:"name"`
	N    int    `json:"n"`
}

// Strength is a kind of work and how many tool calls it took.
type Strength struct {
	Kind  string  `json:"kind"` // code | research | writing | planning | memory | team | media | computer | files | connectors
	N     int     `json:"n"`
	Share float64 `json:"share"`
}

// Badge is an achievement, earned or with progress towards it.
type Badge struct {
	ID       string `json:"id"`
	Earned   bool   `json:"earned"`
	Progress int    `json:"progress"`
	Goal     int    `json:"goal"`
	At       string `json:"at,omitempty"` // the day it was earned, when known
}

// workKind sorts a tool into a kind of work ("" = not telling).
func workKind(tool string) string {
	has := func(ps ...string) bool {
		for _, p := range ps {
			if strings.HasPrefix(tool, p) {
				return true
			}
		}
		return false
	}
	switch {
	case has("git_", "edit_file", "write_file", "apply_patch", "bash", "shell", "run_", "job_", "delete_file", "preview_"):
		return "code"
	case has("web_", "read_url", "browser_", "library_search", "library_read", "history_", "deepwiki"):
		return "research"
	case has("library_save", "show_page", "note_"):
		return "writing"
	case has("task_", "goal_", "followup_"):
		return "planning"
	case has("memory_", "team_memory_"):
		return "memory"
	case has("delegate", "handover", "subagent", "spawn_"):
		return "team"
	case has("image_", "video_"):
		return "media"
	case has("computer_"):
		return "computer"
	case has("read_file", "list_dir", "ocr_", "glob", "grep"):
		return "files"
	case strings.Contains(tool, "__"):
		return "connectors"
	}
	return ""
}

type weekStat struct{ decided, rejected int }

type agentStats struct {
	turns, failed, toolCalls, toolErrors int
	tools                                map[string]int
	asked, approved, rejected            int
	tokens                               int
	spent, saved                         float64
	models                               map[string]*ModelShare
	days                                 map[string]int // local day → turns
	active                               map[string]bool
	weeks                                map[string]*weekStat
	firstPR, firstMemory                 string
}

func newAgentStats() *agentStats {
	return &agentStats{tools: map[string]int{}, models: map[string]*ModelShare{}, days: map[string]int{}, active: map[string]bool{}, weeks: map[string]*weekStat{}}
}

func earlier(cur, d string) string {
	if cur == "" || d < cur {
		return d
	}
	return cur
}

// tracker keeps the counters.
type tracker struct {
	u        *Service
	sessions *session.Store
	agents   *agent.Registry
	now      func() time.Time

	mu     sync.Mutex
	cursor map[string]int64 // session → last seq counted
	stats  map[string]*agentStats
	prices map[string]*llm.Pricing
	ready  atomic.Bool

	queue chan session.Appended
	dirty atomic.Bool
}

func (t *tracker) of(id string) *agentStats {
	s := t.stats[id]
	if s == nil {
		s = newAgentStats()
		t.stats[id] = s
	}
	return s
}

// count adds one event (t.mu held).
func (t *tracker) count(e session.Event) {
	if e.Agent == "" {
		return
	}
	day := e.Time.Local().Format("2006-01-02")
	y, w := e.Time.Local().ISOWeek()
	week := fmt.Sprintf("%d-W%02d", y, w)
	switch e.Type {
	case session.TypeAssistantMessage:
		var am session.AssistantMessage
		if e.Decode(&am) != nil {
			return
		}
		s := t.of(e.Agent)
		s.active[day] = true
		v := t.u.value(am, t.prices)
		s.tokens += am.Usage.InputTokens + am.Usage.OutputTokens
		s.spent += v.spent
		s.saved += v.saved
		ref := am.Model.String()
		ms := s.models[ref]
		if ms == nil {
			ms = &ModelShare{Model: ref, Provider: am.Model.Provider, Kind: v.kind}
			s.models[ref] = ms
		}
		ms.add(am, v.spent, v.saved, v.priced)
	case session.TypeTurnEnd:
		var te agentloop.TurnEnd
		if e.Decode(&te) != nil {
			return
		}
		s := t.of(e.Agent)
		switch te.Status {
		case "completed":
			s.turns++
			s.days[day]++
			s.active[day] = true
		case "failed":
			s.failed++
		}
	case session.TypeToolResult:
		var r llm.ToolResult
		if e.Decode(&r) != nil || r.Name == "" {
			return
		}
		s := t.of(e.Agent)
		s.toolCalls++
		s.tools[r.Name]++
		if r.IsError {
			s.toolErrors++
			return
		}
		switch r.Name {
		case "git_pr_merge":
			s.firstPR = earlier(s.firstPR, day)
		case "memory_add":
			s.firstMemory = earlier(s.firstMemory, day)
		}
	case session.TypeApprovalAsked:
		s := t.of(e.Agent)
		s.asked++
	case session.TypeApprovalDecided:
		var d struct {
			Outcome string `json:"outcome"`
		}
		if e.Decode(&d) != nil {
			return
		}
		s := t.of(e.Agent)
		ws := s.weeks[week]
		if ws == nil {
			ws = &weekStat{}
			s.weeks[week] = ws
		}
		switch d.Outcome {
		case "allowed-once":
			s.approved++
			ws.decided++
		case "rejected":
			s.rejected++
			ws.decided++
			ws.rejected++
		}
	}
}

// catchUp counts what the session logged after the cursor (t.mu held).
func (t *tracker) catchUp(id string) {
	ss, err := t.sessions.Get(id)
	if err != nil {
		return
	}
	cur := t.cursor[id]
	for _, e := range ss.Since(cur) {
		if e.Seq <= cur {
			continue
		}
		t.count(e)
		cur = e.Seq
	}
	t.cursor[id] = cur
}

func (t *tracker) catchUpAll(ctx context.Context) {
	for _, m := range t.sessions.List() {
		if ctx.Err() != nil {
			return
		}
		t.mu.Lock()
		t.catchUp(m.ID)
		t.mu.Unlock()
	}
}

func (t *tracker) enqueue(a session.Appended) {
	select {
	case t.queue <- a:
	default:
		t.dirty.Store(true)
	}
}

func (t *tracker) worker(ctx context.Context) {
	t.catchUpAll(ctx)
	t.ready.Store(true)
	for {
		select {
		case <-ctx.Done():
			return
		case a := <-t.queue:
			t.mu.Lock()
			cur, known := t.cursor[a.Session]
			switch {
			case a.Event.Seq <= cur:
			case a.Event.Seq == cur+1 && (known || a.Event.Seq == 1):
				t.count(a.Event)
				t.cursor[a.Session] = a.Event.Seq
			default:
				t.catchUp(a.Session)
			}
			t.mu.Unlock()
			if t.dirty.Load() && len(t.queue) == 0 {
				t.dirty.Store(false)
				t.catchUpAll(ctx)
			}
		}
	}
}

// streaks returns the current and the best run of consecutive active days.
func streaks(active map[string]bool, today time.Time) (cur, best int) {
	days := make([]string, 0, len(active))
	for d := range active {
		days = append(days, d)
	}
	sort.Strings(days)
	run := 0
	var prev time.Time
	for _, d := range days {
		t, err := time.ParseInLocation("2006-01-02", d, time.Local)
		if err != nil {
			continue
		}
		if !prev.IsZero() && prev.AddDate(0, 0, 1).Format("2006-01-02") == d {
			run++
		} else {
			run = 1
		}
		best = max(best, run)
		prev = t
	}
	// the current run ends today, or yesterday (today is not over yet)
	d := today
	if !active[d.Format("2006-01-02")] {
		d = d.AddDate(0, 0, -1)
	}
	for active[d.Format("2006-01-02")] {
		cur++
		d = d.AddDate(0, 0, -1)
	}
	return cur, best
}

// Badge goals.
const (
	goalStreak    = 7
	goalTools     = 100
	goalCleanWeek = 3
	goalTurns     = 50
	goalKinds     = 4
)

// Profile builds the profile of agent id from the counters.
func (t *tracker) Profile(id string) Profile {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.stats[id]
	if s == nil {
		s = newAgentStats()
	}
	p := Profile{Agent: id, Ready: t.ready.Load(), Turns: s.turns, Failed: s.failed, ToolCalls: s.toolCalls, ToolErrors: s.toolErrors,
		Asked: s.asked, Approved: s.approved, Rejected: s.rejected, Tokens: s.tokens, SpentUSD: s.spent, SavedUSD: s.saved,
		ActiveDays: len(s.active), Models: sortedModels(s.models)}
	for n, c := range s.tools {
		p.Tools = append(p.Tools, Count{Name: n, N: c})
	}
	sort.Slice(p.Tools, func(i, j int) bool {
		if p.Tools[i].N != p.Tools[j].N {
			return p.Tools[i].N > p.Tools[j].N
		}
		return p.Tools[i].Name < p.Tools[j].Name
	})
	kinds := map[string]int{}
	total := 0
	for n, c := range s.tools {
		if k := workKind(n); k != "" {
			kinds[k] += c
			total += c
		}
	}
	for k, c := range kinds {
		p.Strengths = append(p.Strengths, Strength{Kind: k, N: c, Share: float64(c) / float64(total)})
	}
	sort.Slice(p.Strengths, func(i, j int) bool {
		if p.Strengths[i].N != p.Strengths[j].N {
			return p.Strengths[i].N > p.Strengths[j].N
		}
		return p.Strengths[i].Kind < p.Strengths[j].Kind
	})
	usedKinds := len(p.Strengths)
	if len(p.Tools) > 8 {
		p.Tools = p.Tools[:8]
	}
	if len(p.Strengths) > 3 {
		p.Strengths = p.Strengths[:3]
	}
	for d := range s.active {
		p.First = earlier(p.First, d)
		if d > p.Last {
			p.Last = d
		}
	}
	now := t.now()
	p.Streak, p.BestStreak = streaks(s.active, now)
	p.Recent = make([]int, 28)
	for i := range p.Recent {
		p.Recent[i] = s.days[now.AddDate(0, 0, i-27).Format("2006-01-02")]
	}
	clean := 0
	for _, w := range s.weeks {
		if w.rejected == 0 {
			clean = max(clean, w.decided)
		}
	}
	one := func(d string) int {
		if d != "" {
			return 1
		}
		return 0
	}
	add := func(id string, prog, goal int, at string) {
		p.Badges = append(p.Badges, Badge{ID: id, Earned: prog >= goal, Progress: min(prog, goal), Goal: goal, At: at})
	}
	add("first-pr", one(s.firstPR), 1, s.firstPR)
	add("streak-7", p.BestStreak, goalStreak, "")
	add("tools-100", s.toolCalls, goalTools, "")
	add("clean-week", clean, goalCleanWeek, "")
	add("first-memory", one(s.firstMemory), 1, s.firstMemory)
	add("turns-50", s.turns, goalTurns, "")
	add("versatile", usedKinds, goalKinds, "")
	if p.Tools == nil {
		p.Tools = []Count{}
	}
	if p.Strengths == nil {
		p.Strengths = []Strength{}
	}
	return p
}

// ProfileQueue bounds the profile's event queue.
const ProfileQueue = 2048

// ProfilePlugin keeps agent profiles and serves GET /api/agents/{id}/profile.
var ProfilePlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/agent-profile",
	Inject:      []string{gateway.ServiceKey, ServiceKey, session.ServiceKey, agent.ServiceKey},
	Description: "Agent track records (turns, tools, approvals, models and cost, active days, badges), counted incrementally from the session logs.",
	Apply: func(c *kernel.Context) error {
		t := &tracker{
			u:        kernel.Use[*Service](c, ServiceKey),
			sessions: kernel.Use[*session.Store](c, session.ServiceKey),
			agents:   kernel.Use[*agent.Registry](c, agent.ServiceKey),
			now:      time.Now,
			cursor:   map[string]int64{}, stats: map[string]*agentStats{}, prices: map[string]*llm.Pricing{},
			queue: make(chan session.Appended, ProfileQueue),
		}
		session.OnEvent.Listen(c, t.enqueue)
		c.Go(t.worker)
		kernel.Use[*gateway.Gateway](c, gateway.ServiceKey).Handle(c, gateway.Route{Method: "GET", Path: "/api/agents/{id}/profile", Devices: []string{"mobile"},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				id := r.Params["id"]
				if _, ok := t.agents.Get(id); !ok {
					return nil, gateway.NotFound()
				}
				return t.Profile(id), nil
			}})
		return nil
	},
})
