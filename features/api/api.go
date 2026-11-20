// Package api registers the core remote methods on the gateway and bridges
// kernel events to the gateway's SSE stream.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/attachments"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/kernel"
)

// AgentView is the API shape of an agent.
type AgentView struct {
	agent.Spec
	Status agent.Status `json:"status"`
	Detail string       `json:"detail,omitempty"`
	// StatusSession is where a waiting status is waiting (approval / takeover).
	StatusSession string        `json:"statusSession,omitempty"`
	Paused        bool          `json:"paused"`
	Queue         []agent.Input `json:"queue"`
	Workspace     string        `json:"workspace"`
	Effective     string        `json:"effectiveMode"`
}

func view(a *agent.Agent) AgentView {
	st, d := a.Status()
	q := a.Queue()
	if q == nil {
		q = []agent.Input{}
	}
	return AgentView{Spec: a.Spec(), Status: st, Detail: d, StatusSession: a.StatusSession(), Paused: a.Paused(), Queue: q, Workspace: a.Workspace(), Effective: a.Mode()}
}

// connectorsPatch reads the "connectors" field of an agent PATCH: absent →
// unchanged (set=false); null → no limit; a list → those connectors.
// Names must be known connectors; names already on the agent's list are
// kept even when their connector is gone (removed or not loaded yet), so
// a disconnected server never blocks saving.
func connectorsPatch(c *kernel.Context, a *agent.Agent, raw json.RawMessage) (list *[]string, set bool, err error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	if string(raw) == "null" {
		return nil, true, nil
	}
	var names []string
	if err := json.Unmarshal(raw, &names); err != nil {
		return nil, false, gateway.BadRequest("connectors must be a list of names or null")
	}
	prev, _ := a.Spec().ConnectorList()
	reg, _ := kernel.Lookup[*tools.Registry](c, tools.ServiceKey)
	out := []string{}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || slices.Contains(out, n) {
			continue
		}
		if !slices.Contains(prev, n) && (reg == nil || !reg.HasConnector(n)) {
			return nil, false, gateway.BadRequest("unknown connector %q", n)
		}
		out = append(out, n)
	}
	return &out, true, nil
}

