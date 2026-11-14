package plugins

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/bridge/process"
)

const (
	maxPluginBytes  = 200 << 20 // unpacked size limit
	maxTarballBytes = 100 << 20
)

var npmNameRe = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._~-]*/)?[a-z0-9][a-z0-9._~-]*(@[A-Za-z0-9._^~<>=*-]+)?$`)

// fetch places the plugin from src into a fresh directory under tmp and
// returns that directory, the source kind and a canonical source string.
// digest is the SHA-256 of the package file (nil for a directory).
func fetch(ctx context.Context, src, tmp string) (root, kind, canon string, digest []byte, err error) {
	switch {
	case strings.HasPrefix(src, "http://"), strings.HasPrefix(src, "https://"):
		tgz := filepath.Join(tmp, "download.tgz")
		if err := download(ctx, src, tgz); err != nil {
			return "", "", "", nil, err
		}
		if digest, err = FileDigest(tgz); err != nil {
			return "", "", "", nil, err
		}
		root, err := unpack(tgz, filepath.Join(tmp, "unpacked"))
		return root, "tarball", src, digest, err
	case strings.HasPrefix(src, "file://"):
		src = strings.TrimPrefix(src, "file://")
	}
	if st, serr := os.Stat(src); serr == nil {
		abs, err := filepath.Abs(src)
		if err != nil {
			return "", "", "", nil, err
		}
		if st.IsDir() {
			if _, err := os.Stat(filepath.Join(abs, process.ManifestFile)); err != nil {
				return "", "", "", nil, fmt.Errorf("%w: %s has no %s", ErrBadSource, abs, process.ManifestFile)
			}
			dst := filepath.Join(tmp, "copy")
			if err := copyTree(abs, dst); err != nil {
				return "", "", "", nil, err
			}
			return dst, "local", abs, nil, nil
		}
		if digest, err = FileDigest(abs); err != nil {
			return "", "", "", nil, err
		}
		root, err := unpack(abs, filepath.Join(tmp, "unpacked"))
		return root, "tarball", abs, digest, err
	}
	if npmNameRe.MatchString(src) {
		tgz, err := npmPack(ctx, src, filepath.Join(tmp, "npm"))
		if err != nil {
			return "", "", "", nil, err
		}
		if digest, err = FileDigest(tgz); err != nil {
			return "", "", "", nil, err
		}
		root, err := unpack(tgz, filepath.Join(tmp, "unpacked"))
		return root, "npm", src, digest, err
	}
	return "", "", "", nil, fmt.Errorf("%w: %q is not a directory, tarball, URL or npm package", ErrBadSource, src)
}

// copyTree copies a plugin directory. Symlinks are skipped (they could point
// anywhere on the machine), as are VCS data and the host-provided SDKs.
func copyTree(src, dst string) error {
	var total int64
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".crewbase-sdk", "__pycache__":
				if rel != "." {
					return filepath.SkipDir
				}
			}
			if rel == filepath.Join("node_modules", "@crewbase", "plugin-sdk") {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if total += info.Size(); total > maxPluginBytes {
			return fmt.Errorf("%w: plugin larger than %d MB", ErrBadSource, maxPluginBytes>>20)
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(filepath.Join(dst, rel), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm()|0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

func download(ctx context.Context, url, dst string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadSource, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: download: %v", ErrBadSource, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: download: HTTP %d", ErrBadSource, resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxTarballBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n > maxTarballBytes {
		return fmt.Errorf("%w: tarball larger than %d MB", ErrBadSource, maxTarballBytes>>20)
	}
	return nil
}

// npmPack downloads an npm package tarball with `npm pack`.
func npmPack(ctx context.Context, name, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "npm", "pack", name, "--pack-destination", dir, "--silent", "--ignore-scripts")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 500 {
			msg = msg[:500]
		}
		return "", fmt.Errorf("%w: npm pack %s: %v %s", ErrBadSource, name, err, msg)
	}
	ms, _ := filepath.Glob(filepath.Join(dir, "*.tgz"))
	if len(ms) != 1 {
		return "", fmt.Errorf("%w: npm pack %s produced %d tarballs", ErrBadSource, name, len(ms))
	}
	return ms[0], nil
}

// unpack extracts a (gzipped) tarball safely into dst and returns the
// plugin root: dst itself or its single top-level directory (npm packs
// everything under "package/").
func unpack(tarball, dst string) (string, error) {
	f, err := os.Open(tarball)
	if err != nil {
		return "", err
	}
	defer f.Close()
	br := bufio.NewReader(f)
	var r io.Reader = br
	if magic, _ := br.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrBadSource, err)
		}
		defer gz.Close()
		r = gz
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return "", err
	}
	tr := tar.NewReader(r)
	var total int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrBadSource, err)
		}
		name := filepath.Clean(filepath.FromSlash(hdr.Name))
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("%w: unsafe path %q in tarball", ErrBadSource, hdr.Name)
		}
		target := filepath.Join(dst, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return "", err
			}
		case tar.TypeReg:
			if total += hdr.Size; total > maxPluginBytes {
				return "", fmt.Errorf("%w: plugin larger than %d MB", ErrBadSource, maxPluginBytes>>20)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return "", err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode).Perm()|0o600)
			if err != nil {
				return "", err
			}
			if _, err := io.Copy(out, io.LimitReader(tr, hdr.Size)); err != nil {
				out.Close()
				return "", err
			}
			if err := out.Close(); err != nil {
				return "", err
			}
		default:
			// symlinks, devices, ...: skipped
		}
	}
	if _, err := os.Stat(filepath.Join(dst, process.ManifestFile)); err == nil {
		return dst, nil
	}
	ents, _ := os.ReadDir(dst)
	if len(ents) == 1 && ents[0].IsDir() {
		sub := filepath.Join(dst, ents[0].Name())
		if _, err := os.Stat(filepath.Join(sub, process.ManifestFile)); err == nil {
			return sub, nil
		}
	}
	return "", fmt.Errorf("%w: no %s in tarball", ErrBadSource, process.ManifestFile)
}
