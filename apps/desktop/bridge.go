package main

import (
	"encoding/json"
	"errors"
	"net/url"
	"runtime"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
)

// Desktop is the Go side of window.crewbaseDesktop. It is bound with
// mygo.Bind, so only trusted pages (the daemon's origin, listed in
// PageOptions.TrustedOrigins) can call it. Each call runs on its own
// goroutine.
type Desktop struct{ s *shell }

// Quit quits the app (as the menu's Quit: in-process agents stop with it).
func (d Desktop) Quit() { mygo.App.Quit() }

// OpenExternal opens an http(s) or mailto URL in the system's default app.
func (d Desktop) OpenExternal(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	switch u.Scheme {
	case "http", "https", "mailto":
		return mygo.Shell.OpenExternal(u.String())
	}
	return errors.New("only http, https and mailto URLs open outside the app")
}

// PickFolder shows the system's folder chooser (a sheet on the window) and
// returns the chosen folder, "" when cancelled. start is where it opens.
func (d Desktop) PickFolder(start string) (string, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent: d.s.window(), Title: tr("pickFolder"), ButtonLabel: tr("pick"),
		Message:     tr("pickFolderMsg"),
		DefaultPath: start, Directory: true, CreateDirectories: true,
	})
	if err != nil || len(paths) == 0 {
		return "", err
	}
	return paths[0], nil
}

// UpdateStatus is what 设置 › 通用 shows about updates.
type UpdateStatus struct {
	Version            string `json:"version"`
	Enabled            bool   `json:"enabled"` // this build can update itself
	AutomaticChecks    bool   `json:"automaticChecks"`
	AutomaticDownloads bool   `json:"automaticDownloads"`
	LastCheck          string `json:"lastCheck,omitempty"` // RFC 3339
	Available          string `json:"available,omitempty"` // a newer version found
}

// Updates reports the update state.
func (d Desktop) Updates() UpdateStatus {
	st := UpdateStatus{Version: mygo.App.Version(), Enabled: mygo.Updater.Enabled(),
		AutomaticChecks: updater.AutomaticChecks(), AutomaticDownloads: updater.AutomaticDownloads(), Available: updater.AvailableVersion()}
	if t := updater.LastCheck(); !t.IsZero() {
		st.LastCheck = t.UTC().Format(time.RFC3339)
	}
	return st
}

// CheckForUpdates checks now, showing the update window (also when up to date).
func (d Desktop) CheckForUpdates() { updater.CheckForUpdates() }

// SetUpdatePrefs changes the background check and download preferences.
func (d Desktop) SetUpdatePrefs(checks, downloads bool) UpdateStatus {
	updater.SetAutomaticChecks(checks)
	updater.SetAutomaticDownloads(downloads)
	return d.Updates()
}

// SetLanguage switches the shell's menus, tray and dialogs to the UI's
// language ("zh" | "en").
func (d Desktop) SetLanguage(l string) {
	if l != "zh" && l != "en" {
		return
	}
	if prev, _ := uiLang.Load().(string); prev == l {
		return
	}
	uiLang.Store(l)
	mygo.App.SetLanguages(mygoLang())
	mygo.App.SetMenu(d.s.menuBar())
	d.s.setDockMenu()
	d.s.updateTray()
}

// SetBadge sets the page's count for the Dock badge. The badge shows the
// larger of it and the shell's own pending count (approvals + takeovers),
// so the page cannot hide a waiting approval by setting 0.
func (d Desktop) SetBadge(n int) {
	d.s.webBadge.Store(int64(max(n, 0)))
	d.s.refreshBadge()
}

// preloadScript defines window.crewbaseDesktop in pages of origin. The
// surface is exactly { version, platform, quit(), openExternal(url),
// setBadge(n), pickFolder(start), updates(), checkForUpdates(),
// setUpdatePrefs(checks, downloads) }; it calls the Desktop service through window.mygo, which
// MyGo injects before preload scripts run.
func preloadScript(origin, version string) string {
	o, _ := json.Marshal(origin)
	v, _ := json.Marshal(version)
	p, _ := json.Marshal(runtime.GOOS)
	return `(() => {
  if (window.top !== window || location.origin !== ` + string(o) + ` || !window.mygo) return;
  const call = (m, ...a) => window.mygo.call("Desktop." + m, ...a).catch((e) => console.warn("crewbaseDesktop." + m, e));
  Object.defineProperty(window, "crewbaseDesktop", {
    configurable: false, enumerable: false, writable: false,
    value: Object.freeze({
      version: ` + string(v) + `,
      platform: ` + string(p) + `,
      quit: () => { call("Quit"); },
      openExternal: (url) => { call("OpenExternal", String(url)); },
      setBadge: (n) => { call("SetBadge", Math.max(0, Math.floor(Number(n) || 0))); },
      pickFolder: (start) => window.mygo.call("Desktop.PickFolder", String(start || "")),
      updates: () => window.mygo.call("Desktop.Updates"),
      checkForUpdates: () => { call("CheckForUpdates"); },
      setUpdatePrefs: (checks, downloads) => window.mygo.call("Desktop.SetUpdatePrefs", !!checks, !!downloads),
      setLanguage: (l) => { call("SetLanguage", String(l)); },
    }),
  });
})();`
}

// KeepRunning reports whether closing the window keeps the app (and its
// agents) running; Settings › 通用 shows it.
func (d Desktop) KeepRunning() bool { return d.s.prefs.keepRunning() }

// SetKeepRunning changes it (the tray menu's checkbox follows).
func (d Desktop) SetKeepRunning(on bool) bool {
	d.s.prefs.setKeepRunning(on)
	d.s.updateTray()
	return d.s.prefs.keepRunning()
}
