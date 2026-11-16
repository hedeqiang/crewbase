package acp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
)

// CLIInfo describes one subscription CLI for the settings UI.
type CLIInfo struct {
	ID           string   `json:"id"`         // agent id, e.g. "claude-code"
	Provider     string   `json:"provider"`   // llm adapter id, e.g. "acp-claude-code"
	Name         string   `json:"name"`       // display name
	Path         string   `json:"path"`       // resolved CLI binary ("" if missing)
	Version      string   `json:"version"`    // first line of `<cli> --version`
	Installed    bool     `json:"installed"`  // CLI binary found
	ACPCommand   []string `json:"acpCommand"` // how the bridge starts it (empty: no ACP entrypoint found)
	ACPNote      string   `json:"acpNote,omitempty"`
	LoggedIn     string   `json:"loggedIn"` // yes | no | unknown (best effort, no network)
	LoginCommand string   `json:"loginCommand"`
	Running      bool     `json:"running"` // ACP process currently up
	// Bridge is the ACP bridge package, when it is a known npm one.
	Bridge *llm.Bridge `json:"bridge,omitempty"`
}

// Logged-in states.
const (
	LoggedInYes     = "yes"
	LoggedInNo      = "no"
	LoggedInUnknown = "unknown"
)

var reVersion = regexp.MustCompile(`\d+\.\d+(\.\d+)?([-.+][0-9A-Za-z.-]+)?`)

// versionOf runs `<path> --version` (no quota use, no state change).
func versionOf(ctx context.Context, path string) string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	setProcessGroup(cmd)
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return ""
	}
	line := strings.TrimSpace(string(out))
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	if v := reVersion.FindString(line); v != "" {
		return v
	}
	return line
}

// homeDir is replaceable in tests.
var homeDir = os.UserHomeDir

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// loggedIn is a best-effort, offline guess from credential files and env.
// It never reads credential contents.
func loggedIn(id string) string {
	home, err := homeDir()
	if err != nil {
		return LoggedInUnknown
	}
	switch id {
	case "claude-code":
		if os.Getenv("ANTHROPIC_API_KEY") != "" || os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") != "" || exists(filepath.Join(home, ".claude", ".credentials.json")) {
			return LoggedInYes
		}
		// on macOS the token lives in the Keychain; ~/.claude.json alone
		// does not prove a login
		return LoggedInUnknown
	case "codex":
		if os.Getenv("OPENAI_API_KEY") != "" || exists(filepath.Join(home, ".codex", "auth.json")) {
			return LoggedInYes
		}
		return LoggedInNo
	case "antigravity":
		return LoggedInUnknown // signs in with Google in its own app data
	case "cursor":
		if os.Getenv("CURSOR_API_KEY") != "" {
			return LoggedInYes
		}
	}
	return LoggedInUnknown
}

// Detect inspects every configured/built-in agent. It runs only
// `<cli> --version`; it never starts an ACP session or touches logins.
func Detect(ctx context.Context, cfg Config) []CLIInfo {
	agents := agentList(cfg)
	out := make([]CLIInfo, len(agents))
	var wg sync.WaitGroup
	for i, ac := range agents {
		r := resolve(ac)
		out[i] = CLIInfo{ID: r.ID, Provider: providerID(r.ID), Name: r.Name, Path: r.cliPath, Installed: r.cliPath != "",
			ACPCommand: r.Command, ACPNote: r.entryNote, LoginCommand: r.LoginCommand, LoggedIn: LoggedInUnknown, Bridge: bridgeOf(r)}
		if !out[i].Installed {
			out[i].LoggedIn = LoggedInNo
			continue
		}
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			out[i].Version = versionOf(ctx, out[i].Path)
			out[i].LoggedIn = loggedIn(id)
		}(i, r.ID)
	}
	wg.Wait()
	return out
}

// providerID is the llm adapter id for an agent id.
func providerID(agentID string) string { return "acp-" + agentID }
