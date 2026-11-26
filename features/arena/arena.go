// Package arena is 模型擂台: one task sent to several models at once, the
// answers side by side with their time, tokens and cost, and the person
// picks the best. The pick continues the conversation (the winning answer
// becomes the conversation's reply), and every arena is remembered in a
// small store (<root>/arena.json), so the stats can say which model wins
// which kind of task, at what cost, and the picker can suggest one.
//
// Each model answers as an ephemeral sub-agent (features/subagent, the
// "local" provider) with that one model: the same agent loop, tools, run
// mode (never wider than the conversation's) and approvals as any
// delegation, in its own session. The conversation's log carries the
// arena as events:
//
//	user/message    the task, as the person's message (data.arena = id)
//	arena/started   the task, its category and the models
//	arena/run       model i runs as agent child in session childSession
//	arena/result    how model i's run went: answer, time, tokens, cost
//	arena/picked    the person picked model i
//	message/assistant  the picked answer, by the conversation's agent
//	                (data.arena = id; no usage, it was counted in the run)
package arena

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hedeqiang/crewbase/capabilities/subagent"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/features/channel"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/features/usage"
	"github.com/hedeqiang/crewbase/kernel"
)

// Session event types (in the conversation's session).
const (
	TypeStarted = "arena/started"
	TypeRun     = "arena/run"
	TypeResult  = "arena/result"
	TypePicked  = "arena/picked"
)

// Limits.
const (
	MinModels = 2
	MaxModels = 4
	// MaxTask bounds the task (bytes).
	MaxTask = 32 << 10
	// MaxOutput bounds an answer kept in arena/result (bytes).
	MaxOutput = 128 << 10
)

// Instructions are what each arena model is told besides the task.
const Instructions = "Answer the person's request directly and completely, as you would in a conversation with them. " +
	"Several models answer the same request side by side and the person picks the best answer, so give your best complete answer in this one reply.\n"

// StartedData is arena/started.
type StartedData struct {
	ID       string   `json:"id"`
	Task     string   `json:"task"`
	Category string   `json:"category"`
	Models   []string `json:"models"`
	// Agent is the conversation's agent (a channel: its owner), whose
	// reply the picked answer becomes.
	Agent string `json:"agent"`
	// Members: in a channel, the member each model belongs to ("" none):
	// the picked answer is that member's reply.
	Members []string `json:"members,omitempty"`
}

// RunData is arena/run.
type RunData struct {
	ID           string `json:"id"`
	Index        int    `json:"index"`
	Model        string `json:"model"`
	Child        string `json:"child"`
	ChildSession string `json:"childSession"`
}

// ResultData is arena/result.
type ResultData struct {
	ID     string `json:"id"`
	Index  int    `json:"index"`
	Model  string `json:"model"`
	Status string `json:"status"` // completed | failed | cancelled
	Error  string `json:"error,omitempty"`
	Answer string `json:"answer,omitempty"`
	Ms     int64  `json:"ms"`
	// Calls are the model calls the run made; Input/Output their tokens.
	Calls     int     `json:"calls"`
	Input     int     `json:"input"`
	Output    int     `json:"output"`
	CacheRead int     `json:"cacheRead,omitempty"`
	CostUSD   float64 `json:"costUsd"`
	// Priced: every call had a known price (or was free).
	Priced bool `json:"priced"`
}

// PickedData is arena/picked.
type PickedData struct {
	ID    string `json:"id"`
	Index int    `json:"index"`
	Model string `json:"model"`
	// Agent wrote the picked answer into the conversation.
	Agent string `json:"agent"`
}

// userMessage is the task as the person's message, marked as an arena's.
type userMessage struct {
	session.UserMessage
	Arena string `json:"arena"`
}

// assistantMessage is the picked answer, marked as an arena's.
type assistantMessage struct {
	session.AssistantMessage
	Arena string `json:"arena"`
}

// StartRequest is POST /api/sessions/{id}/arena.
type StartRequest struct {
	Text   string   `json:"text"`
	Models []string `json:"models"`
	// Agent is the conversation's agent ("" = the session's first; a
	// channel always uses its owner).
	Agent string `json:"agent,omitempty"`
}

// Service runs arenas.
type Service struct {
	c        *kernel.Context
	agents   *agent.Registry
	sessions *session.Store
	subs     *subagent.Service
	llms     *llm.Registry
	store    *Store

	mu      sync.Mutex
	running map[string]context.CancelFunc // arena id → stop
}

// ServiceKey is the arena service.
const ServiceKey = "arena"

