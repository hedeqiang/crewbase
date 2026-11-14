// Package devices pairs phones and other computers with this Crewbase
// (docs/06 §8 设备分级). The desktop shows a one-time pairing code (or a
// QR code holding a link with it); the new device redeems it once for its
// own token. Only a SHA-256 of each token is stored, so devices.json
// leaks nothing usable. Every device has a tier:
//
//	mobile   view, approve, send messages (the default)
//	desktop  full control, like the local desktop token
//
// Devices can be renamed and revoked; revoking closes its open event
// streams at once. Pairing and revocation are written to the audit log.
package devices

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
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

	"github.com/hedeqiang/crewbase/kernel"
)

const ServiceKey = "devices"

// Tiers.
const (
	TierMobile  = "mobile"
	TierDesktop = "desktop"
	// TierViewer may only read (GET routes open to mobile).
	TierViewer = "viewer"
)

// RoleResolver maps a member to the tier of their role; ok=false when the
// member no longer exists (their devices then stop working). Implemented
// by the `members` service.
type RoleResolver interface {
	MemberTier(member string) (tier string, ok bool)
}

// Limits.
const (
	CodeTTL      = 5 * time.Minute
	maxPending   = 5
	maxDevices   = 50
	seenInterval = 5 * time.Minute // lastSeen is persisted at most this often
)

// codeAlphabet has no look-alikes (0/O, 1/I/L).
const codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// Device is a paired device (the token itself is never stored).
type Device struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Tier     string    `json:"tier"`
	Platform string    `json:"platform,omitempty"` // a hint from the device (e.g. "iPhone · Safari")
	Created  time.Time `json:"created"`
	LastSeen time.Time `json:"lastSeen,omitzero"`
	// Via is how it reaches this computer: "lan" (direct) or "relay".
	Via string `json:"via,omitempty"`
	// Member is the person the device belongs to ("" = the owner).
	Member string `json:"member,omitempty"`
	Hash   string `json:"hash"` // sha256(token), hex
}

// View is a device without its hash.
type View struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Tier     string    `json:"tier"`
	Platform string    `json:"platform,omitempty"`
	Created  time.Time `json:"created"`
	LastSeen time.Time `json:"lastSeen,omitzero"`
	Via      string    `json:"via,omitempty"`
	Member   string    `json:"member,omitempty"`
}

func (d Device) view() View {
	return View{ID: d.ID, Name: d.Name, Tier: d.Tier, Platform: d.Platform, Created: d.Created, LastSeen: d.LastSeen, Via: d.Via, Member: d.Member}
}

// Pairing is an open pairing code.
type Pairing struct {
	Code    string    `json:"code"` // XXXX-XXXX
	Tier    string    `json:"tier"`
	Member  string    `json:"member,omitempty"`
	Expires time.Time `json:"expires"`
}

// Paired is what a redeemed code returns to the new device (once).
type Paired struct {
	Token  string `json:"token"`
	Device View   `json:"device"`
}

// Event payloads.
type Event struct {
	Kind   string `json:"kind"` // paired | revoked | renamed
	Device View   `json:"device"`
}

// Changed fires on pair / revoke / rename.
var Changed = kernel.NewEmit[Event]("devices/changed")

// Errors.
var (
	ErrBadCode  = errors.New("the pairing code is wrong or has expired")
	ErrNotFound = errors.New("device not found")
)

// Service is the `devices` service.
type Service struct {
	path string
	now  func() time.Time
	// roles resolves member devices' tiers at every request (nil: members
	// are not loaded, so member devices are refused).
	roles func() (RoleResolver, bool)

	mu      sync.Mutex
	devices map[string]*Device
	pending map[string]Pairing // normalised code → pairing
	saved   map[string]time.Time
}

