// Package teams groups people and agents (docs/decisions/0003-teams.md):
// a team holds agents and the channels between them, and is the boundary
// of the teammate roster, delegation and channel membership. Everything
// created before teams existed is in the default team.
//
// Phase 1: teams, membership of agents and channels, visibility (public /
// private) and people membership are stored; per-person visibility and
// team-level shared resources come next.
//
//	GET    /api/teams                → {teams: [View], defaultTeam}
//	POST   /api/teams        Input   → View
//	PATCH  /api/teams/{id}   Input   → View
//	DELETE /api/teams/{id}           (empty teams only; never the default team)
//	POST   /api/teams/{id}/agents {agent} moves an agent into the team
package teams

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/features/members"
	"github.com/hedeqiang/crewbase/kernel"
)

// ServiceKey of the *Service.
const ServiceKey = "teams"

// Visibility.
const (
	Public  = "public"  // everyone on the host sees it and can ask to join
	Private = "private" // only its members see it
)

// Team roles of people.
const (
	RoleLead   = "lead"
	RoleMember = "member"
	RoleViewer = "viewer"
)

// Person is a person in a team ("owner" = the host's owner, else a
// member id from @crewbase/members).
type Person struct {
	ID   string `json:"id"`
	Role string `json:"role"`
}

// Team is a stored team.
type Team struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Color      string    `json:"color,omitempty"`
	Visibility string    `json:"visibility"`
	People     []Person  `json:"people"`
	Created    time.Time `json:"created"`
	// Seeded: the host's existing members were added (default team, once).
	Seeded bool `json:"seeded,omitempty"`
	// DefaultMode: the run mode new agents of the team start in
	// (read-only | sandbox; "" = sandbox).
	DefaultMode string `json:"defaultMode,omitempty"`
}

// View is a team with what it holds.
type View struct {
	Team
	Default  bool `json:"default"`
	Agents   int  `json:"agents"`
	Channels int  `json:"channels"`
	// For the person asking: their role ("" = not in it), whether they may
	// act in it and manage it.
	MyRole    string `json:"myRole"`
	CanAct    bool   `json:"canAct"`
	CanManage bool   `json:"canManage"`
	// Tokens used by the team's agents in the usage period.
	Tokens int `json:"tokens"`
}

// Input creates or edits a team (nil / "" = unchanged).
type Input struct {
	Name        string    `json:"name"`
	Color       string    `json:"color"`
	Visibility  string    `json:"visibility"`
	People      *[]Person `json:"people"`
	DefaultMode *string   `json:"defaultMode"`
}

// DefaultTeamName is the default team's name until someone renames it (the
// UI shows it in the viewer's language).
const DefaultTeamName = "我的团队"

// Changed fires after every change.
var Changed = kernel.NewEmit[string]("teams/changed")

// Service manages teams.
type Service struct {
	c      *kernel.Context
	path   string
	agents *agent.Registry

	mu    sync.Mutex
	items map[string]*Team
	memMu sync.Mutex // TEAM.md writes
}

func newID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "team-" + hex.EncodeToString(b)
}

func (s *Service) load() error {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var list []*Team
	if err := json.Unmarshal(b, &list); err != nil {
		return fmt.Errorf("teams.json: %w", err)
	}
	for _, t := range list {
		s.items[t.ID] = t
	}
	return nil
}

func (s *Service) saveLocked() error {
	if s.path == "" {
		return nil
	}
	list := make([]*Team, 0, len(s.items))
	for _, t := range s.items {
		list = append(list, t)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Created.Before(list[j].Created) })
	b, _ := json.MarshalIndent(list, "", "  ")
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// ensureDefault creates the default team on first start.
func (s *Service) ensureDefault() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[agent.DefaultTeam]; ok {
		return nil
	}
	s.items[agent.DefaultTeam] = &Team{ID: agent.DefaultTeam, Name: DefaultTeamName, Visibility: Public, Color: "#3b32c4",
		People: []Person{{ID: "owner", Role: RoleLead}}, Created: time.Unix(0, 0).UTC()}
	return s.saveLocked()
}

func cleanName(n string) (string, error) {
	n = strings.Join(strings.Fields(n), " ")
	if n == "" {
		return "", errors.New("团队需要一个名字")
	}
	if utf8.RuneCountInString(n) > 40 {
		return "", errors.New("团队名字不能超过 40 个字")
	}
	return n, nil
}

func cleanPeople(ps []Person) ([]Person, error) {
	seen := map[string]bool{}
	var out []Person
	for _, p := range ps {
		p.ID = strings.TrimSpace(p.ID)
		if p.ID == "" || seen[p.ID] {
			continue
		}
		switch p.Role {
		case RoleLead, RoleMember, RoleViewer:
		case "":
			p.Role = RoleMember
		default:
			return nil, fmt.Errorf("未知的团队角色 %q", p.Role)
		}
		seen[p.ID] = true
		out = append(out, p)
	}
	if out == nil {
		out = []Person{}
	}
	return out, nil
}

