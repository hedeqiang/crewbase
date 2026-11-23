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
}

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
	return Release{Version: version, Tarball: v.Dist.Tarball, Integrity: v.Dist.Integrity}, nil
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

// findNode looks for node (configured path, else PATH) and checks its version.
func findNode(ctx context.Context, configured string) NodeInfo {
	p := configured
	if p == "" {
		var err error
		if p, err = exec.LookPath("node"); err != nil {
			return NodeInfo{Problem: "Node.js is not installed (Open Artifact needs Node 22.18 or later)"}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, p, "--version").Output()
	if err != nil {
		return NodeInfo{Path: p, Problem: "could not run " + p}
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
		n.Problem = fmt.Sprintf("Node %s is too old: Open Artifact needs %d.%d or later", v, MinNode[0], MinNode[1])
	}
	return n
}
