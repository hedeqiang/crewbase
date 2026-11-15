// Package api registers the core remote methods on the gateway and bridges
// kernel events to the gateway's SSE stream.
package api

import (
	"context"
	"fmt"
	"strconv"
	"strings"
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

// ProviderView describes one model provider.
type ProviderView struct {
	ID     string          `json:"id"`
	Kind   string          `json:"kind"`
	Models []llm.ModelInfo `json:"models"`
	Quota  *llm.Quota      `json:"quota,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// TeamChecker is the `teams` service as seen by the API.
type TeamChecker interface{ Exists(id string) bool }

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
	// Attachments returned by POST /api/agents/{id}/uploads.
	Attachments []struct {
		Path string `json:"path"`
		Name string `json:"name"`
	} `json:"attachments,omitempty"`
}

type createBody struct {
	agent.Spec
	Models []string `json:"modelRefs,omitempty"`
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
			Handle: func(context.Context, *gateway.Request) (any, error) {
				out := []AgentView{}
				for _, a := range agents.List() {
					out = append(out, view(a))
				}
				return map[string]any{"agents": out}, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/agents",
			Validate: func(r *gateway.Request) error {
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
					if _, err := llm.ParseRef(m); err != nil {
						return gateway.BadRequest("%v", err)
					}
				}
				return nil
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b createBody
				_ = r.Decode(&b)
				spec := b.Spec
				spec.ID = ""
				for _, m := range b.Models {
					ref, _ := llm.ParseRef(m)
					spec.Models = append(spec.Models, ref)
				}
				a, err := agents.Create(spec)
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
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
						if _, err := llm.ParseRef(s); err != nil {
							return gateway.BadRequest("%v", err)
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
					Name        *string   `json:"name"`
					Role        *string   `json:"role"`
					Mode        *string   `json:"mode"`
					Color       *string   `json:"color"`
					Shape       *string   `json:"shape"`
					Folder      *string   `json:"folder"`
					Models      *[]string `json:"modelRefs"`
					NativeTools *[]string `json:"nativeTools"`
					// ProjectDir: a folder to work in ("" = the agent's own workspace)
					ProjectDir *string `json:"projectDir"`
				}
				_ = r.Decode(&b)
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
					set(&s.Folder, b.Folder)
					if b.Mode != nil {
						s.Mode = *b.Mode
						s.ModeUntil = time.Time{}
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
				in := agent.Input{Text: b.Text, From: b.From, Session: b.Session, Front: b.Front}
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
				return map[string]any{"tools": reg.Schemas(a.ID())}, nil
			}})

		// ---- sessions
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/sessions", Devices: mobile,
			Handle: func(context.Context, *gateway.Request) (any, error) {
				return map[string]any{"sessions": sessions.List()}, nil
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
			Handle: func(context.Context, *gateway.Request) (any, error) {
				in, ok := kernel.Lookup[*approval.Inbox](c, approval.InboxKey)
				if !ok {
					return map[string]any{"pending": []any{}}, nil
				}
				p := in.Pending()
				if p == nil {
					p = []approval.Request{}
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
					ms, err := ad.Models(mctx)
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

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
