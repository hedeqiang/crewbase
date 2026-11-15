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
		Parent: d.s.window(), Title: "选择项目文件夹", ButtonLabel: "选择",
		Message:     "Agent 会在这个文件夹里读写文件、运行命令（按它的运行权限）。",
		DefaultPath: start, Directory: true, CreateDirectories: true,
	})
	if err != nil || len(paths) == 0 {
		return "", err
	}
	return paths[0], nil
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
// setBadge(n), pickFolder(start) }; it calls the Desktop service through window.mygo, which
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
    }),
  });
})();`
}
