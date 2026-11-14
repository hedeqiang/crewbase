package audit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/rules"
	"github.com/hedeqiang/crewbase/kernel"
)

// ServiceKey is the key of the audit service (*Service).
const ServiceKey = "audit"

// Record types written by the plugin.
const (
	TypeStarted         = "audit/started"
	TypeRuleDecision    = "rule/decision"  // pre-execute verdict ask | deny
	TypeToolDenied      = "tool/denied"    // denied later (guard, policy engine missing)
	TypeApprovalAsked   = "approval/asked" // approval requested
	TypeApprovalDecided = "approval/decided"
	TypeRulesChanged    = "rules/changed"
	TypeModeChanged     = "mode/changed"
	TypeApprovalPolicy  = "approval/policy"
	TypeAgentCreated    = "agent/created"
	TypeAgentDeleted    = "agent/deleted"
	TypePluginLoaded    = "plugin/loaded"
	TypePluginState     = "plugin/state"
	TypePluginUnloaded  = "plugin/unloaded"
	// Session events with these prefixes are copied (goal authorisation
	// and status changes are security relevant: docs/06 §9).
	goalPrefix = "goal/"
)

// Service is the `audit` service.
type Service struct {
	log  *Log
	root string
}

// Log returns the underlying log.
func (s *Service) Log() *Log { return s.log }

// Record appends an entry (for other plugins: remote login, device pairing,
// credential changes...). Errors are returned, not swallowed.
func (s *Service) Record(e Entry) (Record, error) { return s.log.Append(e) }

// Verify checks the chain.
func (s *Service) Verify() (VerifyResult, error) { return Verify(s.root) }

// Config of the plugin.
type Config struct {
	Dir string `json:"dir"`
	// Deprecated: plugin lifecycle now comes from kernel events; kept so
	// existing configs still decode. Was: how often the tree is diffed for
	// lifecycle changes (the kernel has no global plugin event). Default 5;
	// negative disables.
	PluginPollSeconds int `json:"pluginPollSeconds"`
}

