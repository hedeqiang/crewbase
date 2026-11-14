package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/egoist/mygo"
)

// prefs are the shell's own settings, kept in desktop.json of the user data
// directory (~/Library/Application Support/Crewbase on macOS). They are not
// the daemon's settings: those live in ${CREWBASE_HOME}.
type prefs struct {
	mu   sync.Mutex
	path string
	// KeepRunning: closing the window hides it and agents keep working
	// (docs/03 §5.7 "关闭窗口后继续运行"); off, closing the window quits.
	KeepRunning bool `json:"keepRunning"`
}

func loadPrefs() *prefs {
	p := &prefs{KeepRunning: true}
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		slog.Warn("desktop prefs: no user data dir", "err", err)
		return p
	}
	p.path = filepath.Join(dir, "desktop.json")
	if b, err := os.ReadFile(p.path); err == nil {
		_ = json.Unmarshal(b, p)
	}
	return p
}

func (p *prefs) keepRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.KeepRunning
}

func (p *prefs) setKeepRunning(v bool) {
	p.mu.Lock()
	p.KeepRunning = v
	b, _ := json.MarshalIndent(p, "", "  ")
	path := p.path
	p.mu.Unlock()
	if path == "" {
		return
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		slog.Warn("desktop prefs: save", "err", err)
	}
}
