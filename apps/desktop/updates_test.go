package main

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo"
)

// fakeFeed stands in for mygo.Updater: no network, nothing installed.
type fakeFeed struct {
	mu        sync.Mutex
	enabled   bool
	found     *release
	checkErr  error
	checks    int
	emitted   []updateState
	relaunch  int
	installed int
}

func (f *fakeFeed) deps() updateDeps {
	return updateDeps{
		current: "0.5.8",
		enabled: func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.enabled },
		check: func(context.Context) (*release, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.checks++
			return f.found, f.checkErr
		},
		emit:      func(s updateState) { f.mu.Lock(); f.emitted = append(f.emitted, s); f.mu.Unlock() },
		relaunch:  func() { f.mu.Lock(); f.relaunch++; f.mu.Unlock() },
		now:       time.Now,
		installed: func() { f.mu.Lock(); f.installed++; f.mu.Unlock() },
	}
}

// states lists the states emitted, without repeats in a row.
func (f *fakeFeed) states() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, s := range f.emitted {
		if len(out) == 0 || out[len(out)-1] != s.State {
			out = append(out, s.State)
		}
	}
	return out
}

// installer returns an install func that reports progress in steps of a
// quarter and then returns err.
func installer(err error) func(context.Context, func(int64, int64)) error {
	return func(ctx context.Context, progress func(int64, int64)) error {
		for _, n := range []int64{0, 25, 50, 75, 100} {
			progress(n, 100)
		}
		return err
	}
}

func newTestUpdates(f *fakeFeed) *updates {
	return newUpdates(f.deps(), &prefs{}) // no path: nothing is saved
}

func TestUpdateDownloadsInPlaceAndRestarts(t *testing.T) {
	f := &fakeFeed{enabled: true, found: &release{version: "0.6.0", notes: "## 新功能", install: installer(nil)}}
	u := newTestUpdates(f)
	if !u.state().AutoDownload {
		t.Fatal("automatic downloads are on by default")
	}
	if err := u.restart(); !errors.Is(err, errNotReady) || f.relaunch != 0 {
		t.Fatal("restart before an update is in place must do nothing")
	}
	u.check(false)
	if got, want := f.states(), []string{updChecking, updAvailable, updDownloading, updReady}; !slices.Equal(got, want) {
		t.Fatalf("states %v, want %v", got, want)
	}
	var progress []float64
	for _, s := range f.emitted {
		if s.State == updDownloading {
			progress = append(progress, s.Progress)
		}
	}
	if !slices.Equal(progress, []float64{0, 0.25, 0.5, 0.75, 1}) {
		t.Fatalf("progress %v", progress)
	}
	st := u.state()
	if st.Version != "0.6.0" || st.Notes != "## 新功能" || st.Current != "0.5.8" || st.LastCheck == "" {
		t.Fatalf("ready state %+v", st)
	}
	if f.installed != 1 {
		t.Fatal("installed must be told once (quitting then is not deferred)")
	}
	// ready: no more checks, and a check asked for just shows it
	u.check(true)
	if f.checks != 1 || u.state().State != updReady {
		t.Fatalf("checks %d, state %s", f.checks, u.state().State)
	}
	if err := u.restart(); err != nil || f.relaunch != 1 {
		t.Fatalf("restart: %v, relaunches %d", err, f.relaunch)
	}
}

func TestUpdateUpToDate(t *testing.T) {
	f := &fakeFeed{enabled: true}
	u := newTestUpdates(f)
	u.check(true)
	st := u.state()
	if st.State != updIdle || !st.UpToDate || !st.User || st.LastCheck == "" {
		t.Fatalf("state %+v", st)
	}
}

func TestUpdateAutoDownloadOff(t *testing.T) {
	installs := 0
	f := &fakeFeed{enabled: true, found: &release{version: "0.6.0", install: func(ctx context.Context, p func(int64, int64)) error {
		installs++
		return installer(nil)(ctx, p)
	}}}
	u := newTestUpdates(f)
	u.setAutoDownload(false)
	u.check(false)
	if st := u.state(); st.State != updAvailable || st.Version != "0.6.0" || installs != 0 {
		t.Fatalf("with automatic downloads off a version waits: %+v, installs %d", st, installs)
	}
	u.retry() // the page's "下载"
	if st := u.state(); st.State != updReady || installs != 1 {
		t.Fatalf("after download: %+v", st)
	}
}

