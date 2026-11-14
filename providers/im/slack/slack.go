// Package slack is the Slack IM gateway ("@crewbase/im-slack") over Socket
// Mode: the bot opens an outbound websocket with an app-level token, so no
// public Request URL is needed (docs/06 §8).
//
// Config (see im.Config):
//
//	botTokenRef: keychain:crewbase/slack-bot   # xoxb-… (chat:write, users:read, app_mentions:read, im:history, channels:history)
//	appTokenRef: keychain:crewbase/slack-app   # xapp-… (connections:write)
//	channel:     C0123456789
//	allowUsers:  [U0123456789]
//	relayReplies: true
//	apiURL:      https://slack.com/api/        # tests
package slack

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/capabilities/notify"
	"github.com/hedeqiang/crewbase/features/im"
	"github.com/hedeqiang/crewbase/kernel"
)

// Config of the Slack gateway.
type Config struct {
	im.Config
	APIURL string `json:"apiURL,omitempty"`
}

// Plugin registers the Slack gateway.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/im-slack",
	Inject:      []string{notify.ServiceKey, approval.ServiceKey, credentials.ServiceKey},
	Description: "Slack IM gateway: notifications, approvals and chat (Socket Mode).",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		if cfg.AppTokenRef == "" {
			return errors.New("im-slack: appTokenRef (xapp- app-level token) is required for Socket Mode")
		}
		_, err = im.Start(c, "slack", cfg.Config, func(g *im.Gateway) (im.Transport, error) { return New(g, cfg), nil })
		return err
	},
})

// Transport implements im.Transport.
type Transport struct {
	g   *im.Gateway
	cfg Config

	mu    sync.Mutex
	api   *slack.Client
	botID string
	names map[string]string
}

// New creates the transport.
func New(g *im.Gateway, cfg Config) *Transport {
	return &Transport{g: g, cfg: cfg, names: map[string]string{}}
}

func (t *Transport) client() (*slack.Client, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.api == nil {
		return nil, errors.New("slack: not connected")
	}
	return t.api, nil
}

func (t *Transport) err(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("slack %s: %s", op, t.g.Scrub(err.Error()))
}

// Run implements im.Transport.
func (t *Transport) Run(ctx context.Context, ev im.Events) error {
	bot, err := t.g.Resolve(ctx, t.cfg.BotTokenRef)
	if err != nil {
		return err
	}
	app, err := t.g.Resolve(ctx, t.cfg.AppTokenRef)
	if err != nil {
		return err
	}
	opts := []slack.Option{slack.OptionAppLevelToken(app)}
	if t.cfg.APIURL != "" {
		opts = append(opts, slack.OptionAPIURL(strings.TrimRight(t.cfg.APIURL, "/")+"/"))
	}
	api := slack.New(bot, opts...)
	auth, err := api.AuthTestContext(ctx)
	if err != nil {
		return t.err("auth.test", err)
	}
	t.mu.Lock()
	t.api, t.botID = api, auth.UserID
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		t.api = nil
		t.mu.Unlock()
	}()

	sm := socketmode.New(api)
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sm.RunContext(rctx) }()
	for {
		select {
		case <-ctx.Done():
			cancel()
			<-done
			return nil
		case err := <-done:
			if ctx.Err() != nil {
				return nil
			}
			if err == nil {
				err = errors.New("socket mode closed")
			}
			return t.err("socket mode", err)
		case e := <-sm.Events:
			t.dispatch(ctx, sm, ev, e)
		}
	}
}

func (t *Transport) dispatch(ctx context.Context, sm *socketmode.Client, ev im.Events, e socketmode.Event) {
	switch e.Type {
	case socketmode.EventTypeConnected:
		ev.Connected(true, nil)
	case socketmode.EventTypeConnectionError, socketmode.EventTypeInvalidAuth:
		ev.Connected(false, t.err("connection", fmt.Errorf("%v: %v", e.Type, e.Data)))
	case socketmode.EventTypeEventsAPI:
		if e.Request != nil {
			_ = sm.Ack(*e.Request)
		}
		api, ok := e.Data.(slackevents.EventsAPIEvent)
		if !ok {
			return
		}
		t.mu.Lock()
		botID := t.botID
		t.mu.Unlock()
		in, ok := Inbound(api.InnerEvent.Data, botID)
		if !ok {
			return
		}
		in.User.Name = t.userName(ctx, in.User.ID)
		ev.Message(ctx, in)
	case socketmode.EventTypeInteractive:
		cb, ok := e.Data.(slack.InteractionCallback)
		if e.Request != nil {
			_ = sm.Ack(*e.Request)
		}
		if !ok {
			return
		}
		for _, a := range Actions(cb) {
			if toast := ev.Action(ctx, a); toast != "" {
				t.ephemeral(ctx, a.Ref.Chat, a.User.ID, toast)
			}
		}
	}
}

func (t *Transport) ephemeral(ctx context.Context, channel, user, text string) {
	api, err := t.client()
	if err != nil || channel == "" {
		return
	}
	if _, err := api.PostEphemeralContext(ctx, channel, user, slack.MsgOptionText(text, false)); err != nil && ctx.Err() == nil {
		t.g.Logger().Debug("slack ephemeral", "err", t.err("chat.postEphemeral", err).Error())
	}
}

