// Package topics gives sessions a short generated topic after their first
// completed turn, using the cheap model routed for the "title" task (e.g.
// DeepSeek Flash). Without such a route nothing is generated: topics are
// never worth spending an agent's main model on.
package topics

import (
	"context"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/kernel"
)

const prompt = "You name conversations. Reply with a topic of at most 12 Chinese characters or 6 English words, " +
	"in the language of the conversation, no quotes, no punctuation at the end, nothing else."

// systemFor names the language outright: cheap models asked for "the
// language of the conversation" often answer in Chinese anyway, because the
// prompt mentions Chinese characters.
func systemFor(user string) string {
	for _, r := range user {
		if unicode.Is(unicode.Han, r) {
			return prompt + " Write it in Chinese."
		}
	}
	if strings.IndexFunc(user, unicode.IsLetter) < 0 {
		return prompt
	}
	for _, r := range user {
		if unicode.IsLetter(r) && r > unicode.MaxASCII {
			return prompt // another language: let the model see it
		}
	}
	return prompt + " The conversation is in English: write the topic in English."
}

func clean(s string) string {
	s = strings.TrimSpace(strings.Split(strings.TrimSpace(s), "\n")[0])
	s = strings.Trim(s, "\"'“”‘’「」《》。.!！ ")
	if utf8.RuneCountInString(s) > 40 {
		s = string([]rune(s)[:40])
	}
	return s
}

func firstExchange(ss *session.Session) (user, reply string) {
	for _, e := range ss.Events() {
		switch e.Type {
		case session.TypeUserMessage:
			if user == "" {
				var m session.UserMessage
				_ = e.Decode(&m)
				user = m.Text
			}
		case session.TypeAssistantMessage:
			if user != "" && reply == "" {
				var m session.AssistantMessage
				_ = e.Decode(&m)
				reply = m.Message.Text()
			}
		}
	}
	trim := func(s string) string {
		if r := []rune(s); len(r) > 1500 {
			return string(r[:1500])
		}
		return s
	}
	return trim(user), trim(reply)
}

// Plugin generates topics.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/topics",
	Inject:      []string{session.ServiceKey, llm.ServiceKey},
	Description: "Short session topics from a cheap routed model.",
	Apply: func(c *kernel.Context) error {
		sessions := kernel.Use[*session.Store](c, session.ServiceKey)
		llms := kernel.Use[*llm.Registry](c, llm.ServiceKey)
		type job struct{ session, agent string }
		pending := make(chan job, 64)
		agentloop.TurnEnded.Listen(c, func(e agentloop.TurnEndEvent) {
			if e.Status != "completed" || strings.HasPrefix(e.Input.Tag, "routine:") || strings.HasPrefix(e.Input.Tag, "workflow:") {
				return
			}
			select {
			case pending <- job{e.Session, e.Agent}:
			default:
			}
		})
		c.Go(func(ctx context.Context) {
			for {
				select {
				case <-ctx.Done():
					return
				case j := <-pending:
					id := j.session
					ss, err := sessions.Get(id)
					if err != nil || ss.Meta().Topic != "" {
						continue
					}
					rt, ok := kernel.Lookup[agentloop.Router](c, agentloop.RouterKey)
					if !ok {
						continue
					}
					r, ok := rt.(*router.Router)
					if !ok {
						continue
					}
					var who *agent.Agent
					if ar, ok := kernel.Lookup[*agent.Registry](c, agent.ServiceKey); ok {
						who, _ = ar.Get(j.agent)
					}
					ref, ok := r.RouteForAgent(router.TaskTitle, who)
					if !ok {
						continue
					}
					ad, ok := llms.Get(ref.Provider)
					if !ok {
						continue
					}
					user, reply := firstExchange(ss)
					if user == "" {
						continue
					}
					tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
					resp, err := ad.Stream(tctx, llm.Request{Model: ref.Model, Effort: "minimal", MaxTokens: 60, System: systemFor(user),
						Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "User: "+user+"\n\nAssistant: "+reply)}},
						func(llm.Chunk) error { return nil })
					cancel()
					if err != nil {
						c.Log().Debug("topic generation failed", "session", id, "err", err)
						continue
					}
					if t := clean(resp.Message.Text()); t != "" {
						_ = ss.UpdateMeta(func(m *session.Meta) { m.Topic = t })
						// a blank chat named after its first message takes the
						// topic, so approvals, notifications and search use one
						// name; a name the person gave it stays
						if who != nil && who.IsChat() && who.Spec().Session == id {
							if n := who.Spec().Name; n == agent.ChatDefaultName || n == agent.ChatNameFrom(user) {
								_ = who.Update(func(s *agent.Spec) { s.Name = t })
							}
						}
					}
				}
			}
		})
		return nil
	},
})
