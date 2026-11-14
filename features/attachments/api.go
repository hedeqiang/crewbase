package attachments

import (
	"context"
	"os"
	"path"
	"time"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// maxDownload caps files served from the workspace.
const maxDownload = 64 << 20

// APIPlugin exposes uploads and workspace file downloads:
//
//	POST /api/agents/{id}/uploads?name=<file name>   raw body → llm.Attachment
//	GET  /api/agents/{id}/raw?path=<workspace path>  the file (images inline, else as a download)
var APIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/attachments-api",
	Inject:      []string{gateway.ServiceKey, agent.ServiceKey},
	Description: "Upload conversation attachments into an agent workspace; download workspace files.",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		agents := kernel.Use[*agent.Registry](c, agent.ServiceKey)
		viewers := []string{"mobile"}
		getAgent := func(r *gateway.Request) (*agent.Agent, error) {
			a, ok := agents.Get(r.Params["id"])
			if !ok {
				return nil, gateway.NotFound()
			}
			return a, nil
		}
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/agents/{id}/uploads", Devices: viewers, RawBody: true, MaxBody: MaxFileBytes,
			Validate: func(r *gateway.Request) error {
				if r.HTTP.URL.Query().Get("name") == "" {
					return gateway.BadRequest("name is required")
				}
				if len(r.Body) == 0 {
					return gateway.BadRequest("the file is empty")
				}
				return nil
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				a, err := getAgent(r)
				if err != nil {
					return nil, err
				}
				at, err := Save(a.Workspace(), r.HTTP.URL.Query().Get("name"), r.Body, time.Now())
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return at, nil
			}})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/agents/{id}/raw", Devices: viewers,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				a, err := getAgent(r)
				if err != nil {
					return nil, err
				}
				rel := r.HTTP.URL.Query().Get("path")
				p, err := resolveIn(a.Workspace(), rel)
				if err != nil {
					return nil, gateway.NotFound()
				}
				info, err := os.Stat(p)
				if err != nil || info.IsDir() {
					return nil, gateway.NotFound()
				}
				if info.Size() > maxDownload {
					return nil, gateway.BadRequest("the file is too large to download here")
				}
				data, err := os.ReadFile(p)
				if err != nil {
					return nil, err
				}
				name := path.Base(rel)
				return &gateway.Blob{Name: name, Mime: MimeOf(name, data), Data: data}, nil
			}})
		return nil
	},
})
