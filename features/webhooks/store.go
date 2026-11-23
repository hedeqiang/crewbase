package webhooks

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// Target kinds.
const (
	TargetWorkflow = "workflow"
	TargetRoutine  = "routine"
	TargetChannel  = "channel"
)

// Target is where a matching event goes.
type Target struct {
	Kind string `json:"kind"` // workflow | routine | channel
	ID   string `json:"id"`
	// Mention: an agent of the channel the event is addressed to (@it);
	// "" = the channel's owner.
	Mention string `json:"mention,omitempty"`
}

// Endpoint is an inbound webhook: who may call it (the provider's
// signature), which events count, and where they go.
type Endpoint struct {
	ID       string `json:"id"`
	Team     string `json:"team"`
	Name     string `json:"name"`
	Provider string `json:"provider"` // verifiers.GitHub …
	// Secret is a vault reference (vault:webhooks/<id>), never the secret.
	Secret  string `json:"secret"`
	Enabled bool   `json:"enabled"`
	// Events: event types that count ("" list = any), e.g. pull_request.
	Events []string `json:"events,omitempty"`
	// Filter is a condition on the event (cond.go).
	Filter string `json:"filter,omitempty"`
	// Instruction is what the agents are asked to do with an event
	// ({{path}} placeholders are filled from it, quoted).
	Instruction string   `json:"instruction,omitempty"`
	Targets     []Target `json:"targets"`
	// Cooldown: after a delivery triggers, the same CooldownKey value
	// (e.g. repository.full_name, "" = the whole endpoint) waits this long.
	Cooldown    int    `json:"cooldown,omitempty"` // seconds
	CooldownKey string `json:"cooldownKey,omitempty"`
	// RateLimit caps triggers per minute (0 = DefaultRateLimit).
	RateLimit int `json:"rateLimit,omitempty"`
	// Mode is the widest run mode its turns get: sandbox (default) or
	// read-only. Never host.
	Mode string `json:"mode,omitempty"`
	// Relay: also reachable through the relay at /v1/hooks/<RelayToken>.
	Relay      bool      `json:"relay,omitempty"`
	RelayToken string    `json:"relayToken,omitempty"`
	By         string    `json:"by,omitempty"`
	Created    time.Time `json:"created"`
	Updated    time.Time `json:"updated"`
}

// Subscription delivers internal events to an outside URL.
type Subscription struct {
	ID      string    `json:"id"`
	Team    string    `json:"team"`
	Name    string    `json:"name"`
	URL     string    `json:"url"`
	Events  []string  `json:"events"` // event names (OutEvents), "*" = all
	Filter  string    `json:"filter,omitempty"`
	Secret  string    `json:"secret"` // vault reference for the signature
	Enabled bool      `json:"enabled"`
	By      string    `json:"by,omitempty"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

// Delivery statuses.
const (
	// inbound
	StAccepted  = "accepted"  // verified and sent to its targets
	StFiltered  = "filtered"  // verified, but no event or condition matched
	StDuplicate = "duplicate" // a delivery seen before
	StCooldown  = "cooldown"  // matched, held back by the cooldown
	StLimited   = "limited"   // matched, over the rate limit
	StRejected  = "rejected"  // the signature did not verify
	StPartial   = "partial"   // some targets failed
	StDryRun    = "dry-run"   // a replay that only showed what would happen
	// outbound
	StPending   = "pending"
	StDelivered = "delivered"
	StDead      = "dead" // gave up after MaxAttempts; can be replayed
)

// TargetResult is what one target did with an event.
type TargetResult struct {
	Target
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Ref    string `json:"ref,omitempty"` // run or session id
	Detail string `json:"detail,omitempty"`
}

// Delivery is one inbound event or one outbound attempt series.
type Delivery struct {
	ID       string            `json:"id"`
	Dir      string            `json:"dir"` // in | out
	Team     string            `json:"team"`
	Endpoint string            `json:"endpoint,omitempty"`     // in
	Sub      string            `json:"subscription,omitempty"` // out
	Name     string            `json:"name"`                   // the endpoint's / subscription's
	Event    string            `json:"event,omitempty"`
	Key      string            `json:"key,omitempty"` // provider delivery id (in) / the id we sent (out)
	Via      string            `json:"via,omitempty"` // direct | relay | replay | tool
	Status   string            `json:"status"`
	Error    string            `json:"error,omitempty"`
	Code     int               `json:"code,omitempty"` // the response status (out)
	Attempts int               `json:"attempts,omitempty"`
	Next     time.Time         `json:"next,omitzero"`
	Targets  []TargetResult    `json:"targets,omitempty"`
	Prompt   string            `json:"prompt,omitempty"` // what the agents were given (in)
	Headers  map[string]string `json:"headers,omitempty"`
	Body     json.RawMessage   `json:"body,omitempty"` // kept for replay (capped)
	BodyText string            `json:"bodyText,omitempty"`
	At       time.Time         `json:"at"`
	Updated  time.Time         `json:"updated"`
}

// Settings of the webhooks feature.
type Settings struct {
	// AllowPrivate: address ranges outbound deliveries may reach although
	// they are not public (an admin's own network). Loopback and
	// link-local are refused unless listed here exactly.
	AllowPrivate []string `json:"allowPrivate"`
	MaxAttempts  int      `json:"maxAttempts"`
}

// DefaultSettings are the settings before any change.
func DefaultSettings() Settings { return Settings{AllowPrivate: []string{}, MaxAttempts: 8} }

// Limits.
const (
	DefaultRateLimit = 30      // triggers per minute per endpoint
	MaxBody          = 1 << 20 // an inbound body
	MaxKeptBody      = 256 << 10
	MaxDeliveries    = 1000 // kept (pending ones always)
	DedupeWindow     = 48 * time.Hour
)

type state struct {
	Endpoints     []*Endpoint     `json:"endpoints"`
	Subscriptions []*Subscription `json:"subscriptions"`
	Settings      *Settings       `json:"settings,omitempty"`
}

type store struct {
	mu   sync.Mutex
	dir  string
	st   state
	dels []*Delivery // newest last
}

func openStore(dir string) (*store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &store{dir: dir}
	if b, err := os.ReadFile(filepath.Join(dir, "state.json")); err == nil {
		if err := json.Unmarshal(b, &s.st); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if b, err := os.ReadFile(filepath.Join(dir, "deliveries.json")); err == nil {
		_ = json.Unmarshal(b, &s.dels)
	}
	return s, nil
}

func writeJSON(p string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// saveState / saveDeliveries: the caller holds mu.
func (s *store) saveState() error { return writeJSON(filepath.Join(s.dir, "state.json"), s.st) }

func (s *store) saveDeliveries() error {
	// keep the newest MaxDeliveries, and every pending one
	if n := len(s.dels); n > MaxDeliveries {
		keep := make([]*Delivery, 0, MaxDeliveries)
		drop := n - MaxDeliveries
		for _, d := range s.dels {
			if drop > 0 && d.Status != StPending {
				drop--
				continue
			}
			keep = append(keep, d)
		}
		s.dels = keep
	}
	return writeJSON(filepath.Join(s.dir, "deliveries.json"), s.dels)
}

func (s *store) settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.Settings == nil {
		return DefaultSettings()
	}
	x := *s.st.Settings
	x.AllowPrivate = slices.Clone(x.AllowPrivate)
	return x
}

func newID(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func randToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
