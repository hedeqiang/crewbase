package compaction_test

import (
	"context"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/compaction"
	"github.com/hedeqiang/crewbase/kernel"
)

func sumsByKind(b compaction.Breakdown) (items, segs map[string]int) {
	items, segs = map[string]int{}, map[string]int{}
	for _, it := range b.Items {
		items[it.Kind] += it.Tokens
	}
	for _, s := range b.Segments {
		if s.Key != "free" {
			segs[s.Key] = s.Tokens
		}
	}
	return items, segs
}

func checkSums(t *testing.T, b compaction.Breakdown) {
	t.Helper()
	items, segs := sumsByKind(b)
	for k, v := range segs {
		if items[k] != v {
			t.Fatalf("items of %s add up to %d, segment is %d (items %+v)", k, items[k], v, b.Items)
		}
	}
	for k := range items {
		if _, ok := segs[k]; !ok {
			t.Fatalf("items of kind %s without a segment", k)
		}
	}
}

func TestItemsAddUpToSegments(t *testing.T) {
	e := newEnv(t, t.TempDir(), nil)
	a, _ := e.agents.Create(agent.Spec{Name: "Lu"})
	ss, _ := a.Session()
	send(t, a, ss.ID(), "hello there")
	send(t, a, ss.ID(), "/tool big_output {}")
	b, err := e.comp.Usage(a.ID(), "")
	if err != nil {
		t.Fatal(err)
	}
	checkSums(t, b)
	var call, tool, sys, msg string
	for _, it := range b.Items {
		switch {
		case it.Kind == "toolio" && it.Label == "big_output":
			call = it.ID
		case it.ID == "tool:big_output":
			tool = it.ID
		case it.Kind == "system" && sys == "":
			sys = it.ID
		case it.Kind == "messages" && it.Label == "hello there":
			msg = it.ID
		}
	}
	if call == "" || tool == "" || sys == "" || msg == "" {
		t.Fatalf("missing items: %+v", b.Items)
	}
	// contents
	c, err := e.comp.Item(a.ID(), "", call)
	if err != nil || len(c.Parts) != 2 || c.Parts[0].Title != "arguments" || !strings.Contains(c.Parts[1].Text, "BEGIN lorem") {
		t.Fatalf("tool call content %+v %v", c, err)
	}
	c, _ = e.comp.Item(a.ID(), "", tool)
	if c.Parts[0].Title != "description" || c.Parts[len(c.Parts)-1].Format != "json" {
		t.Fatalf("tool content %+v", c)
	}
	c, _ = e.comp.Item(a.ID(), "", msg)
	if c.Parts[0].Text != "hello there" {
		t.Fatalf("message content %+v", c)
	}
	c, _ = e.comp.Item(a.ID(), "", sys)
	if c.Parts[0].Format != "markdown" || c.Parts[0].Text == "" {
		t.Fatalf("section content %+v", c)
	}
	if _, err := e.comp.Item(a.ID(), "", "nope"); err == nil {
		t.Fatal("unknown item found")
	}
	if b.Counted || b.Cache != nil {
		t.Fatalf("the mock's estimated usage counted: %+v", b)
	}
}

// A planted secret in a tool result is redacted in the item content.
func TestItemContentIsRedacted(t *testing.T) {
	secret := "ghp_" + strings.Repeat("Z", 30)
	e := newEnv(t, t.TempDir(), nil, func(load func(kernel.Spec, map[string]any)) {
		load(kernel.Spec{Name: "test-secret", Inject: []string{tools.ServiceKey}, Apply: func(c *kernel.Context) error {
			kernel.Use[*tools.Registry](c, tools.ServiceKey).Register(c, &tools.Tool{Name: "leak", Category: tools.CategoryRead,
				Execute: func(_ context.Context, _ tools.Call) (tools.Result, error) { return tools.Text("token=" + secret), nil }})
			return nil
		}}, nil)
	})
	a, _ := e.agents.Create(agent.Spec{Name: "Sec"})
	ss, _ := a.Session()
	send(t, a, ss.ID(), "/tool leak {}")
	b, _ := e.comp.Usage(a.ID(), "")
	for _, it := range b.Items {
		if it.Kind == "toolio" {
			c, err := e.comp.Item(a.ID(), "", it.ID)
			if err != nil {
				t.Fatal(err)
			}
			body := c.Parts[len(c.Parts)-1].Text
			if strings.Contains(body, secret) || !strings.Contains(body, "[redacted]") {
				t.Fatalf("not redacted: %q", body)
			}
			return
		}
	}
	t.Fatal("no tool item")
}

