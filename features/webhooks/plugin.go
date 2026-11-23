package webhooks

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/audit"
	"github.com/hedeqiang/crewbase/features/channel"
	"github.com/hedeqiang/crewbase/features/relay"
	"github.com/hedeqiang/crewbase/features/routines"
	"github.com/hedeqiang/crewbase/features/workflows"
	"github.com/hedeqiang/crewbase/kernel"
)

// Config of @crewbase/webhooks.
type Config struct {
	Dir string `json:"dir"` // the data directory (state in <dir>/webhooks)
}

// Plugin is @crewbase/webhooks.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/webhooks",
	Inject:      []string{agent.ServiceKey, tools.ServiceKey},
	Optional:    []string{credentials.ServiceKey, workflows.ServiceKey, routines.ServiceKey, channel.ServiceKey, audit.ServiceKey},
	Description: "Webhooks in and out: verified third-party events fan out to workflows, routines and channels; internal events go out signed, with retries.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		dir := cfg.Dir
		if dir == "" {
			if dir, err = os.MkdirTemp("", "crewbase-webhooks-"); err != nil {
				return err
			}
		}
		st, err := openStore(filepath.Join(dir, "webhooks"))
		if err != nil {
			return fmt.Errorf("webhooks state: %w", err)
		}
		s := New(c, st, kernel.Use[*agent.Registry](c, agent.ServiceKey))
		if err := c.Provide(ServiceKey, s); err != nil {
			return err
		}
		s.registerTools(c, kernel.Use[*tools.Registry](c, tools.ServiceKey))
		s.bridge(c)
		// public intake through the relay, for endpoints that opted in
		relay.HookTokens.Listen(c, func(context.Context, struct{}) ([]string, bool) { return s.RelayTokens(), true })
		relay.HookIntake.Listen(c, func(ctx context.Context, h relay.HookCall) (int, bool) {
			e, ok := s.endpointByRelay(h.Token)
			if !ok {
				return http.StatusNotFound, true
			}
			_, st := s.Ingest(ctx, e, h.Header, h.Body, "relay")
			return st, true
		})
		Changed.Listen(c, func(ev ChangeEvent) {
			if ev.Kind == "endpoint" {
				relay.HooksChanged.Emit(c, struct{}{})
			}
		})
		c.Go(s.worker)
		return nil
	},
})

// New makes the service (the plugin, and tests).
func New(c *kernel.Context, st *store, agents *agent.Registry) *Service {
	look := func(key string) any {
		if c == nil {
			return nil
		}
		v, _ := kernel.Lookup[any](c, key)
		return v
	}
	s := &Service{c: c, st: st, agents: agents, now: time.Now, fired: map[string][]time.Time{}, cool: map[string]time.Time{}, kick: make(chan struct{}, 1)}
	s.creds = func() *credentials.Service { v, _ := look(credentials.ServiceKey).(*credentials.Service); return v }
	s.workflows = func() *workflows.Service { v, _ := look(workflows.ServiceKey).(*workflows.Service); return v }
	s.routines = func() *routines.Service { v, _ := look(routines.ServiceKey).(*routines.Service); return v }
	s.channels = func() *channel.Service { v, _ := look(channel.ServiceKey).(*channel.Service); return v }
	s.audit = func(typ, actor, ag, session string, data any) {
		if a, ok := look(audit.ServiceKey).(*audit.Service); ok {
			_, _ = a.Record(audit.Entry{Type: typ, Actor: actor, Agent: ag, Session: session, Data: data})
		}
	}
	s.out.allow = func() []netip.Prefix {
		p, _ := parsePrefixes(s.Settings().AllowPrivate)
		return p
	}
	return s
}

// ---------------------------------------------------------------- tools

