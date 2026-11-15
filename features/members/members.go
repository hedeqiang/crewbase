// Package members lets several people share one Crewbase (docs/06 §8,
// 团队版). The owner is whoever holds the local desktop token; other
// people are members, each with a role, connecting from their own paired
// devices:
//
//	admin   full control (desktop tier)
//	member  view, chat, approve the action categories they are trusted with (mobile tier)
//	viewer  read only (viewer tier)
//
// A member's role is resolved at every request, so changing it takes
// effect at once and removing a member disconnects all their devices.
// What people send and decide carries their name — into the conversation
// (message From), approvals (decided by) and the audit log.
package members

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/devices"
	"github.com/hedeqiang/crewbase/kernel"
)

const ServiceKey = "members"

// Roles.
const (
	RoleAdmin  = "admin"
	RoleMember = "member"
	RoleViewer = "viewer"
)

// Roles in order of trust.
var Roles = []string{RoleAdmin, RoleMember, RoleViewer}

// Approvable lists the action categories a person can be trusted to
// approve. Payment is never approvable remotely.
var Approvable = []string{
	tools.CategoryRead, tools.CategoryWrite, tools.CategoryExec, tools.CategoryInstall,
	tools.CategoryDelete, tools.CategoryPush, tools.CategoryPublish, tools.CategoryNone,
}

// DefaultApprove is what each role may approve unless changed.
func DefaultApprove(role string) []string {
	switch role {
	case RoleAdmin:
		return append([]string(nil), Approvable...)
	case RoleMember:
		return []string{tools.CategoryRead, tools.CategoryWrite, tools.CategoryExec, tools.CategoryInstall, tools.CategoryNone}
	}
	return []string{}
}

// Member is a person (not the owner).
type Member struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Role    string    `json:"role"`
	Approve []string  `json:"approve"` // categories this person may approve
	Created time.Time `json:"created"`
}

// Event reports member changes.
type Event struct {
	Kind   string `json:"kind"` // added | changed | removed
	Member Member `json:"member"`
	By     string `json:"by"`
}

// Changed fires on every change.
var Changed = kernel.NewEmit[Event]("members/changed")

// Errors.
var ErrNotFound = errors.New("member not found")

// Service is the `members` service.
type Service struct {
	path    string
	devices *devices.Service

	mu    sync.Mutex
	items map[string]*Member
}

