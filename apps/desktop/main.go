// Command desktop is Crewbase.app, the desktop shell built with MyGo
// (github.com/egoist/mygo; pure Go, no cgo): a window on the daemon's web
// UI, a menu bar (tray) icon with what needs you, native notifications and
// the Dock badge.
//
// The shell attaches to a crewd already running on 127.0.0.1:7788 that
// accepts the token in ${CREWBASE_HOME:-~/.crewbase}/token; otherwise it
// runs the host in this process (profile "desktop"). Closing the window
// hides it and agents keep working; quitting (menu, tray, ⌘Q) closes the
// in-process host, which kills subprocess trees. See docs/desktop.md.
//
//	go run ./apps/desktop            # development
//	make desktop                     # Crewbase.app + .dmg in apps/desktop/build
package main

//go:generate go run ./internal/icongen -web ../web/public

import (
	_ "embed"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	"github.com/hedeqiang/crewbase/internal/loginpath"
	"github.com/hedeqiang/crewbase/kernel"
	// The UI is written in Chinese: MyGo's own menus, dialogs and roles
	// follow it (App.SetLanguages below).
	_ "github.com/egoist/mygo/locales/zh-Hans"
)

//go:embed assets/tray.png
var trayIcon []byte

const (
	appName    = "Crewbase"
	devVersion = "0.1.0-dev"
	// titleBarHeight is the web UI's draggable title strip (docs/03).
	titleBarHeight = 44
	// The traffic lights are 14 pt tall: their top at (44-14)/2 centres them.
	trafficLightX, trafficLightY = 16, 15
)

type shell struct {
	prefs *prefs

	mu      sync.Mutex
	d       *daemon
	win     *mygo.Window
	tray    *mygo.Tray
	needs   []need
	pending int // len(needs), for the badge

	webBadge atomic.Int64 // window.crewbaseDesktop.setBadge
	cu       *computerUI  // computer-use indicator, border and hotkey
	// updateInstalled: an update is staged; quitting must not be deferred
	// (see beforeQuit)
	updateInstalled atomic.Bool
	quitting        atomic.Bool // a quit started (OnBeforeQuit)
	hidden          bool        // opened at login: stay hidden until asked
}

func main() {
	// A second launch hands its arguments to the first and exits; the first
	// shows its window (OnSecondInstance).
	if !mygo.App.RequestSingleInstanceLock() {
		return
	}
	// Opened from Finder or the Dock, the app has the system's minimal PATH;
	// take the login shell's so Claude Code, Codex, Gemini CLI, npx… are
	// found (by the host in this process and everything it starts).
	loginpath.Fix()
	if mygo.App.Version() == "" { // go run / go build: no mygo.json linked in
		mygo.App.SetName(appName)
		mygo.App.SetVersion(devVersion)
	}
	// the in-process host (and its API) report the app's version
	kernel.Version = mygo.App.Version()
	mygo.App.SetLanguages(mygoLang())
	setupLogging()
	// Signed updates from the GitHub releases (mygo.json updates); builds
	// that cannot update themselves (go run, package managers) never check.
	// the update window follows the app's language (App.SetLanguages)
	mygo.Use(updater.Plugin)

	s := &shell{prefs: loadPrefs(), hidden: mygo.App.WasOpenedAtLogin()}
	mygo.Bind(Desktop{s})
	mygo.App.SetMenu(s.menuBar())
	s.setDockMenu()

	mygo.App.OnSecondInstance(func([]string, string) {
		slog.Info("desktop: second launch; showing the window")
		s.showWindow()
	})
	// A listener here keeps the app running when the window closes; the
	// window normally hides instead of closing (see newWindow).
	mygo.App.OnWindowAllClosed(func() {
		if !s.prefs.keepRunning() {
			mygo.App.Quit()
		}
	})
	mygo.App.OnActivate(func(hasVisibleWindows bool) { // Dock icon click
		if !hasVisibleWindows {
			s.showWindow()
		}
	})
	mygo.App.OnBeforeQuit(s.beforeQuit)
	updater.OnInstalled(func() { s.updateInstalled.Store(true) })
	mygo.App.OnQuit(func() {
		// Last resort (e.g. a second Ctrl+C): never leave agents running.
		if d := s.daemon(); d != nil {
			d.closeHost()
		}
	})
	mygo.App.WhenReady(s.ready)
	if err := mygo.App.Run(); err != nil {
		slog.Error("desktop: run", "err", err)
		os.Exit(1)
	}
}

