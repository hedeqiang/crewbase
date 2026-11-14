package discord_test

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/bwmarrin/discordgo"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/features/im"
	"github.com/hedeqiang/crewbase/features/im/imtest"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/im/discord"
)

type fakeAPI struct {
	mu        sync.Mutex
	sent      []*discordgo.MessageSend
	edits     []*discordgo.MessageEdit
	responses []*discordgo.InteractionResponse
	seq       int
}

func (f *fakeAPI) ChannelMessageSendComplex(ch string, d *discordgo.MessageSend, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	f.sent = append(f.sent, d)
	return &discordgo.Message{ID: "m" + strconv.Itoa(f.seq), ChannelID: ch}, nil
}

func (f *fakeAPI) ChannelMessageEditComplex(m *discordgo.MessageEdit, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits = append(f.edits, m)
	return &discordgo.Message{ID: m.ID, ChannelID: m.Channel}, nil
}

func (f *fakeAPI) InteractionRespond(_ *discordgo.Interaction, r *discordgo.InteractionResponse, _ ...discordgo.RequestOption) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses = append(f.responses, r)
	return nil
}

func (f *fakeAPI) snapshot() ([]*discordgo.MessageSend, []*discordgo.MessageEdit, []*discordgo.InteractionResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*discordgo.MessageSend(nil), f.sent...), append([]*discordgo.MessageEdit(nil), f.edits...),
		append([]*discordgo.InteractionResponse(nil), f.responses...)
}

// offline wraps the real transport but skips the Gateway connection.
type offline struct {
	*discord.Transport
	ev chan im.Events
}

func (o offline) Run(ctx context.Context, ev im.Events) error {
	ev.Connected(true, nil)
	o.ev <- ev
	<-ctx.Done()
	return nil
}

func TestRender(t *testing.T) {
	card := im.ApprovalCard(approval.Request{Agent: "kai", Category: "exec", Summary: "deploy"}, "Kai", "tok")
	embeds, comps := discord.Render(card)
	row := comps[0].(discordgo.ActionsRow)
	if embeds[0].Title != "Kai 等你批准" || len(row.Components) != 3 {
		t.Fatalf("%+v %+v", embeds[0], row)
	}
	if b := row.Components[0].(discordgo.Button); b.Style != discordgo.SuccessButton || b.CustomID != "ap:tok:allow" {
		t.Fatalf("%+v", b)
	}
	_, comps = discord.Render(im.ApprovalCard(approval.Request{Category: "push"}, "Kai", "tok"))
	for _, c := range comps[0].(discordgo.ActionsRow).Components {
		if c.(discordgo.Button).Label == im.LabelAllow {
			t.Fatal("push must not offer approve")
		}
	}
	in, ok := discord.Inbound(&discordgo.Message{ID: "1", ChannelID: "c", GuildID: "g", Content: "<@B> @kai hi", Author: &discordgo.User{ID: "u", Username: "ada"}}, "B")
	if !ok || !in.Direct || in.Text != "@kai hi" || in.User.Name != "ada" {
		t.Fatalf("%+v", in)
	}
	if _, ok := discord.Inbound(&discordgo.Message{Content: "x", Author: &discordgo.User{ID: "B", Bot: true}}, "B"); ok {
		t.Fatal("bot messages dropped")
	}
}

func TestDiscordApprovalAndChat(t *testing.T) {
	e := imtest.New(t)
	api := &fakeAPI{}
	evs := make(chan im.Events, 1)
	var tr *discord.Transport
	spec := kernel.Spec{Name: "discord-test", Apply: func(c *kernel.Context) error {
		cfg := im.Config{Channel: "C1", AllowUsers: []string{"u7"}, RelayReplies: true}
		_, err := im.Start(c, "discord", cfg, func(g *im.Gateway) (im.Transport, error) {
			tr = discord.New(g, cfg)
			tr.SetAPI(api, "BOT")
			return offline{tr, evs}, nil
		})
		return err
	}}
	if _, err := e.Host.Load(spec, kernel.LoadOptions{}); err != nil {
		t.Fatal(err)
	}
	ev := imtest.Recv(t, (<-chan im.Events)(evs))

	kai := e.Agent("Kai")
	done := e.RunTool(kai, "deploy")
	imtest.Eventually(t, func() bool { s, _, _ := api.snapshot(); return len(s) == 1 })
	sent, _, _ := api.snapshot()
	allow := sent[0].Components[0].(discordgo.ActionsRow).Components[0].(discordgo.Button).CustomID
	press := func(uid string) {
		tr.OnInteraction(context.Background(), ev, &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
			Type: discordgo.InteractionMessageComponent, ChannelID: "C1", Message: &discordgo.Message{ID: "m1"},
			Member: &discordgo.Member{User: &discordgo.User{ID: uid, Username: uid}},
			Data:   discordgo.MessageComponentInteractionData{CustomID: allow, ComponentType: discordgo.ButtonComponent}}})
	}
	press("stranger")
	_, _, rs := api.snapshot()
	if len(rs) != 1 || rs[0].Data == nil || !strings.Contains(rs[0].Data.Content, "没有权限") || rs[0].Data.Flags != discordgo.MessageFlagsEphemeral {
		t.Fatalf("%+v", rs)
	}
	press("u7")
	if exe := imtest.Recv(t, done); exe.Outcome != approval.OutcomeAllowedOnce {
		t.Fatal(exe.Outcome)
	}
	imtest.Eventually(t, func() bool {
		_, eds, _ := api.snapshot()
		return len(eds) == 1 && len(*eds[0].Components) == 0
	})

	queued := make(chan agent.Input, 2)
	agent.QueueChanged.Listen(e.Host.Root(), func(string) {
		for _, in := range kai.Queue() {
			queued <- in
		}
	})
	tr.OnMessage(context.Background(), ev, &discordgo.MessageCreate{Message: &discordgo.Message{ID: "x1", ChannelID: "C1", Content: "@kai status?",
		Author: &discordgo.User{ID: "u7", GlobalName: "Ada"}}})
	in := imtest.Recv(t, queued)
	if in.Text != "status?" || in.From != "Ada" {
		t.Fatalf("%+v", in)
	}
	kai.Claim()
	e.EndTurn(kai, in, "all green")
	imtest.Eventually(t, func() bool {
		s, _, _ := api.snapshot()
		last := s[len(s)-1]
		return strings.Contains(last.Embeds[0].Description, "all green") && last.Reference != nil && last.Reference.MessageID == "x1"
	})
}
