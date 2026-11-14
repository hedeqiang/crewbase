// Package discord is the Discord IM gateway ("@crewbase/im-discord") over
// the Discord Gateway websocket (outbound only; interactions arrive on the
// same connection, so no Interactions Endpoint URL is needed — docs/06 §8).
//
// Config (see im.Config):
//
//	botTokenRef: keychain:crewbase/discord-bot
//	channel:     "123456789012345678"   # channel id for notifications/approvals
//	allowUsers:  ["234567890123456789"] # Discord user ids
//	relayReplies: true
//
// The bot needs the privileged MESSAGE CONTENT intent to read "@kai …"
// messages in guild channels (DMs and @bot mentions work without it).
package discord

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/bwmarrin/discordgo"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/capabilities/notify"
	"github.com/hedeqiang/crewbase/features/im"
	"github.com/hedeqiang/crewbase/kernel"
)

// Plugin registers the Discord gateway.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/im-discord",
	Inject:      []string{notify.ServiceKey, approval.ServiceKey, credentials.ServiceKey},
	Description: "Discord IM gateway: notifications, approvals and chat (Gateway websocket).",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[im.Config](c)
		if err != nil {
			return err
		}
		_, err = im.Start(c, "discord", cfg, func(g *im.Gateway) (im.Transport, error) { return New(g, cfg), nil })
		return err
	},
})

// API is the subset of *discordgo.Session the transport uses (faked in tests).
type API interface {
	ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend, options ...discordgo.RequestOption) (*discordgo.Message, error)
	ChannelMessageEditComplex(m *discordgo.MessageEdit, options ...discordgo.RequestOption) (*discordgo.Message, error)
	InteractionRespond(interaction *discordgo.Interaction, resp *discordgo.InteractionResponse, options ...discordgo.RequestOption) error
}

// Transport implements im.Transport.
type Transport struct {
	g   *im.Gateway
	cfg im.Config

	mu    sync.Mutex
	api   API
	botID string
}

// New creates the transport.
func New(g *im.Gateway, cfg im.Config) *Transport { return &Transport{g: g, cfg: cfg} }

// SetAPI installs an API and bot id (tests; Run does it for real sessions).
func (t *Transport) SetAPI(api API, botID string) {
	t.mu.Lock()
	t.api, t.botID = api, botID
	t.mu.Unlock()
}

func (t *Transport) client() (API, string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.api == nil {
		return nil, "", errors.New("discord: not connected")
	}
	return t.api, t.botID, nil
}

func (t *Transport) err(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("discord %s: %s", op, t.g.Scrub(err.Error()))
}

// Run implements im.Transport.
func (t *Transport) Run(ctx context.Context, ev im.Events) error {
	tok, err := t.g.Resolve(ctx, t.cfg.BotTokenRef)
	if err != nil {
		return err
	}
	s, err := discordgo.New("Bot " + tok)
	if err != nil {
		return t.err("session", err)
	}
	s.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentMessageContent
	s.LogLevel = -1 // discordgo's own logger is silenced; errors are reported via Connected
	s.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		t.SetAPI(s, r.User.ID)
		ev.Connected(true, nil)
	})
	s.AddHandler(func(_ *discordgo.Session, _ *discordgo.Disconnect) {
		ev.Connected(false, errors.New("disconnected (reconnecting)"))
	})
	s.AddHandler(func(_ *discordgo.Session, _ *discordgo.Resumed) { ev.Connected(true, nil) })
	s.AddHandler(func(_ *discordgo.Session, m *discordgo.MessageCreate) { t.OnMessage(ctx, ev, m) })
	s.AddHandler(func(_ *discordgo.Session, i *discordgo.InteractionCreate) { t.OnInteraction(ctx, ev, i) })
	if err := s.Open(); err != nil {
		return t.err("open", err)
	}
	defer func() {
		t.mu.Lock()
		t.api = nil
		t.mu.Unlock()
		_ = s.Close()
	}()
	<-ctx.Done() // discordgo reconnects on its own
	return nil
}

// OnMessage handles MESSAGE_CREATE.
func (t *Transport) OnMessage(ctx context.Context, ev im.Events, m *discordgo.MessageCreate) {
	t.mu.Lock()
	botID := t.botID
	t.mu.Unlock()
	if in, ok := Inbound(m.Message, botID); ok {
		ev.Message(ctx, in)
	}
}