func setupLogging() {
	var w io.Writer = os.Stderr
	if dir, err := mygo.App.Path(mygo.PathLogs); err == nil {
		if f, err := os.OpenFile(filepath.Join(dir, "desktop.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			w = io.MultiWriter(os.Stderr, f)
		}
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})))
	slog.Info("desktop: starting", "version", mygo.App.Version(), "pid", os.Getpid())
}

func (s *shell) daemon() *daemon {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.d
}

func (s *shell) window() *mygo.Window {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.win
}

// ready runs on the main thread once the app has launched.
func (s *shell) ready() {
	tray, err := mygo.NewTray(mygo.TrayOptions{
		ID:             "crewbase",
		Icon:           trayIcon,
		IconIsTemplate: true,
		ToolTip:        "Crewbase",
	})
	if err != nil {
		slog.Warn("desktop: tray", "err", err) // e.g. Linux without AppIndicator
	} else {
		s.mu.Lock()
		s.tray = tray
		s.mu.Unlock()
		s.updateTray()
		go func() { // once the menu bar has laid the item out
			time.Sleep(2 * time.Second)
			slog.Info("desktop: tray ready", "bounds", tray.Bounds())
		}()
	}
	// Starting the host can take a moment (plugins, MCP connections): off
	// the main thread, which keeps drawing the menu bar and the Dock.
	go s.boot()
}

func (s *shell) boot() {
	d, err := startDaemon()
	if err != nil {
		slog.Error("desktop: daemon", "err", err)
		_, _ = mygo.Dialog.Message(mygo.MessageOptions{
			Type:    mygo.MessageError,
			Message: "Crewbase 无法启动",
			Detail:  err.Error(),
		})
		mygo.App.Quit()
		return
	}
	s.mu.Lock()
	s.d = d
	s.mu.Unlock()
	mygo.RunOnMain(s.newWindow)
	go s.watchNeeds(d)
	go s.showNotifications(d)
	s.watchComputer(d)
}

// newWindow runs on the main thread, so its listeners are in place before
// the page can be ready.
func (s *shell) newWindow() {
	d := s.daemon()
	origin := d.baseURL()
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:                appName,
		URL:                  origin + "/?token=" + url.QueryEscape(d.token),
		Width:                1200,
		Height:               780,
		MinWidth:             820,
		MinHeight:            560,
		StateKey:             "main",
		Hidden:               true,
		TitleBarStyle:        mygo.TitleBarHidden,
		TitleBarHeight:       titleBarHeight,
		TrafficLightPosition: &mygo.Point{X: trafficLightX, Y: trafficLightY},
		BackgroundColor:      "light-dark(#F5F5F6, #1A1A1A)",
		Page: mygo.PageOptions{
			// The page is served by the daemon over loopback http, not from
			// mygo://: trust that origin so it may call the Desktop service.
			TrustedOrigins: []string{origin},
			PreloadScript:  preloadScript(origin, mygo.App.Version()),
			// SSE and timers keep running while the window is hidden.
			BackgroundThrottling: mygo.ThrottlingOff,
		},
	})
	s.mu.Lock()
	s.win = win
	s.mu.Unlock()

	win.OnReadyToShow(func() {
		if !s.hidden {
			win.Show()
		}
	})
	win.OnClose(func(e *mygo.CloseEvent) {
		if s.quitting.Load() || !s.prefs.keepRunning() {
			return // closes; OnWindowAllClosed quits when keepRunning is off
		}
		e.PreventDefault()
		win.Hide()
	})
	page := win.Page()
	// target=_blank and window.open: http(s) to the system browser.
	page.SetWindowOpenHandler(func(req mygo.WindowOpenRequest) *mygo.WindowOptions {
		if u, err := url.Parse(req.URL); err == nil && (u.Scheme == "http" || u.Scheme == "https" || u.Scheme == "mailto") {
			go mygo.Shell.OpenExternal(req.URL)
		}
		return nil
	})
	// Links to other origins leave the app for the browser; the window
	// stays on the daemon's UI.
	page.OnWillNavigate(func(e *mygo.NavigateEvent) {
		u, err := url.Parse(e.URL)
		if err != nil || u.Scheme == "about" || u.Scheme == "data" {
			return
		}
		if (u.Scheme == "http" || u.Scheme == "https") && u.Scheme+"://"+u.Host == origin {
			return
		}
		e.PreventDefault()
		if u.Scheme == "http" || u.Scheme == "https" || u.Scheme == "mailto" {
			go mygo.Shell.OpenExternal(e.URL)
		}
	})
}

