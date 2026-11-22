package library

// Links between a workspace file and the library document it was
// published as. Publishing is explicit (the person, or an agent's
// library_save of a file); afterwards each side can move on and the other
// says so: the workspace file changed since → 同步 makes it a new version
// of the document; the document changed since (edited, restored, saved by
// someone else) → 取回 writes it back to the workspace. Nothing is copied
// on its own, so neither side is overwritten without someone asking.
//
// A link remembers where the file came from: the agent, its workspace
// path, and the conversation message it was published from, so the
// document can be traced back to the work that made it.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const linksSchema = `
CREATE TABLE IF NOT EXISTS links (
  item INTEGER PRIMARY KEY,
  agent TEXT NOT NULL, path TEXT NOT NULL,
  session TEXT NOT NULL DEFAULT '', seq INTEGER NOT NULL DEFAULT 0,
  file_sha TEXT NOT NULL, doc_sha TEXT NOT NULL,
  at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS links_file ON links(agent, path);
`

// Link is a document's tie to the workspace file it was published from.
type Link struct {
	Item    int64     `json:"item"`
	Agent   string    `json:"agent"`
	Path    string    `json:"path"`              // relative to the agent's workspace
	Session string    `json:"session,omitempty"` // the conversation it was published from
	Seq     int64     `json:"seq,omitempty"`     // the message there (0 = the conversation)
	At      time.Time `json:"at"`                // when the two last matched
	// what both sides held then (sha256 of the bytes)
	FileSHA string `json:"-"`
	DocSHA  string `json:"-"`
}

// Sync states of a link.
const (
	SyncSame      = "same"      // both still hold what was published
	SyncFileAhead = "file"      // the workspace file changed since: 同步 to the library
	SyncDocAhead  = "doc"       // the document changed since: 取回 to the workspace
	SyncDiverged  = "both"      // both changed: the person picks a side
	SyncFileGone  = "file-gone" // the workspace file is no longer there
)

// LinkView is a link with how its two sides stand now.
type LinkView struct {
	Link
	State string `json:"state"`
}

// ErrNoLink: the document was not published from a workspace file.
var ErrNoLink = errors.New("the document is not linked to a workspace file")

func (s *Service) linksInit() error {
	_, err := s.db.Exec(linksSchema)
	return err
}

func shaOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// wsFile resolves rel inside ws (no "..", no link out of it).
func wsFile(ws, rel string) (string, error) {
	rel = filepath.Clean(filepath.FromSlash(strings.TrimSpace(rel)))
	if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q is not a file in the workspace", rel)
	}
	root, err := filepath.EvalSymlinks(ws)
	if err != nil {
		return "", err
	}
	p := filepath.Join(root, rel)
	real := p
	if r, err := filepath.EvalSymlinks(p); err == nil {
		real = r
	}
	if real != root && !strings.HasPrefix(real, root+string(filepath.Separator)) {
		return "", fmt.Errorf("%q is outside the workspace", rel)
	}
	return real, nil
}

// SetLink records that item it was published from rel in agent's
// workspace (both sides hold data now).
func (s *Service) SetLink(it Item, agent, rel, session string, seq int64, data []byte) error {
	sha := shaOf(data)
	_, err := s.db.Exec(`INSERT INTO links(item, agent, path, session, seq, file_sha, doc_sha, at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(item) DO UPDATE SET agent=excluded.agent, path=excluded.path, session=excluded.session, seq=excluded.seq,
		file_sha=excluded.file_sha, doc_sha=excluded.doc_sha, at=excluded.at`,
		it.ID, agent, filepath.ToSlash(filepath.Clean(rel)), session, seq, sha, it.SHA256, ts(time.Now().UTC()))
	return err
}

func (s *Service) link(item int64) (Link, error) {
	var l Link
	var at string
	err := s.db.QueryRow(`SELECT item, agent, path, session, seq, file_sha, doc_sha, at FROM links WHERE item = ?`, item).
		Scan(&l.Item, &l.Agent, &l.Path, &l.Session, &l.Seq, &l.FileSHA, &l.DocSHA, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return Link{}, ErrNoLink
	}
	if err != nil {
		return Link{}, err
	}
	l.At, _ = time.Parse(time.RFC3339Nano, at)
	return l, nil
}

// LinkOf is item id's link and how its sides stand, as v sees it; ws is
// the linked agent's workspace (from the caller, which knows the agents).
func (s *Service) LinkOf(v Viewer, id int64, ws func(agent string) (string, bool)) (LinkView, error) {
	it, err := s.Get(v, id)
	if err != nil {
		return LinkView{}, err
	}
	l, err := s.link(id)
	if err != nil {
		return LinkView{}, err
	}
	return LinkView{Link: l, State: s.state(it, l, ws)}, nil
}

func (s *Service) state(it Item, l Link, ws func(agent string) (string, bool)) string {
	dir, ok := ws(l.Agent)
	if !ok {
		return SyncFileGone
	}
	p, err := wsFile(dir, l.Path)
	if err != nil {
		return SyncFileGone
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return SyncFileGone
	}
	file, doc := shaOf(b) != l.FileSHA, it.SHA256 != l.DocSHA
	switch {
	case file && doc:
		if shaOf(b) == it.SHA256 {
			return SyncSame // both moved to the same content
		}
		return SyncDiverged
	case file:
		return SyncFileAhead
	case doc:
		return SyncDocAhead
	}
	return SyncSame
}

// Push makes the linked workspace file a new version of the document
// (同步到文档库). The document's earlier content stays a version.
func (s *Service) Push(v Viewer, id int64, ws func(agent string) (string, bool), by string) (Item, error) {
	it, err := s.Get(v, id)
	if err != nil {
		return Item{}, err
	}
	l, err := s.link(id)
	if err != nil {
		return Item{}, err
	}
	dir, ok := ws(l.Agent)
	if !ok {
		return Item{}, fmt.Errorf("the agent %s is gone", l.Agent)
	}
	p, err := wsFile(dir, l.Path)
	if err != nil {
		return Item{}, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return Item{}, fmt.Errorf("%s: %w", l.Path, err)
	}
	if len(data) > MaxItemBytes {
		return Item{}, ErrTooLarge
	}
	if shaOf(data) != it.SHA256 {
		if it, err = s.replaceContent(it, data, by); err != nil {
			return Item{}, err
		}
	}
	_, err = s.db.Exec(`UPDATE links SET file_sha=?, doc_sha=?, at=? WHERE item=?`, shaOf(data), it.SHA256, ts(time.Now().UTC()), id)
	return it, err
}

// Pull writes the document back to the linked workspace file (取回到工作区).
func (s *Service) Pull(v Viewer, id int64, ws func(agent string) (string, bool)) (Link, error) {
	it, err := s.Get(v, id)
	if err != nil {
		return Link{}, err
	}
	l, err := s.link(id)
	if err != nil {
		return Link{}, err
	}
	dir, ok := ws(l.Agent)
	if !ok {
		return Link{}, fmt.Errorf("the agent %s is gone", l.Agent)
	}
	data, err := os.ReadFile(s.File(it))
	if err != nil {
		return Link{}, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return Link{}, err
	}
	defer root.Close()
	rel := filepath.FromSlash(l.Path)
	if d := filepath.Dir(rel); d != "." {
		if err := root.MkdirAll(d, 0o755); err != nil {
			return Link{}, err
		}
	}
	if err := root.WriteFile(rel, data, 0o644); err != nil {
		return Link{}, err
	}
	sha := shaOf(data)
	l.FileSHA, l.DocSHA, l.At = sha, it.SHA256, time.Now().UTC()
	_, err = s.db.Exec(`UPDATE links SET file_sha=?, doc_sha=?, at=? WHERE item=?`, sha, it.SHA256, ts(l.At), id)
	return l, err
}

// Unlink forgets the tie (the document and the file both stay).
func (s *Service) Unlink(v Viewer, id int64) error {
	if _, err := s.Get(v, id); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM links WHERE item = ?`, id)
	return err
}

