package kernel

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"sync"

	"gopkg.in/yaml.v3"
)

// Entry is one plugin entry in a bundle, profile or patch. Every entry should
// carry a stable id; without one, editing config looks like remove + add.
type Entry struct {
	ID          string         `yaml:"id" json:"id"`
	Name        string         `yaml:"name,omitempty" json:"name,omitempty"`
	Config      map[string]any `yaml:"config,omitempty" json:"config,omitempty"`
	Disabled    bool           `yaml:"disabled,omitempty" json:"disabled,omitempty"`
	Runtime     string         `yaml:"runtime,omitempty" json:"runtime,omitempty"` // "" (builtin) | process | wasm
	Permissions []string       `yaml:"permissions,omitempty" json:"permissions,omitempty"`
	Isolate     []string       `yaml:"isolate,omitempty" json:"isolate,omitempty"`
	Group       string         `yaml:"group,omitempty" json:"group,omitempty"`
}

// Bundle is a named, ordered list of entries.
type Bundle struct {
	Name    string  `yaml:"name" json:"name"`
	Entries []Entry `yaml:"entries" json:"entries"`
}

// Profile is a named assembly: bundles in order plus its own patch.
type Profile struct {
	Name    string   `yaml:"name" json:"name"`
	Bundles []string `yaml:"bundles" json:"bundles"`
	Patch   []Entry  `yaml:"patch,omitempty" json:"patch,omitempty"`
}

// ---------------------------------------------------------------- catalog

var (
	catalogMu sync.RWMutex
	catalog   = map[string]Spec{}
	bundles   = map[string]Bundle{}
	profiles  = map[string]Profile{}
	runtimes  = map[string]RuntimeLoader{}
)

// RuntimeLoader turns a non-builtin entry (process, wasm) into a Spec.
type RuntimeLoader func(e Entry) (Spec, error)

// Register adds a builtin plugin to the catalog under spec.Name.
func Register(spec Spec) Spec {
	catalogMu.Lock()
	defer catalogMu.Unlock()
	if _, ok := catalog[spec.Name]; ok {
		panic("kernel: plugin already registered: " + spec.Name)
	}
	catalog[spec.Name] = spec
	return spec
}

// RegisterBundle adds a builtin bundle.
func RegisterBundle(b Bundle) {
	catalogMu.Lock()
	defer catalogMu.Unlock()
	bundles[b.Name] = b
}

// RegisterProfile adds a builtin profile.
func RegisterProfile(p Profile) {
	catalogMu.Lock()
	defer catalogMu.Unlock()
	profiles[p.Name] = p
}

// RegisterRuntime adds a loader for entries with the given runtime.
func RegisterRuntime(name string, l RuntimeLoader) {
	catalogMu.Lock()
	defer catalogMu.Unlock()
	runtimes[name] = l
}

