// Package memory is memory-markdown-sqlite: each agent's long-term memory
// is its MEMORY.md (human-editable, the source of truth, injected into the
// system prompt by the agents plugin); an SQLite FTS5 index at
// <root>/index.sqlite covers memory entries and the text of every session
// (user/assistant messages, tool calls, truncated tool results). The index
// is derived data: it is updated incrementally from session/event and can
// be rebuilt from the logs with Reindex.
//
// Indexing is asynchronous: appended events go through a bounded queue to
// one worker goroutine, so session appends never wait on SQLite. When the
// queue is full the event is dropped and the index marked dirty; the worker
// then runs a catch-up pass from the session logs. Searches wait briefly
// for the queue to drain (read-your-writes for history_search).
//
// Two FTS5 indexes share the rows: the word index (unicode61) and a
// trigram index (docs_tri, external content) used for queries containing
// CJK text, where words are not space-separated. CJK terms shorter than
// three characters cannot be matched by trigrams and use the substring
// fallback.
//
// Tools: memory_add (write), memory_search, history_search, history_read
// (read). history_* let an agent recall parts of a conversation that were
// compacted away (C-2).
package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"

	_ "modernc.org/sqlite" // pure-Go SQLite with FTS5
)

// ServiceKey is the context key of the memory service.
const ServiceKey = "memory"

// Config of the plugin.
type Config struct {
	// Root holds index.sqlite; defaults to the agents data root.
	Root string `json:"root"`
	// ToolResultChars truncates indexed tool results.
	ToolResultChars int `json:"toolResultChars"`
	// ReadMaxChars caps history_read output.
	ReadMaxChars int `json:"readMaxChars"`
}

// Hit is one search result.
type Hit struct {
	Kind    string `json:"kind"` // message | memory
	Agent   string `json:"agent,omitempty"`
	Session string `json:"session,omitempty"`
	Seq     int64  `json:"seq"` // event seq (message) or line number (memory)
	Role    string `json:"role"`
	Time    string `json:"time,omitempty"`
	Snippet string `json:"snippet"`
}

// Store is the `memory` service.
type Store struct {
	c      *kernel.Context
	cfg    Config
	agents *agent.Registry
	db     *sql.DB
	path   string

	mu       sync.Mutex // serialises index writes
	memMtime map[string]time.Time

	queue chan queued
	dirty atomic.Bool // events were dropped: the worker catches up from the logs
}

// QueueSize bounds the indexing queue.
const QueueSize = 1024

// queued is one indexing job: an event, or a barrier (done) closed once
// everything enqueued before it is indexed.
type queued struct {
	ev   session.Event
	done chan struct{}
}

const schema = `
CREATE VIRTUAL TABLE IF NOT EXISTS docs USING fts5(
  text, kind UNINDEXED, agent UNINDEXED, session UNINDEXED, seq UNINDEXED, role UNINDEXED, time UNINDEXED,
  tokenize = 'unicode61 remove_diacritics 2'
);
CREATE TABLE IF NOT EXISTS indexed (session TEXT PRIMARY KEY, last_seq INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS session_agents (session TEXT NOT NULL, agent TEXT NOT NULL, PRIMARY KEY (session, agent));
`

// triSchema is the trigram index over docs (external content: the text is
// stored once, in docs; rowids match).
const triSchema = `CREATE VIRTUAL TABLE docs_tri USING fts5(text, content='docs', content_rowid='rowid', tokenize='trigram')`

// Plugin provides the `memory` service and the memory/history tools.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/memory-markdown-sqlite",
	Inject:      []string{agent.ServiceKey, session.ServiceKey, tools.ServiceKey},
	Description: "Long-term memory: MEMORY.md + SQLite FTS5 index of memory and sessions; memory_* and history_* tools.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		st, err := Open(c, cfg)
		if err != nil {
			return err
		}
		c.Defer(func() { _ = st.db.Close() })
		session.OnEvent.Listen(c, func(ap session.Appended) { st.enqueue(ap.Event) })
		c.Go(st.worker)
		st.registerTools(kernel.Use[*tools.Registry](c, tools.ServiceKey))
		if err := c.Provide(ServiceKey, st); err != nil {
			return err
		}
		// Catch up on sessions written while the plugin was not loaded.
		c.Go(func(ctx context.Context) {
			if err := st.CatchUp(ctx); err != nil && ctx.Err() == nil {
				c.Log().Warn("memory: catch-up", "err", err)
			}
		})
		return nil
	},
})

