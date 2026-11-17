package main

import (
	"encoding/json"
	"errors"
	"net/url"
	"runtime"

	"github.com/egoist/mygo"
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

// UpdateState reports the update state; the "crewbase:update" event
// (crewbaseDesktop.onUpdate) carries every change of it.
func (d Desktop) UpdateState() updateState { return d.s.upd.state() }

// CheckForUpdates checks now, in place: the sidebar and Settings show how
// it goes. A version found downloads when automatic downloads are on.
func (d Desktop) CheckForUpdates() { go d.s.upd.check(true) }

// DownloadUpdate downloads and installs the version found (automatic
// downloads off), or tries again what failed.
func (d Desktop) DownloadUpdate() { go d.s.upd.retry() }

// RestartToUpdate relaunches into the update once it is in place; agents
// are stopped first (beforeQuit).
func (d Desktop) RestartToUpdate() error { return d.s.upd.restart() }

// SetAutoUpdate turns downloading and installing in the background on or off.
func (d Desktop) SetAutoUpdate(on bool) updateState { return d.s.upd.setAutoDownload(on) }

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
// setBadge(n), pickFolder(start), updateState(), onUpdate(fn) → off,
// checkForUpdates(), downloadUpdate(), restartToUpdate(),
// setAutoUpdate(on), setLanguage(l), keepRunning(),
// setKeepRunning(on), permissions(), requestPermission(id),
// openPermissionSettings(id) }; it calls the Desktop service through window.mygo, which
// MyGo injects before preload scripts run.
func preloadScript(origin, version string) string {
	o, _ := json.Marshal(origin)
	v, _ := json.Marshal(version)
	p, _ := json.Marshal(runtime.GOOS)
	ev, _ := json.Marshal(updateEvent.Name())
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
      updateState: () => window.mygo.call("Desktop.UpdateState"),
      onUpdate: (fn) => {
        if (typeof fn !== "function") return () => {};
        return window.mygo.on(` + string(ev) + `, (st) => { try { fn(st); } catch (e) { console.warn("crewbaseDesktop.onUpdate", e); } });
      },
      checkForUpdates: () => { call("CheckForUpdates"); },
      downloadUpdate: () => { call("DownloadUpdate"); },
      restartToUpdate: () => window.mygo.call("Desktop.RestartToUpdate"),
      setAutoUpdate: (on) => window.mygo.call("Desktop.SetAutoUpdate", !!on),
      setLanguage: (l) => { call("SetLanguage", String(l)); },
      keepRunning: () => window.mygo.call("Desktop.KeepRunning"),
      setKeepRunning: (on) => window.mygo.call("Desktop.SetKeepRunning", !!on),
      permissions: () => window.mygo.call("Desktop.Permissions"),
      requestPermission: (id) => window.mygo.call("Desktop.RequestPermission", String(id)),
      openPermissionSettings: (id) => { call("OpenPermissionSettings", String(id)); },
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
