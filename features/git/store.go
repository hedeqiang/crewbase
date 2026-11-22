package git

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

// Worktree states.
const (
	WTActive   = "active"
	WTArchived = "archived" // removed with its changes kept as a patch / bundle
	WTRemoved  = "removed"
	WTMerged   = "merged"
)

// Worktree is a working tree in the pool (<root>/worktrees/<repo>/<id>).
type Worktree struct {
	ID       string `json:"id"`
	Repo     string `json:"repo"`     // vcs.RepoInfo.ID
	RepoRoot string `json:"repoRoot"` // the main working tree
	Path     string `json:"path"`
	Branch   string `json:"branch"`
	Base     string `json:"base"`            // the branch it was made from
	BaseHead string `json:"baseHead"`        // that branch's commit then
	Holder   string `json:"holder"`          // the agent working in it
	Child    string `json:"child,omitempty"` // the sub-agent working in it
	// Children: every sub-agent that worked in it (who they worked for is Parent)
	Children []string  `json:"children,omitempty"`
	Parent   string    `json:"parent"`  // the agent whose folder it came from
	Session  string    `json:"session"` // the conversation that made it
	Task     string    `json:"task"`    // what it is for
	Origin   string    `json:"origin"`  // subagent | tool | person
	State    string    `json:"state"`   // WTActive …
	Archive  *Archive  `json:"archive,omitempty"`
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
}

// Archive is what was kept of a worktree removed with changes.
type Archive struct {
	Patch  string    `json:"patch,omitempty"`
	Bundle string    `json:"bundle,omitempty"`
	At     time.Time `json:"at"`
}

// Lock freezes a file (Start = End = 0) or a range of its lines against
// everyone but its holder (and the holder's sub-agents).
type Lock struct {
	ID      string    `json:"id"`
	Repo    string    `json:"repo"`
	Path    string    `json:"path"` // relative to the repository root
	Start   int       `json:"start,omitempty"`
	End     int       `json:"end,omitempty"`
	Holder  string    `json:"holder"` // actor: agent:<id> | person:<id>
	Reason  string    `json:"reason,omitempty"`
	Created time.Time `json:"created"`
}

// Whole reports whether the lock covers the whole file.
func (l Lock) Whole() bool { return l.Start <= 0 && l.End <= 0 }

// Review states.
const (
	RvDraft            = "draft"
	RvOpen             = "open"
	RvChangesRequested = "changes_requested"
	RvApproved         = "approved"
	RvMerged           = "merged"
	RvClosed           = "closed"
)

// Comment severities.
const (
	SevBlocker = "blocker"
	SevMajor   = "major"
	SevMinor   = "minor"
	SevNit     = "nit"
	SevNote    = "note"
)

// Review is a local code review of a branch against its target.
type Review struct {
	ID        string     `json:"id"`
	Repo      string     `json:"repo"`
	RepoRoot  string     `json:"repoRoot"`
	Worktree  string     `json:"worktree,omitempty"`
	Source    string     `json:"source"` // the branch under review
	Target    string     `json:"target"`
	Title     string     `json:"title"`
	Body      string     `json:"body,omitempty"`
	Author    string     `json:"author"` // actor
	Origin    string     `json:"origin"` // the author's origin (who it works for)
	Reviewers []string   `json:"reviewers"`
	State     string     `json:"state"`
	Head      string     `json:"head"` // the source commit under review now
	Base      string     `json:"base"` // the merge base then
	Round     int        `json:"round"`
	MaxRounds int        `json:"maxRounds"`
	Comments  []Comment  `json:"comments"`
	Verdicts  []Verdict  `json:"verdicts"`
	Session   string     `json:"session,omitempty"` // where it was opened
	Merge     *MergeInfo `json:"merge,omitempty"`
	Created   time.Time  `json:"created"`
	Updated   time.Time  `json:"updated"`
}

// Comment is anchored to a line of a file at a commit.
type Comment struct {
	ID       string    `json:"id"`
	Author   string    `json:"author"`
	Path     string    `json:"path,omitempty"`
	Line     int       `json:"line,omitempty"`
	Side     string    `json:"side,omitempty"` // new | old
	Severity string    `json:"severity"`
	Body     string    `json:"body"`
	Commit   string    `json:"commit"`
	Round    int       `json:"round"`
	Resolved bool      `json:"resolved,omitempty"`
	Created  time.Time `json:"created"`
}

// Verdict decisions.
const (
	VApprove        = "approve"
	VRequestChanges = "request_changes"
	VComment        = "comment"
)

// Verdict is a reviewer's decision at a commit.
type Verdict struct {
	Reviewer string    `json:"reviewer"`
	Decision string    `json:"decision"`
	Body     string    `json:"body,omitempty"`
	Commit   string    `json:"commit"`
	Round    int       `json:"round"`
	At       time.Time `json:"at"`
}

// MergeInfo is how a review was merged.
type MergeInfo struct {
	Commit   string    `json:"commit"`
	Strategy string    `json:"strategy"`
	By       string    `json:"by"`
	At       time.Time `json:"at"`
}

// Remote ties a host to the credential pushes to it use.
type Remote struct {
	Host       string `json:"host"`               // github.com
	Credential string `json:"credential"`         // vault:<id>
	Username   string `json:"username,omitempty"` // default x-access-token
}

// Settings are the git feature's settings (Settings › Git).
type Settings struct {
	// Isolation is where a sub-agent works when its parent's folder is a
	// repository (vcs.IsolateShared, IsolateWorktree, IsolateOnWrite).
	Isolation string `json:"isolation"`
	// Protected branches: committing or merging into one, and every push,
	// asks the person (glob patterns: main, release/*).
	Protected []string `json:"protected"`
	// MaxAutoRounds bounds the automatic fix → re-review rounds.
	MaxAutoRounds int `json:"maxAutoRounds"`
	// Approvals needed before a merge.
	Approvals int `json:"approvals"`
	// AutoReview: agents named as reviewers are asked to review at once.
	AutoReview bool     `json:"autoReview"`
	Remotes    []Remote `json:"remotes"`
}

// DefaultSettings are the settings before any change.
func DefaultSettings() Settings {
	return Settings{Isolation: "worktree-on-write", Protected: []string{"main", "master", "release/*"}, MaxAutoRounds: 3, Approvals: 1, AutoReview: true, Remotes: []Remote{}}
}

type state struct {
	Worktrees []*Worktree `json:"worktrees"`
	Locks     []*Lock     `json:"locks"`
	Reviews   []*Review   `json:"reviews"`
	Settings  *Settings   `json:"settings,omitempty"`
}

// store keeps the state in <root>/git/state.json.
type store struct {
	mu   sync.Mutex
	path string
	st   state
}

func openStore(dir string) (*store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &store{path: filepath.Join(dir, "state.json")}
	b, err := os.ReadFile(s.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(b, &s.st); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// save writes the state (the caller holds mu).
func (s *store) save() error {
	b, err := json.MarshalIndent(s.st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *store) settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.Settings == nil {
		return DefaultSettings()
	}
	x := *s.st.Settings
	x.Protected = slices.Clone(x.Protected)
	x.Remotes = slices.Clone(x.Remotes)
	return x
}

func newID(prefix string) string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func clone[T any](p *T) T { return *p }
