package im_test

import (
	"context"
	"github.com/hedeqiang/crewbase/core/session"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/capabilities/notify"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/features/im"
	"github.com/hedeqiang/crewbase/features/im/imtest"
	"github.com/hedeqiang/crewbase/kernel"
)

func start(t *testing.T, e *imtest.Env, cfg map[string]any) (*imtest.Fake, *im.Gateway) {
	t.Helper()
	f := imtest.NewFake()
	if cfg == nil {
		cfg = map[string]any{}
	}
	if cfg["channel"] == nil {
		cfg["channel"] = "C1"
	}
	if cfg["allowUsers"] == nil {
		cfg["allowUsers"] = []string{"u-ada"}
	}
	if _, err := e.Host.Load(imtest.Plugin("fake", f), kernel.LoadOptions{Config: cfg}); err != nil {
		t.Fatal(err)
	}
	g, ok := kernel.Lookup[*im.Gateway](e.Host.Root(), im.ServicePrefix+"fake")
	if !ok {
		p, _ := e.Host.Plugin("imtest-fake")
		t.Fatalf("gateway not provided: %+v", p)
	}
	f.Events(t)
	return f, g
}

func TestQuietHours(t *testing.T) {
	q := &im.QuietHours{From: "22:00", To: "08:00", TZ: "UTC"}
	at := func(h, m int) time.Time { return time.Date(2026, 1, 1, h, m, 0, 0, time.UTC) }
	for _, c := range []struct {
		t    time.Time
		want bool
	}{{at(23, 0), true}, {at(2, 0), true}, {at(8, 0), false}, {at(12, 0), false}, {at(22, 0), true}} {
		if q.Active(c.t) != c.want {
			t.Errorf("%v: want %v", c.t, c.want)
		}
	}
	day := &im.QuietHours{From: "12:00", To: "13:00"}
	if !day.Active(time.Date(2026, 1, 1, 12, 30, 0, 0, time.Local)) || day.Active(time.Date(2026, 1, 1, 13, 30, 0, 0, time.Local)) {
		t.Fatal("same-day window")
	}
	var none *im.QuietHours
	if none.Active(time.Now()) {
		t.Fatal("nil quiet hours")
	}
}

func TestParseMention(t *testing.T) {
	for in, want := range map[string][2]string{
		"@kai hello there": {"kai", "hello there"},
		"  @Kai: hi":       {"Kai", "hi"},
		"@kai\nmultiline":  {"kai", "multiline"},
		"@kai":             {"kai", ""},
	} {
		n, r, ok := im.ParseMention(in)
		if !ok || n != want[0] || r != want[1] {
			t.Errorf("%q → %q %q %v", in, n, r, ok)
		}
	}
	if _, _, ok := im.ParseMention("hello @kai"); ok {
		t.Fatal("mention must lead")
	}
}

func TestApprovalCardButtons(t *testing.T) {
	c := im.ApprovalCard(approval.Request{Agent: "kai", Category: "exec", Summary: "run make"}, "Kai", "tok")
	if len(c.Buttons) != 3 || c.Buttons[0].Label != im.LabelAllow || c.Buttons[2].Label != im.LabelLater {
		t.Fatalf("%+v", c.Buttons)
	}
	for _, cat := range []string{"push", "payment"} {
		c = im.ApprovalCard(approval.Request{Agent: "kai", Category: cat}, "Kai", "tok")
		if len(c.Buttons) != 2 || c.Buttons[0].Label != im.LabelDeny || c.Buttons[1].Label != im.LabelLater {
			t.Fatalf("%s: %+v", cat, c.Buttons)
		}
	}
	for _, b := range c.Buttons {
		if strings.Contains(b.Label, "以后都允许") || len(b.Value) > 64 {
			t.Fatal(b)
		}
	}
}

