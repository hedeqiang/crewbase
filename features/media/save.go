package media

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/hedeqiang/crewbase/kernel"
)

// Dir is the workspace folder generated files go to.
const Dir = "generated"

// Limits.
const (
	MaxImageBytes      = 40 << 20  // one generated image
	MaxVideoBytes      = 512 << 20 // one generated video
	MaxInputImageBytes = 20 << 20  // one picture sent to a model
	MaxInputImages     = 10
)

// extensions of the formats generated files may have. Anything else (SVG,
// HTML, unknown bytes) is refused: these files are shown in the app.
var imageExt = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp", "image/gif": ".gif"}
var videoExt = map[string]string{"video/mp4": ".mp4", "video/webm": ".webm"}

// sniff classifies the first bytes: a provider's claimed type is not
// trusted (a "video" that is really HTML must not be saved as one).
func sniff(head []byte) string {
	m := http.DetectContentType(head)
	if i := strings.IndexByte(m, ';'); i >= 0 {
		m = m[:i]
	}
	return m
}

// slug is a short file-name-safe summary of a prompt (ASCII words only;
// "" when nothing usable is left).
func slug(prompt string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(prompt) {
		switch {
		case r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		default:
			dash = true
		}
		if b.Len() >= 32 {
			break
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 32 {
		s = s[:32]
		if i := strings.LastIndexByte(s, '-'); i >= 12 {
			s = s[:i] // end on a whole word
		}
		s = strings.TrimRight(s, "-")
	}
	if len(s) < 3 {
		return ""
	}
	return s
}

// generatedDir returns workspace/generated, created if needed, after
// checking it really is inside the workspace (no symlink out of it).
func generatedDir(workspace string) (string, error) {
	if workspace == "" || !filepath.IsAbs(workspace) {
		return "", errors.New("the agent has no workspace")
	}
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(real, root+string(filepath.Separator)) {
		return "", fmt.Errorf("%s/ leaves the workspace", Dir)
	}
	return real, nil
}

// Created is emitted for every generated image or video saved (the
// library files it).
var Created = kernel.NewEmit[CreatedFile]("media/created")

// CreatedFile is a generated file of an agent.
type CreatedFile struct {
	Agent     string
	Session   string
	Workspace string
	Kind      string // image | video
	Prompt    string
	Saved     Saved
}

// Saved is a file written into the workspace.
type Saved struct {
	Path string `json:"path"` // workspace-relative, slash-separated
	Mime string `json:"mime"`
	Size int64  `json:"size"`
}

// Save writes r (at most max bytes) to generated/<time>-<slug>.<ext> in the
// workspace. kind is "image" or "video"; the type comes from the bytes.
func Save(workspace, kind, prompt string, r io.Reader, max int64, now time.Time) (Saved, error) {
	dir, err := generatedDir(workspace)
	if err != nil {
		return Saved{}, err
	}
	head := make([]byte, 512)
	n, err := io.ReadFull(r, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return Saved{}, err
	}
	head = head[:n]
	if n == 0 {
		return Saved{}, errors.New("the provider returned an empty file")
	}
	mime := sniff(head)
	exts := imageExt
	if kind == "video" {
		exts = videoExt
	}
	ext, ok := exts[mime]
	if !ok {
		return Saved{}, fmt.Errorf("the provider returned %s, not a supported %s format", mime, kind)
	}
	tmp, err := os.CreateTemp(dir, ".gen-*")
	if err != nil {
		return Saved{}, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	size, err := io.Copy(tmp, io.LimitReader(io.MultiReader(bytes.NewReader(head), r), max+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Saved{}, err
	}
	if size > max {
		return Saved{}, fmt.Errorf("the %s is larger than %d MB", kind, max>>20)
	}
	base := slug(prompt)
	if base == "" {
		base = kind
	}
	stem := now.Local().Format("20060102-150405") + "-" + base
	for i := 1; i <= 100; i++ {
		name := stem + ext
		if i > 1 {
			name = fmt.Sprintf("%s-%d%s", stem, i, ext)
		}
		final := filepath.Join(dir, name)
		// Link fails when the name is taken, so a file is never replaced.
		if err := os.Link(tmpName, final); err != nil {
			if os.IsExist(err) {
				continue
			}
			if _, serr := os.Lstat(final); serr == nil {
				continue
			}
			if rerr := os.Rename(tmpName, final); rerr != nil { // file systems without hard links
				return Saved{}, rerr
			}
		}
		_ = os.Chmod(final, 0o644)
		return Saved{Path: path.Join(Dir, name), Mime: mime, Size: size}, nil
	}
	return Saved{}, errors.New("too many files with the same name")
}

// ReadInput loads a workspace image to send to a model: inside the
// workspace (symlinks resolved), a raster image by its bytes, size-capped.
func ReadInput(workspace, rel string) (name, mime string, data []byte, err error) {
	c := path.Clean(strings.ReplaceAll(rel, "\\", "/"))
	if filepath.IsAbs(rel) {
		// absolute paths are accepted only when they point into the workspace
		if r, rerr := filepath.Rel(workspace, rel); rerr == nil {
			c = path.Clean(filepath.ToSlash(r))
		}
	}
	if c == "." || c == ".." || strings.HasPrefix(c, "../") || path.IsAbs(c) || strings.ContainsRune(c, 0) {
		return "", "", nil, fmt.Errorf("%s is not a path inside the workspace", rel)
	}
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", "", nil, err
	}
	real, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(c)))
	if err != nil {
		return "", "", nil, fmt.Errorf("%s: no such file", rel)
	}
	if !strings.HasPrefix(real, root+string(filepath.Separator)) {
		return "", "", nil, fmt.Errorf("%s leaves the workspace", rel)
	}
	f, err := os.Open(real)
	if err != nil {
		return "", "", nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		return "", "", nil, fmt.Errorf("%s is not a file", rel)
	}
	if st.Size() > MaxInputImageBytes {
		return "", "", nil, fmt.Errorf("%s is larger than %d MB", rel, MaxInputImageBytes>>20)
	}
	data, err = io.ReadAll(io.LimitReader(f, MaxInputImageBytes+1))
	if err != nil {
		return "", "", nil, err
	}
	mime = sniff(data)
	switch mime {
	case "image/png", "image/jpeg", "image/webp":
	default:
		return "", "", nil, fmt.Errorf("%s is %s; only PNG, JPEG and WebP images can be sent", rel, mime)
	}
	return path.Base(c), mime, data, nil
}