// Open opens (creating if needed) the index.
func Open(c *kernel.Context, cfg Config) (*Store, error) {
	ag := kernel.Use[*agent.Registry](c, agent.ServiceKey)
	if cfg.Root == "" {
		cfg.Root = ag.Root()
	}
	if cfg.ToolResultChars <= 0 {
		cfg.ToolResultChars = 2000
	}
	if cfg.ReadMaxChars <= 0 {
		cfg.ReadMaxChars = 8000
	}
	if err := os.MkdirAll(cfg.Root, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(cfg.Root, "index.sqlite")
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("memory: init index: %w", err)
	}
	if err := initTrigram(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("memory: init trigram index: %w", err)
	}
	return &Store{c: c, cfg: cfg, agents: ag, db: db, path: path, memMtime: map[string]time.Time{}, queue: make(chan queued, QueueSize)}, nil
}

// initTrigram creates the trigram index, filling it from docs when an
// existing index predates it.
func initTrigram(db *sql.DB) error {
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'docs_tri'`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(triSchema); err != nil {
		return err
	}
	// ('rebuild' cannot read an FTS5 content table; copy the rows instead.)
	if _, err := tx.Exec(`INSERT INTO docs_tri(rowid, text) SELECT rowid, text FROM docs`); err != nil {
		return err
	}
	return tx.Commit()
}

// enqueue hands an appended event to the worker without blocking: on
// overflow the index is marked dirty and caught up from the logs.
func (s *Store) enqueue(e session.Event) {
	select {
	case s.queue <- queued{ev: e}:
	default:
		s.dirty.Store(true)
	}
}

// worker indexes queued events in order; after an overflow it runs a
// catch-up pass once the queue is empty (or at a barrier).
func (s *Store) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case q := <-s.queue:
			if q.done == nil {
				if err := s.indexEvent(q.ev); err != nil {
					s.c.Log().Warn("memory: index event", "session", q.ev.Session, "seq", q.ev.Seq, "err", err)
				}
			}
			if s.dirty.Load() && (q.done != nil || len(s.queue) == 0) {
				s.catchUpDirty(ctx)
			}
			if q.done != nil {
				close(q.done)
			}
		}
	}
}

func (s *Store) catchUpDirty(ctx context.Context) {
	s.dirty.Store(false)
	var errs []error
	for _, m := range s.agents.Sessions().List() {
		if ctx.Err() != nil {
			s.dirty.Store(true)
			return
		}
		if err := s.IndexSession(m.ID); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		s.c.Log().Warn("memory: catch-up after queue overflow", "err", err)
	}
}

// Sync waits until every event appended before the call is indexed (or ctx
// ends). Searches call it with a short timeout.
func (s *Store) Sync(ctx context.Context) error {
	done := make(chan struct{})
	select {
	case s.queue <- queued{done: done}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// syncWait is how long a search waits for pending indexing.
const syncWait = 2 * time.Second

func (s *Store) syncBrief() {
	ctx, cancel := context.WithTimeout(s.c.Context(), syncWait)
	defer cancel()
	_ = s.Sync(ctx)
}

// Path returns the index file path.
func (s *Store) Path() string { return s.path }

// ---------------------------------------------------------------- indexing

type row struct {
	text, role string
	agent      string
}

// rows extracts indexable text from an event.
func (s *Store) rows(e session.Event) []row {
	switch e.Type {
	case session.TypeUserMessage:
		var m session.UserMessage
		if e.Decode(&m) != nil || strings.TrimSpace(m.Text) == "" {
			return nil
		}
		role := "user"
		if m.From != "" {
			role = "user:" + m.From
		}
		return []row{{text: m.Text, role: role, agent: m.To}}
	case session.TypeInject:
		var m session.InjectData
		if e.Decode(&m) != nil || strings.TrimSpace(m.Text) == "" {
			return nil
		}
		return []row{{text: m.Text, role: "inject", agent: e.Agent}}
	case session.TypeAssistantMessage:
		var m session.AssistantMessage
		if e.Decode(&m) != nil {
			return nil
		}
		var out []row
		if t := strings.TrimSpace(m.Message.Text()); t != "" {
			out = append(out, row{text: t, role: "assistant", agent: e.Agent})
		}
		for _, tc := range m.Message.ToolCalls() {
			out = append(out, row{text: tc.Name + " " + truncate(string(tc.Args), 500), role: "tool_call", agent: e.Agent})
		}
		return out
	case session.TypeToolResult:
		var r llm.ToolResult
		if e.Decode(&r) != nil || r.Content == "" {
			return nil
		}
		return []row{{text: truncate(r.Content, s.cfg.ToolResultChars), role: "tool:" + r.Name, agent: e.Agent}}
	}
	return nil
}

func (s *Store) lastSeq(tx *sql.Tx, sessionID string) (int64, bool, error) {
	var n int64
	err := tx.QueryRow(`SELECT last_seq FROM indexed WHERE session = ?`, sessionID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return n, err == nil, err
}

// indexEvent indexes one appended event, catching up first if earlier
// events of the session were missed.
func (s *Store) indexEvent(e session.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	last, known, err := s.lastSeq(tx, e.Session)
	if err != nil {
		return err
	}
	if e.Seq <= last {
		return nil
	}
	evs := []session.Event{e}
	if e.Seq != last+1 || !known && e.Seq != 1 {
		ss, err := s.agents.Sessions().Get(e.Session)
		if err != nil {
			return err
		}
		evs = nil
		for _, x := range ss.Since(last) {
			if x.Seq <= e.Seq {
				evs = append(evs, x)
			}
		}
	}
	if err := s.insert(tx, e.Session, evs); err != nil {
		return err
	}
	return tx.Commit()
}

// insertTri mirrors a docs row into the trigram index.
func insertTri(tx *sql.Tx, res sql.Result, text string) error {
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO docs_tri(rowid, text) VALUES (?, ?)`, id, text)
	return err
}

