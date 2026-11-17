package channel_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/channel"
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

func setup(t *testing.T) (*channel.Service, *agent.Registry) {
	t.Helper()
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	root := t.TempDir()
	for _, x := range []struct {
		s   kernel.Spec
		cfg map[string]any
	}{
		{session.Plugin, nil}, {prompt.Plugin, nil}, {tools.Plugin, nil}, {approval.Plugin, nil},
		{llm.Plugin, nil}, {mock.Plugin, nil}, {router.Plugin, nil},
		{agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"mock/default"}}},
		{agentloop.Plugin, nil}, {channel.Plugin, nil},
	} {
		h.Load(x.s, kernel.LoadOptions{Config: x.cfg})
	}
	return kernel.Use[*channel.Service](h.Root(), channel.ServiceKey), kernel.Use[*agent.Registry](h.Root(), agent.ServiceKey)
}

// handoverCmd builds a message whose last paragraph makes the mock model
// call the handover tool; the note can itself carry the next command.
func handoverCmd(to, note string) string {
	b, _ := json.Marshal(map[string]any{"to": to, "note": note, "files": []string{"notes/" + to + ".md"}})
	return "/tool handover " + string(b)
}

func waitAll(as ...*agent.Agent) {
	for i := 0; i < 3; i++ { // handovers chain across agents
		for _, a := range as {
			agentloop.WaitIdle(a, 2*time.Second)
		}
	}
}

// Phase 2 exit criterion: three agents in one channel complete
// research → implement → review with two handovers.
func TestResearchImplementReview(t *testing.T) {
	s, agents := setup(t)
	nova, _ := agents.Create(agent.Spec{Name: "Nova", Role: "research"})
	kai, _ := agents.Create(agent.Spec{Name: "Kai", Role: "engineering"})
	rex, _ := agents.Create(agent.Spec{Name: "Rex", Role: "review"})
	ch, err := s.Create("#pricing-revamp", []string{nova.ID(), kai.ID(), rex.ID()}, "", "lin")
	if err != nil {
		t.Fatal(err)
	}
	if ch.Owner != "nova" {
		t.Fatal(ch.Owner)
	}
	// Kai's handover to Rex is embedded in Nova's note.
	second := "implemented\n\n" + handoverCmd("Rex", "please review")
	msg := "@Nova research pricing\n\n" + handoverCmd("kai", "research done\n\n"+second)
	res, err := s.Post(ch.ID, msg, "lin")
	if err != nil || res.Agent != "nova" {
		t.Fatal(res, err)
	}
	waitAll(nova, kai, rex)

	v, _ := s.Get(ch.ID)
	if v.Owner != "rex" {
		t.Fatalf("owner %s", v.Owner)
	}
	st, _ := agents.Sessions().Get(ch.ID)
	var handovers []string
	for _, e := range st.Events() {
		if e.Type == channel.TypeHandover {
			var h channel.Handover
			e.Decode(&h)
			handovers = append(handovers, h.From+"->"+h.To)
		}
	}
	if strings.Join(handovers, ",") != "nova->kai,kai->rex" {
		t.Fatalf("handovers %v", handovers)
	}
	// Rex sees earlier agents' messages attributed by name
	msgs := session.DeriveMessages(st.Events(), "rex", agents.Names())
	joined := fmt.Sprint(msgs)
	if !strings.Contains(joined, "[Kai]") {
		t.Fatal("receiver should see shared context")
	}
}

