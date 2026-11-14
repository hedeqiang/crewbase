// Package taskboard is the team task board (A-7): every delegation tree —
// a conversation and the sub-agent sessions it spawned — shares one task
// DAG. Each task has exactly one owner (an agent, a person, or nobody
// yet), dependencies on other tasks of the same board, and an optional
// write scope.
//
// The board lives in the root session's log as board/task events (one
// snapshot per change), so it is replayed like everything else, survives
// restarts and forks, and is visible to the people in that conversation.
//
// The write scope is a hint for coordination, not a lock (docs/06 §9):
// actual write limits come from the sandbox.
package taskboard

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/kernel"
)

const ServiceKey = "taskboard"

// TypeTask is the session event holding one task snapshot.
const TypeTask = "board/task"

// Statuses.
const (
	StatusTodo    = "todo"
	StatusDoing   = "doing"
	StatusBlocked = "blocked"
	StatusDone    = "done"
	StatusRemoved = "removed"
)

// Limits.
const (
	MaxTasks  = 200
	maxTitle  = 120
	maxDetail = 4000
	maxScope  = 20
)

// MemberPrefix marks a person as an owner ("member:<id>").
const MemberPrefix = "member:"

// Task is one node of the DAG.
type Task struct {
	ID      string    `json:"id"` // "1", "2", … per board
	Title   string    `json:"title"`
	Detail  string    `json:"detail,omitempty"`
	Owner   string    `json:"owner,omitempty"` // agent id, member:<id>, or "" (unassigned)
	Status  string    `json:"status"`
	Deps    []string  `json:"deps,omitempty"`
	Scope   []string  `json:"scope,omitempty"` // write-scope hint: workspace-relative paths
	Note    string    `json:"note,omitempty"`  // blocker or hand-off note
	By      string    `json:"by"`              // who made this change
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

// View is a task with derived state.
type View struct {
	Task
	// Ready: todo and every dependency is done.
	Ready bool `json:"ready"`
	// Waiting lists dependencies that are not done.
	Waiting []string `json:"waiting,omitempty"`
}

// Board is a snapshot of one board.
type Board struct {
	Root  string `json:"root"` // root session id
	Tasks []View `json:"tasks"`
}

// Change is a requested edit (nil = unchanged).
type Change struct {
	Title  *string   `json:"title,omitempty"`
	Detail *string   `json:"detail,omitempty"`
	Owner  *string   `json:"owner,omitempty"`
	Status *string   `json:"status,omitempty"`
	Deps   *[]string `json:"deps,omitempty"`
	Scope  *[]string `json:"scope,omitempty"`
	Note   *string   `json:"note,omitempty"`
}

// Actor is who edits: an agent (restricted to its own or unassigned
// tasks) or a person (any task).
type Actor struct {
	Agent  string // agent id; "" for a person
	Person string // display name of the person
}

func (a Actor) by() string {
	if a.Agent != "" {
		return a.Agent
	}
	if a.Person != "" {
		return "person:" + a.Person
	}
	return "person"
}

// Errors.
var (
	ErrNotFound = errors.New("task not found")
	ErrCycle    = errors.New("dependencies would form a cycle")
)

// Changed fires after a board changes (payload: root session id).
var Changed = kernel.NewEmit[string]("taskboard/changed")

// Service is the `taskboard` service.
type Service struct {
	h        *kernel.Host
	sessions *session.Store
	// owners reports whether an owner id is valid (agent ids, members);
	// nil accepts any non-empty id.
	owners func(owner string) bool

	mu sync.Mutex // serialises edits of all boards
}

// New makes the service.
func New(h *kernel.Host, sessions *session.Store, owners func(string) bool) *Service {
	return &Service{h: h, sessions: sessions, owners: owners}
}

// Root returns the root of sessionID's delegation tree.
func (s *Service) Root(sessionID string) (*session.Session, error) {
	ss, err := s.sessions.Get(sessionID)
	if err != nil {
		return nil, err
	}
	for i := 0; i < 64; i++ {
		p := ss.Meta().Parent
		if p == "" {
			return ss, nil
		}
		ps, err := s.sessions.Get(p)
		if err != nil {
			return ss, nil // parent gone: this is the root now
		}
		ss = ps
	}
	return ss, nil
}

// load replays the board of a root session.
func load(root *session.Session) map[string]Task {
	out := map[string]Task{}
	for _, e := range root.Events() {
		if e.Type != TypeTask {
			continue
		}
		var t Task
		if e.Decode(&t) == nil && t.ID != "" {
			out[t.ID] = t
		}
	}
	return out
}

func numID(id string) int {
	n, _ := strconv.Atoi(id)
	return n
}

func view(tasks map[string]Task) []View {
	out := make([]View, 0, len(tasks))
	for _, t := range tasks {
		if t.Status == StatusRemoved {
			continue
		}
		v := View{Task: t}
		for _, d := range t.Deps {
			if dt, ok := tasks[d]; ok && dt.Status != StatusDone && dt.Status != StatusRemoved {
				v.Waiting = append(v.Waiting, d)
			}
		}
		v.Ready = t.Status == StatusTodo && len(v.Waiting) == 0
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return numID(out[i].ID) < numID(out[j].ID) })
	return out
}

