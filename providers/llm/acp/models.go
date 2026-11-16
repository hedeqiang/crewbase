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
// there right after a restart. It is refreshed at most once a day, and on
// every real session.

// modelsFile is the cache of learned model lists, by agent id.
const modelsFile = "acp-models.json"

// probeEvery is how old a cached list may be before it is probed again.
var probeEvery = 24 * time.Hour

type modelCache struct {
	Models  []acpModel `json:"models"`
	Learned time.Time  `json:"learned"`
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

func writeModelCache(dir, id string, ms []acpModel, at time.Time) {
	if dir == "" || len(ms) == 0 {
		return
	}
	cacheMu.Lock()
	defer cacheMu.Unlock()
	all := readModelCache(dir)
	all[id] = modelCache{Models: ms, Learned: at.UTC()}
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
// reports whether it is fresh enough to skip probing.
func (a *Adapter) loadCachedModels() (fresh bool) {
	c, ok := readModelCache(a.stateDir)[a.cfg.ID]
	if !ok || len(c.Models) == 0 {
		return false
	}
	a.setModels(c.Models)
	return time.Since(c.Learned) < probeEvery
}

// ProbeModels learns the CLI's models by opening a session in a scratch
// directory. It starts the process (which then stops after the idle
// timeout like any other) and sends no prompt.
func (a *Adapter) ProbeModels(ctx context.Context) error {
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
	_, err = p.newSession(ctx, "", dir, true)
	return err
}
