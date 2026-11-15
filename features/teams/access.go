package teams

// Access: what the person behind a device may see and do (phase 2).
//
//   - The host's owner and admins see every team (they run the host).
//   - Other people see public teams and the teams they are in; a private
//     team is invisible to everyone else.
//   - Blank chats are personal: only the person who started one sees it.
//   - Acting (sending, approving, changing) in a team needs a lead or
//     member role in it, or host owner / admin. Team viewers only read.
//
// It is enforced in three places: the gateway guard (every route on one
// agent, session, channel, approval or team), the list endpoints (agents,
// sessions, channels, approvals, teams) and the event stream filter.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// Who is a resolved person.
type Who struct {
	ID       string // "owner" for the host's owner, else the member id
	HostRole string // owner | admin | member | viewer
}

func (w Who) admin() bool { return w.HostRole == "owner" || w.HostRole == "admin" }

// people is the members service as access needs it.
type people interface {
	MemberIDOf(deviceID string) string
	HostRoleOf(deviceID string) string
}

// WhoIs resolves a device ("" = the local desktop token, the owner).
func (s *Service) WhoIs(deviceID string) Who {
	if deviceID == "" {
		return Who{ID: "owner", HostRole: "owner"}
	}
	p, ok := kernel.Lookup[people](s.c, "members")
	if !ok {
		return Who{ID: "owner", HostRole: "owner"}
	}
	id := p.MemberIDOf(deviceID)
	if id == "" {
		return Who{ID: "owner", HostRole: "owner"} // a device paired by the owner
	}
	return Who{ID: id, HostRole: p.HostRoleOf(deviceID)}
}

// RoleIn is w's role in a team ("" = not a member).
func (s *Service) RoleIn(w Who, team string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.items[agent.TeamOf(team)]
	if !ok {
		return ""
	}
	for _, p := range t.People {
		if p.ID == w.ID {
			return p.Role
		}
	}
	return ""
}

// TeamVisible reports whether w sees the team.
func (s *Service) TeamVisible(w Who, team string) bool {
	if w.admin() {
		return true
	}
	team = agent.TeamOf(team)
	s.mu.Lock()
	t, ok := s.items[team]
	s.mu.Unlock()
	if !ok {
		return false
	}
	return t.Visibility == Public || s.RoleIn(w, team) != ""
}

// CanAct reports whether w may send, approve and change things in team.
func (s *Service) CanAct(w Who, team string) bool {
	if w.admin() {
		return true
	}
	r := s.RoleIn(w, team)
	return r == RoleLead || r == RoleMember
}

// CanManage reports whether w may change the team itself.
func (s *Service) CanManage(w Who, team string) bool {
	return w.admin() || s.RoleIn(w, team) == RoleLead
}

// AgentVisible reports whether w sees agent a.
func (s *Service) AgentVisible(w Who, a *agent.Agent) bool {
	sp := a.Spec()
	if sp.Kind == agent.KindChat {
		owner := sp.Owner
		if owner == "" {
			owner = "owner"
		}
		return owner == w.ID
	}
	if sp.Ephemeral && sp.Parent != "" {
		if p, ok := s.agents.Get(sp.Parent); ok {
			return s.AgentVisible(w, p)
		}
	}
	return s.TeamVisible(w, sp.TeamID())
}

// agentAct reports whether w may act on agent a.
func (s *Service) agentAct(w Who, a *agent.Agent) bool {
	sp := a.Spec()
	if sp.Kind == agent.KindChat {
		return s.AgentVisible(w, a)
	}
	return s.CanAct(w, sp.TeamID())
}

// SessionVisible reports whether w sees a session.
func (s *Service) SessionVisible(w Who, id string) bool {
	ss, err := s.agents.Sessions().Get(id)
	if err != nil {
		return false
	}
	m := ss.Meta()
	if m.Kind == "channel" {
		return s.TeamVisible(w, m.Team)
	}
	for _, x := range m.Agents {
		if a, ok := s.agents.Get(x); ok {
			return s.AgentVisible(w, a)
		}
	}
	if m.Parent != "" && m.Parent != id {
		return s.SessionVisible(w, m.Parent)
	}
	// a session nobody owns any more (deleted agent): admins only
	return w.admin()
}

func (s *Service) sessionAct(w Who, id string) bool {
	ss, err := s.agents.Sessions().Get(id)
	if err != nil {
		return false
	}
	m := ss.Meta()
	if m.Kind == "channel" {
		return s.CanAct(w, m.Team)
	}
	for _, x := range m.Agents {
		if a, ok := s.agents.Get(x); ok {
			return s.agentAct(w, a)
		}
	}
	return w.admin()
}

var (
	errHidden = gateway.NotFound() // invisible things do not exist
	errRead   = &gateway.Error{Status: 403, Code: "forbidden", Message: "你在这个团队里只能查看，不能操作；请团队负责人把你的角色改为成员"}
)

