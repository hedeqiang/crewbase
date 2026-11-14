package feishu_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/features/im"
	"github.com/hedeqiang/crewbase/features/im/imtest"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/im/feishu"
)

func sp(s string) *string { return &s }

func msgEvent(user, chatType, text, parent string, mentions ...*larkim.MentionEvent) *larkim.P2MessageReceiveV1 {
	c, _ := json.Marshal(map[string]string{"text": text})
	return &larkim.P2MessageReceiveV1{EventV2Base: &larkevent.EventV2Base{}, Event: &larkim.P2MessageReceiveV1Data{
		Sender: &larkim.EventSender{SenderId: &larkim.UserId{OpenId: sp(user)}, SenderType: sp("user")},
		Message: &larkim.EventMessage{MessageId: sp("om_" + strconv.Itoa(len(text))), ChatId: sp("oc_1"), ChatType: sp(chatType),
			MessageType: sp("text"), Content: sp(string(c)), ParentId: sp(parent), Mentions: mentions},
	}}
}

func TestCardAndConversions(t *testing.T) {
	card := feishu.Card(im.ApprovalCard(approval.Request{Agent: "kai", Category: "exec", Summary: "deploy"}, "Kai", "tok"))
	for _, want := range []string{`"update_multi":true`, "Kai 等你批准", `"tag":"action"`, `"v":"ap:tok:allow"`, `"type":"danger"`, im.LabelLater} {
		if !strings.Contains(card, want) {
			t.Errorf("missing %s in %s", want, card)
		}
	}
	if c := feishu.Card(im.ApprovalCard(approval.Request{Category: "push"}, "Kai", "tok")); strings.Contains(c, "ap:tok:allow") {
		t.Fatal("push card offers approve")
	}

	in, ok := feishu.Inbound(msgEvent("ou_7", "group", "@_user_1 @kai hi", "",
		&larkim.MentionEvent{Key: sp("@_user_1"), MentionedType: sp("bot"), Name: sp("Crew")}))
	if !ok || !in.Direct || in.Text != "@kai hi" || in.User.ID != "ou_7" || in.Chat != "oc_1" {
		t.Fatalf("%+v", in)
	}
	in, _ = feishu.Inbound(msgEvent("ou_7", "group", "ping @_user_1", "",
		&larkim.MentionEvent{Key: sp("@_user_1"), MentionedType: sp("user"), Name: sp("Bob")}))
	if in.Text != "ping @Bob" || in.Direct {
		t.Fatalf("%+v", in)
	}
	bot := msgEvent("x", "p2p", "x", "")
	bot.Event.Sender.SenderType = sp("app")
	if _, ok := feishu.Inbound(bot); ok {
		t.Fatal("app messages dropped")
	}

	a, ok := feishu.CardAction(&callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "ou_7"}, Action: &callback.CallBackAction{Value: map[string]any{"v": "ap:t:deny"}},
		Context: &callback.Context{OpenChatID: "oc_1", OpenMessageID: "om_9"}}})
	if !ok || a.Value != "ap:t:deny" || a.User.ID != "ou_7" || a.Ref.ID != "om_9" {
		t.Fatalf("%+v", a)
	}
}

type fakeAPI struct {
	mu      sync.Mutex
	created []string
	replies [][2]string
	patches [][2]string
	seq     int
}

func (f *fakeAPI) Create(_ context.Context, chat, _, content string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	f.created = append(f.created, content)
	return "om_c" + strconv.Itoa(f.seq), nil
}

func (f *fakeAPI) Reply(_ context.Context, id, _, content string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	f.replies = append(f.replies, [2]string{id, content})
	return "om_r" + strconv.Itoa(f.seq), nil
}

func (f *fakeAPI) Patch(_ context.Context, id, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.patches = append(f.patches, [2]string{id, content})
	return nil
}

func (f *fakeAPI) snap() ([]string, [][2]string, [][2]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.created...), append([][2]string(nil), f.replies...), append([][2]string(nil), f.patches...)
}

type offline struct {
	*feishu.Transport
	ev chan im.Events
}

func (o offline) Run(ctx context.Context, ev im.Events) error {
	ev.Connected(true, nil)
	o.ev <- ev
	<-ctx.Done()
	return nil
}

