package taskboard

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// MemberLookup is implemented by the `members` service.
type MemberLookup interface {
	MemberExists(id string) bool
}

// MembersKey is the service key of MemberLookup.
const MembersKey = "members"

// Format renders a board for a model (and for logs).
func Format(b Board, self string) string {
	if len(b.Tasks) == 0 {
		return "The task board is empty. Use task_add to plan work as tasks with owners and dependencies."
	}
	var sb strings.Builder
	sb.WriteString("Task board (#id [status] title — owner; deps; scope):\n")
	for _, t := range b.Tasks {
		st := t.Status
		if t.Ready {
			st = "ready"
		}
		owner := t.Owner
		switch {
		case owner == "":
			owner = "unassigned"
		case owner == self:
			owner += " (you)"
		}
		fmt.Fprintf(&sb, "#%s [%s] %s — %s", t.ID, st, t.Title, owner)
		if len(t.Deps) > 0 {
			fmt.Fprintf(&sb, "; after #%s", strings.Join(t.Deps, ", #"))
		}
		if len(t.Waiting) > 0 {
			fmt.Fprintf(&sb, " (waiting for #%s)", strings.Join(t.Waiting, ", #"))
		}
		if len(t.Scope) > 0 {
			fmt.Fprintf(&sb, "; scope %s", strings.Join(t.Scope, ", "))
		}
		if t.Note != "" {
			fmt.Fprintf(&sb, "\n    note: %s", t.Note)
		}
		sb.WriteByte('\n')
	}
	sb.WriteString("Scopes are coordination hints: stay inside your task's scope; other tasks' scopes belong to their owners.")
	return sb.String()
}

func strs(v any) *[]string {
	xs, ok := v.([]any)
	if !ok {
		return nil
	}
	out := []string{}
	for _, x := range xs {
		if s, ok := x.(string); ok {
			out = append(out, s)
		} else if n, ok := x.(float64); ok {
			out = append(out, fmt.Sprint(int(n)))
		}
	}
	return &out
}

func change(args json.RawMessage) (Change, string, error) {
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return Change{}, "", fmt.Errorf("invalid arguments: %v", err)
	}
	var c Change
	sp := func(k string) *string {
		if v, ok := m[k].(string); ok {
			return &v
		}
		return nil
	}
	c.Title, c.Detail, c.Owner, c.Status, c.Note = sp("title"), sp("detail"), sp("owner"), sp("status"), sp("note")
	c.Deps, c.Scope = strs(m["deps"]), strs(m["scope"])
	id := ""
	switch v := m["id"].(type) {
	case string:
		id = strings.TrimPrefix(v, "#")
	case float64:
		id = fmt.Sprint(int(v))
	}
	return c, id, nil
}

// Plugin provides the board, its tools and its API.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/taskboard",
	Inject:      []string{session.ServiceKey, agent.ServiceKey, tools.ServiceKey},
	Optional:    []string{MembersKey},
	Description: "Team task board: one task DAG per delegation tree, one owner per task (A-7).",
	Apply: func(c *kernel.Context) error {
		agents := kernel.Use[*agent.Registry](c, agent.ServiceKey)
		s := New(c.Host(), kernel.Use[*session.Store](c, session.ServiceKey), func(o string) bool {
			if id, ok := strings.CutPrefix(o, MemberPrefix); ok {
				if m, ok := kernel.Lookup[MemberLookup](c, MembersKey); ok {
					return m.MemberExists(id)
				}
				return id == "owner"
			}
			a, ok := agents.Get(o)
			return ok && !a.IsChat()
		})
		reg := kernel.Use[*tools.Registry](c, tools.ServiceKey)
		sessionOf := func(call tools.Call) (string, error) {
			if call.Session == nil {
				return "", fmt.Errorf("the task board needs a conversation")
			}
			return call.Session.ID(), nil
		}
		reg.Register(c, &tools.Tool{
			Name:        "task_list",
			Description: "Show the team task board shared by this conversation and its sub-agents: tasks, owners, dependencies (a DAG), what is ready to start, and write scopes.",
			Params:      tools.Schema(map[string]any{}),
			Execute: func(_ context.Context, call tools.Call) (tools.Result, error) {
				sid, err := sessionOf(call)
				if err != nil {
					return tools.Errorf("%v", err), nil
				}
				b, err := s.Get(sid)
				if err != nil {
					return tools.Errorf("%v", err), nil
				}
				return tools.Result{Content: Format(b, call.Agent)}, nil
			}})
		reg.Register(c, &tools.Tool{
			Name: "task_add",
			Description: "Add a task to the shared board. Give it one owner (an agent id, member:<id> for a person, or leave empty), " +
				"the ids of tasks it depends on, and optionally the paths it will write (scope). Dependencies may not form a cycle.",
			Params: tools.Schema(map[string]any{
				"title":  tools.Prop("string", "short title"),
				"detail": tools.Prop("string", "what done looks like"),
				"owner":  tools.Prop("string", "agent id, member:<id>, or empty"),
				"deps":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "ids of tasks that must be done first"},
				"scope":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "workspace-relative paths this task writes (a hint, not a lock)"},
			}, "title"),
			Execute: func(_ context.Context, call tools.Call) (tools.Result, error) {
				sid, err := sessionOf(call)
				if err != nil {
					return tools.Errorf("%v", err), nil
				}
				ch, _, err := change(call.Args)
				if err != nil {
					return tools.Errorf("%v", err), nil
				}
				v, err := s.Add(sid, Actor{Agent: call.Agent}, ch)
				if err != nil {
					return tools.Errorf("%v", err), nil
				}
				return tools.Result{Content: fmt.Sprintf("added task #%s", v.ID)}, nil
			}})
		reg.Register(c, &tools.Tool{
			Name: "task_update",
			Description: "Update a task on the shared board: status (todo, doing, blocked with a note, done), note, deps, scope, or take an " +
				"unassigned task (owner = your id) / hand yours back (owner = \"\"). You can only change tasks you own or unassigned ones; " +
				"a task can start (doing) only when its dependencies are done.",
			Params: tools.Schema(map[string]any{
				"id":     tools.Prop("string", "task id, e.g. 3"),
				"status": map[string]any{"type": "string", "enum": []string{StatusTodo, StatusDoing, StatusBlocked, StatusDone}},
				"note":   tools.Prop("string", "progress, hand-off or blocker note"),
				"owner":  tools.Prop("string", "your id to take it, empty to hand it back"),
				"title":  tools.Prop("string", ""),
				"detail": tools.Prop("string", ""),
				"deps":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"scope":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			}, "id"),
			Execute: func(_ context.Context, call tools.Call) (tools.Result, error) {
				sid, err := sessionOf(call)
				if err != nil {
					return tools.Errorf("%v", err), nil
				}
				ch, id, err := change(call.Args)
				if err != nil {
					return tools.Errorf("%v", err), nil
				}
				v, err := s.Update(sid, id, Actor{Agent: call.Agent}, ch)
				if err != nil {
					return tools.Errorf("%v", err), nil
				}
				msg := fmt.Sprintf("task #%s is %s", v.ID, v.Status)
				// tell the agent what became ready
				if v.Status == StatusDone {
					b, _ := s.Get(sid)
					var ready []string
					for _, t := range b.Tasks {
						if t.Ready && contains(t.Deps, v.ID) {
							ready = append(ready, "#"+t.ID)
						}
					}
					if len(ready) > 0 {
						msg += "; now ready: " + strings.Join(ready, ", ")
					}
				}
				return tools.Result{Content: msg}, nil
			}})
		// Agents in a tree with a non-empty board see it in their request
		// (blank chats included: the board belongs to the conversation).
		agentloop.Request.Use(c, func(ctx context.Context, x agentloop.RequestArgs, next kernel.Next[agentloop.RequestArgs, *llm.Request]) (*llm.Request, error) {
			req, err := next(ctx, x)
			if err != nil || req == nil || x.Session == nil {
				return req, err
			}
			b, err := s.Get(x.Session.ID())
			if err != nil || len(b.Tasks) == 0 {
				return req, nil
			}
			open := 0
			for _, t := range b.Tasks {
				if t.Status != StatusDone {
					open++
				}
			}
			if open == 0 {
				return req, nil
			}
			req.System = strings.TrimSpace(req.System + "\n\n" + Format(b, x.Agent.ID()) +
				"\nKeep the board current with task_update: start (doing) when you begin, done when finished, blocked with a note when stuck.")
			return req, nil
		})
		return c.Provide(ServiceKey, s)
	},
})

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// PersonFunc names the person behind a request (the members service).
type PersonFunc interface {
	PersonFor(deviceID string) (name string, canEdit bool)
}

