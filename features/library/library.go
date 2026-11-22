// Package library is the 文档库: one place per team for what is kept for
// the long term (files people upload, images and videos agents generate,
// documents agents save), searchable by every agent of the team.
//
// Layout, under <root>/library/<team>/ (plain files, browsable in Finder):
//
//	inbox/   uploads           media/   generated images and videos
//	docs/    agents' documents pages/   saved web pages and HTML
//
// Files keep their own names (a clash gets " (2)"); identical content is
// stored once per team. <root>/library/index.sqlite holds each item's
// metadata (source, agent, conversation, time, tags, private) and the
// searchable text: the extracted text of PDFs, Word, Excel, PowerPoint and
// text files, OCR for scans when an OCR service is there, and for images
// and videos their name, title and prompt. Two FTS5 indexes share the rows,
// as in features/memory: a word index and a trigram one for CJK queries.
//
// Agents use library_search, library_read and library_save. An item marked
// private is seen only by the agent that saved it. Saving over an item of
// the same name keeps the earlier content as a version.
package library

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	_ "modernc.org/sqlite"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/attachments"
	"github.com/hedeqiang/crewbase/kernel"
)

// ServiceKey is the library service.
const ServiceKey = "library"

// Folders (Item.Folder) and sources (Item.Source).
const (
	FolderInbox = "inbox"
	FolderMedia = "media"
	FolderDocs  = "docs"
	FolderPages = "pages"

	SourceUpload    = "upload"
	SourceGenerated = "generated"
	SourceAgent     = "agent"
	SourcePerson    = "person"
)

// Limits.
const (
	MaxItemBytes = 64 << 20
	maxTextChars = 400_000 // indexed text per item
	maxReadChars = 60_000  // library_read returns at most this much text
)

// Item is one document of the library.
type Item struct {
	ID      int64     `json:"id"`
	Team    string    `json:"team"`
	Folder  string    `json:"folder"`
	Name    string    `json:"name"`
	Title   string    `json:"title,omitempty"`
	Mime    string    `json:"mime"`
	Size    int64     `json:"size"`
	SHA256  string    `json:"sha256"`
	Source  string    `json:"source"`
	Agent   string    `json:"agent,omitempty"`
	Session string    `json:"session,omitempty"`
	Tags    []string  `json:"tags,omitempty"`
	Private bool      `json:"private,omitempty"`
	Note    string    `json:"note,omitempty"` // e.g. the prompt of a generated image
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	// Text: the item's text was extracted (searchable beyond its name).
	Text     bool `json:"text"`
	Versions int  `json:"versions,omitempty"`
	// Summary and Keywords: written by a model when summaries are on (Get only).
	Summary  string   `json:"summary,omitempty"`
	Keywords []string `json:"keywords,omitempty"`
}

// Path is the item's file, relative to its team's folder.
func (it Item) Path() string { return path.Join(it.Folder, it.Name) }

// Hit is a search result.
type Hit struct {
	Item
	Snippet string `json:"snippet,omitempty"`
	// Passage: where in the document the best match is.
	Passage *Passage `json:"passage,omitempty"`
	// BySummary: only its summary or keywords matched (Snippet is the summary).
	BySummary bool `json:"bySummary,omitempty"`
}

// AddRequest files content in the library.
type AddRequest struct {
	Team    string
	Folder  string
	Name    string
	Title   string
	Data    []byte
	Source  string
	Agent   string
	Session string
	Tags    []string
	Private bool
	Note    string
	// Replace: an item of the same name in the folder gets this content
	// (its earlier content kept as a version); otherwise a new name is picked.
	Replace bool
	// distinct: a document of its own even when the team holds the same
	// bytes already (a published file is linked to its own document)
	distinct bool
}

// Errors.
var (
	ErrNotFound = errors.New("no such document in the library")
	ErrEmpty    = errors.New("the document is empty")
	ErrTooLarge = fmt.Errorf("the document is larger than %d MB", MaxItemBytes>>20)
)

// Service is the library.
type Service struct {
	root string // <data root>/library
	db   *sql.DB
	ocr  func() attachments.OCR

	mu      sync.Mutex // serialises writes
	pending chan int64 // items whose text still has to be extracted

	summarize Summarizer    // nil: no summaries
	sumKick   chan struct{} // a summary may be due
}

