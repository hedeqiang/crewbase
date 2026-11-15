// Package console serves the desktop UI's remaining remote methods: session
// summaries, context breakdown, compact now / new conversation, workspace
// files (INSTRUCTIONS.md, MEMORY.md…), remembered approval choices and
// history, router controls, defaults for new agents, and hosts.
package console

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	"github.com/hedeqiang/crewbase/providers/llm/acp"
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
			if strings.TrimSpace(text) == "" && len(m.Attachments) > 0 {
				names := make([]string, len(m.Attachments))
				for i, at := range m.Attachments {
					names[i] = at.Name
				}
				text = "📎 " + strings.Join(names, "、")
			}
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
	// Notify holds the event × channel choices and quiet hours.
	Notify *notify.Prefs `json:"notify,omitempty"`
	// Folders created from the list (＋ › 新建文件夹); a folder also exists
	// while any agent or channel is in it, so this mainly keeps empty ones.
	Folders []string `json:"folders,omitempty"`
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
			// UpdateCheck: daily check for a newer release (default on)
			UpdateCheck *bool `json:"updateCheck"`
		}](c)
		if err != nil {
			return err
		}
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		// a subscription CLI changed workspace files on its own: views refresh
		acp.FilesChanged.Listen(c, func(ch acp.FilesChange) { g.Hub().Publish("files.changed", ch) })
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
		var smu sync.Mutex // serialises read-modify-write of settings.json
		save := func(st Settings) error {
			if settingsPath == "" {
				return nil
			}
			data, _ := json.MarshalIndent(st, "", "  ")
			tmp := settingsPath + ".tmp"
			if err := os.WriteFile(tmp, data, 0o600); err != nil {
				return err
			}
			return os.Rename(tmp, settingsPath)
		}
		apply := func(st Settings) {
			if n, ok := kernel.Lookup[*notify.Service](c, notify.ServiceKey); ok {
				var until time.Time
				if st.DndUntil != "" {
					until, _ = time.Parse(time.RFC3339, st.DndUntil)
				}
				n.SetDoNotDisturb(until)
				if st.Notify != nil {
					n.SetPrefs(*st.Notify)
				} else {
					n.SetPrefs(notify.Prefs{})
				}
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
				if b.Notify != nil {
					if err := b.Notify.Validate(); err != nil {
						return gateway.BadRequest("%v", err)
					}
				}
				return nil
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				smu.Lock()
				defer smu.Unlock()
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
				if _, ok := raw["notify"]; ok {
					cur.Notify = b.Notify
				}
				if err := save(cur); err != nil {
					return nil, err
				}
				apply(cur)
				return cur, nil
			}})

		// ---- notifications (settings › 通知)
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/notify",
			Handle: func(context.Context, *gateway.Request) (any, error) {
				n, ok := kernel.Lookup[*notify.Service](c, notify.ServiceKey)
				if !ok {
					return nil, gateway.NotFound()
				}
				chans := n.Channels()
				matrix := map[string]map[string]bool{}
				for _, k := range notify.Kinds {
					matrix[k] = map[string]bool{}
					for _, ch := range chans {
						matrix[k][ch] = n.Enabled(k, ch)
					}
				}
				var dnd string
				if t := n.DoNotDisturb(); !t.IsZero() {
					dnd = t.Format(time.RFC3339)
				}
				return map[string]any{
					"kinds": notify.Kinds, "channels": chans, "matrix": matrix,
					"quiet": n.Prefs().Quiet, "quietNow": n.Quiet(), "dndUntil": dnd,
				}, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/notify/test",
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				var b struct {
					Channel string `json:"channel"`
				}
				if err := r.Decode(&b); err != nil {
					return nil, err
				}
				n, ok := kernel.Lookup[*notify.Service](c, notify.ServiceKey)
				if !ok {
					return nil, gateway.NotFound()
				}
				if err := n.SendTo(ctx, b.Channel, notify.Notification{Kind: notify.KindBrief, Title: "Crewbase 测试通知", Body: "如果你看到这条消息，说明这个渠道可以收到通知。"}); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return map[string]any{"ok": true}, nil
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
					if m.Kind == "channel" {
						if e, ok := ss.Last("channel/state"); ok {
							var st map[string]string
							if e.Decode(&st) == nil && st["state"] == "deleted" {
								continue
							}
						}
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

		registerDirs(c, g)

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
					if d.Name() == "agent.json" || d.Name() == "browser" || d.Name() == ".remote" {
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
				return map[string]any{"routes": routes, "tasks": []string{router.TaskTitle, router.TaskRoutine, router.TaskSummarize, router.TaskCode, router.TaskLongDoc}}, nil
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

		// ---- folders: a folder is the `folder` of agents and sessions.
		// Renaming moves every member; deleting dissolves the folder (its
		// agents and channels move to the top level, nothing is deleted).
		moveFolder := func(from, to string) int {
			n := 0
			for _, a := range agents.List() {
				if a.Spec().Folder == from {
					_ = a.Update(func(s *agent.Spec) { s.Folder = to })
					n++
				}
			}
			for _, m := range sessions.List() {
				if m.Folder != from || m.Kind == "agent" {
					continue
				}
				if ss, err := sessions.Get(m.ID); err == nil {
					_ = ss.UpdateMeta(func(mm *session.Meta) { mm.Folder = to })
					n++
				}
			}
			return n
		}
		folderName := func(raw string) (string, error) {
			n := strings.TrimSpace(raw)
			if n == "" || len([]rune(n)) > 60 || strings.ContainsAny(n, "/\\") {
				return "", gateway.BadRequest("a folder name is required (at most 60 characters, no slashes)")
			}
			return n, nil
		}
		// setSaved edits the saved folder list under smu.
		setSaved := func(f func([]string) []string) error {
			smu.Lock()
			defer smu.Unlock()
			cur := load()
			cur.Folders = f(cur.Folders)
			return save(cur)
		}
		hasSaved := func(name string) bool { return slices.Contains(load().Folders, name) }
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/folders", Devices: mobile,
			Handle: func(context.Context, *gateway.Request) (any, error) {
				type folder struct {
					Name  string `json:"name"`
					Count int    `json:"count"`
				}
				counts := map[string]int{}
				for _, n := range load().Folders {
					counts[n] += 0
				}
				for _, a := range agents.List() {
					if f := a.Spec().Folder; f != "" {
						counts[f]++
					}
				}
				for _, m := range sessions.List() {
					if m.Folder != "" && m.Kind != "agent" && !m.Deleted {
						counts[m.Folder]++
					}
				}
				out := []folder{}
				for n, k := range counts {
					out = append(out, folder{n, k})
				}
				sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
				return map[string]any{"folders": out}, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/folders",
			Validate: func(r *gateway.Request) error {
				var b struct {
					Name string `json:"name"`
				}
				if err := r.Decode(&b); err != nil {
					return err
				}
				_, err := folderName(b.Name)
				return err
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b struct {
					Name string `json:"name"`
				}
				_ = r.Decode(&b)
				name, _ := folderName(b.Name)
				err := setSaved(func(fs []string) []string {
					if slices.Contains(fs, name) {
						return fs
					}
					return append(fs, name)
				})
				if err != nil {
					return nil, err
				}
				return map[string]any{"name": name}, nil
			}})
		g.Handle(c, gateway.Route{Method: "PATCH", Path: "/api/folders/{name}",
			Validate: func(r *gateway.Request) error {
				var b struct {
					Name string `json:"name"`
				}
				if err := r.Decode(&b); err != nil {
					return err
				}
				_, err := folderName(b.Name)
				return err
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b struct {
					Name string `json:"name"`
				}
				_ = r.Decode(&b)
				from := r.Params["name"]
				to, _ := folderName(b.Name)
				saved := hasSaved(from)
				n := moveFolder(from, to)
				if n == 0 && !saved {
					return nil, gateway.NotFound()
				}
				if err := setSaved(func(fs []string) []string {
					out := slices.DeleteFunc(slices.Clone(fs), func(x string) bool { return x == from || x == to })
					if saved {
						out = append(out, to)
					}
					return out
				}); err != nil {
					return nil, err
				}
				return map[string]any{"moved": n}, nil
			}})
		g.Handle(c, gateway.Route{Method: "DELETE", Path: "/api/folders/{name}",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				name := r.Params["name"]
				saved := hasSaved(name)
				n := moveFolder(name, "")
				if n == 0 && !saved {
					return nil, gateway.NotFound()
				}
				if saved {
					if err := setSaved(func(fs []string) []string {
						return slices.DeleteFunc(slices.Clone(fs), func(x string) bool { return x == name })
					}); err != nil {
						return nil, err
					}
				}
				return map[string]any{"moved": n}, nil
			}})

		// ---- updates (crewd in a browser / server; the desktop app has its own)
		up := &updates{enabled: cfg.UpdateCheck == nil || *cfg.UpdateCheck, client: &http.Client{}}
		c.Go(up.run)
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/updates", Devices: mobile,
			Handle: func(context.Context, *gateway.Request) (any, error) { return up.get(), nil }})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/updates/check",
			Handle: func(ctx context.Context, _ *gateway.Request) (any, error) { return up.check(ctx), nil }})

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
