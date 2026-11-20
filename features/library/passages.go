package library

// Passages: every document's text is also kept in passages of about
// passageChars, each with the heading (and page) it falls under and where
// it starts in the text. A search finds documents as before, then the
// passage of each that matches best: its snippet, its place ("第 3 页 ·
// 风险"), and the offset library_read can start from.
//
// Summaries (optional, off by default): a cheap model writes two or three
// sentences and keywords (synonyms, both languages) for each document as
// it comes in; they are searched with its name and title, so the person's
// own search box finds documents by what they are about.

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const passageChars = 600

const passagesSchema = `
CREATE TABLE IF NOT EXISTS passages (
  id INTEGER PRIMARY KEY, item INTEGER NOT NULL, ord INTEGER NOT NULL,
  start INTEGER NOT NULL, heading TEXT NOT NULL DEFAULT '', body TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS passages_item ON passages(item, ord);
CREATE VIRTUAL TABLE IF NOT EXISTS passages_fts USING fts5(body, content='passages', content_rowid='id', tokenize='unicode61 remove_diacritics 2');
CREATE VIRTUAL TABLE IF NOT EXISTS passages_tri USING fts5(body, content='passages', content_rowid='id', tokenize='trigram');
CREATE TABLE IF NOT EXISTS summaries (
  id INTEGER PRIMARY KEY, summary TEXT NOT NULL DEFAULT '', keywords TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL, at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
`

// migratePassages builds passages for a library made before they existed.
func (s *Service) migratePassages() error {
	if _, err := s.db.Exec(passagesSchema); err != nil {
		return err
	}
	var v string
	if s.db.QueryRow(`SELECT value FROM meta WHERE key = 'passages'`).Scan(&v) == nil {
		return nil
	}
	// every document is indexed again (by the worker, in the background)
	if _, err := s.db.Exec(`UPDATE items SET text_state = 'pending'`); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT OR REPLACE INTO meta(key, value) VALUES ('passages', '1')`)
	return err
}

// Passage is where a search hit is in its document.
type Passage struct {
	Start   int    `json:"start"`             // rune offset in the text (library_read from)
	Heading string `json:"heading,omitempty"` // the section / page it is under
}

var (
	mdHeading = regexp.MustCompile(`^#{1,6}\s+(.+?)\s*#*\s*$`)
	pdfPage   = regexp.MustCompile(`^---\s*第\s*(\d+)\s*页\s*---$`)
)

type chunk struct {
	start   int
	heading string
	body    string
}

// passagesOf splits text (an item's Body) into passages under their headings.
func passagesOf(text string) []chunk {
	var out []chunk
	var cur strings.Builder
	curStart, pos := 0, 0
	page, head := "", ""
	label := func() string {
		switch {
		case page != "" && head != "":
			return "第 " + page + " 页 · " + head
		case page != "":
			return "第 " + page + " 页"
		}
		return head
	}
	curLabel := ""
	flush := func() {
		if b := strings.TrimSpace(cur.String()); b != "" {
			out = append(out, chunk{start: curStart, heading: curLabel, body: b})
		}
		cur.Reset()
	}
	for _, raw := range splitLong(strings.SplitAfter(text, "\n")) {
		line := strings.TrimSpace(raw)
		n := utf8.RuneCountInString(raw)
		newSection := false
		if m := pdfPage.FindStringSubmatch(line); m != nil {
			page, head, newSection = m[1], "", true
		} else if m := mdHeading.FindStringSubmatch(line); m != nil {
			head, newSection = m[1], true
		}
		// a new section, or a full passage at a paragraph break, starts a new one
		if newSection || (line == "" && utf8.RuneCountInString(cur.String()) >= passageChars) {
			flush()
			curStart, curLabel = pos, label()
		}
		if cur.Len() == 0 {
			curStart, curLabel = pos, label()
		}
		cur.WriteString(raw)
		// a very long paragraph (no blank lines) is cut too
		if utf8.RuneCountInString(cur.String()) >= passageChars*2 {
			flush()
			curStart = pos + n
		}
		pos += n
	}
	flush()
	return out
}

