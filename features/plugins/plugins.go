// Package plugins installs, approves and runs third-party out-of-process
// plugins (docs/05 §7 and §11, docs/06 §7).
//
// Layout under Config.Dir (default ${root}/plugins):
//
//	enabled.json                   names of enabled plugins (loaded at startup)
//	<name>/installed.json          current version, source
//	<name>/grants.json             approved permissions, version, trustFutureVersions
//	<name>/<version>/              plugin files (crewbase-plugin.toml, entry, ...)
//	<name>/.data/                  the plugin's writable data dir
//
// A plugin runs only while it is enabled and its grant covers every
// permission its manifest asks for, for this version (or the user trusted
// future versions and the permissions did not grow). Otherwise it stays
// PENDING-APPROVAL and is not started. Running plugins are kernel entries
// with runtime "process" (bridge/process) loaded next to the manager.
package plugins

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/bridge/process"
	"github.com/hedeqiang/crewbase/bridge/wasm"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/sdk"
)

// ServiceKey is the key of the plugin manager service.
const ServiceKey = "plugins"

// States reported for installed plugins besides kernel states.
const (
	StateDisabled        = "DISABLED"
	StatePendingApproval = "PENDING-APPROVAL"
	StateStopped         = "STOPPED"
)

var (
	// ErrNotInstalled is returned for unknown plugin names.
	ErrNotInstalled = errors.New("plugin not installed")
	// ErrBadSource is returned when a source cannot be fetched or is not a plugin.
	ErrBadSource = errors.New("invalid plugin source")
)

//go:embed market.json
var defaultMarket []byte

// Config of the plugin manager.
type Config struct {
	Dir string `json:"dir"` // default ${root}/plugins (required)
	// MarketURL is the marketplace index: an http(s) URL or a file path;
	// empty uses the bundled index (market.json).
	MarketURL string `json:"marketURL,omitempty"`
	// MarketBase resolves relative ("./…") sources in the index; default
	// the index file's directory, or the working directory for the bundled
	// index and URLs.
	MarketBase string `json:"marketBase,omitempty"`
	// Process is merged into every process entry config (timeouts, backoff).
	Process map[string]any `json:"process,omitempty"`
	// Publishers are the trusted signing keys.
	Publishers []Publisher `json:"publishers,omitempty"`
	// RequireSigned refuses packages without a trusted signature.
	RequireSigned bool `json:"requireSigned,omitempty"`
}

// verifyOpts is what the marketplace says about a package.
type verifyOpts struct {
	name      string // expected plugin name ("" = any)
	integrity string
	signed    *Signed
}

// Grant is an approval record.
type Grant struct {
	Version             string              `json:"version"`
	Permissions         process.Permissions `json:"permissions"`
	TrustFutureVersions bool                `json:"trustFutureVersions"`
	ApprovedAt          time.Time           `json:"approvedAt"`
}

type record struct {
	Name        string    `json:"name"`
	Version     string    `json:"version"`
	Source      string    `json:"source"`
	SourceKind  string    `json:"sourceKind"` // local | tarball | npm
	InstalledAt time.Time `json:"installedAt"`
	Integrity   string    `json:"integrity,omitempty"`
	Publisher   string    `json:"publisher,omitempty"` // verified publisher id
	// the marketplace data, so updates verify the same way
	Signed *Signed `json:"signed,omitempty"`
	Pinned string  `json:"pinned,omitempty"`
	// Market is the marketplace entry it was installed from (updates).
	Market string `json:"market,omitempty"`
}

