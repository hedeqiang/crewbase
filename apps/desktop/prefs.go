package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

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
	// AutoUpdate: new versions download and install in the background
	// (Settings › 关于 "自动下载并安装更新"); nil until chosen, which is on.
	AutoUpdate *bool `json:"autoUpdate,omitempty"`
	// LastUpdateCheck is when a check for updates last succeeded.
	LastUpdateCheck time.Time `json:"lastUpdateCheck,omitzero"`
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
	p.change(func() { p.KeepRunning = v })
}

func (p *prefs) autoUpdate() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.AutoUpdate == nil || *p.AutoUpdate
}

func (p *prefs) setAutoUpdate(v bool) {
	p.change(func() { p.AutoUpdate = &v })
}

func (p *prefs) lastUpdateCheck() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.LastUpdateCheck
}

func (p *prefs) setLastUpdateCheck(t time.Time) {
	p.change(func() { p.LastUpdateCheck = t.UTC().Truncate(time.Second) })
}

// change applies fn and saves the file.
func (p *prefs) change(fn func()) {
	p.mu.Lock()
	fn()
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