func (s *Store) insert(tx *sql.Tx, sessionID string, evs []session.Event) error {
	if len(evs) == 0 {
		return nil
	}
	agents := map[string]bool{}
	for _, e := range evs {
		if e.Type == session.TypeCreated || e.Type == "session/meta" {
			var m session.Meta
			if e.Decode(&m) == nil {
				for _, a := range m.Agents {
					agents[a] = true
				}
			}
		}
		if e.Agent != "" {
			agents[e.Agent] = true
		}
		for _, r := range s.rows(e) {
			if r.agent != "" {
				agents[r.agent] = true
			}
			res, err := tx.Exec(`INSERT INTO docs(text, kind, agent, session, seq, role, time) VALUES (?, 'message', ?, ?, ?, ?, ?)`,
				r.text, r.agent, sessionID, e.Seq, r.role, e.Time.Format(time.RFC3339))
			if err != nil {
				return err
			}
			if err := insertTri(tx, res, r.text); err != nil {
				return err
			}
		}
	}
	for a := range agents {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO session_agents(session, agent) VALUES (?, ?)`, sessionID, a); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`INSERT INTO indexed(session, last_seq) VALUES (?, ?) ON CONFLICT(session) DO UPDATE SET last_seq = excluded.last_seq`,
		sessionID, evs[len(evs)-1].Seq)
	return err
}

// IndexSession indexes events of a session not yet in the index.
func (s *Store) IndexSession(sessionID string) error {
	ss, err := s.agents.Sessions().Get(sessionID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	last, _, err := s.lastSeq(tx, sessionID)
	if err != nil {
		return err
	}
	if err := s.insert(tx, sessionID, ss.Since(last)); err != nil {
		return err
	}
	return tx.Commit()
}

// CatchUp incrementally indexes every session and every agent's memory.
func (s *Store) CatchUp(ctx context.Context) error {
	var errs []error
	for _, m := range s.agents.Sessions().List() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.IndexSession(m.ID); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", m.ID, err))
		}
	}
	for _, a := range s.agents.List() {
		if err := s.refreshMemory(a, false); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Reindex drops the index and rebuilds it from the session logs and the
// agents' MEMORY.md files.
func (s *Store) Reindex(ctx context.Context) error {
	s.mu.Lock()
	_, err := s.db.Exec(`INSERT INTO docs_tri(docs_tri) VALUES ('delete-all'); DELETE FROM docs; DELETE FROM indexed; DELETE FROM session_agents;`)
	s.memMtime = map[string]time.Time{}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.CatchUp(ctx)
}

// refreshMemory re-indexes an agent's MEMORY.md when it changed (or always
// when force). MEMORY.md is edited by people too, so it is checked lazily.
func (s *Store) refreshMemory(a *agent.Agent, force bool) error {
	path := filepath.Join(a.Home(), "MEMORY.md")
	fi, err := os.Stat(path)
	var mt time.Time
	if err == nil {
		mt = fi.ModTime()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.memMtime[a.ID()]; ok && !force && prev.Equal(mt) {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO docs_tri(docs_tri, rowid, text) SELECT 'delete', rowid, text FROM docs WHERE kind = 'memory' AND agent = ?`, a.ID()); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM docs WHERE kind = 'memory' AND agent = ?`, a.ID()); err != nil {
		return err
	}
	for i, line := range strings.Split(a.Memory(), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") && !strings.Contains(t, " ") {
			continue
		}
		res, err := tx.Exec(`INSERT INTO docs(text, kind, agent, session, seq, role, time) VALUES (?, 'memory', ?, '', ?, 'memory', '')`,
			t, a.ID(), i+1)
		if err != nil {
			return err
		}
		if err := insertTri(tx, res, t); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.memMtime[a.ID()] = mt
	return nil
}

// ---------------------------------------------------------------- search

// ftsQuery turns free text into a safe FTS5 query: every word becomes a
// quoted term, joined with AND (or OR).
func ftsQuery(q, op string) string {
	words := strings.FieldsFunc(q, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '_' })
	var terms []string
	for _, w := range words {
		terms = append(terms, `"`+strings.ReplaceAll(w, `"`, `""`)+`"`)
	}
	return strings.Join(terms, " "+op+" ")
}

