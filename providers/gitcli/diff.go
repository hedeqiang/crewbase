package gitcli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/hedeqiang/crewbase/capabilities/vcs"
)

// maxUntracked caps an untracked file shown as an addition.
const maxUntracked = 1 << 20

// Diff implements vcs.Provider.
func (g *Git) Diff(ctx context.Context, dir string, o vcs.DiffOptions) ([]vcs.FileDiff, error) {
	ctxLines := o.Context
	if ctxLines <= 0 {
		ctxLines = 3
	}
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", "--src-prefix=a/", "--dst-prefix=b/",
		"-U" + strconv.Itoa(ctxLines)}
	switch o.Side {
	case "", vcs.DiffWork:
	case vcs.DiffStaged:
		args = append(args, "--cached")
	case vcs.DiffHead:
		if g.hasHead(ctx, dir) {
			args = append(args, "HEAD")
		} else {
			args = append(args, "--cached") // nothing committed: all is new
		}
	case vcs.DiffRange:
		if err := checkRev(o.Base); err != nil {
			return nil, err
		}
		head := o.Head
		if head == "" {
			head = "HEAD"
		}
		if err := checkRev(head); err != nil {
			return nil, err
		}
		args = append(args, o.Base+"..."+head)
	default:
		return nil, fmt.Errorf("unknown diff side %q", o.Side)
	}
	args = append(args, "--")
	args = append(args, o.Paths...)
	r, err := g.run(ctx, dir, opts{}, args...)
	if err != nil {
		return nil, err
	}
	out := ParseDiff(r.out)
	if o.Untracked && (o.Side == "" || o.Side == vcs.DiffWork || o.Side == vcs.DiffHead) {
		u, err := g.untracked(ctx, dir, o.Paths)
		if err != nil {
			return nil, err
		}
		out = append(out, u...)
	}
	return out, nil
}

func (g *Git) hasHead(ctx context.Context, dir string) bool {
	_, err := g.run(ctx, dir, opts{}, "rev-parse", "--verify", "--quiet", "HEAD")
	return err == nil
}

// untracked are the untracked files as whole-file additions.
func (g *Git) untracked(ctx context.Context, dir string, paths []string) ([]vcs.FileDiff, error) {
	r, err := g.run(ctx, dir, opts{}, append([]string{"ls-files", "--others", "--exclude-standard", "-z", "--"}, paths...)...)
	if err != nil {
		return nil, err
	}
	var out []vcs.FileDiff
	for _, p := range strings.Split(strings.TrimRight(string(r.out), "\x00"), "\x00") {
		if p == "" {
			continue
		}
		out = append(out, untrackedDiff(dir, p))
	}
	return out, nil
}

func untrackedDiff(dir, p string) vcs.FileDiff {
	f := vcs.FileDiff{Path: p, Status: vcs.StUntracked, Hunks: []vcs.Hunk{}}
	full := filepath.Join(dir, filepath.FromSlash(p))
	st, err := os.Lstat(full)
	if err != nil || !st.Mode().IsRegular() || st.Size() > maxUntracked {
		f.Binary = err == nil && st.Mode().IsRegular()
		return f
	}
	b, err := os.ReadFile(full)
	if err != nil || bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b) {
		f.Binary = true
		return f
	}
	if len(b) == 0 {
		return f
	}
	text := string(b)
	noEOL := !strings.HasSuffix(text, "\n")
	ls := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	h := vcs.Hunk{Header: fmt.Sprintf("@@ -0,0 +1,%d @@", len(ls)), NewStart: 1, NewLines: len(ls)}
	for i, l := range ls {
		h.Lines = append(h.Lines, vcs.Line{Kind: "+", Text: l, New: i + 1})
	}
	if noEOL {
		h.Lines[len(h.Lines)-1].NoEOL = true
	}
	h.ID = hunkID(p, h.Lines)
	f.Hunks = []vcs.Hunk{h}
	f.Added = len(ls)
	return f
}