func TestNotificationsQuietHoursAndDedup(t *testing.T) {
	e := imtest.New(t)
	now := time.Now().UTC()
	from := now.Add(-time.Hour).Format("15:04")
	to := now.Add(time.Hour).Format("15:04")
	f, _ := start(t, e, map[string]any{"quietHours": map[string]any{"from": from, "to": to, "tz": "UTC"}})

	e.Notify.Send(context.Background(), notify.Notification{Kind: notify.KindTaskDone, Title: "done"})
	if len(f.Posts()) != 0 {
		t.Fatal("non-urgent notification must respect quiet hours")
	}
	e.Notify.Send(context.Background(), notify.Notification{Kind: notify.KindFailed, Title: "urgent thing", Urgent: true})
	if p := f.Posts(); len(p) != 1 || p[0].Msg.Title != "urgent thing" || p[0].Ref.Chat != "C1" {
		t.Fatalf("%+v", p)
	}

	// A needs-you notification for a request that already has a card is
	// not posted twice.
	kai := e.Agent("Kai")
	done := e.RunTool(kai, "deploy")
	e.WaitPending(1)
	imtest.Eventually(t, func() bool { return len(f.Posts()) == 2 })
	ss, _ := kai.Session()
	e.Notify.Send(context.Background(), notify.Notification{Kind: notify.KindNeedsYou, Urgent: true, Agent: kai.ID(), Session: ss.ID(), Title: "Kai 等你批准"})
	if len(f.Posts()) != 2 {
		t.Fatalf("needs-you duplicated the card: %+v", f.Posts())
	}
	// Decided on the desktop: the IM card is updated with the outcome.
	req := e.Inbox.Pending()[0]
	e.Inbox.Decide(req.CallID, approval.Decision{Outcome: approval.OutcomeRejected, By: "desktop"})
	if exe := imtest.Recv(t, done); exe.Outcome != approval.OutcomeRejected {
		t.Fatal(exe.Outcome)
	}
	imtest.Eventually(t, func() bool {
		for _, ed := range f.Edits() {
			if strings.Contains(ed.Msg.Footer, "已在其他端拒绝") && len(ed.Msg.Buttons) == 0 {
				return true
			}
		}
		return false
	})
}

