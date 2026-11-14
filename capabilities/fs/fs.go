// Package fs defines the `fs` capability: read, write, exact-replace edit,
// list, delete and stat, with read-before-edit tracking. Providers (fs-local,
// later container mounts / SSH) implement FS; consumers are the file tools
// and the "Files" panel.
//
// Every operation carries an Access: which agent is acting, which roots it
// may touch (empty = unconfined, host mode) and whether writes are allowed.
// Before any write, edit or delete a provider runs the WriteIntent
// waterfall so policy plugins can veto it.
package fs

import (
	"context"
	"errors"
	"time"

	"github.com/hedeqiang/crewbase/kernel"
)

// ServiceKey is the service key of the fs capability.
const ServiceKey = "fs"

// Access is the per-call confinement.
type Access struct {
	Agent string `json:"agent"`
	// Roots the call may touch. Empty means unconfined (host mode only);
	// consumers must pass the workspace in confined modes.
	Roots    []string `json:"roots,omitempty"`
	ReadOnly bool     `json:"readOnly,omitempty"`
}

// Info describes a file or directory.
type Info struct {
	Path    string    `json:"path"` // resolved absolute path
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	Mode    string    `json:"mode"`
	ModTime time.Time `json:"modTime"`
	IsDir   bool      `json:"isDir,omitempty"`
	Symlink bool      `json:"symlink,omitempty"`
}

// File is a read result.
type File struct {
	Info
	Content []byte `json:"-"`
}

// WriteResult reports a write or edit.
type WriteResult struct {
	Path         string `json:"path"`
	Created      bool   `json:"created,omitempty"`
	Bytes        int    `json:"bytes"`
	Replacements int    `json:"replacements,omitempty"`
}

// FS is the `fs` service. Paths must be absolute; consumers resolve
// relative paths against the agent workspace.
type FS interface {
	// Read returns the file and records it as read by a.Agent.
	Read(ctx context.Context, a Access, path string) (File, error)
	// Write creates or replaces a file (parents are created). Replacing an
	// existing file requires a prior Read by the same agent of its current
	// version.
	Write(ctx context.Context, a Access, path string, content []byte) (WriteResult, error)
	// Edit replaces old with new exactly once; old must occur exactly once
	// and the file must have been read since it last changed.
	Edit(ctx context.Context, a Access, path, old, new string) (WriteResult, error)
	List(ctx context.Context, a Access, path string) ([]Info, error)
	Delete(ctx context.Context, a Access, path string, recursive bool) error
	Stat(ctx context.Context, a Access, path string) (Info, error)
}

// Errors. Providers wrap them with context; test with errors.Is.
var (
	ErrOutsideRoots = errors.New("path is outside the allowed roots")
	ErrReadOnly     = errors.New("file system is read-only in this mode")
	ErrNotRead      = errors.New("file has not been read yet; read it before editing")
	ErrStale        = errors.New("file changed since it was last read; read it again before editing")
	ErrNoMatch      = errors.New("old text not found in file")
	ErrNotUnique    = errors.New("old text occurs more than once; include more context to make it unique")
	ErrVetoed       = errors.New("write vetoed by policy")
	ErrNotAbs       = errors.New("path must be absolute")
	ErrTooLarge     = errors.New("file is too large")
	ErrIsDir        = errors.New("path is a directory")
	ErrNotFound     = errors.New("no such file or directory")
)

// Write-intent operations.
const (
	OpWrite  = "write"
	OpEdit   = "edit"
	OpDelete = "delete"
)

// Intent describes a pending mutation. A policy listener vetoes it by
// setting Deny (and not calling next is also a veto).
type Intent struct {
	Agent  string `json:"agent"`
	Op     string `json:"op"`
	Path   string `json:"path"` // resolved absolute path
	Exists bool   `json:"exists"`
	Size   int    `json:"size"` // new content size (write/edit)
	Deny   string `json:"deny,omitempty"`
}

// WriteIntent runs before every write, edit and delete (Waterfall). The
// terminal handler returns the intent unchanged; a nil result, an error or
// a non-empty Deny vetoes the mutation.
var WriteIntent = kernel.NewWaterfall[*Intent, *Intent]("fs/write-intent")