// Plugin records security-relevant events in the audit log.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/audit",
	Optional:    []string{rules.ServiceKey},
	Description: "Append-only, hash-chained audit log (rule decisions, approvals, mode, rules, agents, plugins).",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		if cfg.Dir == "" {
			return errors.New("audit: config dir is required")
		}
		lg, err := Open(cfg.Dir)
		if err != nil {
			return err
		}
		c.Defer(func() { _ = lg.Close() })
		s := &Service{log: lg, root: cfg.Dir}
		log := c.Log()
		rec := func(e Entry) {
			if _, err := lg.Append(e); err != nil {
				log.Error("audit write failed", "type", e.Type, "err", err)
			}
		}
		started := time.Now()
		seq, head := lg.Head()
		rec(Entry{Type: TypeStarted, Actor: "system", Data: map[string]any{"resumedSeq": seq, "resumedHead": head}})

		// Rule decisions: a pass-through middleware placed outermost sees
		// the final pre-execute verdict. It never changes the decision.
		var dmu sync.Mutex
		ruled := map[string]bool{} // call ids already recorded as rule decisions
		tools.PreExecute.Prepend(c, func(ctx context.Context, d *tools.Decision, next kernel.Next[*tools.Decision, *tools.Decision]) (*tools.Decision, error) {
			out, err := next(ctx, d)
			if err == nil && out != nil && (out.Verdict == tools.Ask || out.Verdict == tools.Deny) && out.Call != nil {
				e := Entry{Type: TypeRuleDecision, Actor: "rules", Agent: out.Call.Agent, Data: map[string]any{
					"callId": out.Call.ID, "tool": out.Call.Name, "category": out.Category,
					"verdict": string(out.Verdict), "reason": out.Reason,
				}}
				if out.Call.Session != nil {
					e.Session = out.Call.Session.ID()
				}
				rec(e)
				if out.Verdict == tools.Deny {
					dmu.Lock()
					ruled[out.Call.ID] = true
					dmu.Unlock()
				}
			}
			return out, err
		})
		tools.OnResult.Listen(c, func(x tools.Execution) {
			dmu.Lock()
			already := ruled[x.Call.ID]
			delete(ruled, x.Call.ID)
			dmu.Unlock()
			// Approval outcomes are recorded via approval/decided.
			if !x.Denied || already || x.Outcome != "" {
				return
			}
			e := Entry{Type: TypeToolDenied, Actor: "system", Agent: x.Call.Agent, Data: map[string]any{
				"callId": x.Call.ID, "tool": x.Call.Name, "category": x.Category, "reason": x.Result.Content,
			}}
			if x.Call.Session != nil {
				e.Session = x.Call.Session.ID()
			}
			rec(e)
		})
		approval.Asked.Listen(c, func(r approval.Request) {
			rec(Entry{Type: TypeApprovalAsked, Actor: r.Agent, Agent: r.Agent, Session: r.Session, Data: map[string]any{
				"callId": r.CallID, "tool": r.Tool, "category": r.Category, "summary": r.Summary, "reason": r.Reason,
			}})
		})
		approval.Decided.Listen(c, func(d approval.DecidedData) {
			actor := d.By
			if actor == "" {
				actor = "unknown"
			}
			rec(Entry{Type: TypeApprovalDecided, Actor: actor, Data: map[string]any{
				"callId": d.CallID, "outcome": string(d.Outcome), "remember": d.Remember, "confirmed": d.Confirmed, "message": d.Message,
			}})
		})
		rules.Changed.Listen(c, func(struct{}) {
			data := map[string]any{}
			if e, ok := kernel.Lookup[*rules.Engine](c, rules.ServiceKey); ok {
				data["remembered"] = e.Remembered()
			}
			rec(Entry{Type: TypeRulesChanged, Actor: "user", Data: data})
		})
		session.OnEvent.Listen(c, func(a session.Appended) {
			ev := a.Event
			var typ string
			switch {
			case ev.Type == session.TypeModeChanged:
				typ = TypeModeChanged
			case ev.Type == session.TypeApprovalPolicy:
				typ = TypeApprovalPolicy
			case strings.HasPrefix(ev.Type, goalPrefix):
				typ = ev.Type
			default:
				return
			}
			actor := "user"
			var by struct {
				By string `json:"by"`
			}
			if json.Unmarshal(ev.Data, &by) == nil && by.By != "" {
				actor = by.By
			}
			rec(Entry{Type: typ, Actor: actor, Agent: ev.Agent, Session: a.Session, Data: map[string]any{"sessionSeq": ev.Seq, "event": ev.Data}})
		})
		agent.Created.Listen(c, func(_ context.Context, a *agent.Agent) (struct{}, error) {
			sp := a.Spec()
			// Agents restored at startup were recorded when created.
			if sp.Created.Before(started.Add(-time.Second)) {
				return struct{}{}, nil
			}
			rec(Entry{Type: TypeAgentCreated, Actor: "user", Agent: sp.ID, Session: sp.Session, Data: map[string]any{
				"name": sp.Name, "mode": sp.Mode, "ephemeral": sp.Ephemeral, "parent": sp.Parent,
			}})
			return struct{}{}, nil
		})
		agent.Deleted.Listen(c, func(id string) {
			rec(Entry{Type: TypeAgentDeleted, Actor: "user", Agent: id})
		})

		// Plugin lifecycle from the kernel's own events (no polling).
		self := c.PluginID()
		rec(Entry{Type: TypePluginState, Actor: "system", Data: map[string]any{"snapshot": snapshotPlugins(c.Host(), self)}})
		kernel.PluginChanged.Listen(c, func(ev kernel.PluginEvent) {
			if ev.ID == self {
				return
			}
			switch {
			case ev.From == "":
				rec(Entry{Type: TypePluginLoaded, Actor: "system", Data: map[string]any{"id": ev.ID, "name": ev.Name, "state": string(ev.To)}})
			case ev.To == kernel.StateDisposed:
				rec(Entry{Type: TypePluginUnloaded, Actor: "system", Data: map[string]any{"id": ev.ID, "name": ev.Name}})
			default:
				rec(Entry{Type: TypePluginState, Actor: "system", Data: map[string]any{"id": ev.ID, "name": ev.Name, "from": string(ev.From), "to": string(ev.To), "error": ev.Error}})
			}
		})
		return c.Provide(ServiceKey, s)
	},
})

type pluginState struct {
	Name  string `json:"name"`
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

func snapshotPlugins(h *kernel.Host, self string) map[string]pluginState {
	out := map[string]pluginState{}
	for _, p := range h.Plugins() {
		if p.ID == self {
			continue
		}
		out[p.ID] = pluginState{Name: p.Name, State: string(p.State), Error: p.Error}
	}
	return out
}
