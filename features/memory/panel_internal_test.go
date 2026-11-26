package memory

import (
	"strings"
	"testing"
)

const memFile = "# Memory\n\n- 2026-11-20: likes pnpm\n## Team\n- 2026-11-21: UI text in Chinese\nplain line\n"

func TestParseLines(t *testing.T) {
	ls := parseLines(memFile, false)
	if len(ls) != 3 {
		t.Fatalf("want 3 entries, got %d: %+v", len(ls), ls)
	}
	if ls[0].text != "likes pnpm" || ls[0].date != "2026-11-20" || ls[0].line != 3 || ls[0].sec != "" {
		t.Fatalf("first entry: %+v", ls[0])
	}
	if ls[1].sec != "Team" || ls[2].text != "plain line" {
		t.Fatalf("sections: %+v", ls)
	}
	team := parseLines("# Team memory\n- 2026-11-26 release on the first Tuesday (阿秋)\n", true)
	if len(team) != 1 || team[0].text != "release on the first Tuesday" || team[0].by != "阿秋" {
		t.Fatalf("team entry: %+v", team)
	}
}

func TestEdit(t *testing.T) {
	next, m, err := edit(memFile, 3, "- 2026-11-20: likes pnpm", false, func(m memLine) (string, bool) {
		return replaceText(m, "likes  pnpm,\nnot npm", false), false
	})
	if err != nil || m.text != "likes pnpm" {
		t.Fatalf("edit: %v %+v", err, m)
	}
	if !strings.Contains(next, "- 2026-11-20: likes pnpm, not npm\n") {
		t.Fatalf("edited file:\n%s", next)
	}
	// lines moved: found by its text
	moved := "# Memory\n\n- 2026-11-19: new first\n" + strings.TrimPrefix(memFile, "# Memory\n\n")
	next, _, err = edit(moved, 3, "- 2026-11-20: likes pnpm", false, func(memLine) (string, bool) { return "", true })
	if err != nil || strings.Contains(next, "likes pnpm") || !strings.Contains(next, "new first") {
		t.Fatalf("delete after a move: %v\n%s", err, next)
	}
	// changed meanwhile: refused
	if _, _, err := edit(memFile, 3, "- 2026-11-20: something else", false, func(memLine) (string, bool) { return "", true }); err == nil {
		t.Fatal("a stale line was changed")
	}
	// a heading is not an entry
	if _, _, err := edit(memFile, 4, "## Team", false, func(memLine) (string, bool) { return "", true }); err == nil {
		t.Fatal("a heading was deleted")
	}
	team := "# Team memory\n- 2026-11-26 release day (阿秋)\n"
	next, _, err = edit(team, 2, "- 2026-11-26 release day (阿秋)", true, func(m memLine) (string, bool) { return replaceText(m, "release on Tuesdays", true), false })
	if err != nil || !strings.Contains(next, "- 2026-11-26 release on Tuesdays (阿秋)") {
		t.Fatalf("team edit: %v\n%s", err, next)
	}
}
