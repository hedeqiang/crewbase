// Package audit is the append-only, hash-chained audit log (docs/06 §10,
// requirement S-7). Records live in ${dir}/audit/audit-YYYYMM.jsonl, one JSON
// object per line:
//
//	{seq, time, type, actor, agent, session, data, prev, hash}
//
// hash = hex(sha256(prev || canonical JSON of the record without hash)), and
// prev is the previous record's hash ("" for the first record), so the chain
// spans month files. Verify recomputes it. Audit records are a separate log:
// they are never written to sessions and never enter model context.
package audit

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Record is one audit entry.
type Record struct {
	Seq     int64           `json:"seq"`
	Time    time.Time       `json:"time"`
	Type    string          `json:"type"`
	Actor   string          `json:"actor,omitempty"`
	Agent   string          `json:"agent,omitempty"`
	Session string          `json:"session,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
	Prev    string          `json:"prev"`
	Hash    string          `json:"hash"`
}

// body is the hashed part of a record: every field except Hash, in a fixed
// order.
type body struct {
	Seq     int64           `json:"seq"`
	Time    time.Time       `json:"time"`
	Type    string          `json:"type"`
	Actor   string          `json:"actor,omitempty"`
	Agent   string          `json:"agent,omitempty"`
	Session string          `json:"session,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
	Prev    string          `json:"prev"`
}

// canonicalData re-encodes JSON with sorted object keys and no
// insignificant whitespace; numbers are kept verbatim.
func canonicalData(raw []byte) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v) // maps marshal with sorted keys
}

// ComputeHash returns the chain hash of r (r.Hash is ignored).
func ComputeHash(r Record) (string, error) {
	data, err := canonicalData(r.Data)
	if err != nil {
		return "", fmt.Errorf("audit: data: %w", err)
	}
	b, err := json.Marshal(body{Seq: r.Seq, Time: r.Time.UTC(), Type: r.Type, Actor: r.Actor, Agent: r.Agent, Session: r.Session, Data: data, Prev: r.Prev})
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte(r.Prev))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Log is an open audit log.
type Log struct {
	dir string // ${dir}/audit
	now func() time.Time
	// sync fsyncs after every record (default true).
	sync bool

	mu    sync.Mutex
	seq   int64
	head  string
	file  *os.File
	month string
	// unsynced: records written by AppendLazy not yet fsynced
	unsynced bool
}

const filePrefix, fileSuffix = "audit-", ".jsonl"

// FileFor returns the file name holding records of t.
func FileFor(t time.Time) string { return filePrefix + t.UTC().Format("200601") + fileSuffix }

// Open opens (or creates) the log under root/audit and recovers the chain
// head from the newest file.
func Open(root string) (*Log, error) {
	l := &Log{dir: filepath.Join(root, "audit"), now: time.Now, sync: true}
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		return nil, err
	}
	files, err := l.files()
	if err != nil {
		return nil, err
	}
	for i := len(files) - 1; i >= 0; i-- {
		last, ok, err := lastRecord(filepath.Join(l.dir, files[i]))
		if err != nil {
			return nil, err
		}
		if ok {
			l.seq, l.head = last.Seq, last.Hash
			break
		}
	}
	return l, nil
}

// Dir returns the directory holding the month files.
func (l *Log) Dir() string { return l.dir }

func (l *Log) files() ([]string, error) { return listFiles(l.dir) }

func listFiles(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if !e.IsDir() && strings.HasPrefix(n, filePrefix) && strings.HasSuffix(n, fileSuffix) {
			out = append(out, n)
		}
	}
	sort.Strings(out) // YYYYMM sorts chronologically
	return out, nil
}

func lastRecord(path string) (Record, bool, error) {
	var last Record
	found := false
	err := scan(path, func(_ int, line []byte) error {
		var r Record
		if json.Unmarshal(line, &r) == nil && r.Hash != "" {
			last, found = r, true
		}
		return nil
	})
	return last, found, err
}