// guard is the gateway guard.
func (s *Service) guard(r *gateway.Request, route string) error {
	w := s.WhoIs(r.DeviceID)
	if w.HostRole == "owner" {
		// the owner reaches everything except other people's chats
		if strings.HasPrefix(route, "/api/agents/{id}") {
			if a, ok := s.agents.Get(r.Params["id"]); ok && !s.AgentVisible(w, a) {
				return errHidden
			}
		}
		return nil
	}
	write := r.HTTP.Method != "GET"
	switch {
	case strings.HasPrefix(route, "/api/agents/{id}"):
		a, ok := s.agents.Get(r.Params["id"])
		if !ok {
			return nil // the handler answers 404
		}
		if !s.AgentVisible(w, a) {
			return errHidden
		}
		if write && !s.agentAct(w, a) {
			return errRead
		}
	case strings.HasPrefix(route, "/api/sessions/{id}"), strings.HasPrefix(route, "/api/channels/{id}"):
		id := r.Params["id"]
		if _, err := s.agents.Sessions().Get(id); err != nil {
			return nil
		}
		if !s.SessionVisible(w, id) {
			return errHidden
		}
		if write && !s.sessionAct(w, id) {
			return errRead
		}
	case strings.HasPrefix(route, "/api/approvals/{"):
		if in, ok := kernel.Lookup[*approval.Inbox](s.c, approval.InboxKey); ok {
			for _, p := range in.Pending() {
				if p.CallID != r.Params["callId"] {
					continue
				}
				if !s.SessionVisible(w, p.Session) {
					return errHidden
				}
				if write && !s.sessionAct(w, p.Session) {
					return errRead
				}
			}
		}
	case strings.HasPrefix(route, "/api/teams/{id}"):
		id := r.Params["id"]
		if !s.TeamVisible(w, id) {
			return errHidden
		}
		if write && !strings.HasSuffix(route, "/join") && !s.CanManage(w, id) {
			return &gateway.Error{Status: 403, Code: "forbidden", Message: "只有团队负责人或管理员能修改这个团队"}
		}
	case route == "/api/agents" && write:
		var b struct {
			Kind string `json:"kind"`
			Team string `json:"team"`
		}
		_ = json.Unmarshal(r.Body, &b)
		if b.Kind != agent.KindChat && !s.CanAct(w, b.Team) {
			return &gateway.Error{Status: 403, Code: "forbidden", Message: "你不能在这个团队里新建 Agent"}
		}
	case route == "/api/channels" && write:
		var b struct {
			Members []string `json:"members"`
		}
		_ = json.Unmarshal(r.Body, &b)
		for _, id := range b.Members {
			if a, ok := s.agents.Get(id); ok && !s.agentAct(w, a) {
				return errRead
			}
		}
	}
	return nil
}

// eventVisible is the event stream filter.
func (s *Service) eventVisible(deviceID string, m gateway.Message) bool {
	w := s.WhoIs(deviceID)
	ref := map[string]any{}
	switch d := m.Data.(type) {
	case nil:
		return true
	case map[string]any:
		ref = d
	default:
		b, err := json.Marshal(d)
		if err != nil || json.Unmarshal(b, &ref) != nil {
			return true
		}
	}
	str := func(k string) string { v, _ := ref[k].(string); return v }
	if id := str("agent"); id != "" {
		if a, ok := s.agents.Get(id); ok && !s.AgentVisible(w, a) {
			return false
		}
	}
	if m.Type == "agent.updated" {
		if a, ok := s.agents.Get(str("id")); ok && !s.AgentVisible(w, a) {
			return false
		}
	}
	for _, k := range []string{"session", "channel", "root"} {
		if id := str(k); id != "" && !s.SessionVisible(w, id) {
			if _, err := s.agents.Sessions().Get(id); err == nil {
				return false
			}
		}
	}
	return true
}

// Visible is what the list endpoints use (the `access` service).
type Visible struct{ s *Service }

// Agent reports whether the person behind deviceID sees agent a.
func (v Visible) Agent(deviceID string, a *agent.Agent) bool {
	return v.s.AgentVisible(v.s.WhoIs(deviceID), a)
}

// Session reports whether the person behind deviceID sees a session.
func (v Visible) Session(deviceID, id string) bool {
	return v.s.SessionVisible(v.s.WhoIs(deviceID), id)
}

// ChatOwner is who owns a chat started from deviceID ("" = the host owner).
func (v Visible) ChatOwner(deviceID string) string {
	if w := v.s.WhoIs(deviceID); w.ID != "owner" {
		return w.ID
	}
	return ""
}

// AccessKey is the service key of Visible.
const AccessKey = "access"

func (w Who) String() string { return fmt.Sprintf("%s(%s)", w.ID, w.HostRole) }