// APIPlugin exposes the board to people:
//
//	GET    /api/board?session=<id>
//	POST   /api/board/tasks?session=<id>          Change
//	PATCH  /api/board/tasks/{id}?session=<id>     Change
//	DELETE /api/board/tasks/{id}?session=<id>
var APIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/taskboard-api",
	Inject:      []string{gateway.ServiceKey, ServiceKey},
	Optional:    []string{MembersKey},
	Description: "Task board API and live updates.",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		s := kernel.Use[*Service](c, ServiceKey)
		viewers := []string{"mobile", "viewer"}
		editors := []string{"mobile"}
		Changed.Listen(c, func(root string) { g.Hub().Publish("board.changed", map[string]string{"root": root}) })
		actor := func(r *gateway.Request) (Actor, error) {
			name := "你"
			if p, ok := kernel.Lookup[PersonFunc](c, MembersKey); ok && r.DeviceID != "" {
				n, can := p.PersonFor(r.DeviceID)
				if !can {
					return Actor{}, &gateway.Error{Status: 403, Code: "forbidden", Message: "your role cannot change the task board"}
				}
				name = n
			}
			return Actor{Person: name}, nil
		}
		sid := func(r *gateway.Request) string { return r.HTTP.URL.Query().Get("session") }
		wrap := func(err error) error {
			switch {
			case err == nil:
				return nil
			case err == ErrNotFound || strings.Contains(err.Error(), "not found"):
				return gateway.NotFound()
			}
			var ge *gateway.Error
			if e, ok := err.(*gateway.Error); ok {
				ge = e
				return ge
			}
			return gateway.BadRequest("%v", err)
		}
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/board", Devices: viewers,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				b, err := s.Get(sid(r))
				if err != nil {
					return nil, gateway.NotFound()
				}
				return b, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/board/tasks", Devices: editors,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				a, err := actor(r)
				if err != nil {
					return nil, err
				}
				var ch Change
				if err := r.Decode(&ch); err != nil {
					return nil, err
				}
				v, err := s.Add(sid(r), a, ch)
				return v, wrap(err)
			}})
		g.Handle(c, gateway.Route{Method: "PATCH", Path: "/api/board/tasks/{id}", Devices: editors,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				a, err := actor(r)
				if err != nil {
					return nil, err
				}
				var ch Change
				if err := r.Decode(&ch); err != nil {
					return nil, err
				}
				v, err := s.Update(sid(r), r.Params["id"], a, ch)
				return v, wrap(err)
			}})
		g.Handle(c, gateway.Route{Method: "DELETE", Path: "/api/board/tasks/{id}", Devices: editors,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				a, err := actor(r)
				if err != nil {
					return nil, err
				}
				return nil, wrap(s.Remove(sid(r), r.Params["id"], a))
			}})
		return nil
	},
})