// New loads dir/members.json ("" = memory only).
func New(dir string, ds *devices.Service) (*Service, error) {
	s := &Service{devices: ds, items: map[string]*Member{}}
	if dir == "" {
		return s, nil
	}
	s.path = filepath.Join(dir, "members.json")
	b, err := os.ReadFile(s.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(b) > 0 {
		var list []Member
		if err := json.Unmarshal(b, &list); err != nil {
			return nil, fmt.Errorf("members.json: %w", err)
		}
		for i := range list {
			m := list[i]
			s.items[m.ID] = &m
		}
	}
	return s, nil
}

func cleanName(n string) string {
	n = strings.Join(strings.Fields(n), " ")
	if utf8.RuneCountInString(n) > 30 {
		n = string([]rune(n)[:30])
	}
	return n
}

func cleanApprove(xs []string) ([]string, error) {
	out := []string{}
	for _, x := range xs {
		if x == tools.CategoryPayment {
			return nil, errors.New("payments are never approved remotely")
		}
		if !slices.Contains(Approvable, x) {
			return nil, fmt.Errorf("unknown category %q", x)
		}
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out, nil
}

// TierOf maps a role to a device tier.
func TierOf(role string) string {
	switch role {
	case RoleAdmin:
		return devices.TierDesktop
	case RoleMember:
		return devices.TierMobile
	}
	return devices.TierViewer
}

// Input creates or edits a member (nil = unchanged).
type Input struct {
	Name    *string   `json:"name,omitempty"`
	Role    *string   `json:"role,omitempty"`
	Approve *[]string `json:"approve,omitempty"`
}

// Add creates a member.
func (s *Service) Add(in Input) (Member, error) {
	if in.Name == nil || cleanName(*in.Name) == "" {
		return Member{}, errors.New("a name is required")
	}
	role := RoleMember
	if in.Role != nil {
		role = *in.Role
	}
	if !slices.Contains(Roles, role) {
		return Member{}, fmt.Errorf("unknown role %q", role)
	}
	m := Member{Name: cleanName(*in.Name), Role: role, Approve: DefaultApprove(role), Created: time.Now().UTC()}
	if in.Approve != nil && role != RoleViewer {
		a, err := cleanApprove(*in.Approve)
		if err != nil {
			return Member{}, err
		}
		m.Approve = a
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.items {
		if strings.EqualFold(x.Name, m.Name) {
			return Member{}, fmt.Errorf("%s is already a member", m.Name)
		}
	}
	if len(s.items) >= 100 {
		return Member{}, errors.New("at most 100 members")
	}
	base := "m" + fmt.Sprint(time.Now().UnixNano()%1e9)
	m.ID = base
	for i := 2; s.items[m.ID] != nil; i++ {
		m.ID = fmt.Sprintf("%s-%d", base, i)
	}
	s.items[m.ID] = &m
	if err := s.saveLocked(); err != nil {
		delete(s.items, m.ID)
		return Member{}, err
	}
	return m, nil
}

// Update edits a member. Changing the role resets approvals to the role's
// default unless Approve is given too.
func (s *Service) Update(id string, in Input) (Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.items[id]
	if !ok {
		return Member{}, ErrNotFound
	}
	next := *m
	if in.Name != nil {
		n := cleanName(*in.Name)
		if n == "" {
			return Member{}, errors.New("a name is required")
		}
		for _, x := range s.items {
			if x.ID != id && strings.EqualFold(x.Name, n) {
				return Member{}, fmt.Errorf("%s is already a member", n)
			}
		}
		next.Name = n
	}
	if in.Role != nil && *in.Role != next.Role {
		if !slices.Contains(Roles, *in.Role) {
			return Member{}, fmt.Errorf("unknown role %q", *in.Role)
		}
		next.Role = *in.Role
		next.Approve = DefaultApprove(next.Role)
	}
	if in.Approve != nil {
		a, err := cleanApprove(*in.Approve)
		if err != nil {
			return Member{}, err
		}
		next.Approve = a
	}
	if next.Role == RoleViewer {
		next.Approve = []string{}
	}
	*m = next
	return next, s.saveLocked()
}

// Remove deletes a member and revokes their devices.
func (s *Service) Remove(id string) (Member, []devices.View, error) {
	s.mu.Lock()
	m, ok := s.items[id]
	if !ok {
		s.mu.Unlock()
		return Member{}, nil, ErrNotFound
	}
	delete(s.items, id)
	err := s.saveLocked()
	s.mu.Unlock()
	if err != nil {
		return Member{}, nil, err
	}
	var revoked []devices.View
	if s.devices != nil {
		revoked = s.devices.RevokeMember(id)
	}
	return *m, revoked, nil
}

// List returns members by name.
func (s *Service) List() []Member {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Member, 0, len(s.items))
	for _, m := range s.items {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Get returns a member.
func (s *Service) Get(id string) (Member, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.items[id]
	if !ok {
		return Member{}, false
	}
	return *m, true
}

// MemberTier implements devices.RoleResolver.
func (s *Service) MemberTier(id string) (string, bool) {
	m, ok := s.Get(id)
	if !ok {
		return "", false
	}
	return TierOf(m.Role), true
}

// MemberExists implements taskboard.MemberLookup ("owner" is the owner).
func (s *Service) MemberExists(id string) bool {
	if id == "owner" {
		return true
	}
	_, ok := s.Get(id)
	return ok
}

// Person describes who is behind a device: the owner ("" device or one
// without a member) or a member.
type Person struct {
	ID      string // "" for the owner
	Name    string
	Role    string // "owner" for the owner
	Approve []string
}

// OwnerName is how the owner appears to others.
const OwnerName = "你"

// PersonOf resolves a device to its person.
func (s *Service) PersonOf(deviceID string) Person {
	owner := Person{Name: OwnerName, Role: "owner", Approve: append(append([]string(nil), Approvable...), tools.CategoryPayment)}
	if deviceID == "" || s.devices == nil {
		return owner
	}
	d, ok := s.devices.Get(deviceID)
	if !ok || d.Member == "" {
		return owner
	}
	m, ok := s.Get(d.Member)
	if !ok {
		return Person{ID: d.Member, Name: "?", Role: RoleViewer}
	}
	return Person{ID: m.ID, Name: m.Name, Role: m.Role, Approve: m.Approve}
}

// CanApprove reports whether the person behind deviceID may approve an
// action of category, and who they are.
func (s *Service) CanApprove(deviceID, category string) (string, bool) {
	p := s.PersonOf(deviceID)
	if p.Role == "owner" {
		return "", true // the owner's own devices: the gateway records the device
	}
	return p.Name, slices.Contains(p.Approve, category)
}

// SenderName is the From of messages sent from deviceID ("" for the owner).
func (s *Service) SenderName(deviceID string) string {
	p := s.PersonOf(deviceID)
	if p.Role == "owner" {
		return ""
	}
	return p.Name
}

// MemberIDOf is the member behind deviceID ("" = the owner).
func (s *Service) MemberIDOf(deviceID string) string { return s.PersonOf(deviceID).ID }

// PersonFor implements taskboard.PersonFunc.
func (s *Service) PersonFor(deviceID string) (string, bool) {
	p := s.PersonOf(deviceID)
	return p.Name, p.Role != RoleViewer
}

func (s *Service) saveLocked() error {
	if s.path == "" {
		return nil
	}
	list := make([]Member, 0, len(s.items))
	for _, m := range s.items {
		list = append(list, *m)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Created.Before(list[j].Created) })
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
