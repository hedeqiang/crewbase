// Package reactions: emoji on messages. The person (or a team member)
// reacts to any message of a conversation; an agent reacts to the message
// it is answering with the react tool — a quick "got it" (👀, 👍) without
// a reply of its own. Each reaction is a session event (message/reaction),
// so they stay with the conversation and reach every open window.
package reactions

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// Data is the message/reaction event data.
type Data struct {
	Target int64  `json:"target"`         // the reacted message's seq
	Emoji  string `json:"emoji"`          // one emoji
	From   string `json:"from,omitempty"` // a team member's name ("" = the person)
	Remove bool   `json:"remove,omitempty"`
}

// valid: one short emoji, nothing else.
func valid(e string) bool {
	if e == "" || len(e) > 32 || !utf8.ValidString(e) {
		return false
	}
	n := 0
	for _, r := range e {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) || unicode.IsPunct(r) || r < 0x80 {
			return false
		}
		n++
	}
	return n <= 8
}

// lastFor is the seq of the newest message to agent (or to whoever owns
// the conversation) in ss.
func lastFor(ss *session.Session, agent string) int64 {
	ev := ss.Events()
	for i := len(ev) - 1; i >= 0; i-- {
		e := ev[i]
		if e.Type != session.TypeUserMessage {
			continue
		}
		var m session.UserMessage
		if e.Decode(&m) == nil && (m.To == "" || m.To == agent || containsStr(m.Also, agent)) {
			return e.Seq
		}
	}
	return 0
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func tool() *tools.Tool {
	return &tools.Tool{
		Name: "react",
		Description: "React with one emoji to the person's latest message, as a person would in a chat: 👀 when you have seen it and start on it, " +
			"👍 to agree or acknowledge, ✅ when what they asked is done, ❤️ for thanks. It shows on their message; it is not a reply. " +
			"Use it sparingly, instead of a separate \"got it\" message, never instead of an answer that is needed.",
		Params:   tools.Schema(map[string]any{"emoji": tools.Prop("string", "one emoji, e.g. 👍")}, "emoji"),
		Category: tools.CategoryRead,
		Execute: func(_ context.Context, call tools.Call) (tools.Result, error) {
			emoji := strings.TrimSpace(call.String("emoji"))
			if !valid(emoji) {
				return tools.Errorf("give one emoji, e.g. 👍"), nil
			}
			if call.Session == nil {
				return tools.Errorf("there is no conversation to react in"), nil
			}
			target := lastFor(call.Session, call.Agent)
			if target == 0 {
				return tools.Errorf("there is no message from the person to react to"), nil
			}
			if _, err := call.Session.Append(session.TypeReaction, call.Agent, Data{Target: target, Emoji: emoji}); err != nil {
				return tools.Errorf("react: %v", err), nil
			}
			return tools.Text(fmt.Sprintf("Reacted %s to their message.", emoji)), nil
		},
	}
}

// Namer gives a team member's name for a device (the members plugin).
type Namer interface{ SenderName(deviceID string) string }

// Plugin is @crewbase/reactions: the react tool and
// POST /api/sessions/{id}/reactions {target, emoji, remove?}.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/reactions",
	Inject:      []string{tools.ServiceKey, session.ServiceKey, gateway.ServiceKey},
	Description: "Emoji reactions on messages: the person's, and an agent's react tool.",
	Apply: func(c *kernel.Context) error {
		kernel.Use[*tools.Registry](c, tools.ServiceKey).Register(c, tool())
		sessions := kernel.Use[*session.Store](c, session.ServiceKey)
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/sessions/{id}/reactions", Devices: []string{"mobile"},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				ss, err := sessions.Get(r.Params["id"])
				if err != nil {
					return nil, gateway.NotFound()
				}
				var b Data
				if err := r.Decode(&b); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				b.Emoji = strings.TrimSpace(b.Emoji)
				if !valid(b.Emoji) {
					return nil, gateway.BadRequest("not an emoji")
				}
				if b.Target <= 0 || b.Target > int64(len(ss.Events())) {
					return nil, gateway.BadRequest("no such message")
				}
				b.From = ""
				if n, ok := kernel.Lookup[Namer](c, "members"); ok && r.DeviceID != "" {
					b.From = n.SenderName(r.DeviceID)
				}
				if _, err := ss.Append(session.TypeReaction, "", b); err != nil {
					return nil, err
				}
				return nil, nil
			}})
		return nil
	},
})
