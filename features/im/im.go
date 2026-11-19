// Package im is the shared model of the IM gateways (PRD D-4: Slack,
// Telegram, Discord, Feishu). A gateway is one bot connection that
//
//   - is a notify.Channel: notifications are posted to a configured chat;
//   - is an approval.Responder: approval cards with 批准 / 拒绝 / 稍后在桌面处理
//     buttons; the first allowed person to press claims the request
//     (docs/06 §4). Push approvals only offer 拒绝 and 稍后在桌面处理;
//     "以后都允许" is never offered in IM;
//   - accepts inbound chat: "@<agent> text" or a reply in a thread bound to
//     an agent enqueues an agent.Input, and (opt-in, relayReplies) the
//     agent's reply is posted back to the same thread.
//
// Every connection is outbound (long polling / Socket Mode / Gateway
// websocket / Feishu long connection); no gateway opens an inbound port
// (docs/06 §8). Bot tokens are credential references resolved at connect
// time and scrubbed from every logged or returned error.
//
// Providers live in providers/im/<name> and implement Transport; Start
// wires a transport into the kernel.
package im

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/time/rate"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/capabilities/notify"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/kernel"
)

// ServicePrefix prefixes the service key of every gateway ("im.telegram").
const ServicePrefix = "im."

// ---------------------------------------------------------------- model

// Button is one interactive button on a posted message.
type Button struct {
	Value string `json:"value"` // opaque payload returned in Action.Value
	Label string `json:"label"`
	Style string `json:"style,omitempty"` // "primary" | "danger" | ""
}

// Message is a platform-neutral outgoing message.
type Message struct {
	Chat string // conversation id; "" = the gateway's configured channel
	// Thread is the root anchor of a conversation thread (Slack thread_ts).
	// ReplyTo is the specific message to reply to (Telegram, Discord,
	// Feishu); transports fall back to Thread when it is empty.
	Thread  string
	ReplyTo string
	Title   string
	Body    string
	Agent   string // agent display name, shown as context
	Footer  string // status line, e.g. "已批准 · Ada"
	Buttons []Button
}

// Text renders m as plain text (used by transports without rich layout and
// as a fallback/notification text).
func (m Message) Text() string {
	var b strings.Builder
	if m.Title != "" {
		b.WriteString(m.Title)
	}
	if m.Body != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(m.Body)
	}
	if m.Agent != "" && m.Title == "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("— " + m.Agent)
	}
	if m.Footer != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(m.Footer)
	}
	return b.String()
}

// Ref identifies a posted message.
type Ref struct {
	Chat   string `json:"chat"`
	ID     string `json:"id"`
	Thread string `json:"thread,omitempty"`
}

// User is an IM user.
type User struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

func (u User) display() string {
	if u.Name != "" {
		return u.Name
	}
	return u.ID
}

// Action is a button press.
type Action struct {
	User  User
	Value string
	Ref   Ref // the message carrying the button
}

// Inbound is a text message received by the bot.
type Inbound struct {
	User      User
	Chat      string
	MessageID string
	// Thread is the anchor the message replies to (Slack thread_ts,
	// Telegram/Discord replied-to message id, Feishu parent/root id); ""
	// for a top-level message.
	Thread string
	Text   string // bot mention already stripped
	// Direct: the bot is addressed (DM, @bot, reply to the bot).
	Direct bool
}

// Events receives inbound traffic from a transport.
type Events interface {
	// Action handles a button press and returns a short text to show the
	// presser ("" = none). It returns quickly (platforms require an ack
	// within seconds).
	Action(ctx context.Context, a Action) string
	// Message handles an inbound text message.
	Message(ctx context.Context, in Inbound)
	// Connected reports connection state changes.
	Connected(ok bool, err error)
}

// Transport is the platform-specific part of a gateway.
type Transport interface {
	// Run connects (resolving tokens now) and delivers events until ctx is
	// done, then disconnects. A returned error makes the gateway reconnect
	// with backoff.
	Run(ctx context.Context, ev Events) error
	Post(ctx context.Context, m Message) (Ref, error)
	Edit(ctx context.Context, ref Ref, m Message) error
}

// ---------------------------------------------------------------- config

// QuietHours mutes non-urgent notifications between From and To ("22:00",
// "08:00") in TZ (IANA name; "" = local). Approvals ignore quiet hours.
type QuietHours struct {
	From string `json:"from"`
	To   string `json:"to"`
	TZ   string `json:"tz,omitempty"`
}

// Active reports whether t falls inside the quiet window.
func (q *QuietHours) Active(t time.Time) bool {
	if q == nil || q.From == "" || q.To == "" {
		return false
	}
	from, ok1 := parseClock(q.From)
	to, ok2 := parseClock(q.To)
	if !ok1 || !ok2 || from == to {
		return false
	}
	if q.TZ != "" {
		if loc, err := time.LoadLocation(q.TZ); err == nil {
			t = t.In(loc)
		}
	}
	now := t.Hour()*60 + t.Minute()
	if from < to {
		return now >= from && now < to
	}
	return now >= from || now < to // wraps midnight
}

func parseClock(s string) (int, bool) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}