// LinksOfFile are the documents published from rel in agent's workspace,
// that v may see (the 文件 panel shows them on the file).
func (s *Service) LinksOfFile(v Viewer, agent, rel string, ws func(agent string) (string, bool)) ([]LinkView, error) {
	rows, err := s.db.Query(`SELECT item FROM links WHERE agent = ? AND path = ?`, agent, filepath.ToSlash(filepath.Clean(rel)))
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	var out []LinkView
	for _, id := range ids {
		it, err := s.Get(v, id)
		if err != nil {
			continue
		}
		l, err := s.link(id)
		if err != nil {
			continue
		}
		out = append(out, LinkView{Link: l, State: s.state(it, l, ws)})
	}
	return out, nil
}

// replaceContent gives it new content, keeping the current one as a version.
func (s *Service) replaceContent(it Item, data []byte, by string) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.keepVersion(it); err != nil {
		return Item{}, err
	}
	if err := writeFileAtomic(s.File(it), data); err != nil {
		return Item{}, err
	}
	now := time.Now().UTC()
	sha := shaOf(data)
	if _, err := s.db.Exec(`UPDATE items SET size=?, sha256=?, updated=?, agent=CASE WHEN ? != '' THEN ? ELSE agent END, text_state='pending' WHERE id=?`,
		len(data), sha, ts(now), by, by, it.ID); err != nil {
		return Item{}, err
	}
	s.queue(it.ID)
	return s.get(it.ID)
}

