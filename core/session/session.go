// Package session implements the append-only session log, the single source
// of truth. Model history, UI, audit, fork and crash recovery are all
// projections of it. Files are `sessions/<id>/session.v1.jsonl`; written
// files are never rewritten.
package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/kernel"
)

// ServiceKey is the context key of the session store.
const ServiceKey = "sessions"

// Version is the current log format version.
const Version = 1

// Well-known event types. Plugins may define their own (namespaced) types.
const (
	TypeCreated          = "session/created"
	TypeUserMessage      = "message/user"
	TypeAssistantMessage = "message/assistant"
	TypeInject           = "agent/inject"
	TypeToolCall         = "tool/call"
	TypeToolResult       = "tool/result"
	TypeToolPruned       = "tool/pruned"
	TypeTurnStart        = "turn/start"
	TypeTurnEnd          = "turn/end"
	TypeStepStart        = "step/start"
	TypeStepEnd          = "step/end"
	TypeModelSwitched    = "model/switched"
	TypeCompaction       = "compaction/summary"
	TypeApprovalAsked    = "approval/asked"
	TypeApprovalDecided  = "approval/decided"
	TypeApprovalPolicy   = "approval/policy"
	TypeModeChanged      = "mode/changed"
	TypeInputQueued      = "input/queued"
	TypeInputClaimed     = "input/claimed"
	TypeInputCancelled   = "input/cancelled"
	TypeQueuePaused      = "queue/paused"
	TypeQueueResumed     = "queue/resumed"
	TypeError            = "turn/error"
	TypeNotice           = "notice"
)