// Config is the common gateway config. Providers embed it.
type Config struct {
	// Name overrides the gateway name (default: the provider, e.g.
	// "telegram"); it is the notify channel name and the API path segment.
	Name string `json:"name,omitempty"`
	// BotTokenRef / AppTokenRef are credential references
	// (vault:crewbase/telegram-bot); never plaintext.
	BotTokenRef string `json:"botTokenRef"`
	AppTokenRef string `json:"appTokenRef,omitempty"`
	// Channel is the chat/channel id notifications and approval cards go to.
	Channel string `json:"channel"`
	// AllowUsers are platform user ids allowed to approve and to talk to
	// agents. Empty = nobody (fail closed).
	AllowUsers []string `json:"allowUsers"`
	// RelayReplies posts agent replies back into the thread they came from.
	RelayReplies bool        `json:"relayReplies"`
	QuietHours   *QuietHours `json:"quietHours,omitempty"`
	// Approvals makes the gateway an approval responder (default true).
	Approvals *bool `json:"approvals,omitempty"`
	// NotifyKinds limits which notification kinds this channel receives
	// (default: notify's policy — everything except model switches).
	NotifyKinds []string `json:"notifyKinds,omitempty"`
	// RatePerMinute caps outgoing messages (default 20, burst 5).
	RatePerMinute int `json:"ratePerMinute,omitempty"`
	// StateDir keeps thread → conversation bindings across restarts
	// (im-<name>-threads.json); "" = memory only.
	StateDir string `json:"stateDir,omitempty"`
	// Listen lets an agent follow the channel without being @'d and step
	// in when it can help (off by default).
	Listen *Listen `json:"listen,omitempty"`
	// Team binds the chat to a team: only that team's agents can be
	// addressed from it ("" = any agent).
	Team string `json:"team,omitempty"`
}

// Listen configures listening in on the channel. Messages pass a program
// filter first (the channel itself, people in AllowUsers, MinChars,
// Keywords, PerHour); the agent then decides whether to reply and stays
// quiet otherwise. Listened-in turns run read-only: group chatter is
// untrusted input.
type Listen struct {
	Agent    string   `json:"agent"`
	Keywords []string `json:"keywords,omitempty"` // any of them (case-insensitive); empty = all
	MinChars int      `json:"minChars,omitempty"` // default 8
	PerHour  int      `json:"perHour,omitempty"`  // messages handed to the agent, default 20
}

// NoReply is what a listening agent answers to stay quiet.
const NoReply = "NO_REPLY"

// ListenPrompt wraps a message the agent was not addressed in.
const ListenPrompt = "[Listening in on the team chat — you were not addressed] %s wrote:\n\n%s\n\n" +
	"Reply only if you can clearly help right now: answer a question you know the answer to, point out a mistake or a risk, " +
	"or offer something useful you have. Your reply goes under that message in the chat. Otherwise reply with exactly " + NoReply +
	" and nothing else. When unsure, stay quiet."

func (c Config) approvals() bool { return c.Approvals == nil || *c.Approvals }

// ---------------------------------------------------------------- gateway

// Approval button verbs.
const (
	VerbAllow = "allow"
	VerbDeny  = "deny"
	VerbLater = "later"
)

// Labels (docs/06 §4, docs/03 §5.7).
const (
	LabelAllow = "批准"
	LabelDeny  = "拒绝"
	LabelLater = "稍后在桌面处理"
)

// NoApprove reports whether IM may only reject or defer requests of this
// category (code push: diffs are hard to review on a phone; payment is
// always blocked).
func NoApprove(category string) bool { return category == "push" || category == "payment" }

type pendingCard struct {
	req       approval.Request
	ref       Ref
	posted    time.Time
	noApprove bool
	ch        chan verdict
}

type verdict struct {
	verb string
	user User
}

type binding struct {
	id      string
	agent   string
	session string
	chat    string
	anchor  string
	listen  string // listened-in message id: its thread is bound only once the agent replies
}

// Status is the public state of a gateway (never contains tokens).
type Status struct {
	Name         string      `json:"name"`
	Provider     string      `json:"provider"`
	Plugin       string      `json:"plugin"`
	Connected    bool        `json:"connected"`
	Error        string      `json:"error,omitempty"`
	Since        time.Time   `json:"since,omitzero"`
	Channel      string      `json:"channel,omitempty"`
	AllowUsers   int         `json:"allowUsers"`
	RelayReplies bool        `json:"relayReplies"`
	Approvals    bool        `json:"approvals"`
	QuietHours   *QuietHours `json:"quietHours,omitempty"`
	Pending      int         `json:"pending"`
}

// Gateway is one running IM gateway.
type Gateway struct {
	name, provider string
	cfg            Config
	c              *kernel.Context
	log            *slog.Logger
	t              Transport
	limiter        *rate.Limiter
	now            func() time.Time

	mu        sync.Mutex
	secrets   []string
	connected bool
	lastErr   string
	since     time.Time
	pending   map[string]*pendingCard         // token → card
	closed    map[string]closedCard           // callID → card awaiting final outcome
	early     map[string]approval.DecidedData // outcome that arrived before the card was closed
	bindings  map[string]*binding             // chat\x00msgID → binding
	byID      map[string]*binding
	order     []string // binding ids, oldest first (bounded)
	saveTimer *time.Timer
	refused   map[string]time.Time
	cardWait  *sync.Cond
	listenLim *rate.Limiter
	// inputs enqueued from chat (input id → queued), for the chat norm
	chatInputs map[string]time.Time
}