// PublishRequest publishes a workspace file to the library.
type PublishRequest struct {
	Team    string
	Agent   string // whose workspace
	Dir     string // that workspace
	Path    string // relative to it
	Session string // the conversation it came from
	Seq     int64  // the message there
	Title   string
	By      string // who published ("" the owner)
}

// Publish files the workspace file in the library and links the two. A
// file published before becomes a new version of its document (同步)
// instead of a second one; otherwise it gets a name of its own (an
// existing document of that name is not replaced).
func (s *Service) Publish(ctx context.Context, r PublishRequest) (LinkView, error) {
	p, err := wsFile(r.Dir, r.Path)
	if err != nil {
		return LinkView{}, err
	}
	rel, err := filepath.Rel(mustEval(r.Dir), p)
	if err != nil {
		return LinkView{}, err
	}
	rel = filepath.ToSlash(rel)
	data, err := os.ReadFile(p)
	if err != nil {
		return LinkView{}, err
	}
	if len(data) > MaxItemBytes {
		return LinkView{}, ErrTooLarge
	}
	v := Viewer{Team: r.Team}
	ws := func(string) (string, bool) { return r.Dir, true }
	if prev, _ := s.LinksOfFile(v, r.Agent, rel, ws); len(prev) > 0 {
		it, err := s.Push(v, prev[0].Item, ws, r.By)
		if err != nil {
			return LinkView{}, err
		}
		if r.Session != "" {
			_, _ = s.db.Exec(`UPDATE links SET session=?, seq=? WHERE item=?`, r.Session, r.Seq, it.ID)
		}
		return s.LinkOf(v, it.ID, ws)
	}
	it, err := s.Add(ctx, AddRequest{Team: r.Team, Name: filepath.Base(p), Title: r.Title, Data: data, Source: SourceAgent,
		Agent: r.Agent, Session: r.Session, distinct: true})
	if err != nil {
		return LinkView{}, err
	}
	if err := s.SetLink(it, r.Agent, rel, r.Session, r.Seq, data); err != nil {
		return LinkView{}, err
	}
	return s.LinkOf(v, it.ID, ws)
}

func mustEval(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// FileLink is one published file of an agent's workspace.
type FileLink struct {
	LinkView
	Name  string `json:"name"`
	Title string `json:"title,omitempty"`
}

// AgentLinks are the published files of agent's workspace dir, as v sees them.
func (s *Service) AgentLinks(v Viewer, agent, dir string) ([]FileLink, error) {
	rows, err := s.db.Query(`SELECT item FROM links WHERE agent = ? ORDER BY path`, agent)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	ws := func(string) (string, bool) { return dir, true }
	out := []FileLink{}
	for _, id := range ids {
		it, err := s.Get(v, id)
		if err != nil {
			continue
		}
		l, err := s.link(id)
		if err != nil {
			continue
		}
		out = append(out, FileLink{LinkView: LinkView{Link: l, State: s.state(it, l, ws)}, Name: it.Name, Title: it.Title})
	}
	return out, nil
}
