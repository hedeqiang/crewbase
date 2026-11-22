package library

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/features/attachments"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// APIPlugin is the 文档库 page's API. Every route takes ?team= (the teams
// guard checks it; items of other teams are not found):
//
//	GET    /api/library?q=&folder=&source=&tag=&limit=&offset=  {items, stats (first page)}
//	POST   /api/library?name=&folder=                          raw body → item (the person adds a file)
//	GET    /api/library/{id}                                   {item, versions}
//	GET    /api/library/{id}/raw?version=                      the file (or an earlier version)
//	GET    /api/library/{id}/text                              {text} extracted from it
//	PATCH  /api/library/{id}                                   {title?, tags?, private?}
//	DELETE /api/library/{id}
//	POST   /api/library/{id}/restore                           {version}
//	POST   /api/library/{id}/attach                            {agent} → an attachment in the agent's workspace
//	POST   /api/library/publish                                {agent, path, session?, seq?, title?} → link (工作区 → 文档库)
//	GET    /api/library/links?agent=                           the agent's published files and how they stand
//	GET    /api/library/{id}/link                              {link} (null: not from a workspace file)
//	POST   /api/library/{id}/sync                              the workspace file → a new version of the document
//	POST   /api/library/{id}/pull                              the document → the workspace file
//	DELETE /api/library/{id}/link                              forget the tie (both stay)
var APIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/library-api",
	Inject:      []string{gateway.ServiceKey, ServiceKey, agent.ServiceKey},
	Description: "The 文档库 page's API: browse, search, preview, version history and attaching documents to a conversation.",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		s := kernel.Use[*Service](c, ServiceKey)
		agents := kernel.Use[*agent.Registry](c, agent.ServiceKey)
		viewers := []string{"mobile"}
		// people see the whole team's library, private items included
		viewer := func(r *gateway.Request) Viewer { return Viewer{Team: r.HTTP.URL.Query().Get("team")} }
		id := func(r *gateway.Request) (int64, error) {
			n, err := strconv.ParseInt(r.Params["id"], 10, 64)
			if err != nil {
				return 0, gateway.NotFound()
			}
			return n, nil
		}
		item := func(r *gateway.Request) (Item, error) {
			n, err := id(r)
			if err != nil {
				return Item{}, err
			}
			it, err := s.Get(viewer(r), n)
			if err != nil {
				return Item{}, notFound(err)
			}
			return it, nil
		}
		by := func(r *gateway.Request) string {
			if r.DeviceID == "" {
				return ""
			}
			return r.Device
		}

		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/library", Devices: viewers,
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				q := r.HTTP.URL.Query()
				v := viewer(r)
				limit, _ := strconv.Atoi(q.Get("limit"))
				offset, _ := strconv.Atoi(q.Get("offset"))
				out := map[string]any{}
				if text := strings.TrimSpace(q.Get("q")); text != "" {
					hits, err := s.Search(ctx, v, text, max(limit, 100))
					// nothing for the whole: each word on its own (流失 churn, …)
					if err == nil && len(hits) == 0 && len(strings.Fields(text)) > 1 {
						hits, err = s.SearchMany(ctx, v, strings.Fields(text), max(limit, 100))
					}
					if err != nil {
						return nil, err
					}
					keep := hits[:0]
					for _, h := range hits {
						if (q.Get("folder") == "" || h.Folder == q.Get("folder")) && (q.Get("source") == "" || h.Source == q.Get("source")) &&
							(q.Get("tag") == "" || slices.Contains(h.Tags, q.Get("tag"))) {
							keep = append(keep, h)
						}
					}
					out["items"] = keep
				} else {
					items, err := s.List(v, ListOptions{Folder: q.Get("folder"), Source: q.Get("source"), Tag: q.Get("tag"), Limit: limit, Offset: offset})
					if err != nil {
						return nil, err
					}
					hits := make([]Hit, len(items))
					for i, it := range items {
						hits[i] = Hit{Item: it}
					}
					out["items"] = hits
				}
				if offset == 0 {
					st, err := s.Stats(v)
					if err != nil {
						return nil, err
					}
					out["stats"] = st
				}
				return out, nil
			}})

		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/library/settings", Devices: viewers,
			Handle: func(context.Context, *gateway.Request) (any, error) { return s.Settings(), nil }})
		g.Handle(c, gateway.Route{Method: "PUT", Path: "/api/library/settings",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b Settings
				if err := r.Decode(&b); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				if err := s.SetSettings(b); err != nil {
					return nil, err
				}
				return s.Settings(), nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/library", Devices: viewers, RawBody: true, MaxBody: MaxItemBytes,
			Validate: func(r *gateway.Request) error {
				if strings.TrimSpace(r.HTTP.URL.Query().Get("name")) == "" {
					return gateway.BadRequest("name is required")
				}
				if len(r.Body) == 0 {
					return gateway.BadRequest("the file is empty")
				}
				return nil
			},
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				q := r.HTTP.URL.Query()
				it, err := s.Add(ctx, AddRequest{Team: q.Get("team"), Folder: q.Get("folder"), Name: q.Get("name"), Data: r.Body, Source: SourcePerson})
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return it, nil
			}})

		// before /api/library/{id}: /api/library/links and /publish are routes of their own
		links(c, g, s, agents, viewers, viewer, id, by)

		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/library/{id}", Devices: viewers,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				it, err := item(r)
				if err != nil {
					return nil, err
				}
				vs, err := s.Versions(viewer(r), it.ID)
				if err != nil {
					return nil, err
				}
				return map[string]any{"item": it, "versions": vs}, nil
			}})

		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/library/{id}/raw", Devices: viewers,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				it, err := item(r)
				if err != nil {
					return nil, err
				}
				var data []byte
				if vs := r.HTTP.URL.Query().Get("version"); vs != "" {
					vid, err := strconv.ParseInt(vs, 10, 64)
					if err != nil {
						return nil, gateway.NotFound()
					}
					if data, err = s.VersionData(viewer(r), it.ID, vid); err != nil {
						return nil, notFound(err)
					}
					return &gateway.Blob{Name: it.Name, Mime: attachments.MimeOf(it.Name, data), Data: data}, nil
				}
				f, err := os.Open(s.File(it))
				if err != nil {
					return nil, notFound(err)
				}
				head := make([]byte, 512)
				n, _ := f.ReadAt(head, 0)
				// the bytes decide: a renamed HTML file is a download
				return &gateway.Blob{Name: it.Name, Mime: attachments.MimeOf(it.Name, head[:n]), Content: f, ModTime: it.Updated}, nil
			}})

		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/library/{id}/text", Devices: viewers,
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				it, err := item(r)
				if err != nil {
					return nil, err
				}
				s.drain(ctx, 5*time.Second)
				return map[string]any{"text": s.Body(it.ID)}, nil
			}})

		g.Handle(c, gateway.Route{Method: "PATCH", Path: "/api/library/{id}", Devices: viewers,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				n, err := id(r)
				if err != nil {
					return nil, err
				}
				var b struct {
					Title   *string   `json:"title"`
					Tags    *[]string `json:"tags"`
					Private *bool     `json:"private"`
				}
				if err := r.Decode(&b); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				it, err := s.Update(viewer(r), n, b.Title, b.Tags, b.Private)
				if err != nil {
					return nil, notFound(err)
				}
				return it, nil
			}})

		g.Handle(c, gateway.Route{Method: "DELETE", Path: "/api/library/{id}", Devices: viewers,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				n, err := id(r)
				if err != nil {
					return nil, err
				}
				if err := s.Delete(viewer(r), n); err != nil {
					return nil, notFound(err)
				}
				return map[string]any{"ok": true}, nil
			}})

		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/library/{id}/restore", Devices: viewers,
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				n, err := id(r)
				if err != nil {
					return nil, err
				}
				var b struct {
					Version int64 `json:"version"`
				}
				if err := r.Decode(&b); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				it, err := s.Restore(ctx, viewer(r), n, b.Version, by(r))
				if err != nil {
					return nil, notFound(err)
				}
				return it, nil
			}})

		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/library/{id}/attach", Devices: viewers,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				it, err := item(r)
				if err != nil {
					return nil, err
				}
				var b struct {
					Agent string `json:"agent"`
				}
				if err := r.Decode(&b); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				a, ok := agents.Get(b.Agent)
				// only an agent of the document's team
				if !ok || a.Spec().TeamID() != it.Team {
					return nil, gateway.NotFound()
				}
				data, err := os.ReadFile(s.File(it))
				if err != nil {
					return nil, notFound(err)
				}
				at, err := attachments.Save(a.Workspace(), it.Name, data, time.Now())
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return at, nil
			}})
		return nil
	},
})

