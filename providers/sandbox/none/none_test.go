package none_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hedeqiang/crewbase/capabilities/sandbox"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/sandbox/none"
)

func TestOnlyHostMode(t *testing.T) {
	h := kernel.NewHost(nil)
	defer h.Close()
	if _, err := h.Load(none.Plugin, kernel.LoadOptions{}); err != nil {
		t.Fatal(err)
	}
	sb := kernel.Use[sandbox.Sandbox](h.Root(), sandbox.ServiceKey)
	ctx := context.Background()
	c, err := sb.Confine(ctx, []string{"echo", "hi"}, "/tmp", []string{"A=1"}, sandbox.Policy{Mode: sandbox.ModeFull})
	if err != nil || c.Enforcement != sandbox.EnforcementNone || len(c.Argv) != 2 || c.Env[0] != "A=1" {
		t.Fatal(c, err)
	}
	for _, m := range []string{sandbox.ModeReadOnly, sandbox.ModeWorkspaceWrite, ""} {
		_, err := sb.Confine(ctx, []string{"echo"}, "/tmp", nil, sandbox.Policy{Mode: m, WritableRoots: []string{"/tmp"}})
		if !errors.Is(err, sandbox.ErrUnavailable) {
			t.Fatalf("%q: %v", m, err)
		}
		if _, err := sb.Check(ctx, sandbox.Policy{Mode: m}); !errors.Is(err, sandbox.ErrUnavailable) {
			t.Fatal(err)
		}
	}
}