// New creates a gateway without starting it (Start does both).
func New(c *kernel.Context, provider string, cfg Config) *Gateway {
	name := cfg.Name
	if name == "" {
		name = provider
	}
	rpm := cfg.RatePerMinute
	if rpm <= 0 {
		rpm = 20
	}
	g := &Gateway{
		name: name, provider: provider, cfg: cfg, c: c,
		log:      c.Log().With("im", name),
		limiter:  rate.NewLimiter(rate.Every(time.Minute/time.Duration(rpm)), 5),
		now:      time.Now,
		pending:  map[string]*pendingCard{},
		closed:   map[string]closedCard{},
		bindings: map[string]*binding{},
		byID:     map[string]*binding{},
		refused:  map[string]time.Time{},
	}
	g.cardWait = sync.NewCond(&g.mu)
	g.loadBindings()
	return g
}

// ---- binding persistence

type savedBinding struct {
	ID      string   `json:"id"`
	Agent   string   `json:"agent"`
	Session string   `json:"session"`
	Chat    string   `json:"chat"`
	Anchor  string   `json:"anchor,omitempty"`
	Msgs    []string `json:"msgs"`
	Listen  string   `json:"listen,omitempty"`
}

func (g *Gateway) statePath() string {
	if g.cfg.StateDir == "" {
		return ""
	}
	return filepath.Join(g.cfg.StateDir, "im-"+safeFile(g.name)+"-threads.json")
}

func safeFile(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, s)
}

func (g *Gateway) loadBindings() {
	p := g.statePath()
	if p == "" {
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var list []savedBinding
	if json.Unmarshal(b, &list) != nil {
		return
	}
	for _, x := range list {
		bd := &binding{id: x.ID, agent: x.Agent, session: x.Session, chat: x.Chat, anchor: x.Anchor, listen: x.Listen}
		g.byID[bd.id] = bd
		g.order = append(g.order, bd.id)
		for _, m := range x.Msgs {
			g.bindings[bkey(x.Chat, m)] = bd
		}
	}
}

// saveSoon writes the bindings at most every two seconds. Caller holds g.mu.
func (g *Gateway) saveSoon() {
	if g.statePath() == "" || g.saveTimer != nil {
		return
	}
	g.saveTimer = time.AfterFunc(2*time.Second, func() {
		g.mu.Lock()
		g.saveTimer = nil
		list := make([]savedBinding, 0, len(g.order))
		msgs := map[string][]string{}
		for k, b := range g.bindings {
			if _, m, ok := strings.Cut(k, "\x00"); ok {
				msgs[b.id] = append(msgs[b.id], m)
			}
		}
		for _, id := range g.order {
			b := g.byID[id]
			if b == nil {
				continue
			}
			sort.Strings(msgs[id])
			list = append(list, savedBinding{ID: b.id, Agent: b.agent, Session: b.session, Chat: b.chat, Anchor: b.anchor, Msgs: msgs[id], Listen: b.listen})
		}
		g.mu.Unlock()
		data, err := json.Marshal(list)
		if err != nil {
			return
		}
		p := g.statePath()
		if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
			return
		}
		tmp := p + ".tmp"
		if os.WriteFile(tmp, data, 0o600) == nil {
			_ = os.Rename(tmp, p)
		}
	})
}

// Name returns the gateway name.
func (g *Gateway) Name() string { return g.name }

// Config returns the gateway config.
func (g *Gateway) Config() Config { return g.cfg }

// Start creates a gateway, builds its transport with mk, registers the
// notify channel, approval responder and listeners on c, provides it as
// "im.<name>" and connects in the background. Everything is undone (and
// the connection closed) when c disposes.
func Start(c *kernel.Context, provider string, cfg Config, mk func(g *Gateway) (Transport, error)) (*Gateway, error) {
	g := New(c, provider, cfg)
	t, err := mk(g)
	if err != nil {
		return nil, g.ScrubErr(err)
	}
	g.t = t
	if len(cfg.AllowUsers) == 0 {
		g.log.Warn("im gateway has no allowUsers: nobody can approve or talk to agents")
	}
	if n, ok := kernel.Lookup[*notify.Service](c, notify.ServiceKey); ok {
		n.AddChannel(c, channel{g})
		if len(cfg.NotifyKinds) > 0 {
			for _, k := range notify.Kinds {
				n.SetEnabled(k, g.name, slices.Contains(cfg.NotifyKinds, k))
			}
		}
	}
	if cfg.approvals() {
		if a, ok := kernel.Lookup[*approval.Service](c, approval.ServiceKey); ok {
			a.AddResponder(c, responder{g})
		}
	}
	approval.Decided.Listen(c, g.onDecided)
	agentloop.TurnEnded.Listen(c, g.onTurnEnded)
	if cfg.RelayReplies {
		agentloop.Request.Use(c, g.chatNorm)
	}
	if err := c.Provide(ServicePrefix+g.name, g); err != nil {
		return nil, err
	}
	c.Go(g.run)
	return g, nil
}

// Resolve resolves a credential reference and remembers the value so it is
// scrubbed from errors. The value must never be logged.
func (g *Gateway) Resolve(ctx context.Context, ref string) (string, error) {
	if ref == "" {
		return "", errors.New("credential reference not configured")
	}
	cs, ok := kernel.Lookup[*credentials.Service](g.c, credentials.ServiceKey)
	if !ok {
		return "", errors.New("credentials service unavailable")
	}
	v, err := cs.Resolve(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", ref, err)
	}
	g.AddSecret(v)
	return v, nil
}

// AddSecret registers a value to scrub from errors and logs.
func (g *Gateway) AddSecret(v string) {
	if v == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !slices.Contains(g.secrets, v) {
		g.secrets = append(g.secrets, v)
	}
}

