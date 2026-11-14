package sandbox_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/hedeqiang/crewbase/capabilities/sandbox"
	"github.com/hedeqiang/crewbase/core/agent"
)

func TestPolicyForRunModes(t *testing.T) {
	ws := "/w/kai"
	cases := []struct {
		mode  string
		hosts []string
		want  sandbox.Policy
	}{
		{agent.ModeReadOnly, nil, sandbox.Policy{Mode: sandbox.ModeReadOnly, ProtectedRoots: []string{ws}, Network: sandbox.NetworkNone}},
		{agent.ModeSandbox, nil, sandbox.Policy{Mode: sandbox.ModeWorkspaceWrite, WritableRoots: []string{ws}, Network: sandbox.NetworkNone}},
		{"", nil, sandbox.Policy{Mode: sandbox.ModeWorkspaceWrite, WritableRoots: []string{ws}, Network: sandbox.NetworkNone}},
		{agent.ModeHost, nil, sandbox.Policy{Mode: sandbox.ModeFull, Network: sandbox.NetworkOpen}},
		{"bogus", nil, sandbox.Policy{Mode: sandbox.ModeReadOnly, ProtectedRoots: []string{ws}, Network: sandbox.NetworkNone}},
		{agent.ModeSandbox, []string{"pypi.org"}, sandbox.Policy{Mode: sandbox.ModeWorkspaceWrite, WritableRoots: []string{ws}, Network: sandbox.NetworkAllowlist, AllowHosts: []string{"pypi.org"}}},
		{agent.ModeHost, []string{"pypi.org"}, sandbox.Policy{Mode: sandbox.ModeFull, Network: sandbox.NetworkOpen}},
	}
	for _, c := range cases {
		if got := sandbox.PolicyFor(c.mode, ws, c.hosts); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: got %+v want %+v", c.mode, got, c.want)
		}
	}
	if p := sandbox.PolicyFor(agent.ModeSandbox, "", nil); len(p.WritableRoots) != 0 {
		t.Fatal("no workspace must mean nothing writable", p)
	}
}

func TestNormalizeFailsClosed(t *testing.T) {
	p := sandbox.Policy{Mode: "weird", WritableRoots: []string{"/x"}, Network: "maybe"}.Normalize()
	if p.Mode != sandbox.ModeReadOnly || p.Network != sandbox.NetworkNone || p.WritableRoots != nil {
		t.Fatal(p)
	}
	if p := (sandbox.Policy{Mode: sandbox.ModeWorkspaceWrite, Network: sandbox.NetworkAllowlist}).Normalize(); p.Network != sandbox.NetworkNone {
		t.Fatal("empty allowlist must be none", p)
	}
	if p := (sandbox.Policy{Mode: sandbox.ModeFull}).Normalize(); p.Network != sandbox.NetworkOpen {
		t.Fatal(p)
	}
	if p := (sandbox.Policy{Mode: sandbox.ModeReadOnly}).Normalize(); p.Network != sandbox.NetworkNone {
		t.Fatal(p)
	}
}

func TestEffectiveRunMode(t *testing.T) {
	for in, want := range map[string]string{"": agent.ModeSandbox, "host": "host", "read-only": "read-only", "sandbox": "sandbox", "x": "read-only"} {
		if got := sandbox.EffectiveRunMode(in); got != want {
			t.Errorf("%q → %q want %q", in, got, want)
		}
	}
}

func TestValidateAndErrors(t *testing.T) {
	if sandbox.Validate(nil, sandbox.Policy{}) == nil {
		t.Fatal("empty argv")
	}
	if sandbox.Validate([]string{"ls"}, sandbox.Policy{WritableRoots: []string{"rel"}}) == nil {
		t.Fatal("relative root")
	}
	if sandbox.Validate([]string{"ls"}, sandbox.Policy{WritableRoots: []string{"/"}}) == nil {
		t.Fatal("root /")
	}
	err := sandbox.Unavailable("docker down")
	if !errors.Is(err, sandbox.ErrUnavailable) || err.Error() != "SANDBOX_UNAVAILABLE: docker down" {
		t.Fatal(err)
	}
	if !sandbox.MatchBlocked([]string{"operation not permitted"}, "", "sh: /x: Operation not permitted") {
		t.Fatal("case-insensitive match")
	}
	if sandbox.MatchBlocked([]string{""}, "anything") || sandbox.MatchBlocked(nil, "x") {
		t.Fatal("empty signatures never match")
	}
	c := sandbox.Unconfined([]string{"a"}, []string{"K=V"})
	if c.Enforcement != sandbox.EnforcementNone || c.Argv[0] != "a" {
		t.Fatal(c)
	}
}