func TestOnlyOwnerCanHandOverAndLifecycle(t *testing.T) {
	s, agents := setup(t)
	a, _ := agents.Create(agent.Spec{Name: "A"})
	b, _ := agents.Create(agent.Spec{Name: "B"})
	ch, _ := s.Create("x", []string{a.ID(), b.ID()}, "", "lin")
	// @b gives b this task only: a stays the owner, so b cannot hand over
	res, _ := s.Post(ch.ID, "@b hi\n\n"+handoverCmd("a", "nope"), "lin")
	if res.Agent != "b" {
		t.Fatalf("@b went to %q", res.Agent)
	}
	waitAll(a, b)
	if v, _ := s.Get(ch.ID); v.Owner != "a" {
		t.Fatal(v.Owner)
	}
	if _, err := s.HandOver(t.Context(), channel.Handover{Channel: ch.ID, From: "b", To: "a"}); err == nil {
		t.Fatal("non-owner handover must fail")
	}
	// a plain message still goes to the owner
	if res, _ := s.Post(ch.ID, "next step", "lin"); res.Agent != "a" {
		t.Fatalf("plain message went to %q", res.Agent)
	}
	waitAll(a, b)
	if err := s.Reassign(ch.ID, "b", "lin", ""); err != nil {
		t.Fatal(err)
	}
	waitAll(a, b)
	if v, _ := s.Get(ch.ID); v.Owner != "b" {
		t.Fatal(v.Owner)
	}
	s.Stop(ch.ID, "lin")
	if v, _ := s.Get(ch.ID); v.State != channel.StatePaused {
		t.Fatal(v.State)
	}
	s.Archive(ch.ID, "lin")
	if _, err := s.Post(ch.ID, "hello", "lin"); err == nil {
		t.Fatal("archived channel must reject posts")
	}
}

func TestHandoverConfirmation(t *testing.T) {
	s, agents := setup(t)
	a, _ := agents.Create(agent.Spec{Name: "A"})
	b, _ := agents.Create(agent.Spec{Name: "B"})
	ch, _ := s.Create("confirm", []string{a.ID(), b.ID()}, "", "lin")
	if err := s.SetSettings(ch.ID, channel.Settings{HandoverConfirm: true}); err != nil {
		t.Fatal(err)
	}
	// no desktop approver loaded in this setup → fail closed
	if _, err := s.HandOver(t.Context(), channel.Handover{Channel: ch.ID, From: "a", To: "b", Note: "x"}); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Fatalf("want rejection, got %v", err)
	}
	if v, _ := s.Get(ch.ID); v.Owner != "a" || !v.Settings.HandoverConfirm {
		t.Fatalf("%+v", v)
	}
}

func TestRenameMoveDelete(t *testing.T) {
	s, agents := setup(t)
	a, _ := agents.Create(agent.Spec{Name: "A"})
	ch, _ := s.Create("old", []string{a.ID()}, "", "lin")
	title, folder := "#new-name", "增长"
	if err := s.Rename(ch.ID, &title, &folder); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Get(ch.ID); v.Title != "new-name" || v.Folder != "增长" {
		t.Fatalf("%+v", v)
	}
	if err := s.Delete(ch.ID, "lin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ch.ID); err == nil {
		t.Fatal("deleted channel still visible")
	}
	if len(s.List()) != 0 {
		t.Fatal("deleted channel listed")
	}
	if _, err := s.Post(ch.ID, "hi", "lin"); err == nil {
		t.Fatal("deleted channel accepts posts")
	}
}