// Scrub replaces every known secret in s.
func (g *Gateway) Scrub(s string) string {
	g.mu.Lock()
	secs := append([]string(nil), g.secrets...)
	g.mu.Unlock()
	for _, v := range secs {
		s = strings.ReplaceAll(s, v, "***")
	}
	return s
}

// ScrubErr returns err with secrets scrubbed from its message (nil stays nil).
func (g *Gateway) ScrubErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if s := g.Scrub(msg); s != msg {
		return errors.New(s)
	}
	return err
}

// Logger returns the gateway's logger. Never log tokens.
func (g *Gateway) Logger() *slog.Logger { return g.log }

func (g *Gateway) run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := g.t.Run(ctx, g)
		if ctx.Err() != nil {
			break
		}
		g.Connected(false, err)
		if err != nil {
			g.log.Warn("im connection lost", "err", g.Scrub(err.Error()))
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
	g.Connected(false, nil)
}

// Connected implements Events.
func (g *Gateway) Connected(ok bool, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ok != g.connected {
		g.since = g.now().UTC()
	}
	g.connected = ok
	g.lastErr = ""
	if err != nil {
		g.lastErr = err.Error()
	}
	for _, v := range g.secrets {
		g.lastErr = strings.ReplaceAll(g.lastErr, v, "***")
	}
}

// Status returns the public state.
func (g *Gateway) Status() Status {
	g.mu.Lock()
	defer g.mu.Unlock()
	return Status{
		Name: g.name, Provider: g.provider, Plugin: g.c.PluginID(),
		Connected: g.connected, Error: g.lastErr, Since: g.since,
		Channel: g.cfg.Channel, AllowUsers: len(g.cfg.AllowUsers),
		RelayReplies: g.cfg.RelayReplies, Approvals: g.cfg.approvals(),
		QuietHours: g.cfg.QuietHours, Pending: len(g.pending),
	}
}

// Allowed reports whether a platform user id is on the allowlist.
func (g *Gateway) Allowed(userID string) bool {
	return userID != "" && slices.Contains(g.cfg.AllowUsers, userID)
}

// Post sends a rate-limited message.
func (g *Gateway) Post(ctx context.Context, m Message) (Ref, error) {
	if m.Chat == "" {
		m.Chat = g.cfg.Channel
	}
	if m.Chat == "" {
		return Ref{}, errors.New("im: no channel configured")
	}
	if err := g.limiter.Wait(ctx); err != nil {
		return Ref{}, err
	}
	ref, err := g.t.Post(ctx, m)
	return ref, g.ScrubErr(err)
}

// Edit updates a posted message (rate-limited).
func (g *Gateway) Edit(ctx context.Context, ref Ref, m Message) error {
	if err := g.limiter.Wait(ctx); err != nil {
		return err
	}
	return g.ScrubErr(g.t.Edit(ctx, ref, m))
}

// Test posts a test message to the configured channel.
func (g *Gateway) Test(ctx context.Context) (Ref, error) {
	return g.Post(ctx, Message{Title: "Crewbase 测试消息", Body: "IM 网关「" + g.name + "」已连接，通知和审批会发到这里。"})
}

func (g *Gateway) agentName(id string) string {
	if id == "" {
		return ""
	}
	if r, ok := kernel.Lookup[*agent.Registry](g.c, agent.ServiceKey); ok {
		if a, ok := r.Get(id); ok {
			return a.Spec().Name
		}
	}
	return id
}

// later runs fn in the gateway's lifetime (async side effects of
// synchronous events must not block the emitter).
func (g *Gateway) later(fn func(ctx context.Context)) {
	g.c.Go(func(ctx context.Context) {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		fn(cctx)
	})
}

// ---------------------------------------------------------------- notify

type channel struct{ g *Gateway }

func (ch channel) Name() string { return ch.g.name }

func (ch channel) Send(ctx context.Context, n notify.Notification) error {
	g := ch.g
	if !n.Urgent && g.cfg.QuietHours.Active(g.now()) {
		return nil
	}
	// An approval card already carries the buttons for this request.
	if n.Kind == notify.KindNeedsYou && g.cfg.approvals() && g.awaitCard(ctx, n.Agent, n.Session, 2*time.Second) {
		return nil
	}
	_, err := g.Post(ctx, Message{Title: n.Title, Body: n.Body, Agent: g.agentName(n.Agent)})
	return err
}

// awaitCard waits up to d for an approval card for agent+session posted
// in the last minute.
func (g *Gateway) awaitCard(ctx context.Context, agentID, sess string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	stop := context.AfterFunc(ctx, func() { g.mu.Lock(); g.cardWait.Broadcast(); g.mu.Unlock() })
	defer stop()
	t := time.AfterFunc(d, func() { g.mu.Lock(); g.cardWait.Broadcast(); g.mu.Unlock() })
	defer t.Stop()
	g.mu.Lock()
	defer g.mu.Unlock()
	for {
		for _, p := range g.pending {
			if p.req.Agent == agentID && p.req.Session == sess && !p.posted.IsZero() && time.Since(p.posted) < time.Minute {
				return true
			}
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return false
		}
		g.cardWait.Wait()
	}
}

// ---------------------------------------------------------------- approval

type responder struct{ g *Gateway }

func (r responder) Name() string { return "im:" + r.g.name }

func (r responder) Respond(ctx context.Context, req approval.Request) (approval.Decision, bool) {
	return r.g.respond(ctx, req)
}

