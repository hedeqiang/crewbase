package im_test

import (
	"context"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/features/im"
	"github.com/hedeqiang/crewbase/features/im/imtest"
)

// Listening in: the program filter drops what the agent never sees; the
// agent's NO_REPLY posts nothing; a real reply goes under the message and
// binds its thread; listened-in turns are read-only.
func TestListenInOnTheChannel(t *testing.T) {
	e := imtest.New(t)
	a := e.Agent("Kai")
	f, g := start(t, e, map[string]any{"relayReplies": true, "listen": map[string]any{"agent": a.ID(), "keywords": []string{"bug", "报错"}}})
	a.Pause()
	msg := func(id, user, chat, text string) {
		g.Message(context.Background(), im.Inbound{User: im.User{ID: user}, Chat: chat, MessageID: id, Text: text})
	}
	msg("m1", "u-ada", "C1", "午饭吃什么好呢大家")        // no keyword
	msg("m2", "u-ada", "C1", "bug")                    // too short
	msg("m3", "stranger", "C1", "登录页有个 bug 一直报错啊") // not an allowed user
	msg("m4", "u-ada", "C9", "登录页有个 bug 一直报错啊")    // another chat
	if q := a.Queue(); len(q) != 0 {
		t.Fatalf("passed the filter: %+v", q)
	}
	msg("m5", "u-ada", "C1", "登录页有个 bug，点提交一直报错 500")
	q := a.Queue()
	if len(q) != 1 || q[0].Mode != agent.ModeReadOnly || !strings.Contains(q[0].Text, "you were not addressed") || !strings.Contains(q[0].Text, "报错 500") {
		t.Fatalf("listened-in input: %+v", q)
	}
	if len(f.Posts()) != 0 {
		t.Fatal("posted something while listening")
	}

	// the agent has nothing to add
	e.EndTurn(a, q[0], im.NoReply)
	a.Cancel(q[0].ID)
	imtest.Eventually(t, func() bool { return true })
	if len(f.Posts()) != 0 {
		t.Fatalf("NO_REPLY was posted: %+v", f.Posts())
	}
	// a reply in m5's thread is not routed to the agent (it stayed quiet)
	msg("m6", "u-ada", "C1", "有人看下吗")
	if len(a.Queue()) != 0 {
		t.Fatalf("thread bound although the agent stayed quiet: %+v", a.Queue())
	}

	// the agent speaks up: posted under the message, thread now bound
	msg("m7", "u-ada", "C1", "构建一直报错：module not found @/utils")
	q = a.Queue()
	e.EndTurn(a, q[len(q)-1], "看起来是 tsconfig 里的 paths 没配 @/*，我可以帮你加上。")
	imtest.Eventually(t, func() bool { return len(f.Posts()) == 1 })
	if p := f.Posts()[0].Msg; p.ReplyTo != "m7" || !strings.Contains(p.Body, "tsconfig") {
		t.Fatalf("reply: %+v", p)
	}
	before := len(a.Queue())
	g.Message(context.Background(), im.Inbound{User: im.User{ID: "u-ada"}, Chat: "C1", MessageID: "m8", Thread: "m7", Text: "好的，帮我加"})
	imtest.Eventually(t, func() bool { return len(a.Queue()) == before+1 })
	if last := a.Queue()[len(a.Queue())-1]; last.Mode != "" || last.Text != "好的，帮我加" {
		t.Fatalf("thread reply: %+v", last)
	}
	_ = agentloop.TurnEndEvent{}
}
