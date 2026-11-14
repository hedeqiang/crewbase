// Package console serves the desktop UI's remaining remote methods: session
// summaries, context breakdown, compact now / new conversation, workspace
// files (INSTRUCTIONS.md, MEMORY.md…), remembered approval choices and
// history, router controls, defaults for new agents, and hosts.
package console

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/capabilities/notify"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/features/compaction"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/features/rules"
	"github.com/hedeqiang/crewbase/kernel"
)

// SessionSummary is one row of the session list.
type SessionSummary struct {
	session.Meta
	Updated time.Time `json:"updated"`
	Last    *LastLine `json:"last,omitempty"`
	Events  int64     `json:"events"`
}

// LastLine is the latest visible line of a session.
type LastLine struct {
	Type  string    `json:"type"`
	Text  string    `json:"text"`
	Agent string    `json:"agent,omitempty"`
	Time  time.Time `json:"time"`
}

func lastLine(ss *session.Session) *LastLine {
	evs := ss.Events()
	for i := len(evs) - 1; i >= 0; i-- {
		e := evs[i]
		var text string
		switch e.Type {
		case session.TypeUserMessage:
			var m session.UserMessage
			_ = e.Decode(&m)
			text = m.Text
		case session.TypeAssistantMessage:
			var m session.AssistantMessage
			_ = e.Decode(&m)
			text = m.Message.Text()
		case session.TypeApprovalAsked:
			var r approval.Request
			_ = e.Decode(&r)
			text = r.Summary
		default:
			continue
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		if r := []rune(text); len(r) > 140 {
			text = string(r[:140]) + "…"
		}
		return &LastLine{Type: e.Type, Text: text, Agent: e.Agent, Time: e.Time}
	}
	return nil
}

// Settings are user defaults (settings › general / routing).
type Settings struct {
	DefaultMode   string   `json:"defaultMode"`
	DefaultModels []string `json:"defaultModels"`
	// DisplayName is how the person is shown (default "你").
	DisplayName string `json:"displayName,omitempty"`
	// DndUntil (RFC 3339) suppresses non-urgent notifications; "" = off.
	DndUntil string `json:"dndUntil,omitempty"`
}

// resolveIn returns an absolute path inside root, refusing escapes
// (including through symlinks).
func resolveIn(root, rel string) (string, error) {
	if rel == "" {
		rel = "."
	}
	if filepath.IsAbs(rel) {
		return "", errors.New("path must be relative to the workspace")
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	p := filepath.Join(rootReal, filepath.Clean(rel))
	real := p
	if r, err := filepath.EvalSymlinks(p); err == nil {
		real = r
	} else if r, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		real = filepath.Join(r, filepath.Base(p))
	}
	if real != rootReal && !strings.HasPrefix(real, rootReal+string(filepath.Separator)) {
		return "", errors.New("path is outside the workspace")
	}
	return real, nil
}

const maxFileBytes = 1 << 20

// Plugin registers the console API.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/console",
	Inject:      []string{gateway.ServiceKey, agent.ServiceKey, session.ServiceKey},
	Description: "Remote methods backing the desktop UI.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[struct {
			Dir string `json:"dir"`
		}](c)
		if err != nil {
			return err
		}
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		agents := kernel.Use[*agent.Registry](c, agent.ServiceKey)
		sessions := kernel.Use[*session.Store](c, session.ServiceKey)
		mobile := []string{"mobile", "im"}
		getAgent := func(r *gateway.Request) (*agent.Agent, error) {
			a, ok := agents.Get(r.Params["id"])
			if !ok {
				return nil, gateway.NotFound()
			}
			return a, nil
		}

		// ---- settings (defaults for new agents)
		settingsPath := ""
		if cfg.Dir != "" {
			settingsPath = filepath.Join(cfg.Dir, "settings.json")
		}
		load := func() Settings {
			st := Settings{DefaultMode: agent.ModeSandbox}
			if settingsPath != "" {
				if b, err := os.ReadFile(settingsPath); err == nil {
					_ = json.Unmarshal(b, &st)
				}
			}
			if len(st.DefaultModels) == 0 {
				for _, m := range agents.DefaultModels() {
					st.DefaultModels = append(st.DefaultModels, m.Full())
				}
			}
			return st
		}
		apply := func(st Settings) {
			if n, ok := kernel.Lookup[*notify.Service](c, notify.ServiceKey); ok {
				var until time.Time
				if st.DndUntil != "" {
					until, _ = time.Parse(time.RFC3339, st.DndUntil)
				}
				n.SetDoNotDisturb(until)
			}
			var refs []llm.ModelRef
			for _, s := range st.DefaultModels {
				if ref, err := llm.ParseRef(s); err == nil {
					refs = append(refs, ref)
				}
			}
			if len(refs) > 0 {
				agents.SetDefaultModels(refs)
			}
		}
		apply(load())
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/settings",
			Handle: func(context.Context, *gateway.Request) (any, error) { return load(), nil }})
		g.Handle(c, gateway.Route{Method: "PATCH", Path: "/api/settings",
			Validate: func(r *gateway.Request) error {
				var b Settings
				if err := r.Decode(&b); err != nil {
					return err
				}
				if b.DefaultMode == agent.ModeHost {
					return gateway.BadRequest("host mode cannot be a default")
				}
				if b.DefaultMode != "" && b.DefaultMode != agent.ModeSandbox && b.DefaultMode != agent.ModeReadOnly {
					return gateway.BadRequest("unknown mode %q", b.DefaultMode)
				}
				for _, m := range b.DefaultModels {
					if _, err := llm.ParseRef(m); err != nil {
						return gateway.BadRequest("%v", err)
					}
				}
				if b.DndUntil != "" {
					if _, err := time.Parse(time.RFC3339, b.DndUntil); err != nil {
						return gateway.BadRequest("dndUntil must be RFC 3339")
					}
				}
				if len([]rune(b.DisplayName)) > 40 {
					return gateway.BadRequest("displayName is too long")
				}
				return nil
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				cur := load()
				var b Settings
				_ = r.Decode(&b)
				if b.DefaultMode != "" {
					cur.DefaultMode = b.DefaultMode
				}
				if b.DefaultModels != nil {
					cur.DefaultModels = b.DefaultModels
				}
				var raw map[string]json.RawMessage
				_ = json.Unmarshal(r.Body, &raw)
				if _, ok := raw["displayName"]; ok {
					cur.DisplayName = strings.TrimSpace(b.DisplayName)
				}
				if _, ok := raw["dndUntil"]; ok {
					cur.DndUntil = b.DndUntil
				}
				if settingsPath != "" {
					data, _ := json.MarshalIndent(cur, "", "  ")
					if err := os.WriteFile(settingsPath, data, 0o600); err != nil {
						return nil, err
					}
				}
				apply(cur)
				return cur, nil
			}})

		// ---- session summaries
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/sessions/summary", Devices: mobile,
			Handle: func(context.Context, *gateway.Request) (any, error) {
				out := []SessionSummary{}
				for _, m := range sessions.List() {
					ss, err := sessions.Get(m.ID)
					if err != nil {
						continue
					}
					evs := ss.Events()
					sum := SessionSummary{Meta: m, Updated: m.Created, Last: lastLine(ss), Events: int64(len(evs))}
					if n := len(evs); n > 0 {
						sum.Updated = evs[n-1].Time
					}
					out = append(out, sum)
				}
				sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
				return map[string]any{"sessions": out}, nil
			}})

		// ---- context / compaction / new conversation
		compactor := func() (*compaction.Service, error) {
			cs, ok := kernel.Lookup[*compaction.Service](c, agentloop.CompactionKey)
			if !ok {
				return nil, gateway.Conflict("compaction is not enabled")
			}
			return cs, nil
		}
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/agents/{id}/context",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				a, err := getAgent(r)
				if err != nil {
					return nil, err
				}
				cs, err := compactor()
				if err != nil {
					return nil, err
				}
				return cs.Usage(a.ID(), r.HTTP.URL.Query().Get("session"))
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/agents/{id}/compact",
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				a, err := getAgent(r)
				if err != nil {
					return nil, err
				}
				cs, err := compactor()
				if err != nil {
					return nil, err
				}
				var b struct {
					Session string `json:"session"`
				}
				_ = r.Decode(&b)
				res, err := cs.CompactNow(context.WithoutCancel(ctx), a.ID(), b.Session)
				if err != nil {
					return nil, gateway.Conflict("%v", err)
				}
				return res, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/agents/{id}/sessions",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				a, err := getAgent(r)
				if err != nil {
					return nil, err
				}
				if cs, ok := kernel.Lookup[*compaction.Service](c, agentloop.CompactionKey); ok {
					ss, err := cs.NewConversation(a)
					if err != nil {
						return nil, err
					}
					return ss.Meta(), nil
				}
				sp := a.Spec()
				ss, err := sessions.Create(session.Meta{Kind: "agent", Title: sp.Name, Agents: []string{sp.ID}, Folder: sp.Folder})
				if err != nil {
					return nil, err
				}
				if err := a.SetSession(ss.ID()); err != nil {
					return nil, err
				}
				return ss.Meta(), nil
			}})

		// ---- workspace files
		type entry struct {
			Name     string    `json:"name"`
			Dir      bool      `json:"dir"`
			Size     int64     `json:"size"`
			Modified time.Time `json:"modified"`
		}
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/agents/{id}/files",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				a, err := getAgent(r)
				if err != nil {
					return nil, err
				}
				p, err := resolveIn(a.Workspace(), r.HTTP.URL.Query().Get("path"))
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				des, err := os.ReadDir(p)
				if err != nil {
					return nil, gateway.NotFound()
				}
				out := []entry{}
				for _, d := range des {
					if d.Name() == "agent.json" || d.Name() == "browser" {
						continue // internal
					}
					info, err := d.Info()
					if err != nil {
						continue
					}
					out = append(out, entry{Name: d.Name(), Dir: d.IsDir(), Size: info.Size(), Modified: info.ModTime()})
				}
				sort.Slice(out, func(i, j int) bool {
					if out[i].Dir != out[j].Dir {
						return out[i].Dir
					}
					return out[i].Name < out[j].Name
				})
				return map[string]any{"entries": out}, nil
			}})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/agents/{id}/file",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				a, err := getAgent(r)
				if err != nil {
					return nil, err
				}
				rel := r.HTTP.URL.Query().Get("path")
				p, err := resolveIn(a.Workspace(), rel)
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				info, err := os.Stat(p)
				if err != nil || info.IsDir() {
					return nil, gateway.NotFound()
				}
				if info.Size() > maxFileBytes {
					return map[string]any{"path": rel, "size": info.Size(), "tooLarge": true}, nil
				}
				b, err := os.ReadFile(p)
				if err != nil {
					return nil, err
				}
				return map[string]any{"path": rel, "size": info.Size(), "content": string(b), "binary": !utf8ish(b)}, nil
			}})
		g.Handle(c, gateway.Route{Method: "PUT", Path: "/api/agents/{id}/file",
			Validate: func(r *gateway.Request) error {
				var b struct {
					Path    string `json:"path"`
					Content string `json:"content"`
				}
				if err := r.Decode(&b); err != nil {
					return err
				}
				if b.Path == "" || len(b.Content) > maxFileBytes {
					return gateway.BadRequest("path is required and content must be under 1 MiB")
				}
				return nil
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				a, err := getAgent(r)
				if err != nil {
					return nil, err
				}
				var b struct {
					Path    string `json:"path"`
					Content string `json:"content"`
				}
				_ = r.Decode(&b)
				p, err := resolveIn(a.Workspace(), b.Path)
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					return nil, err
				}
				tmp := p + ".crewbase-tmp"
				if err := os.WriteFile(tmp, []byte(b.Content), 0o644); err != nil {
					return nil, err
				}
				if err := os.Rename(tmp, p); err != nil {
					return nil, err
				}
				return map[string]any{"path": b.Path, "size": len(b.Content)}, nil
			}})

		// ---- approvals: remembered choices and history
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/approvals/remembered",
			Handle: func(context.Context, *gateway.Request) (any, error) {
				e, ok := kernel.Lookup[*rules.Engine](c, rules.ServiceKey)
				if !ok {
					return map[string]any{"remembered": []any{}}, nil
				}
				list := e.Remembered()
				if list == nil {
					list = []rules.Choice{}
				}
				return map[string]any{"remembered": list}, nil
			}})
		g.Handle(c, gateway.Route{Method: "DELETE", Path: "/api/approvals/remembered/{agent}/{category}",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				e, ok := kernel.Lookup[*rules.Engine](c, rules.ServiceKey)
				if !ok {
					return nil, gateway.NotFound()
				}
				if err := e.Revoke(r.Params["agent"], r.Params["category"]); err != nil {
					return nil, gateway.NotFound()
				}
				return nil, nil
			}})
		type historyItem struct {
			approval.Request
			Decision *approval.Decision `json:"decision,omitempty"`
			Decided  time.Time          `json:"decided,omitzero"`
		}
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/approvals/history", Devices: mobile,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				limit, _ := strconv.Atoi(r.HTTP.URL.Query().Get("limit"))
				if limit <= 0 || limit > 500 {
					limit = 100
				}
				var items []historyItem
				for _, m := range sessions.List() {
					ss, err := sessions.Get(m.ID)
					if err != nil {
						continue
					}
					asked := map[string]int{}
					for _, e := range ss.Events() {
						switch e.Type {
						case session.TypeApprovalAsked:
							var req approval.Request
							if e.Decode(&req) == nil {
								asked[req.CallID] = len(items)
								items = append(items, historyItem{Request: req})
							}
						case session.TypeApprovalDecided:
							var d approval.DecidedData
							if e.Decode(&d) == nil {
								if i, ok := asked[d.CallID]; ok {
									dec := d.Decision
									items[i].Decision, items[i].Decided = &dec, e.Time
								}
							}
						}
					}
				}
				sort.Slice(items, func(i, j int) bool { return items[i].Asked.After(items[j].Asked) })
				if len(items) > limit {
					items = items[:limit]
				}
				if items == nil {
					items = []historyItem{}
				}
				return map[string]any{"history": items}, nil
			}})

		// ---- router controls
		getRouter := func() (*router.Router, error) {
			rt, ok := kernel.Lookup[agentloop.Router](c, agentloop.RouterKey)
			if !ok {
				return nil, gateway.Conflict("router is not enabled")
			}
			r, ok := rt.(*router.Router)
			if !ok {
				return nil, gateway.Conflict("router does not support this")
			}
			return r, nil
		}
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/models/reset",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				rt, err := getRouter()
				if err != nil {
					return nil, err
				}
				var b struct {
					Model string `json:"model"`
				}
				if err := r.Decode(&b); err != nil {
					return nil, err
				}
				if ref, err := llm.ParseRef(b.Model); err == nil {
					b.Model = ref.String()
				}
				rt.Reset(b.Model)
				return nil, nil
			}})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/router/routes",
			Handle: func(context.Context, *gateway.Request) (any, error) {
				rt, err := getRouter()
				if err != nil {
					return nil, err
				}
				routes := rt.Routes()
				if routes == nil {
					routes = []router.Route{}
				}
				return map[string]any{"routes": routes, "tasks": []string{router.TaskRoutine, router.TaskSummarize, router.TaskCode, router.TaskLongDoc}}, nil
			}})
		g.Handle(c, gateway.Route{Method: "PUT", Path: "/api/router/routes",
			Validate: func(r *gateway.Request) error {
				var b struct {
					Routes []router.Route `json:"routes"`
				}
				if err := r.Decode(&b); err != nil {
					return err
				}
				for _, rt := range b.Routes {
					if _, err := llm.ParseRef(rt.Model); err != nil {
						return gateway.BadRequest("%v", err)
					}
				}
				return nil
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				rt, err := getRouter()
				if err != nil {
					return nil, err
				}
				var b struct {
					Routes []router.Route `json:"routes"`
				}
				_ = r.Decode(&b)
				rt.SetRoutes(b.Routes)
				return map[string]any{"routes": rt.Routes()}, nil
			}})

		// ---- system info (profile menu, about)
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/system", Devices: mobile,
			Handle: func(context.Context, *gateway.Request) (any, error) {
				name, _ := os.Hostname()
				return map[string]any{
					"version": kernel.Version, "startedAt": kernel.StartedAt, "hostname": name,
					"os": runtime.GOOS, "arch": runtime.GOARCH, "dataDir": agents.Root(),
				}, nil
			}})

		// ---- hosts (remote hosts arrive with Phase 3)
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/hosts", Devices: mobile,
			Handle: func(context.Context, *gateway.Request) (any, error) {
				name, _ := os.Hostname()
				return map[string]any{"hosts": []map[string]any{{
					"id": "local", "name": name, "online": true, "os": runtime.GOOS, "arch": runtime.GOARCH,
					"latencyMs": 0, "allowHost": true, "local": true,
				}}}, nil
			}})
		return nil
	},
})

func utf8ish(b []byte) bool {
	n := len(b)
	if n > 8000 {
		n = 8000
	}
	for _, c := range b[:n] {
		if c == 0 {
			return false
		}
	}
	return true
}
