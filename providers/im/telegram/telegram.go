// Package telegram is the Telegram IM gateway ("@crewbase/im-telegram").
// It talks to the Bot API over plain HTTPS with long polling (getUpdates):
// an outbound connection only, no webhook URL (docs/06 §8).
//
// Config (see im.Config):
//
//	botTokenRef: keychain:crewbase/telegram-bot
//	channel:     "123456789"         # chat id for notifications and approvals
//	allowUsers:  ["123456789"]       # numeric Telegram user ids
//	relayReplies: true
//	quietHours:  {from: "22:00", to: "08:00", tz: "Asia/Shanghai"}
//	apiBase:     https://api.telegram.org   # tests / self-hosted Bot API
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/capabilities/notify"
	"github.com/hedeqiang/crewbase/features/im"
	"github.com/hedeqiang/crewbase/kernel"
)

// Config of the Telegram gateway.
type Config struct {
	im.Config
	APIBase string `json:"apiBase,omitempty"`
	// PollTimeout is the getUpdates long-poll timeout in seconds (default 25).
	PollTimeout int `json:"pollTimeout,omitempty"`
}

// Plugin registers the Telegram gateway.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/im-telegram",
	Inject:      []string{notify.ServiceKey, approval.ServiceKey, credentials.ServiceKey},
	Description: "Telegram IM gateway: notifications, approvals and chat (long polling).",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		_, err = im.Start(c, "telegram", cfg.Config, func(g *im.Gateway) (im.Transport, error) {
			return New(g, cfg), nil
		})
		return err
	},
})

// Transport implements im.Transport over the Bot API.
type Transport struct {
	g       *im.Gateway
	cfg     Config
	base    string
	http    *http.Client
	timeout int

	mu    sync.Mutex
	token string
	botID int64
	bot   string // username, without @
}

// New creates the transport.
func New(g *im.Gateway, cfg Config) *Transport {
	base := strings.TrimRight(cfg.APIBase, "/")
	if base == "" {
		base = "https://api.telegram.org"
	}
	to := cfg.PollTimeout
	if to <= 0 {
		to = 25
	}
	return &Transport{g: g, cfg: cfg, base: base, timeout: to, http: &http.Client{}}
}

type apiResp struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

var errNotConnected = errors.New("telegram: not connected")

func (t *Transport) currentToken() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.token
}

// call POSTs a Bot API method. Errors never contain the token (the URL is
// not included and the message is scrubbed).
func (t *Transport) call(ctx context.Context, token, method string, payload, out any) error {
	if token == "" {
		return errNotConnected
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.base+"/bot"+token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram %s: bad request", method)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var ue interface{ Unwrap() error }
		if errors.As(err, &ue) && ue.Unwrap() != nil {
			err = ue.Unwrap() // drop the *url.Error wrapper (it carries the URL)
		}
		return fmt.Errorf("telegram %s: %s", method, t.g.Scrub(strings.ReplaceAll(err.Error(), token, "***")))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	var r apiResp
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("telegram %s: HTTP %d", method, resp.StatusCode)
	}
	if !r.OK {
		msg := fmt.Sprintf("telegram %s: %d %s", method, r.ErrorCode, t.g.Scrub(r.Description))
		if r.Parameters != nil && r.Parameters.RetryAfter > 0 {
			msg += fmt.Sprintf(" (retry after %ds)", r.Parameters.RetryAfter)
		}
		return errors.New(msg)
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

// ---------------------------------------------------------------- types

type tgUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

func (u *tgUser) user() im.User {
	if u == nil {
		return im.User{}
	}
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name == "" {
		name = u.Username
	}
	return im.User{ID: strconv.FormatInt(u.ID, 10), Name: name}
}

type tgChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type tgMessage struct {
	MessageID int64      `json:"message_id"`
	From      *tgUser    `json:"from"`
	Chat      tgChat     `json:"chat"`
	Text      string     `json:"text"`
	ReplyTo   *tgMessage `json:"reply_to_message"`
}

type tgCallback struct {
	ID      string     `json:"id"`
	From    tgUser     `json:"from"`
	Message *tgMessage `json:"message"`
	Data    string     `json:"data"`
}

type tgUpdate struct {
	UpdateID      int64       `json:"update_id"`
	Message       *tgMessage  `json:"message"`
	CallbackQuery *tgCallback `json:"callback_query"`
}

type inlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type inlineKeyboard struct {
	InlineKeyboard [][]inlineButton `json:"inline_keyboard"`
}

// ---------------------------------------------------------------- run

// Run implements im.Transport: resolve the token, getMe, then long-poll.
func (t *Transport) Run(ctx context.Context, ev im.Events) error {
	tok, err := t.g.Resolve(ctx, t.cfg.BotTokenRef)
	if err != nil {
		return err
	}
	var me tgUser
	if err := t.call(ctx, tok, "getMe", map[string]any{}, &me); err != nil {
		return err
	}
	t.mu.Lock()
	t.token, t.botID, t.bot = tok, me.ID, me.Username
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		t.token = ""
		t.mu.Unlock()
	}()
	ev.Connected(true, nil)

	var offset int64
	for ctx.Err() == nil {
		var ups []tgUpdate
		pctx, cancel := context.WithTimeout(ctx, time.Duration(t.timeout+10)*time.Second)
		err := t.call(pctx, tok, "getUpdates", map[string]any{
			"offset": offset, "timeout": t.timeout, "allowed_updates": []string{"message", "callback_query"},
		}, &ups)
		cancel()
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err
		}
		for _, u := range ups {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			t.handle(ctx, tok, ev, u)
		}
	}
	return nil
}