// ParseDiff reads a unified git diff (--no-renames, a/ b/ prefixes).
func ParseDiff(b []byte) []vcs.FileDiff {
	var out []vcs.FileDiff
	var f *vcs.FileDiff
	var h *vcs.Hunk
	var head strings.Builder
	oldN, newN := 0, 0
	flush := func() {
		if f == nil {
			return
		}
		if h != nil {
			h.ID = hunkID(f.Path, h.Lines)
			f.Hunks = append(f.Hunks, *h)
			h = nil
		}
		f.Head = head.String()
		out = append(out, *f)
		f = nil
	}
	for _, l := range strings.Split(string(b), "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			flush()
			head.Reset()
			head.WriteString(l + "\n")
			f = &vcs.FileDiff{Status: vcs.StModified, Hunks: []vcs.Hunk{}}
			// diff --git a/x b/x: the path is the b/ half (names with
			// spaces: the --- / +++ lines below correct it)
			if i := strings.Index(l, " b/"); i >= 0 {
				f.Path = l[i+3:]
			}
		case f == nil:
		case h == nil && strings.HasPrefix(l, "--- "):
			head.WriteString(l + "\n")
			if p := strings.TrimPrefix(l, "--- "); p != "/dev/null" {
				f.Orig = strings.TrimPrefix(unquote(p), "a/")
			}
		case h == nil && strings.HasPrefix(l, "+++ "):
			head.WriteString(l + "\n")
			if p := strings.TrimPrefix(l, "+++ "); p != "/dev/null" {
				f.Path = strings.TrimPrefix(unquote(p), "b/")
			} else if f.Orig != "" {
				f.Path = f.Orig
			}
			if f.Orig == f.Path {
				f.Orig = ""
			}
		case h == nil && strings.HasPrefix(l, "new file mode"):
			head.WriteString(l + "\n")
			f.Status = vcs.StAdded
		case h == nil && strings.HasPrefix(l, "deleted file mode"):
			head.WriteString(l + "\n")
			f.Status = vcs.StDeleted
		case strings.HasPrefix(l, "Binary files ") || strings.HasPrefix(l, "GIT binary patch"):
			f.Binary = true
		case strings.HasPrefix(l, "@@ "):
			if h != nil {
				h.ID = hunkID(f.Path, h.Lines)
				f.Hunks = append(f.Hunks, *h)
			}
			h = &vcs.Hunk{Header: l}
			h.OldStart, h.OldLines, h.NewStart, h.NewLines = parseHeader(l)
			oldN, newN = h.OldStart, h.NewStart
		case h != nil && l != "" && (l[0] == ' ' || l[0] == '+' || l[0] == '-'):
			ln := vcs.Line{Kind: l[:1], Text: l[1:]}
			switch l[0] {
			case ' ':
				ln.Old, ln.New = oldN, newN
				oldN++
				newN++
			case '+':
				ln.New = newN
				newN++
				f.Added++
			case '-':
				ln.Old = oldN
				oldN++
				f.Removed++
			}
			h.Lines = append(h.Lines, ln)
		case h != nil && strings.HasPrefix(l, `\ `):
			if n := len(h.Lines); n > 0 {
				h.Lines[n-1].NoEOL = true
			}
		case h == nil:
			head.WriteString(l + "\n")
		}
	}
	flush()
	return out
}

func unquote(p string) string {
	if strings.HasPrefix(p, `"`) {
		if s, err := strconv.Unquote(p); err == nil {
			return s
		}
	}
	return p
}

func parseHeader(l string) (os, ol, ns, nl int) {
	// @@ -a,b +c,d @@ …
	f := strings.Fields(l)
	if len(f) < 3 {
		return
	}
	os, ol = rng(strings.TrimPrefix(f[1], "-"))
	ns, nl = rng(strings.TrimPrefix(f[2], "+"))
	return
}

func rng(s string) (int, int) {
	a, b, ok := strings.Cut(s, ",")
	x, _ := strconv.Atoi(a)
	if !ok {
		return x, 1
	}
	y, _ := strconv.Atoi(b)
	return x, y
}

// hunkID is stable while the hunk's lines are (not their numbers).
func hunkID(path string, ls []vcs.Line) string {
	h := sha256.New()
	h.Write([]byte(path))
	for _, l := range ls {
		h.Write([]byte{0})
		h.Write([]byte(l.Kind))
		h.Write([]byte(l.Text))
	}
	return "h" + hex.EncodeToString(h.Sum(nil))[:12]
}

// patchOf is one hunk of f as a patch git apply takes.
func patchOf(f vcs.FileDiff, h vcs.Hunk) []byte {
	var b strings.Builder
	b.WriteString(f.Head)
	b.WriteString(h.Header + "\n")
	for _, l := range h.Lines {
		b.WriteString(l.Kind + l.Text + "\n")
		if l.NoEOL {
			b.WriteString("\\ No newline at end of file\n")
		}
	}
	return []byte(b.String())
}

// ApplyHunk implements vcs.Provider.
func (g *Git) ApplyHunk(ctx context.Context, dir, path, hunkID, action string) error {
	side := vcs.DiffWork
	if action == vcs.HunkUnstage {
		side = vcs.DiffStaged
	}
	fs, err := g.Diff(ctx, dir, vcs.DiffOptions{Side: side, Paths: []string{path}, Untracked: true})
	if err != nil {
		return err
	}
	for _, f := range fs {
		if f.Path != path {
			continue
		}
		for _, h := range f.Hunks {
			if h.ID != hunkID {
				continue
			}
			if f.Status == vcs.StUntracked {
				// an untracked file is one hunk: the whole file
				switch action {
				case vcs.HunkStage:
					return g.Stage(ctx, dir, []string{path})
				case vcs.HunkDiscard:
					return removeIn(dir, path)
				}
			}
			args := []string{"apply", "--recount", "--whitespace=nowarn"}
			switch action {
			case vcs.HunkStage:
				args = append(args, "--cached")
			case vcs.HunkUnstage:
				args = append(args, "--cached", "-R")
			case vcs.HunkDiscard:
				args = append(args, "-R")
			default:
				return fmt.Errorf("unknown hunk action %q", action)
			}
			_, err := g.run(ctx, dir, opts{write: true, lock: g.key(ctx, dir), stdin: patchOf(f, h)}, append(args, "-")...)
			return err
		}
	}
	return errors.Join(vcs.ErrStaleHunk, fmt.Errorf("%s: no hunk %s", path, hunkID))
}
