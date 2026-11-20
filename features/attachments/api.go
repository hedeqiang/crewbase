package attachments

import (
	"context"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// maxDownload caps files served from the workspace; videos stream from
// disk (with Range) up to maxVideo.
const (
	maxDownload = 64 << 20
	maxVideo    = 1 << 30
)

// APIPlugin exposes uploads and workspace file downloads:
//
//	POST /api/agents/{id}/uploads?name=<file name>   raw body → llm.Attachment
//	GET  /api/agents/{id}/raw?path=<workspace path>  the file (images and MP4 / WebM video inline, else as a download)
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
				Uploaded.Emit(c, UploadedFile{Agent: a.ID(), Team: a.Spec().TeamID(), Workspace: a.Workspace(), Attachment: at})
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
				name := path.Base(rel)
				if ext := strings.ToLower(path.Ext(name)); ext == ".mp4" || ext == ".webm" {
					if info.Size() > maxVideo {
						return nil, gateway.BadRequest("the file is too large to download here")
					}
					f, err := os.Open(p)
					if err != nil {
						return nil, err
					}
					head := make([]byte, 512)
					n, _ := f.Read(head)
					if _, err := f.Seek(0, io.SeekStart); err != nil {
						f.Close()
						return nil, err
					}
					// the bytes decide: a renamed HTML file is a download
					return &gateway.Blob{Name: name, Mime: MimeOf(name, head[:n]), Content: f, ModTime: info.ModTime()}, nil
				}
				if info.Size() > maxDownload {
					return nil, gateway.BadRequest("the file is too large to download here")
				}
				data, err := os.ReadFile(p)
				if err != nil {
					return nil, err
				}
				return &gateway.Blob{Name: name, Mime: MimeOf(name, data), Data: data}, nil
			}})
		return nil
	},
})
