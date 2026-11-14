package channel

import (
	"context"
	"strings"

	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// APIPlugin exposes channels on the gateway.
var APIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/channel-api",
	Inject:      []string{gateway.ServiceKey, ServiceKey},
	Description: "Remote methods for channels.",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		s := kernel.Use[*Service](c, ServiceKey)
		mobile := []string{"mobile", "im"}
		get := func(r *gateway.Request) (View, error) {
			v, err := s.Get(r.Params["id"])
			if err != nil {
				return v, gateway.NotFound()
			}
			return v, nil
		}
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/channels", Devices: mobile,
			Handle: func(context.Context, *gateway.Request) (any, error) {
				l := s.List()
				if l == nil {
					l = []View{}
				}
				return map[string]any{"channels": l}, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/channels",
			Validate: func(r *gateway.Request) error {
				var b struct {
					Name    string   `json:"name"`
					Members []string `json:"members"`
				}
				if err := r.Decode(&b); err != nil {
					return err
				}
				if strings.TrimSpace(b.Name) == "" || len(b.Members) == 0 {
					return gateway.BadRequest("name and at least one member are required")
				}
				return nil
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b struct {
					Name    string   `json:"name"`
					Members []string `json:"members"`
					Folder  string   `json:"folder"`
				}
				_ = r.Decode(&b)
				v, err := s.Create(b.Name, b.Members, b.Folder, r.Device)
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return v, nil
			}})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/channels/{id}", Devices: mobile,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) { return get(r) }})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/channels/{id}/messages", Devices: mobile,
			Validate: func(r *gateway.Request) error {
				var b struct {
					Text string `json:"text"`
				}
				if err := r.Decode(&b); err != nil {
					return err
				}
				if strings.TrimSpace(b.Text) == "" {
					return gateway.BadRequest("text is required")
				}
				return nil
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if _, err := get(r); err != nil {
					return nil, err
				}
				var b struct {
					Text string `json:"text"`
					From string `json:"from"`
				}
				_ = r.Decode(&b)
				res, err := s.Post(r.Params["id"], b.Text, b.From)
				if err != nil {
					return nil, gateway.Conflict("%v", err)
				}
				return res, nil
			}})
		for _, act := range []string{"stop", "resume", "archive"} {
			act := act
			g.Handle(c, gateway.Route{Method: "POST", Path: "/api/channels/{id}/" + act, Devices: mobile,
				Handle: func(_ context.Context, r *gateway.Request) (any, error) {
					if _, err := get(r); err != nil {
						return nil, err
					}
					var err error
					switch act {
					case "stop":
						err = s.Stop(r.Params["id"], r.Device)
					case "resume":
						err = s.Resume(r.Params["id"], r.Device)
					case "archive":
						err = s.Archive(r.Params["id"], r.Device)
					}
					if err != nil {
						return nil, gateway.Conflict("%v", err)
					}
					return get(r)
				}})
		}
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/channels/{id}/reassign",
			Validate: func(r *gateway.Request) error {
				var b struct {
					To string `json:"to"`
				}
				if err := r.Decode(&b); err != nil {
					return err
				}
				if b.To == "" {
					return gateway.BadRequest("to is required")
				}
				return nil
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if _, err := get(r); err != nil {
					return nil, err
				}
				var b struct {
					To   string `json:"to"`
					Note string `json:"note"`
				}
				_ = r.Decode(&b)
				if err := s.Reassign(r.Params["id"], b.To, r.Device, b.Note); err != nil {
					return nil, gateway.Conflict("%v", err)
				}
				return get(r)
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/channels/{id}/members",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if _, err := get(r); err != nil {
					return nil, err
				}
				var b struct {
					Agent string `json:"agent"`
				}
				if err := r.Decode(&b); err != nil {
					return nil, err
				}
				if err := s.AddMember(r.Params["id"], b.Agent); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return get(r)
			}})
		OwnerChanged.Listen(c, func(e OwnerEvent) { g.Hub().Publish("channel.owner", e) })
		return nil
	},
})
