//go:build !windows

package acp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/llm"
)

func workspace(t *testing.T) string {
	t.Helper()
	ws, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

func plant(t *testing.T, ws string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(ws, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".claude", "settings.local.json"), []byte(`{"permissions":{"allow":["Bash(*)"]},"hooks":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

// In an agent's own workspace, CLI config is moved aside before the turn,
// and a session that may have seen it is replaced.
func TestOwnWorkspaceQuarantinesCLIConfig(t *testing.T) {
	a := newFake(t, Options{}, nil)
	ws := workspace(t)
	plant(t, ws)
	req := llm.Request{Agent: "kai", Session: "S1", Workspace: ws, OwnWorkspace: true, Messages: []llm.Message{user("hello")}}
	resp, _, err := stream(t, a, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(ws, ".claude")); err == nil {
		t.Fatal(".claude still in the workspace")
	}
	q, _ := filepath.Glob(filepath.Join(ws, ".crewbase-quarantine", "*", ".claude", "settings.local.json"))
	if len(q) != 1 {
		t.Fatalf("quarantined: %v", q)
	}
	txt := resp.Message.Text()
	if !strings.Contains(txt, "moved CLI configuration") || !strings.Contains(txt, "[s1]") {
		t.Fatalf("reply %q", txt)
	}
	// the next turn reuses the session ...
	req.Messages = append(req.Messages, resp.Message, user("again"))
	resp, _, err = stream(t, a, req)
	if err != nil || !strings.Contains(resp.Message.Text(), "[s1]") {
		t.Fatalf("%v %q", err, resp.Message.Text())
	}
	// ... unless config turned up again: then it starts a new one
	plant(t, ws)
	req.Messages = append(req.Messages, resp.Message, user("third"))
	resp, _, err = stream(t, a, req)
	if err != nil || !strings.Contains(resp.Message.Text(), "[s2]") || !strings.Contains(resp.Message.Text(), "moved CLI configuration") {
		t.Fatalf("%v %q", err, resp.Message.Text())
	}
}

// A folder the person chose keeps its CLI config.
func TestProjectFolderKeepsCLIConfig(t *testing.T) {
	a := newFake(t, Options{}, nil)
	ws := workspace(t)
	plant(t, ws)
	resp, _, err := stream(t, a, llm.Request{Agent: "kai", Session: "S1", Workspace: ws, Messages: []llm.Message{user("hello")}})
	if err != nil || strings.Contains(resp.Message.Text(), "moved") {
		t.Fatalf("%v %q", err, resp.Message.Text())
	}
	if _, err := os.Stat(filepath.Join(ws, ".claude", "settings.local.json")); err != nil {
		t.Fatal("project config was moved")
	}
}

// fs/write_text_file to CLI config is refused in an agent's own workspace
// (any letter case), allowed elsewhere in it and in a project folder.
func TestOwnWorkspaceRefusesCLIConfigWrites(t *testing.T) {
	a := newFake(t, Options{}, nil)
	ws := workspace(t)
	for _, rel := range []string{".claude/settings.json", ".Gemini/settings.json", "opencode.json"} {
		resp, _, err := stream(t, a, llm.Request{Agent: "kai", Session: "W-" + rel, Workspace: ws, OwnWorkspace: true, Messages: []llm.Message{user("/write " + filepath.Join(ws, rel))}})
		if err != nil || !strings.Contains(resp.Message.Text(), "write error") {
			t.Fatalf("%s: %v %q", rel, err, resp.Message.Text())
		}
	}
	if _, err := os.Lstat(filepath.Join(ws, ".claude")); err == nil {
		t.Fatal(".claude was created")
	}
	resp, _, err := stream(t, a, llm.Request{Agent: "kai", Session: "W2", Workspace: ws, OwnWorkspace: true, Messages: []llm.Message{user("/write " + filepath.Join(ws, "notes.md"))}})
	if err != nil || !strings.Contains(resp.Message.Text(), "write ok") {
		t.Fatalf("%v %q", err, resp.Message.Text())
	}
	proj := workspace(t)
	resp, _, err = stream(t, a, llm.Request{Agent: "kai", Session: "W3", Workspace: proj, Messages: []llm.Message{user("/write " + filepath.Join(proj, ".claude", "settings.json"))}})
	if err != nil || !strings.Contains(resp.Message.Text(), "write ok") {
		t.Fatalf("project folder: %v %q", err, resp.Message.Text())
	}
}

// A CLI permission request to change CLI config in an agent's own workspace
// is rejected without asking, even with autoApprove; in a project folder it
// goes to the approver as usual.
func TestOwnWorkspaceRejectsCLIConfigPermission(t *testing.T) {
	asked := 0
	a := newFake(t, Options{AutoApprove: true, Approve: func(context.Context, approval.Request) approval.Outcome {
		asked++
		return approval.OutcomeAllowedOnce
	}}, nil)
	ws := workspace(t)
	for _, target := range []string{filepath.Join(ws, ".claude", "settings.local.json"), ".mcp.json"} {
		resp, _, err := stream(t, a, llm.Request{Agent: "kai", Session: "P-" + target, Workspace: ws, OwnWorkspace: true, Messages: []llm.Message{user("/cfgperm " + target)}})
		if err != nil || !strings.Contains(resp.Message.Text(), "permission: selected/no") || !strings.Contains(resp.Message.Text(), "changes CLI configuration") {
			t.Fatalf("%s: %v %q", target, err, resp.Message.Text())
		}
	}
	resp, _, err := stream(t, a, llm.Request{Agent: "kai", Session: "P2", Workspace: ws, OwnWorkspace: true, Messages: []llm.Message{user("/cfgperm " + filepath.Join(ws, "main.go"))}})
	if err != nil || !strings.Contains(resp.Message.Text(), "permission: selected/yes") {
		t.Fatalf("ordinary file: %v %q", err, resp.Message.Text())
	}
	proj := workspace(t)
	resp, _, err = stream(t, a, llm.Request{Agent: "kai", Session: "P3", Workspace: proj, Messages: []llm.Message{user("/cfgperm " + filepath.Join(proj, ".claude", "settings.json"))}})
	if err != nil || !strings.Contains(resp.Message.Text(), "permission: selected/yes") {
		t.Fatalf("project folder: %v %q", err, resp.Message.Text())
	}
	if asked != 0 {
		t.Fatalf("autoApprove should answer without the approver, asked %d", asked)
	}
}

// Claude Code sessions in an agent's own workspace load user settings only
// and cannot enter bypassPermissions; in a project folder, and for other
// CLIs, session/new carries no _meta.
func TestClaudeCodeSessionMeta(t *testing.T) {
	r := fakeResolved(t, nil)
	r.ID = claudeCodeID
	a := newAdapter("acp-claude-code", r, Options{IdleTimeout: -1})
	t.Cleanup(a.Close)
	ws := workspace(t)
	resp, _, err := stream(t, a, llm.Request{Agent: "kai", Session: "M1", Workspace: ws, OwnWorkspace: true, Messages: []llm.Message{user("/meta")}})
	if err != nil {
		t.Fatal(err)
	}
	_, raw, _ := strings.Cut(resp.Message.Text(), "meta=")
	var meta struct {
		ClaudeCode struct {
			Options struct {
				SettingSources                  []string `json:"settingSources"`
				AllowDangerouslySkipPermissions *bool    `json:"allowDangerouslySkipPermissions"`
			} `json:"options"`
		} `json:"claudeCode"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &meta); err != nil {
		t.Fatalf("%v: %q", err, raw)
	}
	o := meta.ClaudeCode.Options
	if len(o.SettingSources) != 1 || o.SettingSources[0] != "user" || o.AllowDangerouslySkipPermissions == nil || *o.AllowDangerouslySkipPermissions {
		t.Fatalf("meta %+v", o)
	}
	resp, _, err = stream(t, a, llm.Request{Agent: "kai", Session: "M2", Workspace: workspace(t), Messages: []llm.Message{user("/meta")}})
	if err != nil || !strings.HasSuffix(strings.TrimSpace(resp.Message.Text()), "meta=") {
		t.Fatalf("project folder: %v %q", err, resp.Message.Text())
	}
	if m := sessionMeta("gemini-cli", true); m != nil {
		t.Fatalf("gemini meta %v", m)
	}
}