func TestNotifyKindsAndStatus(t *testing.T) {
	e := imtest.New(t)
	e.Creds.Set("mem:fake", "SECRET-TOKEN-123456")
	f, g := start(t, e, map[string]any{"notifyKinds": []string{notify.KindFailed}, "botTokenRef": "mem:fake"})
	e.Notify.Send(context.Background(), notify.Notification{Kind: notify.KindTaskDone, Title: "done"})
	e.Notify.Send(context.Background(), notify.Notification{Kind: notify.KindFailed, Title: "failed"})
	if p := f.Posts(); len(p) != 1 || p[0].Msg.Title != "failed" {
		t.Fatalf("%+v", p)
	}
	if _, err := g.Resolve(context.Background(), "mem:fake"); err != nil {
		t.Fatal(err)
	}
	g.Connected(false, context.DeadlineExceeded)
	g.Connected(false, errString("dial https://x/botSECRET-TOKEN-123456/getMe: refused"))
	st := g.Status()
	if st.Connected || strings.Contains(st.Error, "SECRET") || !strings.Contains(st.Error, "***") {
		t.Fatalf("%+v", st)
	}
	if strings.Contains(e.Logs.String(), "SECRET-TOKEN") {
		t.Fatal("token leaked into logs")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestNoApproverWithoutAllowedUsers(t *testing.T) {
	e := imtest.New(t)
	f, _ := start(t, e, map[string]any{"allowUsers": []string{}})
	ev := f.Events(t)
	kai := e.Agent("Kai")
	done := e.RunTool(kai, "deploy")
	e.WaitPending(1)
	imtest.Eventually(t, func() bool { return len(f.Posts()) == 1 })
	card := f.Posts()[0]
	if toast := ev.Action(context.Background(), im.Action{User: im.User{ID: "u-ada"}, Value: card.Msg.Buttons[0].Value}); !strings.Contains(toast, "没有权限") {
		t.Fatal(toast)
	}
	if len(e.Inbox.Pending()) != 1 {
		t.Fatal("still pending")
	}
	e.Inbox.Decide(e.Inbox.Pending()[0].CallID, approval.Decision{Outcome: approval.OutcomeAllowedOnce})
	if exe := imtest.Recv(t, done); exe.Outcome != approval.OutcomeAllowedOnce {
		t.Fatal(exe.Outcome)
	}
}

func TestAPI(t *testing.T) {
	e := imtest.New(t)
	if _, err := e.Host.Load(gateway.Plugin, kernel.LoadOptions{Config: map[string]any{"addr": "127.0.0.1:0", "token": "desk-token-0123456789"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Host.Load(im.APIPlugin, kernel.LoadOptions{}); err != nil {
		t.Fatal(err)
	}
	e.Creds.Set("mem:fake", "SECRET-BOT-TOKEN-xyz")
	f, _ := start(t, e, map[string]any{"botTokenRef": "mem:fake"})
	gw := kernel.Use[*gateway.Gateway](e.Host.Root(), gateway.ServiceKey)
	do := func(method, path string) (int, string) {
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Authorization", "Bearer desk-token-0123456789")
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	code, body := do("GET", "/api/im")
	if code != 200 || !strings.Contains(body, `"name":"fake"`) || !strings.Contains(body, `"connected":true`) || strings.Contains(body, "SECRET") || strings.Contains(body, "mem:fake") {
		t.Fatal(code, body)
	}
	if code, body = do("POST", "/api/im/fake/test"); code != 200 || !strings.Contains(body, `"ok":true`) || len(f.Posts()) != 1 {
		t.Fatal(code, body)
	}
	if code, _ = do("POST", "/api/im/nope/test"); code != 404 {
		t.Fatal(code)
	}
}

func TestSplitReply(t *testing.T) {
	if p := im.SplitReply("短消息", 100, 4); len(p) != 1 || p[0] != "短消息" {
		t.Fatalf("%q", p)
	}
	long := strings.Repeat("第一段内容。", 30) + "\n\n" + strings.Repeat("第二段内容。", 30)
	p := im.SplitReply(long, 200, 4)
	if len(p) < 2 || !strings.HasSuffix(p[0], "（1/"+strconv.Itoa(len(p))+"）") {
		t.Fatalf("%d parts: %q", len(p), p)
	}
	for _, x := range p {
		if n := utf8.RuneCountInString(x); n > 220 {
			t.Fatalf("part too long: %d", n)
		}
		if !strings.Contains(x, "。") {
			t.Fatalf("cut mid-sentence: %q", x)
		}
	}
	// code fences stay balanced
	code := "看代码：\n```go\n" + strings.Repeat("fmt.Println(1)\n", 40) + "```\n完"
	for _, x := range im.SplitReply(code, 200, 6) {
		if strings.Count(x, "```")%2 != 0 {
			t.Fatalf("unbalanced fence: %q", x)
		}
	}
	// too long: last part points to the desktop
	p = im.SplitReply(strings.Repeat("字", 2000), 200, 3)
	if len(p) != 3 || !strings.Contains(p[2], "完整回复请在桌面查看") {
		t.Fatalf("%d %q", len(p), p[len(p)-1])
	}
}

// Thread bindings survive a restart: a reply in an old thread still
// reaches the same agent and conversation.
func TestBindingsPersist(t *testing.T) {
	dir := t.TempDir()
	e := imtest.New(t)
	a := e.Agent("Kai")
	_, g := start(t, e, map[string]any{"stateDir": dir, "relayReplies": true})
	g.Message(context.Background(), im.Inbound{User: im.User{ID: "u-ada"}, Chat: "C1", MessageID: "m1", Text: "@kai 你好", Direct: true})
	imtest.Eventually(t, func() bool { return len(a.Queue()) > 0 || a.CurrentInput() != "" })
	imtest.Eventually(t, func() bool {
		b, err := os.ReadFile(filepath.Join(dir, "im-fake-threads.json"))
		return err == nil && strings.Contains(string(b), `"m1"`)
	})
	// a new gateway on the same state (a restart) loads the binding: a reply
	// in the old thread goes to the same agent and conversation
	saved, _ := os.ReadFile(filepath.Join(dir, "im-fake-threads.json"))
	if !strings.Contains(string(saved), a.Spec().Session) || !strings.Contains(string(saved), `"agent":"`+a.ID()+`"`) {
		t.Fatalf("saved %s", saved)
	}
	if !e.Host.Unload("imtest-fake") {
		t.Fatal("unload")
	}
	_ = g
	_, g2 := start(t, e, map[string]any{"stateDir": dir, "relayReplies": true})
	g2.Message(context.Background(), im.Inbound{User: im.User{ID: "u-ada"}, Chat: "C1", MessageID: "m2", Thread: "m1", Text: "接着说"})
	ss, _ := a.Session()
	imtest.Eventually(t, func() bool {
		for _, ev := range ss.Events() {
			if ev.Type == session.TypeInputQueued && strings.Contains(string(ev.Data), "接着说") {
				return true
			}
		}
		return false
	})
}
