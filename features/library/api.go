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
