package console

// Update check for crewd opened in a browser or run as a server, where the
// desktop app's updater is not there: once a day (and on demand) the latest
// GitHub release is compared with the running version. It only tells; the
// desktop app installs updates itself, servers update their package or image.
//
//	GET  /api/updates        → UpdateInfo (cached)
//	POST /api/updates/check  → UpdateInfo (checks now)
//
// One request a day for the latest release's update manifest (the file the
// desktop updater reads, served by github.com without the API's rate
// limit), carrying only the version in the User-Agent. Off with config
// "updateCheck": false.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/kernel"
)

// Releases is the latest release; its manifest is
// <Releases>/download/update-linux-amd64.json (every platform's manifest
// carries the version and notes).
var Releases = "https://github.com/hedeqiang/crewbase/releases/latest"

// UpdateInfo is what the UI shows.
type UpdateInfo struct {
	Current   string    `json:"current"`
	Latest    string    `json:"latest,omitempty"`
	Available bool      `json:"available"`
	URL       string    `json:"url,omitempty"`   // release page
	Notes     string    `json:"notes,omitempty"` // release notes (Markdown)
	Checked   time.Time `json:"checked,omitzero"`
	Error     string    `json:"error,omitempty"`
	Enabled   bool      `json:"enabled"` // automatic daily checks
}

type updates struct {
	enabled bool
	client  *http.Client
	mu      sync.Mutex
	info    UpdateInfo
}

func (u *updates) get() UpdateInfo {
	u.mu.Lock()
	defer u.mu.Unlock()
	i := u.info
	i.Current, i.Enabled = kernel.Version, u.enabled && !devBuild()
	// a development build (go run, make) is not something an update replaces
	i.Available = i.Latest != "" && newer(i.Latest, i.Current) && !devBuild()
	return i
}

func (u *updates) check(ctx context.Context) UpdateInfo {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", Releases+"/download/update-linux-amd64.json", nil)
	req.Header.Set("User-Agent", "crewbase/"+kernel.Version)
	var r struct {
		Version string `json:"version"`
		Notes   string `json:"notes"`
	}
	res, err := u.client.Do(req)
	if err == nil {
		defer res.Body.Close()
		if res.StatusCode != 200 {
			err = fmt.Errorf("GitHub answered %s", res.Status)
		} else {
			err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&r)
		}
	}
	u.mu.Lock()
	u.info.Checked = time.Now().UTC()
	if err != nil {
		u.info.Error = err.Error() // the UI words it
	} else {
		u.info.Error = ""
		u.info.Latest = strings.TrimPrefix(r.Version, "v")
		u.info.URL = strings.TrimSuffix(Releases, "/latest") + "/tag/v" + u.info.Latest
		u.info.Notes = notesOnly(r.Notes)
	}
	u.mu.Unlock()
	return u.get()
}

// notesOnly drops the install section the release notes end with.
func notesOnly(body string) string {
	if i := strings.Index(body, "\n## 安装"); i >= 0 {
		body = body[:i]
	}
	return strings.TrimSpace(body)
}

// newer reports whether version a is newer than b (semantic versions; a
// dev build is older than its release).
func newer(a, b string) bool {
	pa, pb := parseVer(a), parseVer(b)
	for i := 0; i < 3; i++ {
		if pa.n[i] != pb.n[i] {
			return pa.n[i] > pb.n[i]
		}
	}
	// 1.2.0 > 1.2.0-dev / 1.2.0-beta.1
	if (pa.pre == "") != (pb.pre == "") {
		return pa.pre == ""
	}
	return pa.pre > pb.pre
}

type ver struct {
	n   [3]int
	pre string
}

func parseVer(s string) ver {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	var v ver
	core, pre, _ := strings.Cut(s, "-")
	v.pre = pre
	for i, p := range strings.SplitN(core, ".", 3) {
		n, _ := strconv.Atoi(p)
		v.n[i] = n
	}
	return v
}

// devBuild: a build without a release version (go run, go build, make).
func devBuild() bool { return strings.HasSuffix(kernel.Version, "-dev") }

func (u *updates) run(ctx context.Context) {
	if !u.enabled || devBuild() {
		return
	}
	t := time.NewTimer(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			u.check(ctx)
			t.Reset(24 * time.Hour)
		}
	}
}
