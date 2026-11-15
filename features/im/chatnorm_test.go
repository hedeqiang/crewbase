package im_test

import (
	"context"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/features/im"
	"github.com/hedeqiang/crewbase/features/im/imtest"
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

// A turn answering a chat message is told to reply like a colleague in
// chat; a turn for a message typed in the app is not.
func TestChatNormOnlyForChatTurns(t *testing.T) {
	e := imtest.New(t)
	for _, s := range []kernel.Spec{llm.Plugin, router.Plugin, agentloop.Plugin} {
		if _, err := e.Host.Load(s, kernel.LoadOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.Host.Load(mock.Plugin, kernel.LoadOptions{Config: map[string]any{"ids": []string{"m"}}}); err != nil {
		t.Fatal(err)
	}
	a, err := e.Agents.Create(agent.Spec{Name: "Kai", Models: []llm.ModelRef{{Provider: "m", Model: "default"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, g := start(t, e, map[string]any{"relayReplies": true})
	ad, _ := kernel.Use[*llm.Registry](e.Host.Root(), llm.ServiceKey).Get("m")
	m := ad.(*mock.Adapter)

	g.Message(context.Background(), im.Inbound{User: im.User{ID: "u-ada"}, Chat: "C1", MessageID: "m1", Text: "@kai CI 挂了吗", Direct: true})
	imtest.Eventually(t, func() bool { return len(m.Requests()) >= 1 })
	agentloop.WaitIdle(a, 0)
	if sys := m.Requests()[0].System; !strings.Contains(sys, "team chat (fake)") || !strings.Contains(sys, "about 100 words") {
		t.Fatalf("chat turn without the chat norm:\n%s", sys)
	}

	if _, err := a.Enqueue(agent.Input{Text: "typed in the app"}); err != nil {
		t.Fatal(err)
	}
	imtest.Eventually(t, func() bool { return len(m.Requests()) >= 2 })
	if sys := m.Requests()[1].System; strings.Contains(sys, "team chat") {
		t.Fatal("app turn got the chat norm")
	}
}