func newToken() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ActionValue encodes an approval button payload (≤ 32 bytes, fits
// Telegram's 64-byte callback_data).
func ActionValue(token, verb string) string { return "ap:" + token + ":" + verb }

func parseAction(v string) (token, verb string, ok bool) {
	rest, ok := strings.CutPrefix(v, "ap:")
	if !ok {
		return "", "", false
	}
	token, verb, ok = strings.Cut(rest, ":")
	return token, verb, ok
}

// ApprovalCard builds the card for req. Push/payment requests have no
// approve button; "以后都允许" is never offered in IM.
func ApprovalCard(req approval.Request, agentName, token string) Message {
	if agentName == "" {
		agentName = req.Agent
	}
	var body strings.Builder
	body.WriteString(req.Summary)
	if req.Reason != "" {
		body.WriteString("\n规则：" + req.Reason)
	}
	if req.Category != "" {
		body.WriteString("\n类别：" + req.Category)
	}
	m := Message{Title: agentName + " 等你批准", Body: body.String(), Agent: agentName}
	if !NoApprove(req.Category) {
		m.Buttons = append(m.Buttons, Button{Value: ActionValue(token, VerbAllow), Label: LabelAllow, Style: "primary"})
	} else {
		m.Body += "\n（这类操作只能在桌面批准）"
	}
	m.Buttons = append(m.Buttons,
		Button{Value: ActionValue(token, VerbDeny), Label: LabelDeny, Style: "danger"},
		Button{Value: ActionValue(token, VerbLater), Label: LabelLater},
	)
	return m
}

func (g *Gateway) respond(ctx context.Context, req approval.Request) (approval.Decision, bool) {
	tok := newToken()
	p := &pendingCard{req: req, noApprove: NoApprove(req.Category), ch: make(chan verdict, 1)}
	g.mu.Lock()
	g.pending[tok] = p
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		delete(g.pending, tok)
		g.mu.Unlock()
	}()

	card := ApprovalCard(req, g.agentName(req.Agent), tok)
	ref, err := g.Post(ctx, card)
	if err != nil {
		if ctx.Err() == nil {
			g.log.Warn("post approval card", "err", err.Error())
		}
		return approval.Decision{}, false
	}
	g.mu.Lock()
	p.ref, p.posted = ref, time.Now()
	g.cardWait.Broadcast()
	g.mu.Unlock()

	done := func(footer string) Message {
		m := card
		m.Buttons, m.Footer = nil, footer
		return m
	}
	select {
	case v := <-p.ch:
		by := "im:" + g.name + ":" + v.user.ID
		switch v.verb {
		case VerbAllow:
			g.later(func(ctx context.Context) { g.editLog(ctx, ref, done("✅ 已批准 · "+v.user.display())) })
			return approval.Decision{Outcome: approval.OutcomeAllowedOnce, By: by}, true
		case VerbDeny:
			g.later(func(ctx context.Context) { g.editLog(ctx, ref, done("❌ 已拒绝 · "+v.user.display())) })
			return approval.Decision{Outcome: approval.OutcomeRejected, Message: "rejected in " + g.name + " by " + v.user.display(), By: by}, true
		default: // later: leave it to the desktop inbox
			g.awaitOutcome(req.CallID, ref, card)
			g.later(func(ctx context.Context) {
				g.editLog(ctx, ref, done("🖥 稍后在桌面处理 · "+v.user.display()))
			})
			return approval.Decision{}, false
		}
	case <-ctx.Done():
		// Decided elsewhere (or cancelled / timed out): approval.Decided
		// tells us the outcome and we update the card then.
		g.awaitOutcome(req.CallID, ref, card)
		return approval.Decision{}, false
	}
}

func (g *Gateway) editLog(ctx context.Context, ref Ref, m Message) {
	if err := g.Edit(ctx, ref, m); err != nil && ctx.Err() == nil {
		g.log.Debug("edit card", "err", err.Error())
	}
}

type closedCard struct {
	ref  Ref
	card Message
}

func (g *Gateway) awaitOutcome(callID string, ref Ref, card Message) {
	g.mu.Lock()
	if d, ok := g.early[callID]; ok {
		// The decision was announced before this responder noticed it
		// lost the race: apply it now.
		delete(g.early, callID)
		g.mu.Unlock()
		g.applyOutcome(closedCard{ref: ref, card: card}, d)
		return
	}
	defer g.mu.Unlock()
	if len(g.closed) > 500 {
		clear(g.closed)
	}
	g.closed[callID] = closedCard{ref: ref, card: card}
}

func (g *Gateway) onDecided(d approval.DecidedData) {
	g.mu.Lock()
	cc, ok := g.closed[d.CallID]
	delete(g.closed, d.CallID)
	if !ok {
		// Our card may still be open (the responder has not seen its
		// context end yet): remember the outcome for awaitOutcome.
		for _, p := range g.pending {
			if p.req.CallID == d.CallID {
				if g.early == nil {
					g.early = map[string]approval.DecidedData{}
				}
				if len(g.early) > 500 {
					clear(g.early)
				}
				g.early[d.CallID] = d
				break
			}
		}
	}
	g.mu.Unlock()
	if !ok {
		return
	}
	g.applyOutcome(cc, d)
}

