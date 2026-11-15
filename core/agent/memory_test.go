package agent

import (
	"fmt"
	"strings"
	"testing"
)

func TestMemoryForPrompt(t *testing.T) {
	short := "# Memory\n\n- likes tabs\n"
	if MemoryForPrompt(short) != short {
		t.Fatal("short memory changed")
	}
	var b strings.Builder
	b.WriteString("# Memory\n\n- FIRST: the user is called Ada\n")
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&b, "- note %d about the project\n", i)
	}
	b.WriteString("- LAST: deploys happen on Fridays\n")
	got := MemoryForPrompt(b.String())
	if n := len([]rune(got)); n > MemoryHead+MemoryTail+300 {
		t.Fatalf("not bounded: %d", n)
	}
	for _, want := range []string{"FIRST: the user is called Ada", "LAST: deploys happen on Fridays", "memory_search finds them"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if strings.Contains(got, "- note 1000 about") {
		t.Fatal("middle kept")
	}
}