// showWindow brings the window back (Dock click, tray, second launch).
func (s *shell) showWindow() {
	s.hidden = false
	win := s.window()
	if win == nil {
		return // still starting; it shows when ready
	}
	if win.IsMinimized() {
		win.Restore()
	}
	win.Show()
	win.Focus()
	mygo.App.Focus()
}

// navigate shows the window and points the single-page app at a view by
// its hash, without reloading it. The web UI does not route hashes yet
// (see docs/desktop.md): until it does, this only shows the window.
func (s *shell) navigate(hash string) {
	s.showWindow()
	win := s.window()
	if win == nil || hash == "" {
		return
	}
	go func() {
		code := fmt.Sprintf("location.hash = %q; return true", hash)
		if _, err := win.Page().Eval(code); err != nil {
			slog.Warn("desktop: navigate", "hash", hash, "err", err)
		}
	}()
}

func (s *shell) openSession(id string) {
	if id == "" {
		s.navigate("")
		return
	}
	s.navigate("#/session/" + url.PathEscape(id))
}

func (s *shell) openSettings() { s.navigate("#/settings") }

// beforeQuit stops the in-process host before the app exits. Close kills
// the agents' subprocess trees, which may take a few seconds, so it runs
// off the main thread: the quit is held, the window hidden, and the app
// quits again once the host is down.
func (s *shell) beforeQuit(e *mygo.QuitEvent) {
	s.quitting.Store(true)
	d := s.daemon()
	if d == nil || d.attached() || !d.stopping.CompareAndSwap(false, true) {
		return // nothing to stop, or already stopping: let the quit go on
	}
	// logout/shutdown must finish synchronously. So must a quit after an
	// update was installed: the updater's "Relaunch Now" quits through
	// here, and a quit that is prevented (to stop agents in the background)
	// drops the relaunch, so the app quit and never came back.
	if e.SessionEnding || s.updateInstalled.Load() {
		if win := s.window(); win != nil {
			win.Hide()
		}
		closeWithin(d, 15*time.Second)
		return
	}
	e.PreventDefault()
	if win := s.window(); win != nil {
		win.Hide()
	}
	go func() {
		closeWithin(d, 15*time.Second)
		mygo.App.Quit()
	}()
}

// closeWithin stops the in-process host, giving up after d.
func closeWithin(dm *daemon, d time.Duration) {
	done := make(chan struct{})
	go func() { dm.closeHost(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		slog.Warn("desktop: host close timed out; quitting anyway")
	}
}

func (s *shell) setDockMenu() {
	mygo.App.Dock.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
		{Label: fmt.Sprintf(tr("open"), appName), Click: func(*mygo.MenuItem, *mygo.Window) { s.showWindow() }},
	}))
}

func getenv(k string) string { return os.Getenv(k) }
