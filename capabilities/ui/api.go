package ui

import (
	"context"
	"errors"
	"time"

	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// APIPlugin exposes the slots to the desktop UI.
var APIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/ui-api",
	Inject:      []string{gateway.ServiceKey, ServiceKey},
	Description: "Remote methods for interface slots.",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		s := kernel.Use[*Service](c, ServiceKey)
		mobile := []string{"mobile"}
		Changed.Listen(c, func(struct{}) { g.Hub().Publish("ui.changed", struct{}{}) })
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/ui", Devices: mobile,
			Handle: func(context.Context, *gateway.Request) (any, error) {
				return map[string]any{"slots": Slots, "contributions": s.List()}, nil
			}})
		type ref struct {
			Slot    string `json:"slot"`
			Key     string `json:"key"`
			Session string `json:"session"`
		}
		fail := func(err error) error {
			if errors.Is(err, ErrNotFound) {
				return gateway.NotFound()
			}
			// a plugin's own error message, shown on the card
			return gateway.BadRequest("%s", clip(err.Error(), 300))
		}
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/ui/panel", Devices: mobile,
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				var b ref
				if err := r.Decode(&b); err != nil {
					return nil, err
				}
				if b.Slot != SlotSidebarTab && b.Slot != SlotSettingsCard {
					return nil, gateway.BadRequest("slot %q has no panel", b.Slot)
				}
				cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				p, err := s.Panel(cctx, b.Slot, b.Key, b.Session)
				if err != nil {
					return nil, fail(err)
				}
				return p, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/ui/run",
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				var b ref
				if err := r.Decode(&b); err != nil {
					return nil, err
				}
				if b.Slot != SlotPaletteCommand && b.Slot != SlotComposerAction {
					return nil, gateway.BadRequest("slot %q has no command", b.Slot)
				}
				cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
				defer cancel()
				res, err := s.Run(cctx, b.Slot, b.Key, b.Session)
				if err != nil {
					return nil, fail(err)
				}
				return res, nil
			}})
		return nil
	},
})