func (t *Transport) handle(ctx context.Context, tok string, ev im.Events, u tgUpdate) {
	switch {
	case u.CallbackQuery != nil:
		q := u.CallbackQuery
		a := im.Action{User: q.From.user(), Value: q.Data}
		if q.Message != nil {
			a.Ref = im.Ref{Chat: strconv.FormatInt(q.Message.Chat.ID, 10), ID: strconv.FormatInt(q.Message.MessageID, 10)}
		}
		toast := ev.Action(ctx, a)
		ans := map[string]any{"callback_query_id": q.ID}
		if toast != "" {
			ans["text"] = toast
		}
		if err := t.call(ctx, tok, "answerCallbackQuery", ans, nil); err != nil && ctx.Err() == nil {
			t.g.Logger().Debug("answerCallbackQuery", "err", err.Error())
		}
	case u.Message != nil:
		in, ok := t.inbound(u.Message)
		if ok {
			ev.Message(ctx, in)
		}
	}
}

// inbound converts a message; bot messages and non-text are dropped.
func (t *Transport) inbound(m *tgMessage) (im.Inbound, bool) {
	if m.From == nil || m.From.IsBot || strings.TrimSpace(m.Text) == "" {
		return im.Inbound{}, false
	}
	t.mu.Lock()
	bot, botID := t.bot, t.botID
	t.mu.Unlock()
	text := m.Text
	direct := m.Chat.Type == "private"
	if bot != "" && strings.Contains(strings.ToLower(text), "@"+strings.ToLower(bot)) {
		direct = true
		text = replaceFold(text, "@"+bot, "")
	}
	in := im.Inbound{
		User:      m.From.user(),
		Chat:      strconv.FormatInt(m.Chat.ID, 10),
		MessageID: strconv.FormatInt(m.MessageID, 10),
		Text:      strings.TrimSpace(text),
		Direct:    direct,
	}
	if m.ReplyTo != nil {
		in.Thread = strconv.FormatInt(m.ReplyTo.MessageID, 10)
		if m.ReplyTo.From != nil && m.ReplyTo.From.ID == botID {
			in.Direct = true
		}
	}
	return in, true
}

func replaceFold(s, old, repl string) string {
	i := strings.Index(strings.ToLower(s), strings.ToLower(old))
	if i < 0 {
		return s
	}
	return s[:i] + repl + s[i+len(old):]
}

// ---------------------------------------------------------------- send

// MaxText is Telegram's message length limit.
const MaxText = 4096

// Render returns the text and keyboard for m.
func Render(m im.Message) (string, *inlineKeyboard) {
	text := m.Text()
	if m.Title != "" {
		text = "🔔 " + text
	}
	if r := []rune(text); len(r) > MaxText {
		text = string(r[:MaxText-1]) + "…"
	}
	if len(m.Buttons) == 0 {
		return text, nil
	}
	row := make([]inlineButton, 0, len(m.Buttons))
	for _, b := range m.Buttons {
		label := b.Label
		switch b.Style {
		case "primary":
			label = "✅ " + label
		case "danger":
			label = "❌ " + label
		}
		row = append(row, inlineButton{Text: label, CallbackData: b.Value})
	}
	return text, &inlineKeyboard{InlineKeyboard: [][]inlineButton{row}}
}

// Post implements im.Transport.
func (t *Transport) Post(ctx context.Context, m im.Message) (im.Ref, error) {
	text, kb := Render(m)
	p := map[string]any{"chat_id": m.Chat, "text": text}
	if kb != nil {
		p["reply_markup"] = kb
	}
	reply := m.ReplyTo
	if reply == "" {
		reply = m.Thread
	}
	if id, err := strconv.ParseInt(reply, 10, 64); err == nil && id > 0 {
		p["reply_parameters"] = map[string]any{"message_id": id, "allow_sending_without_reply": true}
	}
	var out tgMessage
	if err := t.call(ctx, t.currentToken(), "sendMessage", p, &out); err != nil {
		return im.Ref{}, err
	}
	return im.Ref{Chat: strconv.FormatInt(out.Chat.ID, 10), ID: strconv.FormatInt(out.MessageID, 10), Thread: m.Thread}, nil
}

// Edit implements im.Transport (removes the keyboard when m has no buttons).
func (t *Transport) Edit(ctx context.Context, ref im.Ref, m im.Message) error {
	id, err := strconv.ParseInt(ref.ID, 10, 64)
	if err != nil {
		return fmt.Errorf("telegram: bad message id %q", ref.ID)
	}
	text, kb := Render(m)
	p := map[string]any{"chat_id": ref.Chat, "message_id": id, "text": text}
	if kb != nil {
		p["reply_markup"] = kb
	} else {
		p["reply_markup"] = inlineKeyboard{InlineKeyboard: [][]inlineButton{}}
	}
	return t.call(ctx, t.currentToken(), "editMessageText", p, nil)
}