// Get returns the board of sessionID's tree.
func (s *Service) Get(sessionID string) (Board, error) {
	root, err := s.Root(sessionID)
	if err != nil {
		return Board{}, err
	}
	return Board{Root: root.ID(), Tasks: view(load(root))}, nil
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > n {
		return string([]rune(s)[:n])
	}
	return s
}

func cleanScope(xs []string) ([]string, error) {
	if len(xs) > maxScope {
		return nil, fmt.Errorf("at most %d scope paths", maxScope)
	}
	var out []string
	for _, x := range xs {
		x = strings.TrimSpace(strings.ReplaceAll(x, "\\", "/"))
		if x == "" {
			continue
		}
		c := path.Clean(x)
		if path.IsAbs(c) || c == ".." || strings.HasPrefix(c, "../") {
			return nil, fmt.Errorf("scope %q must be a path inside the workspace", x)
		}
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out, nil
}

// cyclic reports whether task id reaches itself through deps.
func cyclic(tasks map[string]Task, id string) bool {
	seen := map[string]bool{}
	var walk func(string) bool
	walk = func(cur string) bool {
		for _, d := range tasks[cur].Deps {
			if d == id {
				return true
			}
			if !seen[d] {
				seen[d] = true
				if walk(d) {
					return true
				}
			}
		}
		return false
	}
	return walk(id)
}

func (s *Service) checkOwner(o string) error {
	if o == "" {
		return nil
	}
	if s.owners != nil && !s.owners(o) {
		return fmt.Errorf("unknown owner %q", o)
	}
	return nil
}

func (s *Service) checkDeps(tasks map[string]Task, deps []string) ([]string, error) {
	var out []string
	for _, d := range deps {
		d = strings.TrimPrefix(strings.TrimSpace(d), "#")
		if d == "" || slices.Contains(out, d) {
			continue
		}
		t, ok := tasks[d]
		if !ok || t.Status == StatusRemoved {
			return nil, fmt.Errorf("dependency #%s does not exist", d)
		}
		out = append(out, d)
	}
	return out, nil
}

func (s *Service) save(root *session.Session, a Actor, t Task) error {
	if _, err := root.Append(TypeTask, a.Agent, t); err != nil {
		return err
	}
	Changed.Emit(s.h, root.ID())
	return nil
}

// Add creates a task on sessionID's board.
func (s *Service) Add(sessionID string, a Actor, c Change) (View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.Root(sessionID)
	if err != nil {
		return View{}, err
	}
	tasks := load(root)
	live := 0
	maxID := 0
	for _, t := range tasks {
		if t.Status != StatusRemoved {
			live++
		}
		maxID = max(maxID, numID(t.ID))
	}
	if live >= MaxTasks {
		return View{}, fmt.Errorf("the board is full (%d tasks)", MaxTasks)
	}
	now := time.Now().UTC()
	t := Task{ID: strconv.Itoa(maxID + 1), Status: StatusTodo, By: a.by(), Created: now, Updated: now}
	if c.Status != nil && *c.Status != StatusTodo {
		return View{}, errors.New("new tasks start as todo")
	}
	if err := s.apply(tasks, &t, a, c, true); err != nil {
		return View{}, err
	}
	tasks[t.ID] = t
	if err := s.save(root, a, t); err != nil {
		return View{}, err
	}
	return find(view(tasks), t.ID), nil
}

func find(vs []View, id string) View {
	for _, v := range vs {
		if v.ID == id {
			return v
		}
	}
	return View{}
}

// Update edits a task.
func (s *Service) Update(sessionID, id string, a Actor, c Change) (View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.Root(sessionID)
	if err != nil {
		return View{}, err
	}
	tasks := load(root)
	t, ok := tasks[strings.TrimPrefix(id, "#")]
	if !ok || t.Status == StatusRemoved {
		return View{}, ErrNotFound
	}
	// agents edit their own tasks; an unassigned task may be claimed
	if a.Agent != "" && t.Owner != "" && t.Owner != a.Agent {
		return View{}, fmt.Errorf("task #%s belongs to %s; ask them (or a person) to change it", t.ID, t.Owner)
	}
	if err := s.apply(tasks, &t, a, c, false); err != nil {
		return View{}, err
	}
	t.By, t.Updated = a.by(), time.Now().UTC()
	tasks[t.ID] = t
	if err := s.save(root, a, t); err != nil {
		return View{}, err
	}
	return find(view(tasks), t.ID), nil
}

// Remove deletes a task (people only) and drops it from others' deps.
func (s *Service) Remove(sessionID, id string, a Actor) error {
	if a.Agent != "" {
		return errors.New("only people remove tasks; mark it done or blocked instead")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.Root(sessionID)
	if err != nil {
		return err
	}
	tasks := load(root)
	t, ok := tasks[id]
	if !ok || t.Status == StatusRemoved {
		return ErrNotFound
	}
	now := time.Now().UTC()
	t.Status, t.By, t.Updated = StatusRemoved, a.by(), now
	if err := s.save(root, a, t); err != nil {
		return err
	}
	tasks[id] = t
	for _, o := range tasks {
		if o.Status == StatusRemoved || !slices.Contains(o.Deps, id) {
			continue
		}
		o.Deps = slices.DeleteFunc(o.Deps, func(d string) bool { return d == id })
		o.By, o.Updated = a.by(), now
		if err := s.save(root, a, o); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) apply(tasks map[string]Task, t *Task, a Actor, c Change, isNew bool) error {
	if c.Title != nil {
		t.Title = clip(*c.Title, maxTitle)
	}
	if strings.TrimSpace(t.Title) == "" {
		return errors.New("a title is required")
	}
	if c.Detail != nil {
		t.Detail = clip(*c.Detail, maxDetail)
	}
	if c.Note != nil {
		t.Note = clip(*c.Note, 1000)
	}
	if c.Owner != nil {
		o := strings.TrimSpace(*c.Owner)
		if a.Agent != "" && o != "" && o != a.Agent && !isNew {
			return errors.New("agents can take a task or hand it back (owner \"\"), not assign it to someone else")
		}
		if err := s.checkOwner(o); err != nil {
			return err
		}
		t.Owner = o
	}
	if c.Scope != nil {
		sc, err := cleanScope(*c.Scope)
		if err != nil {
			return err
		}
		t.Scope = sc
	}
	if c.Deps != nil {
		deps, err := s.checkDeps(tasks, *c.Deps)
		if err != nil {
			return err
		}
		if slices.Contains(deps, t.ID) {
			return ErrCycle
		}
		prev := t.Deps
		t.Deps = deps
		tasks[t.ID] = *t
		if cyclic(tasks, t.ID) {
			t.Deps = prev
			tasks[t.ID] = *t
			return ErrCycle
		}
	}
	if c.Status != nil {
		st := *c.Status
		switch st {
		case StatusTodo, StatusDone:
		case StatusDoing:
			var waiting []string
			for _, d := range t.Deps {
				if dt := tasks[d]; dt.Status != StatusDone && dt.Status != StatusRemoved {
					waiting = append(waiting, "#"+d)
				}
			}
			if len(waiting) > 0 {
				return fmt.Errorf("task #%s waits for %s", t.ID, strings.Join(waiting, ", "))
			}
			if t.Owner == "" && a.Agent != "" {
				t.Owner = a.Agent // starting an unassigned task claims it
			}
		case StatusBlocked:
			if strings.TrimSpace(t.Note) == "" {
				return errors.New("say why it is blocked (note)")
			}
		default:
			return fmt.Errorf("unknown status %q (todo, doing, blocked, done)", st)
		}
		t.Status = st
	}
	return nil
}