// Installed is the API view of an installed plugin.
type Installed struct {
	Name        string    `json:"name"`
	Version     string    `json:"version"`
	Description string    `json:"description,omitempty"`
	Author      string    `json:"author,omitempty"`
	Repo        string    `json:"repo,omitempty"`
	Source      string    `json:"source"`
	SourceKind  string    `json:"sourceKind"`
	Market      string    `json:"market,omitempty"`
	InstalledAt time.Time `json:"installedAt"`
	// Integrity is the package's sha256 (packages only).
	Integrity string `json:"integrity,omitempty"`
	// Publisher is set only when a trusted signature was verified.
	Publisher *Publisher          `json:"publisher,omitempty"`
	Dir       string              `json:"dir"`
	Sandbox   bool                `json:"sandbox"`
	Requested process.Permissions `json:"requested"`
	Grant     *Grant              `json:"grant,omitempty"`
	Approved  bool                `json:"approved"`
	// NeedsApproval lists what the user still has to approve.
	NeedsApproval []string        `json:"needsApproval,omitempty"`
	Enabled       bool            `json:"enabled"`
	State         string          `json:"state"`
	Error         string          `json:"error,omitempty"`
	Process       *process.Status `json:"process,omitempty"`
}

// Manager is the `plugins` service.
type Manager struct {
	c   *kernel.Context
	h   *kernel.Host
	cfg Config

	mu     sync.Mutex
	loaded map[string]bool // names loaded into the host
}

// EntryID is the kernel entry id of an installed plugin.
func EntryID(name string) string { return "plugin:" + name }

func (m *Manager) pdir(name string) string { return filepath.Join(m.cfg.Dir, name) }

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (m *Manager) enabledSet() map[string]bool {
	var names []string
	_ = readJSON(filepath.Join(m.cfg.Dir, "enabled.json"), &names)
	out := map[string]bool{}
	for _, n := range names {
		out[n] = true
	}
	return out
}

func (m *Manager) setEnabled(name string, on bool) error {
	set := m.enabledSet()
	if on {
		set[name] = true
	} else {
		delete(set, name)
	}
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	return writeJSON(filepath.Join(m.cfg.Dir, "enabled.json"), names)
}

type installed struct {
	rec   record
	man   process.Manifest
	grant *Grant
	dir   string
}

func (m *Manager) read(name string) (installed, error) {
	if !sdk.ValidName(name) {
		return installed{}, ErrNotInstalled
	}
	var in installed
	if err := readJSON(filepath.Join(m.pdir(name), "installed.json"), &in.rec); err != nil {
		return installed{}, ErrNotInstalled
	}
	in.dir = filepath.Join(m.pdir(name), in.rec.Version)
	man, err := process.ReadManifest(in.dir)
	if err != nil {
		return installed{}, fmt.Errorf("%s: %w", name, err)
	}
	in.man = man
	var g Grant
	if err := readJSON(filepath.Join(m.pdir(name), "grants.json"), &g); err == nil {
		in.grant = &g
	}
	return in, nil
}

// approval decides whether grant covers the manifest; needs lists the gaps.
func approval(man process.Manifest, g *Grant) (bool, []string) {
	req := man.Requested()
	if g == nil {
		needs := req.Strings()
		if len(needs) == 0 {
			needs = []string{"install"}
		}
		return false, needs
	}
	needs := g.Permissions.Missing(req)
	if g.Version != man.Version && !g.TrustFutureVersions {
		needs = append(needs, fmt.Sprintf("version %s (approved %s)", man.Version, g.Version))
	}
	return len(needs) == 0, needs
}

func (m *Manager) view(in installed, enabled bool) Installed {
	ok, needs := approval(in.man, in.grant)
	v := Installed{Name: in.man.Name, Version: in.man.Version, Description: in.man.Description, Author: in.man.Author,
		Repo: in.man.Repo, Source: in.rec.Source, SourceKind: in.rec.SourceKind, Market: in.rec.Market, InstalledAt: in.rec.InstalledAt, Integrity: in.rec.Integrity,
		Dir: in.dir, Sandbox: in.man.Sandbox, Requested: in.man.Requested(), Grant: in.grant, Approved: ok,
		NeedsApproval: needs, Enabled: enabled}
	if in.rec.Publisher != "" {
		for _, p := range m.cfg.Publishers {
			if p.ID == in.rec.Publisher {
				pp := Publisher{ID: p.ID, Name: p.Name}
				v.Publisher = &pp // the key is not repeated in the view
			}
		}
	}
	switch {
	case !enabled:
		v.State = StateDisabled
	case !ok:
		v.State = StatePendingApproval
	default:
		v.State = StateStopped
		if info, found := m.h.Plugin(EntryID(in.man.Name)); found {
			v.State, v.Error = string(info.State), info.Error
		}
		if st, found := process.Lookup(m.h, EntryID(in.man.Name)); found {
			v.Process = &st
		}
	}
	return v
}