func TestFeishuApprovalAndChat(t *testing.T) {
	e := imtest.New(t)
	api := &fakeAPI{}
	evs := make(chan im.Events, 1)
	spec := kernel.Spec{Name: "feishu-test", Apply: func(c *kernel.Context) error {
		cfg := feishu.Config{AppID: "cli_x", Config: im.Config{Channel: "oc_1", AllowUsers: []string{"ou_7"}, RelayReplies: true}}
		_, err := im.Start(c, "feishu", cfg.Config, func(g *im.Gateway) (im.Transport, error) {
			tr := feishu.New(g, cfg)
			tr.SetAPI(api)
			return offline{tr, evs}, nil
		})
		return err
	}}
	if _, err := e.Host.Load(spec, kernel.LoadOptions{}); err != nil {
		t.Fatal(err)
	}
	ev := imtest.Recv(t, (<-chan im.Events)(evs))
	kai := e.Agent("Kai")

	done := e.RunTool(kai, "git_push")
	imtest.Eventually(t, func() bool { c, _, _ := api.snap(); return len(c) == 1 })
	c, _, _ := api.snap()
	if strings.Contains(c[0], ":allow") {
		t.Fatal("push card offers approve")
	}
	var card struct {
		Elements []struct {
			Tag     string `json:"tag"`
			Actions []struct {
				Value map[string]string `json:"value"`
			} `json:"actions"`
		} `json:"elements"`
	}
	json.Unmarshal([]byte(c[0]), &card)
	var deny string
	for _, el := range card.Elements {
		if el.Tag == "action" {
			deny = el.Actions[0].Value["v"]
		}
	}
	if toast := ev.Action(context.Background(), im.Action{User: im.User{ID: "ou_x"}, Value: deny}); !strings.Contains(toast, "没有权限") {
		t.Fatal(toast)
	}
	ev.Action(context.Background(), im.Action{User: im.User{ID: "ou_7"}, Value: deny})
	if exe := imtest.Recv(t, done); exe.Outcome != approval.OutcomeRejected {
		t.Fatal(exe.Outcome)
	}
	imtest.Eventually(t, func() bool { _, _, p := api.snap(); return len(p) == 1 && strings.Contains(p[0][1], "已拒绝") })

	queued := make(chan agent.Input, 2)
	agent.QueueChanged.Listen(e.Host.Root(), func(string) {
		for _, in := range kai.Queue() {
			queued <- in
		}
	})
	in, _ := feishu.Inbound(msgEvent("ou_7", "p2p", "@kai 早上好", ""))
	ev.Message(context.Background(), in)
	q := imtest.Recv(t, queued)
	if q.Text != "早上好" || q.From != "ou_7" {
		t.Fatalf("%+v", q)
	}
	kai.Claim()
	e.EndTurn(kai, q, "早，今天一切正常")
	imtest.Eventually(t, func() bool {
		_, r, _ := api.snap()
		return len(r) == 2 && r[1][0] == in.MessageID && strings.Contains(r[1][1], "今天一切正常")
	})
}

// TestSDKAPI exercises the real SDK wrapper against a fake OpenAPI server.
func TestSDKAPI(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "tenant_access_token"):
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "tenant_access_token": "t-abc", "expire": 7200})
		case r.URL.Path == "/open-apis/im/v1/messages/om_bad":
			json.NewEncoder(w).Encode(map[string]any{"code": 230001, "msg": "bad message"})
		default:
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"message_id": "om_new"}})
		}
	}))
	defer srv.Close()
	e := imtest.New(t)
	var g *im.Gateway
	e.Host.Load(kernel.Spec{Name: "g", Apply: func(c *kernel.Context) error {
		g = im.New(c, "feishu", im.Config{})
		return nil
	}}, kernel.LoadOptions{})
	api := feishu.NewAPI(g, lark.NewClient("cli_x", "app-secret-xyz", lark.WithOpenBaseUrl(srv.URL)))
	if id, err := api.Create(context.Background(), "oc_1", "interactive", "{}"); err != nil || id != "om_new" {
		t.Fatal(id, err)
	}
	if id, err := api.Reply(context.Background(), "om_1", "interactive", "{}"); err != nil || id != "om_new" {
		t.Fatal(id, err)
	}
	if err := api.Patch(context.Background(), "om_bad", "{}"); err == nil || !strings.Contains(err.Error(), "230001") {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	all := strings.Join(paths, "\n")
	for _, want := range []string{"POST /open-apis/im/v1/messages?receive_id_type=chat_id", "POST /open-apis/im/v1/messages/om_1/reply", "PATCH /open-apis/im/v1/messages/om_bad"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %s in\n%s", want, all)
		}
	}
}