func scan(path string, fn func(lineNo int, line []byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	rd := bufio.NewReaderSize(f, 64<<10)
	n := 0
	for {
		line, err := rd.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			n++
			if ferr := fn(n, bytes.TrimSpace(line)); ferr != nil {
				return ferr
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// Head returns the last sequence number and hash.
func (l *Log) Head() (int64, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seq, l.head
}

// Entry is the caller-supplied part of a record.
type Entry struct {
	Type    string
	Actor   string
	Agent   string
	Session string
	Data    any
}

// Append writes one record and advances the chain. It is safe for
// concurrent use.
func (l *Log) Append(e Entry) (Record, error) { return l.append(e, true) }

// AppendLazy is Append without its own fsync: the record reaches the disk
// with the next synced record or Close. For routine records a crash may
// lose without harm (plugin lifecycle: every start records a snapshot),
// which would otherwise cost an fsync each on every start.
func (l *Log) AppendLazy(e Entry) (Record, error) { return l.append(e, false) }

func (l *Log) append(e Entry, durable bool) (Record, error) {
	var raw json.RawMessage
	if e.Data != nil {
		b, err := json.Marshal(e.Data)
		if err != nil {
			return Record{}, err
		}
		if raw, err = canonicalData(b); err != nil {
			return Record{}, err
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	r := Record{Seq: l.seq + 1, Time: l.now().UTC(), Type: e.Type, Actor: e.Actor, Agent: e.Agent, Session: e.Session, Data: raw, Prev: l.head}
	h, err := ComputeHash(r)
	if err != nil {
		return Record{}, err
	}
	r.Hash = h
	line, err := json.Marshal(r)
	if err != nil {
		return Record{}, err
	}
	f, err := l.fileLocked(r.Time)
	if err != nil {
		return Record{}, err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return Record{}, err
	}
	if l.sync && durable {
		_ = f.Sync() // also flushes earlier lazy records
		l.unsynced = false
	} else if l.sync {
		l.unsynced = true
	}
	l.seq, l.head = r.Seq, r.Hash
	return r, nil
}

func (l *Log) fileLocked(t time.Time) (*os.File, error) {
	name := FileFor(t)
	if l.file != nil && l.month == name {
		return l.file, nil
	}
	if l.file != nil {
		l.flushLocked()
		_ = l.file.Close()
		l.file = nil
	}
	path := filepath.Join(l.dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	// A torn final line (crash mid-write) is terminated so the next record
	// starts on its own line; Verify reports the torn line.
	if st, err := f.Stat(); err == nil && st.Size() > 0 {
		if rf, err := os.Open(path); err == nil {
			buf := make([]byte, 1)
			if _, err := rf.ReadAt(buf, st.Size()-1); err == nil && buf[0] != '\n' {
				_, _ = f.Write([]byte{'\n'})
			}
			rf.Close()
		}
	}
	l.file, l.month = f, name
	return f, nil
}

// flushLocked fsyncs records written by AppendLazy.
func (l *Log) flushLocked() {
	if l.unsynced && l.file != nil {
		_ = l.file.Sync()
	}
	l.unsynced = false
}

// Close releases the file handle.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		l.flushLocked()
		err := l.file.Close()
		l.file = nil
		return err
	}
	return nil
}

// Query returns records with from <= time < to (zero bounds are open),
// optionally filtered by type prefix, oldest first, at most limit (0 = all).
func (l *Log) Query(from, to time.Time, typePrefix string, limit int) ([]Record, error) {
	files, err := l.files()
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, name := range files {
		month := strings.TrimSuffix(strings.TrimPrefix(name, filePrefix), fileSuffix)
		if mt, err := time.Parse("200601", month); err == nil {
			if !to.IsZero() && !mt.Before(to) {
				continue
			}
			if !from.IsZero() && !mt.AddDate(0, 1, 0).After(from) {
				continue
			}
		}
		err := scan(filepath.Join(l.dir, name), func(_ int, line []byte) error {
			var r Record
			if json.Unmarshal(line, &r) != nil {
				return nil
			}
			if !from.IsZero() && r.Time.Before(from) {
				return nil
			}
			if !to.IsZero() && !r.Time.Before(to) {
				return nil
			}
			if typePrefix != "" && !strings.HasPrefix(r.Type, typePrefix) {
				return nil
			}
			out = append(out, r)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

// VerifyResult is the outcome of a chain check.
type VerifyResult struct {
	OK      bool   `json:"ok"`
	Records int64  `json:"records"`
	Files   int    `json:"files"`
	Head    string `json:"head,omitempty"`
	// On failure: where the chain breaks and why.
	BrokenSeq  int64  `json:"brokenSeq,omitempty"`
	BrokenFile string `json:"brokenFile,omitempty"`
	BrokenLine int    `json:"brokenLine,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Verify checks the whole chain under root/audit (root is the plugin's
// configured dir): every line parses, seq increases by one, prev links to
// the previous hash and every hash recomputes.
func Verify(root string) (VerifyResult, error) {
	dir := filepath.Join(root, "audit")
	files, err := listFiles(dir)
	if err != nil {
		return VerifyResult{}, err
	}
	res := VerifyResult{OK: true, Files: len(files)}
	var prevHash string
	var prevSeq int64
	errBreak := errors.New("break")
	for _, name := range files {
		err := scan(filepath.Join(dir, name), func(n int, line []byte) error {
			fail := func(seq int64, msg string) error {
				res.OK, res.BrokenSeq, res.BrokenFile, res.BrokenLine, res.Error = false, seq, name, n, msg
				return errBreak
			}
			var r Record
			if err := json.Unmarshal(line, &r); err != nil {
				return fail(prevSeq+1, "unparsable record: "+err.Error())
			}
			if r.Seq != prevSeq+1 {
				return fail(r.Seq, fmt.Sprintf("sequence gap: want %d, got %d", prevSeq+1, r.Seq))
			}
			if r.Prev != prevHash {
				return fail(r.Seq, "prev does not match the previous record's hash")
			}
			h, err := ComputeHash(r)
			if err != nil {
				return fail(r.Seq, err.Error())
			}
			if h != r.Hash {
				return fail(r.Seq, "hash mismatch (record modified)")
			}
			prevHash, prevSeq = r.Hash, r.Seq
			res.Records++
			return nil
		})
		if errors.Is(err, errBreak) {
			return res, nil
		}
		if err != nil {
			return res, err
		}
	}
	res.Head = prevHash
	return res, nil
}
