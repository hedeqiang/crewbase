// Package none is sandbox-none: the passthrough provider of the
// `sandbox` capability. It only accepts policy mode "full" (host mode, C)
// and reports enforcement "none"; any confined policy fails closed with
// SANDBOX_UNAVAILABLE so a deployment without a real backend never runs a
// sandbox-mode command unconfined.
//
// It enforces no ProtectedRoots, and needs not: a policy that has them
// (sandbox and read-only modes) is refused, so no command runs, and host
// mode (full) carries none. Under this provider the coding CLIs' project
// config (agent.CLIConfigNames) is guarded only by what does not depend on
// the command sandbox: the fs tools refuse those paths in sandbox mode, and
// an agent's own workspace is quarantined before each CLI turn.
package none

import (
	"context"

	"github.com/hedeqiang/crewbase/capabilities/sandbox"
	"github.com/hedeqiang/crewbase/kernel"
)

// None implements sandbox.Sandbox.
type None struct{}

func (None) Name() string { return "none" }

func (None) Check(_ context.Context, p sandbox.Policy) (string, error) {
	if p.Normalize().Mode != sandbox.ModeFull {
		return "", sandbox.Unavailable("sandbox-none cannot enforce %q (only host mode is allowed)", p.Normalize().Mode)
	}
	return sandbox.EnforcementNone, nil
}

func (n None) Confine(ctx context.Context, argv []string, _ string, env []string, p sandbox.Policy) (sandbox.Confined, error) {
	if _, err := n.Check(ctx, p); err != nil {
		return sandbox.Confined{}, err
	}
	if err := sandbox.Validate(argv, p); err != nil {
		return sandbox.Confined{}, err
	}
	return sandbox.Unconfined(argv, env), nil
}

var _ sandbox.Sandbox = None{}

// Plugin provides `sandbox` with no confinement.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/sandbox-none",
	Description: "Passthrough sandbox (host mode only; confined policies fail with SANDBOX_UNAVAILABLE).",
	Apply: func(c *kernel.Context) error {
		return c.Provide(sandbox.ServiceKey, None{})
	},
})
