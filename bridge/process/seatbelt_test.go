//go:build darwin

package process_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/bridge/process"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/sandbox/seatbelt"
)

// The examples run under the real macOS sandbox with the policy a plugin
// gets by default: read-only files except its data dir, no network.
func TestExamplesUnderSeatbelt(t *testing.T) {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec not available")
	}
	cases := []struct {
		name, dir, tool, args, want string
		need                        string
		build                       bool
	}{
		{"py", "hello-py", "py_hello", `{"name":"box"}`, "Hello, box!", "python3", false},
		{"node", "word-count", "word_count", `{"text":"a b c"}`, "words: 3", "node", false},
		{"go", "hello-go", "go_hello", `{"name":"box"}`, "Hello, box!", "go", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := exec.LookPath(c.need); err != nil {
				t.Skip(c.need + " not available")
			}
			h, _ := bareHost(t)
			if _, err := h.Load(seatbelt.Plugin, kernel.LoadOptions{}); err != nil {
				t.Fatal(err)
			}
			dir := copyDir(t, "../../examples/plugins/"+c.dir)
			if c.build {
				b := exec.Command("go", "build", "-o", filepath.Join(dir, "plugin"), "./examples/plugins/hello-go")
				b.Dir = "../.."
				if out, err := b.CombinedOutput(); err != nil {
					t.Fatalf("build: %v\n%s", err, out)
				}
			}
			load(t, h, c.name, dir, []string{"service:tools", "event:tools/result"}, map[string]any{"dataDir": filepath.Join(t.TempDir(), ".data")})
			if info, _ := h.Plugin(c.name); info.State != kernel.StateActive {
				t.Fatalf("state %s: %s", info.State, info.Error)
			}
			st, _ := process.Lookup(h, c.name)
			if st.Enforcement == "none" {
				t.Fatalf("not confined: %+v", st)
			}
			if got := run(h, c.tool, c.args).Result; got.IsError || !strings.Contains(got.Content, c.want) {
				t.Fatalf("result %+v", got)
			}
		})
	}
}