// New loads devices from dir/devices.json ("" = memory only).
func New(dir string) (*Service, error) {
	s := &Service{now: time.Now, devices: map[string]*Device{}, pending: map[string]Pairing{}, saved: map[string]time.Time{}}
	if dir != "" {
		s.path = filepath.Join(dir, "devices.json")
		b, err := os.ReadFile(s.path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if len(b) > 0 {
			var list []Device
			if err := json.Unmarshal(b, &list); err != nil {
				return nil, fmt.Errorf("devices.json: %w", err)
			}
			for i := range list {
				d := list[i]
				s.devices[d.ID] = &d
			}
		}
	}
	return s, nil
}

func hashToken(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

func randString(n int, alphabet string) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i, x := range b {
		// 256 % 31 bias is negligible for a short-lived code
		out[i] = alphabet[int(x)%len(alphabet)]
	}
	return string(out), nil
}

// normCode accepts codes typed with spaces, dashes or lower case.
func normCode(c string) string {
	c = strings.ToUpper(c)
	var b strings.Builder
	for _, r := range c {
		if strings.ContainsRune(codeAlphabet, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (s *Service) sweepLocked() {
	now := s.now()
	for k, p := range s.pending {
		if now.After(p.Expires) {
			delete(s.pending, k)
		}
	}
}

// SetRoles installs the member role resolver.
func (s *Service) SetRoles(f func() (RoleResolver, bool)) { s.roles = f }

func (s *Service) memberTier(member string) (string, bool) {
	if s.roles == nil {
		return "", false
	}
	r, ok := s.roles()
	if !ok {
		return "", false
	}
	return r.MemberTier(member)
}

// StartPairing opens a pairing code for a device of tier. For a member's
// device the tier comes from the member's role.
func (s *Service) StartPairing(tier string) (Pairing, error) { return s.StartPairingFor(tier, "") }

// StartPairingFor opens a code for a device of member ("" = the owner).
func (s *Service) StartPairingFor(tier, member string) (Pairing, error) {
	if member != "" {
		t, ok := s.memberTier(member)
		if !ok {
			return Pairing{}, fmt.Errorf("unknown member %q", member)
		}
		tier = t
	}
	if tier == "" {
		tier = TierMobile
	}
	if tier != TierMobile && tier != TierDesktop && tier != TierViewer {
		return Pairing{}, fmt.Errorf("unknown tier %q", tier)
	}
	raw, err := randString(8, codeAlphabet)
	if err != nil {
		return Pairing{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	if len(s.pending) >= maxPending {
		return Pairing{}, errors.New("too many open pairing codes; wait a few minutes")
	}
	if len(s.devices) >= maxDevices {
		return Pairing{}, fmt.Errorf("at most %d devices can be paired", maxDevices)
	}
	p := Pairing{Code: raw[:4] + "-" + raw[4:], Tier: tier, Member: member, Expires: s.now().Add(CodeTTL)}
	s.pending[raw] = p
	return p, nil
}

// CancelPairing closes an open code.
func (s *Service) CancelPairing(code string) {
	s.mu.Lock()
	delete(s.pending, normCode(code))
	s.mu.Unlock()
}

// Pending lists open codes.
func (s *Service) Pending() []Pairing {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	out := make([]Pairing, 0, len(s.pending))
	for _, p := range s.pending {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Expires.Before(out[j].Expires) })
	return out
}

func cleanName(n string) string {
	n = strings.Join(strings.Fields(n), " ")
	n = strings.Map(func(r rune) rune {
		if r < 0x20 {
			return -1
		}
		return r
	}, n)
	if utf8.RuneCountInString(n) > 40 {
		n = string([]rune(n)[:40])
	}
	return n
}

// Redeem consumes a code and creates the device. The code works once.
func (s *Service) Redeem(code, name, platform, via string) (Paired, error) {
	k := normCode(code)
	s.mu.Lock()
	s.sweepLocked()
	p, ok := s.pending[k]
	if !ok || len(k) != 8 {
		s.mu.Unlock()
		return Paired{}, ErrBadCode
	}
	delete(s.pending, k)
	s.mu.Unlock()

	tok, err := randString(43, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
	if err != nil {
		return Paired{}, err
	}
	tok = "cbd_" + tok // recognisable as a Crewbase device token (secret scanners)
	id, err := randString(10, "abcdefghijkmnpqrstuvwxyz23456789")
	if err != nil {
		return Paired{}, err
	}
	name = cleanName(name)
	if name == "" {
		name = "未命名设备"
	}
	if via == "" {
		via = "lan"
	}
	d := &Device{ID: "dev_" + id, Name: name, Tier: p.Tier, Member: p.Member, Platform: cleanName(platform), Created: s.now().UTC(), LastSeen: s.now().UTC(), Via: via, Hash: hashToken(tok)}
	s.mu.Lock()
	s.devices[d.ID] = d
	err = s.saveLocked()
	if err != nil {
		delete(s.devices, d.ID)
	}
	s.mu.Unlock()
	if err != nil {
		return Paired{}, err
	}
	return Paired{Token: tok, Device: d.view()}, nil
}

// Verify maps a token to its device (constant time over the hash).
func (s *Service) Verify(tok string) (View, bool) {
	if !strings.HasPrefix(tok, "cbd_") {
		return View{}, false
	}
	h := []byte(hashToken(tok))
	s.mu.Lock()
	defer s.mu.Unlock()
	var found *Device
	for _, d := range s.devices {
		if subtle.ConstantTimeCompare(h, []byte(d.Hash)) == 1 {
			found = d
		}
	}
	if found == nil {
		return View{}, false
	}
	if found.Member != "" {
		// the member's current role decides; a removed member's devices stop
		tier, ok := s.memberTier(found.Member)
		if !ok {
			return View{}, false
		}
		found.Tier = tier
	}
	now := s.now().UTC()
	found.LastSeen = now
	if now.Sub(s.saved[found.ID]) > seenInterval {
		s.saved[found.ID] = now
		_ = s.saveLocked()
	}
	return found.view(), true
}

// List returns devices, most recently seen first.
func (s *Service) List() []View {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]View, 0, len(s.devices))
	for _, d := range s.devices {
		out = append(out, d.view())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}

// RevokeMember removes every device of member; it returns them.
func (s *Service) RevokeMember(member string) []View {
	if member == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []View
	for id, d := range s.devices {
		if d.Member == member {
			out = append(out, d.view())
			delete(s.devices, id)
		}
	}
	if len(out) > 0 {
		_ = s.saveLocked()
	}
	return out
}

// Get returns one device.
func (s *Service) Get(id string) (View, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[id]
	if !ok {
		return View{}, false
	}
	return d.view(), true
}

// Rename changes a device's name.
func (s *Service) Rename(id, name string) (View, error) {
	name = cleanName(name)
	if name == "" {
		return View{}, errors.New("a name is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[id]
	if !ok {
		return View{}, ErrNotFound
	}
	d.Name = name
	return d.view(), s.saveLocked()
}

// Revoke removes a device; its token stops working immediately.
func (s *Service) Revoke(id string) (View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[id]
	if !ok {
		return View{}, ErrNotFound
	}
	delete(s.devices, id)
	delete(s.saved, id)
	if err := s.saveLocked(); err != nil {
		s.devices[id] = d
		return View{}, err
	}
	return d.view(), nil
}

func (s *Service) saveLocked() error {
	if s.path == "" {
		return nil
	}
	list := make([]Device, 0, len(s.devices))
	for _, d := range s.devices {
		list = append(list, *d)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Created.Before(list[j].Created) })
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
