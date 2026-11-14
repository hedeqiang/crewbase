package process

import (
	"slices"
	"strings"
	"testing"
)

func TestManifestParse(t *testing.T) {
	m, err := ParseManifest([]byte(`name = "notify-feishu"
version = "0.3.1"
runtime = "process"
entry = ["node", "dist/index.js"]
[inject]
services = ["notify", "credentials"]
[permissions]
network = ["open.feishu.cn"]
credentials = ["feishu-bot"]
`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"service:credentials", "service:notify", "network:open.feishu.cn", "credentials:feishu-bot"}
	if got := m.Requested().Strings(); !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
	m, err = ParseManifest([]byte("name = \"x\"\nversion = \"1\"\nentry = [\"a\"]\n[runtime]\nkind = \"process\"\nsandbox = true\n"))
	if err != nil || !m.Sandbox {
		t.Fatalf("table runtime: %+v %v", m, err)
	}
	for src, msg := range map[string]string{
		"name = \"X Y\"\nversion = \"1\"\nentry = [\"a\"]":                                            "invalid name",
		"name = \"x\"\nversion = \"1\"\nentry = [\"a\"]\n[permissions]\nshell = true":                 "unknown key",
		"name = \"x\"\nversion = \"1\"\nentry = [\"a\"]\n[inject]\nservices = [\"llm\"]":              "unknown service",
		"name = \"x\"\nversion = \"1\"\nentry = [\"a\"]\n[permissions]\nevents = [\"agent/x\"]":       "not exposed",
		"name = \"x\"\nversion = \"../1\"\nentry = [\"a\"]":                                           "invalid version",
		"name = \"x\"\nversion = \"1\"\nruntime = \"wasm\"\nentry = [\"a\"]":                          "one .wasm file",
		"name = \"x\"\nversion = \"1\"\nruntime = \"wasm\"\nentry = [\"p.wasm\"]":                     "declares hooks",
		"name = \"x\"\nversion = \"1\"\nruntime = \"wasm\"\nentry = [\"p.wasm\"]\nhooks = [\"exec\"]": "unknown hook",
		"name = \"x\"\nversion = \"1\"\nruntime = \"lua\"\nentry = [\"a\"]":                           "unsupported runtime",
		"name = \"x\"\nversion = \"1\"\nentry = [\"node\"]\nhooks = [\"rule\"]":                       "wasm plugins only",
	} {
		if _, err := ParseManifest([]byte(src)); err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%q: want %q, got %v", src, msg, err)
		}
	}
}

func TestPermissionSets(t *testing.T) {
	granted, err := ParsePermissions([]string{"service:tools", "network:a.com", "subprocess"})
	if err != nil {
		t.Fatal(err)
	}
	want := Permissions{Services: []string{"tools", "notify"}, Network: []string{"a.com"}}
	if got := granted.Missing(want); !slices.Equal(got, []string{"service:notify"}) {
		t.Fatalf("missing %v", got)
	}
	if got := granted.Intersect(want).Strings(); !slices.Equal(got, []string{"service:tools", "network:a.com"}) {
		t.Fatalf("intersect %v", got)
	}
	if _, err := ParsePermissions([]string{"root:all"}); err == nil {
		t.Fatal("unknown permission accepted")
	}
}
