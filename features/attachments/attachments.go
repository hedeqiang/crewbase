// Package attachments handles files attached to conversations: they are
// saved into the agent workspace (uploads/<date>/), the session log records
// only a reference, and right before each model request the `attachments`
// service loads them — bytes for files the model takes natively (images for
// vision models, PDFs, documents through a provider file API), extracted
// text plus the workspace path for everything else. The agent can always
// reach the original with its own file tools.
package attachments

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/kernel"
)

// Limits.
const (
	MaxFileBytes = 30 << 20 // per file (provider inline limits are ~32 MB)
	MaxPerInput  = 10
	// Dir is the workspace folder uploads go to.
	Dir = "uploads"
)

const (
	mimeDOCX = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	mimePPTX = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	mimeXLSX = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
)

// byExt maps extensions to MIME types. A fixed table (not the OS one) so
// the same file is classified the same way on every machine.
var byExt = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp",
	".bmp": "image/bmp", ".tif": "image/tiff", ".tiff": "image/tiff", ".heic": "image/heic", ".svg": "image/svg+xml",
	".pdf": "application/pdf",
	".mp4": "video/mp4", ".webm": "video/webm", ".mov": "video/quicktime",
	".docx": mimeDOCX, ".pptx": mimePPTX, ".xlsx": mimeXLSX,
	".doc": "application/msword", ".ppt": "application/vnd.ms-powerpoint", ".xls": "application/vnd.ms-excel",
	".txt": "text/plain", ".md": "text/markdown", ".markdown": "text/markdown", ".csv": "text/csv", ".tsv": "text/tab-separated-values",
	".json": "application/json", ".yaml": "application/yaml", ".yml": "application/yaml", ".toml": "application/toml",
	".xml": "application/xml", ".html": "text/html", ".htm": "text/html", ".css": "text/css", ".log": "text/plain",
	".go": "text/x-go", ".py": "text/x-python", ".js": "text/javascript", ".ts": "text/x-typescript", ".tsx": "text/x-typescript",
	".jsx": "text/javascript", ".java": "text/x-java", ".rs": "text/x-rust", ".c": "text/x-c", ".h": "text/x-c",
	".cpp": "text/x-c++", ".rb": "text/x-ruby", ".php": "text/x-php", ".swift": "text/x-swift", ".kt": "text/x-kotlin",
	".sql": "text/x-sql", ".sh": "application/x-sh", ".ini": "text/plain", ".env.example": "text/plain",
}

// MimeOf classifies a file by name, checking the bytes for images and
// PDFs (a renamed file is not trusted as an image).
func MimeOf(name string, data []byte) string {
	m := byExt[strings.ToLower(path.Ext(name))]
	sniff := http.DetectContentType(data)
	switch {
	case strings.HasPrefix(m, "image/") && m != "image/svg+xml" && m != "image/heic" && m != "image/tiff" && m != "image/bmp":
		if sniff != m {
			if strings.HasPrefix(sniff, "image/") {
				return sniff
			}
			return "application/octet-stream"
		}
	case m == "application/pdf", m == "video/mp4", m == "video/webm":
		if sniff != m {
			return "application/octet-stream"
		}
	case m == "":
		switch {
		case strings.HasPrefix(sniff, "image/"), sniff == "application/pdf":
			return sniff
		case strings.HasPrefix(sniff, "text/plain") && utf8.Valid(data):
			return "text/plain"
		}
		return "application/octet-stream"
	}
	return m
}

// safeName keeps a file name usable on every OS.
func safeName(name string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.TrimLeft(strings.TrimSpace(name), ".")
	if name == "" {
		name = "file"
	}
	if r := []rune(name); len(r) > 120 {
		ext := path.Ext(name)
		name = string(r[:120-len([]rune(ext))]) + ext
	}
	return name
}

// Save writes data to <workspace>/uploads/<date>/<name>, keeping existing
// files (a numbered name is picked; an identical file is reused).
func Save(workspace, name string, data []byte, now time.Time) (llm.Attachment, error) {
	if len(data) == 0 {
		return llm.Attachment{}, errors.New("the file is empty")
	}
	if len(data) > MaxFileBytes {
		return llm.Attachment{}, fmt.Errorf("the file is larger than %d MB", MaxFileBytes>>20)
	}
	name = safeName(name)
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	day := now.Format("2006-01-02")
	dir := filepath.Join(workspace, Dir, day)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return llm.Attachment{}, err
	}
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 0; i < 1000; i++ {
		n := name
		if i > 0 {
			n = fmt.Sprintf("%s (%d)%s", stem, i, ext)
		}
		p := filepath.Join(dir, n)
		if old, err := os.ReadFile(p); err == nil {
			if s := sha256.Sum256(old); s == sum {
				return attachment(path.Join(Dir, day, n), name, data, hash), nil
			}
			continue
		}
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return llm.Attachment{}, err
		}
		if _, err := f.Write(data); err != nil {
			f.Close()
			os.Remove(p)
			return llm.Attachment{}, err
		}
		if err := f.Close(); err != nil {
			return llm.Attachment{}, err
		}
		return attachment(path.Join(Dir, day, n), name, data, hash), nil
	}
	return llm.Attachment{}, errors.New("too many files with this name")
}

