// Package vcs defines the `vcs` capability: version control of the
// folders agents work in. A provider (providers/gitcli: the git binary)
// answers what a folder's repository is, its worktrees, status and diffs,
// and does the operations agents and people need to work side by side:
// isolated worktrees, staging single hunks, commits, merge previews that
// touch no working tree, merges, and pushes with a credential handed over
// for that one command.
//
// Consumers are features/git (the @crewbase/git plugin: worktree pool,
// freeze locks, code review, tools and API) and features/subagent (a
// sub-agent's workspace is allocated through AllocateWorkspace).
//
// Rules every provider follows:
//
//   - Commands are argv, never a shell line. Repository content cannot run
//     code through them: hooks, fsmonitor, external diff and textconv
//     drivers, signing programs and file:// transports are switched off on
//     every call.
//   - Writes to one repository are serialised (no fight over index.lock);
//     reads take no locks (GIT_OPTIONAL_LOCKS=0).
//   - Credentials are never written to disk or put in argv, and are
//     scrubbed from every output.
package vcs

import (
	"context"
	"errors"
	"time"

	"github.com/hedeqiang/crewbase/kernel"
)

// ServiceKey is the service key of the vcs capability (a Provider).
const ServiceKey = "vcs"

// Errors. Providers wrap them; test with errors.Is.
var (
	ErrNotRepo     = errors.New("not inside a git repository")
	ErrUnavailable = errors.New("git is not installed")
	ErrConflict    = errors.New("the merge has conflicts")
	ErrNotFF       = errors.New("not a fast-forward")
	ErrUpToDate    = errors.New("nothing to merge: already up to date")
	ErrDirty       = errors.New("the working tree has uncommitted changes")
	ErrStaleHunk   = errors.New("the hunk no longer matches the file; look at the diff again")
	ErrNothing     = errors.New("nothing to commit")
)

// RepoInfo describes the repository a folder lies in.
type RepoInfo struct {
	// ID is stable for a repository (all its worktrees share it).
	ID string `json:"id"`
	// Root is the top of the working tree the folder lies in (a worktree's
	// own root for a linked worktree).
	Root string `json:"root"`
	// Main is the main working tree of the repository ("" for a bare one).
	Main string `json:"main"`
	// CommonDir is the repository's .git directory (shared by worktrees);
	// GitDir is this working tree's own (differs in a linked worktree).
	CommonDir string `json:"commonDir"`
	GitDir    string `json:"gitDir"`
	Name      string `json:"name"` // the main tree's folder name
	Branch    string `json:"branch,omitempty"`
	Head      string `json:"head,omitempty"`
	Linked    bool   `json:"linked,omitempty"` // a linked worktree, not the main one
}

// Worktree is one working tree of a repository (git worktree list).
type Worktree struct {
	Path     string `json:"path"`
	Branch   string `json:"branch,omitempty"`
	Head     string `json:"head,omitempty"`
	Main     bool   `json:"main,omitempty"`
	Detached bool   `json:"detached,omitempty"`
	Locked   bool   `json:"locked,omitempty"`
	Prunable bool   `json:"prunable,omitempty"`
}

// File states in Status and FileDiff.
const (
	StModified   = "modified"
	StAdded      = "added"
	StDeleted    = "deleted"
	StRenamed    = "renamed"
	StUntracked  = "untracked"
	StConflicted = "conflicted"
	StTypeChange = "typechange"
)

// FileStatus is one changed path. Index and Work are git's X and Y letters.
type FileStatus struct {
	Path   string `json:"path"`
	Orig   string `json:"orig,omitempty"` // a rename's old path
	Index  string `json:"index"`
	Work   string `json:"work"`
	Status string `json:"status"`
	Staged bool   `json:"staged,omitempty"` // has changes in the index
	// Unstaged: has changes in the working tree not in the index.
	Unstaged bool `json:"unstaged,omitempty"`
}

