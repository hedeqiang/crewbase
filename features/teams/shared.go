package teams

// What a team shares (phase 3):
//
//   - Team memory: <dir>/teams/<id>/TEAM.md, in every team agent's prompt
//     (bounded like MEMORY.md); agents add to it with team_memory_add,
//     leads edit it in 设置 › 团队.
//   - Default run mode for new agents of the team.
//   - Usage: tokens of the team's agents (from the usage service).
//   - MCP servers shared with a team (scope team:<id>, features/mcp) and
//     chat gateways bound to a team (features/imconfig) live with those
//     features.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

// MaxTeamMemory caps TEAM.md (runes).
const MaxTeamMemory = 50_000

func (s *Service) memoryPath(id string) string {
	if s.path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.path), "teams", agent.TeamOf(id), "TEAM.md")
}

// Memory returns a team's TEAM.md.
func (s *Service) Memory(id string) string {
	p := s.memoryPath(id)
	if p == "" {
		return ""
	}
	b, _ := os.ReadFile(p)
	return string(b)
}

// SetMemory replaces TEAM.md.
func (s *Service) SetMemory(id, text string) error {
	if !s.Exists(id) {
		return os.ErrNotExist
	}
	if utf8.RuneCountInString(text) > MaxTeamMemory {
		return fmt.Errorf("团队记忆不能超过 %d 字", MaxTeamMemory)
	}
	p := s.memoryPath(id)
	if p == "" {
		return errors.New("team memory needs a data directory")
	}
	s.memMu.Lock()
	defer s.memMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(text), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// AddMemory appends a dated line to TEAM.md.
func (s *Service) AddMemory(id, note, by string) error {
	note = strings.Join(strings.Fields(note), " ")
	if note == "" {
		return errors.New("note is empty")
	}
	cur := strings.TrimRight(s.Memory(id), "\n")
	if cur == "" {
		cur = "# Team memory"
	}
	line := fmt.Sprintf("- %s %s", time.Now().Format("2006-01-02"), note)
	if by != "" {
		line += " (" + by + ")"
	}
	return s.SetMemory(id, cur+"\n"+line+"\n")
}

// TeamMemoryPrompt is the prompt fragment of an agent's team memory.
func (s *Service) TeamMemoryPrompt(agentID string) string {
	a, ok := s.agents.Get(agentID)
	if !ok || a.IsChat() {
		return ""
	}
	m := strings.TrimSpace(s.Memory(a.Spec().TeamID()))
	if m == "" || m == "# Team memory" {
		return ""
	}
	return "Team memory (TEAM.md, shared by everyone in your team; add lasting team facts with team_memory_add):\n" + agent.MemoryForPrompt(m)
}

func (s *Service) memoryTool() *tools.Tool {
	return &tools.Tool{
		Name: "team_memory_add", Load: tools.LoadDeferred, Group: "team",
		Description: "Add a lasting fact to your team's shared memory (TEAM.md), which every agent in your team sees: " +
			"conventions, decisions, who owns what, links that matter. Only add what is new: never repeat what the team memory already says. " +
			"Not for personal notes (use memory_add) or temporary state.",
		Params:   tools.Schema(map[string]any{"note": tools.Prop("string", "One fact, one line.")}, "note"),
		Category: tools.CategoryWrite,
		Execute: func(_ context.Context, call tools.Call) (tools.Result, error) {
			a, ok := s.agents.Get(call.Agent)
			if !ok || a.IsChat() {
				return tools.Errorf("only team agents have a team memory"), nil
			}
			sp := a.Spec()
			if err := s.AddMemory(sp.TeamID(), call.String("note"), sp.Name); err != nil {
				return tools.Errorf("%v", err), nil
			}
			return tools.Text("Added to the team memory."), nil
		},
	}
}

// DefaultMode is the run mode new agents of a team start in ("" = sandbox).
func (s *Service) DefaultMode(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.items[agent.TeamOf(id)]; ok {
		return t.DefaultMode
	}
	return ""
}

type usageStats interface {
	TokensByAgent() map[string]int
}

// tokens sums the team's agents' tokens (current usage period).
func (s *Service) tokens() map[string]int {
	u, ok := kernel.Lookup[usageStats](s.c, "usage")
	if !ok {
		return nil
	}
	out := map[string]int{}
	for id, n := range u.TokensByAgent() {
		if a, ok := s.agents.Get(id); ok {
			out[a.Spec().TeamID()] += n
		}
	}
	return out
}

// installShared registers the team memory fragment and tool.
func (s *Service) installShared(c *kernel.Context) {
	if pa, ok := kernel.Lookup[*prompt.Assembler](c, prompt.ServiceKey); ok {
		pa.Register(c, &prompt.Fragment{Name: "team-memory", Order: 21, Render: s.TeamMemoryPrompt})
	}
	if tr, ok := kernel.Lookup[*tools.Registry](c, tools.ServiceKey); ok {
		tr.Register(c, s.memoryTool())
	}
}
