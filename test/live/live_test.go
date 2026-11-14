//go:build live

// Package live runs Crewbase end to end against real models served by a
// local magpie gateway. It costs real tokens, so it only runs with
//
//	go test -tags live ./test/live -v
//
// CREWBASE_LIVE_MODELS overrides the chat matrix (comma separated refs).
package live

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/apps/launch"
	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/features/channel"
	"github.com/hedeqiang/crewbase/features/workflows"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/magpie"
)

const toolModel = "magpie/auto/claude-haiku-5-5"

var chatModels = []string{
	"magpie/auto/claude-haiku-5-5",
	"magpie/linkrouter/claude-sonnet-5-5@low",
	"magpie/deepseek/deepseek-flash",
	"magpie/openai/gpt-6-luna",
	"magpie/codex/gpt-5.6-luna",
	"magpie/cursor/composer-2.5",
}

type env struct {
	h      *kernel.Host
	agents *agent.Registry
	inbox  *approval.Inbox
	root   string
}

func start(t *testing.T) *env {
	t.Helper()
	req, _ := http.NewRequest("GET", magpie.DiscoverBaseURL()+"/api/hello", nil)
	if resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req); err != nil {
		t.Skip("magpie is not running")
	} else {
		resp.Body.Close()
	}
	root := t.TempDir()
	patch := filepath.Join(root, "live.patch.yml")
	os.WriteFile(patch, []byte(`
- id: approval-desktop
  name: '@crewbase/approval-desktop'
- id: llm-acp
  disabled: true
- id: llm-local
  disabled: true
- id: agents
  name: '@crewbase/agents'
  config:
    root: `+root+`
    defaultModels: ['`+toolModel+`']
`), 0o600)
	h, err := launch.Start(launch.Options{Profile: "sdk", Root: root, Patches: []string{patch}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	for _, p := range h.Plugins() {
		if p.State != kernel.StateActive {
			t.Fatalf("plugin %s %s %v %s", p.ID, p.State, p.Missing, p.Error)
		}
	}
	r := h.Root()
	if _, ok := kernel.Use[*llm.Registry](r, llm.ServiceKey).Get("magpie"); !ok {
		t.Fatal("magpie adapter not registered")
	}
	return &env{h: h, root: root,
		agents: kernel.Use[*agent.Registry](r, agent.ServiceKey),
		inbox:  kernel.Use[*approval.Inbox](r, approval.InboxKey)}
}

func (e *env) newAgent(t *testing.T, name, role string, models ...string) *agent.Agent {
	t.Helper()
	var refs []llm.ModelRef
	for _, m := range models {
		ref, err := llm.ParseRef(m)
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, ref)
	}
	a, err := e.agents.Create(agent.Spec{Name: name, Role: role, Models: refs})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func ask(t *testing.T, a *agent.Agent, text string, timeout time.Duration) (string, session.AssistantMessage) {
	t.Helper()
	ss, _ := a.Session()
	in, err := a.Enqueue(agent.Input{Text: text})
	if err != nil {
		t.Fatal(err)
	}
	return waitTurn(t, a, ss, in.ID, timeout)
}

func waitTurn(t *testing.T, a *agent.Agent, ss *session.Session, inputID string, timeout time.Duration) (string, session.AssistantMessage) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var started bool
		for _, e := range ss.Events() {
			if e.Type == session.TypeTurnStart && e.Agent == a.ID() {
				var ts agentloop.TurnStart
				e.Decode(&ts)
				started = started || ts.Input == inputID
			}
			if started && e.Type == session.TypeTurnEnd && e.Agent == a.ID() {
				var te agentloop.TurnEnd
				e.Decode(&te)
				if te.Status != "completed" {
					t.Fatalf("turn %s: %s", te.Status, te.Error)
				}
				last, _ := ss.Last(session.TypeAssistantMessage)
				var m session.AssistantMessage
				last.Decode(&m)
				return m.Message.Text(), m
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("turn did not finish in %s", timeout)
	return "", session.AssistantMessage{}
}

func count(ss *session.Session, typ string) int {
	n := 0
	for _, e := range ss.Events() {
		if e.Type == typ {
			n++
		}
	}
	return n
}

func TestChatAcrossVendors(t *testing.T) {
	e := start(t)
	models := chatModels
	if v := os.Getenv("CREWBASE_LIVE_MODELS"); v != "" {
		models = strings.Split(v, ",")
	}
	for i, m := range models {
		m := strings.TrimSpace(m)
		t.Run(m, func(t *testing.T) {
			a := e.newAgent(t, fmt.Sprintf("Echo%d", i), "test", m)
			t0 := time.Now()
			text, msg := ask(t, a, "Reply with exactly the word PONG and nothing else.", 2*time.Minute)
			if !strings.Contains(strings.ToUpper(text), "PONG") {
				t.Fatalf("reply %q", text)
			}
			if msg.Model.Full() != m {
				t.Fatalf("answered by %s", msg.Model.Full())
			}
			t.Logf("%s: %q in %s (tokens in=%d out=%d)", m, text, time.Since(t0).Round(time.Millisecond), msg.Usage.InputTokens, msg.Usage.OutputTokens)
		})
	}
}

func TestToolUseInSandbox(t *testing.T) {
	e := start(t)
	a := e.newAgent(t, "Kai", "engineer", toolModel)
	text, _ := ask(t, a, "Use the bash tool to run: echo crewbase-$((6*7)) > out.txt && cat out.txt\nThen tell me the exact output.", 3*time.Minute)
	ss, _ := a.Session()
	if count(ss, session.TypeToolCall) == 0 {
		t.Fatal("model did not call a tool")
	}
	if !strings.Contains(text, "crewbase-42") {
		t.Fatalf("reply %q", text)
	}
	b, err := os.ReadFile(filepath.Join(a.Workspace(), "out.txt"))
	if err != nil || strings.TrimSpace(string(b)) != "crewbase-42" {
		t.Fatalf("file in workspace: %q %v", b, err)
	}
	t.Logf("reply: %s", text)
}

func TestPushNeedsApprovalAndRejectionIsHandled(t *testing.T) {
	e := start(t)
	a := e.newAgent(t, "Rex", "release engineer", toolModel)
	ss, _ := a.Session()
	in, _ := a.Enqueue(agent.Input{Text: "Use the bash tool to run exactly this command and nothing else: git push origin main"})
	var req approval.Request
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		if p := e.inbox.Pending(); len(p) > 0 {
			req = p[0]
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if req.CallID == "" {
		t.Fatal("no approval was requested")
	}
	if req.Category != "push" || req.Agent != a.ID() {
		t.Fatalf("%+v", req)
	}
	e.inbox.Decide(req.CallID, approval.Decision{Outcome: approval.OutcomeRejected, Message: "not today, the release is frozen"})
	text, _ := waitTurn(t, a, ss, in.ID, 2*time.Minute)
	last, _ := ss.Last(session.TypeToolResult)
	var r llm.ToolResult
	last.Decode(&r)
	if !r.IsError || !strings.Contains(r.Content, "frozen") {
		t.Fatalf("tool result %+v", r)
	}
	t.Logf("agent after rejection: %s", text)
}

func TestFailoverToNextModel(t *testing.T) {
	e := start(t)
	// magpie answers 404 for a model it no longer serves: fail over.
	a := e.newAgent(t, "Iris", "analyst", "magpie/retired/model-x", toolModel)
	text, msg := ask(t, a, "Reply with exactly OK.", 2*time.Minute)
	ss, _ := a.Session()
	sw, ok := ss.Last(session.TypeModelSwitched)
	if !ok {
		t.Fatal("no model/switched event")
	}
	var d session.ModelSwitchData
	sw.Decode(&d)
	if d.From.Model != "retired/model-x" || d.Reason != "model_not_found" || d.To.Full() != toolModel || msg.Model.Full() != toolModel {
		t.Fatalf("%+v answered by %s", d, msg.Model.Full())
	}
	t.Logf("switched %s → %s (%s); reply %q", d.From, d.To, d.Reason, text)
}

func TestMemoryTool(t *testing.T) {
	e := start(t)
	a := e.newAgent(t, "Otto", "assistant", toolModel)
	ask(t, a, "Save this to your long-term memory with the memory_add tool: the user's favourite colour is teal.", 2*time.Minute)
	if !strings.Contains(strings.ToLower(a.Memory()), "teal") {
		t.Fatalf("MEMORY.md: %q", a.Memory())
	}
}

func TestChannelHandoverWithRealModels(t *testing.T) {
	e := start(t)
	nova := e.newAgent(t, "Nova", "researcher", toolModel)
	kai := e.newAgent(t, "Kai", "engineer", toolModel)
	ch := kernel.Use[*channel.Service](e.h.Root(), channel.ServiceKey)
	v, err := ch.Create("live-test", []string{nova.ID(), kai.ID()}, "", "tester")
	if err != nil {
		t.Fatal(err)
	}
	_, err = ch.Post(v.ID, "@Nova Write one sentence about why Go is good for daemons. "+
		"Then hand the task over to Kai with the handover tool (note: \"review the sentence\"). "+
		"Kai: when you receive the handover, reply with one word: REVIEWED.", "tester")
	if err != nil {
		t.Fatal(err)
	}
	ss, _ := e.agents.Sessions().Get(v.ID)
	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		if cur, _ := ch.Get(v.ID); cur.Owner == kai.ID() {
			if e, ok := ss.Last(session.TypeAssistantMessage); ok && e.Agent == kai.ID() {
				var m session.AssistantMessage
				e.Decode(&m)
				t.Logf("Kai: %s", m.Message.Text())
				if count(ss, channel.TypeHandover) != 1 {
					t.Fatal("expected exactly one handover")
				}
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	b, _ := json.MarshalIndent(ss.Events(), "", " ")
	t.Fatalf("no handover to Kai: %s", b[:min(len(b), 4000)])
	_ = context.Background
}

func TestWorkflowWithApprovalGateAndParallelSteps(t *testing.T) {
	e := start(t)
	writer := e.newAgent(t, "Writer", "writes short copy", toolModel)
	critic := e.newAgent(t, "Critic", "reviews copy", "magpie/deepseek/deepseek-flash")
	editor := e.newAgent(t, "Editor", "finalises copy", "magpie/openai/gpt-6-luna")
	wfs := kernel.Use[*workflows.Service](e.h.Root(), workflows.ServiceKey)
	v, err := wfs.Create(workflows.Workflow{Name: "slogan", Trigger: "manual", Steps: []workflows.Step{
		{ID: "draft", Kind: "agent", Agent: writer.ID(), Prompt: "Write one short slogan (max 8 words) for: {{input}}. Output only the slogan."},
		{ID: "gate", Kind: "approval", Approval: &workflows.ApprovalGate{Summary: "Publish-check slogan: {{prev}}"}},
		{ID: "reviews", Kind: "parallel", Parallel: []workflows.Step{
			{ID: "r1", Kind: "agent", Agent: critic.ID(), Prompt: "In one sentence, critique this slogan: {{steps.draft}}"},
			{ID: "r2", Kind: "agent", Agent: editor.ID(), Prompt: "Rate this slogan 1-10, reply with just the number: {{steps.draft}}"},
		}},
		{ID: "final", Kind: "agent", Agent: writer.ID(), Prompt: "Given these reviews:\n{{prev}}\nRewrite the slogan '{{steps.draft}}' once. Output only the final slogan."},
	}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := wfs.Start(v.ID, "a local-first AI team workbench")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Minute)
	for len(e.inbox.Pending()) == 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	p := e.inbox.Pending()
	if len(p) != 1 {
		t.Fatal("workflow gate did not ask for approval")
	}
	t.Logf("gate: %s", p[0].Summary)
	e.inbox.Decide(p[0].CallID, approval.Decision{Outcome: approval.OutcomeAllowedOnce})
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	done, err := wfs.Wait(ctx, run.ID)
	if err != nil || done.Status != "completed" {
		t.Fatalf("%v %+v", err, done)
	}
	for _, s := range done.Steps {
		t.Logf("step %-7s %-9s %s", s.ID, s.Status, strings.ReplaceAll(s.Output, "\n", " "))
	}
	if strings.TrimSpace(done.Output) == "" {
		t.Fatal("empty final output")
	}
}

func TestDelegateToSubagent(t *testing.T) {
	e := start(t)
	lead := e.newAgent(t, "Lead", "team lead", toolModel)
	text, _ := ask(t, lead, "Use the delegate tool to have a subagent compute 17*23 and reply with just the number. Then tell me the number it returned.", 3*time.Minute)
	ss, _ := lead.Session()
	if count(ss, "subagent/spawned") == 0 {
		t.Fatal("no subagent was spawned")
	}
	if !strings.Contains(text, "391") {
		t.Fatalf("reply %q", text)
	}
	for _, a := range e.agents.List() {
		if strings.Contains(a.ID(), "-sub-") {
			t.Fatalf("ephemeral subagent %s not cleaned up", a.ID())
		}
	}
	t.Logf("lead: %s", text)
}

func TestDeepSeekNativeWebSearch(t *testing.T) {
	e := start(t)
	a := e.newAgent(t, "Scout", "researcher", "magpie/deepseek/deepseek-flash@high")
	a.Update(func(s *agent.Spec) { s.NativeTools = []string{llm.NativeWebSearch} })
	text, msg := ask(t, a, "DeepSeek 在 2026 年 11 月 13 日发布了什么？一句话回答。", 3*time.Minute)
	var cites, searches int
	for _, p := range msg.Message.Parts {
		switch p.Type {
		case llm.PartCitation:
			cites++
			if !strings.HasPrefix(p.Meta["url"], "http") {
				t.Fatalf("citation without url: %+v", p)
			}
		case llm.PartServerTool:
			searches++
		}
	}
	if cites == 0 || searches == 0 || msg.Usage.ServerTools["tool_search"] == 0 {
		t.Fatalf("cites=%d searches=%d usage=%+v text=%q", cites, searches, msg.Usage, text)
	}
	// provider-only parts never go back to a model
	ss, _ := a.Session()
	for _, m := range session.DeriveMessages(ss.Events(), a.ID(), nil) {
		for _, p := range m.Parts {
			if p.Type == llm.PartCitation || p.Type == llm.PartServerTool {
				t.Fatal("citation/server_tool part leaked into model history")
			}
		}
	}
	t.Logf("%s\n(citations=%d, search steps=%d, billed searches=%v)", text, cites, searches, msg.Usage.ServerTools)
}