func attachment(rel, name string, data []byte, hash string) llm.Attachment {
	return llm.Attachment{Path: rel, Name: name, Mime: MimeOf(name, data), Size: int64(len(data)), SHA256: hash}
}

// resolveIn joins a workspace-relative path, refusing escapes and symlinks
// that leave the workspace.
func resolveIn(workspace, rel string) (string, error) {
	c := path.Clean(strings.ReplaceAll(rel, "\\", "/"))
	if c == "." || c == ".." || strings.HasPrefix(c, "../") || path.IsAbs(c) || strings.ContainsRune(c, 0) {
		return "", fmt.Errorf("invalid path %q", rel)
	}
	p := filepath.Join(workspace, filepath.FromSlash(c))
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(real, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q leaves the workspace", rel)
	}
	return real, nil
}

// Resolve checks a client-sent attachment against the workspace and
// rebuilds it from the file on disk (the client's mime / size are not
// trusted). Only files under uploads/ may be attached.
func Resolve(workspace string, rel, name string) (llm.Attachment, error) {
	if !strings.HasPrefix(path.Clean(rel), Dir+"/") {
		return llm.Attachment{}, fmt.Errorf("attachments must be uploaded first (%s)", rel)
	}
	p, err := resolveIn(workspace, rel)
	if err != nil {
		return llm.Attachment{}, err
	}
	info, err := os.Stat(p)
	if err != nil || info.IsDir() {
		return llm.Attachment{}, fmt.Errorf("attachment %s not found", rel)
	}
	if info.Size() > MaxFileBytes {
		return llm.Attachment{}, fmt.Errorf("attachment %s is too large", rel)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return llm.Attachment{}, err
	}
	if name == "" {
		name = path.Base(rel)
	}
	sum := sha256.Sum256(data)
	return attachment(path.Clean(rel), safeName(name), data, hex.EncodeToString(sum[:])), nil
}

// ---------------------------------------------------------------- service

// OCRKey is the optional `ocr` service (DeepSeek OCR, features/deepseek).
const OCRKey = "ocr"

// OCR recognizes the text of scanned documents and images.
type OCR interface {
	OCRAvailable(ctx context.Context) bool
	Recognize(ctx context.Context, name, mime string, data []byte) (string, error)
}

// ocrBudget bounds recognition while a request is being prepared.
const ocrBudget = 3 * time.Minute

// Service is the `attachments` service (agentloop.Hydrator).
type Service struct {
	mu    sync.Mutex
	texts map[string]textEntry // sha256 → extracted text
	order []string
	// OCR is looked up per request (it may load after this plugin).
	OCR func() OCR

	failed map[string]time.Time // sha256 → last failed recognition
}

type textEntry struct {
	text      string
	ok        bool
	truncated bool
}

const cacheEntries = 64

// NewService makes a service with an empty text cache.
func NewService() *Service {
	return &Service{texts: map[string]textEntry{}, failed: map[string]time.Time{}}
}

var _ agentloop.Hydrator = (*Service)(nil)

func (s *Service) extract(at *llm.Attachment, data []byte) textEntry {
	key := at.SHA256
	if key != "" {
		s.mu.Lock()
		e, hit := s.texts[key]
		s.mu.Unlock()
		if hit {
			return e
		}
	}
	t, ok, _ := Extract(at.Name, at.Mime, data)
	t, cut := clipText(strings.TrimSpace(t))
	e := textEntry{text: t, ok: ok && t != "", truncated: cut}
	if key != "" {
		s.mu.Lock()
		if _, dup := s.texts[key]; !dup {
			s.texts[key] = e
			s.order = append(s.order, key)
			if len(s.order) > cacheEntries {
				delete(s.texts, s.order[0])
				s.order = s.order[1:]
			}
		}
		s.mu.Unlock()
	}
	return e
}

// Hydrate fills Data and Text of every attachment part in req.
func (s *Service) Hydrate(ctx context.Context, workspace string, ad llm.Adapter, model string, req *llm.Request) {
	fa, _ := ad.(llm.FileAccepter)
	for i := range req.Messages {
		for j := range req.Messages[i].Parts {
			p := &req.Messages[i].Parts[j]
			if p.Type != llm.PartAttachment || p.Attachment == nil {
				continue
			}
			at := *p.Attachment // copy: never write into the projected log
			p.Attachment = &at
			s.fill(ctx, workspace, fa, model, &at)
		}
	}
}

