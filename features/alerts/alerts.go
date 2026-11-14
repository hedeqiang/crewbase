// Package alerts turns product events into notifications: approvals and
// takeovers (urgent), failed turns and channel handovers. Model switches
// never notify (quiet by default). Notifications are also pushed to the
// gateway's event stream for the desktop UI.
package alerts

import (
	"context"
	"fmt"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/capabilities/notify"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/features/channel"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// Plugin wires events to the notify service.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/alerts",
	Inject:      []string{notify.ServiceKey, agent.ServiceKey},
	Description: "Approval / failure / handover notifications.",
	Apply: func(c *kernel.Context) error {
		n := kernel.Use[*notify.Service](c, notify.ServiceKey)
		agents := kernel.Use[*agent.Registry](c, agent.ServiceKey)
		name := func(id string) string {
			if a, ok := agents.Get(id); ok {
				return a.Spec().Name
			}
			return id
		}
		send := func(x notify.Notification) {
			go n.Send(context.Background(), x)
		}
		approval.Asked.Listen(c, func(r approval.Request) {
			send(notify.Notification{Kind: notify.KindNeedsYou, Urgent: true, Agent: r.Agent, Session: r.Session,
				Title: name(r.Agent) + " 等你批准", Body: r.Summary})
		})
		agentloop.TurnEnded.Listen(c, func(e agentloop.TurnEndEvent) {
			if e.Status == "failed" && e.Input.Tag == "" {
				send(notify.Notification{Kind: notify.KindFailed, Agent: e.Agent, Session: e.Session,
					Title: name(e.Agent) + " 的任务失败", Body: e.Error})
			}
		})
		channel.OwnerChanged.Listen(c, func(e channel.OwnerEvent) {
			if e.Reason == "handover" {
				send(notify.Notification{Kind: notify.KindHandover, Agent: e.Owner, Session: e.Channel,
					Title: fmt.Sprintf("%s 把任务交给了 %s", name(e.Prev), name(e.Owner))})
			}
		})
		if g, ok := kernel.Lookup[*gateway.Gateway](c, gateway.ServiceKey); ok {
			notify.Sent.Listen(c, func(x notify.Notification) { g.Hub().Publish("notify", x) })
		}
		return nil
	},
})