// ProviderView describes one model provider.
type ProviderView struct {
	ID     string          `json:"id"`
	Kind   string          `json:"kind"`
	Models []llm.ModelInfo `json:"models"`
	Quota  *llm.Quota      `json:"quota,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// Access is the `access` service (teams): what the person behind a device
// may see. Without it everything is visible.
type Access interface {
	Agent(deviceID string, a *agent.Agent) bool
	Session(deviceID, id string) bool
	ChatOwner(deviceID string) string
}

// TeamChecker is the `teams` service as seen by the API.
type TeamChecker interface {
	Exists(id string) bool
	DefaultMode(id string) string
}

// Approver is the `members` service as seen by the API: who may approve
// what, and the name a device's messages carry.
type Approver interface {
	CanApprove(deviceID, category string) (name string, ok bool)
	SenderName(deviceID string) string
}

type sendBody struct {
	Text    string `json:"text"`
	Model   string `json:"model,omitempty"` // provider/model[@effort]
	Session string `json:"session,omitempty"`
	From    string `json:"from,omitempty"`
	Front   bool   `json:"front,omitempty"`
	// Queue: wait for the running turn to end instead of joining it.
	Queue bool `json:"queue,omitempty"`
	// Attachments returned by POST /api/agents/{id}/uploads.
	Attachments []struct {
		Path string `json:"path"`
		Name string `json:"name"`
	} `json:"attachments,omitempty"`
}

// resendBody: POST /api/agents/{id}/resend. Seq is a user message to the
// agent; Text unset regenerates (that message and the ones after it are
// sent again as they were), Text set edits it (only it is sent, edited).
type resendBody struct {
	Session string  `json:"session"`
	Seq     int64   `json:"seq"`
	Text    *string `json:"text,omitempty"`
	Model   string  `json:"model,omitempty"` // provider/model[@effort]
}

type createBody struct {
	agent.Spec
	Models []string `json:"modelRefs,omitempty"`
	// Template is the key of the template the form started from; it names
	// the id when the agent's name is not ASCII.
	Template string `json:"template,omitempty"`
	// Greet: the new agent introduces itself in its conversation (a model
	// turn), before the person says anything.
	Greet bool `json:"greet,omitempty"`
}

type decideBody struct {
	Outcome   approval.Outcome `json:"outcome"`
	Message   string           `json:"message,omitempty"`
	Remember  bool             `json:"remember,omitempty"`
	Confirmed bool             `json:"confirmed,omitempty"`
}

// Plugin registers the core API.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/api",
	Inject:      []string{gateway.ServiceKey, agent.ServiceKey, session.ServiceKey, llm.ServiceKey},
	Description: "Core remote methods: agents, sessions, approvals, models, plugins.",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		agents := kernel.Use[*agent.Registry](c, agent.ServiceKey)
		sessions := kernel.Use[*session.Store](c, session.ServiceKey)
		llms := kernel.Use[*llm.Registry](c, llm.ServiceKey)
		hub := g.Hub()
		h := c.Host()

		getAgent := func(r *gateway.Request) (*agent.Agent, error) {
			a, ok := agents.Get(r.Params["id"])
			if !ok {
				return nil, gateway.NotFound()
			}
			return a, nil
		}
		mobile := []string{"mobile", "im"}

		// ---- system
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/health", Devices: mobile,
			Handle: func(context.Context, *gateway.Request) (any, error) {
				return map[string]any{"ok": true, "time": time.Now().UTC()}, nil
			}})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/plugins",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				ps := h.Plugins()
				if st := r.HTTP.URL.Query().Get("state"); st != "" {
					var out []kernel.PluginInfo
					for _, p := range ps {
						if strings.EqualFold(string(p.State), st) {
							out = append(out, p)
						}
					}
					ps = out
				}
				return map[string]any{"plugins": ps, "services": h.Services()}, nil
			}})

		// ---- agents
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/agents", Devices: mobile,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				out := []AgentView{}
				acc, limited := kernel.Lookup[Access](c, "access")
				for _, a := range agents.List() {
					if limited && !acc.Agent(r.DeviceID, a) {
						continue
					}
					out = append(out, view(a))
				}
				return map[string]any{"agents": out}, nil
			}})
		// phones too: the access layer decides (a team member creates agents
		// in teams they act in, and anyone their own chats)
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/agents", Devices: mobile,
			Validate: func(r *gateway.Request) error {
				// without the access layer only the desktop creates agents
				if _, ok := kernel.Lookup[Access](c, "access"); !ok && r.Device != "desktop" {
					return &gateway.Error{Status: 403, Code: "forbidden", Message: "not allowed from this device"}
				}
				var b createBody
				if err := r.Decode(&b); err != nil {
					return err
				}
				if strings.TrimSpace(b.Name) == "" && b.Kind != agent.KindChat {
					return gateway.BadRequest("name is required")
				}
				if b.Kind != "" && b.Kind != agent.KindChat {
					return gateway.BadRequest("unknown kind %q", b.Kind)
				}
				// subagents only; a project folder is checked by the registry
				if b.Ephemeral || b.Parent != "" {
					return gateway.BadRequest("ephemeral agents are created by delegation only")
				}
				if b.Team != "" {
					if ts, ok := kernel.Lookup[TeamChecker](c, "teams"); ok && !ts.Exists(b.Team) {
						return gateway.BadRequest("no team %q", b.Team)
					}
				}
				if b.Mode == agent.ModeHost {
					return gateway.BadRequest("host mode cannot be enabled at creation")
				}
				for _, m := range b.Models {
					ref, err := llm.ParseRef(m)
					if err != nil {
						return gateway.BadRequest("%v", err)
					}
					if err := knownModel(llms, ref); err != nil {
						return err
					}
				}
				return nil
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b createBody
				_ = r.Decode(&b)
				spec := b.Spec
				spec.ID, spec.IDHint = "", b.Template
				// a new agent starts in its team's default run mode
				if spec.Mode == "" && spec.Kind != agent.KindChat {
					if ts, ok := kernel.Lookup[TeamChecker](c, "teams"); ok {
						spec.Mode = ts.DefaultMode(spec.Team)
					}
				}
				// a blank chat is personal: it belongs to whoever started it
				spec.Owner = ""
				if spec.Kind == agent.KindChat {
					if acc, ok := kernel.Lookup[Access](c, "access"); ok {
						spec.Owner = acc.ChatOwner(r.DeviceID)
					}
				}
				for _, m := range b.Models {
					ref, _ := llm.ParseRef(m)
					spec.Models = append(spec.Models, ref)
				}
				a, err := agents.Create(spec)
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				if b.Greet && spec.Kind != agent.KindChat {
					// it waits for the brief being written from the name, so it
					// introduces itself as that agent
					if _, err := a.Enqueue(agent.Input{Text: agentloop.GreetingPrompt, Tag: agentloop.GreetingTag}); err != nil {
						c.Log().Warn("new agent greeting not queued", "agent", a.ID(), "err", err)
					}
				}
				return view(a), nil
			}})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/agents/{id}", Devices: mobile,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				a, err := getAgent(r)
				if err != nil {
					return nil, err
				}
				return view(a), nil
			}})
		g.Handle(c, gateway.Route{Method: "PATCH", Path: "/api/agents/{id}",
			Validate: func(r *gateway.Request) error {
				var b map[string]any
				if err := r.Decode(&b); err != nil {
					return err
				}
				if refs, ok := b["modelRefs"].([]any); ok {
					for _, x := range refs {
						s, _ := x.(string)
						ref, err := llm.ParseRef(s)
						if err != nil {
							return gateway.BadRequest("%v", err)
						}
						if err := knownModel(llms, ref); err != nil {
							return err
						}
					}
				}
				if m, ok := b["mode"].(string); ok && m != agent.ModeReadOnly && m != agent.ModeSandbox && m != agent.ModeHost {
					return gateway.BadRequest("unknown mode %q", m)
				}
				return nil
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				a, err := getAgent(r)
				if err != nil {
					return nil, err
				}
				var b struct {
					Name   *string   `json:"name"`
					Role   *string   `json:"role"`
					Mode   *string   `json:"mode"`
					Color  *string   `json:"color"`
					Shape  *string   `json:"shape"`
					Face   *string   `json:"face"`
					Folder *string   `json:"folder"`
					Models *[]string `json:"modelRefs"`
					// Session is where the person changed it (the timeline
					// that records a new first model); "" = the agent's own.
					Session     string    `json:"session"`
					NativeTools *[]string `json:"nativeTools"`
					// Connectors: the connectors (by name) the agent may use;
					// null = every connector its scope allows.
					Connectors json.RawMessage `json:"connectors"`
					// ProjectDir: a folder to work in ("" = the agent's own workspace)
					ProjectDir *string `json:"projectDir"`
					// ToolSearch: "" (auto) | always | never; AlwaysLoad: tool
					// names or groups loaded from the start.
					ToolSearch *string   `json:"toolSearch"`
					AlwaysLoad *[]string `json:"alwaysLoad"`
				}
				_ = r.Decode(&b)
				if b.ToolSearch != nil && *b.ToolSearch != "" && *b.ToolSearch != tools.DeferAuto && *b.ToolSearch != tools.DeferAlways && *b.ToolSearch != tools.DeferNever {
					return nil, gateway.BadRequest("toolSearch is auto, always or never")
				}
				conns, setConns, err := connectorsPatch(c, a, b.Connectors)
				if err != nil {
					return nil, err
				}
				if b.ProjectDir != nil {
					prev := a.Workspace()
					if err := a.SetProjectDir(strings.TrimSpace(*b.ProjectDir)); err != nil {
						return nil, gateway.BadRequest("%v", err)
					}
					if now := a.Workspace(); now != prev {
						if ss, err := a.Session(); err == nil {
							_ = a.Inject(ss, fmt.Sprintf("[工作目录已更改] 从现在起在 %s 工作（之前是 %s）。相对路径和命令都从这里开始；之前目录里的文件不在这里。", now, prev), "workdir")
						}
					}
				}
				prevMode := a.Mode()
				var prevFirst llm.ModelRef
				if ms := a.Spec().Models; len(ms) > 0 {
					prevFirst = ms[0]
				}
				err = a.Update(func(s *agent.Spec) {
					set := func(dst *string, v *string) {
						if v != nil {
							*dst = *v
						}
					}
					set(&s.Name, b.Name)
					set(&s.Role, b.Role)
					set(&s.Color, b.Color)
					set(&s.Shape, b.Shape)
					set(&s.Face, b.Face)
					set(&s.Folder, b.Folder)
					if b.Mode != nil {
						s.Mode = *b.Mode
						s.ModeUntil = time.Time{}
					}
					if setConns {
						s.Connectors = conns
					}
					if b.ToolSearch != nil {
						s.ToolSearch = *b.ToolSearch
						if s.ToolSearch == tools.DeferAuto {
							s.ToolSearch = ""
						}
					}
					if b.AlwaysLoad != nil {
						s.AlwaysLoad = nil
						for _, n := range *b.AlwaysLoad {
							if n = strings.TrimSpace(n); n != "" && !slices.Contains(s.AlwaysLoad, n) {
								s.AlwaysLoad = append(s.AlwaysLoad, n)
							}
						}
					}
					if b.NativeTools != nil {
						s.NativeTools = nil
						for _, t := range *b.NativeTools {
							if t == llm.NativeWebSearch || t == llm.NativeReadURL {
								s.NativeTools = append(s.NativeTools, t)
							}
						}
					}
					if b.Models != nil {
						s.Models = nil
						for _, m := range *b.Models {
							ref, _ := llm.ParseRef(m)
							s.Models = append(s.Models, ref)
						}
					}
				})
				if err != nil {
					return nil, err
				}
				// a new first model is worth a line in the conversation
				if ms := a.Spec().Models; b.Models != nil && len(ms) > 0 && ms[0].String() != prevFirst.String() {
					ss, err := a.Session()
					if b.Session != "" {
						if x, e := sessions.Get(b.Session); e == nil && slices.Contains(x.Meta().Agents, a.ID()) {
							ss, err = x, nil
						}
					}
					if err == nil {
						_, _ = ss.Append(session.TypeModelChosen, a.ID(), session.ModelSwitchData{From: prevFirst, To: ms[0], Reason: "chosen"})
					}
				}
				if b.Mode != nil && *b.Mode != prevMode {
					if ss, err := a.Session(); err == nil {
						sp := a.Spec()
						_, _ = ss.Append(session.TypeModeChanged, a.ID(), map[string]any{"from": prevMode, "to": sp.Mode, "until": sp.ModeUntil})
					}
				}
				return view(a), nil
			}})
		g.Handle(c, gateway.Route{Method: "DELETE", Path: "/api/agents/{id}",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if _, err := getAgent(r); err != nil {
					return nil, err
				}
				return nil, agents.Delete(r.Params["id"])
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/agents/{id}/messages", Devices: mobile,
			Validate: func(r *gateway.Request) error {
				var b sendBody
				if err := r.Decode(&b); err != nil {
					return err
				}
				if strings.TrimSpace(b.Text) == "" && len(b.Attachments) == 0 {
					return gateway.BadRequest("text is required")
				}
				if len(b.Attachments) > attachments.MaxPerInput {
					return gateway.BadRequest("at most %d attachments per message", attachments.MaxPerInput)
				}
				if b.Model != "" {
					if _, err := llm.ParseRef(b.Model); err != nil {
						return gateway.BadRequest("%v", err)
					}
				}
				return nil
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				a, err := getAgent(r)
				if err != nil {
					return nil, err
				}
				var b sendBody
				_ = r.Decode(&b)
				in := agent.Input{Text: b.Text, From: b.From, Session: b.Session, Front: b.Front, Queue: b.Queue}
				// a team member's messages carry their name, whatever the client says
				if who, ok := kernel.Lookup[Approver](c, "members"); ok && r.DeviceID != "" {
					in.From = who.SenderName(r.DeviceID)
				}
				for _, x := range b.Attachments {
					at, err := attachments.Resolve(a.Workspace(), x.Path, x.Name)
					if err != nil {
						return nil, gateway.BadRequest("%v", err)
					}
					in.Attachments = append(in.Attachments, at)
				}
				if b.Model != "" {
					ref, _ := llm.ParseRef(b.Model)
					in.Model = &ref
				}
				if in.Session != "" {
					ss, err := sessions.Get(in.Session)
					if err != nil || !contains(ss.Meta().Agents, a.ID()) {
						return nil, gateway.NotFound()
					}
				}
				in, err = a.Enqueue(in)
				if err != nil {
					return nil, err
				}
				return in, nil
			}})
		// Regenerate / edit and resend: history from a message on is taken
		// back for the agent's model (history/rewind; the log keeps it) and
		// the message is sent again, as it was or edited. One-to-one
		// conversations only, while the agent is idle.
		var resendMu sync.Mutex
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/agents/{id}/resend", Devices: mobile,
			Validate: func(r *gateway.Request) error {
				var b resendBody
				if err := r.Decode(&b); err != nil {
					return err
				}
				if b.Session == "" || b.Seq <= 0 {
					return gateway.BadRequest("session and seq are required")
				}
				if b.Text != nil && strings.TrimSpace(*b.Text) == "" {
					return gateway.BadRequest("text is required")
				}
				if b.Model != "" {
					if _, err := llm.ParseRef(b.Model); err != nil {
						return gateway.BadRequest("%v", err)
					}
				}
				return nil
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				a, err := getAgent(r)
				if err != nil {
					return nil, err
				}
				var b resendBody
				_ = r.Decode(&b)
				ss, err := sessions.Get(b.Session)
				if err != nil || !contains(ss.Meta().Agents, a.ID()) {
					return nil, gateway.NotFound()
				}
				if ss.Meta().Kind != "agent" {
					return nil, gateway.BadRequest("regenerate and edit work in one-to-one conversations only")
				}
				resendMu.Lock()
				defer resendMu.Unlock()
				if st, _ := a.Status(); st == agent.StatusRunning || st == agent.StatusWaiting || a.CurrentInput() != "" || len(a.Queue()) > 0 || a.Paused() {
					return nil, gateway.Conflict("the agent is busy; wait until it is idle")
				}
				events := ss.Events()
				if b.Seq <= session.CompactedUpto(events, a.ID()) {
					return nil, gateway.Conflict("that part of the conversation was compacted into a summary and cannot be resent")
				}
				mine := func(e session.Event) (session.UserMessage, bool) {
					var m session.UserMessage
					if e.Type != session.TypeUserMessage || e.Decode(&m) != nil || (m.To != "" && m.To != a.ID()) {
						return m, false
					}
					return m, true
				}
				var ins []agent.Input
				found := false
				for _, e := range session.Visible(events, a.ID()) {
					if e.Seq < b.Seq {
						continue
					}
					m, ok := mine(e)
					if e.Seq == b.Seq {
						if !ok {
							break
						}
						found = true
					}
					if !ok {
						continue
					}
					ins = append(ins, agent.Input{Text: m.Text, From: m.From, Attachments: m.Attachments})
					if b.Text != nil {
						break // edit: only the edited message is sent; later ones are dropped
					}
				}
				if !found {
					return nil, gateway.BadRequest("seq is not a message to this agent that it still sees")
				}
				reason := session.RewindRegenerate
				if b.Text != nil {
					reason = session.RewindEdit
					ins[0].Text = *b.Text
					if who, ok := kernel.Lookup[Approver](c, "members"); ok && r.DeviceID != "" {
						ins[0].From = who.SenderName(r.DeviceID)
					}
				}
				var ref *llm.ModelRef
				if b.Model != "" {
					x, _ := llm.ParseRef(b.Model)
					ref = &x
				}
				rw, err := ss.Append(session.TypeRewind, a.ID(), session.RewindData{From: b.Seq, Reason: reason})
				if err != nil {
					return nil, err
				}
				out := make([]agent.Input, 0, len(ins))
				for _, in := range ins {
					in.Session, in.Model = ss.ID(), ref
					q, err := a.Enqueue(in)
					if err != nil {
						return nil, err
					}
					out = append(out, q)
				}
				return map[string]any{"rewind": rw.Seq, "from": b.Seq, "reason": reason, "inputs": out}, nil
			}})
		for _, action := range []string{"stop", "pause", "resume"} {
			action := action
			g.Handle(c, gateway.Route{Method: "POST", Path: "/api/agents/{id}/" + action, Devices: mobile,
				Handle: func(_ context.Context, r *gateway.Request) (any, error) {
					a, err := getAgent(r)
					if err != nil {
						return nil, err
					}
					switch action {
					case "stop":
						a.Stop()
					case "pause":
						a.Pause()
					case "resume":
						a.Resume()
					}
					return view(a), nil
				}})
		}
		g.Handle(c, gateway.Route{Method: "DELETE", Path: "/api/agents/{id}/queue/{input}", Devices: mobile,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				a, err := getAgent(r)
				if err != nil {
					return nil, err
				}
				if !a.Cancel(r.Params["input"]) {
					return nil, gateway.NotFound()
				}
				return view(a), nil
			}})
		// 插话: a queued message joins the running turn now
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/agents/{id}/queue/{input}/steer", Devices: mobile,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				a, err := getAgent(r)
				if err != nil {
					return nil, err
				}
				if !a.Steer(r.Params["input"]) {
					return nil, gateway.NotFound()
				}
				return view(a), nil
			}})
		// connectors agents can be given (MCP servers, built-in connectors)
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/connectors",
			Handle: func(context.Context, *gateway.Request) (any, error) {
				reg, ok := kernel.Lookup[*tools.Registry](c, tools.ServiceKey)
				if !ok {
					return map[string]any{"connectors": []any{}}, nil
				}
				return map[string]any{"connectors": reg.Connectors()}, nil
			}})
		// Settings › 通用 › 默认工作区: where new agents' own folders go.
		type wsView struct {
			Dir       string `json:"dir"`
			IsDefault bool   `json:"isDefault"`
			Default   string `json:"default"`
		}
		wsNow := func() wsView {
			d, def := agents.WorkspaceBase()
			return wsView{Dir: d, IsDefault: def, Default: agents.DefaultWorkspaceBase()}
		}
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/settings/workspace",
			Handle: func(context.Context, *gateway.Request) (any, error) { return wsNow(), nil }})
		g.Handle(c, gateway.Route{Method: "PUT", Path: "/api/settings/workspace",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b struct {
					Dir string `json:"dir"`
				}
				if err := r.Decode(&b); err != nil {
					return nil, err
				}
				if _, err := agents.SetWorkspaceBase(b.Dir); err != nil {
					var we *agent.WorkspaceError
					if errors.As(err, &we) {
						return nil, &gateway.Error{Status: 400, Code: "workspace_" + we.Code, Message: we.Msg}
					}
					return nil, gateway.BadRequest("%v", err)
				}
				return wsNow(), nil
			}})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/agents/{id}/tools",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				a, err := getAgent(r)
				if err != nil {
					return nil, err
				}
				reg, ok := kernel.Lookup[*tools.Registry](c, tools.ServiceKey)
				if !ok {
					return map[string]any{"tools": []any{}}, nil
				}
				// groups: the agent's tool families and whether each is in the
				// tier tool search holds back (Settings: 工具加载)
				type group struct {
					tools.Group
					Deferred bool `json:"deferred"`
				}
				var deferred, core []*tools.Tool
				for _, t := range reg.Offerable(a.ID(), "agent") {
					if t.Load == tools.LoadMeta {
						continue
					}
					if t.Deferred() {
						deferred = append(deferred, t)
					} else {
						core = append(core, t)
					}
				}
				var groups []group
				for _, g := range tools.Groups(deferred) {
					groups = append(groups, group{Group: g, Deferred: true})
				}
				for _, g := range tools.Groups(core) {
					groups = append(groups, group{Group: g})
				}
				return map[string]any{"tools": reg.Schemas(a.ID()), "groups": groups, "deferredTokens": tools.SchemaTokens(deferred)}, nil
			}})

		// ---- sessions
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/sessions", Devices: mobile,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				list := sessions.List()
				live := list[:0:0]
				for _, m := range list {
					if !m.Deleted {
						live = append(live, m)
					}
				}
				list = live
				if acc, ok := kernel.Lookup[Access](c, "access"); ok {
					out := list[:0:0]
					for _, m := range list {
						if acc.Session(r.DeviceID, m.ID) {
							out = append(out, m)
						}
					}
					list = out
				}
				return map[string]any{"sessions": list}, nil
			}})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/sessions/{id}/events", Devices: mobile,
			Validate: func(r *gateway.Request) error {
				if s := r.HTTP.URL.Query().Get("since"); s != "" {
					if _, err := strconv.ParseInt(s, 10, 64); err != nil {
						return gateway.BadRequest("since must be an integer")
					}
				}
				return nil
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				ss, err := sessions.Get(r.Params["id"])
				if err != nil {
					return nil, gateway.NotFound()
				}
				since, _ := strconv.ParseInt(r.HTTP.URL.Query().Get("since"), 10, 64)
				evs := ss.Since(since)
				if evs == nil {
					evs = []session.Event{}
				}
				return map[string]any{"meta": ss.Meta(), "events": evs}, nil
			}})

		// ---- approvals
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/approvals", Devices: mobile,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				in, ok := kernel.Lookup[*approval.Inbox](c, approval.InboxKey)
				if !ok {
					return map[string]any{"pending": []any{}}, nil
				}
				p := []approval.Request{}
				acc, limited := kernel.Lookup[Access](c, "access")
				for _, x := range in.Pending() {
					if !limited || acc.Session(r.DeviceID, x.Session) {
						p = append(p, x)
					}
				}
				return map[string]any{"pending": p}, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/approvals/{callId}", Devices: mobile,
			Validate: func(r *gateway.Request) error {
				var b decideBody
				if err := r.Decode(&b); err != nil {
					return err
				}
				if b.Outcome != approval.OutcomeAllowedOnce && b.Outcome != approval.OutcomeRejected {
					return gateway.BadRequest("outcome must be allowed-once or rejected")
				}
				return nil
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				in, ok := kernel.Lookup[*approval.Inbox](c, approval.InboxKey)
				if !ok {
					return nil, gateway.NotFound()
				}
				var b decideBody
				_ = r.Decode(&b)
				// Mobile/IM may not remember choices (rules changes are desktop-only).
				if r.Device != "desktop" {
					b.Remember = false
				}
				by := r.Device
				// Team members approve only what their role trusts them with.
				if who, ok := kernel.Lookup[Approver](c, "members"); ok && r.DeviceID != "" {
					cat := ""
					for _, p := range in.Pending() {
						if p.CallID == r.Params["callId"] {
							cat = p.Category
						}
					}
					if cat == "" {
						return nil, gateway.NotFound()
					}
					name, allowed := who.CanApprove(r.DeviceID, cat)
					if !allowed && b.Outcome != approval.OutcomeRejected {
						return nil, &gateway.Error{Status: 403, Code: "forbidden", Message: fmt.Sprintf("your role cannot approve %s actions; you can reject it or ask the owner", cat)}
					}
					if name != "" {
						by = name
					}
				}
				if !in.Decide(r.Params["callId"], approval.Decision{Outcome: b.Outcome, Message: b.Message, Remember: b.Remember, Confirmed: b.Confirmed, By: by}) {
					return nil, gateway.NotFound()
				}
				return nil, nil
			}})

		// ---- models
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/models", Devices: mobile,
			Handle: func(ctx context.Context, _ *gateway.Request) (any, error) {
				out := []ProviderView{}
				for _, ad := range llms.List() {
					pv := ProviderView{ID: ad.ID(), Kind: ad.Kind(), Models: []llm.ModelInfo{}}
					mctx, cancel := context.WithTimeout(ctx, 3*time.Second)
					ms, err := llms.ModelsOf(mctx, ad)
					cancel()
					if err != nil {
						pv.Error = err.Error()
					} else if ms != nil {
						pv.Models = ms
					}
					if q, ok := ad.(llm.QuotaReporter); ok {
						qq := q.Quota(ctx)
						pv.Quota = &qq
					}
					out = append(out, pv)
				}
				resp := map[string]any{"providers": out}
				if rt, ok := kernel.Lookup[*router.Router](c, agentloop.RouterKey); ok {
					resp["exhausted"] = rt.States()
					resp["switches"] = rt.Switches()
				}
				return resp, nil
			}})

		// ---- event bridge
		session.OnEvent.Listen(c, func(a session.Appended) { hub.Publish("session.event", a.Event) })
		agent.StatusChanged.Listen(c, func(e agent.StatusEvent) { hub.Publish("agent.status", e) })
		agent.QueueChanged.Listen(c, func(id string) {
			if a, ok := agents.Get(id); ok {
				hub.Publish("agent.updated", view(a))
			}
		})
		agent.Updated.Listen(c, func(s agent.Spec) {
			if a, ok := agents.Get(s.ID); ok {
				hub.Publish("agent.updated", view(a))
			}
		})
		agent.Created.Listen(c, func(_ context.Context, a *agent.Agent) (struct{}, error) {
			hub.Publish("agent.updated", view(a))
			return struct{}{}, nil
		})
		agent.Deleted.Listen(c, func(id string) { hub.Publish("agent.deleted", map[string]string{"id": id}) })
		agentloop.Stream.Listen(c, func(e agentloop.StreamEvent) { hub.Publish("agent.stream", e) })
		approval.Changed.Listen(c, func(struct{}) { hub.Publish("approvals.changed", struct{}{}) })
		llm.AdaptersChanged.Listen(c, func(struct{}) { hub.Publish("models.changed", struct{}{}) })
		return nil
	},
})

// knownModel refuses a model its provider does not offer: a connected
// provider whose list is known and lacks it, or a provider that is not
// connected. A typo would otherwise only show at the first message, as a
// failover. "default" (the provider's own choice) is always accepted.
func knownModel(llms *llm.Registry, ref llm.ModelRef) error {
	ad, ok := llms.Get(ref.Provider)
	if !ok {
		return gateway.BadRequest("no connected provider %q", ref.Provider)
	}
	if ref.Model == "" || ref.Model == "default" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ms, err := llms.ModelsOf(ctx, ad)
	if err != nil || len(ms) == 0 {
		return nil // cannot tell: let the first request decide
	}
	for _, m := range ms {
		if m.ID == ref.Model {
			return nil
		}
	}
	return gateway.BadRequest("%s has no model %q", ref.Provider, ref.Model)
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
