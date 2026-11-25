package mcp

// The acting window of MCP skills, and what it gates.
//
// A conversation is "acting on" an MCP skill from the moment the skill is
// loaded (<server>__skill action=load) until the conversation ends: the
// spec lets a host hold the window longer than the SKILL.md stays in
// context, never shorter, and a conversation-long window is the one that
// cannot close early (compaction or a rewind may or may not have dropped
// the text). The held entry — the manifest the skill was loaded from — is
// kept with it, in memory and in the session's side state (so the window
// and the manifest survive a daemon restart).
//
// While a conversation acts on any MCP skill:
//
//   - every call of a host code-execution tool (a built-in or plugin tool,
//     or a host-trusted MCP server's tool, whose category is exec or
//     install — bash, scripts) needs the person's approval, per call, never
//     rememberable ("always allow" would turn into an agent-wide rule);
//   - a resource read on another MCP server (<other>__resources read, or
//     loading another server's skill) needs the person's approval naming
//     both servers, per call.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

const heldStateFile = "mcp-skills.json"

// heldSkill is a skill a conversation is acting on, with its held entry.
type heldSkill struct {
	Server  string      `json:"server"`
	URI     string      `json:"uri"`
	Name    string      `json:"name"`
	Dynamic bool        `json:"dynamic,omitempty"`
	Files   []skillFile `json:"files,omitempty"`
	Loaded  time.Time   `json:"loaded"`
	// Fetched counts the bytes a dynamic skill returned (size limit).
	Fetched int64 `json:"fetched,omitempty"`
}

type heldSet struct {
	Skills []heldSkill `json:"skills"`
}

// heldKey is the conversation a call belongs to (the agent when it has no
// session, e.g. a direct API call).
func heldKey(call tools.Call) string {
	if call.Session != nil {
		return "s:" + call.Session.ID()
	}
	return "a:" + call.Agent
}

// setLocked returns the call's held set, loading it from the session's
// side state the first time. Callers hold h.mu.
func (h *skillsHost) setLocked(call tools.Call) *heldSet {
	if h.held == nil {
		h.held = map[string]*heldSet{}
	}
	k := heldKey(call)
	if hs := h.held[k]; hs != nil {
		return hs
	}
	hs := &heldSet{}
	if call.Session != nil {
		if b, err := call.Session.ReadState(heldStateFile); err == nil {
			_ = json.Unmarshal(b, hs)
		}
	}
	h.held[k] = hs
	return hs
}

func (h *skillsHost) persistLocked(call tools.Call, hs *heldSet) {
	if call.Session == nil {
		return
	}
	if b, err := json.Marshal(hs); err == nil {
		_ = call.Session.WriteState(heldStateFile, b)
	}
}

// hold records that the call's conversation acts on x (replacing an
// earlier load of the same skill: the newest entry is the one held).
func (h *skillsHost) hold(call tools.Call, x heldSkill) {
	h.mu.Lock()
	defer h.mu.Unlock()
	hs := h.setLocked(call)
	kept := hs.Skills[:0]
	for _, y := range hs.Skills {
		if y.Server != x.Server || y.URI != x.URI {
			kept = append(kept, y)
		}
	}
	hs.Skills = append(kept, x)
	h.persistLocked(call, hs)
}

// heldSkillFor finds a held skill of server by uri or (unique) name.
func (h *skillsHost) heldSkillFor(call tools.Call, server, ref string) (heldSkill, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var hits []heldSkill
	for _, x := range h.setLocked(call).Skills {
		if x.Server == server && (x.URI == ref || x.Name == ref) {
			if x.URI == ref {
				return x, true
			}
			hits = append(hits, x)
		}
	}
	if len(hits) == 1 {
		return hits[0], true
	}
	return heldSkill{}, false
}

// addFetched adds n bytes to a dynamic skill's total; false once the total
// would pass the per-skill limit.
func (h *skillsHost) addFetched(call tools.Call, server, uri string, n int64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	hs := h.setLocked(call)
	for i := range hs.Skills {
		x := &hs.Skills[i]
		if x.Server == server && x.URI == uri {
			if x.Fetched+n > maxSkillBytes {
				return false
			}
			x.Fetched += n
			h.persistLocked(call, hs)
			return true
		}
	}
	return false
}

// active lists the skills the call's conversation acts on.
func (h *skillsHost) active(call tools.Call) []heldSkill {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]heldSkill(nil), h.setLocked(call).Skills...)
}

// codeExecution: t runs code on the host (as opposed to a remote or
// sandboxed MCP server doing its own work).
func codeExecution(t *tools.Tool, category string) bool {
	execish := func(c string) bool { return c == tools.CategoryExec || c == tools.CategoryInstall }
	if !execish(t.Category) && !execish(category) {
		return false
	}
	return t.Source == "" || t.Unconfined
}

func actionOf(call *tools.Call) string {
	var a struct {
		Action string `json:"action"`
	}
	_ = json.Unmarshal(call.Args, &a)
	return a.Action
}

func describeHeld(list []heldSkill) string {
	var parts []string
	for _, x := range list {
		parts = append(parts, fmt.Sprintf("%q from MCP server %q", x.Name, x.Server))
	}
	return strings.Join(parts, ", ")
}

// skillGate is the tools/pre-execute step. It runs after the rest of the
// chain decided, and only ever tightens: allow → ask.
func (s *Service) skillGate(ctx context.Context, d *tools.Decision, next kernel.Next[*tools.Decision, *tools.Decision]) (*tools.Decision, error) {
	out, err := next(ctx, d)
	if err != nil || out == nil || out.Verdict == tools.Deny || out.Call == nil || out.Tool == nil {
		return out, err
	}
	held := s.skills.active(*out.Call)
	if len(held) == 0 {
		return out, nil
	}
	t := out.Tool
	if codeExecution(t, out.Category) {
		out.RequireApproval(fmt.Sprintf("the conversation is following MCP skill %s (untrusted third-party instructions); "+
			"running code while acting on it needs your approval each time", describeHeld(held)))
		out.Rememberable, out.ConfirmRemember = false, false
		return out, nil
	}
	if t.Source == "" {
		return out, nil
	}
	target := t.Source
	read := (t.Name == ToolName(target, resourcesSuffix) && actionOf(out.Call) == "read") ||
		(t.Name == ToolName(target, skillSuffix) && actionOf(out.Call) == "load")
	if !read {
		return out, nil
	}
	others := map[string]bool{}
	var from []heldSkill
	for _, x := range held {
		if x.Server != target {
			others[x.Server] = true
			from = append(from, x)
		}
	}
	if len(others) == 0 {
		return out, nil
	}
	names := make([]string, 0, len(others))
	for n := range others {
		names = append(names, fmt.Sprintf("%q", n))
	}
	sort.Strings(names)
	out.RequireApproval(fmt.Sprintf("cross-server read: the conversation is following MCP skill %s, and this reads from a different MCP server, %q "+
		"(skill server: %s; read target: %q)", describeHeld(from), target, strings.Join(names, ", "), target))
	out.Rememberable, out.ConfirmRemember = false, false
	return out, nil
}