// Event is one log record.
type Event struct {
	V       int             `json:"v"`
	Seq     int64           `json:"seq"`
	Time    time.Time       `json:"time"`
	Session string          `json:"session"`
	Type    string          `json:"type"`
	Agent   string          `json:"agent,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Decode unmarshals the event data.
func (e Event) Decode(v any) error {
	if len(e.Data) == 0 {
		return nil
	}
	return json.Unmarshal(e.Data, v)
}

// Meta describes a session.
type Meta struct {
	ID      string    `json:"id"`
	Title   string    `json:"title,omitempty"`
	Kind    string    `json:"kind"` // agent | channel
	Agents  []string  `json:"agents,omitempty"`
	Folder  string    `json:"folder,omitempty"`
	Parent  string    `json:"parent,omitempty"` // forked from
	Created time.Time `json:"created"`
}

// Appended is emitted after every successful append (scoped to the agent).
type Appended struct {
	Session string
	Event   Event
}

// OnEvent notifies that an event was durably appended.
var OnEvent = kernel.NewEmit[Appended]("session/event")

// Store is the `sessions` service.
type Store struct {
	dir  string // "" = in-memory
	host *kernel.Host

	mu       sync.Mutex
	sessions map[string]*Session
}

// NewStore creates a store rooted at dir ("" keeps everything in memory).
func NewStore(h *kernel.Host, dir string) (*Store, error) {
	s := &Store{dir: dir, host: h, sessions: map[string]*Session{}}
	if dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Session is an open, append-only log.
type Session struct {
	store      *Store
	meta       Meta
	mu         sync.Mutex
	events     []Event
	file       *os.File
	failAppend error // test hook: force append failures
}

// Create creates a new session.
func (s *Store) Create(meta Meta) (*Session, error) {
	if meta.ID == "" {
		meta.ID = NewID("s")
	}
	if meta.Created.IsZero() {
		meta.Created = time.Now().UTC()
	}
	s.mu.Lock()
	if _, ok := s.sessions[meta.ID]; ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("session %s exists", meta.ID)
	}
	ss := &Session{store: s, meta: meta}
	s.sessions[meta.ID] = ss
	s.mu.Unlock()
	if s.dir != "" {
		d := filepath.Join(s.dir, meta.ID)
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
		if _, err := os.Stat(filepath.Join(d, logName())); err == nil {
			return nil, fmt.Errorf("session %s exists on disk", meta.ID)
		}
	}
	if _, err := ss.Append(TypeCreated, "", meta); err != nil {
		return nil, err
	}
	return ss, nil
}

func logName() string { return fmt.Sprintf("session.v%d.jsonl", Version) }

// Get returns an open session, loading it from disk if needed.
func (s *Store) Get(id string) (*Session, error) {
	s.mu.Lock()
	ss, ok := s.sessions[id]
	s.mu.Unlock()
	if ok {
		return ss, nil
	}
	if s.dir == "" {
		return nil, fmt.Errorf("session %s not found", id)
	}
	events, err := readLog(filepath.Join(s.dir, id, logName()))
	if err != nil {
		return nil, err
	}
	if len(events) == 0 || events[0].Type != TypeCreated {
		return nil, fmt.Errorf("session %s: missing %s", id, TypeCreated)
	}
	ss = &Session{store: s, events: events}
	if err := events[0].Decode(&ss.meta); err != nil {
		return nil, err
	}
	// Apply meta updates recorded after creation.
	for _, e := range events {
		if e.Type == "session/meta" {
			_ = e.Decode(&ss.meta)
		}
	}
	s.mu.Lock()
	if prev, ok := s.sessions[id]; ok {
		ss = prev
	} else {
		s.sessions[id] = ss
	}
	s.mu.Unlock()
	return ss, nil
}

// List returns all sessions' meta, newest first.
func (s *Store) List() []Meta {
	if s.dir != "" {
		entries, _ := os.ReadDir(s.dir)
		for _, e := range entries {
			if e.IsDir() {
				_, _ = s.Get(e.Name())
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Meta, 0, len(s.sessions))
	for _, ss := range s.sessions {
		out = append(out, ss.Meta())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

func readLog(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			// A torn final line after a crash is dropped; anything else is corruption.
			if !sc.Scan() {
				break
			}
			return nil, fmt.Errorf("%s: corrupt line %d: %w", path, len(out)+1, err)
		}
		if e.V > Version {
			return nil, fmt.Errorf("%s: log version %d is newer than supported %d", path, e.V, Version)
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// ID returns the session id.
func (ss *Session) ID() string { return ss.meta.ID }

// Meta returns a copy of the session meta.
func (ss *Session) Meta() Meta {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	m := ss.meta
	m.Agents = append([]string(nil), m.Agents...)
	return m
}

// UpdateMeta records a meta change as an event.
func (ss *Session) UpdateMeta(fn func(*Meta)) error {
	m := ss.Meta()
	fn(&m)
	if _, err := ss.Append("session/meta", "", m); err != nil {
		return err
	}
	ss.mu.Lock()
	ss.meta = m
	ss.mu.Unlock()
	return nil
}

// Append durably records an event. When it returns an error, the event did
// not happen (callers such as approval must fail closed).
func (ss *Session) Append(typ, agent string, data any) (Event, error) {
	var raw json.RawMessage
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return Event{}, err
		}
		raw = b
	}
	ss.mu.Lock()
	if ss.failAppend != nil {
		err := ss.failAppend
		ss.mu.Unlock()
		return Event{}, err
	}
	e := Event{V: Version, Seq: int64(len(ss.events)) + 1, Time: time.Now().UTC(), Session: ss.meta.ID, Type: typ, Agent: agent, Data: raw}
	if ss.store.dir != "" {
		if ss.file == nil {
			f, err := os.OpenFile(filepath.Join(ss.store.dir, ss.meta.ID, logName()), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				ss.mu.Unlock()
				return Event{}, err
			}
			ss.file = f
		}
		line, _ := json.Marshal(e)
		if _, err := ss.file.Write(append(line, '\n')); err != nil {
			ss.mu.Unlock()
			return Event{}, err
		}
	}
	ss.events = append(ss.events, e)
	ss.mu.Unlock()
	if ss.store.host != nil {
		OnEvent.Emit(kernel.For(ss.store.host, agent), Appended{Session: ss.meta.ID, Event: e})
	}
	return e, nil
}

// MustAppend appends and panics on failure (for events whose loss is a bug).
func (ss *Session) MustAppend(typ, agent string, data any) Event {
	e, err := ss.Append(typ, agent, data)
	if err != nil {
		panic(err)
	}
	return e
}

// SetFailAppend forces Append to fail (testing fail-closed paths).
func (ss *Session) SetFailAppend(err error) {
	ss.mu.Lock()
	ss.failAppend = err
	ss.mu.Unlock()
}

// Events returns a snapshot of all events.
func (ss *Session) Events() []Event {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return append([]Event(nil), ss.events...)
}

// Since returns events with Seq > seq.
func (ss *Session) Since(seq int64) []Event {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if seq < 0 {
		seq = 0
	}
	if int(seq) >= len(ss.events) {
		return nil
	}
	return append([]Event(nil), ss.events[seq:]...)
}

// Last returns the latest event of the given type.
func (ss *Session) Last(typ string) (Event, bool) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	for i := len(ss.events) - 1; i >= 0; i-- {
		if ss.events[i].Type == typ {
			return ss.events[i], true
		}
	}
	return Event{}, false
}

// Close releases the file handle.
func (ss *Session) Close() error {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.file != nil {
		err := ss.file.Close()
		ss.file = nil
		return err
	}
	return nil
}

// Fork creates a new session containing this session's events up to and
// including seq (a turn boundary). The new log starts with session/created
// and replays the copied events under new sequence numbers.
func (s *Store) Fork(src *Session, uptoSeq int64, meta Meta) (*Session, error) {
	meta.Parent = src.ID()
	if meta.Kind == "" {
		meta.Kind = src.Meta().Kind
		meta.Agents = src.Meta().Agents
	}
	ns, err := s.Create(meta)
	if err != nil {
		return nil, err
	}
	for _, e := range src.Events() {
		if e.Seq > uptoSeq {
			break
		}
		if e.Type == TypeCreated || e.Type == "session/meta" {
			continue
		}
		if _, err := ns.Append(e.Type, e.Agent, e.Data); err != nil {
			return nil, err
		}
	}
	return ns, nil
}

// Close closes all open files.
func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ss := range s.sessions {
		_ = ss.Close()
	}
}

var idMu sync.Mutex
var lastID string

// NewID returns a sortable unique id with a prefix.
func NewID(prefix string) string {
	idMu.Lock()
	defer idMu.Unlock()
	for {
		id := prefix + "_" + strings.ToLower(fmt.Sprintf("%x", time.Now().UnixNano()))
		if id != lastID {
			lastID = id
			return id
		}
	}
}

// ErrNotFound is returned for unknown sessions.
var ErrNotFound = errors.New("session not found")

// Plugin provides the `sessions` service. Config: {"dir": "<path>"}; empty
// dir keeps sessions in memory.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/session",
	Description: "Append-only session log (sessions service).",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[struct {
			Dir string `json:"dir"`
		}](c)
		if err != nil {
			return err
		}
		st, err := NewStore(c.Host(), cfg.Dir)
		if err != nil {
			return err
		}
		c.Defer(st.Close)
		return c.Provide(ServiceKey, st)
	},
})