// Catalog lists registered builtin plugin names.
func Catalog() []string {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	out := make([]string, 0, len(catalog))
	for k := range catalog {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// LookupProfile returns a registered profile.
func LookupProfile(name string) (Profile, bool) {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	p, ok := profiles[name]
	return p, ok
}

// ---------------------------------------------------------------- patches

// ApplyPatch overlays patch on base by entry id: an entry with a name
// replaces the whole entry; an entry with only `disabled` toggles it; an
// unknown id is appended.
func ApplyPatch(base, patch []Entry) []Entry {
	out := append([]Entry(nil), base...)
	for _, p := range patch {
		idx := -1
		for i := range out {
			if out[i].ID == p.ID {
				idx = i
				break
			}
		}
		switch {
		case idx < 0:
			out = append(out, p)
		case p.Name == "" && p.Config == nil:
			out[idx].Disabled = p.Disabled
		default:
			if p.Name == "" {
				p.Name = out[idx].Name
			}
			out[idx] = p
		}
	}
	return out
}

// Resolve expands a profile into its final entry list: bundles in order →
// profile patch → extra patches (home patch, --patch) in order.
func Resolve(p Profile, extra ...[]Entry) ([]Entry, error) {
	catalogMu.RLock()
	var entries []Entry
	for _, bn := range p.Bundles {
		b, ok := bundles[bn]
		if !ok {
			catalogMu.RUnlock()
			return nil, fmt.Errorf("kernel: unknown bundle %q", bn)
		}
		entries = ApplyPatch(entries, b.Entries)
	}
	catalogMu.RUnlock()
	entries = ApplyPatch(entries, p.Patch)
	for _, x := range extra {
		entries = ApplyPatch(entries, x)
	}
	return entries, nil
}

// ReadPatchFile reads a YAML list of entries. A missing file is an empty patch.
func ReadPatchFile(path string) ([]Entry, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Entry
	if err := yaml.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return out, nil
}

// DumpConfig renders entries as YAML (for `crew --dump-config`).
func DumpConfig(entries []Entry) string {
	b, _ := yaml.Marshal(entries)
	return string(b)
}

func specFor(e Entry) (Spec, error) {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	if e.Runtime != "" && e.Runtime != "builtin" {
		l, ok := runtimes[e.Runtime]
		if !ok {
			return Spec{}, fmt.Errorf("no loader for runtime %q", e.Runtime)
		}
		return l(e)
	}
	s, ok := catalog[e.Name]
	if !ok {
		return Spec{}, fmt.Errorf("unknown plugin %q", e.Name)
	}
	return s, nil
}

// ApplyEntries reconciles the loaded tree with entries: unchanged entries
// (same id, deep-equal) stay mounted; changed ones remount; removed or
// disabled ones unload. Unknown plugins are reported as FAILED.
func (h *Host) ApplyEntries(entries []Entry) error {
	want := map[string]Entry{}
	for _, e := range entries {
		if e.ID == "" {
			e.ID = e.Name
		}
		if !e.Disabled {
			want[e.ID] = e
		}
	}
	h.mu.Lock()
	current := map[string]Entry{}
	var ids []string // only plugins that came from entries are managed here
	for _, p := range h.plugins {
		if p.fromEntry {
			current[p.opts.ID] = p.entry()
			ids = append(ids, p.opts.ID)
		}
	}
	h.mu.Unlock()

	// unload in reverse order
	for i := len(ids) - 1; i >= 0; i-- {
		id := ids[i]
		if w, ok := want[id]; !ok || !reflect.DeepEqual(normalize(w), normalize(current[id])) {
			h.Unload(id)
		}
	}
	var errs []error
	for _, e := range entries {
		if e.ID == "" {
			e.ID = e.Name
		}
		if e.Disabled {
			continue
		}
		if _, ok := h.Plugin(e.ID); ok {
			continue
		}
		if _, err := h.LoadEntry(e); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("kernel: %d entries failed: %v", len(errs), errs)
	}
	return nil
}

// LoadEntry resolves an entry (builtin catalog or a registered runtime such
// as "process") and loads it. Entries loaded this way are managed by
// ApplyEntries; plugins loaded directly with Load are left alone by it.
func (h *Host) LoadEntry(e Entry) (*Plugin, error) {
	if e.ID == "" {
		e.ID = e.Name
	}
	spec, serr := specFor(e)
	if serr != nil {
		spec = Spec{Name: e.Name, Apply: func(*Context) error { return serr }}
	}
	p, err := h.Load(spec, LoadOptions{ID: e.ID, Config: e.Config, Isolate: e.Isolate, Group: e.Group})
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	p.origin, p.fromEntry = e, true
	h.mu.Unlock()
	if serr != nil {
		return p, fmt.Errorf("%s: %w", e.ID, serr)
	}
	return p, nil
}

func (p *Plugin) entry() Entry {
	if p.origin.ID != "" {
		return p.origin
	}
	return Entry{ID: p.opts.ID, Name: p.spec.Name, Config: p.opts.Config, Isolate: p.opts.Isolate, Group: p.opts.Group}
}

func normalize(e Entry) string {
	b, _ := json.Marshal(e)
	return string(b)
}

// Decode converts the plugin's raw config into T (via JSON round trip).
func Decode[T any](c *Context) (T, error) {
	var out T
	b, err := json.Marshal(c.Config())
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(b, &out)
	return out, err
}
