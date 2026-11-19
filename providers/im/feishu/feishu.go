// Package feishu is the Feishu / Lark IM gateway ("@crewbase/im-feishu").
// Events and card callbacks arrive over the SDK's long connection
// (长连接 websocket), so no public request URL is needed (docs/06 §8).
//
// Config (see im.Config):
//
//	appId:       cli_xxx                         # not secret
//	botTokenRef: vault:crewbase/feishu-app    # the app secret
//	channel:     oc_xxx                          # chat id for notifications
//	allowUsers:  [ou_xxx]                        # open_ids
//	relayReplies: true
//	domain:      https://open.feishu.cn          # or https://open.larksuite.com
//
// In the developer console: enable the bot, subscribe im.message.receive_v1
// and card.action.trigger with "receive events through persistent
// connection", and grant im:message, im:message:send_as_bot (and
// im:chat:readonly so the setup dialog can list the bot's groups).
package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/capabilities/notify"
	"github.com/hedeqiang/crewbase/features/im"
	"github.com/hedeqiang/crewbase/kernel"
)

// Config of the Feishu gateway.
type Config struct {
	im.Config
	AppID  string `json:"appId"`
	Domain string `json:"domain,omitempty"`
}

// Plugin registers the Feishu gateway.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/im-feishu",
	Inject:      []string{notify.ServiceKey, approval.ServiceKey, credentials.ServiceKey},
	Description: "Feishu / Lark IM gateway: notifications, approval cards and chat (long connection).",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		if cfg.AppID == "" {
			return errors.New("im-feishu: appId is required")
		}
		_, err = im.Start(c, "feishu", cfg.Config, func(g *im.Gateway) (im.Transport, error) { return New(g, cfg), nil })
		return err
	},
})

// API is the message API the transport uses (faked in tests).
type API interface {
	Create(ctx context.Context, chat, msgType, content string) (string, error)
	Reply(ctx context.Context, msgID, msgType, content string) (string, error)
	Patch(ctx context.Context, msgID, content string) error
}

// Transport implements im.Transport.
type Transport struct {
	g   *im.Gateway
	cfg Config

	mu  sync.Mutex
	api API
}

// New creates the transport.
func New(g *im.Gateway, cfg Config) *Transport { return &Transport{g: g, cfg: cfg} }

// SetAPI installs the message API (Run does it; tests call it directly).
func (t *Transport) SetAPI(api API) {
	t.mu.Lock()
	t.api = api
	t.mu.Unlock()
}

func (t *Transport) client() (API, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.api == nil {
		return nil, errors.New("feishu: not connected")
	}
	return t.api, nil
}

func (t *Transport) domain() string {
	if t.cfg.Domain != "" {
		return strings.TrimRight(t.cfg.Domain, "/")
	}
	return lark.FeishuBaseUrl
}

// Run implements im.Transport.
func (t *Transport) Run(ctx context.Context, ev im.Events) error {
	secret, err := t.g.Resolve(ctx, t.cfg.BotTokenRef)
	if err != nil {
		return err
	}
	lg := &logger{g: t.g}
	t.SetAPI(NewAPI(t.g, lark.NewClient(t.cfg.AppID, secret, lark.WithOpenBaseUrl(t.domain()), lark.WithLogger(lg), lark.WithLogLevel(larkcore.LogLevelWarn))))
	defer t.SetAPI(nil)

	d := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(ctx context.Context, e *larkim.P2MessageReceiveV1) error {
			if in, ok := Inbound(e); ok {
				go ev.Message(ctx, in) // ack fast; Feishu redelivers slow events
			}
			return nil
		}).
		OnP2CardActionTrigger(func(ctx context.Context, e *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
			a, ok := CardAction(e)
			if !ok {
				return nil, nil
			}
			toast := ev.Action(ctx, a)
			if toast == "" {
				return nil, nil
			}
			return &callback.CardActionTriggerResponse{Toast: &callback.Toast{Type: "info", Content: toast}}, nil
		})
	ws := larkws.NewClient(t.cfg.AppID, secret,
		larkws.WithEventHandler(d), larkws.WithDomain(t.domain()),
		larkws.WithLogger(lg), larkws.WithLogLevel(larkcore.LogLevelWarn),
		larkws.WithOnReady(func() { ev.Connected(true, nil) }),
		larkws.WithOnReconnecting(func() { ev.Connected(false, errors.New("reconnecting")) }),
		larkws.WithOnReconnected(func() { ev.Connected(true, nil) }),
	)
	err = ws.Start(ctx)
	if ctx.Err() != nil {
		return nil
	}
	if err == nil {
		err = errors.New("long connection closed")
	}
	return fmt.Errorf("feishu ws: %s", t.g.Scrub(err.Error()))
}

