package process

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/hedeqiang/crewbase/sdk"
)

// ManifestFile is the manifest file name in a plugin directory.
const ManifestFile = "crewbase-plugin.toml"

// Host services a process plugin may declare in [inject] services. Each one
// is both a dependency (the plugin stays PENDING without it) and a
// permission (what the plugin may register on or call).
var KnownServices = []string{"tools", "prompt", "notify", "credentials"}

// ExposedEvents is the whitelist of events external plugins may listen to.
var ExposedEvents = []string{"tools/result", "tools/pre-execute", "tools/post-execute"}

// Permissions is a permission set: what a manifest requests, or what a user
// approved. Network, files and subprocess are enforced only when the plugin
// runs in a sandbox (see Status.Enforcement).
type Permissions struct {
	Services    []string `toml:"services" json:"services,omitempty"`
	Events      []string `toml:"events" json:"events,omitempty"`
	Network     []string `toml:"network" json:"network,omitempty"`
	Credentials []string `toml:"credentials" json:"credentials,omitempty"`
	Files       []string `toml:"files" json:"files,omitempty"`
	Subprocess  bool     `toml:"subprocess" json:"subprocess,omitempty"`
}

// Manifest is a parsed crewbase-plugin.toml.
//
//	name = "word-count"
//	version = "0.1.0"
//	description = "..."
//	runtime = "process"            # or: [runtime] kind = "process", sandbox = true
//	entry = ["node", "index.js"]
//
//	[inject]
//	services = ["tools"]
//
//	[permissions]
//	events = ["tools/result"]
//	network = ["api.example.com"]
//	credentials = ["mem:token"]
//	files = ["/abs/path"]
//	subprocess = false
type Manifest struct {
	Name        string      `json:"name"`
	Version     string      `json:"version"`
	Description string      `json:"description,omitempty"`
	Author      string      `json:"author,omitempty"`
	Repo        string      `json:"repo,omitempty"`
	Runtime     string      `json:"runtime"`
	Sandbox     bool        `json:"sandbox,omitempty"`
	Entry       []string    `json:"entry"`
	Services    []string    `json:"services,omitempty"` // [inject] services
	Permissions Permissions `json:"permissions"`
}

type rawManifest struct {
	Name        string   `toml:"name"`
	Version     string   `toml:"version"`
	Description string   `toml:"description"`
	Author      string   `toml:"author"`
	Repo        string   `toml:"repo"`
	Runtime     any      `toml:"runtime"`
	Entry       []string `toml:"entry"`
	Inject      struct {
		Services []string `toml:"services"`
	} `toml:"inject"`
	Permissions Permissions `toml:"permissions"`
}

// ParseManifest parses and validates manifest text.
func ParseManifest(data []byte) (Manifest, error) {
	var raw rawManifest
	md, err := toml.Decode(string(data), &raw)
	if err != nil {
		return Manifest{}, fmt.Errorf("manifest: %w", err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		keys := make([]string, len(und))
		for i, k := range und {
			keys[i] = k.String()
		}
		// Unknown permission keys must not be silently dropped: the user
		// would approve less than the plugin thinks it asked for.
		for _, k := range keys {
			if strings.HasPrefix(k, "permissions.") || strings.HasPrefix(k, "inject.") {
				return Manifest{}, fmt.Errorf("manifest: unknown key %s", k)
			}
		}
	}
	m := Manifest{Name: raw.Name, Version: raw.Version, Description: raw.Description, Author: raw.Author,
		Repo: raw.Repo, Entry: raw.Entry, Services: raw.Inject.Services, Permissions: raw.Permissions}
	switch rt := raw.Runtime.(type) {
	case nil:
		m.Runtime = RuntimeName
	case string:
		m.Runtime = rt
	case map[string]any:
		m.Runtime, _ = rt["kind"].(string)
		if m.Runtime == "" {
			m.Runtime = RuntimeName
		}
		m.Sandbox, _ = rt["sandbox"].(bool)
		if e, ok := rt["entry"].([]any); ok && len(m.Entry) == 0 {
			for _, x := range e {
				if s, ok := x.(string); ok {
					m.Entry = append(m.Entry, s)
				}
			}
		}
	default:
		return Manifest{}, errors.New("manifest: runtime must be a string or a table")
	}
	return m, m.validate()
}

func (m Manifest) validate() error {
	if !sdk.ValidName(m.Name) {
		return fmt.Errorf("manifest: invalid name %q (lowercase letters, digits, . _ -)", m.Name)
	}
	if m.Version == "" || strings.ContainsAny(m.Version, "/\\ ") || m.Version == "." || m.Version == ".." {
		return fmt.Errorf("manifest: invalid version %q", m.Version)
	}
	if m.Runtime != RuntimeName {
		return fmt.Errorf("manifest: unsupported runtime %q (want %q)", m.Runtime, RuntimeName)
	}
	if len(m.Entry) == 0 || m.Entry[0] == "" {
		return errors.New("manifest: entry is required, e.g. entry = [\"node\", \"index.js\"]")
	}
	for _, s := range m.Requested().Services {
		if !slices.Contains(KnownServices, s) {
			return fmt.Errorf("manifest: unknown service %q (known: %s)", s, strings.Join(KnownServices, ", "))
		}
	}
	for _, e := range m.Permissions.Events {
		if !slices.Contains(ExposedEvents, e) {
			return fmt.Errorf("manifest: event %q is not exposed to external plugins (exposed: %s)", e, strings.Join(ExposedEvents, ", "))
		}
	}
	for _, f := range m.Permissions.Files {
		if !filepath.IsAbs(f) {
			return fmt.Errorf("manifest: file permission must be an absolute path: %q", f)
		}
	}
	return nil
}

// ReadManifest reads dir/crewbase-plugin.toml.
func ReadManifest(dir string) (Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		return Manifest{}, err
	}
	return ParseManifest(b)
}