func (g *Gateway) applyOutcome(cc closedCard, d approval.DecidedData) {
	ref, card := cc.ref, cc.card
	var footer string
	switch d.Outcome {
	case approval.OutcomeAllowedOnce:
		footer = "✅ 已在其他端批准"
	case approval.OutcomeRejected:
		footer = "❌ 已在其他端拒绝"
	case approval.OutcomeCancelled:
		footer = "⏹ 已取消"
	default:
		footer = "⌛ 已失效（" + string(d.Outcome) + "）"
	}
	if d.By != "" {
		footer += " · " + d.By
	}
	card.Buttons, card.Footer = nil, footer
	g.later(func(ctx context.Context) { g.editLog(ctx, ref, card) })
}

// Action implements Events: an approval button press.
func (g *Gateway) Action(_ context.Context, a Action) string {
	tok, verb, ok := parseAction(a.Value)
	if !ok {
		return ""
	}
	if !g.Allowed(a.User.ID) {
		g.log.Info("im approval press from non-allowed user ignored", "user", a.User.ID)
		return "你没有权限处理这个审批"
	}
	g.mu.Lock()
	p, ok := g.pending[tok]
	if !ok {
		g.mu.Unlock()
		return "这个审批已经处理过了"
	}
	if verb == VerbAllow && p.noApprove {
		g.mu.Unlock()
		return "这类操作只能在桌面批准"
	}
	if verb != VerbAllow && verb != VerbDeny && verb != VerbLater {
		g.mu.Unlock()
		return ""
	}
	delete(g.pending, tok) // first press claims
	g.mu.Unlock()
	p.ch <- verdict{verb: verb, user: a.User}
	switch verb {
	case VerbAllow:
		return "已批准"
	case VerbDeny:
		return "已拒绝"
	}
	return "好的，稍后在桌面处理"
}

// ---------------------------------------------------------------- inbound

const maxBindings = 1000

func bkey(chat, msg string) string { return chat + "\x00" + msg }

func (g *Gateway) bind(b *binding, chat string, msgIDs ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.byID[b.id]; !ok {
		g.byID[b.id] = b
		g.order = append(g.order, b.id)
		if len(g.order) > maxBindings {
			old := g.order[0]
			g.order = g.order[1:]
			delete(g.byID, old)
			for k, x := range g.bindings {
				if x.id == old {
					delete(g.bindings, k)
				}
			}
		}
	}
	for _, id := range msgIDs {
		if id != "" {
			g.bindings[bkey(chat, id)] = b
		}
	}
	g.saveSoon()
}

func (g *Gateway) lookupBinding(chat, msg string) *binding {
	if msg == "" {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	b := g.bindings[bkey(chat, msg)]
	if b != nil && g.byID[b.id] == nil {
		return nil
	}
	return b
}

// Bound reports whether a reply to message msg in chat reaches a
// conversation. A relayed reply is bound once the platform has answered
// with its id, a moment after it shows up in the chat.
func (g *Gateway) Bound(chat, msg string) bool { return g.lookupBinding(chat, msg) != nil }

// reachable: agents a chat may address — not personal chats, and only the
// bound team's when there is one.
func (g *Gateway) reachable(a *agent.Agent) bool {
	sp := a.Spec()
	if sp.Kind == agent.KindChat || sp.Ephemeral {
		return false
	}
	return g.cfg.Team == "" || sp.TeamID() == agent.TeamOf(g.cfg.Team)
}

func (g *Gateway) findAgent(name string) (*agent.Agent, bool) {
	r, ok := kernel.Lookup[*agent.Registry](g.c, agent.ServiceKey)
	if !ok {
		return nil, false
	}
	if a, ok := r.Get(name); ok && g.reachable(a) {
		return a, true
	}
	if a, ok := r.Get(agent.Slug(name)); ok && g.reachable(a) {
		return a, true
	}
	for _, a := range r.List() {
		if strings.EqualFold(a.Spec().Name, name) && g.reachable(a) {
			return a, true
		}
	}
	return nil, false
}

// ParseMention splits "@kai do this" into ("kai", "do this").
func ParseMention(text string) (name, rest string, ok bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "@") || len(text) < 2 {
		return "", "", false
	}
	name, rest, _ = strings.Cut(text[1:], " ")
	if i := strings.IndexAny(name, "\n\t"); i >= 0 {
		rest = name[i+1:] + " " + rest
		name = name[:i]
	}
	name = strings.TrimRight(name, ":：,，")
	return name, strings.TrimSpace(rest), name != ""
}

