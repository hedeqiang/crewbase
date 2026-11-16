//go:build darwin

package macos

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/computer"
)

// Live tests run against the real system, read-only: permissions, the app
// list, and outlines / screenshots of Finder or a TextEdit this test starts
// itself (and quits). They never act on apps and never post events.
//
//	CREWBASE_COMPUTER_LIVE=1 go test -run Live -v ./providers/computer/macos/

func liveProvider(t *testing.T) *Provider {
	t.Helper()
	if os.Getenv("CREWBASE_COMPUTER_LIVE") != "1" {
		t.Skip("set CREWBASE_COMPUTER_LIVE=1 to run against the real system (read-only)")
	}
	pf, err := newPlatform()
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(pf, Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func TestLiveReadOnly(t *testing.T) {
	p := liveProvider(t)
	perms := p.Permissions()
	t.Logf("permissions: %+v", perms)
	t.Logf("idle for %v", p.IdleFor())
	t.Logf("screens: %+v", p.pf.Screens())
	apps, err := p.Apps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(apps, func(a computer.App) bool { return a.ID == "com.apple.finder" }) {
		t.Fatalf("Finder missing from %d apps", len(apps))
	}
	for _, a := range apps {
		t.Logf("app %s %q pid %d active=%t hidden=%t", a.ID, a.Name, a.PID, a.Active, a.Hidden)
	}
	if wins, err := p.pf.Windows(appPID(apps, "com.apple.finder")); err == nil {
		t.Logf("Finder: %d on-screen windows", len(wins))
	}

	if !perms.Accessibility {
		t.Log("no Accessibility for this process: outlines not verified")
		if _, err := p.Observe(context.Background(), "com.apple.finder", 0); !errors.Is(err, computer.ErrNoAccessibility) {
			t.Fatalf("observe without Accessibility: %v", err)
		}
	} else {
		obs, err := p.Observe(context.Background(), "com.apple.finder", 0)
		if err != nil {
			t.Logf("observe Finder: %v (no Finder window open?)", err)
		} else {
			logObservation(t, obs)
		}
	}
	if !perms.ScreenRecording {
		t.Log("no Screen Recording for this process: screenshots not verified")
	} else {
		shot, err := p.Screenshot(context.Background(), "com.apple.finder", 0)
		if err != nil {
			t.Logf("screenshot Finder: %v", err)
		} else {
			t.Logf("screenshot: %d×%d scale %.3f, %d bytes", shot.Width, shot.Height, shot.Scale, len(shot.PNG))
			if max(shot.Width, shot.Height) > DefaultMaxEdge {
				t.Fatalf("screenshot exceeds %d", DefaultMaxEdge)
			}
		}
	}
}

// TestLiveTextEdit opens a new, empty document of our own in TextEdit
// (only when TextEdit is not already running, so none of the person's
// windows are touched), observes it read-only and quits it.
func TestLiveTextEdit(t *testing.T) {
	p := liveProvider(t)
	perms := p.Permissions()
	if !perms.Accessibility && !perms.ScreenRecording {
		t.Skip("neither Accessibility nor Screen Recording: nothing to verify on TextEdit")
	}
	apps, _ := p.Apps(context.Background())
	if appPID(apps, "com.apple.TextEdit") != 0 || pgrep("TextEdit") != 0 {
		t.Skip("TextEdit is already running: not touching the person's documents")
	}
	doc := filepath.Join(t.TempDir(), "crewbase-live.txt")
	if err := os.WriteFile(doc, []byte("Crewbase live test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/usr/bin/open", "-g", "-a", "TextEdit", doc).CombinedOutput(); err != nil {
		t.Fatalf("open TextEdit: %v %s", err, out)
	}
	pid := 0
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && pid == 0 {
		pid = pgrep("TextEdit")
		time.Sleep(200 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("TextEdit did not start")
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGTERM) })
	time.Sleep(1500 * time.Millisecond) // its window

	if perms.Accessibility {
		var obs computer.Observation
		var err error
		for i := 0; i < 10; i++ {
			if obs, err = p.Observe(context.Background(), "com.apple.TextEdit", 0); err == nil {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("observe TextEdit: %v", err)
		}
		logObservation(t, obs)
		if !strings.Contains(obs.Window.Title, "crewbase-live") {
			t.Errorf("window title %q is not our document", obs.Window.Title)
		}
	}
	if perms.ScreenRecording {
		shot, err := p.Screenshot(context.Background(), "com.apple.TextEdit", 640)
		if err != nil {
			t.Fatalf("screenshot TextEdit: %v", err)
		}
		t.Logf("screenshot: %d×%d scale %.3f", shot.Width, shot.Height, shot.Scale)
	}
}

func logObservation(t *testing.T, obs computer.Observation) {
	t.Helper()
	b, _ := json.MarshalIndent(obs.Root, "", " ")
	s := string(b)
	if len(s) > 4000 {
		s = s[:4000] + "…"
	}
	t.Logf("window %+v version %d truncated %t menus %d", obs.Window, obs.Version, obs.Truncated, len(obs.Menus))
	for _, m := range obs.Menus {
		var items []string
		for _, it := range m.Children {
			items = append(items, it.Name+" "+it.Value)
		}
		t.Logf("menu %s: %s", m.Name, strings.Join(items, " | "))
	}
	t.Log(s)
}

func appPID(apps []computer.App, id string) int {
	for _, a := range apps {
		if a.ID == id {
			return a.PID
		}
	}
	return 0
}

func pgrep(name string) int {
	out, err := exec.Command("/usr/bin/pgrep", "-x", name).Output()
	if err != nil {
		return 0
	}
	f := strings.Fields(string(out))
	if len(f) != 1 {
		return 0
	}
	var pid int
	for _, c := range f[0] {
		pid = pid*10 + int(c-'0')
	}
	return pid
}

// TestLiveSelf exercises the accessibility calls on this test process
// itself (no other app is read).
func TestLiveSelf(t *testing.T) {
	p := liveProvider(t)
	el, err := p.pf.AppElement(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer p.pf.Release(el)
	p.pf.SetTimeout(el, 0.5)
	raw, err := p.pf.Read(el)
	t.Logf("own app element: %+v err=%v", raw, err)
	kids, total, err := p.pf.Children(el, 10)
	for _, k := range kids {
		p.pf.Release(k)
	}
	// a test binary is no Cocoa app: "not implemented" (or "not allowed"
	// without Accessibility) is the expected answer; the point is that the
	// calls go through and come back with an AX error, not a crash
	t.Logf("children: %d of %d err=%v", len(kids), total, err)
}
