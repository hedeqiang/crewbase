//go:build darwin

package macos

import (
	"context"
	"os"
	"testing"
)

// Live script tests touch no app: a constant AppleScript and JXA, and the
// list of shortcuts (nothing is run).
//
//	CREWBASE_COMPUTER_LIVE_SCRIPTS=1 go test -run LiveScript -v ./providers/computer/macos/
func liveScripts(t *testing.T) *Provider {
	t.Helper()
	if os.Getenv("CREWBASE_COMPUTER_LIVE_SCRIPTS") != "1" {
		t.Skip("set CREWBASE_COMPUTER_LIVE_SCRIPTS=1 to run osascript / shortcuts list for real")
	}
	p, err := New(newFake(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLiveScriptConstant(t *testing.T) {
	p := liveScripts(t)
	ctx := context.Background()
	if out, err := p.RunScript(ctx, "applescript", "return 1+1"); err != nil || out != "2" {
		t.Fatalf("applescript = %q, %v", out, err)
	}
	if out, err := p.RunScript(ctx, "javascript", "1+1"); err != nil || out != "2" {
		t.Fatalf("jxa = %q, %v", out, err)
	}
	if _, err := p.RunScript(ctx, "applescript", "return 1 +"); err == nil {
		t.Fatal("a syntax error passed")
	} else {
		t.Logf("syntax error text: %v", err)
	}
}

func TestLiveScriptShortcutsList(t *testing.T) {
	p := liveScripts(t)
	names, err := p.Shortcuts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d shortcuts", len(names))
}
