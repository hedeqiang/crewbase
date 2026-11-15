package main

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"slices"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	"github.com/hedeqiang/crewbase/capabilities/notify"
)

// maxNeedItems caps the "需要你" submenu; the rest are in the app.
const maxNeedItems = 12

// menuBar is the app's menu bar. On macOS it has the app menu, Edit, View
// and Window; Linux and Windows show it in the window (hidden under the
// custom title bar there, its shortcuts still work).
func (s *shell) menuBar() *mygo.Menu {
	view := []*mygo.MenuItem{
		{Role: mygo.RoleReload},
		{Role: mygo.RoleForceReload},
	}
	if mygo.IsDev() {
		view = append(view, &mygo.MenuItem{Role: mygo.RoleToggleDevTools})
	}
	view = append(view,
		mygo.Separator(),
		&mygo.MenuItem{Role: mygo.RoleResetZoom},
		&mygo.MenuItem{Role: mygo.RoleZoomIn},
		&mygo.MenuItem{Role: mygo.RoleZoomOut},
		mygo.Separator(),
		&mygo.MenuItem{Role: mygo.RoleToggleFullScreen},
	)
	return mygo.NewMenu([]*mygo.MenuItem{
		{Label: appName, Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleAbout, Label: fmt.Sprintf(tr("about"), appName)},
			updater.MenuItem(),
			mygo.Separator(),
			{Label: tr("settings"), Accelerator: "CmdOrCtrl+,", Click: func(*mygo.MenuItem, *mygo.Window) { s.openSettings() }},
			mygo.Separator(),
			{Role: mygo.RoleServices},
			mygo.Separator(),
			{Role: mygo.RoleHide, Label: fmt.Sprintf(tr("hide"), appName)},
			{Role: mygo.RoleHideOthers},
			{Role: mygo.RoleUnhide},
			mygo.Separator(),
			{Role: mygo.RoleQuit, Label: fmt.Sprintf(tr("quit"), appName)},
		}},
		{Role: mygo.RoleEditMenu},
		{Label: tr("view"), Submenu: view},
		// RoleWindow keeps macOS's list of windows; Close (⌘W) is here
		// because the app has no File menu. Closing hides the window.
		{Role: mygo.RoleWindow, Label: tr("window"), Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleMinimize},
			{Role: mygo.RoleZoom},
			mygo.Separator(),
			{Role: mygo.RoleClose},
			mygo.Separator(),
			{Role: mygo.RoleFront},
		}},
	})
}

// trayMenu is rebuilt whenever what needs you changes.
func (s *shell) trayMenu(needs []need) *mygo.Menu {
	var sub []*mygo.MenuItem
	for i, n := range needs {
		if i == maxNeedItems {
			sub = append(sub, mygo.Separator(), &mygo.MenuItem{
				Label: fmt.Sprintf(tr("needsMore"), len(needs)-maxNeedItems),
				Click: func(*mygo.MenuItem, *mygo.Window) { s.showWindow() },
			})
			break
		}
		session := n.Session
		sub = append(sub, &mygo.MenuItem{
			Label: n.Label,
			Click: func(*mygo.MenuItem, *mygo.Window) { s.openSession(session) },
		})
	}
	if len(sub) == 0 {
		sub = []*mygo.MenuItem{{Label: tr("needsNone"), Disabled: true}}
	}
	return mygo.NewMenu([]*mygo.MenuItem{
		{Label: fmt.Sprintf(tr("open"), appName), Click: func(*mygo.MenuItem, *mygo.Window) { s.showWindow() }},
		{Label: fmt.Sprintf(tr("needs"), len(needs)), Submenu: sub, Disabled: len(needs) == 0},
		mygo.Separator(),
		{Label: tr("atLogin"), Type: mygo.MenuItemCheckbox, Checked: mygo.App.OpenAtLogin(),
			Click: func(it *mygo.MenuItem, _ *mygo.Window) {
				on := it.Checked
				go func() {
					if err := mygo.App.SetOpenAtLogin(on); err != nil {
						slog.Warn("desktop: open at login", "err", err)
					}
					s.updateTray()
				}()
			}},
		{Label: tr("keepRunning"), Type: mygo.MenuItemCheckbox, Checked: s.prefs.keepRunning(),
			Click: func(it *mygo.MenuItem, _ *mygo.Window) {
				on := it.Checked
				go s.prefs.setKeepRunning(on)
			}},
		mygo.Separator(),
		{Label: fmt.Sprintf(tr("quit"), appName), Click: func(*mygo.MenuItem, *mygo.Window) { mygo.App.Quit() }},
	})
}

// updateTray sets the tray's menu and, on macOS, its title (the count).
func (s *shell) updateTray() {
	s.mu.Lock()
	tray, needs := s.tray, slices.Clone(s.needs)
	s.mu.Unlock()
	if tray == nil {
		return
	}
	tray.SetMenu(s.trayMenu(needs))
	if runtime.GOOS == "darwin" {
		title := ""
		if len(needs) > 0 {
			title = fmt.Sprint(len(needs))
		}
		tray.SetTitle(title)
	}
	tip := appName
	if len(needs) > 0 {
		tip = fmt.Sprintf(tr("needsTip"), appName, len(needs))
	}
	tray.SetToolTip(tip)
}

func (s *shell) refreshBadge() {
	s.mu.Lock()
	n := max(s.pending, int(s.webBadge.Load()))
	s.mu.Unlock()
	mygo.App.SetBadgeCount(n)
}

// watchNeeds recomputes what needs you whenever the daemon signals a
// change, coalescing bursts, and every 30 s in case a signal was missed.
func (s *shell) watchNeeds(d *daemon) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	var last []need
	first := true
	for {
		if s.quitting.Load() {
			return
		}
		ns, err := d.needs(context.Background())
		if err != nil {
			slog.Debug("desktop: needs", "err", err)
		} else if first || !slices.Equal(ns, last) {
			first, last = false, ns
			s.mu.Lock()
			s.needs, s.pending = ns, len(ns)
			s.mu.Unlock()
			s.updateTray()
			s.refreshBadge()
		}
		select {
		case <-d.changed:
			time.Sleep(150 * time.Millisecond) // coalesce
		case <-tick.C:
		}
	}
}

// showNotifications turns notify events into system notifications. While
// the window is in front the web UI's in-page toast shows the same event,
// so the native one is skipped then.
func (s *shell) showNotifications(d *daemon) {
	for n := range d.notes {
		if s.quitting.Load() {
			return
		}
		if win := s.window(); win != nil && mygo.App.IsActive() && win.IsVisible() && !win.IsMinimized() {
			continue
		}
		s.notify(n)
	}
}

func (s *shell) notify(n notify.Notification) {
	note := mygo.NewNotification(mygo.NotificationOptions{
		ID:    "crewbase/" + n.ID,
		Title: n.Title,
		Body:  n.Body,
		Group: n.Session,
	})
	session := n.Session
	note.OnClick(func() {
		s.openSession(session)
		note.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := note.ShowContext(ctx); err != nil {
		slog.Warn("desktop: notification", "err", err)
	}
}
