package kernel

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type greeter struct{ prefix string }

func TestPendingUntilDependencyThenActive(t *testing.T) {
	h := NewHost(nil)
	defer h.Close()

	var got string
	_, err := h.Load(Spec{Name: "consumer", Inject: []string{"greeter"}, Apply: func(c *Context) error {
		got = Use[*greeter](c, "greeter").prefix
		return nil
	}}, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := h.Plugin("consumer"); info.State != StatePending || info.Missing[0] != "greeter" {
		t.Fatalf("want PENDING missing greeter, got %+v", info)
	}
	h.Load(Spec{Name: "provider", Apply: func(c *Context) error {
		return c.Provide("greeter", &greeter{prefix: "Hello"})
	}}, LoadOptions{})
	if info, _ := h.Plugin("consumer"); info.State != StateActive || got != "Hello" {
		t.Fatalf("want ACTIVE, got %+v %q", info, got)
	}

	// Removing the provider deactivates the consumer; reloading reactivates.
	var disposed int32
	h.Unload("consumer")
	h.Load(Spec{Name: "consumer2", Inject: []string{"greeter"}, Apply: func(c *Context) error {
		c.Defer(func() { atomic.AddInt32(&disposed, 1) })
		return nil
	}}, LoadOptions{})
	h.Unload("provider")
	if info, _ := h.Plugin("consumer2"); info.State != StatePending || disposed != 1 {
		t.Fatalf("want PENDING after provider unload, got %+v disposed=%d", info, disposed)
	}
	h.Load(Spec{Name: "provider", Apply: func(c *Context) error { return c.Provide("greeter", &greeter{}) }}, LoadOptions{})
	if info, _ := h.Plugin("consumer2"); info.State != StateActive {
		t.Fatalf("want ACTIVE again, got %+v", info)
	}
}

func TestServiceTakenAndFailedState(t *testing.T) {
	h := NewHost(nil)
	defer h.Close()
	h.Load(Spec{Name: "a", Apply: func(c *Context) error { return c.Provide("shell", 1) }}, LoadOptions{})
	h.Load(Spec{Name: "b", Apply: func(c *Context) error { return c.Provide("shell", 2) }}, LoadOptions{})
	info, _ := h.Plugin("b")
	if info.State != StateFailed || !strings.Contains(info.Error, "already provided") {
		t.Fatalf("got %+v", info)
	}
	h.Load(Spec{Name: "p", Apply: func(c *Context) error { panic("boom") }}, LoadOptions{})
	if info, _ := h.Plugin("p"); info.State != StateFailed {
		t.Fatalf("panic should fail plugin: %+v", info)
	}
}

func TestEffectsReverseOrderAndGo(t *testing.T) {
	h := NewHost(nil)
	var order []int
	stopped := make(chan struct{})
	h.Load(Spec{Name: "x", Apply: func(c *Context) error {
		c.Defer(func() { order = append(order, 1) })
		c.Effect(func() func() { return func() { order = append(order, 2) } })
		c.Go(func(ctx context.Context) {
			<-ctx.Done()
			close(stopped)
		})
		return nil
	}}, LoadOptions{})
	h.Unload("x")
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("goroutine not cancelled")
	}
	if len(order) != 2 || order[0] != 2 || order[1] != 1 {
		t.Fatalf("want reverse order, got %v", order)
	}
}

var (
	testEmit      = NewEmit[string]("test/emit")
	testWaterfall = NewWaterfall[int, int]("test/waterfall")
	testParallel  = NewParallel[int]("test/parallel")
	testSerial    = NewSerial[int, string]("test/serial")
	testBail      = NewBail[string, string]("test/bail")
)

func TestWaterfallOrderShortCircuitPrepend(t *testing.T) {
	h := NewHost(nil)
	defer h.Close()
	var trace []string
	h.Load(Spec{Name: "w", Apply: func(c *Context) error {
		testWaterfall.Use(c, func(ctx context.Context, a int, next Next[int, int]) (int, error) {
			trace = append(trace, "outer")
			r, err := next(ctx, a+1)
			return r * 10, err
		})
		testWaterfall.Use(c, func(ctx context.Context, a int, next Next[int, int]) (int, error) {
			trace = append(trace, "inner")
			if a > 100 {
				return -1, nil // short-circuit
			}
			return next(ctx, a)
		})
		testWaterfall.Prepend(c, func(ctx context.Context, a int, next Next[int, int]) (int, error) {
			trace = append(trace, "first")
			return next(ctx, a)
		})
		return nil
	}}, LoadOptions{})
	r, err := testWaterfall.Run(h, context.Background(), 1, func(_ context.Context, a int) (int, error) { return a, nil })
	if err != nil || r != 20 || strings.Join(trace, ",") != "first,outer,inner" {
		t.Fatalf("r=%d err=%v trace=%v", r, err, trace)
	}
	r, _ = testWaterfall.Run(h, context.Background(), 200, func(context.Context, int) (int, error) {
		t.Fatal("terminal must not run after short-circuit")
		return 0, nil
	})
	if r != -10 {
		t.Fatalf("short-circuit result %d", r)
	}
	h.Unload("w")
	r, _ = testWaterfall.Run(h, context.Background(), 5, func(_ context.Context, a int) (int, error) { return a, nil })
	if r != 5 {
		t.Fatalf("listeners must be removed on unload, got %d", r)
	}
}