func (t *Transport) userName(ctx context.Context, id string) string {
	t.mu.Lock()
	n, ok := t.names[id]
	t.mu.Unlock()
	if ok {
		return n
	}
	api, err := t.client()
	if err != nil {
		return ""
	}
	u, err := api.GetUserInfoContext(ctx, id)
	if err != nil {
		return ""
	}
	n = u.Profile.DisplayName
	if n == "" {
		n = u.RealName
	}
	if n == "" {
		n = u.Name
	}
	t.mu.Lock()
	if len(t.names) > 1000 {
		clear(t.names)
	}
	t.names[id] = n
	t.mu.Unlock()
	return n
}

// Inbound converts an Events API inner event. App mentions are addressed
// to the bot; plain channel messages that mention the bot are dropped
// (Slack delivers them again as app_mention).
func Inbound(data any, botID string) (im.Inbound, bool) {
	mention := ""
	if botID != "" {
		mention = "<@" + botID + ">"
	}
	strip := func(s string) string {
		if mention != "" {
			s = strings.ReplaceAll(s, mention, "")
		}
		return strings.TrimSpace(s)
	}
	switch e := data.(type) {
	case *slackevents.AppMentionEvent:
		if e.BotID != "" || e.User == "" || e.User == botID {
			return im.Inbound{}, false
		}
		return im.Inbound{User: im.User{ID: e.User}, Chat: e.Channel, MessageID: e.TimeStamp,
			Thread: e.ThreadTimeStamp, Text: strip(e.Text), Direct: true}, true
	case *slackevents.MessageEvent:
		if e.SubType != "" || e.BotID != "" || e.User == "" || e.User == botID {
			return im.Inbound{}, false
		}
		direct := e.ChannelType == "im"
		if !direct && mention != "" && strings.Contains(e.Text, mention) {
			return im.Inbound{}, false
		}
		return im.Inbound{User: im.User{ID: e.User}, Chat: e.Channel, MessageID: e.TimeStamp,
			Thread: e.ThreadTimeStamp, Text: strip(e.Text), Direct: direct}, true
	}
	return im.Inbound{}, false
}

// Actions converts a block_actions interaction into button presses.
func Actions(cb slack.InteractionCallback) []im.Action {
	if cb.Type != slack.InteractionTypeBlockActions {
		return nil
	}
	ch := cb.Channel.ID
	if ch == "" {
		ch = cb.Container.ChannelID
	}
	ts := cb.Container.MessageTs
	if ts == "" {
		ts = cb.Message.Timestamp
	}
	name := cb.User.Name
	var out []im.Action
	for _, a := range cb.ActionCallback.BlockActions {
		if a == nil || !strings.HasPrefix(a.ActionID, actionPrefix) {
			continue
		}
		out = append(out, im.Action{User: im.User{ID: cb.User.ID, Name: name}, Value: a.Value,
			Ref: im.Ref{Chat: ch, ID: ts, Thread: cb.Container.ThreadTs}})
	}
	return out
}

const actionPrefix = "crewbase_"

// Blocks renders m as Block Kit blocks.
func Blocks(m im.Message) []slack.Block {
	var bs []slack.Block
	if m.Title != "" {
		bs = append(bs, slack.NewHeaderBlock(slack.NewTextBlockObject(slack.PlainTextType, trunc(m.Title, 150), true, false)))
	}
	if m.Body != "" {
		bs = append(bs, slack.NewSectionBlock(slack.NewTextBlockObject(slack.MarkdownType, trunc(m.Body, 3000), false, true), nil, nil))
	}
	if m.Agent != "" {
		bs = append(bs, slack.NewContextBlock("", slack.NewTextBlockObject(slack.MarkdownType, "Crewbase · "+m.Agent, false, true)))
	}
	if len(m.Buttons) > 0 {
		var els []slack.BlockElement
		for i, b := range m.Buttons {
			btn := slack.NewButtonBlockElement(fmt.Sprintf("%s%d", actionPrefix, i), b.Value,
				slack.NewTextBlockObject(slack.PlainTextType, b.Label, true, false))
			switch b.Style {
			case "primary":
				btn = btn.WithStyle(slack.StylePrimary)
			case "danger":
				btn = btn.WithStyle(slack.StyleDanger)
			}
			els = append(els, btn)
		}
		bs = append(bs, slack.NewActionBlock("crewbase_actions", els...))
	}
	if m.Footer != "" {
		bs = append(bs, slack.NewContextBlock("", slack.NewTextBlockObject(slack.MarkdownType, m.Footer, false, true)))
	}
	return bs
}

func trunc(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func options(m im.Message) []slack.MsgOption {
	return []slack.MsgOption{slack.MsgOptionText(trunc(m.Text(), 3000), false), slack.MsgOptionBlocks(Blocks(m)...)}
}

// Post implements im.Transport.
func (t *Transport) Post(ctx context.Context, m im.Message) (im.Ref, error) {
	api, err := t.client()
	if err != nil {
		return im.Ref{}, err
	}
	opts := options(m)
	if m.Thread != "" {
		opts = append(opts, slack.MsgOptionTS(m.Thread))
	}
	ch, ts, err := api.PostMessageContext(ctx, m.Chat, opts...)
	if err != nil {
		return im.Ref{}, t.err("chat.postMessage", err)
	}
	return im.Ref{Chat: ch, ID: ts, Thread: m.Thread}, nil
}

// Edit implements im.Transport.
func (t *Transport) Edit(ctx context.Context, ref im.Ref, m im.Message) error {
	api, err := t.client()
	if err != nil {
		return err
	}
	_, _, _, err = api.UpdateMessageContext(ctx, ref.Chat, ref.ID, options(m)...)
	return t.err("chat.update", err)
}