// Installed lists installed plugins sorted by name.
func (m *Manager) Installed() []Installed {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listLocked()
}

func (m *Manager) listLocked() []Installed {
	ents, _ := os.ReadDir(m.cfg.Dir)
	en := m.enabledSet()
	out := []Installed{}
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		in, err := m.read(e.Name())
		if err != nil {
			continue
		}
		out = append(out, m.view(in, en[e.Name()]))
	}
	return out
}

// Get returns one installed plugin.
func (m *Manager) Get(name string) (Installed, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.getLocked(name)
}

func (m *Manager) getLocked(name string) (Installed, error) {
	in, err := m.read(name)
	if err != nil {
		return Installed{}, err
	}
	return m.view(in, m.enabledSet()[name]), nil
}

// ---------------------------------------------------------------- run state

func (m *Manager) unloadLocked(name string) {
	if m.loaded[name] {
		m.h.Unload(EntryID(name))
		delete(m.loaded, name)
	}
}

// syncLocked makes the host match the desired state of one plugin: loaded
// when enabled and approved, unloaded otherwise. restart forces a reload.
func (m *Manager) syncLocked(name string, restart bool) error {
	in, err := m.read(name)
	if err != nil {
		m.unloadLocked(name)
		return err
	}
	ok, _ := approval(in.man, in.grant)
	if !m.enabledSet()[name] || !ok {
		m.unloadLocked(name)
		return nil
	}
	if m.loaded[name] {
		if !restart {
			if info, found := m.h.Plugin(EntryID(name)); found && info.State != kernel.StateFailed {
				return nil
			}
		}
		m.unloadLocked(name)
	}
	if in.man.Runtime == process.RuntimeWASM {
		perms := in.grant.Permissions.Intersect(in.man.Requested())
		e := kernel.Entry{ID: EntryID(name), Name: in.man.Name, Runtime: process.RuntimeWASM, Permissions: perms.Strings(),
			Config: map[string]any{"dir": in.dir, "entry": in.man.Entry[0], "hooks": in.man.Hooks, "version": in.man.Version}}
		spec, err := wasm.Loader(e)
		if err != nil {
			return err
		}
		if _, err := m.h.Load(spec, kernel.LoadOptions{ID: e.ID, Config: e.Config}); err != nil {
			return err
		}
		m.loaded[name] = true
		if info, _ := m.h.Plugin(e.ID); info.State == kernel.StateFailed {
			return fmt.Errorf("plugin %s failed to start: %s", name, info.Error)
		}
		return nil
	}
	cfg := map[string]any{}
	for k, v := range m.cfg.Process {
		cfg[k] = v
	}
	cfg["dir"] = in.dir
	cfg["dataDir"] = filepath.Join(m.pdir(name), ".data")
	e := kernel.Entry{ID: EntryID(name), Name: in.man.Name, Runtime: process.RuntimeName,
		Permissions: in.grant.Permissions.Strings(), Config: cfg}
	spec, err := process.Loader(e)
	if err != nil {
		return err
	}
	if _, err := m.h.Load(spec, kernel.LoadOptions{ID: e.ID, Config: e.Config}); err != nil {
		return err
	}
	m.loaded[name] = true
	if info, _ := m.h.Plugin(e.ID); info.State == kernel.StateFailed {
		return fmt.Errorf("plugin %s failed to start: %s", name, info.Error)
	}
	return nil
}

// ---------------------------------------------------------------- operations