func TestParallelSerialBail(t *testing.T) {
	h := NewHost(nil)
	defer h.Close()
	var sum int32
	h.Load(Spec{Name: "e", Apply: func(c *Context) error {
		testParallel.Listen(c, func(_ context.Context, n int) error { atomic.AddInt32(&sum, int32(n)); return nil })
		testParallel.Listen(c, func(context.Context, int) error { panic("isolated") })
		testParallel.Listen(c, func(_ context.Context, n int) error { atomic.AddInt32(&sum, int32(n)); return nil })
		testSerial.Listen(c, func(context.Context, int) (string, error) { return "a", nil })
		testSerial.Listen(c, func(context.Context, int) (string, error) { return "b", nil })
		testBail.Listen(c, func(_ context.Context, s string) (string, bool) { return "", false })
		testBail.Listen(c, func(_ context.Context, s string) (string, bool) { return "handled:" + s, true })
		testBail.Listen(c, func(_ context.Context, s string) (string, bool) { return "never", true })
		return nil
	}}, LoadOptions{})
	err := testParallel.Run(h, context.Background(), 3)
	if sum != 6 || err == nil {
		t.Fatalf("sum=%d err=%v", sum, err)
	}
	out, err := testSerial.Run(h, context.Background(), 0)
	if err != nil || strings.Join(out, "") != "ab" {
		t.Fatalf("serial %v %v", out, err)
	}
	r, ok := testBail.Run(h, context.Background(), "x")
	if !ok || r != "handled:x" {
		t.Fatalf("bail %q %v", r, ok)
	}
}

func TestScopedDispatch(t *testing.T) {
	h := NewHost(nil)
	defer h.Close()
	var seen []string
	root := h.Root()
	testEmit.Listen(root, func(s string) { seen = append(seen, "global:"+s) })
	a := root.Fork("agent-a")
	b := root.Fork("agent-b")
	testEmit.Listen(a, func(s string) { seen = append(seen, "a:"+s) })
	testEmit.Listen(b, func(s string) { seen = append(seen, "b:"+s) })

	testEmit.Emit(a, "1")                 // agent-a activity
	testEmit.Emit(For(h, "agent-b"), "2") // on behalf of agent-b
	testEmit.Emit(h, "3")                 // registry event: everyone
	want := "global:1,a:1,global:2,b:2,global:3,a:3,b:3"
	if got := strings.Join(seen, ","); got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	a.Dispose()
	seen = nil
	testEmit.Emit(h, "4")
	if got := strings.Join(seen, ","); got != "global:4,b:4" {
		t.Fatalf("after dispose got %s", got)
	}
}

func TestIsolationGroups(t *testing.T) {
	h := NewHost(nil)
	defer h.Close()
	h.Load(Spec{Name: "shell", Apply: func(c *Context) error { return c.Provide("shell", "ws1-shell") }},
		LoadOptions{ID: "shell-ws1", Isolate: []string{"shell"}, Group: "ws1"})
	h.Load(Spec{Name: "shell", Apply: func(c *Context) error { return c.Provide("shell", "ws2-shell") }},
		LoadOptions{ID: "shell-ws2", Isolate: []string{"shell"}, Group: "ws2"})
	var got string
	h.Load(Spec{Name: "tool", Inject: []string{"shell"}, Apply: func(c *Context) error {
		got = Use[string](c, "shell")
		return nil
	}}, LoadOptions{ID: "tool-ws2", Isolate: []string{"shell"}, Group: "ws2"})
	if got != "ws2-shell" {
		t.Fatalf("got %q", got)
	}
	h.Load(Spec{Name: "global-tool", Inject: []string{"shell"}}, LoadOptions{})
	if info, _ := h.Plugin("global-tool"); info.State != StatePending {
		t.Fatalf("global consumer must not see isolated services: %+v", info)
	}
}

