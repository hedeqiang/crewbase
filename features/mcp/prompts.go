package mcp

// MCP prompt templates picked with / in the composer: listed per agent
// (the servers whose prompts it may use), and filled in with the person's
// arguments when the turn starts (agentloop.ResolveUse), so the message
// records the text the model read.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hedeqiang/crewbase/core/agentloop"
	csession "github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/kernel"
)

// PromptInfo is one prompt template an agent can use.
type PromptInfo struct {
	Server      string      `json:"server"`
	Name        string      `json:"name"`
	Title       string      `json:"title,omitempty"`
	Description string      `json:"description,omitempty"`
	Args        []PromptArg `json:"args,omitempty"`
}

// PromptArg is an argument of a prompt template.
type PromptArg struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// promptConns: the connected servers whose prompt templates agent may use
// (the same check as their <server>__prompt tool: scope, team, connector).
func (s *Service) promptConns(agentID string) []*conn {
	s.mu.Lock()
	var out []*conn
	for _, e := range s.entries {
		if e.conn != nil && e.srv.Enabled {
			out = append(out, e.conn)
		}
	}
	s.mu.Unlock()
	keep := out[:0]
	for _, c := range out {
		sess := c.session()
		if sess == nil {
			continue
		}
		if ir := sess.cs.InitializeResult(); ir == nil || ir.Capabilities == nil || ir.Capabilities.Prompts == nil {
			continue
		}
		if _, ok := s.tools.Get(agentID, ToolName(c.srv.Name, promptSuffix)); !ok {
			continue
		}
		keep = append(keep, c)
	}
	sort.Slice(keep, func(i, j int) bool { return keep[i].srv.Name < keep[j].srv.Name })
	return keep
}

// AgentPrompts lists the prompt templates agent can use.
func (s *Service) AgentPrompts(ctx context.Context, agentID string) []PromptInfo {
	var out []PromptInfo
	for _, c := range s.promptConns(agentID) {
		sess := c.session()
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		n := 0
		for p, err := range sess.cs.Prompts(cctx, nil) {
			if err != nil || n >= maxListed {
				break
			}
			pi := PromptInfo{Server: c.srv.Name, Name: p.Name, Title: p.Title, Description: truncate(p.Description, 300)}
			for _, a := range p.Arguments {
				if a != nil {
					pi.Args = append(pi.Args, PromptArg{Name: a.Name, Description: truncate(a.Description, 200), Required: a.Required})
				}
			}
			out = append(out, pi)
			n++
		}
		cancel()
	}
	return out
}

// RenderPrompt fills in prompt template name of server for agent.
func (s *Service) RenderPrompt(ctx context.Context, agentID, server, name string, args map[string]string) (string, error) {
	for _, c := range s.promptConns(agentID) {
		if c.srv.Name != server {
			continue
		}
		sess := c.session()
		cctx, cancel := context.WithTimeout(ctx, c.s.t.call)
		defer cancel()
		res, err := sess.cs.GetPrompt(cctx, &sdk.GetPromptParams{Name: name, Arguments: args})
		if err != nil {
			return "", fmt.Errorf("prompt %s/%s: %v", server, name, sess.scrubErr(err))
		}
		var b strings.Builder
		if res.Description != "" {
			fmt.Fprintf(&b, "%s\n\n", res.Description)
		}
		for _, m := range res.Messages {
			if m == nil {
				continue
			}
			text := ConvertResult(&sdk.CallToolResult{Content: []sdk.Content{m.Content}}).Content
			fmt.Fprintf(&b, "<%s>\n%s\n</%s>\n", m.Role, text, m.Role)
		}
		out := b.String()
		if len(out) > MaxResultBytes {
			out = out[:MaxResultBytes] + "\n… [truncated]"
		}
		return out, nil
	}
	return "", fmt.Errorf("prompt %s/%s is not available to this agent", server, name)
}

// resolvePrompts is the ResolveUse step for prompt picks.
func (s *Service) resolvePrompts(ctx context.Context, r *agentloop.UseRequest, next kernel.Next[*agentloop.UseRequest, *agentloop.UseRequest]) (*agentloop.UseRequest, error) {
	for i, u := range r.Use {
		if u.Kind != csession.InvokePrompt || u.Text != "" {
			continue
		}
		server, name, ok := strings.Cut(u.Name, "/")
		if !ok {
			return nil, fmt.Errorf("prompt %q: want <server>/<prompt>", u.Name)
		}
		text, err := s.RenderPrompt(ctx, r.Agent, server, name, u.Args)
		if err != nil {
			return nil, err
		}
		r.Use[i].Text = text
	}
	return next(ctx, r)
}
