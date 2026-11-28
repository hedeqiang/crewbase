package agent

// 默认工作区: where new agents' workspaces are made.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ---------------------------------------------------------------- 默认工作区

// workspaceFile keeps Settings › 通用 › 默认工作区 (where new agents' own
// folders go). Agents already made stay where they are.
const workspaceFile = "workspace.json"

// base is the folder new agents' own folders go in.
func (r *Registry) base() string {
	if b, _ := r.wsBase.Load().(string); b != "" {
		return b
	}
	return filepath.Join(r.root, "workspaces")
}

// WorkspaceBase reports where new agents' folders go and whether that is
// the default (<root>/workspaces).
func (r *Registry) WorkspaceBase() (dir string, isDefault bool) {
	b, _ := r.wsBase.Load().(string)
	return r.base(), b == ""
}

// DefaultWorkspaceBase is <root>/workspaces.
func (r *Registry) DefaultWorkspaceBase() string { return filepath.Join(r.root, "workspaces") }

// Why a workspace folder was refused (WorkspaceError.Code).
const (
	WsNotAbsolute = "not_absolute"
	WsTooBroad    = "too_broad"
	WsDataFolder  = "data_folder"
	WsNotWritable = "not_writable"
)

// WorkspaceError explains a refused workspace folder.
type WorkspaceError struct {
	Code string
	Dir  string
	Msg  string
}

func (e *WorkspaceError) Error() string { return e.Msg }

// SetWorkspaceBase makes dir ("" = the default) where new agents' folders
// go. dir must be an absolute folder (~ is expanded) outside the data
// root, which holds the vault and other private state; it is created when
// missing and must be writable.
func (r *Registry) SetWorkspaceBase(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if rest, ok := strings.CutPrefix(dir, "~"); ok && (rest == "" || rest[0] == '/' || rest[0] == filepath.Separator) {
		if h, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(h, rest)
		}
	}
	def := filepath.Join(r.root, "workspaces")
	if dir != "" {
		if !filepath.IsAbs(dir) {
			return "", &WorkspaceError{Code: WsNotAbsolute, Dir: dir, Msg: "the workspace folder must be a full path, e.g. ~/Crewbase"}
		}
		dir = filepath.Clean(dir)
	}
	if dir == "" || dir == def {
		dir = ""
	} else {
		if dir == "/" || dir == filepath.Clean(os.Getenv("HOME")) {
			return "", &WorkspaceError{Code: WsTooBroad, Dir: dir, Msg: "pick a folder of its own, not " + dir}
		}
		rootAbs, _ := filepath.Abs(r.root)
		if within(dir, rootAbs) || within(rootAbs, dir) {
			return "", &WorkspaceError{Code: WsDataFolder, Dir: rootAbs, Msg: "the workspace folder cannot be inside Crewbase's data folder " + rootAbs + ", nor contain it"}
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", &WorkspaceError{Code: WsNotWritable, Dir: dir, Msg: fmt.Sprintf("cannot create %s: %v", dir, err)}
		}
		probe, err := os.CreateTemp(dir, ".crewbase-write-*")
		if err != nil {
			return "", &WorkspaceError{Code: WsNotWritable, Dir: dir, Msg: fmt.Sprintf("cannot write in %s: %v", dir, err)}
		}
		probe.Close()
		_ = os.Remove(probe.Name())
	}
	b, _ := json.Marshal(map[string]string{"dir": dir})
	if err := os.WriteFile(filepath.Join(r.root, workspaceFile), b, 0o600); err != nil {
		return "", err
	}
	r.wsBase.Store(dir)
	return r.base(), nil
}

func (r *Registry) loadWorkspaceBase() {
	b, err := os.ReadFile(filepath.Join(r.root, workspaceFile))
	if err != nil {
		return
	}
	var v struct {
		Dir string `json:"dir"`
	}
	if json.Unmarshal(b, &v) == nil && v.Dir != "" && filepath.IsAbs(v.Dir) {
		if st, err := os.Stat(v.Dir); err == nil && st.IsDir() {
			r.wsBase.Store(v.Dir)
		} else {
			// e.g. an external disk not mounted: never recreate it on the
			// startup disk; new agents go to the data folder this run
			r.c.Log().Warn("default workspace folder unavailable; new agents use the data folder", "dir", v.Dir)
		}
	}
}

// within reports whether p is base or inside it.
func within(p, base string) bool {
	rel, err := filepath.Rel(base, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