func (s *Service) fill(ctx context.Context, workspace string, fa llm.FileAccepter, model string, at *llm.Attachment) {
	where := fmt.Sprintf("工作区路径 %s", at.Path)
	p, err := resolveIn(workspace, at.Path)
	var data []byte
	if err == nil {
		data, err = os.ReadFile(p)
	}
	if err != nil {
		at.Text = fmt.Sprintf("[附件 %s 已不在工作区（%s）]", at.Name, at.Path)
		return
	}
	native := fa != nil && fa.AcceptsFile(model, at.Mime)
	if native {
		at.Data = data
	}
	if at.IsImage() || strings.HasPrefix(at.Mime, "image/") {
		if native {
			at.Text = fmt.Sprintf("[图片 %s，%s]", at.Name, where)
		} else if t, ok := s.recognize(ctx, workspace, at, data); ok {
			at.Text = fmt.Sprintf("[图片 %s（%s）；当前模型不能查看图片，以下是 OCR 识别出的文字]\n<file name=%q>\n%s\n</file>", at.Name, where, at.Name, t)
		} else {
			at.Text = fmt.Sprintf("[图片 %s 已保存（%s）；当前模型不能查看图片，需要时请让用户换用支持图片的模型]", at.Name, where)
		}
		return
	}
	e := s.extract(at, data)
	if !native && needsOCR(at.Mime, e) {
		if t, ok := s.recognize(ctx, workspace, at, data); ok {
			t, cut := clipText(t)
			e = textEntry{text: t, ok: true, truncated: cut}
		}
	}
	head := fmt.Sprintf("[附件 %s（%s，%s）]", at.Name, where, humanSize(at.Size))
	full := head + "\n[无法直接读取此类文件的文字；文件已在工作区，可用工具处理]"
	switch {
	case e.ok && e.truncated:
		full = fmt.Sprintf("%s 文字内容如下：\n<file name=%q>\n%s\n</file>\n[内容过长，只附上前 %d 字；完整文件在工作区，可用文件工具读取]", head, at.Name, e.text, MaxTextRunes)
	case e.ok:
		full = fmt.Sprintf("%s 文字内容如下（已完整提供，无需再读取文件）：\n<file name=%q>\n%s\n</file>", head, at.Name, e.text)
	}
	if native {
		at.Text, at.Alt = head, full // the model reads the file itself
		return
	}
	at.Text = full
}

// needsOCR: scanned PDFs (almost no text layer) and legacy Word /
// PowerPoint files, which have no text form here.
func needsOCR(mime string, e textEntry) bool {
	switch mime {
	case "application/pdf":
		return len([]rune(strings.TrimSpace(e.text))) < 40
	case "application/msword", "application/vnd.ms-powerpoint":
		return !e.ok
	}
	return false
}

// recognize returns the OCR text of an attachment, kept next to the
// uploads (uploads/.ocr/<sha256>.txt) so a file is recognized (and billed)
// once. A failure is not retried for ten minutes.
func (s *Service) recognize(ctx context.Context, workspace string, at *llm.Attachment, data []byte) (string, bool) {
	if s.OCR == nil || at.SHA256 == "" || workspace == "" {
		return "", false
	}
	cache := filepath.Join(workspace, "uploads", ".ocr", at.SHA256+".txt")
	if b, err := os.ReadFile(cache); err == nil {
		return string(b), len(b) > 0
	}
	s.mu.Lock()
	last, bad := s.failed[at.SHA256]
	s.mu.Unlock()
	if bad && time.Since(last) < 10*time.Minute {
		return "", false
	}
	o := s.OCR()
	if o == nil || !o.OCRAvailable(ctx) {
		return "", false
	}
	rctx, cancel := context.WithTimeout(ctx, ocrBudget)
	defer cancel()
	t, err := o.Recognize(rctx, at.Name, at.Mime, data)
	t = strings.TrimSpace(t)
	if err != nil || t == "" {
		s.mu.Lock()
		s.failed[at.SHA256] = time.Now()
		s.mu.Unlock()
		if err != nil {
			slog.Warn("attachments: OCR failed", "file", at.Name, "err", err)
		}
		return "", false
	}
	if os.MkdirAll(filepath.Dir(cache), 0o700) == nil {
		_ = os.WriteFile(cache, []byte(t), 0o600)
	}
	return t, true
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// Plugin provides the attachments service.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/attachments",
	Description: "Conversation attachments: load images, PDFs, Office and text files into model requests.",
	Apply: func(c *kernel.Context) error {
		s := NewService()
		s.OCR = func() OCR {
			o, _ := kernel.Lookup[OCR](c, OCRKey)
			return o
		}
		return c.Provide(agentloop.AttachmentsKey, s)
	},
})
