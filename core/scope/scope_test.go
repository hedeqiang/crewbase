package scope

import (
	"testing"

	"github.com/hedeqiang/crewbase/kernel"
)

func TestShadowingDisposedInAnyOrder(t *testing.T) {
	h := kernel.NewHost(nil)
	defer h.Close()
	r := New[string](nil)
	a, b, c := h.Root().Fork(""), h.Root().Fork(""), h.Root().Fork("")
	r.Register(a, "x", "A")
	r.Register(b, "x", "B")
	r.Register(c, "x", "C")
	if v, _ := r.Get("", "x"); v != "C" {
		t.Fatal(v)
	}
	a.Dispose() // oldest first: must not come back later
	if v, _ := r.Get("", "x"); v != "C" {
		t.Fatal(v)
	}
	c.Dispose()
	if v, _ := r.Get("", "x"); v != "B" {
		t.Fatalf("want B, got %s", v)
	}
	b.Dispose()
	if _, ok := r.Get("", "x"); ok {
		t.Fatal("stale entry resurrected")
	}
}

func TestAgentShadowAndRestrict(t *testing.T) {
	h := kernel.NewHost(nil)
	defer h.Close()
	r := New[string](nil)
	r.Register(h.Root(), "t", "global")
	ag := h.Root().Fork("kai")
	r.Register(ag, "t", "kai's")
	if v, _ := r.Get("kai", "t"); v != "kai's" {
		t.Fatal(v)
	}
	if v, _ := r.Get("nova", "t"); v != "global" {
		t.Fatal(v)
	}
	r.Restrict(ag, func(n string) bool { return n != "t" })
	if _, ok := r.Get("kai", "t"); ok || len(r.Visible("kai")) != 0 {
		t.Fatal("restricted")
	}
	ag.Dispose()
	if v, _ := r.Get("kai", "t"); v != "global" {
		t.Fatal(v)
	}
}