// Exists reports whether id names a team ("" = the default team).
func (s *Service) Exists(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.items[agent.TeamOf(id)]
	return ok
}

// Create adds a team; the creator ("owner" or a member id) leads it.
func (s *Service) Create(in Input, by string) (View, error) {
	name, err := cleanName(in.Name)
	if err != nil {
		return View{}, err
	}
	vis := in.Visibility
	if vis == "" {
		vis = Public
	}
	if vis != Public && vis != Private {
		return View{}, errors.New("可见范围只能是公开或私密")
	}
	if by == "" {
		by = "owner"
	}
	people := []Person{{ID: by, Role: RoleLead}}
	if in.People != nil {
		ps, err := cleanPeople(append(people, *in.People...))
		if err != nil {
			return View{}, err
		}
		people = ps
	}
	t := &Team{ID: newID(), Name: name, Color: in.Color, Visibility: vis, People: people, Created: time.Now().UTC()}
	s.mu.Lock()
	for _, x := range s.items {
		if strings.EqualFold(x.Name, name) {
			s.mu.Unlock()
			return View{}, fmt.Errorf("已经有叫「%s」的团队了", name)
		}
	}
	s.items[t.ID] = t
	err = s.saveLocked()
	s.mu.Unlock()
	if err != nil {
		return View{}, err
	}
	Changed.Emit(s.c, t.ID)
	return s.view(*t), nil
}

// Update edits a team.
func (s *Service) Update(id string, in Input) (View, error) {
	s.mu.Lock()
	t, ok := s.items[id]
	if !ok {
		s.mu.Unlock()
		return View{}, os.ErrNotExist
	}
	nt := *t
	if strings.TrimSpace(in.Name) != "" {
		name, err := cleanName(in.Name)
		if err != nil {
			s.mu.Unlock()
			return View{}, err
		}
		for _, x := range s.items {
			if x.ID != id && strings.EqualFold(x.Name, name) {
				s.mu.Unlock()
				return View{}, fmt.Errorf("已经有叫「%s」的团队了", name)
			}
		}
		nt.Name = name
	}
	if in.Color != "" {
		nt.Color = in.Color
	}
	if in.Visibility != "" {
		if in.Visibility != Public && in.Visibility != Private {
			s.mu.Unlock()
			return View{}, errors.New("可见范围只能是公开或私密")
		}
		nt.Visibility = in.Visibility
	}
	if in.DefaultMode != nil {
		switch *in.DefaultMode {
		case "", agent.ModeReadOnly, agent.ModeSandbox:
			nt.DefaultMode = *in.DefaultMode
		default:
			s.mu.Unlock()
			return View{}, errors.New("团队默认权限只能是只读或容器沙箱")
		}
	}
	if in.People != nil {
		ps, err := cleanPeople(*in.People)
		if err != nil {
			s.mu.Unlock()
			return View{}, err
		}
		lead := false
		for _, p := range ps {
			lead = lead || p.Role == RoleLead
		}
		if !lead {
			s.mu.Unlock()
			return View{}, errors.New("团队至少要有一位负责人")
		}
		nt.People = ps
	}
	*t = nt
	err := s.saveLocked()
	s.mu.Unlock()
	if err != nil {
		return View{}, err
	}
	Changed.Emit(s.c, id)
	return s.view(nt), nil
}

// count returns the agents and channels of a team.
func (s *Service) count(id string) (agents, channels int) {
	for _, a := range s.agents.List() {
		sp := a.Spec()
		if !sp.Ephemeral && sp.TeamID() == id {
			agents++
		}
	}
	for _, m := range s.agents.Sessions().List() {
		if m.Kind == "channel" && !m.Deleted && agent.TeamOf(m.Team) == id {
			channels++
		}
	}
	return
}

func (s *Service) view(t Team) View {
	a, c := s.count(t.ID)
	return View{Team: t, Default: t.ID == agent.DefaultTeam, Agents: a, Channels: c}
}

// List returns the teams, the default first.
func (s *Service) List() []View {
	s.mu.Lock()
	list := make([]Team, 0, len(s.items))
	for _, t := range s.items {
		list = append(list, *t)
	}
	s.mu.Unlock()
	sort.Slice(list, func(i, j int) bool {
		if (list[i].ID == agent.DefaultTeam) != (list[j].ID == agent.DefaultTeam) {
			return list[i].ID == agent.DefaultTeam
		}
		return list[i].Created.Before(list[j].Created)
	})
	out := make([]View, 0, len(list))
	for _, t := range list {
		out = append(out, s.view(t))
	}
	return out
}

