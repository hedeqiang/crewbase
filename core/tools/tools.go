// Package tools provides the tool registry and the single execution pipeline
// every tool call goes through:
//
//	tool/call (logged first) → tools/pre-execute (Waterfall: hooks, rules,
//	run mode) → guards (deny-or-abstain) → approval (on ask) → tools/execute
//	(Waterfall around the body) → body → tools/post-execute (Waterfall:
//	redaction, rewrite) → finalize → tools/result (Emit) → tool/result (log)
//
// Tools themselves know nothing about policy, sandboxing or approval.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/scope"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/kernel"
)

const ServiceKey = "tools"

// Action categories used by the rules engine (docs/06 §3).
const (
	CategoryRead    = "read"    // read files and web pages
	CategoryExec    = "exec"    // run commands (in sandbox)
	CategoryInstall = "install" // install dependencies
	CategoryWrite   = "write"   // write files in the workspace
	CategoryPush    = "push"    // push code, merge PR
	CategoryDelete  = "delete"  // delete files
	CategoryPublish = "publish" // send messages / publish externally
	CategoryPayment = "payment" // external payment
	CategoryNone    = "none"    // pure / internal tools
)

// Call is one tool invocation.
type Call struct {
	ID      string           `json:"id"`
	Name    string           `json:"name"`
	Args    json.RawMessage  `json:"args"`
	Agent   string           `json:"agent"`
	Session *session.Session `json:"-"`
	// Mode is the effective run mode preset for this call (read-only,
	// sandbox, host), carried per call, never fixed on providers.
	Mode string `json:"mode,omitempty"`
	// Workspace is the agent's working directory.
	Workspace string `json:"workspace,omitempty"`
}

// Decode unmarshals args into v.
func (c Call) Decode(v any) error {
	if len(c.Args) == 0 {
		return nil
	}
	return json.Unmarshal(c.Args, v)
}

// String returns a string argument.
func (c Call) String(key string) string {
	var m map[string]any
	_ = json.Unmarshal(c.Args, &m)
	s, _ := m[key].(string)
	return s
}

// Result is a tool outcome. Content is what the model sees.
type Result struct {
	Content string `json:"content"`
	IsError bool   `json:"isError,omitempty"`
	Data    any    `json:"data,omitempty"` // structured, for UI cards
	// StopTurn ends the agent's turn after this step without another
	// model call (e.g. after handing the task over).
	StopTurn bool `json:"stopTurn,omitempty"`
}

// Text is a success result.
func Text(s string) Result { return Result{Content: s} }

// Errorf is an error result shown to the model.
func Errorf(format string, a ...any) Result {
	return Result{Content: fmt.Sprintf(format, a...), IsError: true}
}

// Tool is a model-callable tool.
type Tool struct {
	Name        string
	Description string
	Params      map[string]any // JSON Schema object
	Category    string         // default action category
	// SessionKinds limits the tool to sessions of these kinds (e.g.
	// "channel"); elsewhere it is invisible and indistinguishable from
	// absent. Empty = every session.
	SessionKinds []string
	// Classify optionally derives the category and a human summary from
	// the arguments (e.g. bash: `git push` → push).
	Classify func(call Call) (category, summary string)
	Execute  func(ctx context.Context, call Call) (Result, error)
}

