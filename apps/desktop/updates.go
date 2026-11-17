package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/egoist/mygo"
)

// In-place updates. The shell drives mygo.Updater itself, without the
// updater plugin's window: it checks soon after launch and then daily,
// downloads a new version in the background (Update.Install: the archive or
// delta is checked against the build's updates.publicKey before anything is
// installed, exactly as mygo does it), and tells the page how it goes
// (updateEvent), which shows it in the sidebar and Settings › 关于.
//
// Once Install returns the update is in place for the next launch: on macOS
// it is staged and a helper swaps the bundle once the app has exited,
// however it exits; on Windows and Linux the files are already replaced.
// So it installs quietly at the next quit, and "重启以完成更新" relaunches
// now (beforeQuit stops the agents first, synchronously, so the relaunch is
// not lost).

// updateEvent carries the update state to the page (window.crewbaseDesktop
// .onUpdate).
var updateEvent = mygo.NewEvent[updateState]("crewbase:update")

// The states of an update.
const (
	updDisabled    = "disabled"    // this build cannot update itself
	updIdle        = "idle"        // up to date, or not checked yet
	updChecking    = "checking"    // asking the feed
	updAvailable   = "available"   // found; automatic download is off
	updDownloading = "downloading" // downloading, verifying, staging
	updReady       = "ready"       // installed for the next launch
	updError       = "error"       // the check or the download failed
)

// updateState is what the page shows.
type updateState struct {
	State   string `json:"state"`
	Current string `json:"current"`           // the running version
	Version string `json:"version,omitempty"` // the new version
	Notes   string `json:"notes,omitempty"`   // its release notes (Markdown)
	// Progress of the download, 0…1; 1 while the update is unpacked.
	Progress   float64 `json:"progress"`
	Downloaded int64   `json:"downloaded,omitempty"`
	Total      int64   `json:"total,omitempty"`
	Error      string  `json:"error,omitempty"`
	// ErrorDuring is "check" or "download".
	ErrorDuring string `json:"errorDuring,omitempty"`
	// User: the person asked for the last check (menu, Settings).
	User bool `json:"user,omitempty"`
	// UpToDate: the last check found no newer version.
	UpToDate     bool   `json:"upToDate,omitempty"`
	AutoDownload bool   `json:"autoDownload"`
	LastCheck    string `json:"lastCheck,omitempty"` // RFC 3339
}

// release is an update a check found.
type release struct {
	version, notes string
	install        func(ctx context.Context, progress func(downloaded, total int64)) error
}

// updateDeps are what updates use of the app, which tests replace.
type updateDeps struct {
	current  string
	enabled  func() bool
	check    func(ctx context.Context) (*release, error)
	emit     func(updateState)
	relaunch func()
	now      func() time.Time
	// installed is called once an update is in place.
	installed func()
}

func mygoUpdateDeps(installed func()) updateDeps {
	return updateDeps{
		current: mygo.App.Version(),
		enabled: mygo.Updater.Enabled,
		check: func(ctx context.Context) (*release, error) {
			up, err := mygo.Updater.Check(ctx)
			if err != nil || up == nil {
				return nil, err
			}
			return &release{version: up.Version, notes: up.Notes, install: up.Install}, nil
		},
		emit:      func(st updateState) { _ = updateEvent.Broadcast(st) },
		relaunch:  func() { mygo.App.Relaunch() },
		now:       time.Now,
		installed: installed,
	}
}

type updates struct {
	d     updateDeps
	prefs *prefs
	// interval between automatic checks; firstWait after launch (and the
	// least wait of any automatic check).
	interval, firstWait time.Duration

	mu      sync.Mutex
	st      updateState
	rel     *release // found, downloading or installed
	busy    bool     // checking or downloading
	cancel  context.CancelFunc
	timer   *time.Timer
	started bool
	stopped bool
	checked bool      // a check began since launch
	failed  time.Time // the last automatic check failed then
}

