// Package addonupdates gathers what can be updated among the add-ons —
// marketplace plugins, skills installed from GitHub, and apps Crewbase
// runs (Open Agent Docs) — for the settings page's reminders. It checks at
// start and every 12 hours, keeps the result, and publishes "addons.updates"
// when it changes. MCP connectors are not listed: npx …@latest servers
// update themselves on start and remote ones are kept current by their
// owners.
//
//	GET  /api/addons/updates           {items, checked, error?}
//	POST /api/addons/updates/check     check now
//	POST /api/addons/update            {kind, id, agent?} install one
package addonupdates

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/features/agentdocs"
	"github.com/hedeqiang/crewbase/features/catalog"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/features/plugins"
	"github.com/hedeqiang/crewbase/kernel"
)

// Kinds of Item.
const (
	KindPlugin = "plugin"
	KindSkill  = "skill"
	KindApp    = "app"
)

// Item is one available update.
type Item struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`              // plugin name, skill folder, app id
	Agent string `json:"agent,omitempty"` // a skill of one agent
	Name  string `json:"name"`
	Desc  string `json:"desc,omitempty"`
	// Current / Latest: versions (plugins only; apps and skills have none to show).
	Current string `json:"current,omitempty"`
	Latest  string `json:"latest,omitempty"`
	Notes   string `json:"notes,omitempty"`
	Source  string `json:"source,omitempty"`
	// NewPermissions: the update asks for more; it waits for approval.
	NewPermissions []string `json:"newPermissions,omitempty"`
}

// State is the API view.
type State struct {
	Items   []Item    `json:"items"`
	Checked time.Time `json:"checked,omitzero"`
	Error   string    `json:"error,omitempty"`
}

// Service is the addonupdates service.
type Service struct {
	c   *kernel.Context
	hub interface{ Publish(string, any) }

	mu    sync.Mutex
	state State
	busy  sync.Mutex // one check or update at a time
}

const ServiceKey = "addonupdates"

func (s *Service) plugins() *plugins.Manager {
	v, _ := kernel.Lookup[*plugins.Manager](s.c, plugins.ServiceKey)
	return v
}

func (s *Service) catalog() *catalog.Service {
	v, _ := kernel.Lookup[*catalog.Service](s.c, catalog.ServiceKey)
	return v
}

func (s *Service) docs() *agentdocs.Service {
	v, _ := kernel.Lookup[*agentdocs.Service](s.c, agentdocs.ServiceKey)
	return v
}

// State is the last check.
func (s *Service) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state
	st.Items = append([]Item{}, st.Items...)
	return st
}

// Check looks for updates of every kind now.
func (s *Service) Check(ctx context.Context) State {
	s.busy.Lock()
	defer s.busy.Unlock()
	var items []Item
	var errs []string
	if m := s.plugins(); m != nil {
		us, err := m.Updates(ctx)
		if err != nil {
			errs = append(errs, err.Error())
		}
		for _, u := range us {
			items = append(items, Item{Kind: KindPlugin, ID: u.Name, Name: u.Name, Desc: u.Description, Current: u.Current, Latest: u.Latest, NewPermissions: u.NewPermissions})
		}
	}
	if c := s.catalog(); c != nil {
		us, err := c.SkillUpdates(ctx)
		if err != nil {
			errs = append(errs, err.Error())
		}
		for _, u := range us {
			items = append(items, Item{Kind: KindSkill, ID: u.Name, Agent: u.Agent, Name: u.Title, Desc: u.Desc, Source: u.Source})
		}
	}
	if d := s.docs(); d != nil {
		st, err := d.CheckLocal(ctx)
		if err != nil {
			errs = append(errs, err.Error())
		}
		if st.Update != nil {
			items = append(items, Item{Kind: KindApp, ID: "open-agent-docs", Name: "Open Agent Docs", Notes: st.Update.Notes})
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return order(items[i].Kind) < order(items[j].Kind)
		}
		return items[i].Name < items[j].Name
	})
	st := State{Items: items, Checked: time.Now().UTC(), Error: strings.Join(errs, "; ")}
	if st.Items == nil {
		st.Items = []Item{}
	}
	s.mu.Lock()
	s.state = st
	s.mu.Unlock()
	s.publish()
	return s.State()
}

func order(kind string) int {
	switch kind {
	case KindApp:
		return 0
	case KindPlugin:
		return 1
	}
	return 2
}

func (s *Service) publish() {
	if s.hub != nil {
		st := s.State()
		s.hub.Publish("addons.updates", map[string]any{"count": len(st.Items)})
	}
}

// UpdateRequest names one update.
type UpdateRequest struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	Agent string `json:"agent,omitempty"`
}

// Update installs one update and drops it from the list.
func (s *Service) Update(ctx context.Context, r UpdateRequest) (any, error) {
	s.busy.Lock()
	var out any
	var err error
	switch r.Kind {
	case KindPlugin:
		if m := s.plugins(); m != nil {
			out, err = m.Update(ctx, r.ID)
		} else {
			err = errors.New("plugins are not available")
		}
	case KindSkill:
		if c := s.catalog(); c != nil {
			out, err = c.UpdateSkill(ctx, r.Agent, r.ID)
		} else {
			err = errors.New("skills are not available")
		}
	case KindApp:
		if d := s.docs(); d != nil && r.ID == "open-agent-docs" {
			out, err = d.UpdateLocal(ctx)
		} else {
			err = errors.New("no such app")
		}
	default:
		err = errors.New("kind must be plugin, skill or app")
	}
	s.busy.Unlock()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	keep := s.state.Items[:0]
	for _, it := range s.state.Items {
		if it.Kind != r.Kind || it.ID != r.ID || it.Agent != r.Agent {
			keep = append(keep, it)
		}
	}
	s.state.Items = keep
	s.mu.Unlock()
	s.publish()
	return out, nil
}

// Plugin is @crewbase/addon-updates.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/addon-updates",
	Inject:      []string{gateway.ServiceKey},
	Optional:    []string{plugins.ServiceKey, catalog.ServiceKey, agentdocs.ServiceKey},
	Description: "Update reminders for plugins, skills and apps.",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		s := &Service{c: c, hub: g.Hub(), state: State{Items: []Item{}}}
		c.Go(func(ctx context.Context) {
			// after start (the managed server is up by then), then twice a day
			wait := time.Minute
			for {
				select {
				case <-ctx.Done():
					return
				case <-time.After(wait):
				}
				cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
				s.Check(cctx)
				cancel()
				wait = 12 * time.Hour
			}
		})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/addons/updates",
			Handle: func(context.Context, *gateway.Request) (any, error) { return s.State(), nil }})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/addons/updates/check",
			Handle: func(ctx context.Context, _ *gateway.Request) (any, error) {
				ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
				defer cancel()
				return s.Check(ctx), nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/addons/update",
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				var b UpdateRequest
				if err := r.Decode(&b); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 6*time.Minute)
				defer cancel()
				out, err := s.Update(ctx, b)
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return map[string]any{"result": out, "state": s.State()}, nil
			}})
		return c.Provide(ServiceKey, s)
	},
})
