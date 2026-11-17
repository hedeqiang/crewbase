package mcp_test

import (
	"testing"

	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/mcp"
)

func TestToolsDeferredUnlessAlwaysLoad(t *testing.T) {
	e := newEnv(t, nil)
	if _, err := e.svc.Add(stdioServer("lazy", nil)); err != nil {
		t.Fatal(err)
	}
	eager := stdioServer("eager", nil)
	eager.AlwaysLoad = true
	if _, err := e.svc.Add(eager); err != nil {
		t.Fatal(err)
	}
	e.waitState(t, "lazy", mcp.StateReady)
	e.waitState(t, "eager", mcp.StateReady)
	if tl, ok := e.tools.Get("", "lazy__wipe"); !ok || tl.Load != tools.LoadDeferred || tools.GroupOf(tl) != "lazy" {
		t.Fatalf("lazy: %+v", tl)
	}
	if tl, ok := e.tools.Get("", "eager__wipe"); !ok || tl.Load != tools.LoadCore || tl.Group != "eager" {
		t.Fatalf("eager: %+v", tl)
	}
}