func newUpdates(d updateDeps, p *prefs) *updates {
	u := &updates{d: d, prefs: p, interval: 24 * time.Hour, firstWait: 10 * time.Second}
	u.st = updateState{State: updIdle, Current: d.current, AutoDownload: p.autoUpdate()}
	if t := p.lastUpdateCheck(); !t.IsZero() {
		u.st.LastCheck = t.UTC().Format(time.RFC3339)
	}
	if !d.enabled() {
		u.st.State = updDisabled
	}
	return u
}

// state returns the current state.
func (u *updates) state() updateState {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.st
}

// set changes the state and tells the page. u.mu is held.
func (u *updates) set(fn func(*updateState)) {
	fn(&u.st)
	if u.d.emit != nil {
		u.d.emit(u.st)
	}
}

// start schedules the automatic checks, once the app is ready.
func (u *updates) start() {
	u.mu.Lock()
	if u.st.State == updDisabled {
		u.mu.Unlock()
		return
	}
	u.started = true
	u.mu.Unlock()
	u.schedule()
}

// stop cancels a download under way and the next check (the app quits).
func (u *updates) stop() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.stopped = true
	if u.timer != nil {
		u.timer.Stop()
		u.timer = nil
	}
	if u.cancel != nil {
		u.cancel()
	}
}

// schedule arms the timer of the next automatic check.
func (u *updates) schedule() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.timer != nil {
		u.timer.Stop()
		u.timer = nil
	}
	if !u.started || u.stopped || u.busy || u.st.State == updReady {
		return
	}
	wait := u.firstWait
	if u.checked {
		now := u.d.now()
		wait = max(nextCheck(u.prefs.lastUpdateCheck(), u.failed, u.interval, now).Sub(now), u.firstWait)
	}
	u.timer = time.AfterFunc(wait, func() { u.check(false) })
}

// nextCheck returns when the next automatic check is due: an interval
// after the last successful one or, when a check failed since, an hour
// after that.
func nextCheck(last, failed time.Time, interval time.Duration, now time.Time) time.Time {
	if !failed.IsZero() {
		return failed.Add(min(interval, time.Hour))
	}
	if last.After(now) { // the clock was set back
		last = now
	}
	return last.Add(interval)
}