// Requested is the full permission set the manifest asks for: [permissions]
// plus [inject] services.
func (m Manifest) Requested() Permissions {
	p := m.Permissions
	p.Services = append(append([]string(nil), m.Services...), p.Services...)
	return p.Normalize()
}

func uniq(xs []string) []string {
	var out []string
	for _, x := range xs {
		x = strings.TrimSpace(x)
		if x != "" && !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

// Normalize dedupes and sorts every list.
func (p Permissions) Normalize() Permissions {
	return Permissions{Services: uniq(p.Services), Events: uniq(p.Events), Network: uniq(p.Network),
		Credentials: uniq(p.Credentials), Files: uniq(p.Files), Subprocess: p.Subprocess}
}

// Strings renders the set as entry permission strings: service:x, event:x,
// network:host, credentials:ref, files:/path, subprocess.
func (p Permissions) Strings() []string {
	p = p.Normalize()
	var out []string
	add := func(prefix string, xs []string) {
		for _, x := range xs {
			out = append(out, prefix+":"+x)
		}
	}
	add("service", p.Services)
	add("event", p.Events)
	add("network", p.Network)
	add("credentials", p.Credentials)
	add("files", p.Files)
	if p.Subprocess {
		out = append(out, "subprocess")
	}
	return out
}

// ParsePermissions parses entry permission strings (see Strings).
func ParsePermissions(ss []string) (Permissions, error) {
	var p Permissions
	for _, s := range ss {
		if s == "subprocess" {
			p.Subprocess = true
			continue
		}
		k, v, ok := strings.Cut(s, ":")
		if !ok || v == "" {
			return Permissions{}, fmt.Errorf("invalid permission %q", s)
		}
		switch k {
		case "service", "services":
			p.Services = append(p.Services, v)
		case "event", "events":
			p.Events = append(p.Events, v)
		case "network":
			p.Network = append(p.Network, v)
		case "credentials", "credential":
			p.Credentials = append(p.Credentials, v)
		case "files", "file":
			p.Files = append(p.Files, v)
		default:
			return Permissions{}, fmt.Errorf("invalid permission %q", s)
		}
	}
	return p.Normalize(), nil
}

// Missing lists the permissions in want that p does not grant.
func (p Permissions) Missing(want Permissions) []string {
	have := p.Strings()
	var out []string
	for _, s := range want.Strings() {
		if !slices.Contains(have, s) {
			out = append(out, s)
		}
	}
	return out
}

// Covers reports whether p grants everything in want.
func (p Permissions) Covers(want Permissions) bool { return len(p.Missing(want)) == 0 }

// Intersect keeps what is both granted (p) and requested (want): a plugin
// only reaches what it declared and the user approved.
func (p Permissions) Intersect(want Permissions) Permissions {
	have := p.Strings()
	var keep []string
	for _, s := range want.Strings() {
		if slices.Contains(have, s) {
			keep = append(keep, s)
		}
	}
	out, _ := ParsePermissions(keep)
	return out
}

// HasService reports whether the set includes service s.
func (p Permissions) HasService(s string) bool { return slices.Contains(p.Services, s) }

// HasEvent reports whether the set includes event e.
func (p Permissions) HasEvent(e string) bool { return slices.Contains(p.Events, e) }
