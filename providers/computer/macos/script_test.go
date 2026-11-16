package macos

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// ran is one run through a fake runner.
type ran struct {
	path  string
	args  []string
	stdin string
	limit int
}

// fakeRunner records runs and answers with fn.
func fakeRunner(runs *[]ran, fn func(ctx context.Context, r ran) (string, string, error)) runner {
	return func(ctx context.Context, path string, args []string, stdin string, limit int) (string, string, bool, error) {
		r := ran{path, append([]string(nil), args...), stdin, limit}
		*runs = append(*runs, r)
		out, errOut, err := fn(ctx, r)
		cut := false
		if len(out) > limit {
			out, cut = out[:limit], true
		}
		return out, errOut, cut, err
	}
}

func scriptProvider(t *testing.T, cfg Config, fn func(ctx context.Context, r ran) (string, string, error)) (*Provider, *[]ran) {
	t.Helper()
	p, err := New(newFake(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	var runs []ran
	p.run = fakeRunner(&runs, fn)
	return p, &runs
}

func TestScriptArgv(t *testing.T) {
	src := `tell application "Notes" to get name of every note -- "; rm -rf ~"`
	p, runs := scriptProvider(t, Config{}, func(context.Context, ran) (string, string, error) { return "a, b\n", "", nil })
	ctx := context.Background()
	out, err := p.RunScript(ctx, "applescript", src)
	if err != nil || out != "a, b" {
		t.Fatalf("RunScript = %q, %v", out, err)
	}
	for _, lang := range []string{"javascript", "JXA", "js"} {
		if _, err := p.RunScript(ctx, lang, `Application("Notes").notes.name()`); err != nil {
			t.Fatal(err)
		}
	}
	r := (*runs)[0]
	// no shell: osascript itself, the source on stdin, nothing of it in argv
	if r.path != "/usr/bin/osascript" || len(r.args) != 0 || r.stdin != src {
		t.Fatalf("applescript run = %+v", r)
	}
	for _, r := range (*runs)[1:] {
		if r.path != "/usr/bin/osascript" || !slices.Equal(r.args, []string{"-l", "JavaScript"}) || !strings.HasPrefix(r.stdin, "Application(") {
			t.Fatalf("jxa run = %+v", r)
		}
	}
	for _, c := range []struct{ lang, src, want string }{
		{"python", "print(1)", "unknown script language"},
		{"applescript", "  \n", "empty"},
		{"applescript", strings.Repeat("x", maxScriptSource+1), "longer than"},
	} {
		if _, err := p.RunScript(ctx, c.lang, c.src); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("RunScript(%q) error = %v, want %q", c.lang, err, c.want)
		}
	}
	if len(*runs) != 4 {
		t.Fatalf("invalid scripts ran: %d runs", len(*runs))
	}
}

func TestScriptErrorIsStderr(t *testing.T) {
	p, _ := scriptProvider(t, Config{}, func(_ context.Context, r ran) (string, string, error) {
		if strings.Contains(r.stdin, "Mail") {
			return "", "execution error: Not authorized to send Apple events to Mail. (-1743)\n", errors.New("exit status 1")
		}
		return "", "0:5: syntax error: Expected end of line. (-2741)\n", errors.New("exit status 1")
	})
	_, err := p.RunScript(context.Background(), "applescript", "tell foo")
	if err == nil || !strings.Contains(err.Error(), "syntax error: Expected end of line. (-2741)") || strings.Contains(err.Error(), "exit status") {
		t.Fatalf("error = %v", err)
	}
	_, err = p.RunScript(context.Background(), "applescript", `tell application "Mail" to get name`)
	if err == nil || !strings.Contains(err.Error(), "Automation") {
		t.Fatalf("automation error = %v", err)
	}
}

func TestScriptTimeoutAndCap(t *testing.T) {
	p, _ := scriptProvider(t, Config{ScriptTimeout: "50ms", MaxScriptOutput: 10}, func(ctx context.Context, r ran) (string, string, error) {
		if r.stdin == "hang" {
			<-ctx.Done()
			return "", "", ctx.Err()
		}
		return "héllo wörld and more", "", nil
	})
	start := time.Now()
	_, err := p.RunScript(context.Background(), "applescript", "hang")
	if err == nil || !strings.Contains(err.Error(), "did not finish within 50ms") {
		t.Fatalf("timeout error = %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("the timeout did not stop the run")
	}
	out, err := p.RunScript(context.Background(), "applescript", "big")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "héllo wö") || !strings.Contains(out, "output cut at 10 bytes") || !strings.Contains(out, "\n…") {
		t.Fatalf("capped output = %q", out)
	}
	// a cancelled call is cancelled, not "timed out"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.RunScript(ctx, "applescript", "hang"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	// the defaults
	q, err := New(newFake(), Config{})
	if err != nil || q.scriptTimeout != 30*time.Second || q.maxOutput != DefaultMaxScriptOutput {
		t.Fatalf("defaults: %v %v %d", err, q.scriptTimeout, q.maxOutput)
	}
	if _, err := New(newFake(), Config{ScriptTimeout: "soon"}); err == nil {
		t.Fatal("bad scriptTimeout accepted")
	}
}

func TestCappedWriter(t *testing.T) {
	c := &capped{max: 5}
	for _, s := range []string{"ab", "cd", "ef", "gh"} {
		if n, err := c.Write([]byte(s)); n != 2 || err != nil {
			t.Fatalf("write = %d, %v", n, err)
		}
	}
	if c.b.String() != "abcde" || !c.cut {
		t.Fatalf("capped = %q cut %v", c.b.String(), c.cut)
	}
}

func TestShortcuts(t *testing.T) {
	var input string
	p, runs := scriptProvider(t, Config{}, func(_ context.Context, r ran) (string, string, error) {
		if r.args[0] == "list" {
			return "Morning Routine\n翻译剪贴板\n\n", "", nil
		}
		i := slices.Index(r.args, "--input-path")
		if i > 0 {
			b, _ := os.ReadFile(r.args[i+1])
			input = string(b)
		}
		o := r.args[slices.Index(r.args, "--output-path")+1]
		return "", "", os.WriteFile(o, []byte("Bonjour\n"), 0o600)
	})
	ctx := context.Background()
	names, err := p.Shortcuts(ctx)
	if err != nil || !slices.Equal(names, []string{"Morning Routine", "翻译剪贴板"}) {
		t.Fatalf("Shortcuts = %q, %v", names, err)
	}
	if r := (*runs)[0]; r.path != "/usr/bin/shortcuts" || !slices.Equal(r.args, []string{"list"}) {
		t.Fatalf("list run = %+v", r)
	}
	out, err := p.RunShortcut(ctx, "翻译剪贴板", "Hello; $(rm -rf ~)")
	if err != nil || out != "Bonjour" || input != "Hello; $(rm -rf ~)" {
		t.Fatalf("RunShortcut = %q, %v (input %q)", out, err, input)
	}
	r := (*runs)[1]
	if r.path != "/usr/bin/shortcuts" || r.args[0] != "run" || r.args[1] != "翻译剪贴板" || r.stdin != "" {
		t.Fatalf("run = %+v", r)
	}
	// no input: no input file
	if _, err := p.RunShortcut(ctx, "Morning Routine", ""); err != nil {
		t.Fatal(err)
	}
	if slices.Contains((*runs)[2].args, "--input-path") {
		t.Fatalf("input file without input: %+v", (*runs)[2])
	}
	for _, bad := range []string{"", " ", "--help", "a\nb"} {
		if _, err := p.RunShortcut(ctx, bad, ""); err == nil {
			t.Errorf("shortcut name %q accepted", bad)
		}
	}
	if len(*runs) != 3 {
		t.Fatalf("bad names ran: %d runs", len(*runs))
	}
}