// check asks for a newer version; user: the person asked. A version found
// downloads at once when automatic downloads are on. It returns once the
// check (and a download it starts) is done.
func (u *updates) check(user bool) {
	u.mu.Lock()
	if u.stopped {
		u.mu.Unlock()
		return
	}
	if !u.d.enabled() {
		u.set(func(s *updateState) { *s = updateState{State: updDisabled, Current: u.d.current, AutoDownload: s.AutoDownload, LastCheck: s.LastCheck, User: user} })
		u.mu.Unlock()
		return
	}
	if u.busy || u.st.State == updReady {
		if user {
			u.set(func(s *updateState) { s.User = true }) // shows where it is
		}
		u.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	u.busy, u.cancel, u.checked = true, cancel, true
	if u.timer != nil {
		u.timer.Stop()
		u.timer = nil
	}
	u.set(func(s *updateState) {
		s.State, s.User, s.UpToDate, s.Error, s.ErrorDuring = updChecking, user, false, "", ""
	})
	u.mu.Unlock()

	r, err := u.d.check(ctx)

	u.mu.Lock()
	u.busy, u.cancel = false, nil
	cancel()
	if u.stopped {
		u.mu.Unlock()
		return
	}
	if err != nil {
		slog.Warn("desktop: update check", "err", err)
		u.failed = u.d.now()
		u.set(func(s *updateState) {
			s.State, s.Error, s.ErrorDuring = updError, err.Error(), "check"
		})
		u.mu.Unlock()
		u.schedule()
		return
	}
	t := u.d.now()
	u.failed = time.Time{}
	u.prefs.setLastUpdateCheck(t)
	auto := u.st.AutoDownload
	if r == nil {
		u.rel = nil
		u.set(func(s *updateState) {
			*s = updateState{State: updIdle, Current: s.Current, AutoDownload: s.AutoDownload, User: user, UpToDate: true, LastCheck: t.UTC().Format(time.RFC3339)}
		})
		u.mu.Unlock()
		u.schedule()
		return
	}
	slog.Info("desktop: update found", "version", r.version, "auto", auto)
	u.rel = r
	u.set(func(s *updateState) {
		s.State, s.Version, s.Notes, s.Progress, s.Downloaded, s.Total = updAvailable, r.version, r.notes, 0, 0, 0
		s.LastCheck = t.UTC().Format(time.RFC3339)
	})
	u.mu.Unlock()
	if auto {
		u.download()
		return
	}
	u.schedule()
}

// download downloads and installs the version found. It returns once it
// is in place or failed.
func (u *updates) download() {
	u.mu.Lock()
	r := u.rel
	if u.stopped || u.busy || r == nil || u.st.State == updReady {
		u.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	u.busy, u.cancel = true, cancel
	if u.timer != nil {
		u.timer.Stop()
		u.timer = nil
	}
	u.set(func(s *updateState) {
		s.State, s.Progress, s.Downloaded, s.Total, s.Error, s.ErrorDuring = updDownloading, 0, 0, 0, "", ""
	})
	u.mu.Unlock()

	last := 0 // the state above says 0%
	err := r.install(ctx, func(downloaded, total int64) {
		if total <= 0 {
			return
		}
		p := min(float64(downloaded)/float64(total), 1)
		// whole percents: the page redraws a ring, not a byte counter
		if pc := int(p * 100); pc != last {
			last = pc
			u.mu.Lock()
			if u.busy && u.st.State == updDownloading {
				u.set(func(s *updateState) { s.Progress, s.Downloaded, s.Total = p, downloaded, total })
			}
			u.mu.Unlock()
		}
	})

	u.mu.Lock()
	u.busy, u.cancel = false, nil
	canceled := ctx.Err() != nil
	cancel()
	if u.stopped || canceled && err != nil {
		u.mu.Unlock()
		return
	}
	if err != nil {
		slog.Warn("desktop: update download", "version", r.version, "err", err)
		u.set(func(s *updateState) {
			s.State, s.Error, s.ErrorDuring = updError, err.Error(), "download"
		})
		u.mu.Unlock()
		u.schedule()
		return
	}
	slog.Info("desktop: update ready", "version", r.version)
	u.set(func(s *updateState) { s.State, s.Progress = updReady, 1 })
	installed := u.d.installed
	u.mu.Unlock()
	if installed != nil {
		installed()
	}
}

// retry downloads the version found (automatic downloads off, or its
// download failed), else checks again.
func (u *updates) retry() {
	u.mu.Lock()
	dl := u.rel != nil && (u.st.State == updAvailable || u.st.State == updError && u.st.ErrorDuring == "download")
	u.mu.Unlock()
	if dl {
		u.download()
		return
	}
	u.check(true)
}

// errNotReady is returned by restart before an update is in place.
var errNotReady = errors.New("no update is ready to install")

// restart relaunches into the update once it is in place.
func (u *updates) restart() error {
	u.mu.Lock()
	ready := u.st.State == updReady
	u.mu.Unlock()
	if !ready {
		return errNotReady
	}
	u.d.relaunch()
	return nil
}

// setAutoDownload changes the preference; turned on while a version waits
// to be downloaded, it downloads it.
func (u *updates) setAutoDownload(on bool) updateState {
	u.prefs.setAutoUpdate(on)
	u.mu.Lock()
	u.set(func(s *updateState) { s.AutoDownload = on })
	st := u.st
	pending := on && st.State == updAvailable && !u.busy
	u.mu.Unlock()
	if pending {
		go u.download()
	}
	return st
}
