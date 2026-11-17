package compaction

import (
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/core/llm"
)

func TestCapPartsCutsAndRedacts(t *testing.T) {
	big := strings.Repeat("é", 60<<10) // 120 KB
	ps, cut := capParts([]ContentPart{
		{Title: "arguments", Format: "json", Text: `{"k":"sk-` + strings.Repeat("a", 30) + `"}`},
		{Title: "result", Format: "json", Text: big},
		{Title: "after", Format: "text", Text: "never"},
	}, MaxItemContent)
	if !cut || len(ps) != 2 || strings.Contains(ps[0].Text, "sk-aaa") || ps[1].Format != "text" {
		t.Fatalf("cut=%v %d parts %q", cut, len(ps), ps[0].Text)
	}
	if n := len(ps[0].Text) + len(ps[1].Text); n > MaxItemContent || !strings.HasSuffix(ps[1].Text, "é") {
		t.Fatalf("%d bytes, not cut on a rune", n)
	}
	if _, cut := capParts([]ContentPart{{Text: "small"}}, MaxItemContent); cut {
		t.Fatal("small content cut")
	}
}

func TestCacheHitMaths(t *testing.T) {
	if c := cacheOf(llm.Usage{InputTokens: 2000}); c != nil {
		t.Fatalf("no cache fields: %+v", c)
	}
	c := cacheOf(llm.Usage{InputTokens: 2000, CacheRead: 1500, CacheWrite: 100})
	if c == nil || c.HitPercent != 75 || c.Input != 2000 {
		t.Fatalf("%+v", c)
	}
	if c := cacheOf(llm.Usage{InputTokens: 400, CacheWrite: 400}); c == nil || c.HitPercent != 0 {
		t.Fatalf("cold write %+v", c)
	}
}

func TestRollUpKeepsTheLargest(t *testing.T) {
	var items []*Item
	for i := range 50 {
		items = append(items, &Item{ID: "a" + string(rune('a'+i)), Kind: SegTools, Tokens: 100 + i})
		items = append(items, &Item{ID: "m" + string(rune('a'+i)), Kind: SegMessages, Tokens: i})
	}
	out := rollUp(items, 30)
	if len(out) != 30 {
		t.Fatalf("%d items", len(out))
	}
	sum := map[string]int{}
	for _, it := range out {
		sum[it.Kind] += it.Tokens
	}
	if sum[SegTools] != 50*100+49*50/2 || sum[SegMessages] != 49*50/2 {
		t.Fatalf("roll-up loses tokens: %v", sum)
	}
	if out[0].Kind != SegTools || out[0].Tokens != 149 || out[len(out)-1].ID != "more:messages" {
		t.Fatalf("order: first %+v last %+v", out[0], out[len(out)-1])
	}
}
