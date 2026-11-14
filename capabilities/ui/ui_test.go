package ui_test

import (
	"context"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/capabilities/ui"
	"github.com/hedeqiang/crewbase/kernel"
)

func TestRegisterValidatesAndScopes(t *testing.T) {
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	s := ui.NewService(h)
	c := h.Root().Fork("")
	panel := func(context.Context, string) (ui.Panel, error) {
		return ui.Panel{Sections: []ui.Section{{Title: strings.Repeat("长", 200), Fields: []ui.Field{{Label: "a", Value: "b", Href: "file:///etc/passwd"}}}}}, nil
	}
	bad := []ui.Contribution{
		{Slot: "titlebar.hack", Key: "x"},
		{Slot: ui.SlotConversationNode, Key: "Deploy Result", Card: &ui.Card{Title: "x"}},
		{Slot: ui.SlotConversationNode, Key: "deploy/result"},
		{Slot: ui.SlotConversationNode, Key: "deploy/result", Card: &ui.Card{Title: "x", Fields: []ui.Field{{Label: "l", Value: "v", Href: "javascript:alert(1)"}}}},
		{Slot: ui.SlotConversationNode, Key: "deploy/result", Card: &ui.Card{Title: "x", Tone: "rainbow"}},
		{Slot: ui.SlotSettingsCard, Key: "k", Title: "t"},
		{Slot: ui.SlotPaletteCommand, Key: "k", Title: "t"},
	}
	for _, x := range bad {
		if err := s.Register(c, x); err == nil {
			t.Fatalf("accepted %+v", x)
		}
	}
	changed := 0
	ui.Changed.Listen(h.Root(), func(struct{}) { changed++ })
	if err := s.Register(c, ui.Contribution{Slot: ui.SlotSettingsCard, Key: "audit", Title: "审计", Panel: panel}); err != nil {
		t.Fatal(err)
	}
	if err := s.Register(h.Root(), ui.Contribution{Slot: ui.SlotSettingsCard, Key: "audit", Title: "again", Panel: panel}); err == nil {
		t.Fatal("duplicate key accepted")
	}
	p, err := s.Panel(context.Background(), ui.SlotSettingsCard, "audit", "")
	if err != nil || len([]rune(p.Sections[0].Title)) != 80 || p.Sections[0].Fields[0].Href != "" {
		t.Fatalf("%v %+v", err, p)
	}
	c.Dispose()
	if len(s.List()) != 0 || changed < 2 {
		t.Fatalf("%d %d", len(s.List()), changed)
	}
}