// hasCJK reports whether s contains Han, Hiragana, Katakana or Hangul.
func hasCJK(s string) bool {
	for _, r := range s {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			return true
		}
	}
	return false
}

// trigramQuery turns q into a trigram FTS5 query (quoted substrings joined
// with op), or "" when a term is shorter than three characters (trigrams
// cannot match it; the substring fallback does).
func trigramQuery(q, op string) string {
	words := strings.FieldsFunc(q, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r) && r != '_' && r != '-' || unicode.IsSymbol(r)
	})
	if len(words) == 0 {
		return ""
	}
	terms := make([]string, 0, len(words))
	for _, w := range words {
		if utf8.RuneCountInString(w) < 3 {
			return ""
		}
		terms = append(terms, `"`+strings.ReplaceAll(w, `"`, `""`)+`"`)
	}
	return strings.Join(terms, " "+op+" ")
}

func (s *Store) query(where string, args []any, q string, limit int) ([]Hit, error) {
	if limit <= 0 {
		limit = 10
	}
	limit = min(limit, 50)
	if hasCJK(q) {
		for _, op := range []string{"AND", "OR"} {
			fts := trigramQuery(q, op)
			if fts == "" {
				break
			}
			rows, err := s.db.Query(`SELECT kind, agent, session, seq, role, time, snippet(docs_tri, 0, '«', '»', '…', 16)
				FROM docs_tri JOIN docs ON docs.rowid = docs_tri.rowid WHERE docs_tri MATCH ? AND `+where+` ORDER BY docs_tri.rank LIMIT ?`,
				append(append([]any{fts}, args...), limit)...)
			if err != nil {
				return nil, err
			}
			h, err := scanHits(rows)
			if err != nil {
				return nil, err
			}
			if len(h) > 0 {
				return h, nil
			}
		}
	}
	run := func(fts string) ([]Hit, error) {
		rows, err := s.db.Query(`SELECT kind, agent, session, seq, role, time, snippet(docs, 0, '«', '»', '…', 16)
			FROM docs WHERE docs MATCH ? AND `+where+` ORDER BY rank LIMIT ?`, append(append([]any{fts}, args...), limit)...)
		if err != nil {
			return nil, err
		}
		return scanHits(rows)
	}
	var hits []Hit
	for _, op := range []string{"AND", "OR"} {
		fts := ftsQuery(q, op)
		if fts == "" {
			break
		}
		h, err := run(fts)
		if err != nil {
			return nil, err
		}
		if len(h) > 0 {
			hits = h
			break
		}
	}
	if len(hits) == 0 && strings.TrimSpace(q) != "" {
		// Substring fallback: unicode61 keeps CJK runs as single tokens, so
		// a word inside a CJK sentence is only found this way.
		like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.TrimSpace(q)) + "%"
		rows, err := s.db.Query(`SELECT kind, agent, session, seq, role, time, substr(text, 1, 240)
			FROM docs WHERE text LIKE ? ESCAPE '\' AND `+where+` ORDER BY rowid DESC LIMIT ?`, append(append([]any{like}, args...), limit)...)
		if err != nil {
			return nil, err
		}
		return scanHits(rows)
	}
	return hits, nil
}