// A deleted channel is gone from the session list people see (the sidebar
// reads it): it is flagged in its meta, also for channels deleted before
// the flag existed, which are flagged when the plugin starts.
func TestDeletedChannelLeavesSessionList(t *testing.T) {
	root := t.TempDir()
	start := func() (*kernel.Host, *channel.Service, *agent.Registry, *session.Store) {
		h := kernel.NewHost(nil)
		for _, x := range []struct {
			s   kernel.Spec
			cfg map[string]any
		}{
			{session.Plugin, map[string]any{"dir": root + "/sessions"}}, {prompt.Plugin, nil}, {tools.Plugin, nil}, {approval.Plugin, nil},
			{llm.Plugin, nil}, {mock.Plugin, nil}, {router.Plugin, nil},
			{agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"mock/default"}}},
			{agentloop.Plugin, nil}, {channel.Plugin, nil},
		} {
			if _, err := h.Load(x.s, kernel.LoadOptions{Config: x.cfg}); err != nil {
				t.Fatal(err)
			}
		}
		r := h.Root()
		return h, kernel.Use[*channel.Service](r, channel.ServiceKey), kernel.Use[*agent.Registry](r, agent.ServiceKey), kernel.Use[*session.Store](r, session.ServiceKey)
	}
	h, s, agents, store := start()
	a, _ := agents.Create(agent.Spec{Name: "A"})
	gone, _ := s.Create("gone", []string{a.ID()}, "", "lin")
	old, _ := s.Create("old", []string{a.ID()}, "", "lin")
	kept, _ := s.Create("kept", []string{a.ID()}, "", "lin")
	if err := s.Delete(gone.ID, "lin"); err != nil {
		t.Fatal(err)
	}
	// deleted by an older version: only the state event, no flag
	ss, _ := store.Get(old.ID)
	if _, err := ss.Append(channel.TypeState, "", map[string]string{"state": channel.StateDeleted, "by": "lin"}); err != nil {
		t.Fatal(err)
	}
	flags := func(st *session.Store) map[string]bool {
		out := map[string]bool{}
		for _, m := range st.List() {
			if m.Kind == "channel" {
				out[m.ID] = m.Deleted
			}
		}
		return out
	}
	if f := flags(store); !f[gone.ID] || f[old.ID] || f[kept.ID] {
		t.Fatalf("before restart: %v", f)
	}
	h.Close()

	h2, s2, _, store2 := start()
	defer h2.Close()
	if f := flags(store2); !f[gone.ID] || !f[old.ID] || f[kept.ID] {
		t.Fatalf("after restart: %v", f)
	}
	if l := s2.List(); len(l) != 1 || l[0].ID != kept.ID {
		t.Fatalf("channels: %+v", l)
	}
}

// One message that @mentions two members reaches both, is recorded once,
// and each sees the other's request only as context.
func TestMessageToTwoMembers(t *testing.T) {
	svc, reg := setup(t)
	a, _ := reg.Create(agent.Spec{Name: "Ana"})
	b, _ := reg.Create(agent.Spec{Name: "Ben"})
	c, _ := reg.Create(agent.Spec{Name: "Cy"})
	ch, err := svc.Create("pair", []string{a.ID(), b.ID(), c.ID()}, "", "desktop")
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.Post(ch.ID, "@Ana count the rows; @Ben write a title", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Agents) != 2 || res.Agents[0] != a.ID() || res.Agents[1] != b.ID() {
		t.Fatalf("delivered to %v", res.Agents)
	}
	waitAll(a, b, c)
	ss, _ := reg.Sessions().Get(ch.ID)
	n := 0
	for _, e := range ss.Events() {
		if e.Type == session.TypeUserMessage {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("message recorded %d times", n)
	}
	turns := map[string]int{}
	for _, e := range ss.Events() {
		if e.Type == session.TypeTurnEnd {
			turns[e.Agent]++
		}
	}
	if turns[a.ID()] != 1 || turns[b.ID()] != 1 || turns[c.ID()] != 0 {
		t.Fatalf("turns %v", turns)
	}
	// Cy was not mentioned: the request is marked as someone else's
	for _, m := range session.DeriveMessages(ss.Events(), c.ID(), reg.Names()) {
		if m.Role == llm.RoleUser && strings.Contains(m.Text(), "count the rows") && !strings.HasPrefix(m.Text(), "[Ana]:") && !strings.HasPrefix(m.Text(), "[Ben]:") && !strings.Contains(m.Text(), "not you") {
			t.Fatalf("not marked for someone else: %q", m.Text())
		}
	}
	for _, m := range session.DeriveMessages(ss.Events(), b.ID(), reg.Names()) {
		// other members' replies also reach Ben as "[Name]: …" (the mock echoes the request); only the request itself counts
		if m.Role == llm.RoleUser && strings.Contains(m.Text(), "count the rows") && !strings.HasPrefix(m.Text(), "[Ana]:") && !strings.HasPrefix(m.Text(), "[Cy]:") &&
			(strings.Contains(m.Text(), "not you") || !strings.Contains(m.Text(), "also sent to Ana")) {
			t.Fatalf("Ben should see the request as shared with Ana: %q", m.Text())
		}
	}
}
