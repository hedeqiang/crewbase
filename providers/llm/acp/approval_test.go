//go:build !windows

package acp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/kernel"
)

func TestPermissionRoutedToApproval(t *testing.T) {
	for _, outcome := range []approval.Outcome{approval.OutcomeAllowedOnce, approval.OutcomeRejected, approval.OutcomeUnavailable} {
		var mu sync.Mutex
		var got []approval.Request
		a := newFake(t, Options{Approve: func(_ context.Context, r approval.Request) approval.Outcome {
			mu.Lock()
			got = append(got, r)
			mu.Unlock()
			return outcome
		}}, nil)
		resp, _, err := stream(t, a, llm.Request{Messages: []llm.Message{user("/perm")}, Agent: "lead", Session: "S1"})
		if err != nil {
			t.Fatal(err)
		}
		want := "permission: selected/no"
		if outcome == approval.OutcomeAllowedOnce {
			want = "permission: selected/yes"
		}
		if txt := resp.Message.Text(); !strings.Contains(txt, want) {
			t.Fatalf("%s: %q", outcome, txt)
		}
		mu.Lock()
		if len(got) != 1 {
			t.Fatalf("asked %d times", len(got))
		}
		r := got[0]
		mu.Unlock()
		if r.CallID != "acp/S1/t1" || r.Session != "S1" || r.Agent != "lead" || r.Category != "exec" || r.Summary != "rm -rf build" || r.Tool != "acp-fake" {
			t.Fatalf("request %+v", r)
		}
	}
}

func TestAutoApproveOverridesApproval(t *testing.T) {
	asked := false
	a := newFake(t, Options{AutoApprove: true, Approve: func(context.Context, approval.Request) approval.Outcome {
		asked = true
		return approval.OutcomeRejected
	}}, nil)
	resp, _, err := stream(t, a, llm.Request{Messages: []llm.Message{user("/perm")}, Agent: "lead", Session: "S1"})
	if err != nil || !strings.Contains(resp.Message.Text(), "selected/yes") || asked {
		t.Fatalf("%v %q asked=%v", err, resp.Message.Text(), asked)
	}
}

func TestPermissionCategory(t *testing.T) {
	for kind, want := range map[string]string{"read": "read", "edit": "write", "delete": "delete", "execute": "exec", "fetch": "read", "other": "exec", "": "exec"} {
		if got := permissionCategory(kind); got != want {
			t.Errorf("%q: %s, want %s", kind, got, want)
		}
	}
}

// The plugin's approver asks the approval service with the Crewbase session.
func TestApproverUsesApprovalService(t *testing.T) {
	h := kernel.NewHost(nil)
	t.Cleanup(h.Close)
	for _, s := range []kernel.Spec{session.Plugin, approval.Plugin, approval.DesktopPlugin} {
		if _, err := h.Load(s, kernel.LoadOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	ss, err := kernel.Use[*session.Store](h.Root(), session.ServiceKey).Create(session.Meta{Kind: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	inbox := kernel.Use[*approval.Inbox](h.Root(), approval.InboxKey)
	go func() {
		for {
			for _, r := range inbox.Pending() {
				inbox.Decide(r.CallID, approval.Decision{Outcome: approval.OutcomeAllowedOnce})
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	a := newFake(t, Options{Approve: approver(h.Root())}, nil)
	resp, _, err := stream(t, a, llm.Request{Messages: []llm.Message{user("/perm")}, Agent: "lead", Session: ss.ID()})
	if err != nil || !strings.Contains(resp.Message.Text(), "selected/yes") {
		t.Fatalf("%v %q", err, resp.Message.Text())
	}
	if _, ok := ss.Last(session.TypeApprovalDecided); !ok {
		t.Fatal("decision not recorded in the session")
	}
	// unknown session: unavailable -> rejected
	resp, _, _ = stream(t, a, llm.Request{Messages: []llm.Message{user("/perm")}, Agent: "lead", Session: "nope"})
	if !strings.Contains(resp.Message.Text(), "selected/no") {
		t.Fatalf("unknown session allowed: %q", resp.Message.Text())
	}
}

func TestWorkspaceCwdAndSessionKey(t *testing.T) {
	a := newFake(t, Options{}, nil)
	ws, _ := filepath.EvalSymlinks(t.TempDir())
	req := llm.Request{Messages: []llm.Message{user("/cwd")}, Agent: "lead", Session: "S1", Workspace: ws}
	resp, _, err := stream(t, a, req)
	if err != nil || !strings.Contains(resp.Message.Text(), "cwd="+ws) {
		t.Fatalf("%v %q", err, resp.Message.Text())
	}
	// the next step of the same Crewbase session reuses the ACP session
	req.Messages = append(req.Messages, resp.Message, user("/cwd"))
	resp, _, _ = stream(t, a, req)
	if !strings.Contains(resp.Message.Text(), "[s1] cwd="+ws) {
		t.Fatalf("session not reused: %q", resp.Message.Text())
	}
	// fs access is confined to the workspace (outside the configured cwd)
	f := filepath.Join(ws, "w.txt")
	_ = os.WriteFile(f, []byte("a\nb\n"), 0o644)
	req.Messages = append(req.Messages, resp.Message, user("/read "+f))
	resp, _, _ = stream(t, a, req)
	if !strings.Contains(resp.Message.Text(), "read: b") {
		t.Fatalf("workspace read: %q", resp.Message.Text())
	}
	// same first message, different Crewbase session: separate ACP session
	resp, _, _ = stream(t, a, llm.Request{Messages: []llm.Message{user("/cwd")}, Agent: "lead", Session: "S2"})
	if !strings.Contains(resp.Message.Text(), "[s2] cwd="+a.cfg.Cwd) {
		t.Fatalf("fallback cwd: %q", resp.Message.Text())
	}
	if convKey(llm.Request{Messages: []llm.Message{user("x")}}, "/a") == convKey(llm.Request{Messages: []llm.Message{user("y")}}, "/a") {
		t.Fatal("prompt-hash fallback ignores the first message")
	}
}

func TestThinkingPartsIgnored(t *testing.T) {
	plain := llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: "text", Text: "hi"}}}
	withThinking := llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{
		{Type: "thinking", Text: "hmm", Meta: map[string]string{"anthropic:signature": "sig"}},
		{Type: "text", Text: "hi"},
	}}
	if fingerprint(plain) != fingerprint(withThinking) {
		t.Fatal("thinking part changes the fingerprint")
	}
	if renderMessage(withThinking) != "hi" {
		t.Fatalf("thinking rendered: %q", renderMessage(withThinking))
	}
}