// Delete removes an empty team (never the default one).
func (s *Service) Delete(id string) error {
	if id == agent.DefaultTeam {
		return errors.New("默认团队不能删除")
	}
	if !s.Exists(id) {
		return os.ErrNotExist
	}
	if a, c := s.count(id); a > 0 || c > 0 {
		return fmt.Errorf("这个团队里还有 %d 个 Agent、%d 个频道，先把它们移走或删除", a, c)
	}
	s.mu.Lock()
	delete(s.items, id)
	err := s.saveLocked()
	s.mu.Unlock()
	Changed.Emit(s.c, id)
	return err
}

// Move puts an agent in a team. An agent still in a channel of its current
// team must leave it first (a channel lives in one team).
func (s *Service) Move(agentID, team string) (*agent.Agent, error) {
	team = agent.TeamOf(team)
	if !s.Exists(team) {
		return nil, fmt.Errorf("没有这个团队：%s", team)
	}
	a, ok := s.agents.Get(agentID)
	if !ok {
		return nil, os.ErrNotExist
	}
	if a.Spec().Ephemeral {
		return nil, errors.New("子 Agent 跟随它的上级所在的团队")
	}
	if a.Spec().TeamID() == team {
		return a, nil
	}
	for _, m := range s.agents.Sessions().List() {
		if m.Kind != "channel" || m.Deleted {
			continue
		}
		for _, x := range m.Agents {
			if x == agentID {
				return nil, fmt.Errorf("%s 还在当前团队的频道「%s」里，先把它移出这个频道", a.Spec().Name, m.Title)
			}
		}
	}
	stored := team
	if stored == agent.DefaultTeam {
		stored = ""
	}
	if err := a.Update(func(sp *agent.Spec) { sp.Team = stored }); err != nil {
		return nil, err
	}
	Changed.Emit(s.c, team)
	return a, nil
}

// hostRoleToTeam maps a host role to the role in the default team.
func hostRoleToTeam(r string) string {
	switch r {
	case "admin":
		return RoleLead
	case "viewer":
		return RoleViewer
	}
	return RoleMember
}

type memberList interface {
	List() []members.Member
}

// seed puts the host's existing members in the default team, once, so
// nobody loses what they saw before teams.
func (s *Service) seed() {
	ml, ok := kernel.Lookup[memberList](s.c, "members")
	if !ok {
		return
	}
	s.mu.Lock()
	t := s.items[agent.DefaultTeam]
	if t == nil || t.Seeded {
		s.mu.Unlock()
		return
	}
	for _, m := range ml.List() {
		t.People = addPerson(t.People, Person{ID: m.ID, Role: hostRoleToTeam(m.Role)})
	}
	t.Seeded = true
	_ = s.saveLocked()
	s.mu.Unlock()
}

func addPerson(ps []Person, p Person) []Person {
	for _, x := range ps {
		if x.ID == p.ID {
			return ps
		}
	}
	return append(ps, p)
}

// onMember: new host members join the default team; removed ones leave
// every team.
func (s *Service) onMember(e members.Event) {
	s.mu.Lock()
	changed := false
	switch e.Kind {
	case "added":
		if t := s.items[agent.DefaultTeam]; t != nil {
			t.People = addPerson(t.People, Person{ID: e.Member.ID, Role: hostRoleToTeam(e.Member.Role)})
			changed = true
		}
	case "removed":
		for _, t := range s.items {
			out := t.People[:0]
			for _, p := range t.People {
				if p.ID != e.Member.ID {
					out = append(out, p)
				}
			}
			if len(out) != len(t.People) {
				t.People, changed = out, true
			}
		}
	}
	if changed {
		_ = s.saveLocked()
	}
	s.mu.Unlock()
	if changed {
		Changed.Emit(s.c, agent.DefaultTeam)
	}
}

// Join adds w to a public team as a member.
func (s *Service) Join(w Who, id string) (View, error) {
	s.mu.Lock()
	t, ok := s.items[id]
	if !ok {
		s.mu.Unlock()
		return View{}, os.ErrNotExist
	}
	if t.Visibility != Public && !w.admin() {
		s.mu.Unlock()
		return View{}, errors.New("这是私密团队，需要团队负责人邀请")
	}
	t.People = addPerson(t.People, Person{ID: w.ID, Role: RoleMember})
	err := s.saveLocked()
	v := *t
	s.mu.Unlock()
	if err != nil {
		return View{}, err
	}
	Changed.Emit(s.c, id)
	return s.viewFor(v, w), nil
}

func (s *Service) viewFor(t Team, w Who) View {
	v := s.view(t)
	v.MyRole = s.RoleIn(w, t.ID)
	v.CanAct = s.CanAct(w, t.ID)
	v.CanManage = s.CanManage(w, t.ID)
	return v
}

// ---------------------------------------------------------------- plugin

