package console

// Turn snapshots: before each turn, the files of the agent's workspace are
// recorded (path → size, mtime, content hash; contents in a shared,
// content-addressed blob store). After the turn the workspace is compared
// with that record: the turn's own changes, whoever made them (file tools
// or bash), and a 撤销这一轮 that puts the files back as they were before
// it — files the turn added are removed, ones it changed or deleted come
// back. Git repositories are left alone (no refs, no index): this works
// the same with or without git.
//
// Kept in <root>/snapshots/<agent>/<turn>.json and blobs/, the last
// maxSnapshots turns per agent. Dependency and build folders are skipped,
// and so are workspaces too large to copy (they say so).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxSnapshots      = 30
	maxSnapshotFiles  = 5000
	maxSnapshotBytes  = 64 << 20 // new copies per snapshot in total
	maxSnapshotTime   = 4 * time.Second
	maxSnapshotFileSz = 8 << 20 // a larger file is recorded but not copied
)

// skipped folders: dependencies, builds, caches, VCS
var snapSkip = map[string]bool{
	".git": true, "node_modules": true, ".venv": true, "venv": true, "__pycache__": true, ".next": true,
	"dist": true, "build": true, "target": true, ".cache": true, ".turbo": true, ".gradle": true, ".idea": true,
	".DS_Store": true, "vendor": true, ".pnpm-store": true, ".crewbase": true,
}

// the agent's own files at the workspace root (its persona and memory):
// Crewbase writes them, and undoing a turn's work leaves them alone
var snapOwn = map[string]bool{"INSTRUCTIONS.md": true, "MEMORY.md": true}

// folders at the workspace root that are Crewbase's (the agent's browser
// profile, uploads stay: the person sent them)
var snapOwnDir = map[string]bool{"browser": true}

type snapFile struct {
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"` // ns
	Hash  string `json:"hash,omitempty"`
	// Big: not copied (larger than maxSnapshotFileSz): it can be compared
	// by size and time, not restored.
	Big bool `json:"big,omitempty"`
}

// Snapshot is the workspace before one turn.
type Snapshot struct {
	Turn      string              `json:"turn"` // the turn's input id
	Session   string              `json:"session"`
	Workspace string              `json:"workspace"`
	At        time.Time           `json:"at"`
	Files     map[string]snapFile `json:"files"`
	// Partial: the workspace was too large; only the first files were kept.
	Partial bool `json:"partial,omitempty"`
	// After: the workspace when the turn ended (nil while it runs). The
	// changed files' new contents are in the blob store too, so the turn's
	// diff stays what it was after later turns.
	After map[string]snapFile `json:"after,omitempty"`
	Ended time.Time           `json:"ended,omitzero"`
	// Undone: when its files were put back as they were before it.
	Undone time.Time `json:"undone,omitzero"`
}

// TurnChanges is what one turn changed.
type TurnChanges struct {
	Turn    string       `json:"turn"`
	At      time.Time    `json:"at"`
	Files   []FileChange `json:"files"`
	Added   int          `json:"added"`
	Removed int          `json:"removed"`
	Partial bool         `json:"partial,omitempty"`
	// Restorable: every changed file can be put back.
	Restorable bool      `json:"restorable"`
	Undone     time.Time `json:"undone,omitzero"`
	// Later: turns after this one changed some of the same files
	// (restoring this one undoes theirs to those files too).
	Later []string `json:"later,omitempty"`
}

type snapshots struct {
	root string // <data root>/snapshots
	mu   sync.Mutex
}

func (s *snapshots) dir(agentID string) string { return filepath.Join(s.root, agentID) }
func (s *snapshots) blobDir() string           { return filepath.Join(s.root, "blobs") }