// indexPassages writes item id's passages (callers hold s.mu, in tx).
func indexPassages(tx *sql.Tx, id int64, body string) error {
	for i, c := range passagesOf(body) {
		// the stored body is exactly what is indexed (the heading is searched
		// too): the FTS tables read it back for snippets
		text := c.heading + "\n" + c.body
		res, err := tx.Exec(`INSERT INTO passages(item, ord, start, heading, body) VALUES (?, ?, ?, ?, ?)`, id, i, c.start, c.heading, text)
		if err != nil {
			return err
		}
		pid, _ := res.LastInsertId()
		if _, err := tx.Exec(`INSERT INTO passages_fts(rowid, body) VALUES (?, ?)`, pid, text); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO passages_tri(rowid, body) VALUES (?, ?)`, pid, text); err != nil {
			return err
		}
	}
	return nil
}

// unindexPassages removes item id's passages (callers hold s.mu).
func (s *Service) unindexPassages(id int64) {
	rows, err := s.db.Query(`SELECT id, body FROM passages WHERE item = ?`, id)
	if err != nil {
		return
	}
	type row struct {
		id   int64
		text string
	}
	var rs []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.text) == nil {
			rs = append(rs, r)
		}
	}
	rows.Close()
	for _, r := range rs {
		_, _ = s.db.Exec(`INSERT INTO passages_fts(passages_fts, rowid, body) VALUES('delete', ?, ?)`, r.id, r.text)
		_, _ = s.db.Exec(`INSERT INTO passages_tri(passages_tri, rowid, body) VALUES('delete', ?, ?)`, r.id, r.text)
	}
	_, _ = s.db.Exec(`DELETE FROM passages WHERE item = ?`, id)
}

// bestPassage is item id's passage that best matches match in table
// (passages_fts / passages_tri), or a LIKE match on like when match is "".
func (s *Service) bestPassage(id int64, table, match, like string) (Passage, string, bool) {
	var p Passage
	var snip string
	var err error
	if match != "" {
		err = s.db.QueryRow(`SELECT p.start, p.heading, snippet(`+table+`, 0, '«', '»', '…', 24)
			FROM `+table+` JOIN passages p ON p.id = `+table+`.rowid WHERE `+table+` MATCH ? AND p.item = ? ORDER BY `+table+`.rank LIMIT 1`,
			match, id).Scan(&p.Start, &p.Heading, &snip)
	} else {
		var body string
		err = s.db.QueryRow(`SELECT start, heading, body FROM passages WHERE item = ? AND body LIKE ? ESCAPE '\' ORDER BY ord LIMIT 1`, id, like).Scan(&p.Start, &p.Heading, &body)
		snip = body
	}
	if err != nil {
		return Passage{}, "", false
	}
	// the heading line is searched with the passage, not shown in the snippet
	if i := strings.Index(snip, "\n"); i >= 0 && strings.HasPrefix(p.Heading+"\n", strings.TrimPrefix(snip[:i+1], "…")) {
		snip = snip[i+1:]
	}
	snip = strings.TrimPrefix(snip, "\n")
	return p, snip, true
}

// SearchMany searches several phrasings of one question (synonyms, both
// languages) and merges the hits: documents found by more of them first.
func (s *Service) SearchMany(ctx context.Context, v Viewer, queries []string, limit int) ([]Hit, error) {
	var qs []string
	seen := map[string]bool{}
	for _, q := range queries {
		if q = strings.TrimSpace(q); q != "" && !seen[q] {
			seen[q] = true
			qs = append(qs, q)
		}
	}
	if len(qs) <= 1 {
		q := ""
		if len(qs) == 1 {
			q = qs[0]
		}
		return s.Search(ctx, v, q, limit)
	}
	if len(qs) > 8 {
		qs = qs[:8]
	}
	type acc struct {
		hit   Hit
		n     int
		first int
	}
	by := map[int64]*acc{}
	order := 0
	for _, q := range qs {
		hits, err := s.Search(ctx, v, q, limit)
		if err != nil {
			return nil, err
		}
		for _, h := range hits {
			if a, ok := by[h.ID]; ok {
				a.n++
				continue
			}
			by[h.ID] = &acc{hit: h, n: 1, first: order}
			order++
		}
	}
	out := make([]*acc, 0, len(by))
	for _, a := range by {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].n != out[j].n {
			return out[i].n > out[j].n
		}
		return out[i].first < out[j].first
	})
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	hits := make([]Hit, 0, min(limit, len(out)))
	for i := 0; i < len(out) && i < limit; i++ {
		hits = append(hits, out[i].hit)
	}
	return hits, nil
}

// ---------------------------------------------------------------- summaries

// Settings of the library (library/settings.json).
type Settings struct {
	// Summaries: a model writes a summary and keywords for each document.
	Summaries bool `json:"summaries"`
}

func (s *Service) settingsPath() string { return filepath.Join(s.root, "settings.json") }