func scanHits(rows *sql.Rows) ([]Hit, error) {
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.Kind, &h.Agent, &h.Session, &h.Seq, &h.Role, &h.Time, &h.Snippet); err != nil {
			return nil, err
		}
		h.Snippet = strings.Join(strings.Fields(h.Snippet), " ")
		out = append(out, h)
	}
	return out, rows.Err()
}

// SearchHistory searches messages in sessions agentID took part in
// (optionally one session).
func (s *Store) SearchHistory(agentID, q, sessionID string, limit int) ([]Hit, error) {
	s.syncBrief()
	where := `kind = 'message' AND session IN (SELECT session FROM session_agents WHERE agent = ?)`
	args := []any{agentID}
	if sessionID != "" {
		where += ` AND session = ?`
		args = append(args, sessionID)
	}
	return s.query(where, args, q, limit)
}

// SearchMemory searches agentID's MEMORY.md entries.
func (s *Store) SearchMemory(agentID, q string, limit int) ([]Hit, error) {
	if a, ok := s.agents.Get(agentID); ok {
		if err := s.refreshMemory(a, false); err != nil {
			return nil, err
		}
	}
	return s.query(`kind = 'memory' AND agent = ?`, []any{agentID}, q, limit)
}

// Add appends a dated bullet to agentID's MEMORY.md.
func (s *Store) Add(agentID, text string) (string, error) {
	a, ok := s.agents.Get(agentID)
	if !ok {
		return "", fmt.Errorf("agent %s not found", agentID)
	}
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return "", errors.New("memory text is empty")
	}
	line := fmt.Sprintf("- %s: %s", time.Now().Format("2006-01-02"), text)
	path := filepath.Join(a.Home(), "MEMORY.md")
	s.mu.Lock()
	cur, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		s.mu.Unlock()
		return "", err
	}
	var b strings.Builder
	if len(cur) == 0 {
		b.WriteString("# Memory\n\n")
	} else if cur[len(cur)-1] != '\n' {
		b.WriteString("\n")
	}
	b.WriteString(line + "\n")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err == nil {
		_, err = f.WriteString(b.String())
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	return line, s.refreshMemory(a, true)
}

// ---------------------------------------------------------------- read

