package channel

import (
	"context"
	"strings"

	"github.com/hedeqiang/crewbase/core/session"
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
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				l := []View{}
				type access interface {
					Session(deviceID, id string) bool
				}
				acc, limited := kernel.Lookup[access](c, "access")
				for _, v := range s.List() {
					if !limited || acc.Session(r.DeviceID, v.Meta.ID) {
						l = append(l, v)
					}
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
					// HandoverConfirm: handovers wait for a person (default:
					// they take effect at once)
					HandoverConfirm bool `json:"handoverConfirm"`
				}
				_ = r.Decode(&b)
				v, err := s.Create(b.Name, b.Members, b.Folder, r.Device)
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				if b.HandoverConfirm {
					if err := s.SetSettings(v.ID, Settings{HandoverConfirm: true}); err != nil {
						return nil, err
					}
					v, _ = s.Get(v.ID)
				}
				return v, nil
			}})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/channels/{id}", Devices: mobile,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) { return get(r) }})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/channels/{id}/messages", Devices: mobile,
			Validate: func(r *gateway.Request) error {
				var b struct {
					Text string           `json:"text"`
					Use  []session.Invoke `json:"use"`
				}
				if err := r.Decode(&b); err != nil {
					return err
				}
				if strings.TrimSpace(b.Text) == "" && len(b.Use) == 0 {
					return gateway.BadRequest("text is required")
				}
				return nil
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if _, err := get(r); err != nil {
					return nil, err
				}
				var b struct {
					Text  string           `json:"text"`
					From  string           `json:"from"`
					Reply *session.Quote   `json:"reply"`
					Use   []session.Invoke `json:"use"`
				}
				_ = r.Decode(&b)
				if q := b.Reply; q != nil {
					if q.Seq <= 0 {
						b.Reply = nil
					} else if r := []rune(strings.Join(strings.Fields(q.Text), " ")); len(r) > 200 {
						q.Text = string(r[:200]) + "…"
					}
				}
				// a team member's messages carry their name
				if who, ok := kernel.Lookup[interface{ SenderName(string) string }](c, "members"); ok && r.DeviceID != "" {
					b.From = who.SenderName(r.DeviceID)
				}
				use, err := session.CleanUse(b.Use)
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				res, err := s.PostReply(r.Params["id"], b.Text, b.From, b.Reply, use...)
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
		g.Handle(c, gateway.Route{Method: "PATCH", Path: "/api/channels/{id}",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if _, err := get(r); err != nil {
					return nil, err
				}
				var b struct {
					Title           *string `json:"title"`
					Folder          *string `json:"folder"`
					HandoverConfirm *bool   `json:"handoverConfirm"`
				}
				if err := r.Decode(&b); err != nil {
					return nil, err
				}
				if b.Title != nil || b.Folder != nil {
					if err := s.Rename(r.Params["id"], b.Title, b.Folder); err != nil {
						return nil, err
					}
				}
				if b.HandoverConfirm != nil {
					if err := s.SetSettings(r.Params["id"], Settings{HandoverConfirm: *b.HandoverConfirm}); err != nil {
						return nil, err
					}
				}
				return get(r)
			}})
		g.Handle(c, gateway.Route{Method: "DELETE", Path: "/api/channels/{id}",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if _, err := get(r); err != nil {
					return nil, err
				}
				if err := s.Delete(r.Params["id"], r.Device); err != nil {
					return nil, gateway.Conflict("%v", err)
				}
				return nil, nil
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
