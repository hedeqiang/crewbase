package topics

// Agent briefs: an agent created without instructions (the person gave a
// name such as 翻译 or 前端工程师, maybe a role tag) gets them written by
// a model from that name: the model routed for the "brief" task (Settings
// › 路由与切换 › 按任务分流), else the cheap "title" model, else the
// agent's own first model. Only the placeholder INSTRUCTIONS.md the registry wrote is
// replaced; anything the person typed in the meantime stays.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
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
	Inject:      []string{agent.ServiceKey, llm.ServiceKey},
	Description: "Writes an agent's instructions from its name when none were given.",
	Apply: func(c *kernel.Context) error {
		llms := kernel.Use[*llm.Registry](c, llm.ServiceKey)
		pending := make(chan *agent.Agent, 16)
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
					if err := writeBrief(ctx, c, llms, a); err != nil {
						c.Log().Debug("agent brief not written", "agent", a.ID(), "err", err)
					}
				}
			}
		})
		return nil
	},
})

func writeBrief(ctx context.Context, c *kernel.Context, llms *llm.Registry, a *agent.Agent) error {
	s := a.Spec()
	ref, ok := llm.ModelRef{}, false
	if rt, found := kernel.Lookup[agentloop.Router](c, agentloop.RouterKey); found {
		if r, isR := rt.(*router.Router); isR {
			// its own rule (路由与切换 › 按任务分流), else the cheap title model
			if ref, ok = r.RouteForAgent(router.TaskBrief, a); !ok {
				ref, ok = r.RouteForAgent(router.TaskTitle, a)
			}
		}
	}
	if !ok && len(s.Models) > 0 {
		ref, ok = s.Models[0], true
	}
	if !ok {
		return nil
	}
	ad, found := llms.Get(ref.Provider)
	if !found {
		return nil
	}
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
		return nil
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