// Settings are the library's settings.
func (s *Service) Settings() Settings {
	var st Settings
	if b, err := os.ReadFile(s.settingsPath()); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

// SetSettings saves them; turning summaries on starts writing them for the
// documents that have none.
func (s *Service) SetSettings(st Settings) error {
	b, _ := json.MarshalIndent(st, "", "  ")
	if err := os.WriteFile(s.settingsPath(), b, 0o600); err != nil {
		return err
	}
	if st.Summaries {
		s.kickSummaries()
	}
	return nil
}

// Summarizer writes a document's summary and keywords (the plugin gives it
// a cheap model through the router).
type Summarizer func(ctx context.Context, name, text string) (summary string, keywords []string, err error)

// summaryOf is item id's summary and keywords ("" when none).
func (s *Service) summaryOf(id int64) (string, []string) {
	var sum, kw string
	if s.db.QueryRow(`SELECT summary, keywords FROM summaries WHERE id = ? AND state = 'ok'`, id).Scan(&sum, &kw) != nil {
		return "", nil
	}
	return sum, splitTags(kw)
}

func (s *Service) kickSummaries() {
	select {
	case s.sumKick <- struct{}{}:
	default:
	}
}

// summaryWorker writes missing summaries while they are on: one document
// at a time, a moment apart, never twice for a failure.
func (s *Service) summaryWorker(ctx context.Context) {
	tick := time.NewTicker(10 * time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.sumKick:
		case <-tick.C:
		}
		for ctx.Err() == nil && s.Settings().Summaries && s.summarize != nil {
			var id int64
			// documents with text and no summary yet, newest first
			err := s.db.QueryRow(`SELECT i.id FROM items i LEFT JOIN summaries m ON m.id = i.id
				WHERE i.text_state = 'ok' AND m.id IS NULL ORDER BY i.updated DESC LIMIT 1`).Scan(&id)
			if err != nil {
				break
			}
			s.writeSummary(ctx, id)
			select {
			case <-ctx.Done():
				return
			case <-time.After(1500 * time.Millisecond):
			}
		}
	}
}

func (s *Service) writeSummary(ctx context.Context, id int64) {
	it, err := s.get(id)
	if err != nil {
		return
	}
	text := s.Body(id)
	if r := []rune(text); len(r) > 8000 {
		text = string(r[:8000])
	}
	state, sum, kw := "failed", "", ""
	if strings.TrimSpace(text) != "" {
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		ss, kws, err := s.summarize(cctx, it.Name, text)
		cancel()
		if err == nil && strings.TrimSpace(ss) != "" {
			state, sum = "ok", strings.TrimSpace(ss)
			var keep []string
			for _, k := range kws {
				if k = strings.TrimSpace(strings.ReplaceAll(k, ",", " ")); k != "" && len(keep) < 20 {
					keep = append(keep, k)
				}
			}
			kw = strings.Join(keep, ",")
		}
	}
	s.mu.Lock()
	_, _ = s.db.Exec(`INSERT OR REPLACE INTO summaries(id, summary, keywords, state, at) VALUES (?, ?, ?, ?, ?)`, id, sum, kw, state, time.Now().UTC().Format(time.RFC3339Nano))
	s.mu.Unlock()
	if state == "ok" {
		s.rewriteHead(id)
	}
}

// rewriteHead indexes item id again with its current head (name, title,
// note, tags, summary, keywords), the text as already extracted.
func (s *Service) rewriteHead(id int64) {
	it, err := s.get(id)
	if err != nil {
		return
	}
	body := s.Body(id)
	full := s.headOf(it) + "\n\n" + body
	s.mu.Lock()
	defer s.mu.Unlock()
	var old string
	if s.db.QueryRow(`SELECT body FROM texts WHERE id = ?`, id).Scan(&old) != nil {
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	for _, q := range []string{
		`INSERT INTO texts_fts(texts_fts, rowid, body) VALUES('delete', ?, ?)`,
		`INSERT INTO texts_tri(texts_tri, rowid, body) VALUES('delete', ?, ?)`,
	} {
		if _, err := tx.Exec(q, id, old); err != nil {
			return
		}
	}
	if _, err := tx.Exec(`UPDATE texts SET body = ? WHERE id = ?`, full, id); err != nil {
		return
	}
	for _, q := range []string{`INSERT INTO texts_fts(rowid, body) VALUES (?, ?)`, `INSERT INTO texts_tri(rowid, body) VALUES (?, ?)`} {
		if _, err := tx.Exec(q, id, full); err != nil {
			return
		}
	}
	_ = tx.Commit()
}

// headOf is the searchable head of an item: one line each for its name,
// title, note, tags, and its summary and keywords when written.
func (s *Service) headOf(it Item) string {
	parts := append([]string{it.Name, it.Title, it.Note}, it.Tags...)
	if sum, kw := s.summaryOf(it.ID); sum != "" {
		parts = append(parts, sum, strings.Join(kw, " "))
	}
	var head []string
	for _, p := range parts {
		if p = strings.Join(strings.Fields(p), " "); p != "" {
			head = append(head, p)
		}
	}
	return strings.Join(head, "\n")
}

// splitLong cuts lines longer than a passage at sentence ends (or anywhere),
// so a text without line breaks still becomes several passages.
func splitLong(lines []string) []string {
	var out []string
	for _, l := range lines {
		r := []rune(l)
		for len(r) > passageChars {
			cut := passageChars
			for i := passageChars; i > passageChars/2; i-- {
				if strings.ContainsRune("。！？；.!?;", r[i-1]) {
					cut = i
					break
				}
			}
			out = append(out, string(r[:cut]))
			r = r[cut:]
		}
		out = append(out, string(r))
	}
	return out
}
