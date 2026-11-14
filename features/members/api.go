package members

import (
	"context"

	"github.com/hedeqiang/crewbase/features/devices"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// Config of the plugin.
type Config struct {
	Dir string `json:"dir"`
}

// Plugin provides the members service and its API:
//
//	GET    /api/members                → {members: [{…, devices: n}], roles, approvable}
//	POST   /api/members      Input
//	PATCH  /api/members/{id} Input
//	DELETE /api/members/{id}           removes the member and their devices
//	GET    /api/me                     → who is behind this request
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/members",
	Inject:      []string{gateway.ServiceKey, devices.ServiceKey},
	Description: "Team members and roles: admin, member (approves what they are trusted with), viewer.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		ds := kernel.Use[*devices.Service](c, devices.ServiceKey)
		s, err := New(cfg.Dir, ds)
		if err != nil {
			return err
		}
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		emit := func(kind string, m Member, r *gateway.Request) {
			by := s.PersonOf(r.DeviceID).Name
			Changed.Emit(c.Host(), Event{Kind: kind, Member: m, By: by})
			g.Hub().Publish("members.changed", map[string]string{"kind": kind, "id": m.ID})
		}
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/members",
			Handle: func(context.Context, *gateway.Request) (any, error) {
				count := map[string]int{}
				for _, d := range ds.List() {
					count[d.Member]++
				}
				type row struct {
					Member
					Devices int `json:"devices"`
				}
				out := []row{}
				for _, m := range s.List() {
					out = append(out, row{Member: m, Devices: count[m.ID]})
				}
				return map[string]any{"members": out, "ownerDevices": count[""], "roles": Roles, "approvable": Approvable}, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/members",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var in Input
				if err := r.Decode(&in); err != nil {
					return nil, err
				}
				m, err := s.Add(in)
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				emit("added", m, r)
				return m, nil
			}})
		g.Handle(c, gateway.Route{Method: "PATCH", Path: "/api/members/{id}",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var in Input
				if err := r.Decode(&in); err != nil {
					return nil, err
				}
				// nobody changes their own role (an admin cannot lock out the owner or promote itself)
				if p := s.PersonOf(r.DeviceID); p.ID == r.Params["id"] && in.Role != nil {
					return nil, &gateway.Error{Status: 403, Code: "forbidden", Message: "you cannot change your own role"}
				}
				m, err := s.Update(r.Params["id"], in)
				if err == ErrNotFound {
					return nil, gateway.NotFound()
				}
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				emit("changed", m, r)
				return m, nil
			}})
		g.Handle(c, gateway.Route{Method: "DELETE", Path: "/api/members/{id}",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				m, revoked, err := s.Remove(r.Params["id"])
				if err == ErrNotFound {
					return nil, gateway.NotFound()
				}
				if err != nil {
					return nil, err
				}
				for _, d := range revoked {
					g.Kick(d.ID)
				}
				emit("removed", m, r)
				return map[string]int{"devices": len(revoked)}, nil
			}})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/me", Devices: []string{"mobile", "viewer", "im"},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				p := s.PersonOf(r.DeviceID)
				return map[string]any{"id": p.ID, "name": p.Name, "role": p.Role, "approve": p.Approve, "tier": r.Device}, nil
			}})
		return c.Provide(ServiceKey, s)
	},
})
