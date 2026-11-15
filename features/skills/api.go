package skills

import (
	"context"
	"errors"
	"sort"

	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// APIPlugin lists installed skills and removes them on the gateway, for the
// settings page (desktop only). Installing the built-in ones goes through
// the catalog (POST /api/catalog/{id}/install), which owns their text.
var APIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/skills-api",
	Inject:      []string{gateway.ServiceKey, ServiceKey},
	Description: "Remote methods for skills: list every installed skill, remove one.",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		s := kernel.Use[*Service](c, ServiceKey)
		Changed.Listen(c, func(struct{}) { g.Hub().Publish("skills.changed", struct{}{}) })
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/skills",
			Handle: func(context.Context, *gateway.Request) (any, error) {
				list := s.All()
				sort.SliceStable(list, func(i, j int) bool { return list[i].Name < list[j].Name })
				if list == nil {
					list = []Skill{}
				}
				return map[string]any{"skills": list}, nil
			}})
		// ?agent=<id> removes from that agent's workspace; without it, from
		// the shared skills directory.
		g.Handle(c, gateway.Route{Method: "DELETE", Path: "/api/skills/{name}",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				err := s.Remove(r.HTTP.URL.Query().Get("agent"), r.Params["name"])
				if errors.Is(err, ErrNotFound) {
					return nil, gateway.NotFound()
				}
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return nil, nil
			}})
		return nil
	},
})