// CanRead reports whether agentID took part in the session.
func (s *Store) CanRead(agentID string, ss *session.Session) bool {
	if slices.Contains(ss.Meta().Agents, agentID) {
		return true
	}
	for _, e := range ss.Events() {
		if e.Agent == agentID {
			return true
		}
	}
	return false
}

// Read renders events from..to (inclusive) of a session compactly, capped
// at maxChars (0 = config default).
func (s *Store) Read(agentID, sessionID string, from, to int64, maxChars int) (string, error) {
	ss, err := s.agents.Sessions().Get(sessionID)
	if err != nil {
		return "", err
	}
	if !s.CanRead(agentID, ss) {
		return "", fmt.Errorf("session %s is not one of this agent's conversations", sessionID)
	}
	if maxChars <= 0 {
		maxChars = s.cfg.ReadMaxChars
	}
	maxChars = min(maxChars, 4*s.cfg.ReadMaxChars)
	if from <= 0 {
		from = 1
	}
	if to <= 0 || to < from {
		to = from + 30
	}
	names := s.agents.Names()
	var b strings.Builder
	n := 0
	for _, e := range ss.Since(from - 1) {
		if e.Seq > to {
			break
		}
		line := render(e, names)
		if line == "" {
			continue
		}
		if b.Len()+len(line) > maxChars {
			if n == 0 {
				line = truncate(line, maxChars)
				b.WriteString(line + "\n")
				if e.Seq < to {
					fmt.Fprintf(&b, "…[output limit reached; continue with from=%d]\n", e.Seq+1)
				}
			} else {
				fmt.Fprintf(&b, "…[output limit reached; continue with from=%d]\n", e.Seq)
			}
			return b.String(), nil
		}
		b.WriteString(line + "\n")
		n++
	}
	if n == 0 {
		return fmt.Sprintf("(no messages in seq %d..%d of %s; the session has %d events)", from, to, sessionID, len(ss.Events())), nil
	}
	return b.String(), nil
}

func render(e session.Event, names map[string]string) string {
	who := func(id string) string {
		if n := names[id]; n != "" {
			return n
		}
		return id
	}
	p := fmt.Sprintf("#%d ", e.Seq)
	switch e.Type {
	case session.TypeUserMessage:
		var m session.UserMessage
		if e.Decode(&m) != nil {
			return ""
		}
		from := "user"
		if m.From != "" {
			from = m.From
		}
		return p + "[" + from + "] " + m.Text
	case session.TypeInject:
		var m session.InjectData
		if e.Decode(&m) != nil {
			return ""
		}
		return p + "[injected] " + m.Text
	case session.TypeAssistantMessage:
		var m session.AssistantMessage
		if e.Decode(&m) != nil {
			return ""
		}
		var parts []string
		if t := m.Message.Text(); t != "" {
			parts = append(parts, t)
		}
		for _, tc := range m.Message.ToolCalls() {
			parts = append(parts, fmt.Sprintf("→ %s(%s) [call %s]", tc.Name, truncate(string(tc.Args), 300), tc.ID))
		}
		if len(parts) == 0 {
			return ""
		}
		return p + "[" + who(e.Agent) + "] " + strings.Join(parts, "\n")
	case session.TypeToolResult:
		var r llm.ToolResult
		if e.Decode(&r) != nil {
			return ""
		}
		st := "result"
		if r.IsError {
			st = "error"
		}
		return fmt.Sprintf("%s[%s %s, call %s] %s", p, r.Name, st, r.CallID, truncate(r.Content, 4000))
	case session.TypeCompaction:
		var c session.CompactionData
		if e.Decode(&c) != nil {
			return ""
		}
		return fmt.Sprintf("%s[compaction of seq ≤ %d] %s", p, c.UptoSeq, truncate(c.Summary, 600))
	case session.TypeModelSwitched:
		var m session.ModelSwitchData
		if e.Decode(&m) != nil {
			return ""
		}
		return fmt.Sprintf("%s[model switched %s → %s: %s]", p, m.From, m.To, m.Reason)
	case session.TypeNotice, session.TypeError:
		return p + "[" + e.Type + "] " + truncate(string(e.Data), 300)
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n] + "…"
}
