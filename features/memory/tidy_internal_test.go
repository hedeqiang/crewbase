package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/features/schedule"
	"github.com/hedeqiang/crewbase/kernel"
)

func TestNightlyTidy(t *testing.T) {
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	root := t.TempDir()
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{{session.Plugin, map[string]any{"dir": root + "/sessions"}}, {prompt.Plugin, nil}, {agent.Plugin, map[string]any{"root": root}}} {
		if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
			t.Fatal(err)
		}
	}
	agents := kernel.Use[*agent.Registry](h.Root(), agent.ServiceKey)
	big, _ := agents.Create(agent.Spec{Name: "Big"})
	small, _ := agents.Create(agent.Spec{Name: "Small"})
	ro, _ := agents.Create(agent.Spec{Name: "Reader", Mode: agent.ModeReadOnly})
	long := "# Memory\n\n" + strings.Repeat("- the user prefers short answers\n", 400)
	for _, a := range []*agent.Agent{big, ro} {
		os.WriteFile(filepath.Join(a.Workspace(), "MEMORY.md"), []byte(long), 0o644)
	}
	os.WriteFile(filepath.Join(small.Workspace(), "MEMORY.md"), []byte("# Memory\n\n- likes tabs\n"), 0o644)
	for _, a := range agents.List() {
		a.Pause()
	}
	now := time.Now()
	td := &tidier{c: h.Root(), agents: agents, min: 6000, now: func() time.Time { return now }}

	td.fire(context.Background(), schedule.Firing{})
	if q := big.Queue(); len(q) != 1 || !strings.Contains(q[0].Text, "整理") || !strings.HasPrefix(q[0].Tag, "memory-tidy:") {
		t.Fatalf("big: %+v", q)
	}
	if len(small.Queue()) != 0 || len(ro.Queue()) != 0 {
		t.Fatal("short or read-only memory tidied")
	}
	baks, _ := filepath.Glob(filepath.Join(big.Workspace(), ".memory", "MEMORY-*.md"))
	if len(baks) != 1 {
		t.Fatalf("backups %v", baks)
	}
	if b, _ := os.ReadFile(baks[0]); string(b) != long {
		t.Fatal("backup differs")
	}

	// the agent rewrites its memory; the end of that turn is stamped, so
	// the next night does not tidy it again
	time.Sleep(20 * time.Millisecond)
	os.WriteFile(filepath.Join(big.Workspace(), "MEMORY.md"), []byte(long+"- merged\n"), 0o644)
	now = time.Now().Add(time.Second)
	td.tidied(agentloop.TurnEndEvent{Agent: big.ID(), Input: big.Queue()[0]})
	big.Cancel(big.Queue()[0].ID)
	td.fire(context.Background(), schedule.Firing{})
	if q := big.Queue(); len(q) != 0 {
		t.Fatalf("tidied again without changes: %+v", q)
	}
}
