package openartifact

// The runtime: the npm package @open-artifact/cli (the command line, the
// daemon, the Shell and the built-in types; no dependencies), unpacked in
// <root>/apps/open-artifact/<version>. It is downloaded from the npm
// registry and installed only when its bytes match the registry's sha512
// integrity digest. It runs on the computer's Node (22.18 or later).

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Package is the npm package of the runtime.
const Package = "@open-artifact/cli"

// MinNode is the oldest Node the runtime runs on.
var MinNode = [3]int{22, 18, 0}

const maxTarball = 256 << 20

// Release is one published version.
type Release struct {
	Version   string `json:"version"`
	Tarball   string `json:"-"`
	Integrity string `json:"-"`
	// Older are the stable versions before it, newest first (a fallback
	// while a just-published version is not downloadable yet).
	Older []Release `json:"-"`
}

// errNotYet: the registry lists the version but its file is not there yet
// (it takes a few minutes after a publish to reach every server).
var errNotYet = errors.New("not downloadable yet")

var versionRe = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)

// latest asks the registry for a version ("" = the latest).
func latest(ctx context.Context, client *http.Client, registry, version string) (Release, error) {
	u := strings.TrimRight(registry, "/") + "/" + url.PathEscape(Package)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.npm.install-v1+json; q=1.0, application/json; q=0.8")
	resp, err := client.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("the npm registry: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Release{}, fmt.Errorf("the npm registry answered %d for %s", resp.StatusCode, Package)
	}
	var doc struct {
		Tags     map[string]string `json:"dist-tags"`
		Versions map[string]struct {
			Dist struct {
				Tarball   string `json:"tarball"`
				Integrity string `json:"integrity"`
			} `json:"dist"`
		} `json:"versions"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&doc); err != nil {
		return Release{}, fmt.Errorf("the npm registry: %w", err)
	}
	if version == "" {
		version = doc.Tags["latest"]
	}
	v, ok := doc.Versions[version]
	if !ok || !versionRe.MatchString(version) {
		return Release{}, fmt.Errorf("%s has no version %q", Package, version)
	}
	if !strings.HasPrefix(v.Dist.Integrity, "sha512-") {
		return Release{}, fmt.Errorf("%s %s publishes no sha512 integrity digest; not installing it", Package, version)
	}
	rel := Release{Version: version, Tarball: v.Dist.Tarball, Integrity: v.Dist.Integrity}
	var older []string
	for o := range doc.Versions {
		if o != version && versionRe.MatchString(o) && !strings.Contains(o, "-") && newer(version, o) {
			older = append(older, o)
		}
	}
	sort.Slice(older, func(i, j int) bool { return newer(older[i], older[j]) })
	for _, o := range older[:min(len(older), 3)] {
		d := doc.Versions[o].Dist
		if strings.HasPrefix(d.Integrity, "sha512-") {
			rel.Older = append(rel.Older, Release{Version: o, Tarball: d.Tarball, Integrity: d.Integrity})
		}
	}
	return rel, nil
}

// install downloads rel, checks it against its integrity digest and
// unpacks it into <apps>/<version> (all or nothing).
func install(ctx context.Context, client *http.Client, apps string, rel Release) (string, error) {
	dest := filepath.Join(apps, rel.Version)
	if ok, _ := runtimeAt(dest); ok {
		return dest, nil
	}
	if err := os.MkdirAll(apps, 0o755); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rel.Tarball, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s %s: %w", Package, rel.Version, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		if resp.StatusCode == http.StatusNotFound {
			return "", fmt.Errorf("download %s %s: %w (npm published it moments ago; try again in a few minutes)", Package, rel.Version, errNotYet)
		}
		return "", fmt.Errorf("download %s %s: HTTP %d", Package, rel.Version, resp.StatusCode)
	}
	tmp, err := os.CreateTemp(apps, ".download-*.tgz")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	h := sha512.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, maxTarball+1))
	tmp.Close()
	if err != nil {
		return "", fmt.Errorf("download %s %s: %w", Package, rel.Version, err)
	}
	if n > maxTarball {
		return "", fmt.Errorf("%s %s is larger than %d MB; not installing it", Package, rel.Version, maxTarball>>20)
	}
	want, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(rel.Integrity, "sha512-"))
	if err != nil || string(want) != string(h.Sum(nil)) {
		return "", fmt.Errorf("the download of %s %s does not match its published sha512 digest; nothing was installed", Package, rel.Version)
	}
	stage, err := os.MkdirTemp(apps, ".unpack-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	if err := untar(tmp.Name(), stage); err != nil {
		return "", fmt.Errorf("unpack %s %s: %w", Package, rel.Version, err)
	}
	root := filepath.Join(stage, "package")
	if ok, err := runtimeAt(root); !ok {
		return "", fmt.Errorf("%s %s is not a runtime: %v", Package, rel.Version, err)
	}
	if err := os.Rename(root, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// untar unpacks a .tgz into dir: regular files and folders only, nothing
// outside dir (no absolute paths, no .., no links).
func untar(file, dir string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	var total int64
	for {
		hd, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(strings.TrimPrefix(hd.Name, "./"))
		if name == "." || path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, "\\") {
			return fmt.Errorf("unsafe path %q in the archive", hd.Name)
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		switch hd.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			total += hd.Size
			if total > 1<<30 {
				return errors.New("the archive unpacks to more than 1 GB")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if hd.Mode&0o111 != 0 {
				mode = 0o755
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, io.LimitReader(tr, hd.Size))
			out.Close()
			if err != nil {
				return err
			}
		default:
			// links, devices and the rest are skipped
		}
	}
}

// runtimeAt reports whether dir holds a runtime (its entry script and package.json).
func runtimeAt(dir string) (bool, error) {
	if _, err := os.Stat(filepath.Join(dir, "scripts", "open-artifact.mjs")); err != nil {
		return false, errors.New("no scripts/open-artifact.mjs")
	}
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false, err
	}
	var p struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(b, &p) != nil || p.Name != Package {
		return false, fmt.Errorf("package.json names %q", p.Name)
	}
	return true, nil
}

// installed lists the versions in apps, newest first.
func installed(apps string) []string {
	ents, _ := os.ReadDir(apps)
	var out []string
	for _, e := range ents {
		if e.IsDir() && versionRe.MatchString(e.Name()) {
			if ok, _ := runtimeAt(filepath.Join(apps, e.Name())); ok {
				out = append(out, e.Name())
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return newer(out[i], out[j]) })
	return out
}

// newer reports whether version a is newer than b (numeric parts).
func newer(a, b string) bool {
	pa, pb := parts(a), parts(b)
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parts(v string) [3]int {
	var out [3]int
	core, _, _ := strings.Cut(v, "-")
	for i, s := range strings.SplitN(core, ".", 3) {
		out[i], _ = strconv.Atoi(s)
	}
	return out
}

// NodeInfo is the Node the runtime would run on.
type NodeInfo struct {
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
	OK      bool   `json:"ok"`
	Problem string `json:"problem,omitempty"`
}

// findNode finds a Node to run on: the configured one, else the first that
// runs and is new enough of every node on PATH and in the usual places
// (Homebrew, nvm, volta, fnm, mise, asdf). A node that is there but does
// not start (a Homebrew node@22 whose libraries were upgraded away) is
// passed over, and its error is what the page shows when none works.
func findNode(ctx context.Context, configured string) NodeInfo {
	if configured != "" {
		return checkNode(ctx, configured)
	}
	nodeCache.mu.Lock()
	if time.Since(nodeCache.at) < 30*time.Second {
		n := nodeCache.n
		nodeCache.mu.Unlock()
		return n
	}
	nodeCache.mu.Unlock()
	n := searchNode(ctx, nodeCandidates())
	nodeCache.mu.Lock()
	nodeCache.n, nodeCache.at = n, time.Now()
	nodeCache.mu.Unlock()
	return n
}

var nodeCache struct {
	mu sync.Mutex
	n  NodeInfo
	at time.Time
}

func searchNode(ctx context.Context, cands []string) NodeInfo {
	var first NodeInfo
	for _, p := range cands {
		n := checkNode(ctx, p)
		if n.OK {
			return n
		}
		if first.Path == "" {
			first = n
		}
	}
	if first.Path == "" {
		return NodeInfo{Problem: "Node.js is not installed (Open Artifact needs Node 22.18 or later)"}
	}
	return first
}

// nodeCandidates are the node binaries to try, PATH order first.
func nodeCandidates() []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir != "" {
			add(filepath.Join(dir, "node"))
		}
	}
	add("/opt/homebrew/bin/node")
	add("/usr/local/bin/node")
	globs := []string{"/opt/homebrew/opt/node*/bin/node", "/usr/local/opt/node*/bin/node"}
	if home, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(home, ".volta", "bin", "node"))
		globs = append(globs,
			filepath.Join(home, ".nvm", "versions", "node", "*", "bin", "node"),
			filepath.Join(home, ".local", "share", "fnm", "node-versions", "*", "installation", "bin", "node"),
			filepath.Join(home, "Library", "Application Support", "fnm", "node-versions", "*", "installation", "bin", "node"),
			filepath.Join(home, ".local", "share", "mise", "installs", "node", "*", "bin", "node"),
			filepath.Join(home, ".asdf", "installs", "nodejs", "*", "bin", "node"))
	}
	for _, g := range globs {
		ms, _ := filepath.Glob(g)
		// newest first
		sort.Slice(ms, func(i, j int) bool {
			return newer(verIn(ms[i]), verIn(ms[j])) || (verIn(ms[i]) == verIn(ms[j]) && ms[i] > ms[j])
		})
		for _, m := range ms {
			add(m)
		}
	}
	return out
}

var verInRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// verIn is the version a path names (…/v24.1.0/…, node@22 → 22.0.0).
func verIn(p string) string {
	if m := verInRe.FindString(p); m != "" {
		return m
	}
	if i := strings.Index(p, "node@"); i >= 0 {
		major := p[i+5:]
		if j := strings.IndexByte(major, '/'); j >= 0 {
			major = major[:j]
		}
		return major + ".0.0"
	}
	return "999.0.0" // the unversioned one (Homebrew's node) is the newest
}

// checkNode runs node --version and checks it is new enough.
func checkNode(ctx context.Context, p string) NodeInfo {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var stderr strings.Builder
	cmd := exec.CommandContext(ctx, p, "--version")
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		why := strings.TrimSpace(stderr.String())
		if i := strings.IndexByte(why, '\n'); i >= 0 {
			why = why[:i]
		}
		if why == "" {
			why = err.Error()
		}
		return NodeInfo{Path: p, Problem: fmt.Sprintf("%s does not start (%s)", p, why)}
	}
	v := strings.TrimPrefix(strings.TrimSpace(string(out)), "v")
	n := NodeInfo{Path: p, Version: v}
	got := parts(v)
	for i := range got {
		if got[i] != MinNode[i] {
			n.OK = got[i] > MinNode[i]
			break
		}
		n.OK = true
	}
	if !n.OK {
		n.Problem = fmt.Sprintf("Node %s (%s) is too old: Open Artifact needs %d.%d or later", v, p, MinNode[0], MinNode[1])
	}
	return n
}