func TestUpdateTurningAutoOnDownloads(t *testing.T) {
	done := make(chan struct{})
	f := &fakeFeed{enabled: true, found: &release{version: "0.6.0", install: func(ctx context.Context, p func(int64, int64)) error {
		defer close(done)
		return installer(nil)(ctx, p)
	}}}
	u := newTestUpdates(f)
	u.setAutoDownload(false)
	u.check(false)
	u.setAutoDownload(true)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("turning automatic downloads on must download the waiting version")
	}
	deadline := time.Now().Add(5 * time.Second)
	for u.state().State != updReady {
		if time.Now().After(deadline) {
			t.Fatalf("state %s", u.state().State)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestUpdateErrors(t *testing.T) {
	f := &fakeFeed{enabled: true, checkErr: errors.New("offline")}
	u := newTestUpdates(f)
	u.check(true)
	if st := u.state(); st.State != updError || st.ErrorDuring != "check" || st.Error != "offline" {
		t.Fatalf("check error: %+v", st)
	}
	// retry checks again; now a version is found but its download fails
	// (a bad signature is an error of Install, never skipped)
	f.mu.Lock()
	f.checkErr = nil
	fails := true
	f.found = &release{version: "0.6.0", install: func(ctx context.Context, p func(int64, int64)) error {
		if fails {
			return installer(errors.New("signature does not verify"))(ctx, p)
		}
		return installer(nil)(ctx, p)
	}}
	f.mu.Unlock()
	u.retry()
	st := u.state()
	if st.State != updError || st.ErrorDuring != "download" || st.Version != "0.6.0" {
		t.Fatalf("download error: %+v", st)
	}
	if f.installed != 0 || u.restart() == nil {
		t.Fatal("a failed download is not installed")
	}
	fails = false
	checks := f.checks
	u.retry() // downloads again, without checking
	if u.state().State != updReady || f.checks != checks {
		t.Fatalf("retry: %+v, checks %d → %d", u.state(), checks, f.checks)
	}
}

func TestUpdateDisabled(t *testing.T) {
	f := &fakeFeed{enabled: false}
	u := newTestUpdates(f)
	u.start()
	u.check(true)
	if st := u.state(); st.State != updDisabled || f.checks != 0 {
		t.Fatalf("a build that cannot update itself never checks: %+v", st)
	}
	if u.timer != nil {
		t.Fatal("no automatic checks")
	}
}

func TestUpdateQuitCancelsDownload(t *testing.T) {
	started := make(chan struct{})
	f := &fakeFeed{enabled: true, found: &release{version: "0.6.0", install: func(ctx context.Context, p func(int64, int64)) error {
		p(10, 100)
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}}}
	u := newTestUpdates(f)
	done := make(chan struct{})
	go func() { u.check(false); close(done) }()
	<-started
	if st := u.state(); st.State != updDownloading || st.Progress != 0.1 {
		t.Fatalf("state %+v", st)
	}
	u.stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stop must cancel the download")
	}
	if f.installed != 0 {
		t.Fatal("a canceled download is not installed")
	}
}

func TestUpdateSchedule(t *testing.T) {
	f := &fakeFeed{enabled: true}
	u := newTestUpdates(f)
	u.firstWait = 10 * time.Millisecond
	u.start()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		n := f.checks
		f.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no check soon after launch")
		}
		time.Sleep(5 * time.Millisecond)
	}
	u.stop()

	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	if got := nextCheck(now.Add(-time.Hour), time.Time{}, day, now); !got.Equal(now.Add(23 * time.Hour)) {
		t.Fatalf("daily: %v", got)
	}
	if got := nextCheck(now.Add(-2*day), now, day, now); !got.Equal(now.Add(time.Hour)) {
		t.Fatalf("after a failure: %v", got)
	}
	if got := nextCheck(now.Add(day), time.Time{}, day, now); !got.Equal(now.Add(day)) {
		t.Fatalf("clock set back: %v", got)
	}
}

// The Desktop service binds: its types are ones mygo can send.
func TestDesktopBinds(t *testing.T) {
	mygo.Bind(Desktop{&shell{}})
}
