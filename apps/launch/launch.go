// Package launch resolves a profile into a running plugin host. It is shared
// by the crewd daemon and the crew CLI.
package launch

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hedeqiang/crewbase/bundles"
	"github.com/hedeqiang/crewbase/kernel"
)

// Options select what to run.
type Options struct {
	Profile string
	Root    string   // data dir, default ~/.crewbase
	Patches []string // extra --patch files
	Addr    string   // override gateway address
}

// DefaultRoot returns ~/.crewbase (or $CREWBASE_HOME).
func DefaultRoot() string {
	if v := os.Getenv("CREWBASE_HOME"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".crewbase"
	}
	return filepath.Join(home, ".crewbase")
}

func (o *Options) normalize() {
	if o.Profile == "" {
		o.Profile = "desktop"
	}
	if o.Root == "" {
		o.Root = DefaultRoot()
	}
}

// Resolve returns the final entry list: profile bundles → profile patch file
// (~/.crewbase/profiles/<name>/crewbase.patch.yml) → home patch
// (~/.crewbase/crewbase.patch.yml) → --patch files → --addr.
func Resolve(o Options) ([]kernel.Entry, error) {
	o.normalize()
	bundles.Init()
	p, ok := kernel.LookupProfile(o.Profile)
	if !ok {
		return nil, fmt.Errorf("unknown profile %q", o.Profile)
	}
	var patches [][]kernel.Entry
	files := []string{
		filepath.Join(o.Root, "profiles", o.Profile, "crewbase.patch.yml"),
		filepath.Join(o.Root, "crewbase.patch.yml"),
	}
	files = append(files, o.Patches...)
	// CREWBASE_PATCH (colon-separated) lets the daemon and the CLI checks
	// resolve the same configuration (e.g. in a container).
	if v := os.Getenv("CREWBASE_PATCH"); v != "" {
		for _, f := range strings.Split(v, string(os.PathListSeparator)) {
			if f = strings.TrimSpace(f); f != "" && !slices.Contains(files, f) {
				files = append(files, f)
			}
		}
	}
	for _, f := range files {
		pe, err := kernel.ReadPatchFile(f)
		if err != nil {
			return nil, err
		}
		patches = append(patches, pe)
	}
	entries, err := kernel.Resolve(p, patches...)
	if err != nil {
		return nil, err
	}
	for i := range entries {
		entries[i].Config = substitute(entries[i].Config, o.Root).(map[string]any)
		if o.Addr != "" && entries[i].ID == "gateway" {
			entries[i].Config["addr"] = o.Addr
		}
	}
	return entries, nil
}

func substitute(v any, root string) any {
	switch x := v.(type) {
	case nil:
		return map[string]any{}
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			if vv == nil {
				out[k] = nil
				continue
			}
			out[k] = substitute(vv, root)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = substitute(x[i], root)
		}
		return out
	case []string:
		out := make([]any, len(x))
		for i := range x {
			out[i] = strings.ReplaceAll(x[i], "${root}", root)
		}
		return out
	case string:
		return strings.ReplaceAll(x, "${root}", root)
	default:
		return v
	}
}

// Start creates a host and applies the resolved entries.
func Start(o Options) (*kernel.Host, error) {
	o.normalize()
	if err := os.MkdirAll(o.Root, 0o700); err != nil {
		return nil, err
	}
	entries, err := Resolve(o)
	if err != nil {
		return nil, err
	}
	h := kernel.NewHost(nil)
	if err := h.ApplyEntries(entries); err != nil {
		// keep running: failed plugins are visible in `crew plugins`
		h.Log().Warn("some plugins failed", "err", err)
	}
	return h, nil
}