// logger routes SDK logs to the gateway logger, scrubbed.
type logger struct{ g *im.Gateway }

func (l *logger) log(level slog.Level, args []any) {
	l.g.Logger().Log(context.Background(), level, "feishu sdk", "msg", l.g.Scrub(fmt.Sprint(args...)))
}
func (l *logger) Debug(_ context.Context, a ...any) { l.log(slog.LevelDebug, a) }
func (l *logger) Info(_ context.Context, a ...any)  { l.log(slog.LevelDebug, a) }
func (l *logger) Warn(_ context.Context, a ...any)  { l.log(slog.LevelWarn, a) }
func (l *logger) Error(_ context.Context, a ...any) { l.log(slog.LevelError, a) }

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// Inbound converts im.message.receive_v1 (text messages only).
func Inbound(e *larkim.P2MessageReceiveV1) (im.Inbound, bool) {
	if e == nil || e.Event == nil || e.Event.Message == nil || e.Event.Sender == nil {
		return im.Inbound{}, false
	}
	m, s := e.Event.Message, e.Event.Sender
	if str(s.SenderType) != "user" || s.SenderId == nil || str(m.MessageType) != "text" {
		return im.Inbound{}, false
	}
	var content struct {
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(str(m.Content)), &content) != nil {
		return im.Inbound{}, false
	}
	text := content.Text
	direct := str(m.ChatType) == "p2p"
	for _, mn := range m.Mentions {
		key := str(mn.Key)
		if key == "" {
			continue
		}
		// Group messages reach the bot only when it is @-mentioned (without
		// the group-message permission), so a mention marks it addressed.
		if t := str(mn.MentionedType); t == "bot" || t == "" {
			direct = true
			text = strings.ReplaceAll(text, key, "")
		} else {
			text = strings.ReplaceAll(text, key, "@"+str(mn.Name))
		}
	}
	thread := str(m.ParentId)
	if thread == "" {
		thread = str(m.RootId)
	}
	return im.Inbound{User: im.User{ID: str(s.SenderId.OpenId)}, Chat: str(m.ChatId), MessageID: str(m.MessageId),
		Thread: thread, Text: strings.TrimSpace(text), Direct: direct || thread != ""}, true
}

// CardAction converts a card.action.trigger callback.
func CardAction(e *callback.CardActionTriggerEvent) (im.Action, bool) {
	if e == nil || e.Event == nil || e.Event.Action == nil || e.Event.Operator == nil {
		return im.Action{}, false
	}
	v, _ := e.Event.Action.Value["v"].(string)
	if v == "" {
		return im.Action{}, false
	}
	a := im.Action{User: im.User{ID: e.Event.Operator.OpenID}, Value: v}
	if c := e.Event.Context; c != nil {
		a.Ref = im.Ref{Chat: c.OpenChatID, ID: c.OpenMessageID}
	}
	return a, true
}

// Card renders m as a Feishu interactive card (JSON 1.0 schema).
func Card(m im.Message) string {
	var els []any
	body := m.Body
	if m.Title == "" && body == "" {
		body = m.Text()
	}
	if body != "" {
		els = append(els, map[string]any{"tag": "div", "text": map[string]any{"tag": "lark_md", "content": trunc(body, 3000)}})
	}
	if len(m.Buttons) > 0 {
		var acts []any
		for _, b := range m.Buttons {
			typ := "default"
			switch b.Style {
			case "primary":
				typ = "primary"
			case "danger":
				typ = "danger"
			}
			acts = append(acts, map[string]any{"tag": "button", "type": typ,
				"text": map[string]any{"tag": "plain_text", "content": b.Label}, "value": map[string]any{"v": b.Value}})
		}
		els = append(els, map[string]any{"tag": "action", "actions": acts})
	}
	var note []any
	if m.Footer != "" {
		note = append(note, map[string]any{"tag": "plain_text", "content": m.Footer})
	}
	if m.Agent != "" {
		note = append(note, map[string]any{"tag": "plain_text", "content": "Crewbase · " + m.Agent})
	}
	if len(note) > 0 {
		els = append(els, map[string]any{"tag": "note", "elements": note})
	}
	card := map[string]any{"config": map[string]any{"wide_screen_mode": true, "update_multi": true}, "elements": els}
	if m.Title != "" {
		tpl := "blue"
		if len(m.Buttons) > 0 {
			tpl = "orange"
		}
		card["header"] = map[string]any{"title": map[string]any{"tag": "plain_text", "content": trunc(m.Title, 100)}, "template": tpl}
	}
	b, _ := json.Marshal(card)
	return string(b)
}