// Schema builds a JSON Schema object.
func Schema(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

// Prop is a schema property helper.
func Prop(typ, desc string) map[string]any { return map[string]any{"type": typ, "description": desc} }

// Verdict of the pre-execute stage.
type Verdict string

const (
	Allow Verdict = "allow"
	Ask   Verdict = "ask"
	Deny  Verdict = "deny"
)

// Decision flows through tools/pre-execute.
type Decision struct {
	Call     *Call
	Tool     *Tool
	Category string
	Summary  string
	Verdict  Verdict
	Reason   string
	// Rememberable / ConfirmRemember shape the approval card.
	Rememberable    bool
	ConfirmRemember bool
}

// DenyWith short-circuit helper: marks the decision denied.
func (d *Decision) DenyWith(reason string) *Decision {
	d.Verdict, d.Reason = Deny, reason
	return d
}

// RequireApproval upgrades allow → ask (never downgrades deny).
func (d *Decision) RequireApproval(reason string) {
	if d.Verdict == Deny {
		return
	}
	d.Verdict, d.Reason = Ask, reason
}

// Execution is the finalized record of one call.
type Execution struct {
	Call     Call
	Category string
	Result   Result
	Denied   bool
	Outcome  approval.Outcome `json:",omitempty"`
}

// Remembered is emitted when a person chose "always allow" on an approval
// card; the rules engine turns it into an agent-scoped Allow rule.
type Remembered struct {
	Agent    string
	Category string
}

var (
	PreExecute  = kernel.NewWaterfall[*Decision, *Decision]("tools/pre-execute")
	Guard       = kernel.NewBail[*Decision, string]("tools/guard")
	ExecuteHook = kernel.NewWaterfall[Call, Result]("tools/execute")
	PostExecute = kernel.NewWaterfall[Execution, Result]("tools/post-execute")
	OnResult    = kernel.NewEmit[Execution]("tools/result")
	OnCall      = kernel.NewEmit[Call]("tools/call")
	OnRemember  = kernel.NewEmit[Remembered]("tools/remembered")
	Changed     = kernel.NewEmit[struct{}]("tools/changed")
)

// Registry is the `tools` service.
type Registry struct {
	host *kernel.Host
	reg  *scope.Registry[*Tool]
	// policy is the service key of the required policy plugin (rules
	// engine). While it is missing, only read/none categories run.
	policy string
}

func NewRegistry(h *kernel.Host) *Registry {
	r := &Registry{host: h}
	r.reg = scope.New[*Tool](func() { Changed.Emit(h, struct{}{}) })
	return r
}

// Register adds a tool in ctx's scope (agent scope shadows global).
func (r *Registry) Register(c *kernel.Context, t *Tool) {
	if t.Category == "" {
		t.Category = CategoryNone
	}
	r.reg.Register(c, t.Name, t)
}

// Restrict filters tools for ctx's scope; multiple restrictions intersect.
func (r *Registry) Restrict(c *kernel.Context, keep scope.Filter) { r.reg.Restrict(c, keep) }

// Get resolves a tool visible to agent.
func (r *Registry) Get(agent, name string) (*Tool, bool) { return r.reg.Get(agent, name) }

// Visible lists tools for agent.
func (r *Registry) Visible(agent string) []*Tool {
	var out []*Tool
	for _, n := range r.reg.Visible(agent) {
		out = append(out, n.Value)
	}
	return out
}

// Schemas returns tool schemas for prompt assembly (all session kinds).
func (r *Registry) Schemas(agent string) []llm.ToolSchema { return r.SchemasFor(agent, "") }

func availableIn(t *Tool, kind string) bool {
	return kind == "" || len(t.SessionKinds) == 0 || slices.Contains(t.SessionKinds, kind)
}

// SchemasFor returns the schemas visible to agent in a session of kind.
func (r *Registry) SchemasFor(agent, kind string) []llm.ToolSchema {
	var out []llm.ToolSchema
	for _, t := range r.Visible(agent) {
		if !availableIn(t, kind) {
			continue
		}
		p := t.Params
		if p == nil {
			p = Schema(map[string]any{})
		}
		out = append(out, llm.ToolSchema{Name: t.Name, Description: t.Description, Parameters: p})
	}
	return out
}

// Run executes call through the pipeline and records it in the session.
// It never returns an error: failures become error results the model sees.
func (r *Registry) Run(ctx context.Context, c *kernel.Context, call Call) Execution {
	d := kernel.For(c, call.Agent)
	ss := call.Session
	if ss != nil {
		// Record before executing so a crash leaves a visible open call.
		if _, err := ss.Append(session.TypeToolCall, call.Agent, session.ToolCallData{Call: llm.ToolCall{ID: call.ID, Name: call.Name, Args: call.Args}}); err != nil {
			return r.finish(ctx, d, Execution{Call: call, Result: Errorf("tool call could not be recorded: %v", err), Denied: true})
		}
	}
	OnCall.Emit(d, call)

	tool, ok := r.Get(call.Agent, call.Name)
	if ok && ss != nil && !availableIn(tool, ss.Meta().Kind) {
		ok = false
	}
	if !ok {
		return r.finish(ctx, d, Execution{Call: call, Result: Errorf("unknown tool: %s", call.Name)})
	}
	if err := validate(tool, call.Args); err != nil {
		return r.finish(ctx, d, Execution{Call: call, Result: Errorf("invalid arguments for %s: %v", call.Name, err)})
	}

	dec := &Decision{Call: &call, Tool: tool, Category: tool.Category, Verdict: Allow, Rememberable: true}
	if tool.Classify != nil {
		if cat, sum := tool.Classify(call); cat != "" {
			dec.Category, dec.Summary = cat, sum
		} else {
			dec.Summary = sum
		}
	}
	if dec.Summary == "" {
		dec.Summary = call.Name + " " + truncate(string(call.Args), 200)
	}
	dec, err := PreExecute.Run(d, ctx, dec, func(_ context.Context, x *Decision) (*Decision, error) { return x, nil })
	if err != nil || dec == nil {
		return r.finish(ctx, d, Execution{Call: call, Result: Errorf("denied: pre-execute failed: %v", err), Denied: true})
	}
	exe := Execution{Call: call, Category: dec.Category}
	if r.policy != "" && dec.Verdict == Allow && dec.Category != CategoryRead && dec.Category != CategoryNone {
		if _, ok := kernel.Lookup[any](c, r.policy); !ok {
			dec.DenyWith("policy engine unavailable (" + r.policy + "); only read-only tools may run")
		}
	}
	if dec.Verdict == Deny {
		exe.Result, exe.Denied = Errorf("denied: %s", dec.Reason), true
		return r.finish(ctx, d, exe)
	}
	// Guards may only deny or abstain; they cannot re-allow.
	if reason, denied := Guard.Run(d, ctx, dec); denied {
		exe.Result, exe.Denied = Errorf("denied: %s", reason), true
		return r.finish(ctx, d, exe)
	}
	if dec.Verdict == Ask {
		svc, ok := kernel.Lookup[*approval.Service](c, approval.ServiceKey)
		dd := approval.Decision{Outcome: approval.OutcomeUnavailable, Message: "approval service unavailable"}
		if ok && ss != nil {
			dd = svc.Ask(ctx, ss, approval.Request{
				CallID: call.ID, Session: ss.ID(), Agent: call.Agent, Tool: call.Name,
				Category: dec.Category, Summary: dec.Summary, Reason: dec.Reason,
				Rememberable: dec.Rememberable, ConfirmRemember: dec.ConfirmRemember,
			})
		}
		exe.Outcome = dd.Outcome
		if dd.Outcome != approval.OutcomeAllowedOnce {
			msg := string(dd.Outcome)
			if dd.Message != "" {
				msg += ": " + dd.Message
			}
			exe.Result, exe.Denied = Errorf("not approved (%s). Re-plan without this action or ask the user.", msg), true
			return r.finish(ctx, d, exe)
		}
		if dd.Remember {
			OnRemember.Emit(d, Remembered{Agent: call.Agent, Category: dec.Category})
		}
	}
	res, err := ExecuteHook.Run(d, ctx, call, func(ctx context.Context, cl Call) (res Result, err error) {
		defer func() {
			if rec := recover(); rec != nil {
				res, err = Errorf("tool %s crashed: %v", cl.Name, rec), nil
			}
		}()
		return tool.Execute(ctx, cl)
	})
	if err != nil {
		res = Errorf("%v", err)
	}
	if ctx.Err() != nil && !res.IsError {
		res = Errorf("cancelled")
	}
	exe.Result = res
	return r.finish(ctx, d, exe)
}

func (r *Registry) finish(ctx context.Context, d kernel.Dispatcher, exe Execution) Execution {
	res, err := PostExecute.Run(d, ctx, exe, func(_ context.Context, e Execution) (Result, error) { return e.Result, nil })
	if err == nil {
		exe.Result = res
	} else {
		// Fail closed: the raw result may hold data a post-execute
		// listener (e.g. redaction) was supposed to remove.
		exe.Result = Errorf("tool result withheld: post-processing failed")
	}
	// finalized: immutable from here
	OnResult.Emit(d, exe)
	if ss := exe.Call.Session; ss != nil {
		_, _ = ss.Append(session.TypeToolResult, exe.Call.Agent, llm.ToolResult{
			CallID: exe.Call.ID, Name: exe.Call.Name, Content: exe.Result.Content, IsError: exe.Result.IsError,
		})
	}
	return exe
}

func validate(t *Tool, args json.RawMessage) error {
	var m map[string]any
	if len(args) == 0 {
		args = []byte("{}")
	}
	if err := json.Unmarshal(args, &m); err != nil {
		return fmt.Errorf("arguments must be a JSON object")
	}
	req, _ := t.Params["required"].([]string)
	if req == nil {
		if xs, ok := t.Params["required"].([]any); ok {
			for _, x := range xs {
				if s, ok := x.(string); ok {
					req = append(req, s)
				}
			}
		}
	}
	var missing []string
	for _, k := range req {
		if _, ok := m[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required: %s", strings.Join(missing, ", "))
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Plugin provides the `tools` service. Config: {"requirePolicy": "rules"}
// makes the pipeline fail closed while that service is missing.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/tools",
	Description: "Tool registry and execution pipeline (tools service).",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[struct {
			RequirePolicy string `json:"requirePolicy"`
		}](c)
		if err != nil {
			return err
		}
		r := NewRegistry(c.Host())
		r.policy = cfg.RequirePolicy
		return c.Provide(ServiceKey, r)
	},
})