// Message implements Events.
func (g *Gateway) Message(ctx context.Context, in Inbound) {
	name, rest, mention := ParseMention(in.Text)
	var target *agent.Agent
	var b *binding
	if mention {
		target, _ = g.findAgent(name)
	}
	if target == nil {
		b = g.lookupBinding(in.Chat, in.Thread)
	}
	addressed := in.Direct || target != nil || b != nil
	if !addressed {
		g.listen(in)
		return
	}
	if !g.Allowed(in.User.ID) {
		g.refuse(ctx, in)
		return
	}
	reply := Message{Chat: in.Chat, Thread: in.Thread, ReplyTo: in.MessageID}
	if reply.Thread == "" {
		reply.Thread = in.MessageID
	}
	switch {
	case target != nil:
		if rest == "" {
			reply.Body = "要对 " + target.Spec().Name + " 说什么？用法：@" + target.ID() + " 你的消息"
			g.postLog(ctx, reply)
			return
		}
		anchor := in.Thread
		if anchor == "" {
			anchor = in.MessageID
		}
		b = &binding{id: newToken(), agent: target.ID(), session: target.Spec().Session, chat: in.Chat, anchor: anchor}
		g.bind(b, in.Chat, anchor, in.MessageID)
	case b != nil:
		rest = strings.TrimSpace(in.Text)
		g.bind(b, in.Chat, in.MessageID)
	default:
		if mention {
			reply.Body = "找不到 Agent「" + name + "」。" + g.agentHelp()
		} else {
			reply.Body = "用「@agent 消息」把消息发给某个 Agent。" + g.agentHelp()
		}
		g.postLog(ctx, reply)
		return
	}
	a, ok := g.findAgent(b.agent)
	if !ok || rest == "" {
		return
	}
	from := in.User.display()
	// noted before it is queued: the agent may claim it and build the
	// request (chatNorm) before Enqueue even returns
	id := session.NewID("in")
	g.noteChatInput(id)
	if _, err := a.Enqueue(agent.Input{ID: id, Session: b.session, Text: rest, From: from, Tag: g.tag(b.id, in.MessageID)}); err != nil {
		g.forgetChatInput(id)
		g.log.Warn("im enqueue failed", "agent", b.agent, "err", err.Error())
		reply.Body = "没能把消息交给 " + a.Spec().Name + "，请稍后再试。"
		g.postLog(ctx, reply)
		return
	}
	reply.Thread = b.anchor
	reply.Body = "已转给 " + a.Spec().Name
	if !g.cfg.RelayReplies {
		reply.Body += "（回复请在桌面查看）"
	}
	if ref, err := g.Post(ctx, reply); err == nil {
		g.bind(b, in.Chat, ref.ID)
	}
}

func (g *Gateway) agentHelp() string {
	r, ok := kernel.Lookup[*agent.Registry](g.c, agent.ServiceKey)
	if !ok {
		return ""
	}
	var ids []string
	for _, a := range r.List() {
		if g.reachable(a) {
			ids = append(ids, "@"+a.ID())
		}
	}
	if len(ids) == 0 {
		return ""
	}
	return "可用：" + strings.Join(ids, " ")
}

func (g *Gateway) postLog(ctx context.Context, m Message) {
	if _, err := g.Post(ctx, m); err != nil && ctx.Err() == nil {
		g.log.Debug("im reply", "err", err.Error())
	}
}

// refuse answers a non-allowed user politely, at most every 10 minutes.
func (g *Gateway) refuse(ctx context.Context, in Inbound) {
	g.log.Info("im message from non-allowed user ignored", "user", in.User.ID)
	g.mu.Lock()
	last, seen := g.refused[in.User.ID]
	if seen && g.now().Sub(last) < 10*time.Minute {
		g.mu.Unlock()
		return
	}
	g.refused[in.User.ID] = g.now()
	g.mu.Unlock()
	th := in.Thread
	if th == "" {
		th = in.MessageID
	}
	g.postLog(ctx, Message{Chat: in.Chat, Thread: th, ReplyTo: in.MessageID,
		Body: "抱歉，你还没有被授权使用这个 Crewbase 机器人。请让管理员把你的用户 ID（" + in.User.ID + "）加到 allowUsers。"})
}

func (g *Gateway) tag(bindingID, msgID string) string {
	return "im:" + g.name + ":" + bindingID + ":" + msgID
}

// MaxRelay is the length of one relayed message (runes); longer replies
// are split at paragraph / line / sentence boundaries into at most
// MaxRelayParts messages.
const (
	MaxRelay      = 3500
	MaxRelayParts = 4
)

// SplitReply cuts text into messages of at most n runes, preferring
// paragraph breaks, then line breaks, then sentence ends, then spaces.
// Code fences that a cut would split are closed and reopened. Beyond max
// messages the rest is replaced by a pointer to the desktop.
func SplitReply(text string, n, max int) []string {
	r := []rune(strings.TrimSpace(text))
	var out []string
	for len(r) > 0 {
		if len(out) == max-1 && len(r) > n {
			cut := n - 30
			out = append(out, strings.TrimSpace(string(r[:cut]))+"…（完整回复请在桌面查看）")
			break
		}
		if len(r) <= n {
			out = append(out, string(r))
			break
		}
		cut := bestCut(r[:n])
		out = append(out, strings.TrimSpace(string(r[:cut])))
		r = []rune(strings.TrimLeft(string(r[cut:]), " \n"))
	}
	// keep fenced code blocks balanced across parts
	open := false
	for i, p := range out {
		if open {
			p = "```\n" + p
		}
		if strings.Count(p, "```")%2 == 1 {
			p += "\n```"
			open = !open
		}
		out[i] = p
	}
	if len(out) > 1 {
		for i := range out {
			out[i] = fmt.Sprintf("%s\n（%d/%d）", out[i], i+1, len(out))
		}
	}
	return out
}

func bestCut(r []rune) int {
	s := string(r)
	min := len(r) / 2
	for _, sep := range []string{"\n\n", "\n", "。", "！", "？", ". ", "! ", "? ", "；", "; ", " "} {
		if i := strings.LastIndex(s, sep); i >= 0 {
			at := utf8.RuneCountInString(s[:i+len(sep)])
			if at >= min {
				return at
			}
		}
	}
	return len(r)
}