// InstallRequest installs from Source (local dir, tarball path/URL or npm
// package name) or from a marketplace entry by name.
type InstallRequest struct {
	Source string `json:"source,omitempty"`
	Market string `json:"market,omitempty"`
	// Enable (default true) marks the plugin enabled; it still only starts
	// once its permissions are approved.
	Enable *bool `json:"enable,omitempty"`
}

// Install fetches, unpacks and records a plugin. Installing a new version
// keeps the grant; whether it still applies is decided by approval.
func (m *Manager) Install(ctx context.Context, req InstallRequest) (Installed, error) {
	src := strings.TrimSpace(req.Source)
	var vo verifyOpts
	if req.Market != "" {
		ents, err := m.Market(ctx)
		if err != nil {
			return Installed{}, err
		}
		for _, e := range ents {
			if e.Name == req.Market {
				src = e.Source
				vo = verifyOpts{name: e.Name, integrity: e.Integrity, signed: e.Signed}
			}
		}
		if src == "" {
			return Installed{}, fmt.Errorf("%w: %q is not in the marketplace", ErrBadSource, req.Market)
		}
	}
	if src == "" {
		return Installed{}, fmt.Errorf("%w: source is required", ErrBadSource)
	}
	enable := req.Enable == nil || *req.Enable
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.installLocked(ctx, src, enable, false, vo)
}

func (m *Manager) installLocked(ctx context.Context, src string, enable, keepEnabled bool, vo verifyOpts) (Installed, error) {
	staging := filepath.Join(m.cfg.Dir, ".staging")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return Installed{}, err
	}
	tmp, err := os.MkdirTemp(staging, "install-")
	if err != nil {
		return Installed{}, err
	}
	defer os.RemoveAll(tmp)
	root, kind, canon, digest, err := fetch(ctx, src, tmp)
	if err != nil {
		return Installed{}, err
	}
	man, err := process.ReadManifest(root)
	if err != nil {
		return Installed{}, fmt.Errorf("%w: %v", ErrBadSource, err)
	}
	if vo.name != "" && man.Name != vo.name {
		return Installed{}, fmt.Errorf("%w: the package is %q, not %q", ErrUntrusted, man.Name, vo.name)
	}
	if err := checkIntegrity(vo.integrity, digest); err != nil {
		return Installed{}, err
	}
	pub, err := verify(m.cfg.Publishers, vo.signed, man.Name, man.Version, digest)
	if err != nil {
		return Installed{}, err
	}
	if m.cfg.RequireSigned && pub == nil {
		return Installed{}, fmt.Errorf("%w: unsigned packages are not allowed here (requireSigned)", ErrUntrusted)
	}
	if strings.HasPrefix(man.Version, ".") {
		return Installed{}, fmt.Errorf("%w: invalid version %q", ErrBadSource, man.Version)
	}
	name := man.Name
	// Stop the running copy before its files are replaced.
	m.unloadLocked(name)
	dest := filepath.Join(m.pdir(name), man.Version)
	if err := os.MkdirAll(m.pdir(name), 0o700); err != nil {
		return Installed{}, err
	}
	if err := os.RemoveAll(dest); err != nil {
		return Installed{}, err
	}
	if err := os.Rename(root, dest); err != nil {
		return Installed{}, err
	}
	rec := record{Name: name, Version: man.Version, Source: canon, SourceKind: kind, InstalledAt: time.Now().UTC(), Signed: vo.signed, Pinned: vo.integrity, Market: vo.name}
	if digest != nil {
		rec.Integrity = Integrity(digest)
	}
	if pub != nil {
		rec.Publisher = pub.ID
	}
	if err := writeJSON(filepath.Join(m.pdir(name), "installed.json"), rec); err != nil {
		return Installed{}, err
	}
	// keep only the current version
	if ents, err := os.ReadDir(m.pdir(name)); err == nil {
		for _, e := range ents {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && e.Name() != man.Version {
				_ = os.RemoveAll(filepath.Join(m.pdir(name), e.Name()))
			}
		}
	}
	if !keepEnabled {
		if err := m.setEnabled(name, enable); err != nil {
			return Installed{}, err
		}
	}
	serr := m.syncLocked(name, true)
	v, err := m.getLocked(name)
	if err != nil {
		return Installed{}, err
	}
	if serr != nil && v.Error == "" {
		v.Error = serr.Error()
	}
	return v, nil
}

