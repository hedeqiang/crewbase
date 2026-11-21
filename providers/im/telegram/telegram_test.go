package telegram_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/features/im"
	"github.com/hedeqiang/crewbase/features/im/imtest"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/im/telegram"
)

const token = "123456:SECRET-telegram-token-xyz"

// badToken is rejected by the fake server.
const badToken = "999:WRONG-SECRET-token"

// fakeBot is a fake Telegram Bot API server.
type fakeBot struct {
	t       *testing.T
	mu      sync.Mutex
	updates []map[string]any
	nextUID int
	nextMID int
	wake    chan struct{}
	sent    []map[string]any
	edits   []map[string]any
	answers []map[string]any
	badAuth int
}

func newFakeBot(t *testing.T) (*fakeBot, *httptest.Server) {
	f := &fakeBot{t: t, wake: make(chan struct{}, 1), nextMID: 100}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeBot) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	prefix := "/bot" + token + "/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		f.mu.Lock()
		f.badAuth++
		f.mu.Unlock()
		w.WriteHeader(401)
		io.WriteString(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
		return
	}
	method := strings.TrimPrefix(r.URL.Path, prefix)
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	reply := func(v any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": v})
	}
	switch method {
	case "getMe":
		reply(map[string]any{"id": 999, "is_bot": true, "first_name": "Crew", "username": "crew_bot"})
	case "getUpdates":
		offset := int(body["offset"].(float64))
		deadline := time.After(time.Duration(body["timeout"].(float64)) * time.Second)
		for {
			f.mu.Lock()
			var out []map[string]any
			for _, u := range f.updates {
				if int(u["update_id"].(int)) >= offset {
					out = append(out, u)
				}
			}
			f.mu.Unlock()
			if len(out) > 0 {
				reply(out)
				return
			}
			select {
			case <-f.wake:
			case <-deadline:
				reply([]any{})
				return
			case <-r.Context().Done():
				return
			}
		}
	case "sendMessage":
		f.mu.Lock()
		f.nextMID++
		id := f.nextMID
		f.sent = append(f.sent, body)
		f.mu.Unlock()
		chat, _ := strconv.ParseInt(body["chat_id"].(string), 10, 64)
		reply(map[string]any{"message_id": id, "chat": map[string]any{"id": chat, "type": "private"}, "text": body["text"]})
	case "editMessageText":
		f.mu.Lock()
		f.edits = append(f.edits, body)
		f.mu.Unlock()
		reply(true)
	case "answerCallbackQuery":
		f.mu.Lock()
		f.answers = append(f.answers, body)
		f.mu.Unlock()
		reply(true)
	default:
		w.WriteHeader(404)
		io.WriteString(w, `{"ok":false,"error_code":404,"description":"Not Found"}`)
	}
}

func (f *fakeBot) push(u map[string]any) {
	f.mu.Lock()
	f.nextUID++
	u["update_id"] = f.nextUID
	f.updates = append(f.updates, u)
	f.mu.Unlock()
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

func (f *fakeBot) Sent() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.sent...)
}

func (f *fakeBot) Edits() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.edits...)
}

func (f *fakeBot) Answers() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.answers...)
}

func (f *fakeBot) press(user int, msgID any, data string) {
	f.push(map[string]any{"callback_query": map[string]any{
		"id": "cb" + strconv.Itoa(f.nextUID), "from": map[string]any{"id": user, "first_name": "User" + strconv.Itoa(user)},
		"message": map[string]any{"message_id": msgID, "chat": map[string]any{"id": 42, "type": "private"}},
		"data":    data,
	}})
}

func (f *fakeBot) say(user, msgID int, text string, replyTo int) {
	m := map[string]any{"message_id": msgID, "from": map[string]any{"id": user, "first_name": "Ada", "last_name": "L"},
		"chat": map[string]any{"id": 42, "type": "private"}, "text": text}
	if replyTo > 0 {
		m["reply_to_message"] = map[string]any{"message_id": replyTo, "from": map[string]any{"id": 999, "is_bot": true}}
	}
	f.push(map[string]any{"message": m})
}