// Config of the plugin.
type Config struct {
	Dir string `json:"dir"`
}

// Plugin is @crewbase/teams.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/teams",
	Inject:      []string{agent.ServiceKey, session.ServiceKey, gateway.ServiceKey},
	Description: "Teams: groups of people and agents with their channels (roster, delegation and channels stay inside a team).",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		s := &Service{c: c, agents: kernel.Use[*agent.Registry](c, agent.ServiceKey), items: map[string]*Team{}}
		if cfg.Dir != "" {
			s.path = filepath.Join(cfg.Dir, "teams.json")
		}
		if err := s.load(); err != nil {
			return err
		}
		if err := s.ensureDefault(); err != nil {
			return err
		}
		s.seed()
		members.Changed.Listen(c, s.onMember)
		if tr, ok := kernel.Lookup[*tools.Registry](c, tools.ServiceKey); ok {
			tr.SetTeamOf(func(id string) string {
				if a, ok := s.agents.Get(id); ok {
					return a.Spec().TeamID()
				}
				return ""
			})
		}
		if err := c.Provide(ServiceKey, s); err != nil {
			return err
		}
		if err := c.Provide(AccessKey, Visible{s}); err != nil {
			return err
		}
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		g.SetGuard(s.guard)
		g.Hub().SetFilter(s.eventVisible)
		c.Defer(func() {
			g.SetGuard(nil)
			g.Hub().SetFilter(nil)
		})
		Changed.Listen(c, func(string) { g.Hub().Publish("teams.changed", struct{}{}) })
		mobile := []string{"mobile"}
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/teams", Devices: mobile,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				w := s.WhoIs(r.DeviceID)
				out := []View{}
				tok := s.tokens()
				for _, v := range s.List() {
					if s.TeamVisible(w, v.ID) {
						x := s.viewFor(v.Team, w)
						x.Tokens = tok[v.ID]
						out = append(out, x)
					}
				}
				return map[string]any{"teams": out, "defaultTeam": agent.DefaultTeam, "me": w.ID}, nil
			}})
		s.installShared(c)
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/teams/{id}/memory", Devices: mobile,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if !s.Exists(r.Params["id"]) {
					return nil, gateway.NotFound()
				}
				return map[string]any{"text": s.Memory(r.Params["id"])}, nil
			}})
		g.Handle(c, gateway.Route{Method: "PUT", Path: "/api/teams/{id}/memory", Devices: mobile, MaxBody: 1 << 20,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b struct {
					Text string `json:"text"`
				}
				if err := r.Decode(&b); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				err := s.SetMemory(r.Params["id"], b.Text)
				if errors.Is(err, os.ErrNotExist) {
					return nil, gateway.NotFound()
				}
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return map[string]any{"text": b.Text}, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/teams/{id}/join", Devices: mobile,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				v, err := s.Join(s.WhoIs(r.DeviceID), r.Params["id"])
				if errors.Is(err, os.ErrNotExist) {
					return nil, gateway.NotFound()
				}
				if err != nil {
					return nil, gateway.Conflict("%v", err)
				}
				return v, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/teams", Devices: mobile,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var in Input
				if err := r.Decode(&in); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				v, err := s.Create(in, personOf(c, r))
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return v, nil
			}})
		g.Handle(c, gateway.Route{Method: "PATCH", Path: "/api/teams/{id}", Devices: mobile,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var in Input
				if err := r.Decode(&in); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				v, err := s.Update(r.Params["id"], in)
				if errors.Is(err, os.ErrNotExist) {
					return nil, gateway.NotFound()
				}
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return v, nil
			}})
		g.Handle(c, gateway.Route{Method: "DELETE", Path: "/api/teams/{id}",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				err := s.Delete(r.Params["id"])
				if errors.Is(err, os.ErrNotExist) {
					return nil, gateway.NotFound()
				}
				if err != nil {
					return nil, gateway.Conflict("%v", err)
				}
				return nil, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/teams/{id}/agents",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b struct {
					Agent string `json:"agent"`
				}
				if err := r.Decode(&b); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				a, err := s.Move(b.Agent, r.Params["id"])
				if errors.Is(err, os.ErrNotExist) {
					return nil, gateway.NotFound()
				}
				if err != nil {
					return nil, gateway.Conflict("%v", err)
				}
				return map[string]any{"id": a.ID(), "team": a.Spec().TeamID()}, nil
			}})
		return nil
	},
})

// personOf names who is behind a request: a team member's id, else the
// host owner.
func personOf(c *kernel.Context, r *gateway.Request) string {
	type memberID interface{ MemberIDOf(deviceID string) string }
	if m, ok := kernel.Lookup[memberID](c, "members"); ok && r.DeviceID != "" {
		if id := m.MemberIDOf(r.DeviceID); id != "" {
			return id
		}
	}
	return "owner"
}
