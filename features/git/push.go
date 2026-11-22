package git

import (
	"context"
	"fmt"
	"strings"

	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/capabilities/vcs"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/gitcli"
)

// PushRequest pushes a branch.
type PushRequest struct {
	Dir     string
	Branch  string
	Remote  string
	Force   bool
	Actor   string
	Session string
}

// Push pushes a branch to its remote. The credential configured for the
// remote's host (Settings › Git, a vault: reference) is resolved for this
// one command and handed to git through an askpass; a remote on another
// host never sees it.
func (s *Service) Push(ctx context.Context, r PushRequest) (string, error) {
	g, err := s.vcs()
	if err != nil {
		return "", err
	}
	info, err := s.Repo(ctx, r.Dir)
	if err != nil {
		return "", err
	}
	branch := r.Branch
	if branch == "" {
		branch = info.Branch
	}
	if branch == "" {
		return "", fmt.Errorf("no branch to push (detached HEAD)")
	}
	remote := r.Remote
	if remote == "" {
		remote = "origin"
	}
	u, err := g.RemoteURL(ctx, r.Dir, remote)
	if err != nil {
		return "", err
	}
	host := gitcli.HostOf(u)
	req := vcs.PushRequest{Remote: remote, Branch: branch, Force: r.Force, SetUp: true}
	var used string
	for _, rm := range s.st.settings().Remotes {
		if host != "" && strings.EqualFold(rm.Host, host) && rm.Credential != "" {
			cs, ok := s.creds()
			if !ok {
				return "", fmt.Errorf("the vault is not available")
			}
			tok, err := cs.Resolve(ctx, rm.Credential)
			if err != nil {
				return "", fmt.Errorf("credential %s for %s: %w", rm.Credential, host, err)
			}
			req.Token, req.Username, used = tok, rm.Username, rm.Credential
			break
		}
	}
	out, err := g.Push(ctx, r.Dir, req)
	data := map[string]any{"remote": remote, "host": host, "branch": branch, "force": r.Force, "credential": used, "ok": err == nil}
	s.changed(vcs.Event{Type: "git/push", Repo: info.ID, Actor: r.Actor, Session: r.Session, Data: data})
	return out, err
}

func (s *Service) creds() (*credentials.Service, bool) {
	if s.c == nil {
		return nil, false
	}
	return kernel.Lookup[*credentials.Service](s.c, credentials.ServiceKey)
}