func TestPatchAndApplyEntries(t *testing.T) {
	var applies int32
	testRegister(Spec{Name: "@test/counter", Apply: func(c *Context) error {
		atomic.AddInt32(&applies, 1)
		return nil
	}})
	RegisterBundle(Bundle{Name: "test-base", Entries: []Entry{
		{ID: "counter", Name: "@test/counter", Config: map[string]any{"n": 1}},
		{ID: "hmr", Name: "@test/counter"},
	}})
	entries, err := Resolve(Profile{Name: "t", Bundles: []string{"test-base"}}, []Entry{{ID: "hmr", Disabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	if !entries[1].Disabled || entries[1].Name != "@test/counter" {
		t.Fatalf("disable patch: %+v", entries)
	}
	h := NewHost(nil)
	defer h.Close()
	if err := h.ApplyEntries(entries); err != nil {
		t.Fatal(err)
	}
	if applies != 1 {
		t.Fatalf("applies=%d", applies)
	}
	// same config → no remount
	h.ApplyEntries(entries)
	if applies != 1 {
		t.Fatalf("unchanged entry remounted: %d", applies)
	}
	// changed config → remount
	entries = ApplyPatch(entries, []Entry{{ID: "counter", Name: "@test/counter", Config: map[string]any{"n": 2}}})
	h.ApplyEntries(entries)
	if applies != 2 {
		t.Fatalf("changed entry not remounted: %d", applies)
	}
	// unknown plugin is reported, not fatal to others
	err = h.ApplyEntries(append(entries, Entry{ID: "nope", Name: "@test/missing"}))
	if err == nil {
		t.Fatal("want error for unknown plugin")
	}
	if info, _ := h.Plugin("nope"); info.State != StateFailed {
		t.Fatalf("unknown plugin should be FAILED: %+v", info)
	}
	if !strings.Contains(DumpConfig(entries), "@test/counter") {
		t.Fatal("dump config")
	}
}

func TestDecode(t *testing.T) {
	h := NewHost(nil)
	defer h.Close()
	type cfg struct {
		Image string `json:"image"`
	}
	var got cfg
	h.Load(Spec{Name: "d", Apply: func(c *Context) error {
		var err error
		got, err = Decode[cfg](c)
		return err
	}}, LoadOptions{Config: map[string]any{"image": "crewbase/sandbox"}})
	if got.Image != "crewbase/sandbox" {
		t.Fatal(got)
	}
	_ = errors.New
}

func TestApplyEntriesLeavesDirectLoadsAndFail(t *testing.T) {
	testRegister(Spec{Name: "@test/managed"})
	h := NewHost(nil)
	defer h.Close()
	if err := h.ApplyEntries([]Entry{{ID: "m", Name: "@test/managed"}}); err != nil {
		t.Fatal(err)
	}
	var ctx *Context
	h.Load(Spec{Name: "direct", Apply: func(c *Context) error { ctx = c; return nil }}, LoadOptions{})
	h.ApplyEntries(nil) // removes managed entries only
	if _, ok := h.Plugin("m"); ok {
		t.Fatal("managed entry should be unloaded")
	}
	if info, ok := h.Plugin("direct"); !ok || info.State != StateActive {
		t.Fatal("directly loaded plugin must be left alone")
	}
	ctx.Fail(errors.New("crash loop"))
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if info, _ := h.Plugin("direct"); info.State == StateFailed && info.Error == "crash loop" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("Fail did not mark the plugin FAILED")
}

func TestPluginChangedEvents(t *testing.T) {
	h := NewHost(nil)
	defer h.Close()
	var got []string
	PluginChanged.Listen(h.Root(), func(e PluginEvent) { got = append(got, e.ID+":"+string(e.From)+">"+string(e.To)) })
	h.Load(Spec{Name: "needs", Inject: []string{"svc"}}, LoadOptions{})
	h.Load(Spec{Name: "prov", Apply: func(c *Context) error { return c.Provide("svc", 1) }}, LoadOptions{})
	h.Load(Spec{Name: "bad", Apply: func(c *Context) error { return errors.New("x") }}, LoadOptions{})
	h.Unload("prov")
	want := []string{
		"needs:>PENDING",
		"prov:>PENDING", "prov:PENDING>ACTIVE", "needs:PENDING>ACTIVE",
		"bad:>PENDING", "bad:PENDING>FAILED",
		"needs:ACTIVE>PENDING", "prov:ACTIVE>DISPOSED",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
}

func TestFailFromStaleContextIgnored(t *testing.T) {
	h := NewHost(nil)
	defer h.Close()
	var ctxs []*Context
	h.Load(Spec{Name: "dep", Apply: func(c *Context) error { return c.Provide("d", 1) }}, LoadOptions{})
	h.Load(Spec{Name: "p", Inject: []string{"d"}, Apply: func(c *Context) error { ctxs = append(ctxs, c); return nil }}, LoadOptions{})
	h.Unload("dep")
	h.Load(Spec{Name: "dep", Apply: func(c *Context) error { return c.Provide("d", 1) }}, LoadOptions{})
	if len(ctxs) != 2 {
		t.Fatalf("activations %d", len(ctxs))
	}
	ctxs[0].Fail(errors.New("old supervisor"))
	time.Sleep(50 * time.Millisecond)
	if info, _ := h.Plugin("p"); info.State != StateActive {
		t.Fatalf("stale Fail tore down the new activation: %+v", info)
	}
}

// testRegister is Register for tests: it replaces an existing entry, so a
// test can run again in the same process (go test -count=N) with its own
// Apply.
func testRegister(spec Spec) Spec {
	catalogMu.Lock()
	defer catalogMu.Unlock()
	catalog[spec.Name] = spec
	return spec
}
