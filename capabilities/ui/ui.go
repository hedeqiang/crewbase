// Package ui defines the `ui` capability: interface slots plugins
// contribute to (docs/05 §8). Contributions are declarative — cards,
// panels and commands described as data and rendered by the desktop UI —
// so a plugin's interface never runs plugin code in the client, never sees
// `ctx`, and renders the same on every device and on replay.
//
// Slots:
//
//	conversation.node  keyed by session event type: a card rendered from that event's data;
//	                   or keyed "tool:<name>": a card for each result of that tool, rendered
//	                   from {args, result, isError} — result is the tool's content parsed as
//	                   JSON (else {text}). Both come from the session log after redaction,
//	                   so out-of-process plugins get cards without writing session events.
//	sidebar-right.tab  list: a tab in the right panel, content is a Panel fetched per session
//	settings.card      list: a card in settings › 插件与技能, content is a Panel
//	palette.command    list: a ⌘K command
//	composer.action    list: a button in the message composer (runs a command)
//
// Every contribution is bound to the registering context: unloading the
// plugin removes it. Keyed slots refuse a second owner of the same key
// (to replace a built-in, a plugin must use a new key).
package ui

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/hedeqiang/crewbase/kernel"
)

const ServiceKey = "ui"

// Slots.
const (
	SlotConversationNode = "conversation.node"
	SlotSidebarTab       = "sidebar-right.tab"
	SlotSettingsCard     = "settings.card"
	SlotPaletteCommand   = "palette.command"
	SlotComposerAction   = "composer.action"
)

// Slots lists the known slots.
var Slots = []string{SlotConversationNode, SlotSidebarTab, SlotSettingsCard, SlotPaletteCommand, SlotComposerAction}

// Tones of a card.
const (
	ToneInfo  = "info"
	ToneOK    = "ok"
	ToneWarn  = "warn"
	ToneError = "error"
)

// Field is one label / value row. Value and Href may hold {{path}}
// placeholders resolved against the event data (conversation.node only);
// values are always shown as plain text.
type Field struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Href  string `json:"href,omitempty"` // http(s) links only
	Mono  bool   `json:"mono,omitempty"`
}

// Section groups fields in a Panel.
type Section struct {
	Title  string  `json:"title,omitempty"`
	Text   string  `json:"text,omitempty"` // a short paragraph (plain text)
	Fields []Field `json:"fields,omitempty"`
}

// Panel is the content of a tab or settings card.
type Panel struct {
	Sections []Section `json:"sections"`
	// Empty is shown when there are no sections.
	Empty string `json:"empty,omitempty"`
}

// Card renders a session event (conversation.node).
type Card struct {
	Title  string  `json:"title"` // may use {{path}}
	Tone   string  `json:"tone,omitempty"`
	Icon   string  `json:"icon,omitempty"`
	Fields []Field `json:"fields,omitempty"`
}

// Result of running a command.
type Result struct {
	Toast string `json:"toast,omitempty"`
	// Session to open afterwards (e.g. a report the command produced).
	Open string `json:"open,omitempty"`
}

// Lang is the viewer's interface language ("zh" | "en"), for panels and
// commands that word their output (from the page's request; "" = zh).
func Lang(ctx context.Context) string {
	if l, _ := ctx.Value(langKey{}).(string); l == "en" {
		return "en"
	}
	return "zh"
}

type langKey struct{}

// WithLang carries the viewer's language to Panel and Run.
func WithLang(ctx context.Context, lang string) context.Context {
	return context.WithValue(ctx, langKey{}, lang)
}

// Contribution is one entry in a slot.
type Contribution struct {
	Slot  string `json:"slot"`
	Key   string `json:"key"` // event type for conversation.node, else a unique id
	Title string `json:"title,omitempty"`
	Desc  string `json:"desc,omitempty"`
	// TitleEn / DescEn: the same in English (shown to English viewers).
	TitleEn string `json:"titleEn,omitempty"`
	DescEn  string `json:"descEn,omitempty"`
	Icon    string `json:"icon,omitempty"` // a lucide icon name; unknown names fall back
	Order   int    `json:"order,omitempty"`
	// Owner is the registering plugin (defaults to the context's plugin id;
	// the process bridge sets the external plugin's name).
	Owner string `json:"owner"`

	Card *Card `json:"card,omitempty"` // conversation.node

	// Panel fills sidebar-right.tab (session = current conversation) and
	// settings.card (session = "").
	Panel func(ctx context.Context, session string) (Panel, error) `json:"-"`
	// Run executes palette.command / composer.action.
	Run func(ctx context.Context, session string) (Result, error) `json:"-"`
	// Confirm asks the person before Run (shown as the confirmation text).
	Confirm string `json:"confirm,omitempty"`
}

var keyRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_./:-]{0,79}$`)

// Changed notifies that contributions changed (no delta).
var Changed = kernel.NewEmit[struct{}]("ui/changed")

// Service is the `ui` service.
type Service struct {
	h  *kernel.Host
	mu sync.Mutex
	// all contributions in registration order
	items []*Contribution
}

// NewService makes an empty service.
func NewService(h *kernel.Host) *Service { return &Service{h: h} }

func (c *Contribution) validate() error {
	if !slices.Contains(Slots, c.Slot) {
		return fmt.Errorf("unknown ui slot %q", c.Slot)
	}
	if !keyRe.MatchString(c.Key) {
		return fmt.Errorf("ui key %q must be letters, digits and _./:- (max 80)", c.Key)
	}
	switch c.Slot {
	case SlotConversationNode:
		if c.Card == nil || strings.TrimSpace(c.Card.Title) == "" {
			return errors.New("conversation.node needs a card with a title")
		}
		if c.Card.Tone != "" && !slices.Contains([]string{ToneInfo, ToneOK, ToneWarn, ToneError}, c.Card.Tone) {
			return fmt.Errorf("unknown card tone %q", c.Card.Tone)
		}
		for _, f := range c.Card.Fields {
			if err := checkHref(f.Href, true); err != nil {
				return err
			}
		}
	case SlotSidebarTab, SlotSettingsCard:
		if strings.TrimSpace(c.Title) == "" || c.Panel == nil {
			return fmt.Errorf("%s needs a title and a panel", c.Slot)
		}
	case SlotPaletteCommand, SlotComposerAction:
		if strings.TrimSpace(c.Title) == "" || c.Run == nil {
			return fmt.Errorf("%s needs a title and a run handler", c.Slot)
		}
	}
	return nil
}

// checkHref allows only http(s) links (and {{placeholders}} in templates).
func checkHref(h string, template bool) error {
	if h == "" {
		return nil
	}
	if template && strings.HasPrefix(h, "{{") {
		return nil
	}
	if strings.HasPrefix(h, "https://") || strings.HasPrefix(h, "http://") {
		return nil
	}
	return fmt.Errorf("link %q must be http(s)", h)
}

// Register adds a contribution for c's lifetime.
func (s *Service) Register(c *kernel.Context, x Contribution) error {
	if err := x.validate(); err != nil {
		return err
	}
	if x.Owner == "" {
		x.Owner = c.PluginID()
	}
	s.mu.Lock()
	for _, y := range s.items {
		if y.Slot == x.Slot && y.Key == x.Key {
			s.mu.Unlock()
			return fmt.Errorf("ui %s %q is already provided by %s", x.Slot, x.Key, y.Owner)
		}
	}
	p := &x
	s.items = append(s.items, p)
	s.mu.Unlock()
	c.Defer(func() {
		s.mu.Lock()
		s.items = slices.DeleteFunc(s.items, func(y *Contribution) bool { return y == p })
		s.mu.Unlock()
		Changed.Emit(s.h, struct{}{})
	})
	Changed.Emit(s.h, struct{}{})
	return nil
}

// List returns contributions ordered by slot, order, then title.
func (s *Service) List() []Contribution {
	s.mu.Lock()
	out := make([]Contribution, 0, len(s.items))
	for _, x := range s.items {
		out = append(out, *x)
	}
	s.mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Slot != out[j].Slot {
			return slices.Index(Slots, out[i].Slot) < slices.Index(Slots, out[j].Slot)
		}
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].Title < out[j].Title
	})
	return out
}

func (s *Service) get(slot, key string) (*Contribution, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.items {
		if x.Slot == slot && x.Key == key {
			return x, true
		}
	}
	return nil, false
}

// ErrNotFound is returned for an unknown slot / key.
var ErrNotFound = errors.New("ui contribution not found")

// Panel fills a tab or settings card. Plugin links are checked; text is
// capped so a plugin cannot flood the UI.
func (s *Service) Panel(ctx context.Context, slot, key, session string) (Panel, error) {
	x, ok := s.get(slot, key)
	if !ok || x.Panel == nil {
		return Panel{}, ErrNotFound
	}
	p, err := x.Panel(ctx, session)
	if err != nil {
		return Panel{}, err
	}
	return clampPanel(p), nil
}

// Run runs a command.
func (s *Service) Run(ctx context.Context, slot, key, session string) (Result, error) {
	x, ok := s.get(slot, key)
	if !ok || x.Run == nil {
		return Result{}, ErrNotFound
	}
	r, err := x.Run(ctx, session)
	if err != nil {
		return Result{}, err
	}
	r.Toast = clip(r.Toast, 200)
	return r, nil
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func clampPanel(p Panel) Panel {
	if len(p.Sections) > 20 {
		p.Sections = p.Sections[:20]
	}
	for i := range p.Sections {
		sec := &p.Sections[i]
		sec.Title = clip(sec.Title, 80)
		sec.Text = clip(sec.Text, 2000)
		if len(sec.Fields) > 50 {
			sec.Fields = sec.Fields[:50]
		}
		for j := range sec.Fields {
			f := &sec.Fields[j]
			f.Label, f.Value = clip(f.Label, 80), clip(f.Value, 500)
			if checkHref(f.Href, false) != nil {
				f.Href = ""
			}
		}
	}
	p.Empty = clip(p.Empty, 200)
	if p.Sections == nil {
		p.Sections = []Section{}
	}
	return p
}

// Plugin provides the ui service.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/ui",
	Description: "Interface slots: conversation cards, panel tabs, settings cards and commands contributed by plugins.",
	Apply: func(c *kernel.Context) error {
		return c.Provide(ServiceKey, NewService(c.Host()))
	},
})