func keyboard(m map[string]any) []map[string]any {
	rm, _ := m["reply_markup"].(map[string]any)
	if rm == nil {
		return nil
	}
	rows, _ := rm["inline_keyboard"].([]any)
	var out []map[string]any
	for _, r := range rows {
		for _, b := range r.([]any) {
			out = append(out, b.(map[string]any))
		}
	}
	return out
}

func button(t *testing.T, m map[string]any, label string) string {
	t.Helper()
	for _, b := range keyboard(m) {
		if strings.Contains(b["text"].(string), label) {
			return b["callback_data"].(string)
		}
	}
	t.Fatalf("no %q button in %v", label, m)
	return ""
}

func setup(t *testing.T, relay bool) (*imtest.Env, *fakeBot) {
	t.Helper()
	e := imtest.New(t)
	bot, srv := newFakeBot(t)
	if err := e.Creds.Set("mem:telegram-bot", token); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{
		"botTokenRef": "mem:telegram-bot", "channel": "42", "allowUsers": []string{"7"},
		"relayReplies": relay, "apiBase": srv.URL, "pollTimeout": 1,
	}
	if _, err := e.Host.Load(telegram.Plugin, kernel.LoadOptions{Config: cfg}); err != nil {
		t.Fatal(err)
	}
	g, ok := kernel.Lookup[*im.Gateway](e.Host.Root(), im.ServicePrefix+"telegram")
	if !ok {
		p, _ := e.Host.Plugin(telegram.Plugin.Name)
		t.Fatalf("%+v", p)
	}
	imtest.Eventually(t, func() bool { return g.Status().Connected })
	t.Cleanup(func() {
		if s := e.Logs.String(); strings.Contains(s, token) {
			t.Errorf("token leaked into logs:\n%s", s)
		}
	})
	return e, bot
}

func lastSent(t *testing.T, bot *fakeBot, n int) map[string]any {
	t.Helper()
	imtest.Eventually(t, func() bool { return len(bot.Sent()) >= n })
	return bot.Sent()[n-1]
}

func TestApprovalAllowedFromTelegram(t *testing.T) {
	e, bot := setup(t, false)
	kai := e.Agent("Kai")
	done := e.RunTool(kai, "deploy")
	e.WaitPending(1) // the desktop inbox sees it too
	card := lastSent(t, bot, 1)
	if !strings.Contains(card["text"].(string), "Kai 等你批准") || len(keyboard(card)) != 3 {
		t.Fatalf("%v", card)
	}
	allow := button(t, card, im.LabelAllow)

	// A press by a user who is not on the allowlist is ignored.
	bot.press(8, 101, allow)
	imtest.Eventually(t, func() bool { return len(bot.Answers()) == 1 })
	if !strings.Contains(bot.Answers()[0]["text"].(string), "没有权限") {
		t.Fatal(bot.Answers())
	}
	select {
	case <-done:
		t.Fatal("non-allowed press must not decide")
	case <-time.After(100 * time.Millisecond):
	}

	bot.press(7, 101, allow)
	exe := imtest.Recv(t, done)
	if exe.Outcome != approval.OutcomeAllowedOnce || exe.Result.Content != "deploy done" {
		t.Fatalf("%+v", exe)
	}
	// decided by im:telegram:7, recorded in the session
	ss, _ := kai.Session()
	var by string
	for _, ev := range ss.Events() {
		if ev.Type == "approval/decided" {
			var d approval.DecidedData
			ev.Decode(&d)
			by = d.By
		}
	}
	if by != "im:telegram:7" {
		t.Fatalf("by=%q", by)
	}
	imtest.Eventually(t, func() bool {
		ed := bot.Edits()
		return len(ed) == 1 && strings.Contains(ed[0]["text"].(string), "已批准") && len(keyboard(ed[0])) == 0
	})
	// the desktop card is withdrawn once the decision has gone through
	imtest.Eventually(t, func() bool { return len(e.Inbox.Pending()) == 0 })
	// a second press is told it was already handled
	bot.press(7, 101, allow)
	imtest.Eventually(t, func() bool { return len(bot.Answers()) == 3 })
	if !strings.Contains(bot.Answers()[2]["text"].(string), "已经处理") {
		t.Fatal(bot.Answers())
	}
}