// Inbound converts a message; bot messages are dropped and the bot mention
// stripped.
func Inbound(m *discordgo.Message, botID string) (im.Inbound, bool) {
	if m == nil || m.Author == nil || m.Author.Bot || m.Author.ID == botID || strings.TrimSpace(m.Content) == "" {
		return im.Inbound{}, false
	}
	text := m.Content
	direct := m.GuildID == ""
	if botID != "" {
		for _, mention := range []string{"<@" + botID + ">", "<@!" + botID + ">"} {
			if strings.Contains(text, mention) {
				direct = true
				text = strings.ReplaceAll(text, mention, "")
			}
		}
	}
	in := im.Inbound{User: user(m.Author), Chat: m.ChannelID, MessageID: m.ID, Text: strings.TrimSpace(text), Direct: direct}
	if m.MessageReference != nil && m.MessageReference.MessageID != "" {
		in.Thread = m.MessageReference.MessageID
		if m.ReferencedMessage != nil && m.ReferencedMessage.Author != nil && m.ReferencedMessage.Author.ID == botID {
			in.Direct = true
		}
	}
	return in, true
}

func user(u *discordgo.User) im.User {
	if u == nil {
		return im.User{}
	}
	name := u.GlobalName
	if name == "" {
		name = u.Username
	}
	return im.User{ID: u.ID, Name: name}
}

// OnInteraction handles button presses (message components).
func (t *Transport) OnInteraction(ctx context.Context, ev im.Events, i *discordgo.InteractionCreate) {
	if i.Interaction == nil || i.Type != discordgo.InteractionMessageComponent {
		return
	}
	u := i.User
	if i.Member != nil && i.Member.User != nil {
		u = i.Member.User
	}
	a := im.Action{User: user(u), Value: i.MessageComponentData().CustomID, Ref: im.Ref{Chat: i.ChannelID}}
	if i.Message != nil {
		a.Ref.ID = i.Message.ID
	}
	toast := ev.Action(ctx, a)
	api, _, err := t.client()
	if err != nil {
		return
	}
	resp := &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredMessageUpdate}
	if toast != "" {
		resp = &discordgo.InteractionResponse{Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: toast, Flags: discordgo.MessageFlagsEphemeral}}
	}
	if err := api.InteractionRespond(i.Interaction, resp, discordgo.WithContext(ctx)); err != nil && ctx.Err() == nil {
		t.g.Logger().Debug("discord interaction respond", "err", t.err("interaction", err).Error())
	}
}

// Render builds the embed and button row for m.
func Render(m im.Message) ([]*discordgo.MessageEmbed, []discordgo.MessageComponent) {
	em := &discordgo.MessageEmbed{Title: trunc(m.Title, 256), Description: trunc(m.Body, 4000)}
	if m.Agent != "" {
		em.Footer = &discordgo.MessageEmbedFooter{Text: "Crewbase · " + m.Agent}
	}
	if m.Footer != "" {
		em.Fields = []*discordgo.MessageEmbedField{{Name: "状态", Value: trunc(m.Footer, 1024)}}
	}
	if m.Title == "" && m.Body == "" {
		em.Description = trunc(m.Text(), 4000)
	}
	var comps []discordgo.MessageComponent
	if len(m.Buttons) > 0 {
		row := discordgo.ActionsRow{}
		for _, b := range m.Buttons {
			st := discordgo.SecondaryButton
			switch b.Style {
			case "primary":
				st = discordgo.SuccessButton
			case "danger":
				st = discordgo.DangerButton
			}
			row.Components = append(row.Components, discordgo.Button{Label: b.Label, Style: st, CustomID: b.Value})
		}
		comps = append(comps, row)
	}
	return []*discordgo.MessageEmbed{em}, comps
}

func trunc(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// Post implements im.Transport.
func (t *Transport) Post(ctx context.Context, m im.Message) (im.Ref, error) {
	api, _, err := t.client()
	if err != nil {
		return im.Ref{}, err
	}
	embeds, comps := Render(m)
	send := &discordgo.MessageSend{Embeds: embeds, Components: comps}
	reply := m.ReplyTo
	if reply == "" {
		reply = m.Thread
	}
	if reply != "" {
		f := false
		send.Reference = &discordgo.MessageReference{MessageID: reply, ChannelID: m.Chat, FailIfNotExists: &f}
	}
	msg, err := api.ChannelMessageSendComplex(m.Chat, send, discordgo.WithContext(ctx))
	if err != nil {
		return im.Ref{}, t.err("send", err)
	}
	return im.Ref{Chat: msg.ChannelID, ID: msg.ID, Thread: m.Thread}, nil
}

// Edit implements im.Transport (an empty component list removes buttons).
func (t *Transport) Edit(ctx context.Context, ref im.Ref, m im.Message) error {
	api, _, err := t.client()
	if err != nil {
		return err
	}
	embeds, comps := Render(m)
	if comps == nil {
		comps = []discordgo.MessageComponent{}
	}
	_, err = api.ChannelMessageEditComplex(&discordgo.MessageEdit{ID: ref.ID, Channel: ref.Chat, Embeds: &embeds, Components: &comps}, discordgo.WithContext(ctx))
	return t.err("edit", err)
}
