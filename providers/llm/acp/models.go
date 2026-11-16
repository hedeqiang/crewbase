package acp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Model discovery. A CLI reports its models only in the session/new
// result, so before the first turn the picker showed just "default". When
// a CLI is registered the plugin opens one session in an empty scratch
// directory (initialize + session/new: no prompt, no model call, no quota)
// to learn them, and keeps the list in <stateDir>/acp-models.json so it is
// there right after a restart. It is refreshed at most once a day, on
// every real session, and whenever the CLI or its ACP bridge changes
// version (a list learned from an old bridge is not kept).
//
// Some CLIs know their models better than their bridge: Codex lists them
// with `codex debug models` (see codex.go), which is preferred over what
// codex-acp reports. How the raw lists become Crewbase models (names,
// variants folded into reasoning levels) is in shape.go.

// modelsFile is the cache of learned model lists, by agent id.
const modelsFile = "acp-models.json"

// probeEvery is how old a cached list may be before it is probed again.
var probeEvery = 24 * time.Hour

type modelCache struct {
	// Models are what the bridge reported (session/new).
	Models []acpModel `json:"models"`
	// Native is the CLI's own list (codex debug models), when it has one.
	Native  []nativeModel `json:"native,omitempty"`
	Learned time.Time     `json:"learned"`
	// Versions are the CLI and bridge versions the list was learned with.
	Versions string `json:"versions,omitempty"`
}

var cacheMu sync.Mutex

func readModelCache(dir string) map[string]modelCache {
	out := map[string]modelCache{}
	if dir == "" {
		return out
	}
	b, err := os.ReadFile(filepath.Join(dir, modelsFile))
	if err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

func writeModelCache(dir, id string, c modelCache) {
	if dir == "" || (len(c.Models) == 0 && len(c.Native) == 0) {
		return
	}
	cacheMu.Lock()
	defer cacheMu.Unlock()
	all := readModelCache(dir)
	c.Learned = c.Learned.UTC()
	all[id] = c
	b, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return
	}
	tmp := filepath.Join(dir, modelsFile+".tmp")
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, filepath.Join(dir, modelsFile))
	}
}

// loadCachedModels fills the adapter's model list from the cache and
// reports whether it is fresh enough to skip probing: learned less than
// probeEvery ago, with the CLI and bridge versions the adapter runs now
// (when those are known; see SetVersions).
func (a *Adapter) loadCachedModels() (fresh bool) {
	c, ok := readModelCache(a.stateDir)[a.cfg.ID]
	if !ok || (len(c.Models) == 0 && len(c.Native) == 0) {
		return false
	}
	a.mu.Lock()
	a.acpModels, a.native = c.Models, c.Native
	a.learned, a.learnedWith = c.Learned, c.Versions
	a.mu.Unlock()
	a.reshape()
	return a.cacheFresh()
}

// cacheFresh reports whether the models in memory need no new probe.
func (a *Adapter) cacheFresh() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.acpModels) == 0 && len(a.native) == 0 {
		return false
	}
	if a.versions != "" && a.learnedWith != a.versions {
		return false
	}
	return time.Since(a.learned) < probeEvery
}

// SetVersions records the CLI and bridge versions the adapter runs ("cli
// <v> | bridge <v>"); a model list learned with others is stale.
func (a *Adapter) SetVersions(v string) {
	a.mu.Lock()
	a.versions = v
	a.mu.Unlock()
}

// saveModels writes what is known now to the cache.
func (a *Adapter) saveModels() {
	a.mu.Lock()
	c := modelCache{Models: a.acpModels, Native: a.native, Learned: a.learned, Versions: a.versions}
	a.learnedWith = a.versions
	a.mu.Unlock()
	writeModelCache(a.stateDir, a.cfg.ID, c)
}

// ProbeModels learns the CLI's models: its own list when it has one (see
// nativeLister), and the bridge's by opening a session in a scratch
// directory. It starts the process (which then stops after the idle
// timeout like any other) and sends no prompt.
func (a *Adapter) ProbeModels(ctx context.Context) error {
	if ls := nativeLister(a.cfg); ls != nil {
		if ms, err := ls(ctx); err == nil && len(ms) > 0 {
			a.mu.Lock()
			a.native, a.learned = ms, time.Now()
			a.mu.Unlock()
			a.reshape()
			a.saveModels()
		} else if err != nil {
			a.log.Debug("acp: CLI model list not read; using the bridge's", "agent", a.cfg.ID, "err", err)
		}
	}
	dir, err := os.MkdirTemp("", "crewbase-acp-probe-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	p, err := a.ensureProc(ctx)
	if err != nil {
		return err
	}
	// own: Claude Code loads user settings only, nothing from the scratch dir
	s, err := p.newSession(ctx, "", "", dir, true, nil)
	if err == nil {
		p.dropSession(s)
	}
	return err
}