func TestPushHasNoApproveAndLaterLeavesDesktop(t *testing.T) {
	e, bot := setup(t, false)
	kai := e.Agent("Kai")
	done := e.RunTool(kai, "git_push")
	e.WaitPending(1)
	card := lastSent(t, bot, 1)
	for _, b := range keyboard(card) {
		if strings.Contains(b["text"].(string), im.LabelAllow) {
			t.Fatal("push approvals must not offer approve in IM")
		}
	}
	// a forged allow payload is refused server-side
	later := button(t, card, im.LabelLater)
	forged := strings.TrimSuffix(later, im.VerbLater) + im.VerbAllow
	bot.press(7, 101, forged)
	imtest.Eventually(t, func() bool { return len(bot.Answers()) == 1 })
	if !strings.Contains(bot.Answers()[0]["text"].(string), "只能在桌面") {
		t.Fatal(bot.Answers())
	}

	bot.press(7, 101, later)
	imtest.Eventually(t, func() bool { return len(bot.Answers()) == 2 })
	imtest.Eventually(t, func() bool {
		for _, ed := range bot.Edits() {
			if strings.Contains(ed["text"].(string), "稍后在桌面处理") {
				return true
			}
		}
		return false
	})
	select {
	case <-done:
		t.Fatal("later must leave the decision to the desktop")
	case <-time.After(100 * time.Millisecond):
	}
	p := e.WaitPending(1)
	e.Inbox.Decide(p[0].CallID, approval.Decision{Outcome: approval.OutcomeAllowedOnce, By: "desktop"})
	if exe := imtest.Recv(t, done); exe.Outcome != approval.OutcomeAllowedOnce {
		t.Fatal(exe.Outcome)
	}
	// the card shows the final outcome
	imtest.Eventually(t, func() bool {
		for _, ed := range bot.Edits() {
			if strings.Contains(ed["text"].(string), "已在其他端批准") {
				return true
			}
		}
		return false
	})
}

func TestRejectFromTelegram(t *testing.T) {
	e, bot := setup(t, false)
	kai := e.Agent("Kai")
	done := e.RunTool(kai, "deploy")
	card := lastSent(t, bot, 1)
	bot.press(7, 101, button(t, card, im.LabelDeny))
	exe := imtest.Recv(t, done)
	if exe.Outcome != approval.OutcomeRejected || !strings.Contains(exe.Result.Content, "rejected in telegram") {
		t.Fatalf("%+v", exe)
	}
}