// Approve records the user's grant. perms nil approves exactly what the
// manifest requests. A grant smaller than the request keeps the plugin
// PENDING-APPROVAL.
func (m *Manager) Approve(name string, perms *process.Permissions, trustFutureVersions bool) (Installed, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	in, err := m.read(name)
	if err != nil {
		return Installed{}, err
	}
	g := Grant{Version: in.man.Version, Permissions: in.man.Requested(), TrustFutureVersions: trustFutureVersions, ApprovedAt: time.Now().UTC()}
	if perms != nil {
		g.Permissions = perms.Normalize()
	}
	if err := writeJSON(filepath.Join(m.pdir(name), "grants.json"), g); err != nil {
		return Installed{}, err
	}
	serr := m.syncLocked(name, true)
	v, err := m.getLocked(name)
	if serr != nil && v.Error == "" {
		v.Error = serr.Error()
	}
	return v, err
}

// SetEnabled enables or disables a plugin.
func (m *Manager) SetEnabled(name string, on bool) (Installed, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.read(name); err != nil {
		return Installed{}, err
	}
	if err := m.setEnabled(name, on); err != nil {
		return Installed{}, err
	}
	serr := m.syncLocked(name, false)
	v, err := m.getLocked(name)
	if serr != nil && v.Error == "" {
		v.Error = serr.Error()
	}
	return v, err
}

// Remove uninstalls a plugin: its process tree is killed, every
// registration revoked and its files and grant deleted.
func (m *Manager) Remove(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.read(name); err != nil {
		return err
	}
	m.unloadLocked(name)
	if err := m.setEnabled(name, false); err != nil {
		return err
	}
	return os.RemoveAll(m.pdir(name))
}

// Reload restarts a plugin: dispose (all registrations revoked, process
// killed) and start again. Plugins installed from a local directory are
// re-synced from it first, so `crew dev` sees source changes; a changed
// version or permission set goes through approval again.
func (m *Manager) Reload(ctx context.Context, name string) (Installed, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	in, err := m.read(name)
	if err != nil {
		return Installed{}, err
	}
	if in.rec.SourceKind == "local" {
		if st, err := os.Stat(in.rec.Source); err == nil && st.IsDir() {
			return m.installLocked(ctx, in.rec.Source, false, true, verifyOpts{})
		}
	}
	serr := m.syncLocked(name, true)
	v, err := m.getLocked(name)
	if serr != nil && v.Error == "" {
		v.Error = serr.Error()
	}
	return v, err
}

func (m *Manager) startEnabled() {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0)
	for n := range m.enabledSet() {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := m.syncLocked(n, false); err != nil {
			m.c.Log().Warn("plugin not started", "plugin", n, "err", err)
		}
	}
}

func (m *Manager) unloadAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.loaded))
	for n := range m.loaded {
		names = append(names, n)
	}
	slices.Sort(names)
	for i := len(names) - 1; i >= 0; i-- {
		m.unloadLocked(names[i])
	}
}

// Plugin provides the `plugins` service and starts enabled, approved
// plugins. Config: {"dir": "${root}/plugins", "marketURL": "..."}.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/plugin-manager",
	Description: "Installs, approves and runs third-party process plugins (plugins service).",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		if cfg.Dir == "" {
			return errors.New("plugin-manager: config.dir is required")
		}
		if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
			return err
		}
		m := &Manager{c: c, h: c.Host(), cfg: cfg, loaded: map[string]bool{}}
		if err := c.Provide(ServiceKey, m); err != nil {
			return err
		}
		c.Defer(m.unloadAll)
		m.startEnabled()
		return nil
	},
})