// links are the routes tying workspace files to documents. Each works
// with an agent's workspace, so the caller must be able to work with that
// agent (the teams access), and the agent must be of the library's team.
func links(c *kernel.Context, g *gateway.Gateway, s *Service, agents *agent.Registry, viewers []string,
	viewer func(*gateway.Request) Viewer, id func(*gateway.Request) (int64, error), by func(*gateway.Request) string) {
	type access interface {
		Agents(deviceID string, ids []string, act bool) bool
		Session(deviceID, id string) bool
	}
	reach := func(r *gateway.Request, name string, act bool) (*agent.Agent, bool) {
		a, ok := agents.Get(name)
		if !ok || a.Spec().TeamID() != agent.TeamOf(viewer(r).Team) {
			return nil, false
		}
		if acc, ok := kernel.Lookup[access](c, "access"); ok && !acc.Agents(r.DeviceID, []string{name}, act) {
			return nil, false
		}
		return a, true
	}
	ws := func(r *gateway.Request, act bool) func(string) (string, bool) {
		return func(name string) (string, bool) {
			a, ok := reach(r, name, act)
			if !ok {
				return "", false
			}
			return a.Workspace(), true
		}
	}
	// the link of {id}, when the caller may reach its agent
	linked := func(r *gateway.Request, act bool) (int64, error) {
		n, err := id(r)
		if err != nil {
			return 0, err
		}
		l, err := s.LinkOf(viewer(r), n, func(string) (string, bool) { return "", false })
		if err != nil {
			return 0, notFound(err)
		}
		if _, ok := reach(r, l.Agent, act); !ok {
			return 0, gateway.NotFound()
		}
		return n, nil
	}

	g.Handle(c, gateway.Route{Method: "POST", Path: "/api/library/publish", Devices: viewers,
		Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
			var b struct {
				Agent   string `json:"agent"`
				Path    string `json:"path"`
				Session string `json:"session"`
				Seq     int64  `json:"seq"`
				Title   string `json:"title"`
			}
			if err := r.Decode(&b); err != nil {
				return nil, gateway.BadRequest("%v", err)
			}
			a, ok := reach(r, b.Agent, true)
			if !ok {
				return nil, gateway.NotFound()
			}
			if b.Session != "" {
				// a conversation the agent is in, that the caller sees (a link back to it)
				sess, err := agents.Sessions().Get(b.Session)
				acc, guarded := kernel.Lookup[access](c, "access")
				if err != nil || !slices.Contains(sess.Meta().Agents, a.ID()) || guarded && !acc.Session(r.DeviceID, b.Session) {
					b.Session, b.Seq = "", 0
				}
			}
			lv, err := s.Publish(ctx, PublishRequest{Team: a.Spec().TeamID(), Agent: a.ID(), Dir: a.Workspace(), Path: b.Path,
				Session: b.Session, Seq: b.Seq, Title: b.Title, By: by(r)})
			if err != nil {
				return nil, notFound(err)
			}
			it, _ := s.Get(viewer(r), lv.Item)
			return map[string]any{"link": lv, "item": it}, nil
		}})

	g.Handle(c, gateway.Route{Method: "GET", Path: "/api/library/links", Devices: viewers,
		Handle: func(_ context.Context, r *gateway.Request) (any, error) {
			a, ok := reach(r, r.HTTP.URL.Query().Get("agent"), false)
			if !ok {
				return nil, gateway.NotFound()
			}
			ls, err := s.AgentLinks(viewer(r), a.ID(), a.Workspace())
			if err != nil {
				return nil, err
			}
			return map[string]any{"links": ls}, nil
		}})

	g.Handle(c, gateway.Route{Method: "GET", Path: "/api/library/{id}/link", Devices: viewers,
		Handle: func(_ context.Context, r *gateway.Request) (any, error) {
			n, err := id(r)
			if err != nil {
				return nil, err
			}
			lv, err := s.LinkOf(viewer(r), n, ws(r, false))
			if errors.Is(err, ErrNoLink) {
				return map[string]any{"link": nil}, nil
			}
			if err != nil {
				return nil, notFound(err)
			}
			if _, ok := reach(r, lv.Agent, false); !ok {
				// another member's agent: where it came from, not its state
				lv.State = ""
			}
			return map[string]any{"link": lv}, nil
		}})

	g.Handle(c, gateway.Route{Method: "POST", Path: "/api/library/{id}/sync", Devices: viewers,
		Handle: func(_ context.Context, r *gateway.Request) (any, error) {
			n, err := linked(r, true)
			if err != nil {
				return nil, err
			}
			it, err := s.Push(viewer(r), n, ws(r, true), by(r))
			if err != nil {
				return nil, notFound(err)
			}
			lv, _ := s.LinkOf(viewer(r), n, ws(r, false))
			return map[string]any{"link": lv, "item": it}, nil
		}})

	g.Handle(c, gateway.Route{Method: "POST", Path: "/api/library/{id}/pull", Devices: viewers,
		Handle: func(_ context.Context, r *gateway.Request) (any, error) {
			n, err := linked(r, true)
			if err != nil {
				return nil, err
			}
			if _, err := s.Pull(viewer(r), n, ws(r, true)); err != nil {
				return nil, notFound(err)
			}
			lv, _ := s.LinkOf(viewer(r), n, ws(r, false))
			return map[string]any{"link": lv}, nil
		}})

	g.Handle(c, gateway.Route{Method: "DELETE", Path: "/api/library/{id}/link", Devices: viewers,
		Handle: func(_ context.Context, r *gateway.Request) (any, error) {
			n, err := id(r)
			if err != nil {
				return nil, err
			}
			if err := s.Unlink(viewer(r), n); err != nil {
				return nil, notFound(err)
			}
			return map[string]any{"ok": true}, nil
		}})
}

func notFound(err error) error {
	var ge *gateway.Error
	if errors.As(err, &ge) {
		return err
	}
	if errors.Is(err, ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return gateway.NotFound()
	}
	return gateway.BadRequest("%v", err)
}
