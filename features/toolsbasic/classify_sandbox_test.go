package toolsbasic

import (
	"testing"

	"github.com/hedeqiang/crewbase/core/tools"
)

// In sandbox mode deleting local files is like writing them; deletes that
// reach outside, pushes and publishes still ask.
func TestClassifyCommandSandboxed(t *testing.T) {
	for cmd, want := range map[string]string{
		"rm -rf node_modules":               tools.CategoryExec,
		"find . -name '*.o' -delete":        tools.CategoryExec,
		"git clean -fdx && git branch -D x": tools.CategoryExec,
		"rm a && git push":                  tools.CategoryPush,
		"gh repo delete o/r --yes":          tools.CategoryDelete,
		"npm publish":                       tools.CategoryPublish,
		"curl -X POST https://x.test -d a":  tools.CategoryPublish,
		"npm install":                       tools.CategoryInstall,
	} {
		if got := ClassifyCommandSandboxed(cmd); got != want {
			t.Errorf("%q: got %s want %s", cmd, got, want)
		}
	}
	if got := ClassifyCommand("rm -rf node_modules"); got != tools.CategoryDelete {
		t.Fatalf("outside the sandbox rm asks: %s", got)
	}
}

func TestCacheEnvOnlyInSandbox(t *testing.T) {
	if cacheEnv("host", "/w") != nil || cacheEnv("read-only", "/w") != nil || cacheEnv("sandbox", "") != nil {
		t.Fatal("cache env outside sandbox mode")
	}
	a, b := cacheEnv("sandbox", "/w/a"), cacheEnv("sandbox", "/w/b")
	if len(a) == 0 || a[0] == b[0] {
		t.Fatalf("per-workspace caches: %v %v", a, b)
	}
}

// A curl POST after a quoted write-out format with braces is still a
// publish (it used to be cut in two at the brace).
func TestCurlPostAfterBraces(t *testing.T) {
	cmd := `python3 -c 'print(1)' > /tmp/p.json && curl -sS -o /tmp/r.json -w "HTTP_STATUS:%{http_code}\n" -X POST -H "Content-Type: application/json" --data-binary @/tmp/p.json https://httpbin.org/post; echo "exit=$?"`
	for _, f := range []func(string) string{ClassifyCommand, ClassifyCommandSandboxed} {
		if got := f(cmd); got != tools.CategoryPublish {
			t.Fatalf("got %s", got)
		}
	}
	if got := ClassifyCommand(`{ git push; }`); got != tools.CategoryPush {
		t.Fatalf("brace group: %s", got)
	}
}