func TestItemsLeaveOutDeferredTools(t *testing.T) {
	e := newEnv(t, t.TempDir(), nil, func(load func(kernel.Spec, map[string]any)) {
		load(kernel.Spec{Name: "test-deferred-items", Inject: []string{tools.ServiceKey}, Apply: func(c *kernel.Context) error {
			reg := kernel.Use[*tools.Registry](c, tools.ServiceKey)
			for i := range 20 {
				reg.Register(c, &tools.Tool{Name: "bulky__tool_" + string(rune('a'+i)), Description: "x", Load: tools.LoadDeferred, Group: "bulky", Execute: nop})
			}
			reg.Register(c, &tools.Tool{Name: "srv__core", Description: "core tool", Execute: nop})
			return nil
		}}, nil)
	})
	a, _ := e.agents.Create(agent.Spec{Name: "K"})
	b, _ := e.comp.Usage(a.ID(), "")
	checkSums(t, b)
	found := false
	for _, it := range b.Items {
		if strings.HasPrefix(it.ID, "tool:bulky__") {
			t.Fatalf("deferred tool listed: %+v", it)
		}
		if it.ID == "tool:srv__core" {
			found = it.Group == "srv"
		}
	}
	if !found {
		t.Fatalf("core tool missing or not grouped by server: %+v", b.Items)
	}
}

func TestItemsRollUpAtTheCap(t *testing.T) {
	e := newEnv(t, t.TempDir(), nil, func(load func(kernel.Spec, map[string]any)) {
		load(kernel.Spec{Name: "test-many", Inject: []string{tools.ServiceKey}, Apply: func(c *kernel.Context) error {
			reg := kernel.Use[*tools.Registry](c, tools.ServiceKey)
			for i := range 400 {
				reg.Register(c, &tools.Tool{Name: "many__t" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + string(rune('a'+i/26)),
					Description: strings.Repeat("d", 10+i), Execute: nop})
			}
			return nil
		}}, nil)
	})
	a, _ := e.agents.Create(agent.Spec{Name: "M"})
	b, _ := e.comp.Usage(a.ID(), "")
	if len(b.Items) > compaction.MaxItems {
		t.Fatalf("%d items", len(b.Items))
	}
	checkSums(t, b)
	var more *compaction.Item
	for i, it := range b.Items {
		if it.ID == "more:tools" {
			more = &b.Items[i]
		}
	}
	if more == nil || more.Count < 100 || !strings.HasSuffix(more.Label, "more") {
		t.Fatalf("no roll-up: %+v", more)
	}
	c, err := e.comp.Item(a.ID(), "", "more:tools")
	if err != nil || strings.Count(c.Parts[0].Text, "\n")+1 != more.Count {
		t.Fatalf("roll-up content %+v %v", c, err)
	}
}

// The provider's count of the last request wins over the estimate; an
// adapter's own estimate does not.
func TestCountedVersusEstimated(t *testing.T) {
	e := newEnv(t, t.TempDir(), nil)
	a, _ := e.agents.Create(agent.Spec{Name: "Ct"})
	ss, _ := a.Session()
	send(t, a, ss.ID(), "hello there")
	ref := llm.ModelRef{Provider: "tiny", Model: "default"}
	reply := func(u llm.Usage) {
		t.Helper()
		if _, err := ss.Append(session.TypeAssistantMessage, a.ID(), session.AssistantMessage{
			Message: llm.TextMessage(llm.RoleAssistant, "ok"), Model: ref, Usage: u}); err != nil {
			t.Fatal(err)
		}
	}
	reply(llm.Usage{InputTokens: 900, OutputTokens: 5, Estimated: true})
	b, _ := e.comp.Usage(a.ID(), "")
	if b.Counted || b.Used != b.Estimated {
		t.Fatalf("estimated usage counted: %+v", b)
	}
	reply(llm.Usage{InputTokens: 1000})
	reply(llm.Usage{InputTokens: 1100})
	reply(llm.Usage{InputTokens: 1200, CacheRead: 900, CacheWrite: 200})
	b, _ = e.comp.Usage(a.ID(), "")
	// the last reply ("ok", 1 + 4 tokens) is added to the count
	if !b.Counted || b.Used != 1205 {
		t.Fatalf("counted %v used %d: %+v", b.Counted, b.Used, b)
	}
	if b.Cache == nil || b.Cache.Read != 900 || b.Cache.Write != 200 || b.Cache.Input != 1200 || b.Cache.HitPercent != 75 {
		t.Fatalf("cache %+v", b.Cache)
	}
	// growth 100/request, threshold 0.8 × 4000 = 3200: (3200-1205)/100
	if b.Headroom != 19 {
		t.Fatalf("headroom %d", b.Headroom)
	}
	if b.Free != b.Window-b.Used {
		t.Fatalf("free %d", b.Free)
	}
	items, segs := sumsByKind(b)
	if extra := b.Used - b.Estimated; extra > 0 && (segs["other"] != extra || items["other"] != extra) {
		t.Fatalf("difference not shown: %+v", b.Segments)
	}
	// no cache fields: no cache block
	reply(llm.Usage{InputTokens: 1300})
	b, _ = e.comp.Usage(a.ID(), "")
	if b.Cache != nil || b.Used != 1305 {
		t.Fatalf("%+v", b)
	}
	// another provider's reply: estimated again
	if _, err := ss.Append(session.TypeAssistantMessage, a.ID(), session.AssistantMessage{
		Message: llm.TextMessage(llm.RoleAssistant, "ok"), Model: llm.ModelRef{Provider: "other", Model: "x"}, Usage: llm.Usage{InputTokens: 5000}}); err != nil {
		t.Fatal(err)
	}
	b, _ = e.comp.Usage(a.ID(), "")
	if b.Counted {
		t.Fatalf("another provider's count used: %+v", b)
	}
}
