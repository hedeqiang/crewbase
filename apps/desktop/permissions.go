package main

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/permissionflow"
)

// System permissions Crewbase uses on macOS (设置 › 系统权限):
//
//	notifications     approvals, finished tasks and @mentions while the
//	                  window is in the background
//	microphone        voice input in the composer
//	full-disk-access  agents in host mode reading folders macOS protects
//	                  (Desktop, Documents, Downloads, other apps' data)
//
// macOS has no API to read the notification setting, so its status is
// what the last notification shown found: granted, denied, or unknown
// until one is shown (the test button shows one).

// bundleID is mygo.json's identifier (the notification settings page is per app).
const bundleID = "app.crewbase.desktop"

// PermState is one row of the permissions page.
type PermState struct {
	ID     string `json:"id"`
	Status string `json:"status"` // granted | denied | not-determined | unknown
	// CanRequest: the system still has its own prompt to show.
	CanRequest bool `json:"canRequest"`
}

// notifyState: 0 unknown, 1 granted, 2 denied.
var notifyState atomic.Int32

func noteResult(err error) {
	switch {
	case err == nil:
		notifyState.Store(1)
	case errors.Is(err, mygo.ErrNotificationsDenied):
		notifyState.Store(2)
	}
}

var permIDs = map[string]permissionflow.Permission{
	"microphone":       permissionflow.Microphone,
	"full-disk-access": permissionflow.FullDiskAccess,
}

func statusName(s permissionflow.Status) string {
	switch s {
	case permissionflow.Granted:
		return "granted"
	case permissionflow.Denied:
		return "denied"
	case permissionflow.NotDetermined:
		return "not-determined"
	}
	return "unknown"
}

// Permissions reports the permissions the app uses (macOS; none elsewhere).
func (d Desktop) Permissions() []PermState {
	if runtime.GOOS != "darwin" {
		return []PermState{}
	}
	ns := map[int32]string{0: "unknown", 1: "granted", 2: "denied"}[notifyState.Load()]
	out := []PermState{{ID: "notifications", Status: ns, CanRequest: ns == "unknown"}}
	for _, id := range []string{"microphone", "full-disk-access"} {
		st, err := permissionflow.Check(permIDs[id])
		s := statusName(st)
		if err != nil {
			s = "unknown"
		}
		out = append(out, PermState{ID: id, Status: s, CanRequest: id == "microphone" && st == permissionflow.NotDetermined})
	}
	return out
}

// RequestPermission asks for a permission with the system's prompt where
// there is one: a test notification for notifications, the microphone
// prompt, or System Settings with the app to drag in for Full Disk Access.
// It returns the rows afterwards.
func (d Desktop) RequestPermission(id string) []PermState {
	switch id {
	case "notifications":
		note := mygo.NewNotification(mygo.NotificationOptions{ID: "crewbase/permission-test", Title: "Crewbase", Body: tr("notifyTest")})
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		err := note.ShowContext(ctx)
		cancel()
		noteResult(err)
		if err != nil && !errors.Is(err, mygo.ErrNotificationsDenied) {
			slog.Warn("desktop: test notification", "err", err)
		}
	case "microphone":
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		if _, err := permissionflow.Request(ctx, permissionflow.Microphone); err != nil {
			slog.Warn("desktop: microphone permission", "err", err)
		}
		cancel()
	case "full-disk-access":
		// the panel follows System Settings until the switch is on (or 10 min)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			if _, err := permissionflow.Guide(ctx, permissionflow.FullDiskAccess, permissionflow.GuideOptions{}); err != nil {
				slog.Warn("desktop: full disk access", "err", err)
			}
		}()
	}
	return d.Permissions()
}

// OpenPermissionSettings opens the page of System Settings for id.
func (d Desktop) OpenPermissionSettings(id string) error {
	if id == "notifications" {
		return mygo.Shell.OpenExternal("x-apple.systempreferences:com.apple.Notifications-Settings.extension?id=" + bundleID)
	}
	p, ok := permIDs[id]
	if !ok {
		return errors.New("unknown permission")
	}
	return permissionflow.OpenSettings(p)
}