func trunc(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// Post implements im.Transport. Everything is sent as an interactive card
// so it can later be patched (approval outcome).
func (t *Transport) Post(ctx context.Context, m im.Message) (im.Ref, error) {
	api, err := t.client()
	if err != nil {
		return im.Ref{}, err
	}
	reply := m.ReplyTo
	if reply == "" {
		reply = m.Thread
	}
	var id string
	if reply != "" {
		id, err = api.Reply(ctx, reply, "interactive", Card(m))
	} else {
		id, err = api.Create(ctx, m.Chat, "interactive", Card(m))
	}
	if err != nil {
		return im.Ref{}, err
	}
	return im.Ref{Chat: m.Chat, ID: id, Thread: m.Thread}, nil
}

// Edit implements im.Transport.
func (t *Transport) Edit(ctx context.Context, ref im.Ref, m im.Message) error {
	api, err := t.client()
	if err != nil {
		return err
	}
	return api.Patch(ctx, ref.ID, Card(m))
}

// ---------------------------------------------------------------- SDK API

type sdkAPI struct {
	g *im.Gateway
	c *lark.Client
}

// NewAPI wraps an SDK client.
func NewAPI(g *im.Gateway, c *lark.Client) API { return sdkAPI{g: g, c: c} }

func (a sdkAPI) fail(op string, err error, code int, msg string) error {
	if err != nil {
		return fmt.Errorf("feishu %s: %s", op, a.g.Scrub(err.Error()))
	}
	return fmt.Errorf("feishu %s: code %d %s", op, code, a.g.Scrub(msg))
}

func (a sdkAPI) Create(ctx context.Context, chat, msgType, content string) (string, error) {
	idType := "chat_id"
	switch {
	case strings.HasPrefix(chat, "ou_"):
		idType = "open_id"
	case strings.HasPrefix(chat, "on_"):
		idType = "union_id"
	}
	resp, err := a.c.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().ReceiveIdType(idType).
		Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(chat).MsgType(msgType).Content(content).Build()).Build())
	if err != nil || !resp.Success() {
		if resp != nil {
			return "", a.fail("create message", err, resp.Code, resp.Msg)
		}
		return "", a.fail("create message", err, 0, "")
	}
	return str(resp.Data.MessageId), nil
}

func (a sdkAPI) Reply(ctx context.Context, msgID, msgType, content string) (string, error) {
	resp, err := a.c.Im.Message.Reply(ctx, larkim.NewReplyMessageReqBuilder().MessageId(msgID).
		Body(larkim.NewReplyMessageReqBodyBuilder().MsgType(msgType).Content(content).Build()).Build())
	if err != nil || !resp.Success() {
		if resp != nil {
			return "", a.fail("reply message", err, resp.Code, resp.Msg)
		}
		return "", a.fail("reply message", err, 0, "")
	}
	return str(resp.Data.MessageId), nil
}

func (a sdkAPI) Patch(ctx context.Context, msgID, content string) error {
	resp, err := a.c.Im.Message.Patch(ctx, larkim.NewPatchMessageReqBuilder().MessageId(msgID).
		Body(larkim.NewPatchMessageReqBodyBuilder().Content(content).Build()).Build())
	if err != nil || !resp.Success() {
		if resp != nil {
			return a.fail("patch message", err, resp.Code, resp.Msg)
		}
		return a.fail("patch message", err, 0, "")
	}
	return nil
}
