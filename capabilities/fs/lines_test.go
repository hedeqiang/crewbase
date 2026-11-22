package fs

import (
	"reflect"
	"testing"
)

func TestChangedLines(t *testing.T) {
	old := []byte("a\nb\nc\nd\n")
	for _, c := range []struct {
		new  string
		want [][2]int
	}{
		{"a\nb\nc\nd\n", nil},
		{"a\nB\nc\nd\n", [][2]int{{2, 2}}},
		{"a\nB\nC\nd\n", [][2]int{{2, 3}}},
		{"a\nb\nx\nc\nd\n", [][2]int{{3, 2}}}, // inserted before line 3
		{"a\nd\n", [][2]int{{2, 3}}},
		{"z\n", [][2]int{{1, 4}}},
	} {
		if got := ChangedLines(old, []byte(c.new)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: %v want %v", c.new, got, c.want)
		}
	}
	b := []byte("one\ntwo\nthree\nfour\n")
	if got := EditLines(b, 4, len("two\nthree")); !reflect.DeepEqual(got, [][2]int{{2, 3}}) {
		t.Fatal(got)
	}
	if got := EditLines(b, 4, len("two\n")); !reflect.DeepEqual(got, [][2]int{{2, 2}}) {
		t.Fatal(got)
	}
	if got := EditLines(b, 0, 2); !reflect.DeepEqual(got, [][2]int{{1, 1}}) {
		t.Fatal(got)
	}
}