// Open opens (creating) the library under root.
func Open(root string) (*Service, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "index.sqlite")+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("library index: %w", err)
	}
	s := &Service{root: root, db: db, pending: make(chan int64, 256), sumKick: make(chan struct{}, 1)}
	if err := s.linksInit(); err != nil {
		db.Close()
		return nil, fmt.Errorf("library index: %w", err)
	}
	if err := s.migratePassages(); err != nil {
		db.Close()
		return nil, fmt.Errorf("library index: %w", err)
	}
	return s, nil
}

const schema = `
CREATE TABLE IF NOT EXISTS items (
  id INTEGER PRIMARY KEY,
  team TEXT NOT NULL, folder TEXT NOT NULL, name TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '', mime TEXT NOT NULL DEFAULT '', size INTEGER NOT NULL DEFAULT 0,
  sha256 TEXT NOT NULL DEFAULT '', source TEXT NOT NULL DEFAULT '', agent TEXT NOT NULL DEFAULT '',
  session TEXT NOT NULL DEFAULT '', tags TEXT NOT NULL DEFAULT '', private INTEGER NOT NULL DEFAULT 0,
  note TEXT NOT NULL DEFAULT '', created TEXT NOT NULL, updated TEXT NOT NULL,
  text_state TEXT NOT NULL DEFAULT 'pending',
  UNIQUE(team, folder, name)
);
CREATE INDEX IF NOT EXISTS items_team ON items(team, updated);
CREATE INDEX IF NOT EXISTS items_hash ON items(team, sha256);
CREATE TABLE IF NOT EXISTS versions (
  id INTEGER PRIMARY KEY, item INTEGER NOT NULL, blob TEXT NOT NULL, size INTEGER NOT NULL,
  agent TEXT NOT NULL DEFAULT '', source TEXT NOT NULL DEFAULT '', at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS versions_item ON versions(item, id);
CREATE TABLE IF NOT EXISTS texts (id INTEGER PRIMARY KEY, body TEXT NOT NULL);
CREATE VIRTUAL TABLE IF NOT EXISTS texts_fts USING fts5(body, content='texts', content_rowid='id', tokenize='unicode61 remove_diacritics 2');
CREATE VIRTUAL TABLE IF NOT EXISTS texts_tri USING fts5(body, content='texts', content_rowid='id', tokenize='trigram');
`

// Close closes the index.
func (s *Service) Close() error { return s.db.Close() }

// Root is the library folder.
func (s *Service) Root() string { return s.root }

// TeamDir is a team's folder.
func (s *Service) TeamDir(team string) string { return filepath.Join(s.root, teamDir(team)) }

func teamDir(team string) string {
	t := strings.Trim(nameRe.ReplaceAllString(agent.TeamOf(team), "-"), "-")
	if t == "" {
		t = "default"
	}
	return t
}

var nameRe = regexp.MustCompile(`[^\p{L}\p{N}._-]+`)

// File is an item's file on disk.
func (s *Service) File(it Item) string {
	return filepath.Join(s.TeamDir(it.Team), filepath.FromSlash(it.Folder), it.Name)
}

func validFolder(f string) bool {
	switch f {
	case FolderInbox, FolderMedia, FolderDocs, FolderPages:
		return true
	}
	return false
}

// FolderFor picks the folder for content by its name and source.
func FolderFor(name, mime, source string) string {
	switch {
	case source == SourceUpload:
		return FolderInbox
	case strings.HasPrefix(mime, "image/"), strings.HasPrefix(mime, "video/"), strings.HasPrefix(mime, "audio/"):
		return FolderMedia
	case strings.HasPrefix(mime, "text/html"):
		return FolderPages
	}
	return FolderDocs
}

// safeName keeps a file name readable and harmless.
func safeName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == 0 || unicode.IsControl(r) {
			return '-'
		}
		return r
	}, strings.TrimSpace(name))
	name = strings.TrimLeft(name, ".")
	if name == "" {
		name = "document"
	}
	if r := []rune(name); len(r) > 120 {
		ext := path.Ext(name)
		name = string(r[:120-utf8.RuneCountInString(ext)]) + ext
	}
	return name
}