// take records the workspace before turn (in a moment: the turn waits).
func (s *snapshots) take(agentID, session, turn, ws string) error {
	if ws == "" || turn == "" {
		return nil
	}
	snap := Snapshot{Turn: turn, Session: session, Workspace: ws, At: time.Now().UTC(), Files: map[string]snapFile{}}
	prev := s.latest(agentID, ws)
	var total int64
	deadline := time.Now().Add(maxSnapshotTime)
	err := filepath.WalkDir(ws, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if p != ws && (snapSkip[name] || strings.HasPrefix(name, ".crewbase") || filepath.Dir(p) == ws && snapOwnDir[name]) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || snapSkip[name] {
			return nil
		}
		if len(snap.Files) >= maxSnapshotFiles || time.Now().After(deadline) {
			snap.Partial = true
			return filepath.SkipAll
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(ws, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if snapOwn[rel] {
			return nil
		}
		f := snapFile{Size: info.Size(), MTime: info.ModTime().UnixNano()}
		if f.Size > maxSnapshotFileSz {
			f.Big = true
			snap.Files[rel] = f
			return nil
		}
		// unchanged since the last snapshot: its blob is already there
		if o, ok := prev[rel]; ok && o.Size == f.Size && o.MTime == f.MTime && o.Hash != "" {
			f.Hash = o.Hash
			snap.Files[rel] = f
			return nil
		}
		if total += f.Size; total > maxSnapshotBytes {
			snap.Partial = true
			return filepath.SkipAll
		}
		h, err := s.store(p)
		if err != nil {
			return nil
		}
		f.Hash = h
		snap.Files[rel] = f
		return nil
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir(agentID), 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(snap)
	if err := os.WriteFile(filepath.Join(s.dir(agentID), turn+".json"), b, 0o600); err != nil {
		return err
	}
	s.prune(agentID)
	return nil
}

// store copies a file into the blob store; its hash.
func (s *snapshots) store(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	h := hex.EncodeToString(sum[:])
	dst := filepath.Join(s.blobDir(), h[:2], h)
	if _, err := os.Stat(dst); err == nil {
		return h, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", err
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return "", err
	}
	return h, os.Rename(tmp, dst)
}

func (s *snapshots) blob(h string) ([]byte, error) {
	if len(h) < 3 {
		return nil, errors.New("no content")
	}
	return os.ReadFile(filepath.Join(s.blobDir(), h[:2], h))
}

// list is an agent's snapshots, newest first.
func (s *snapshots) list(agentID string) []Snapshot {
	ents, _ := os.ReadDir(s.dir(agentID))
	var out []Snapshot
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if sn, err := s.get(agentID, strings.TrimSuffix(e.Name(), ".json")); err == nil {
			out = append(out, sn)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

func (s *snapshots) get(agentID, turn string) (Snapshot, error) {
	if strings.ContainsAny(turn, `/\`) || turn == "" {
		return Snapshot{}, errors.New("bad turn")
	}
	b, err := os.ReadFile(filepath.Join(s.dir(agentID), turn+".json"))
	if err != nil {
		return Snapshot{}, err
	}
	var sn Snapshot
	return sn, json.Unmarshal(b, &sn)
}

// latest is the newest snapshot's files of workspace ws (for reuse).
func (s *snapshots) latest(agentID, ws string) map[string]snapFile {
	for _, sn := range s.list(agentID) {
		if sn.Workspace == ws {
			return sn.Files
		}
	}
	return nil
}

// prune keeps the newest maxSnapshots per agent and drops blobs no
// snapshot of any agent uses (callers hold s.mu).
func (s *snapshots) prune(agentID string) {
	all := s.list(agentID)
	if len(all) <= maxSnapshots {
		return
	}
	for _, sn := range all[maxSnapshots:] {
		_ = os.Remove(filepath.Join(s.dir(agentID), sn.Turn+".json"))
	}
	used := map[string]bool{}
	agents, _ := os.ReadDir(s.root)
	for _, a := range agents {
		if !a.IsDir() || a.Name() == "blobs" {
			continue
		}
		for _, sn := range s.list(a.Name()) {
			for _, f := range sn.Files {
				if f.Hash != "" {
					used[f.Hash] = true
				}
			}
		}
	}
	_ = filepath.WalkDir(s.blobDir(), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !used[d.Name()] {
			_ = os.Remove(p)
		}
		return nil
	})
}

// now is the workspace's current state of the files a snapshot knows and
// those added since (only what changed is hashed).
func (s *snapshots) now(sn Snapshot, keep bool) (map[string]snapFile, error) {
	cur := map[string]snapFile{}
	err := filepath.WalkDir(sn.Workspace, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != sn.Workspace && (snapSkip[d.Name()] || strings.HasPrefix(d.Name(), ".crewbase") || filepath.Dir(p) == sn.Workspace && snapOwnDir[d.Name()]) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || snapSkip[d.Name()] || len(cur) >= maxSnapshotFiles*2 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(sn.Workspace, p)
		rel = filepath.ToSlash(rel)
		if snapOwn[rel] {
			return nil
		}
		f := snapFile{Size: info.Size(), MTime: info.ModTime().UnixNano()}
		if o, ok := sn.Files[rel]; ok && o.Size == f.Size && o.MTime == f.MTime {
			f.Hash = o.Hash // untouched
		} else if f.Size <= maxSnapshotFileSz {
			if keep {
				f.Hash, _ = s.store(p)
			} else if b, err := os.ReadFile(p); err == nil {
				sum := sha256.Sum256(b)
				f.Hash = hex.EncodeToString(sum[:])
			}
		} else {
			f.Big = true
		}
		cur[rel] = f
		return nil
	})
	return cur, err
}

// changed compares a snapshot with the workspace now, or with until.
func (s *snapshots) changed(sn Snapshot, until map[string]snapFile) ([]FileChange, error) {
	after := until
	if after == nil {
		var err error
		if after, err = s.now(sn, false); err != nil {
			return nil, err
		}
	}
	var out []FileChange
	seen := map[string]bool{}
	for rel, a := range after {
		seen[rel] = true
		if snapOwn[rel] {
			continue
		}
		b, ok := sn.Files[rel]
		switch {
		case !ok:
			if sn.Partial {
				continue // the snapshot did not reach it: unknown, not new
			}
			out = append(out, FileChange{Path: rel, Status: StatusAdded, Updated: time.Unix(0, a.MTime)})
		case a.Hash != b.Hash || (a.Hash == "" && (a.Size != b.Size || a.MTime != b.MTime)):
			out = append(out, FileChange{Path: rel, Status: StatusModified, Updated: time.Unix(0, a.MTime)})
		}
	}
	for rel := range sn.Files {
		if !seen[rel] && !snapOwn[rel] {
			out = append(out, FileChange{Path: rel, Status: StatusDeleted})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	if len(out) > maxChangedFiles {
		out = out[:maxChangedFiles]
	}
	return out, nil
}

// restore puts the workspace's files back as they were in the snapshot:
// what the changes since then touched (files added since are removed).
func (s *snapshots) restore(agentID string, sn Snapshot) (int, error) {
	files, err := s.changed(sn, nil)
	if err != nil {
		return 0, err
	}
	for _, f := range files {
		if f.Status != StatusAdded {
			if b := sn.Files[f.Path]; b.Hash == "" {
				return 0, fmt.Errorf("%s was too large to keep a copy of; nothing was changed", f.Path)
			}
		}
	}
	n := 0
	for _, f := range files {
		p := filepath.Join(sn.Workspace, filepath.FromSlash(f.Path))
		if rel, ok := inWorkspace(sn.Workspace, p); !ok || rel == "" {
			continue
		}
		switch f.Status {
		case StatusAdded:
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				return n, err
			}
		default:
			b, err := s.blob(sn.Files[f.Path].Hash)
			if err != nil {
				return n, fmt.Errorf("%s: its copy is gone", f.Path)
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return n, err
			}
			if err := os.WriteFile(p, b, 0o644); err != nil {
				return n, err
			}
		}
		n++
	}
	return n, s.markUndone(agentID, sn)
}

// markUndone records that sn's turn was undone, and so were the later
// turns in the same workspace (what they did to those files is gone too).
func (s *snapshots) markUndone(agentID string, sn Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	for _, o := range s.list(agentID) {
		if o.Workspace != sn.Workspace || o.At.Before(sn.At) || o.After == nil || !o.Undone.IsZero() {
			continue
		}
		o.Undone = now
		b, _ := json.Marshal(o)
		if err := os.WriteFile(filepath.Join(s.dir(agentID), o.Turn+".json"), b, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// finish records the workspace at the end of turn.
func (s *snapshots) finish(agentID, turn string) error {
	sn, err := s.get(agentID, turn)
	if err != nil || sn.After != nil {
		return err
	}
	after, err := s.now(sn, true)
	if err != nil {
		return err
	}
	sn.After, sn.Ended = after, time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := json.Marshal(sn)
	return os.WriteFile(filepath.Join(s.dir(agentID), turn+".json"), b, 0o600)
}

// turnChanges is what turn changed (from its start to its end; while it
// runs, to now), with line counts.
func (s *snapshots) turnChanges(agentID, turn string) (TurnChanges, error) {
	sn, err := s.get(agentID, turn)
	if err != nil {
		return TurnChanges{}, err
	}
	files, err := s.changed(sn, sn.After)
	if err != nil {
		return TurnChanges{}, err
	}
	tc := TurnChanges{Turn: turn, At: sn.At, Files: files, Partial: sn.Partial, Restorable: true, Undone: sn.Undone}
	if tc.Files == nil {
		tc.Files = []FileChange{}
	}
	touched := map[string]bool{}
	for i := range tc.Files {
		f := &tc.Files[i]
		touched[f.Path] = true
		old, cur := s.contents(sn, f.Path)
		switch {
		case f.Status != StatusAdded && sn.Files[f.Path].Hash == "":
			tc.Restorable = false
			f.Large = true
		case isBinary(old) || isBinary(cur):
			f.Binary = true
		default:
			_, f.Added, f.Removed = Diff(string(old), string(cur), 0)
		}
		tc.Added += f.Added
		tc.Removed += f.Removed
	}
	// later turns that changed the same files
	for _, o := range s.list(agentID) {
		if !o.At.After(sn.At) || o.Workspace != sn.Workspace {
			continue
		}
		fs, _ := s.changed(o, o.After)
		for _, f := range fs {
			if touched[f.Path] {
				tc.Later = append(tc.Later, o.Turn)
				break
			}
		}
	}
	return tc, nil
}

// contents is a file before and after the turn (after: as it ended, or
// the workspace now while it runs).
func (s *snapshots) contents(sn Snapshot, rel string) (old, cur []byte) {
	if b := sn.Files[rel]; b.Hash != "" {
		old, _ = s.blob(b.Hash)
	}
	if sn.After != nil {
		if a := sn.After[rel]; a.Hash != "" {
			cur, _ = s.blob(a.Hash)
		}
		return old, cur
	}
	cur, _ = os.ReadFile(filepath.Join(sn.Workspace, filepath.FromSlash(rel)))
	return old, cur
}

// turnDiff is one file's diff in turn.
func (s *snapshots) turnDiff(agentID, turn, rel string) (FileDiff, error) {
	tc, err := s.turnChanges(agentID, turn)
	if err != nil {
		return FileDiff{}, err
	}
	for _, f := range tc.Files {
		if f.Path != rel {
			continue
		}
		fd := FileDiff{FileChange: f, Hunks: []Hunk{}}
		if f.Binary || f.Large {
			return fd, nil
		}
		sn, _ := s.get(agentID, turn)
		old, cur := s.contents(sn, rel)
		fd.Hunks, fd.Added, fd.Removed = Diff(string(old), string(cur), 3)
		return fd, nil
	}
	return FileDiff{}, os.ErrNotExist
}

// TurnSummary is one turn's line in GET /api/agents/{id}/turns.
type TurnSummary struct {
	Turn    string `json:"turn"`
	Files   int    `json:"files"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Undone  bool   `json:"undone,omitempty"`
}

// summaries are the turns of session that changed files (newest first).
func (s *snapshots) summaries(agentID, session string) []TurnSummary {
	out := []TurnSummary{}
	for _, sn := range s.list(agentID) {
		if session != "" && sn.Session != session || sn.After == nil {
			continue
		}
		tc, err := s.turnChanges(agentID, sn.Turn)
		if err != nil || len(tc.Files) == 0 {
			continue
		}
		out = append(out, TurnSummary{Turn: sn.Turn, Files: len(tc.Files), Added: tc.Added, Removed: tc.Removed, Undone: !sn.Undone.IsZero()})
	}
	return out
}
