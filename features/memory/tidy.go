package memory

// Nightly memory tidy-up (plugin @crewbase/memory-tidy), after the
// "dreaming" of always-on agents: once a night, an agent whose MEMORY.md
// has outgrown what every request can carry, and changed since its last
// tidy-up, is asked to curate it — merge duplicates, drop what is outdated,
// lasting facts first. The file is backed up first (.memory/ in the
// workspace, the last few kept), so nothing is lost and the change can be
// reviewed or undone.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/features/schedule"
	"github.com/hedeqiang/crewbase/kernel"
)

// TidyConfig configures the tidy-up.
type TidyConfig struct {
	// At is the schedule spec (default "daily 03:30"); "off" disables it.
	At string `json:"at,omitempty"`
	// MinChars: memory shorter than this is left alone (default: what the
	// system prompt carries whole).
	MinChars int `json:"minChars,omitempty"`
}

const (
	tidyOwner   = "memory-tidy"
	tidyBackups = 7
)

type tidier struct {
	c      *kernel.Context
	agents *agent.Registry
	min    int
	now    func() time.Time
}

func memDir(a *agent.Agent) string { return filepath.Join(a.Workspace(), ".memory") }

// due reports whether a needs a tidy-up: long enough, and changed since the
// last one.
func (t *tidier) due(a *agent.Agent) (int, bool) {
	s := a.Spec()
	if a.IsChat() || s.Ephemeral || a.Mode() == agent.ModeReadOnly {
		return 0, false
	}
	p := filepath.Join(a.Workspace(), "MEMORY.md")
	st, err := os.Stat(p)
	if err != nil {
		return 0, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return 0, false
	}
	n := utf8.RuneCount(b)
	if n < t.min {
		return n, false
	}
	if last, err := os.Stat(filepath.Join(memDir(a), "last-tidy")); err == nil && !st.ModTime().After(last.ModTime()) {
		return n, false
	}
	return n, true
}

func (t *tidier) backup(a *agent.Agent, stamp string) (string, error) {
	dir := memDir(a)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	b, err := os.ReadFile(filepath.Join(a.Workspace(), "MEMORY.md"))
	if err != nil {
		return "", err
	}
	name := "MEMORY-" + stamp + ".md"
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
		return "", err
	}
	// keep the latest few
	ents, _ := os.ReadDir(dir)
	var olds []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "MEMORY-") && strings.HasSuffix(e.Name(), ".md") {
			olds = append(olds, e.Name())
		}
	}
	sort.Strings(olds)
	for len(olds) > tidyBackups {
		_ = os.Remove(filepath.Join(dir, olds[0]))
		olds = olds[1:]
	}
	return filepath.Join(".memory", name), nil
}

func (t *tidier) fire(_ context.Context, f schedule.Firing) error {
	now := t.now()
	stamp := now.Format("20060102-1504")
	for _, a := range t.agents.List() {
		n, ok := t.due(a)
		if !ok {
			continue
		}
		bak, err := t.backup(a, stamp)
		if err != nil {
			t.c.Log().Warn("memory tidy: backup", "agent", a.ID(), "err", err)
			continue
		}
		_ = os.WriteFile(filepath.Join(memDir(a), "last-tidy"), []byte(now.UTC().Format(time.RFC3339)), 0o600)
		text := fmt.Sprintf("[夜间整理记忆] 你的 MEMORY.md 已有 %d 字，超过每次对话能完整带上的长度。请整理它：\n"+
			"- 合并重复和相近的条目；删掉已经过时、或被后来的信息推翻的条目；\n"+
			"- 长期有效的事实（用户是谁、偏好、约定、项目背景）放在最前面，近期进展放后面；\n"+
			"- 不要编造新内容；拿不准的保留；\n"+
			"- 直接改写 MEMORY.md。整理前的版本已备份在 %s，需要时可以对照。\n"+
			"完成后用一两句话说明合并和删掉了什么。只做这件事，不要做别的。", n, bak)
		if _, err := a.Enqueue(agent.Input{Text: text, From: "memory", Tag: "memory-tidy:" + stamp}); err != nil {
			t.c.Log().Warn("memory tidy: enqueue", "agent", a.ID(), "err", err)
		}
	}
	return nil
}

// tidied stamps last-tidy after the agent's own edit, so the tidy-up itself
// does not count as a change next night.
func (t *tidier) tidied(e agentloop.TurnEndEvent) {
	if !strings.HasPrefix(e.Input.Tag, "memory-tidy:") {
		return
	}
	if a, ok := t.agents.Get(e.Agent); ok {
		p := filepath.Join(memDir(a), "last-tidy")
		now := t.now()
		_ = os.WriteFile(p, []byte(now.UTC().Format(time.RFC3339)), 0o600)
		_ = os.Chtimes(p, now, now)
	}
}

// TidyPlugin is @crewbase/memory-tidy.
var TidyPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/memory-tidy",
	Inject:      []string{agent.ServiceKey, schedule.ServiceKey},
	Description: "Nightly: agents curate an overgrown MEMORY.md (backed up first).",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[TidyConfig](c)
		if err != nil {
			return err
		}
		if cfg.At == "off" {
			return nil
		}
		if cfg.At == "" {
			cfg.At = "daily 03:30"
		}
		if cfg.MinChars <= 0 {
			cfg.MinChars = agent.MemoryHead + agent.MemoryTail
		}
		t := &tidier{c: c, agents: kernel.Use[*agent.Registry](c, agent.ServiceKey), min: cfg.MinChars, now: time.Now}
		sched := kernel.Use[*schedule.Service](c, schedule.ServiceKey)
		sched.Handle(c, tidyOwner, t.fire)
		agentloop.TurnEnded.Listen(c, t.tidied)
		found := false
		for _, j := range sched.List(tidyOwner) {
			found = true
			if j.Spec != cfg.At {
				_, _ = sched.Update(j.ID, func(x *schedule.Job) { x.Spec = cfg.At })
			}
		}
		if !found {
			if _, err := sched.Add(schedule.Job{ID: "memory-tidy", Owner: tidyOwner, Name: "整理记忆", Spec: cfg.At}); err != nil {
				return err
			}
		}
		return nil
	},
})