// Add files data in the library and returns its item. Identical content
// already in the team's library is returned as is (with any new tags).
func (s *Service) Add(ctx context.Context, r AddRequest) (Item, error) {
	if len(r.Data) == 0 {
		return Item{}, ErrEmpty
	}
	if len(r.Data) > MaxItemBytes {
		return Item{}, ErrTooLarge
	}
	r.Team = agent.TeamOf(r.Team)
	r.Name = safeName(r.Name)
	mime := attachments.MimeOf(r.Name, r.Data)
	if r.Folder == "" {
		r.Folder = FolderFor(r.Name, mime, r.Source)
	}
	if !validFolder(r.Folder) {
		return Item{}, fmt.Errorf("unknown library folder %q", r.Folder)
	}
	sum := sha256.Sum256(r.Data)
	hash := hex.EncodeToString(sum[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()

	if !r.Replace && !r.distinct {
		// the same file again: one item (a private one stays its agent's)
		if it, err := s.byHash(r.Team, hash); err == nil && (!it.Private || it.Agent == r.Agent) {
			if len(r.Tags) > 0 {
				it.Tags = mergeTags(it.Tags, r.Tags)
				_, _ = s.db.Exec(`UPDATE items SET tags = ? WHERE id = ?`, strings.Join(it.Tags, ","), it.ID)
				s.queue(it.ID)
			}
			return it, nil
		}
	}

	dir := filepath.Join(s.TeamDir(r.Team), r.Folder)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Item{}, err
	}
	if r.Replace {
		// only an item the saver may see, kept as private (or not) as
		// asked: another agent's private document is not overwritten (nor
		// taken over), and a private save does not land in a shared one;
		// those get a free name instead
		if it, err := s.replaceable(r); err == nil {
			if it.SHA256 == hash {
				return it, nil
			}
			if err := s.keepVersion(it); err != nil {
				return Item{}, err
			}
			if err := writeFileAtomic(s.File(it), r.Data); err != nil {
				return Item{}, err
			}
			it.Mime, it.Size, it.SHA256, it.Updated = mime, int64(len(r.Data)), hash, now
			if r.Title != "" {
				it.Title = r.Title
			}
			it.Tags = mergeTags(it.Tags, r.Tags)
			if _, err := s.db.Exec(`UPDATE items SET mime=?, size=?, sha256=?, updated=?, title=?, tags=?, agent=?, source=?, text_state='pending' WHERE id=?`,
				it.Mime, it.Size, it.SHA256, ts(now), it.Title, strings.Join(it.Tags, ","), r.Agent, r.Source, it.ID); err != nil {
				return Item{}, err
			}
			s.queue(it.ID)
			return s.get(it.ID)
		}
	}
	// a free name: "report.pdf", "report (2).pdf", …
	name := r.Name
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 2; ; i++ {
		var n int
		if err := s.db.QueryRow(`SELECT count(*) FROM items WHERE team=? AND folder=? AND name=?`, r.Team, r.Folder, name).Scan(&n); err != nil {
			return Item{}, err
		}
		if _, err := os.Stat(filepath.Join(dir, name)); n == 0 && errors.Is(err, os.ErrNotExist) {
			break
		}
		if i > 1000 {
			return Item{}, errors.New("too many documents with this name")
		}
		name = fmt.Sprintf("%s (%d)%s", stem, i, ext)
	}
	if err := writeFileAtomic(filepath.Join(dir, name), r.Data); err != nil {
		return Item{}, err
	}
	res, err := s.db.Exec(`INSERT INTO items(team, folder, name, title, mime, size, sha256, source, agent, session, tags, private, note, created, updated)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.Team, r.Folder, name, strings.TrimSpace(r.Title), mime, len(r.Data), hash, r.Source, r.Agent, r.Session,
		strings.Join(mergeTags(nil, r.Tags), ","), boolInt(r.Private), r.Note, ts(now), ts(now))
	if err != nil {
		_ = os.Remove(filepath.Join(dir, name))
		return Item{}, err
	}
	id, _ := res.LastInsertId()
	s.queue(id)
	return s.get(id)
}

// keepVersion copies an item's current content into the version store.
func (s *Service) keepVersion(it Item) error {
	b, err := os.ReadFile(s.File(it))
	if err != nil {
		return nil // nothing to keep
	}
	sum := sha256.Sum256(b)
	blob := hex.EncodeToString(sum[:])
	vd := filepath.Join(s.root, ".versions")
	if err := os.MkdirAll(vd, 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(vd, blob)); errors.Is(err, os.ErrNotExist) {
		if err := writeFileAtomic(filepath.Join(vd, blob), b); err != nil {
			return err
		}
	}
	_, err = s.db.Exec(`INSERT INTO versions(item, blob, size, agent, source, at) VALUES (?,?,?,?,?,?)`, it.ID, blob, len(b), it.Agent, it.Source, ts(it.Updated))
	return err
}

func (s *Service) queue(id int64) {
	select {
	case s.pending <- id:
	default: // the worker catches up from text_state = 'pending'
	}
}

// ---------------------------------------------------------------- reading

const cols = `id, team, folder, name, title, mime, size, sha256, source, agent, session, tags, private, note, created, updated, text_state,
  (SELECT count(*) FROM versions v WHERE v.item = items.id)`

// icols is cols for the items table under alias i (queries that join).
const icols = `i.id, i.team, i.folder, i.name, i.title, i.mime, i.size, i.sha256, i.source, i.agent, i.session, i.tags, i.private, i.note,
  i.created, i.updated, i.text_state, (SELECT count(*) FROM versions v WHERE v.item = i.id)`

func scanItem(sc interface{ Scan(...any) error }) (Item, error) {
	var it Item
	var tags, created, updated, state string
	var private int
	if err := sc.Scan(&it.ID, &it.Team, &it.Folder, &it.Name, &it.Title, &it.Mime, &it.Size, &it.SHA256, &it.Source, &it.Agent, &it.Session,
		&tags, &private, &it.Note, &created, &updated, &state, &it.Versions); err != nil {
		return Item{}, err
	}
	it.Tags = splitTags(tags)
	it.Private = private != 0
	it.Created, _ = time.Parse(time.RFC3339Nano, created)
	it.Updated, _ = time.Parse(time.RFC3339Nano, updated)
	it.Text = state == "ok"
	return it, nil
}

func (s *Service) get(id int64) (Item, error) {
	it, err := scanItem(s.db.QueryRow(`SELECT `+cols+` FROM items WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Item{}, ErrNotFound
	}
	return it, err
}

func (s *Service) byHash(team, hash string) (Item, error) {
	it, err := scanItem(s.db.QueryRow(`SELECT `+cols+` FROM items WHERE team = ? AND sha256 = ? ORDER BY id LIMIT 1`, team, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return Item{}, ErrNotFound
	}
	return it, err
}

// replaceable is the item a replacing save of r updates: the one of that
// name when the saver may see it and it is kept private (or not) as asked;
// else the saver's own copy that an earlier save got under a free name
// ("plan (2).md"), so saving again updates it instead of adding another.
func (s *Service) replaceable(r AddRequest) (Item, error) {
	v := Viewer{Team: r.Team, Agent: r.Agent}
	if it, err := s.byName(r.Team, r.Folder, r.Name); err == nil && v.sees(it) && it.Private == r.Private {
		return it, nil
	}
	ext := path.Ext(r.Name)
	stem := strings.TrimSuffix(r.Name, ext)
	rows, err := s.db.Query(`SELECT id, name FROM items WHERE team=? AND folder=? AND agent=? AND private=? AND substr(name, 1, ?) = ? ORDER BY updated DESC`,
		agent.TeamOf(r.Team), r.Folder, r.Agent, boolInt(r.Private), utf8.RuneCountInString(stem)+2, stem+" (")
	if err != nil {
		return Item{}, err
	}
	var id int64 = -1
	for rows.Next() {
		var x int64
		var name string
		if rows.Scan(&x, &name) != nil {
			continue
		}
		if mid, ok := strings.CutSuffix(strings.TrimPrefix(name, stem+" ("), ")"+ext); ok && mid != "" && strings.Trim(mid, "0123456789") == "" {
			id = x
			break
		}
	}
	rows.Close()
	if id < 0 {
		return Item{}, ErrNotFound
	}
	return s.get(id)
}

func (s *Service) byName(team, folder, name string) (Item, error) {
	it, err := scanItem(s.db.QueryRow(`SELECT `+cols+` FROM items WHERE team = ? AND folder = ? AND name = ?`, team, folder, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Item{}, ErrNotFound
	}
	return it, err
}

// Viewer is who is looking: an agent of a team, or a person ("" agent),
// who sees the team's private items too.
type Viewer struct {
	Team  string
	Agent string
}

func (v Viewer) sees(it Item) bool {
	return it.Team == agent.TeamOf(v.Team) && (!it.Private || v.Agent == "" || it.Agent == v.Agent)
}

// Get returns item id as v sees it.
func (s *Service) Get(v Viewer, id int64) (Item, error) {
	it, err := s.get(id)
	if err != nil {
		return Item{}, err
	}
	if !v.sees(it) {
		return Item{}, ErrNotFound
	}
	it.Summary, it.Keywords = s.summaryOf(id)
	return it, nil
}

// ListOptions narrows List.
type ListOptions struct {
	Folder string
	Source string
	Tag    string
	Limit  int
	Offset int
}

// List lists v's items, newest first.
func (s *Service) List(v Viewer, o ListOptions) ([]Item, error) {
	where, args := s.scope(v, "")
	if o.Folder != "" {
		where += " AND folder = ?"
		args = append(args, o.Folder)
	}
	if o.Source != "" {
		where += " AND source = ?"
		args = append(args, o.Source)
	}
	if o.Tag != "" {
		where += " AND (',' || tags || ',') LIKE ?"
		args = append(args, "%,"+o.Tag+",%")
	}
	limit := o.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.Query(`SELECT `+cols+` FROM items WHERE `+where+` ORDER BY updated DESC, id DESC LIMIT ? OFFSET ?`, append(args, limit, max(o.Offset, 0))...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// scope is the WHERE clause limiting items (alias: "" or "i.") to what v
// may see.
func (s *Service) scope(v Viewer, alias string) (string, []any) {
	where := alias + "team = ?"
	args := []any{agent.TeamOf(v.Team)}
	if v.Agent != "" {
		where += " AND (" + alias + "private = 0 OR " + alias + "agent = ?)"
		args = append(args, v.Agent)
	}
	return where, args
}

// Search finds v's items by their names, titles, tags and text.
func (s *Service) Search(ctx context.Context, v Viewer, q string, limit int) ([]Hit, error) {
	s.drain(ctx, 3*time.Second) // read-your-writes for a just-saved item
	q = strings.TrimSpace(q)
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	where, args := s.scope(v, "i.")
	if q == "" {
		items, err := s.List(v, ListOptions{Limit: limit})
		out := make([]Hit, len(items))
		for i, it := range items {
			out[i] = Hit{Item: it}
		}
		return out, err
	}
	// each hit's best passage: a focused snippet, its place, where to read
	withPassages := func(hits []Hit, table, match, like string) []Hit {
		for i := range hits {
			if p, snip, ok := s.bestPassage(hits[i].ID, table, match, like); ok {
				hits[i].Passage = &p
				if strings.TrimSpace(snip) != "" {
					hits[i].Snippet = snip
				}
			} else if sum, _ := s.summaryOf(hits[i].ID); sum != "" && !strings.Contains(strings.ToLower(hits[i].Name+" "+hits[i].Title), strings.ToLower(strings.Trim(match, `"*`))) {
				// matched by its summary or keywords: show the summary, not the keyword list
				hits[i].Snippet, hits[i].BySummary = sum, true
			}
		}
		return hits
	}
	run := func(table, match string) ([]Hit, error) {
		rows, err := s.db.Query(`SELECT `+icols+`, snippet(`+table+`, 0, '«', '»', '…', 18)
			FROM `+table+` JOIN items i ON i.id = `+table+`.rowid WHERE `+table+` MATCH ? AND `+where+` ORDER BY `+table+`.rank LIMIT ?`,
			append(append([]any{match}, args...), limit)...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []Hit
		for rows.Next() {
			var h Hit
			var tags, created, updated, state string
			var private int
			if err := rows.Scan(&h.ID, &h.Team, &h.Folder, &h.Name, &h.Title, &h.Mime, &h.Size, &h.SHA256, &h.Source, &h.Agent, &h.Session,
				&tags, &private, &h.Note, &created, &updated, &state, &h.Versions, &h.Snippet); err != nil {
				return nil, err
			}
			h.Tags = splitTags(tags)
			h.Private = private != 0
			h.Created, _ = time.Parse(time.RFC3339Nano, created)
			h.Updated, _ = time.Parse(time.RFC3339Nano, updated)
			h.Text = state == "ok"
			out = append(out, h)
		}
		return out, rows.Err()
	}
	for _, op := range []string{"AND", "OR"} {
		if hasCJK(q) {
			if m := trigramQuery(q, op); m != "" {
				if h, err := run("texts_tri", m); err != nil || len(h) > 0 {
					return withPassages(h, "passages_tri", m, ""), err
				}
			}
		}
		if m := ftsQuery(q, op); m != "" {
			if h, err := run("texts_fts", m); err != nil || len(h) > 0 {
				return withPassages(h, "passages_fts", m, ""), err
			}
		}
	}
	// short CJK words: a substring match on the text
	like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(q) + "%"
	rows, err := s.db.Query(`SELECT `+icols+` FROM texts t JOIN items i ON i.id = t.id WHERE t.body LIKE ? ESCAPE '\' AND `+where+` ORDER BY i.updated DESC LIMIT ?`,
		append(append([]any{like}, args...), limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, Hit{Item: it})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return withPassages(out, "", "", like), nil
}

// Text is an item's extracted text ("" when it has none).
func (s *Service) Text(id int64) string {
	var body string
	_ = s.db.QueryRow(`SELECT body FROM texts WHERE id = ?`, id).Scan(&body)
	return body
}

// Body is the text extracted from item id's file, without the searchable
// head ("" when none could be read).
func (s *Service) Body(id int64) string {
	t := s.Text(id)
	if i := strings.Index(t, "\n\n"); i >= 0 {
		return strings.TrimSpace(t[i+2:])
	}
	return ""
}

// Versions lists an item's earlier contents, newest first.
func (s *Service) Versions(v Viewer, id int64) ([]Version, error) {
	if _, err := s.Get(v, id); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id, size, agent, source, at FROM versions WHERE item = ? ORDER BY id DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Version{}
	for rows.Next() {
		var vv Version
		var at string
		if err := rows.Scan(&vv.ID, &vv.Size, &vv.Agent, &vv.Source, &at); err != nil {
			return nil, err
		}
		vv.At, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, vv)
	}
	return out, rows.Err()
}

// Version is an earlier content of an item.
type Version struct {
	ID     int64     `json:"id"`
	Size   int64     `json:"size"`
	Agent  string    `json:"agent,omitempty"`
	Source string    `json:"source"`
	At     time.Time `json:"at"`
}

// VersionData is the content of version vid of item id.
func (s *Service) VersionData(v Viewer, id, vid int64) ([]byte, error) {
	if _, err := s.Get(v, id); err != nil {
		return nil, err
	}
	var blob string
	if err := s.db.QueryRow(`SELECT blob FROM versions WHERE id = ? AND item = ?`, vid, id).Scan(&blob); err != nil {
		return nil, ErrNotFound
	}
	return os.ReadFile(filepath.Join(s.root, ".versions", blob))
}

// Restore puts version vid back as the item's content (the current
// content becomes a version itself).
func (s *Service) Restore(ctx context.Context, v Viewer, id, vid int64, by string) (Item, error) {
	it, err := s.Get(v, id)
	if err != nil {
		return Item{}, err
	}
	b, err := s.VersionData(v, id, vid)
	if err != nil {
		return Item{}, err
	}
	return s.Add(ctx, AddRequest{Team: it.Team, Folder: it.Folder, Name: it.Name, Data: b, Source: SourcePerson, Agent: by, Replace: true})
}

// Delete removes an item, its file and its versions.
func (s *Service) Delete(v Viewer, id int64) error {
	it, err := s.Get(v, id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(`DELETE FROM items WHERE id = ?; DELETE FROM versions WHERE item = ?; DELETE FROM summaries WHERE id = ?; DELETE FROM links WHERE item = ?`, id, id, id, id); err != nil {
		return err
	}
	s.unindex(id)
	return removeFile(s.File(it))
}

// Update changes an item's title, tags or privacy.
func (s *Service) Update(v Viewer, id int64, title *string, tags *[]string, private *bool) (Item, error) {
	it, err := s.Get(v, id)
	if err != nil {
		return Item{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if title != nil {
		it.Title = strings.TrimSpace(*title)
	}
	if tags != nil {
		it.Tags = mergeTags(nil, *tags)
	}
	if private != nil {
		it.Private = *private
	}
	if _, err := s.db.Exec(`UPDATE items SET title=?, tags=?, private=? WHERE id=?`, it.Title, strings.Join(it.Tags, ","), boolInt(it.Private), id); err != nil {
		return Item{}, err
	}
	s.queue(id)
	return s.get(id)
}

// ---------------------------------------------------------------- text

// worker extracts the text of pending items until ctx ends.
func (s *Service) worker(ctx context.Context) {
	s.catchUp(ctx)
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-s.pending:
			s.index(ctx, id)
		case <-tick.C:
			s.catchUp(ctx) // items queued while the channel was full
		}
	}
}

func (s *Service) catchUp(ctx context.Context) {
	rows, err := s.db.Query(`SELECT id FROM items WHERE text_state = 'pending' LIMIT 200`)
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		s.index(ctx, id)
	}
}

// drain waits up to d for the pending queue to empty.
func (s *Service) drain(ctx context.Context, d time.Duration) {
	end := time.Now().Add(d)
	for time.Now().Before(end) && ctx.Err() == nil {
		var n int
		_ = s.db.QueryRow(`SELECT count(*) FROM items WHERE text_state = 'pending'`).Scan(&n)
		if n == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// index extracts item id's text and writes its search row.
func (s *Service) index(ctx context.Context, id int64) {
	it, err := s.get(id)
	if err != nil {
		return
	}
	data, _ := os.ReadFile(s.File(it))
	body, ok := s.extract(ctx, it, data)
	// the head (name, title, note, tags, summary: one line each, never
	// blank) is searchable too; Body strips it again
	full := s.headOf(it) + "\n\n" + strings.TrimSpace(body)
	if r := []rune(full); len(r) > maxTextChars {
		full = string(r[:maxTextChars])
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unindex(id)
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO texts(id, body) VALUES (?, ?)`, id, full); err != nil {
		return
	}
	if _, err := tx.Exec(`INSERT INTO texts_fts(rowid, body) VALUES (?, ?)`, id, full); err != nil {
		return
	}
	if _, err := tx.Exec(`INSERT INTO texts_tri(rowid, body) VALUES (?, ?)`, id, full); err != nil {
		return
	}
	// the passages of the text as kept (what Body returns)
	kept := ""
	if i := strings.Index(full, "\n\n"); i >= 0 {
		kept = strings.TrimSpace(full[i+2:])
	}
	if err := indexPassages(tx, id, kept); err != nil {
		return
	}
	state := "none"
	if ok {
		state = "ok"
	}
	if _, err := tx.Exec(`UPDATE items SET text_state = ? WHERE id = ?`, state, id); err != nil {
		return
	}
	if tx.Commit() == nil && ok {
		s.kickSummaries()
	}
}

// unindex removes item id's search rows (callers hold s.mu).
func (s *Service) unindex(id int64) {
	var body string
	if s.db.QueryRow(`SELECT body FROM texts WHERE id = ?`, id).Scan(&body) != nil {
		return
	}
	_, _ = s.db.Exec(`INSERT INTO texts_fts(texts_fts, rowid, body) VALUES('delete', ?, ?)`, id, body)
	_, _ = s.db.Exec(`INSERT INTO texts_tri(texts_tri, rowid, body) VALUES('delete', ?, ?)`, id, body)
	_, _ = s.db.Exec(`DELETE FROM texts WHERE id = ?`, id)
	s.unindexPassages(id)
}

// extract is a file's text: documents through attachments.Extract, scans
// and images through OCR when it is there.
func (s *Service) extract(ctx context.Context, it Item, data []byte) (string, bool) {
	if len(data) == 0 {
		return "", false
	}
	if strings.HasPrefix(it.Mime, "video/") || strings.HasPrefix(it.Mime, "audio/") {
		return "", false
	}
	text, ok, _ := attachments.Extract(it.Name, it.Mime, data)
	if ok && it.Mime == "text/html" {
		text = htmlText(text)
	}
	text = strings.TrimSpace(text)
	if attachments.Garbled(text) {
		text, ok = "", false // OCR below, or nothing rather than junk
	}
	if (!ok || text == "") && (strings.HasPrefix(it.Mime, "image/") || it.Mime == "application/pdf") && s.ocr != nil {
		if o := s.ocr(); o != nil {
			octx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			if t, err := o.Recognize(octx, it.Name, it.Mime, data); err == nil && strings.TrimSpace(t) != "" {
				return strings.TrimSpace(t), true
			}
		}
	}
	return text, ok && text != ""
}

// ---------------------------------------------------------------- helpers

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func mergeTags(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range append(append([]string(nil), a...), b...) {
		t = strings.TrimSpace(strings.ReplaceAll(t, ",", " "))
		if t != "" && !seen[strings.ToLower(t)] {
			seen[strings.ToLower(t)] = true
			out = append(out, t)
		}
	}
	return out
}

func writeFileAtomic(p string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(p), ".lib-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	_ = os.Chmod(tmp, 0o644)
	return os.Rename(tmp, p)
}

func removeFile(p string) error {
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ftsQuery quotes every word and joins them with op.
func ftsQuery(q, op string) string {
	words := strings.FieldsFunc(q, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '_' })
	terms := make([]string, 0, len(words))
	for _, w := range words {
		terms = append(terms, `"`+strings.ReplaceAll(w, `"`, `""`)+`"`)
	}
	return strings.Join(terms, " "+op+" ")
}

func hasCJK(s string) bool {
	for _, r := range s {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			return true
		}
	}
	return false
}

// trigramQuery is "" when a term is shorter than three characters.
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

// ---------------------------------------------------------------- plugin

// Config of the plugin.
type Config struct {
	// Root is the data root; the library lives in <root>/library.
	Root string `json:"root"`
}

// Plugin provides the library, files uploads and generated media in it,
// and gives agents library_search, library_read and library_save.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/library",
	Inject:      []string{agent.ServiceKey, tools.ServiceKey},
	Description: "文档库: uploads, generated media and agents' documents, per team, searchable by every agent.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		root := cfg.Root
		if root == "" {
			if root, err = os.MkdirTemp("", "crewbase-library-"); err != nil {
				return err
			}
		}
		s, err := Open(filepath.Join(root, "library"))
		if err != nil {
			return err
		}
		c.Defer(func() { _ = s.Close() })
		s.ocr = func() attachments.OCR {
			o, _ := kernel.Lookup[attachments.OCR](c, attachments.OCRKey)
			return o
		}
		c.Go(s.worker)
		s.summarize = summarizer(c)
		c.Go(s.summaryWorker)
		agents := kernel.Use[*agent.Registry](c, agent.ServiceKey)
		teamOf := func(id string) string {
			if a, ok := agents.Get(id); ok {
				return a.Spec().TeamID()
			}
			return agent.DefaultTeam
		}
		file := func(what string, r AddRequest) {
			if _, err := s.Add(context.Background(), r); err != nil {
				c.Log().Warn("library: could not file "+what, "name", r.Name, "err", err)
			}
		}
		attachments.Uploaded.Listen(c, func(u attachments.UploadedFile) {
			data, err := os.ReadFile(filepath.Join(u.Workspace, filepath.FromSlash(u.Attachment.Path)))
			if err != nil {
				return
			}
			file("an upload", AddRequest{Team: u.Team, Name: u.Attachment.Name, Data: data, Source: SourceUpload, Agent: u.Agent})
		})
		registerMediaListener(c, s, teamOf, file)
		registerTools(c, s, agents)
		return c.Provide(ServiceKey, s)
	},
})

// Stats counts what v sees, for the library page's filters.
type Stats struct {
	Total   int            `json:"total"`
	Folders map[string]int `json:"folders"`
	Sources map[string]int `json:"sources"`
	Tags    []TagCount     `json:"tags"`
}

// TagCount is a tag and how many items carry it.
type TagCount struct {
	Tag string `json:"tag"`
	N   int    `json:"n"`
}

// Stats counts v's items by folder, source and tag.
func (s *Service) Stats(v Viewer) (Stats, error) {
	where, args := s.scope(v, "")
	rows, err := s.db.Query(`SELECT folder, source, tags FROM items WHERE `+where, args...)
	if err != nil {
		return Stats{}, err
	}
	defer rows.Close()
	st := Stats{Folders: map[string]int{}, Sources: map[string]int{}, Tags: []TagCount{}}
	tags := map[string]int{}
	for rows.Next() {
		var folder, source, tg string
		if err := rows.Scan(&folder, &source, &tg); err != nil {
			return Stats{}, err
		}
		st.Total++
		st.Folders[folder]++
		st.Sources[source]++
		for _, t := range splitTags(tg) {
			tags[t]++
		}
	}
	for t, n := range tags {
		st.Tags = append(st.Tags, TagCount{t, n})
	}
	slices.SortFunc(st.Tags, func(a, b TagCount) int {
		if a.N != b.N {
			return b.N - a.N
		}
		return strings.Compare(a.Tag, b.Tag)
	})
	return st, rows.Err()
}

// splitTags reads the stored tags column.
func splitTags(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

var (
	htmlDropRe  = regexp.MustCompile(`(?is)<(script|style|noscript|svg|template)\b.*?</(script|style|noscript|svg|template)>|<!--.*?-->`)
	htmlBreakRe = regexp.MustCompile(`(?i)<(br|/p|/div|/h[1-6]|/li|/tr|/table|/section|/article|/header|/footer)\b[^>]*>`)
	htmlTagRe   = regexp.MustCompile(`(?s)<[^>]*>`)
	blankRe     = regexp.MustCompile(`[ \t]*\n[ \t\n]*`)
	spacesRe    = regexp.MustCompile(`[ \t]{2,}`)
)

// htmlText is the visible text of an HTML page (for search and reading).
func htmlText(s string) string {
	s = htmlDropRe.ReplaceAllString(s, " ")
	s = htmlBreakRe.ReplaceAllString(s, "\n")
	s = htmlTagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = spacesRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(blankRe.ReplaceAllString(s, "\n"))
}