func TestInboundMentionAndRelay(t *testing.T) {
	e, bot := setup(t, true)
	kai := e.Agent("Kai")
	queued := make(chan agent.Input, 4)
	agent.QueueChanged.Listen(e.Host.Root(), func(id string) {
		if id == kai.ID() {
			for _, in := range kai.Queue() {
				queued <- in
			}
		}
	})

	// unknown user: polite refusal, nothing enqueued
	bot.push(map[string]any{"message": map[string]any{"message_id": 5, "from": map[string]any{"id": 66, "first_name": "Eve"},
		"chat": map[string]any{"id": 42, "type": "private"}, "text": "@kai rm -rf"}})
	refusal := lastSent(t, bot, 1)
	if !strings.Contains(refusal["text"].(string), "抱歉") || len(kai.Queue()) != 0 {
		t.Fatalf("%v %v", refusal, kai.Queue())
	}

	bot.say(7, 10, "@kai hello", 0)
	in := imtest.Recv(t, queued)
	if in.Text != "hello" || in.From != "Ada L" || !strings.HasPrefix(in.Tag, "im:telegram:") {
		t.Fatalf("%+v", in)
	}
	ack := lastSent(t, bot, 2)
	if !strings.Contains(ack["text"].(string), "已转给 Kai") {
		t.Fatal(ack)
	}
	kai.Claim()

	// The agent's reply goes back to the same thread (reply to message 10).
	e.EndTurn(kai, in, "Hi Ada, all good.")
	reply := lastSent(t, bot, 3)
	rp, _ := reply["reply_parameters"].(map[string]any)
	if !strings.Contains(reply["text"].(string), "Hi Ada, all good.") || rp == nil || rp["message_id"].(float64) != 10 {
		t.Fatalf("%v", reply)
	}

	// Replying to the bot's message continues the conversation with Kai
	// (once the gateway has the reply's id; a person is never that fast).
	replyID := 100 + 3 // fake server ids: 101 refusal, 102 ack, 103 reply
	g, _ := kernel.Lookup[*im.Gateway](e.Host.Root(), im.ServicePrefix+"telegram")
	imtest.Eventually(t, func() bool { return g.Bound("42", strconv.Itoa(replyID)) })
	bot.say(7, 11, "thanks, now deploy", replyID)
	in2 := imtest.Recv(t, queued)
	if in2.Text != "thanks, now deploy" || in2.Session != in.Session {
		t.Fatalf("%+v", in2)
	}
}

func TestBadTokenNeverLeaks(t *testing.T) {
	e := imtest.New(t)
	_, srv := newFakeBot(t)
	e.Creds.Set("mem:tg", badToken)
	cfg := map[string]any{"botTokenRef": "mem:tg", "channel": "42", "allowUsers": []string{"7"}, "apiBase": srv.URL}
	if _, err := e.Host.Load(telegram.Plugin, kernel.LoadOptions{Config: cfg}); err != nil {
		t.Fatal(err)
	}
	g, _ := kernel.Lookup[*im.Gateway](e.Host.Root(), im.ServicePrefix+"telegram")
	imtest.Eventually(t, func() bool { return g.Status().Error != "" })
	if _, err := g.Test(context.Background()); err == nil || strings.Contains(err.Error(), badToken) {
		t.Fatalf("err=%v", err)
	}
	st := g.Status()
	b, _ := json.Marshal(st)
	if strings.Contains(string(b), badToken) || !strings.Contains(st.Error, "Unauthorized") {
		t.Fatal(string(b))
	}
	// connection-level error: the URL (with the token) is never in the error
	srv.Close()
	e.Host.Unload(telegram.Plugin.Name)
	if _, err := e.Host.Load(telegram.Plugin, kernel.LoadOptions{Config: cfg}); err != nil {
		t.Fatal(err)
	}
	g, _ = kernel.Lookup[*im.Gateway](e.Host.Root(), im.ServicePrefix+"telegram")
	imtest.Eventually(t, func() bool { return g.Status().Error != "" })
	if s := g.Status().Error; strings.Contains(s, badToken) || strings.Contains(s, "/bot") {
		t.Fatal(s)
	}
	if strings.Contains(e.Logs.String(), badToken) {
		t.Fatal("token in logs")
	}
}

func TestUnloadDisconnects(t *testing.T) {
	e, _ := setup(t, false)
	done := make(chan struct{})
	go func() { e.Host.Unload(telegram.Plugin.Name); close(done) }()
	imtest.Recv(t, (<-chan struct{})(done))
	if _, ok := kernel.Lookup[*im.Gateway](e.Host.Root(), im.ServicePrefix+"telegram"); ok {
		t.Fatal("gateway still provided")
	}
}