// Status is a working tree's state.
type Status struct {
	Branch   string       `json:"branch,omitempty"`
	Head     string       `json:"head,omitempty"`
	Upstream string       `json:"upstream,omitempty"`
	Ahead    int          `json:"ahead"`
	Behind   int          `json:"behind"`
	Files    []FileStatus `json:"files"`
	Clean    bool         `json:"clean"`
	// Merging: a merge is in progress (conflicts being resolved).
	Merging bool `json:"merging,omitempty"`
}

// Line is one line of a hunk. Kind is ' ', '+' or '-'; Old and New are the
// line numbers on each side (0 where the line is not on that side).
type Line struct {
	Kind string `json:"k"`
	Text string `json:"t"`
	Old  int    `json:"o,omitempty"`
	New  int    `json:"n,omitempty"`
	// NoEOL: "\ No newline at end of file" follows this line.
	NoEOL bool `json:"noEol,omitempty"`
}

// Hunk is one @@ block. ID is derived from the path and the hunk's lines
// (not their numbers), so it stays the same while the hunk does.
type Hunk struct {
	ID       string `json:"id"`
	Header   string `json:"header"`
	OldStart int    `json:"oldStart"`
	OldLines int    `json:"oldLines"`
	NewStart int    `json:"newStart"`
	NewLines int    `json:"newLines"`
	Lines    []Line `json:"lines"`
}

