package browsertools

// Iframes in snapshots and actions. snapshotJS runs in every frame on its
// own (through browser.Session.EvalFrame, which reaches same-origin and
// cross-origin, out-of-process iframes alike); each frame numbers its
// elements locally and this file splices the frames into one outline with
// page-wide numbers, remembering which frame and local number every [n]
// stands for. A click inside a frame is aimed in the frame's own viewport
// and moved into page coordinates by adding the position of each
// enclosing iframe.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/hedeqiang/crewbase/capabilities/browser"
)

const (
	maxFrameDepth = 4
	maxFrames     = 24
)

// ref is what a page-wide element number [n] stands for.
type ref struct {
	frame string // "" = main frame
	local int    // number inside the frame
}

// frameLink is how a frame is reached from its parent.
type frameLink struct {
	parent string
	index  int // window.__crewbaseFrames[index] in parent
}

// pageRefs are the element numbers of an agent's latest snapshot.
type pageRefs struct {
	refs   []ref
	frames map[string]frameLink
}

func (t *bt) setRefs(agent string, pr *pageRefs) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.refs == nil {
		t.refs = map[string]*pageRefs{}
	}
	t.refs[agent] = pr
}

func (t *bt) lookup(agent string, n int) (ref, *pageRefs, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	pr := t.refs[agent]
	if pr == nil || n < 1 || n > len(pr.refs) {
		return ref{}, nil, false
	}
	return pr.refs[n-1], pr, true
}

// fullSnapshot snapshots the main frame and, recursively, its iframes.
func fullSnapshot(ctx context.Context, s *browser.Session) (snapshot, *pageRefs, error) {
	var top snapshot
	if err := s.EvalFrame(ctx, "", snapshotJS, &top); err != nil {
		return snapshot{}, nil, err
	}
	pr := &pageRefs{frames: map[string]frameLink{}}
	out := top
	lines, err := splice(ctx, s, "", top, "", 0, pr, &out)
	if err != nil {
		return snapshot{}, nil, err
	}
	out.Lines, out.Marks, out.Refs = lines, nil, len(pr.refs)
	return out, pr, nil
}

// splice renumbers the lines of frame's snapshot sn into page-wide refs and
// replaces each iframe placeholder with the iframe's own outline. Hints of
// the frames are merged into acc.
func splice(ctx context.Context, s *browser.Session, frame string, sn snapshot, indent string, depth int, pr *pageRefs, acc *snapshot) ([]string, error) {
	var out []string
	for i, l := range sn.Lines {
		m := 0
		if i < len(sn.Marks) {
			m = sn.Marks[i]
		}
		switch {
		case m > 0:
			pr.refs = append(pr.refs, ref{frame: frame, local: m})
			out = append(out, fmt.Sprintf("%s[%d]%s", indent, len(pr.refs), strings.TrimPrefix(l, fmt.Sprintf("[%d]", m))))
		case m < 0:
			k := -m - 1
			if depth >= maxFrameDepth || len(pr.frames) >= maxFrames {
				out = append(out, indent+l)
				continue
			}
			child, err := s.ChildFrame(ctx, frame, k)
			var cs snapshot
			if err == nil {
				err = s.EvalFrame(ctx, child, snapshotJS, &cs)
			}
			if errors.Is(err, browser.ErrTakenOver) || ctx.Err() != nil {
				return nil, err
			}
			if err != nil {
				out = append(out, indent+l) // out of reach (gone, or no frame support)
				continue
			}
			pr.frames[child] = frameLink{parent: frame, index: k}
			acc.Hints.Password = acc.Hints.Password || cs.Hints.Password
			acc.Hints.OTP = acc.Hints.OTP || cs.Hints.OTP
			acc.Hints.Captcha = acc.Hints.Captcha || cs.Hints.Captcha
			head := indent + l
			if o := origin(cs.URL); o != "" {
				head += " " + o
			}
			out = append(out, head)
			sub, err := splice(ctx, s, child, cs, indent+"  ", depth+1, pr, acc)
			if err != nil {
				return nil, err
			}
			out = append(out, sub...)
		default:
			out = append(out, indent+l)
		}
	}
	return out, nil
}

func origin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// toPage moves the point (x, y) of frame's viewport to page coordinates, and
// reports whether it is visible in every enclosing frame.
func toPage(ctx context.Context, s *browser.Session, pr *pageRefs, frame string, x, y float64) (float64, float64, bool, error) {
	visible := true
	for frame != "" {
		link, ok := pr.frames[frame]
		if !ok {
			return 0, 0, false, errStale
		}
		var o struct {
			Error string  `json:"error"`
			X     float64 `json:"x"`
			Y     float64 `json:"y"`
			W     float64 `json:"w"`
			H     float64 `json:"h"`
		}
		if err := s.EvalFrame(ctx, link.parent, fmt.Sprintf(frameOffsetJS, link.index), &o); err != nil {
			return 0, 0, false, err
		}
		if o.Error != "" {
			return 0, 0, false, errStale
		}
		x, y = x+o.X, y+o.Y
		if x < 0 || y < 0 || x > o.W || y > o.H {
			visible = false
		}
		frame = link.parent
	}
	return x, y, visible, nil
}

var errStale = errors.New("stale")

// reveal scrolls the iframes enclosing frame into view, outermost first.
// It is done from each parent (a scroll a cross-process iframe asks its
// parent for happens later, asynchronously), so the positions toPage reads
// afterwards are settled; the element itself then scrolls only within its
// own frame.
func reveal(ctx context.Context, s *browser.Session, pr *pageRefs, frame string) error {
	var chain []frameLink
	for frame != "" {
		link, ok := pr.frames[frame]
		if !ok {
			return errStale
		}
		chain = append(chain, link)
		frame = link.parent
	}
	for i := len(chain) - 1; i >= 0; i-- {
		var o struct {
			Error string `json:"error"`
		}
		if err := s.EvalFrame(ctx, chain[i].parent, fmt.Sprintf(revealFrameJS, chain[i].index), &o); err != nil {
			return err
		}
		if o.Error != "" {
			return errStale
		}
	}
	return nil
}