func newID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "ar" + hex.EncodeToString(b[:])
}

// conversation resolves whose conversation ss is: the agent whose reply a
// picked answer becomes, and in a channel its members.
func (s *Service) conversation(ss *session.Session, want string) (*agent.Agent, []string, error) {
	m := ss.Meta()
	if m.Kind == "channel" {
		id := channel.Owner(ss)
		if id == "" && len(m.Agents) > 0 {
			id = m.Agents[0]
		}
		a, ok := s.agents.Get(id)
		if !ok {
			return nil, nil, errors.New("the channel has no owner")
		}
		if channel.State(ss) == channel.StateArchived {
			return nil, nil, errors.New("the channel is archived")
		}
		return a, m.Agents, nil
	}
	id := want
	if id == "" && len(m.Agents) > 0 {
		id = m.Agents[0]
	}
	if !contains(m.Agents, id) {
		return nil, nil, fmt.Errorf("agent %q is not in this conversation", id)
	}
	a, ok := s.agents.Get(id)
	if !ok {
		return nil, nil, fmt.Errorf("agent %q not found", id)
	}
	return a, nil, nil
}

// Start opens an arena in session sid and runs every model in parallel;
// it returns once the arena is recorded.
func (s *Service) Start(sid string, req StartRequest) (StartedData, error) {
	ss, err := s.sessions.Get(sid)
	if err != nil {
		return StartedData{}, gateway.NotFound()
	}
	task := strings.TrimSpace(req.Text)
	if task == "" {
		return StartedData{}, gateway.BadRequest("the task is empty")
	}
	if len(task) > MaxTask {
		return StartedData{}, gateway.BadRequest("the task is too long (at most %d KB)", MaxTask>>10)
	}
	if len(req.Models) < MinModels || len(req.Models) > MaxModels {
		return StartedData{}, gateway.BadRequest("pick %d to %d models", MinModels, MaxModels)
	}
	refs := make([]llm.ModelRef, 0, len(req.Models))
	seen := map[string]bool{}
	for _, m := range req.Models {
		r, err := llm.ParseRef(strings.TrimSpace(m))
		if err != nil {
			return StartedData{}, gateway.BadRequest("%v", err)
		}
		if seen[r.Full()] {
			return StartedData{}, gateway.BadRequest("%s is picked twice", r)
		}
		seen[r.Full()] = true
		if _, ok := s.llms.Get(r.Provider); !ok {
			return StartedData{}, gateway.BadRequest("provider %s is not connected", r.Provider)
		}
		refs = append(refs, r)
	}
	conv, members, err := s.conversation(ss, req.Agent)
	if err != nil {
		return StartedData{}, gateway.BadRequest("%v", err)
	}
	// in a one-to-one conversation the task joins its history: not in the
	// middle of a turn of the agent's
	if members == nil {
		if st, _ := conv.Status(); st == agent.StatusRunning && conv.StatusSession() == sid {
			return StartedData{}, gateway.Conflict("%s is still replying; start the arena when it is done", conv.Spec().Name)
		}
	}

	d := StartedData{ID: newID(), Task: task, Category: Categorize(task), Agent: conv.ID()}
	for _, r := range refs {
		d.Models = append(d.Models, r.Full())
	}
	if members != nil {
		d.Members = make([]string, len(refs))
		for i, r := range refs {
			d.Members[i] = memberFor(s.agents, members, r)
		}
	}
	to := conv.ID()
	if members != nil {
		to = "" // a channel: the whole table's question
	}
	if _, err := ss.Append(session.TypeUserMessage, "", userMessage{UserMessage: session.UserMessage{Text: task, To: to}, Arena: d.ID}); err != nil {
		return StartedData{}, err
	}
	if _, err := ss.Append(TypeStarted, "", d); err != nil {
		return StartedData{}, err
	}
	rec := Record{ID: d.ID, Session: sid, At: time.Now().UTC(), Task: task, Category: d.Category, Models: d.Models, Runs: make([]RunStat, len(refs))}
	for i := range refs {
		rec.Runs[i].Model = d.Models[i]
	}
	if err := s.store.Add(rec); err != nil {
		s.c.Log().Warn("arena: record", "err", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.running[d.ID] = cancel
	s.mu.Unlock()
	var wg sync.WaitGroup
	for i, r := range refs {
		wg.Add(1)
		s.c.Go(func(host context.Context) {
			defer wg.Done()
			stop := context.AfterFunc(host, cancel)
			defer stop()
			s.run(ctx, ss, d, conv, i, r)
		})
	}
	s.c.Go(func(context.Context) {
		wg.Wait()
		s.mu.Lock()
		delete(s.running, d.ID)
		s.mu.Unlock()
		cancel()
	})
	return d, nil
}

// memberFor is the channel member that runs on r (its first model), or "".
func memberFor(agents *agent.Registry, members []string, r llm.ModelRef) string {
	for _, id := range members {
		if a, ok := agents.Get(id); ok {
			if ms := a.Spec().Models; len(ms) > 0 && ms[0].String() == r.String() {
				return id
			}
		}
	}
	return ""
}

// run is model i's answer: an ephemeral sub-agent on that model alone.
func (s *Service) run(ctx context.Context, ss *session.Session, d StartedData, conv *agent.Agent, i int, ref llm.ModelRef) {
	res := ResultData{ID: d.ID, Index: i, Model: ref.Full()}
	var childSession, child string
	start := time.Now()
	p, ok := s.subs.Provider("local")
	var out subagent.Result
	var err error
	if !ok {
		err = errors.New("sub-agents are not available")
	} else {
		// straight to the provider: an arena is the person's own fan-out
		// (2–4 runs), not an agent's delegation, so the per-parent
		// delegation limit does not apply to it
		out, err = p.Spawn(ctx, subagent.Request{
			Parent: conv.ID(), ParentSession: ss.ID(), Task: d.Task, Depth: 1,
			Models: []llm.ModelRef{ref}, Instructions: Instructions,
			OnStart: func(c, cs string) {
				child, childSession = c, cs
				start = time.Now()
				_, _ = ss.Append(TypeRun, "", RunData{ID: d.ID, Index: i, Model: ref.Full(), Child: c, ChildSession: cs})
			},
		})
	}
	res.Ms = time.Since(start).Milliseconds()
	switch {
	case err != nil:
		res.Status, res.Error = subagent.StopFailed, err.Error()
	case ctx.Err() != nil:
		res.Status, res.Error = subagent.StopCancelled, "stopped"
	default:
		res.Status, res.Error = out.StopReason, out.Diagnostic
	}
	if out.ChildSession != "" {
		childSession = out.ChildSession
	}
	res.Answer = clip(out.Output, MaxOutput)
	if res.Status == subagent.StopCompleted && strings.TrimSpace(res.Answer) == "" {
		res.Status, res.Error = subagent.StopFailed, "no answer"
	}
	s.measure(&res, child, childSession)
	if _, err := ss.Append(TypeResult, "", res); err != nil {
		s.c.Log().Warn("arena: result", "err", err)
	}
	if err := s.store.SetRun(d.ID, i, RunStat{Model: res.Model, Status: res.Status, Ms: res.Ms, Input: res.Input, Output: res.Output, CostUSD: res.CostUSD, Priced: res.Priced}); err != nil {
		s.c.Log().Warn("arena: record", "err", err)
	}
}

// measure sums the run's model calls (child's replies in its session):
// tokens and cost, by the usage feature's pricing.
func (s *Service) measure(res *ResultData, child, childSession string) {
	res.Priced = true
	if childSession == "" {
		return
	}
	css, err := s.sessions.Get(childSession)
	if err != nil {
		return
	}
	pricer, _ := kernel.Lookup[*usage.Service](s.c, usage.ServiceKey)
	for _, e := range css.Events() {
		if e.Type != session.TypeAssistantMessage || (child != "" && e.Agent != child) {
			continue
		}
		var am session.AssistantMessage
		if e.Decode(&am) != nil {
			continue
		}
		res.Calls++
		res.Input += am.Usage.InputTokens
		res.Output += am.Usage.OutputTokens
		res.CacheRead += am.Usage.CacheRead
		if pricer == nil {
			res.Priced = false
			continue
		}
		usd, ok := pricer.Cost(am.Model, am.Usage)
		res.CostUSD += usd
		if !ok {
			res.Priced = false
		}
	}
}

// Stop stops a running arena's models.
func (s *Service) Stop(id string) bool {
	s.mu.Lock()
	cancel, ok := s.running[id]
	s.mu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

// find reads arena id's events from ss.
func find(ss *session.Session, id string) (st *StartedData, results map[int]ResultData, picked *PickedData) {
	results = map[int]ResultData{}
	for _, e := range ss.Events() {
		switch e.Type {
		case TypeStarted:
			var d StartedData
			if e.Decode(&d) == nil && d.ID == id {
				st = &d
			}
		case TypeResult:
			var r ResultData
			if e.Decode(&r) == nil && r.ID == id {
				results[r.Index] = r
			}
		case TypePicked:
			var p PickedData
			if e.Decode(&p) == nil && p.ID == id {
				picked = &p
			}
		}
	}
	return st, results, picked
}

// Pick makes model i's answer the conversation's reply and remembers it.
func (s *Service) Pick(sid, id string, i int) (PickedData, error) {
	ss, err := s.sessions.Get(sid)
	if err != nil {
		return PickedData{}, gateway.NotFound()
	}
	st, results, picked := find(ss, id)
	if st == nil {
		return PickedData{}, gateway.NotFound()
	}
	if picked != nil {
		return PickedData{}, gateway.Conflict("an answer was already picked")
	}
	r, ok := results[i]
	if !ok || r.Status != subagent.StopCompleted || strings.TrimSpace(r.Answer) == "" {
		return PickedData{}, gateway.BadRequest("that model has no answer to pick")
	}
	ref, err := llm.ParseRef(r.Model)
	if err != nil {
		return PickedData{}, gateway.BadRequest("%v", err)
	}
	who := st.Agent
	if i < len(st.Members) && st.Members[i] != "" {
		if _, ok := s.agents.Get(st.Members[i]); ok {
			who = st.Members[i]
		}
	}
	// the picked answer as the conversation's reply: the history the next
	// turn is built from has the task and this answer (its tokens were
	// counted in the run, so none here)
	am := assistantMessage{AssistantMessage: session.AssistantMessage{Message: llm.TextMessage(llm.RoleAssistant, r.Answer), Model: ref, Stop: "end_turn"}, Arena: id}
	if _, err := ss.Append(session.TypeAssistantMessage, who, am); err != nil {
		return PickedData{}, err
	}
	p := PickedData{ID: id, Index: i, Model: r.Model, Agent: who}
	if _, err := ss.Append(TypePicked, "", p); err != nil {
		return PickedData{}, err
	}
	if err := s.store.SetWinner(id, r.Model); err != nil {
		s.c.Log().Warn("arena: record", "err", err)
	}
	return p, nil
}

// Stats is the 擂台 report.
func (s *Service) Stats() Stats { return s.store.Stats() }

// Suggest is the model usually picked for tasks like this one.
func (s *Service) Suggest(task string) Suggestion { return s.store.Suggest(task) }

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// clip cuts s to at most n bytes on a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "\n…"
}

// Config of the arena plugin.
type Config struct {
	// Dir holds arena.json ("" = in memory).
	Dir string `json:"dir"`
}

// Plugin is @crewbase/arena: the service and its API.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/arena",
	Inject:      []string{agent.ServiceKey, session.ServiceKey, subagent.ServiceKey, llm.ServiceKey, gateway.ServiceKey},
	Optional:    []string{usage.ServiceKey},
	Description: "模型擂台: one task to several models side by side; the person's picks say which model is best for which kind of task.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		st, err := OpenStore(cfg.Dir)
		if err != nil {
			return fmt.Errorf("arena store: %w", err)
		}
		s := &Service{
			c: c, store: st, running: map[string]context.CancelFunc{},
			agents:   kernel.Use[*agent.Registry](c, agent.ServiceKey),
			sessions: kernel.Use[*session.Store](c, session.ServiceKey),
			subs:     kernel.Use[*subagent.Service](c, subagent.ServiceKey),
			llms:     kernel.Use[*llm.Registry](c, llm.ServiceKey),
		}
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		dev := []string{"mobile"}
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/sessions/{id}/arena", Devices: dev,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b StartRequest
				if err := r.Decode(&b); err != nil {
					return nil, err
				}
				return s.Start(r.Params["id"], b)
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/sessions/{id}/arena/{arena}/pick", Devices: dev,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b struct {
					Index int `json:"index"`
				}
				if err := r.Decode(&b); err != nil {
					return nil, err
				}
				return s.Pick(r.Params["id"], r.Params["arena"], b.Index)
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/sessions/{id}/arena/{arena}/stop", Devices: dev,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				return map[string]bool{"stopped": s.Stop(r.Params["arena"])}, nil
			}})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/arena/stats", Devices: dev,
			Handle: func(context.Context, *gateway.Request) (any, error) { return s.Stats(), nil }})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/arena/suggest", Devices: dev,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				return s.Suggest(r.HTTP.URL.Query().Get("text")), nil
			}})
		return c.Provide(ServiceKey, s)
	},
})
