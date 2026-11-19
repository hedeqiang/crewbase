package console

// A line diff (Myers' O(ND) algorithm) for the 改动 view: an old and a new
// text become hunks of context, removed and added lines with their line
// numbers, as a unified diff shows them.

import "strings"

// DiffLine is one line of a hunk.
type DiffLine struct {
	// T is " " (context), "-" (removed) or "+" (added).
	T    string `json:"t"`
	Old  int    `json:"o,omitempty"` // line number in the old text (context, removed)
	New  int    `json:"n,omitempty"` // line number in the new text (context, added)
	Text string `json:"s"`
}

// Hunk is a run of changes with the context around them.
type Hunk struct {
	OldStart int        `json:"oldStart"`
	OldLines int        `json:"oldLines"`
	NewStart int        `json:"newStart"`
	NewLines int        `json:"newLines"`
	Lines    []DiffLine `json:"lines"`
}

// Limits: past them a diff is summarised (whole file replaced) rather than
// searched line by line.
const (
	maxDiffLines = 40000
	maxDiffEdits = 4000
)

// splitLines splits text into lines without their newlines.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

type edit struct {
	t    byte // ' ', '-', '+'
	a, b int  // indexes into the old and new lines (the side the edit has)
}

// diffEdits is the shortest edit script from a to b.
func diffEdits(a, b []string) []edit {
	// a common start and end need no search
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	out := make([]edit, 0, len(a)+len(b))
	for i := 0; i < pre; i++ {
		out = append(out, edit{' ', i, i})
	}
	mid := myers(a[pre:len(a)-suf], b[pre:len(b)-suf])
	for _, e := range mid {
		out = append(out, edit{e.t, e.a + pre, e.b + pre})
	}
	for i := 0; i < suf; i++ {
		out = append(out, edit{' ', len(a) - suf + i, len(b) - suf + i})
	}
	return out
}

func replaceAll(a, b []string) []edit {
	out := make([]edit, 0, len(a)+len(b))
	for i := range a {
		out = append(out, edit{'-', i, 0})
	}
	for j := range b {
		out = append(out, edit{'+', 0, j})
	}
	return out
}

func myers(a, b []string) []edit {
	n, m := len(a), len(b)
	if n == 0 && m == 0 {
		return nil
	}
	if n+m > maxDiffLines {
		return replaceAll(a, b)
	}
	max := n + m
	if max > maxDiffEdits {
		max = maxDiffEdits
	}
	off := max
	v := make([]int, 2*max+2)
	var trace [][]int
	found := -1
	for d := 0; d <= max; d++ {
		snap := make([]int, len(v))
		copy(snap, v)
		trace = append(trace, snap)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1] // down: an insertion
			} else {
				x = v[off+k-1] + 1 // right: a deletion
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[off+k] = x
			if x >= n && y >= m {
				found = d
				break
			}
		}
		if found >= 0 {
			break
		}
	}
	if found < 0 {
		return replaceAll(a, b)
	}
	// walk back through the snapshots
	var rev []edit
	x, y := n, m
	for d := found; d > 0; d-- {
		vd := trace[d]
		k := x - y
		var pk int
		if k == -d || (k != d && vd[off+k-1] < vd[off+k+1]) {
			pk = k + 1
		} else {
			pk = k - 1
		}
		px := vd[off+pk]
		py := px - pk
		for x > px && y > py {
			x--
			y--
			rev = append(rev, edit{' ', x, y})
		}
		if x == px {
			y--
			rev = append(rev, edit{'+', x, y})
		} else {
			x--
			rev = append(rev, edit{'-', x, y})
		}
	}
	for x > 0 && y > 0 {
		x--
		y--
		rev = append(rev, edit{' ', x, y})
	}
	out := make([]edit, len(rev))
	for i := range rev {
		out[i] = rev[len(rev)-1-i]
	}
	return out
}

// Diff is the hunks from old to new text with ctx lines of context, and
// the number of added and removed lines.
func Diff(oldText, newText string, ctx int) (hunks []Hunk, added, removed int) {
	a, b := splitLines(oldText), splitLines(newText)
	es := diffEdits(a, b)
	// numbered lines
	lines := make([]DiffLine, len(es))
	for i, e := range es {
		switch e.t {
		case ' ':
			lines[i] = DiffLine{T: " ", Old: e.a + 1, New: e.b + 1, Text: a[e.a]}
		case '-':
			lines[i] = DiffLine{T: "-", Old: e.a + 1, Text: a[e.a]}
			removed++
		case '+':
			lines[i] = DiffLine{T: "+", New: e.b + 1, Text: b[e.b]}
			added++
		}
	}
	if added == 0 && removed == 0 {
		return nil, 0, 0
	}
	// group changes that lie within 2*ctx lines of each other
	i := 0
	for i < len(lines) {
		for i < len(lines) && lines[i].T == " " {
			i++
		}
		if i >= len(lines) {
			break
		}
		start := i - ctx
		if start < 0 {
			start = 0
		}
		end := i
		for j := i; j < len(lines); j++ {
			if lines[j].T != " " {
				end = j
				continue
			}
			if j-end > 2*ctx {
				break
			}
		}
		stop := end + ctx + 1
		if stop > len(lines) {
			stop = len(lines)
		}
		h := Hunk{Lines: append([]DiffLine(nil), lines[start:stop]...)}
		for _, l := range h.Lines {
			if l.T != "+" {
				if h.OldStart == 0 {
					h.OldStart = l.Old
				}
				h.OldLines++
			}
			if l.T != "-" {
				if h.NewStart == 0 {
					h.NewStart = l.New
				}
				h.NewLines++
			}
		}
		hunks = append(hunks, h)
		i = stop
	}
	return hunks, added, removed
}