// FileDiff is the change of one file.
type FileDiff struct {
	Path    string `json:"path"`
	Orig    string `json:"orig,omitempty"`
	Status  string `json:"status"`
	Binary  bool   `json:"binary,omitempty"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Hunks   []Hunk `json:"hunks"`
	// Head is the patch header (diff --git … +++ b/…), so a hunk can be
	// applied on its own.
	Head string `json:"-"`
}

// Diff sides.
const (
	DiffWork   = "work"   // working tree against the index (unstaged)
	DiffStaged = "staged" // index against HEAD
	DiffHead   = "head"   // working tree against HEAD (both)
	DiffRange  = "range"  // Base...Head (what a branch adds since it forked)
)

// DiffOptions selects a diff.
type DiffOptions struct {
	Side  string   // DiffWork (default), DiffStaged, DiffHead, DiffRange
	Base  string   // DiffRange: the branch or commit compared against
	Head  string   // DiffRange: "" = HEAD
	Paths []string // limit to these paths
	// Untracked adds untracked files as whole-file additions (DiffWork,
	// DiffHead).
	Untracked bool
	Context   int // lines of context (default 3)
}

// Hunk actions.
const (
	HunkStage   = "stage"
	HunkUnstage = "unstage"
	HunkDiscard = "discard"
)

// Signature is an author or committer.
type Signature struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Commit is one commit.
type Commit struct {
	Hash    string    `json:"hash"`
	Short   string    `json:"short"`
	Subject string    `json:"subject"`
	Author  string    `json:"author"`
	Email   string    `json:"email,omitempty"`
	When    time.Time `json:"when"`
	Parents []string  `json:"parents,omitempty"`
}

// CommitRequest makes a commit of what is staged (All: of every change,
// untracked files included).
type CommitRequest struct {
	Message string
	Author  Signature
	All     bool
	Paths   []string // stage these first
}

// Merge strategies.
const (
	MergeNoFF   = "merge-no-ff"
	MergeSquash = "squash"
	MergeFFOnly = "ff-only"
)

// MergePreview is what merging Source into Target would do, worked out
// without touching any working tree (git merge-tree --write-tree).
type MergePreview struct {
	Target       string   `json:"target"`
	Source       string   `json:"source"`
	TargetHead   string   `json:"targetHead"`
	SourceHead   string   `json:"sourceHead"`
	Base         string   `json:"base"`
	Clean        bool     `json:"clean"`
	Tree         string   `json:"tree,omitempty"`
	Conflicts    []string `json:"conflicts,omitempty"`
	Messages     []string `json:"messages,omitempty"`
	FastForward  bool     `json:"fastForward"`
	UpToDate     bool     `json:"upToDate,omitempty"`
	Ahead        int      `json:"ahead"`  // commits Source has that Target lacks
	Behind       int      `json:"behind"` // commits Target has that Source lacks
	Files        []string `json:"files,omitempty"`
	CheckedOutIn string   `json:"checkedOutIn,omitempty"` // a working tree that has Target checked out
}

// MergeRequest merges Source into Target.
type MergeRequest struct {
	Target   string
	Source   string
	Strategy string // MergeNoFF (default), MergeSquash, MergeFFOnly
	Message  string
	Author   Signature
	// Expect, when set, is the Source commit the merge was reviewed at: a
	// newer Source is refused.
	Expect string
}

// Conflict is one conflicted file of a merge in progress, with its blocks.
type Conflict struct {
	Path   string          `json:"path"`
	Blocks []ConflictBlock `json:"blocks,omitempty"`
}

// ConflictBlock is one <<<<<<< … >>>>>>> region (1-based line numbers).
type ConflictBlock struct {
	Start  int    `json:"start"`
	End    int    `json:"end"`
	Ours   string `json:"ours"`
	Theirs string `json:"theirs"`
	Base   string `json:"base,omitempty"`
}

// PushRequest pushes a branch. Username and Token, when set, are given to
// git through a one-off askpass for this command only.
type PushRequest struct {
	Remote   string
	Branch   string
	Username string
	Token    string
	Force    bool // --force-with-lease
	SetUp    bool // --set-upstream
}

// FetchRequest fetches from a remote. RefSpecs are full refspecs
// ("+refs/pull/7/head:refs/crewbase/pr/7"); empty = the remote's default.
// Username and Token work as in PushRequest.
type FetchRequest struct {
	Remote   string
	RefSpecs []string
	Username string
	Token    string
	Depth    int // 0 = full history
}

// Provider is the `vcs` service.
type Provider interface {
	// Probe returns the repository dir lies in (ErrNotRepo outside one).
	Probe(ctx context.Context, dir string) (RepoInfo, error)
	Worktrees(ctx context.Context, repo string) ([]Worktree, error)
	// AddWorktree checks out a new branch (from base) in path.
	AddWorktree(ctx context.Context, repo, path, branch, base string) error
	// RemoveWorktree removes the working tree (force: even with changes)
	// and, with deleteBranch, its branch.
	RemoveWorktree(ctx context.Context, repo, path string, force bool, deleteBranch string) error
	Status(ctx context.Context, dir string) (Status, error)
	Diff(ctx context.Context, dir string, o DiffOptions) ([]FileDiff, error)
	// ApplyHunk stages, unstages or discards one hunk of a file (ids from
	// Diff: DiffWork for stage / discard, DiffStaged for unstage).
	ApplyHunk(ctx context.Context, dir, path, hunkID, action string) error
	// Stage / Unstage / Discard whole files.
	Stage(ctx context.Context, dir string, paths []string) error
	Unstage(ctx context.Context, dir string, paths []string) error
	Discard(ctx context.Context, dir string, paths []string) error
	Commit(ctx context.Context, dir string, r CommitRequest) (Commit, error)
	Log(ctx context.Context, dir, rev string, n int) ([]Commit, error)
	// Resolve returns the commit a branch or revision points at.
	Resolve(ctx context.Context, dir, rev string) (string, error)
	MergePreview(ctx context.Context, repo, target, source string) (MergePreview, error)
	// Merge records the merge on Target without a working tree; a working
	// tree that has Target checked out (and no changes) is brought along.
	Merge(ctx context.Context, repo string, r MergeRequest) (Commit, error)
	// StartMerge merges target into the branch checked out in dir, leaving
	// conflict markers to resolve (the conflicts come back).
	StartMerge(ctx context.Context, dir, target string, author Signature) ([]Conflict, error)
	Conflicts(ctx context.Context, dir string) ([]Conflict, error)
	// Resolve a conflicted file: "ours", "theirs" or "manual" (the file as
	// it is now, markers gone).
	ResolveConflict(ctx context.Context, dir, path, how string) error
	ConcludeMerge(ctx context.Context, dir, message string, author Signature) (Commit, error)
	AbortMerge(ctx context.Context, dir string) error
	// Archive keeps a working tree's uncommitted changes as a patch and its
	// branch's commits since base as a bundle, before it is removed.
	Archive(ctx context.Context, dir, base, out string) (patch, bundle string, err error)
	RemoteURL(ctx context.Context, dir, remote string) (string, error)
	Push(ctx context.Context, dir string, r PushRequest) (string, error)
	Fetch(ctx context.Context, dir string, r FetchRequest) (string, error)
}

// ---------------------------------------------------------------- events

// Workspace isolation policies (who works where when a task is delegated).
const (
	// IsolateShared: the sub-agent works in the delegating agent's folder.
	IsolateShared = "shared"
	// IsolateWorktree: every sub-agent gets a worktree of its own.
	IsolateWorktree = "isolated-worktree"
	// IsolateOnWrite: a sub-agent that may write gets a worktree; one that
	// only reads works in the delegating agent's folder.
	IsolateOnWrite = "worktree-on-write"
)

// AllocateRequest asks for the folder a sub-agent works in.
type AllocateRequest struct {
	Parent        string // the delegating agent
	ParentSession string
	Child         string // the sub-agent's id
	Workspace     string // the delegating agent's folder
	Mode          string // the sub-agent's run mode
	Task          string
	// Worktree continues in an existing worktree (its id).
	Worktree string
}

// Allocation is the folder a sub-agent got. Release is called when its
// task is over (the worktree is kept when it holds work).
type Allocation struct {
	Dir      string
	Worktree string // the worktree's id ("" = no worktree)
	Branch   string
	Note     string // told to the sub-agent (where it works, what to use)
	// ReadOnly: the sub-agent may only read there (an untrusted worktree).
	ReadOnly bool
	Release  func(ctx context.Context) string
}

// AllocateWorkspace gives a sub-agent its folder (Bail): the first listener
// that answers ok decides; no answer = the default (its own folder).
var AllocateWorkspace = kernel.NewBail[AllocateRequest, Allocation]("vcs/allocate-workspace")

// PreMergeCheck is what a merge is about to do; listeners veto it by
// setting Deny (with a stable Code such as GIT_LOCK_FROZEN).
type PreMergeCheck struct {
	Repo    string
	Target  string
	Source  string
	Actor   string // who merges
	Author  string // whose work it is
	Preview MergePreview
	Changed []FileDiff // what Source changes since the merge base
	Deny    string
	Code    string
}

// PreMerge runs before every merge (Waterfall, in order).
var PreMerge = kernel.NewWaterfall[*PreMergeCheck, *PreMergeCheck]("git/pre-merge")

// Event is a git state change (worktree, commit, review, lock, merge,
// push), for the UI's live updates and the audit log.
type Event struct {
	Type    string `json:"type"` // git/worktree.created, git/commit, git/review.updated, …
	Repo    string `json:"repo,omitempty"`
	ID      string `json:"id,omitempty"`
	Actor   string `json:"actor,omitempty"`
	Agent   string `json:"agent,omitempty"`
	Session string `json:"session,omitempty"`
	Data    any    `json:"data,omitempty"`
}

// Changed is emitted after every git state change.
var Changed = kernel.NewEmit[Event]("git/changed")

// ForgeEvent is a verified webhook from a code host (GitHub), passed on by
// the webhooks feature so pull requests stay in step with local reviews.
type ForgeEvent struct {
	Team     string
	Provider string // github
	Event    string // pull_request | pull_request_review | check_run | check_suite
	Delivery string
	Body     any // the decoded JSON
}

// ForgeResult says what the event did.
type ForgeResult struct {
	Detail string // e.g. "review rv… : checks failed, fix round 2"
	Ref    string // the review or worktree
}

// Forge hands a verified code-host webhook to whoever tracks pull
// requests (Bail: the first listener that took it answers).
var Forge = kernel.NewBail[ForgeEvent, ForgeResult]("vcs/forge-event")
