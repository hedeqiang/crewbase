package topics

// Agent briefs: an agent created without instructions (the person gave a
// name such as 翻译 or 前端工程师, maybe a role tag) gets them written by
// a model from that name: the model routed for the "brief" task (Settings
// › 路由与切换 › 按任务分流), else the cheap "title" model, else the
// agent's own first model. Only the placeholder INSTRUCTIONS.md the registry wrote is
// replaced; anything the person typed in the meantime stays.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	coreprompt "github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/kernel"
)

const briefPrompt = `You write the working instructions of an AI agent from its name and role tag.
Write them in the language of the name (Chinese name → Chinese). Format:
- first line: "你是<name>：<one sentence on what it is for>" (or the same in English);
- then 4 to 6 lines starting with "- ", each a concrete working rule for this kind of job:
  how to approach the work, what to check, what to deliver and in what form, what to avoid.
Plain text only: no title, no code fences, no closing remarks.`

// placeholder is what the registry writes when no instructions are given.
func placeholder(s agent.Spec) string { return "# " + s.Name + "\n\n" + s.Role + "\n" }

// BriefPlugin writes instructions for agents created without them.
var BriefPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/agent-brief",
	Inject:      []string{agent.ServiceKey, llm.ServiceKey, coreprompt.ServiceKey},
	Description: "Writes an agent's instructions from its name when none were given.",
	Apply: func(c *kernel.Context) error {
		llms := kernel.Use[*llm.Registry](c, llm.ServiceKey)
		pending := make(chan *agent.Agent, 16)
		// agents left on the placeholder (every model failed, or the daemon
		// stopped first) get another try a little after start
		c.Go(func(ctx context.Context) {
			select {
			case <-ctx.Done():
				return
			case <-time.After(20 * time.Second):
			}
			if reg, ok := kernel.Lookup[*agent.Registry](c, agent.ServiceKey); ok {
				for _, a := range reg.List() {
					if needsBrief(a) {
						select {
						case pending <- a:
						default:
						}
					}
				}
			}
		})
		// the first turn waits a moment for a brief still being written, so it
		// answers as that agent rather than as a blank one
		agentloop.Request.Use(c, func(ctx context.Context, x agentloop.RequestArgs, next kernel.Next[agentloop.RequestArgs, *llm.Request]) (*llm.Request, error) {
			if x.Agent != nil && writing.has(x.Agent.ID()) {
				writing.wait(ctx, x.Agent.ID(), 25*time.Second)
				// the prompt was built before the brief landed: rebuild it
				if x.Request != nil {
					x.Request.System = agentloop.System(kernel.Use[*coreprompt.Assembler](c, coreprompt.ServiceKey), x.Agent)
				}
			}
			return next(ctx, x)
		})
		agent.Created.Listen(c, func(_ context.Context, a *agent.Agent) (struct{}, error) {
			s := a.Spec()
			if s.Kind == agent.KindChat || s.Ephemeral || strings.TrimSpace(s.Instructions) != "" {
				return struct{}{}, nil
			}
			select {
			case pending <- a:
			default:
			}
			return struct{}{}, nil
		})
		c.Go(func(ctx context.Context) {
			for {
				select {
				case <-ctx.Done():
					return
				case a := <-pending:
					if !needsBrief(a) {
						continue
					}
					writing.start(a.ID())
					err := writeBrief(ctx, c, llms, a)
					writing.done(a.ID())
					if err != nil {
						c.Log().Warn("agent brief not written; it is retried after a restart", "agent", a.ID(), "err", err)
					}
				}
			}
		})
		return nil
	},
})

func writeBrief(ctx context.Context, c *kernel.Context, llms *llm.Registry, a *agent.Agent) error {
	s := a.Spec()
	// candidates in order: the brief rule, the cheap title model, then the
	// agent's own order; each is tried until one answers
	var cands []llm.ModelRef
	add := func(r llm.ModelRef) {
		for _, x := range cands {
			if x.String() == r.String() {
				return
			}
		}
		cands = append(cands, r)
	}
	if rt, found := kernel.Lookup[agentloop.Router](c, agentloop.RouterKey); found {
		if r, isR := rt.(*router.Router); isR {
			if ref, ok := r.RouteForAgent(router.TaskBrief, a); ok {
				add(ref)
			}
			if ref, ok := r.RouteForAgent(router.TaskTitle, a); ok {
				add(ref)
			}
		}
	}
	for _, m := range s.Models {
		add(m)
	}
	var last error
	for _, ref := range cands {
		ad, found := llms.Get(ref.Provider)
		if !found {
			continue
		}
		if err := briefWith(ctx, ad, ref, a); err != nil {
			last = err
			c.Log().Info("agent brief: model failed, trying the next", "agent", a.ID(), "model", ref.String(), "err", err)
			continue
		}
		return nil
	}
	return last
}

func briefWith(ctx context.Context, ad llm.Adapter, ref llm.ModelRef, a *agent.Agent) error {
	s := a.Spec()
	ask := "Name: " + s.Name
	if s.Role != "" {
		ask += "\nRole tag: " + s.Role
	}
	tctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	resp, err := ad.Stream(tctx, llm.Request{Model: ref.Model, Effort: "minimal", MaxTokens: 600, System: briefPrompt,
		Messages: []llm.Message{llm.TextMessage(llm.RoleUser, ask)}}, func(llm.Chunk) error { return nil })
	if err != nil {
		return err
	}
	brief := strings.TrimSpace(strings.Trim(strings.TrimSpace(resp.Message.Text()), "`"))
	if brief == "" {
		return errors.New("empty answer")
	}
	// only over the placeholder: the person may have written their own
	path := filepath.Join(a.Home(), "INSTRUCTIONS.md")
	cur, err := os.ReadFile(path)
	if err != nil || string(cur) != placeholder(s) {
		return nil
	}
	if err := os.WriteFile(path, []byte(brief+"\n"), 0o644); err != nil {
		return err
	}
	_ = a.Update(func(sp *agent.Spec) {}) // views refresh (the panel reads the file)
	return nil
}

// needsBrief: an agent (not a chat or sub-agent) still on the placeholder
// instructions the registry wrote.
func needsBrief(a *agent.Agent) bool {
	s := a.Spec()
	if s.Kind == agent.KindChat || s.Ephemeral {
		return false
	}
	cur, err := os.ReadFile(filepath.Join(a.Home(), "INSTRUCTIONS.md"))
	return err == nil && string(cur) == placeholder(s)
}

// writing tracks briefs being written, so a first turn can wait for one.
var writing = &inFlight{m: map[string]chan struct{}{}}

type inFlight struct {
	mu sync.Mutex
	m  map[string]chan struct{}
}

func (f *inFlight) start(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.m[id]; !ok {
		f.m[id] = make(chan struct{})
	}
}

func (f *inFlight) done(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ch, ok := f.m[id]; ok {
		close(ch)
		delete(f.m, id)
	}
}

func (f *inFlight) has(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.m[id]
	return ok
}

func (f *inFlight) wait(ctx context.Context, id string, max time.Duration) {
	f.mu.Lock()
	ch, ok := f.m[id]
	f.mu.Unlock()
	if !ok {
		return
	}
	t := time.NewTimer(max)
	defer t.Stop()
	select {
	case <-ch:
	case <-t.C:
	case <-ctx.Done():
	}
}
