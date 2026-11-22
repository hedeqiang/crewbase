package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/fs"
	"github.com/hedeqiang/crewbase/capabilities/vcs"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/audit"
	"github.com/hedeqiang/crewbase/kernel"
)

// Config of @crewbase/git.
type Config struct {
	Root string `json:"root"` // the data directory (worktrees in <root>/worktrees)
}

// Plugin is @crewbase/git.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/git",
	Inject:      []string{agent.ServiceKey, tools.ServiceKey},
	Optional:    []string{vcs.ServiceKey, audit.ServiceKey},
	Description: "Git for agents working side by side: a worktree per sub-agent, freeze locks, local code review with rounds, merge previews and approved pushes.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		root := cfg.Root
		if root == "" {
			if root, err = os.MkdirTemp("", "crewbase-git-"); err != nil {
				return err
			}
		}
		st, err := openStore(filepath.Join(root, "git"))
		if err != nil {
			return fmt.Errorf("git state: %w", err)
		}
		s := &Service{c: c, root: root, st: st, agents: kernel.Use[*agent.Registry](c, agent.ServiceKey),
			vcsFn: func() vcs.Provider { p, _ := kernel.Lookup[vcs.Provider](c, vcs.ServiceKey); return p }}
		s.audit = func(typ, actor, ag, session string, data any) {
			if a, ok := kernel.Lookup[*audit.Service](c, audit.ServiceKey); ok {
				_, _ = a.Record(audit.Entry{Type: typ, Actor: actor, Agent: ag, Session: session, Data: data})
			}
		}
		if err := c.Provide(ServiceKey, s); err != nil {
			return err
		}
		s.registerTools(c, kernel.Use[*tools.Registry](c, tools.ServiceKey))
		vcs.AllocateWorkspace.Listen(c, s.allocate)
		fs.WriteIntent.Use(c, s.writeIntent)
		vcs.PreMerge.Use(c, s.preMerge)
		// a worktree whose folder vanished (deleted by hand) is forgotten
		c.Go(func(ctx context.Context) {
			t := time.NewTicker(10 * time.Minute)
			defer t.Stop()
			for {
				s.sweep(ctx)
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		})
		return nil
	},
})

// sweep forgets worktrees whose folder is gone.
func (s *Service) sweep(ctx context.Context) {
	g, err := s.vcs()
	if err != nil {
		return
	}
	for _, w := range s.Worktrees("", false) {
		if _, err := os.Stat(w.Path); os.IsNotExist(err) {
			_ = g.RemoveWorktree(ctx, w.RepoRoot, w.Path, true, "")
			_, _ = s.updateWorktree(w.ID, func(x *Worktree) { x.State = WTRemoved })
		}
	}
}

// SetSettings changes the settings.
func (s *Service) SetSettings(x Settings) (Settings, error) {
	switch x.Isolation {
	case vcs.IsolateShared, vcs.IsolateWorktree, vcs.IsolateOnWrite:
	default:
		return Settings{}, fmt.Errorf("isolation is shared, isolated-worktree or worktree-on-write")
	}
	if x.MaxAutoRounds < 0 || x.MaxAutoRounds > 10 {
		return Settings{}, fmt.Errorf("automatic rounds: 0 to 10")
	}
	if x.Approvals < 1 || x.Approvals > 5 {
		return Settings{}, fmt.Errorf("approvals: 1 to 5")
	}
	var ps []string
	for _, p := range x.Protected {
		if p = strings.TrimSpace(p); p != "" {
			ps = append(ps, p)
		}
	}
	x.Protected = ps
	var rs []Remote
	for _, r := range x.Remotes {
		r.Host = strings.ToLower(strings.TrimSpace(r.Host))
		if r.Host == "" {
			continue
		}
		if !strings.HasPrefix(r.Credential, "vault:") {
			return Settings{}, fmt.Errorf("%s: the credential must be a vault reference (vault:…), never the secret itself", r.Host)
		}
		rs = append(rs, r)
	}
	if rs == nil {
		rs = []Remote{}
	}
	x.Remotes = rs
	s.st.mu.Lock()
	s.st.st.Settings = &x
	err := s.st.save()
	s.st.mu.Unlock()
	if err != nil {
		return Settings{}, err
	}
	s.changed(vcs.Event{Type: "git/settings", Actor: PersonActor(""), Data: x})
	return x, nil
}

// Settings are the current settings.
func (s *Service) Settings() Settings { return s.st.settings() }
