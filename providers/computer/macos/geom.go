package macos

import (
	"fmt"
	"strings"

	"github.com/hedeqiang/crewbase/capabilities/computer"
)

// Global coordinates are those of the accessibility API and CGEvent: points,
// origin at the top-left of the main display, y down. Other displays have
// their own rectangles in that space, possibly with negative origins (a
// display left of or above the main one).

// toScreen converts a window point to global screen coordinates, checking
// that it lies inside the window and on a display.
func toScreen(win computer.Rect, x, y float64, screens []computer.Rect) (float64, float64, error) {
	if win.W <= 0 || win.H <= 0 {
		return 0, 0, fmt.Errorf("the window has no size; observe again")
	}
	if x < 0 || y < 0 || x >= win.W || y >= win.H {
		return 0, 0, fmt.Errorf("point (%g, %g) is outside the window (%g×%g points)", x, y, win.W, win.H)
	}
	gx, gy := win.X+x, win.Y+y
	if len(screens) > 0 && screenAt(screens, gx, gy) < 0 {
		return 0, 0, fmt.Errorf("point (%g, %g) of the window is off screen; move the window onto a display", x, y)
	}
	return gx, gy, nil
}

// screenAt is the index of the display containing the global point, or -1.
func screenAt(screens []computer.Rect, x, y float64) int {
	for i, s := range screens {
		if x >= s.X && x < s.X+s.W && y >= s.Y && y < s.Y+s.H {
			return i
		}
	}
	return -1
}

// screenOf is the display showing most of the window (0 when unknown).
func screenOf(screens []computer.Rect, win computer.Rect) int {
	best, area := 0, 0.0
	for i, s := range screens {
		w := min(s.X+s.W, win.X+win.W) - max(s.X, win.X)
		h := min(s.Y+s.H, win.Y+win.H) - max(s.Y, win.Y)
		if w > 0 && h > 0 && w*h > area {
			best, area = i, w*h
		}
	}
	return best
}

// centre of a rectangle.
func centre(r computer.Rect) (float64, float64) { return r.X + r.W/2, r.Y + r.H/2 }

// matchWindow picks the window-server window of the frame (an AX window's
// frame), or the frontmost normal window when none matches.
func matchWindow(wins []cgWindow, frame computer.Rect) (cgWindow, bool) {
	var best cgWindow
	bestD := -1.0
	for _, w := range wins {
		if w.Layer != 0 {
			continue
		}
		d := abs(w.Frame.X-frame.X) + abs(w.Frame.Y-frame.Y) + abs(w.Frame.W-frame.W) + abs(w.Frame.H-frame.H)
		if bestD < 0 || d < bestD {
			best, bestD = w, d
		}
	}
	if bestD < 0 {
		return cgWindow{}, false
	}
	if frame.W > 0 && bestD > 8 {
		// nothing matches the frame: the frontmost normal window
		for _, w := range wins {
			if w.Layer == 0 {
				return w, true
			}
		}
	}
	return best, true
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// matchTitle finds want among titles: exact, then case-insensitive, then
// prefix (case-insensitive; "…" and "..." count the same). -1 if none.
func matchTitle(titles []string, want string) int {
	want = strings.TrimSpace(want)
	if want == "" {
		return -1
	}
	for i, t := range titles {
		if t == want {
			return i
		}
	}
	nw := normTitle(want)
	for i, t := range titles {
		if normTitle(t) == nw {
			return i
		}
	}
	for i, t := range titles {
		if nt := normTitle(t); nt != "" && strings.HasPrefix(nt, nw) {
			return i
		}
	}
	return -1
}

func normTitle(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "…", "")
	s = strings.ReplaceAll(s, "...", "")
	return strings.TrimSpace(s)
}
