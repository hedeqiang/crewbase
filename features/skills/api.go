package skills

import (
	"context"
	"errors"
	"sort"

	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// newSkill is a skill written in the app: POST /api/skills.
type newSkill struct {
	Name    string `json:"name"`
	Content string `json:"content"`         // the SKILL.md
	Agent   string `json:"agent,omitempty"` // its workspace; "" = every agent
}

// APIPlugin lists installed skills, saves one written in the app and
// removes them on the gateway, for the settings page (desktop only). Installing the built-in ones goes through
// the catalog (POST /api/catalog/{id}/install), which owns their text.
var APIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/skills-api",
	Inject:      []string{gateway.ServiceKey, ServiceKey},
	Description: "Remote methods for skills: list every installed skill, save one written in the app, remove one.",
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
		// the skills one agent sees (its workspace first): the composer's / menu
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/agents/{id}/skills", Devices: []string{"mobile"},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				list := s.List(r.Params["id"])
				out := make([]map[string]string, 0, len(list))
				for _, sk := range list {
					out = append(out, map[string]string{"name": sk.Name, "description": sk.Description, "source": sk.Source})
				}
				return map[string]any{"skills": out}, nil
			}})
		// a skill written in the app (做成技能): its SKILL.md, for one agent
		// (its workspace) or every agent (the shared directory); never
		// overwrites one of the same name
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/skills",
			Validate: func(r *gateway.Request) error {
				var b newSkill
				if err := r.Decode(&b); err != nil {
					return err
				}
				if !folderRe.MatchString(b.Name) {
					return gateway.BadRequest("the name takes lowercase letters, digits and dashes")
				}
				if len(b.Content) == 0 || len(b.Content) > 256<<10 {
					return gateway.BadRequest("SKILL.md must be between 1 byte and 256 KB")
				}
				return nil
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b newSkill
				_ = r.Decode(&b)
				sk, err := s.Install(b.Agent, b.Name, map[string][]byte{"SKILL.md": []byte(b.Content)})
				if errors.Is(err, ErrExists) {
					return nil, gateway.Conflict("a skill named %s already exists", b.Name)
				}
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return sk, nil
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