func (g *Gateway) onTurnEnded(e agentloop.TurnEndEvent) {
	rest, ok := strings.CutPrefix(e.Input.Tag, "im:"+g.name+":")
	if !ok || !g.cfg.RelayReplies {
		return
	}
	bid, msgID, _ := strings.Cut(rest, ":")
	g.mu.Lock()
	b := g.byID[bid]
	g.mu.Unlock()
	if b == nil {
		return
	}
	text := strings.TrimSpace(e.Text)
	switch {
	case e.Status == "failed" || e.Status == "rejected":
		text = "任务没有完成：" + e.Error
	case e.Status == "cancelled" && text == "":
		text = "已停止。"
	}
	if text == "" {
		return
	}
	if b.listen != "" {
		if quiet(text) {
			return // the listening agent had nothing to add
		}
		g.bind(b, b.chat, b.listen) // replies to that message now reach it
	}
	parts := SplitReply(text, MaxRelay, MaxRelayParts)
	g.later(func(ctx context.Context) {
		reply := msgID
		for _, p := range parts {
			m := Message{Chat: b.chat, Thread: b.anchor, ReplyTo: reply, Agent: g.agentName(b.agent), Body: p}
			ref, err := g.Post(ctx, m)
			if err != nil {
				if ctx.Err() == nil {
					g.log.Warn("im relay reply", "err", err.Error())
				}
				return
			}
			g.bind(b, b.chat, ref.ID)
		}
	})
}

// ChatNorm is added to the system prompt of turns that answer a chat
// message: the reply is posted back to the chat, where people read it like
// a colleague's message, not a terminal transcript.
const ChatNorm = "This turn answers a message from a team chat (%s); your final reply is posted back to that chat thread. " +
	"Write like a colleague in chat: lead with the result, or with the one question you need answered. Keep it to about 100 words, " +
	"with no headings, no long lists and no step-by-step account of what you did. Put long detail (logs, full reports, diffs) in a workspace file " +
	"and give its path. If nothing needs the person's attention, say so in one line."

func (g *Gateway) noteChatInput(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.chatInputs == nil {
		g.chatInputs = map[string]time.Time{}
	}
	now := g.now()
	g.chatInputs[id] = now
	if len(g.chatInputs) > 512 {
		for k, t := range g.chatInputs {
			if now.Sub(t) > 24*time.Hour {
				delete(g.chatInputs, k)
			}
		}
	}
}

func (g *Gateway) forgetChatInput(id string) {
	g.mu.Lock()
	delete(g.chatInputs, id)
	g.mu.Unlock()
}

func (g *Gateway) chatNorm(ctx context.Context, args agentloop.RequestArgs, next kernel.Next[agentloop.RequestArgs, *llm.Request]) (*llm.Request, error) {
	req, err := next(ctx, args)
	if err != nil || req == nil || args.Agent == nil {
		return req, err
	}
	g.mu.Lock()
	_, mine := g.chatInputs[args.Agent.CurrentInput()]
	g.mu.Unlock()
	if mine {
		req.System = strings.TrimRight(req.System, "\n") + "\n\n" + fmt.Sprintf(ChatNorm, g.platform())
	}
	return req, nil
}

func (g *Gateway) platform() string {
	switch g.provider {
	case "slack":
		return "Slack"
	case "telegram":
		return "Telegram"
	case "discord":
		return "Discord"
	case "feishu":
		return "Feishu / Lark"
	}
	return g.provider
}

// quiet reports a listening agent's "nothing to add".
func quiet(text string) bool {
	t := strings.Trim(strings.TrimSpace(text), "`*_.。 \n")
	return t == NoReply || strings.HasPrefix(t, NoReply+"\n") || strings.HasSuffix(t, "\n"+NoReply)
}

// listen hands a message nobody addressed to the listening agent, after
// the program filter (fast path); the agent decides whether to speak.
func (g *Gateway) listen(in Inbound) {
	l := g.cfg.Listen
	if l == nil || l.Agent == "" || in.Chat != g.cfg.Channel || !g.Allowed(in.User.ID) {
		return
	}
	text := strings.TrimSpace(in.Text)
	min := l.MinChars
	if min <= 0 {
		min = 8
	}
	if utf8.RuneCountInString(text) < min {
		return
	}
	if len(l.Keywords) > 0 {
		low := strings.ToLower(text)
		hit := false
		for _, k := range l.Keywords {
			if k = strings.ToLower(strings.TrimSpace(k)); k != "" && strings.Contains(low, k) {
				hit = true
				break
			}
		}
		if !hit {
			return
		}
	}
	g.mu.Lock()
	if g.listenLim == nil {
		per := l.PerHour
		if per <= 0 {
			per = 20
		}
		g.listenLim = rate.NewLimiter(rate.Every(time.Hour/time.Duration(per)), 3)
	}
	ok := g.listenLim.Allow()
	g.mu.Unlock()
	if !ok {
		return
	}
	a, found := g.findAgent(l.Agent)
	if !found {
		return
	}
	anchor := in.Thread
	if anchor == "" {
		anchor = in.MessageID
	}
	b := &binding{id: newToken(), agent: a.ID(), session: a.Spec().Session, chat: in.Chat, anchor: anchor, listen: in.MessageID}
	g.bind(b, in.Chat) // known by id (for the reply), not bound to the thread yet
	from := in.User.display()
	id := session.NewID("in") // noted first: see the mention path
	g.noteChatInput(id)
	if _, err := a.Enqueue(agent.Input{ID: id, Session: b.session, Text: fmt.Sprintf(ListenPrompt, from, text), From: from,
		Tag: g.tag(b.id, in.MessageID), Mode: agent.ModeReadOnly}); err != nil {
		g.forgetChatInput(id)
		g.log.Warn("im listen enqueue failed", "agent", a.ID(), "err", err.Error())
	}
}
