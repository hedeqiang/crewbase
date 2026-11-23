package openartifact

// The tools: Artifact, ArtifactData and ArtifactComments, as the daemon
// lists them (tools.list), registered as Crewbase tools of the "artifact"
// family and run through the bridge (tools.call) for the calling
// conversation.

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

// Group is the tool family.
const Group = "artifact"

// category is the approval category of an action kind.
func category(kind string) string {
	switch kind {
	case "read":
		return tools.CategoryRead
	case "write":
		return tools.CategoryWrite
	case "ui":
		return tools.CategoryNone
	case "remove":
		return tools.CategoryDelete
	case "person":
		return tools.CategoryPublish
	}
	return tools.CategoryWrite
}

// pathArgs are the arguments that are paths on this computer.
var pathArgs = map[string][]string{
	"Artifact":     {"folder"},
	"ArtifactData": {"file_path", "out_file"},
}

// defaultAction is the action a call without one means.
var defaultAction = map[string]string{"Artifact": "publish"}

func (s *Service) loadTools(ctx context.Context, c *conn) error {
	res, err := c.call(ctx, "tools.list", map[string]any{})
	if err != nil {
		return fmt.Errorf("tools.list: %w", err)
	}
	var r struct {
		Tools []toolDef `json:"tools"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return fmt.Errorf("tools.list: %w", err)
	}
	sort.Slice(r.Tools, func(i, j int) bool { return r.Tools[i].Name < r.Tools[j].Name })
	sig, _ := json.Marshal(r.Tools)
	s.mu.Lock()
	defer s.mu.Unlock()
	if string(sig) == s.toolSig && s.toolCtx != nil {
		return nil
	}
	if s.toolCtx != nil {
		s.toolCtx.Dispose()
	}
	tc := s.c.Fork("")
	names := make([]string, 0, len(r.Tools))
	for _, d := range r.Tools {
		if d.Name == "" {
			continue
		}
		s.reg.Register(tc, s.tool(d))
		names = append(names, d.Name)
	}
	s.toolCtx, s.toolSig = tc, string(sig)
	s.status.Tools = names
	return nil
}

// dropTools takes the tools away (the integration was turned off).
func (s *Service) dropTools() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.toolCtx != nil {
		s.toolCtx.Dispose()
		s.toolCtx, s.toolSig, s.status.Tools = nil, "", nil
	}
}

func actionOf(d toolDef, call tools.Call) (string, string) {
	var a struct {
		Action string `json:"action"`
	}
	_ = call.Decode(&a)
	if a.Action == "" {
		a.Action = defaultAction[d.Name]
	}
	kind, ok := d.Actions[a.Action]
	if !ok {
		kind = "write"
		if len(d.Actions) == 0 {
			// a tool without action kinds: read-only if it says so
			if ro, _ := d.Annotations["readOnlyHint"].(bool); ro {
				kind = "read"
			}
		}
	}
	return a.Action, kind
}

func (s *Service) tool(d toolDef) *tools.Tool {
	params := d.InputSchema
	if params == nil {
		params = tools.Schema(map[string]any{})
	}
	return &tools.Tool{
		Name:        d.Name,
		Description: d.Description,
		Params:      params,
		Category:    tools.CategoryWrite,
		PathArgs:    pathArgs[d.Name],
		Load:        tools.LoadDeferred,
		Group:       Group,
		Classify: func(call tools.Call) (string, string) {
			action, kind := actionOf(d, call)
			sum := d.Name
			if action != "" {
				sum += " " + action
			}
			var a struct {
				Title string `json:"title"`
				URL   string `json:"url"`
			}
			_ = call.Decode(&a)
			if a.Title != "" {
				sum += ": " + a.Title
			} else if a.URL != "" {
				sum += ": " + a.URL
			}
			return category(kind), sum
		},
		Execute: func(ctx context.Context, call tools.Call) (tools.Result, error) {
			return s.execute(ctx, d, call)
		},
	}
}

func (s *Service) execute(ctx context.Context, d toolDef, call tools.Call) (tools.Result, error) {
	c, err := s.current()
	if err != nil {
		return tools.Errorf("%s", err.Error()), nil
	}
	k, err := s.ensure(ctx, c, call)
	if err != nil {
		return tools.Errorf("Open Artifact: %v", err), nil
	}
	action, kind := actionOf(d, call)
	var args map[string]any
	if len(call.Args) > 0 {
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return tools.Errorf("arguments: %v", err), nil
		}
	}
	if args == nil {
		args = map[string]any{}
	}
	// outside host mode an agent works only on artifacts in its own folders
	if call.Mode != agent.ModeHost && kind != "ui" {
		if u, _ := args["url"].(string); u != "" && strings.HasPrefix(u, "open-artifact://artifact/") {
			if err := s.confined(ctx, c, k, call, u); err != nil {
				return tools.Errorf("%s", err.Error()), nil
			}
		}
	}
	res, err := c.call(ctx, "tools.call", map[string]any{"name": d.Name, "arguments": args,
		"caller": map[string]any{"session": k, "workspace": call.Workspace}})
	if err != nil {
		return tools.Errorf("Open Artifact: %v", err), nil
	}
	return s.result(call, d.Name, action, res), nil
}

// confined refuses an artifact outside the call's folders.
func (s *Service) confined(ctx context.Context, c *conn, k string, call tools.Call, u string) error {
	root := s.rootOf(ctx, c, k, call.Workspace, u)
	if root == "" {
		return nil // the daemon does not know it: it answers with its own error
	}
	for _, r := range append([]string{call.Workspace}, call.Roots...) {
		if r != "" && within(root, r) {
			return nil
		}
	}
	return fmt.Errorf("%s is in %s, outside your workspace; it can be used in host mode, or opened (action open) for the person", u, root)
}

func within(p, dir string) bool {
	p, dir = filepath.Clean(p), filepath.Clean(dir)
	if rp, err := filepath.EvalSymlinks(p); err == nil {
		p = rp
	}
	if rd, err := filepath.EvalSymlinks(dir); err == nil {
		dir = rd
	}
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// rootOf is an artifact's folder (from earlier results, else the library).
func (s *Service) rootOf(ctx context.Context, c *conn, k, ws, u string) string {
	s.mu.Lock()
	root, ok := s.roots[u]
	s.mu.Unlock()
	if ok {
		return root
	}
	lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := c.call(lctx, "tools.call", map[string]any{"name": "Artifact", "arguments": map[string]any{"action": "list", "scope": "all", "limit": 500},
		"caller": map[string]any{"session": k, "workspace": ws}})
	if err != nil {
		return ""
	}
	var l struct {
		Artifacts []struct {
			URL  string `json:"url"`
			Root string `json:"root"`
		} `json:"artifacts"`
	}
	_ = json.Unmarshal(res, &l)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range l.Artifacts {
		if a.URL != "" && a.Root != "" {
			s.roots[a.URL] = a.Root
		}
	}
	return s.roots[u]
}

// Card is an artifact shown in the conversation.
type Card struct {
	URL     string `json:"url"`
	Title   string `json:"title,omitempty"`
	Type    string `json:"type,omitempty"`
	Root    string `json:"root,omitempty"`
	ViewURL string `json:"viewUrl,omitempty"`
	Action  string `json:"action,omitempty"`
	Agent   string `json:"agent,omitempty"`
}

// result turns the tool's answer into a Crewbase result, remembering the
// artifacts it names and adding a card for one a write made.
func (s *Service) result(call tools.Call, tool, action string, res json.RawMessage) tools.Result {
	var top map[string]any
	_ = json.Unmarshal(res, &top)
	text := string(res)
	if b, err := json.MarshalIndent(top, "", " "); err == nil && top != nil {
		text = string(b)
	}
	if e, ok := top["error"].(map[string]any); ok {
		msg, _ := e["message"].(string)
		fix, _ := e["fix"].(string)
		code, _ := e["code"].(string)
		out := msg
		if code != "" {
			out = code + ": " + msg
		}
		if fix != "" {
			out += "\n" + fix
		}
		return tools.Result{Content: out, IsError: true}
	}
	var card *Card
	note := func(m map[string]any) {
		u, _ := m["url"].(string)
		root, _ := m["root"].(string)
		if !strings.HasPrefix(u, "open-artifact://artifact/") {
			return
		}
		if root != "" {
			s.mu.Lock()
			s.roots[u] = root
			s.mu.Unlock()
		}
		if card == nil {
			card = &Card{URL: u, Root: root, Action: action, Agent: call.Agent}
			card.Title, _ = m["title"].(string)
			card.ViewURL, _ = m["view_url"].(string)
			if t, _ := m["type_url"].(string); t != "" {
				card.Type = strings.TrimPrefix(t, "open-artifact://type/")
			} else {
				card.Type, _ = m["type"].(string)
			}
		}
	}
	note(top)
	if as, ok := top["artifacts"].([]any); ok {
		for _, a := range as {
			if m, ok := a.(map[string]any); ok {
				u, _ := m["url"].(string)
				root, _ := m["root"].(string)
				if u != "" && root != "" {
					s.mu.Lock()
					s.roots[u] = root
					s.mu.Unlock()
				}
			}
		}
	}
	out := tools.Result{Content: text}
	if card != nil && tool == "Artifact" && (action == "publish" || action == "adopt" || action == "call") {
		if card.Title == "" {
			card.Title = s.titleOf(card.URL, call.Args)
		}
		out.Data = card
		if call.Session != nil {
			_, _ = call.Session.Append(EventArtifact, call.Agent, card)
		}
	}
	return out
}

func (s *Service) titleOf(u string, args json.RawMessage) string {
	var a struct {
		Title string `json:"title"`
	}
	_ = json.Unmarshal(args, &a)
	return a.Title
}

// ---------------------------------------------------------------- hooks

// hooks follow turns (session status) and the agent's own file writes.
func (s *Service) hooks(c *kernel.Context) {
	agentloop.TurnStarted.Listen(c, func(e agentloop.TurnStartEvent) {
		if e.Agent == nil || e.Session == nil {
			return
		}
		go s.turnStatus(e.Session.ID(), e.Agent.ID(), "running")
	})
	agentloop.TurnEnded.Listen(c, func(e agentloop.TurnEndEvent) {
		go s.turnStatus(e.Session, e.Agent, "idle")
	})
	tools.OnResult.Listen(c, func(e tools.Execution) {
		if e.Denied || e.Parked || e.Result.IsError || e.Call.Session == nil {
			return
		}
		switch e.Call.Name {
		case "write_file", "edit_file", "multi_edit", "apply_patch":
		default:
			return
		}
		var a struct {
			Path  string   `json:"path"`
			Paths []string `json:"paths"`
		}
		_ = e.Call.Decode(&a)
		ps := a.Paths
		if a.Path != "" {
			ps = append(ps, a.Path)
		}
		var abs []string
		for _, p := range ps {
			if !filepath.IsAbs(p) {
				p = filepath.Join(e.Call.Workspace, p)
			}
			abs = append(abs, filepath.Clean(p))
		}
		go s.wrote(e.Call.Session.ID(), e.Call.Agent, abs)
	})
}
