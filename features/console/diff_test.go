package console

import (
	"math/rand"
	"strings"
	"testing"
)

// apply rebuilds the new text from the old one and a diff's edits, which
// proves the edit script is right.
func apply(a, b []string, es []edit) []string {
	var out []string
	for _, e := range es {
		switch e.t {
		case ' ':
			out = append(out, a[e.a])
		case '+':
			out = append(out, b[e.b])
		}
	}
	return out
}

func TestDiffEditsRebuild(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	words := []string{"a", "b", "c", "d", "e"}
	gen := func() []string {
		n := r.Intn(30)
		out := make([]string, n)
		for i := range out {
			out[i] = words[r.Intn(len(words))]
		}
		return out
	}
	for i := 0; i < 2000; i++ {
		a, b := gen(), gen()
		es := diffEdits(a, b)
		got := apply(a, b, es)
		if strings.Join(got, "\n") != strings.Join(b, "\n") {
			t.Fatalf("a=%v b=%v got=%v", a, b, got)
		}
		// every old line is kept or removed exactly once, in order
		next := 0
		for _, e := range es {
			if e.t != '+' {
				if e.a != next {
					t.Fatalf("old line order: %v", es)
				}
				next++
			}
		}
		if next != len(a) {
			t.Fatalf("old lines lost: %v", es)
		}
	}
}

func TestDiffIsMinimal(t *testing.T) {
	a := splitLines("one\ntwo\nthree\nfour\n")
	b := splitLines("one\n2\nthree\nfour\nfive\n")
	es := diffEdits(a, b)
	var add, del int
	for _, e := range es {
		switch e.t {
		case '+':
			add++
		case '-':
			del++
		}
	}
	if add != 2 || del != 1 {
		t.Fatalf("+%d -%d %v", add, del, es)
	}
}

func TestDiffHunks(t *testing.T) {
	var old, nu []string
	for i := 1; i <= 40; i++ {
		old = append(old, "line "+itoa(i))
		nu = append(nu, "line "+itoa(i))
	}
	nu[4] = "changed 5"                                               // near the top
	nu = append(nu[:30], append([]string{"inserted"}, nu[30:]...)...) // far below: a second hunk
	hs, add, del := Diff(strings.Join(old, "\n")+"\n", strings.Join(nu, "\n")+"\n", 3)
	if add != 2 || del != 1 || len(hs) != 2 {
		t.Fatalf("+%d -%d hunks=%d", add, del, len(hs))
	}
	h := hs[0]
	if h.OldStart != 2 || h.NewStart != 2 || h.OldLines != 7 || h.NewLines != 7 {
		t.Fatalf("hunk 1 %+v", h)
	}
	if h.Lines[3].T != "-" || h.Lines[3].Old != 5 || h.Lines[4].T != "+" || h.Lines[4].New != 5 {
		t.Fatalf("hunk 1 lines %+v", h.Lines)
	}
	if hs[1].Lines[3].T != "+" || hs[1].Lines[3].Text != "inserted" || hs[1].Lines[3].New != 31 {
		t.Fatalf("hunk 2 %+v", hs[1].Lines)
	}
	// a new file is one hunk of additions; no change, no hunks
	if hs, add, _ := Diff("", "a\nb\n", 3); len(hs) != 1 || add != 2 || hs[0].OldStart != 0 || hs[0].NewStart != 1 {
		t.Fatalf("new file %+v", hs)
	}
	if hs, _, _ := Diff("same\n", "same\n", 3); hs != nil {
		t.Fatal("no change gave hunks")
	}
	// CRLF and a missing last newline are not changes
	if hs, _, _ := Diff("a\r\nb\r\n", "a\nb", 3); hs != nil {
		t.Fatalf("line endings: %+v", hs)
	}
}

func TestDiffLargeFallsBack(t *testing.T) {
	a := make([]string, 6000)
	b := make([]string, 6000)
	for i := range a {
		a[i] = "a" + itoa(i)
		b[i] = "b" + itoa(i)
	}
	_, add, del := Diff(strings.Join(a, "\n"), strings.Join(b, "\n"), 3)
	if add != 6000 || del != 6000 {
		t.Fatalf("+%d -%d", add, del)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var s []byte
	for i > 0 {
		s = append([]byte{byte('0' + i%10)}, s...)
		i /= 10
	}
	return string(s)
}
