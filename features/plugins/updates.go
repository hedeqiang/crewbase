package plugins

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// PluginUpdate is a marketplace version newer than the installed one.
type PluginUpdate struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Current     string `json:"current"`
	Latest      string `json:"latest"`
	// NewPermissions are what the new version asks for beyond the grant:
	// updating keeps the plugin stopped until they are approved.
	NewPermissions []string `json:"newPermissions,omitempty"`
}

// Updates lists installed plugins with a newer marketplace version.
func (m *Manager) Updates(ctx context.Context) ([]PluginUpdate, error) {
	ents, err := m.Market(ctx)
	if err != nil {
		return nil, err
	}
	inst := map[string]Installed{}
	for _, p := range m.Installed() {
		inst[p.Name] = p
	}
	out := []PluginUpdate{}
	for _, e := range ents {
		p, ok := inst[e.Name]
		// only what came from the marketplace: a plugin of the same name
		// installed from a folder or another URL is the person's own
		if !ok || !fromMarket(p, e) || !VersionNewer(e.Version, p.Version) {
			continue
		}
		u := PluginUpdate{Name: e.Name, Description: e.Description, Current: p.Version, Latest: e.Version}
		if p.Grant == nil {
			u.NewPermissions = e.Permissions.Normalize().Strings()
		} else if !p.Grant.TrustFutureVersions {
			u.NewPermissions = p.Grant.Permissions.Missing(e.Permissions)
		}
		out = append(out, u)
	}
	return out, nil
}

// Update installs the marketplace's version of an installed plugin,
// verified like a first install; enabled stays as it was, and the grant
// decides whether it starts.
func (m *Manager) Update(ctx context.Context, name string) (Installed, error) {
	cur, err := m.Get(name)
	if err != nil {
		return Installed{}, err
	}
	ents, err := m.Market(ctx)
	if err != nil {
		return Installed{}, err
	}
	for _, e := range ents {
		if e.Name != name {
			continue
		}
		if !VersionNewer(e.Version, cur.Version) {
			return cur, nil
		}
		m.mu.Lock()
		v, err := m.installLocked(ctx, e.Source, cur.Enabled, true, verifyOpts{name: e.Name, integrity: e.Integrity, signed: e.Signed})
		m.mu.Unlock()
		if err != nil || v.Approved || cur.Grant == nil {
			return v, err
		}
		// asking for nothing beyond what was granted: the person's 更新 is the
		// approval of this version (more permissions wait for 批准)
		if cur.Grant.Permissions.Covers(v.Requested) {
			req := v.Requested
			return m.Approve(name, &req, cur.Grant.TrustFutureVersions)
		}
		return v, nil
	}
	return Installed{}, fmt.Errorf("%w: %q is not in the marketplace", ErrBadSource, name)
}

// VersionNewer reports whether version a is later than b (semver-ish:
// numbers compared part by part; a pre-release is older than its release).
func VersionNewer(a, b string) bool {
	na, pa := splitVersion(a)
	nb, pb := splitVersion(b)
	for i := 0; i < max(len(na), len(nb)); i++ {
		var x, y int
		if i < len(na) {
			x = na[i]
		}
		if i < len(nb) {
			y = nb[i]
		}
		if x != y {
			return x > y
		}
	}
	return pa == "" && pb != "" // 1.0.0 > 1.0.0-rc1
}

func splitVersion(v string) ([]int, string) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	pre := ""
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v, pre = v[:i], v[i+1:]
	}
	var out []int
	for _, p := range strings.Split(v, ".") {
		n, _ := strconv.Atoi(p)
		out = append(out, n)
	}
	return out, pre
}

// fromMarket: p was installed from marketplace entry e (recorded since
// updates exist; earlier installs match by their source).
func fromMarket(p Installed, e MarketEntry) bool {
	return p.Market == e.Name || p.Source == e.Source
}