func (s *Service) registerTools(c *kernel.Context, reg *tools.Registry) {
	teamOf := func(id string) string {
		if a, ok := s.agents.Get(id); ok {
			return a.Spec().TeamID()
		}
		return agent.DefaultTeam
	}
	reg.Register(c, &tools.Tool{
		Name: "webhook_deliveries",
		Description: "List your team's recent webhook deliveries: inbound events (from GitHub, Sentry, Stripe… — whether they verified, matched and what they triggered) " +
			"and outbound ones (to subscribed URLs — delivered, retrying or dead, with the status code). Use it to find out why an event did or did not arrive.",
		Params: tools.Schema(map[string]any{
			"dir":    tools.Prop("string", "in | out (default both)"),
			"status": tools.Prop("string", "only this status (accepted, filtered, rejected, cooldown, pending, delivered, dead…)"),
			"limit":  tools.Prop("integer", "how many (default 20)"),
		}),
		Category: tools.CategoryRead,
		Load:     tools.LoadDeferred,
		Group:    "webhooks",
		Execute: func(_ context.Context, call tools.Call) (tools.Result, error) {
			var a struct {
				Dir, Status string
				Limit       int
			}
			_ = call.Decode(&a)
			if a.Limit <= 0 || a.Limit > 100 {
				a.Limit = 20
			}
			var b strings.Builder
			n := 0
			for _, d := range s.Deliveries(teamOf(call.Agent), "", 500) {
				if (a.Dir != "" && d.Dir != a.Dir) || (a.Status != "" && d.Status != a.Status) {
					continue
				}
				fmt.Fprintf(&b, "%s %s %s · %s · %s", d.At.Local().Format("01-02 15:04:05"), d.Dir, d.Name, orDash(d.Event), d.Status)
				if d.Code != 0 {
					fmt.Fprintf(&b, " (HTTP %d, %d attempt(s))", d.Code, d.Attempts)
				}
				if d.Error != "" {
					fmt.Fprintf(&b, " — %s", clip(d.Error, 200))
				}
				for _, t := range d.Targets {
					ok := "ok"
					if !t.OK {
						ok = "failed: " + t.Error
					}
					fmt.Fprintf(&b, "\n    → %s %s: %s", t.Kind, t.ID, ok)
				}
				b.WriteString("\n")
				if n++; n >= a.Limit {
					break
				}
			}
			if n == 0 {
				return tools.Text("No webhook deliveries match."), nil
			}
			return tools.Text(b.String()), nil
		},
	})
	reg.Register(c, &tools.Tool{
		Name: "webhook_emit",
		Description: "Send a notification out of Crewbase to your team's webhook subscriptions that take agent.emit events (another system, a chat, an incident tool). " +
			"It goes out signed; the person approves each one. Give a short message and optional structured data.",
		Params: tools.Schema(map[string]any{
			"message":      tools.Prop("string", "what happened, in one or two sentences"),
			"data":         map[string]any{"type": "object", "description": "structured details for the receiver"},
			"subscription": tools.Prop("string", "only this subscription (id or name); default every one that takes agent.emit"),
		}, "message"),
		Category: tools.CategoryPublish,
		Load:     tools.LoadDeferred,
		Group:    "webhooks",
		Classify: func(call tools.Call) (string, string) {
			to := call.String("subscription")
			if to == "" {
				to = "webhook subscriptions"
			}
			return tools.CategoryPublish, "Send to " + to + ": " + clip(call.String("message"), 120)
		},
		Execute: func(_ context.Context, call tools.Call) (tools.Result, error) {
			var a struct {
				Message, Subscription string
				Data                  map[string]any
			}
			_ = call.Decode(&a)
			team := teamOf(call.Agent)
			data := map[string]any{"agent": call.Agent, "message": a.Message, "data": a.Data}
			if call.Session != nil {
				data["session"] = call.Session.ID()
			}
			var sent []string
			for _, x := range s.Subscriptions(team) {
				if !x.Enabled || !(contains(x.Events, OutEmit) || contains(x.Events, "*")) {
					continue
				}
				if a.Subscription != "" && a.Subscription != x.ID && !strings.EqualFold(a.Subscription, x.Name) {
					continue
				}
				s.enqueue(x, OutEmit, data, "tool")
				sent = append(sent, x.Name)
			}
			if len(sent) == 0 {
				return tools.Errorf("webhook_emit: no subscription of your team takes agent.emit events%s", ifName(a.Subscription)), nil
			}
			s.record("webhook/emitted", "agent:"+call.Agent, map[string]any{"to": sent, "message": clip(a.Message, 500)})
			return tools.Text("Queued for " + strings.Join(sent, ", ") + "; check webhook_deliveries for the result."), nil
		},
	})
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func ifName(n string) string {
	if n == "" {
		return ""
	}
	return " named " + n
}
