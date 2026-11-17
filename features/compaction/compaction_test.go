package compaction_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/compaction"
	"github.com/hedeqiang/crewbase/features/memory"
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/llm/mock"
)

const window = 4000

type env struct {
	h      *kernel.Host
	agents *agent.Registry
	tools  *tools.Registry
	comp   *compaction.Service
	mem    *memory.Store
	mock   *mock.Adapter
	fail   atomic.Bool // summarisation requests fail
	sums   atomic.Int32
}

func text(s string) llm.Response {
	return llm.Response{StopReason: "end", Message: llm.TextMessage(llm.RoleAssistant, s)}
}

// script mimics the mock provider's default behaviour (tool calls via "/tool"), but
// acknowledges tool results briefly and answers summarisation requests.
func (e *env) script(req llm.Request) (llm.Response, error) {
	if strings.HasPrefix(req.System, compaction.SummaryMarker) {
		e.sums.Add(1)
		if e.fail.Load() {
			return llm.Response{}, &llm.Error{Kind: llm.ErrUnavailable, Provider: "tiny", Message: "summariser down"}
		}
		return text("SUMMARY: the user sent filler messages and ran big_output."), nil
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role == llm.RoleTool {
		r := last.Parts[0].ToolResult
		return text(fmt.Sprintf("Tool %s returned %d chars", r.Name, len(r.Content))), nil
	}
	t := strings.TrimSpace(last.Text())
	if i := strings.LastIndex(t, "\n\n"); i >= 0 {
		t = strings.TrimSpace(t[i+2:])
	}
	if rest, ok := strings.CutPrefix(t, "/tool "); ok {
		name, _, _ := strings.Cut(rest, " ")
		return llm.Response{StopReason: "tool_use", Message: llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{
			{Type: "tool_call", ToolCall: &llm.ToolCall{ID: fmt.Sprintf("c%d", time.Now().UnixNano()), Name: name, Args: json.RawMessage(`{}`)}},
		}}}, nil
	}
	return text("ack: " + t), nil
}

func newEnv(t *testing.T, root string, compCfg map[string]any, pre ...func(load func(kernel.Spec, map[string]any))) *env {
	t.Helper()
	e := &env{h: kernel.NewHost(nil)}
	t.Cleanup(e.h.Close)
	load := func(s kernel.Spec, cfg map[string]any) {
		if _, err := e.h.Load(s, kernel.LoadOptions{Config: cfg}); err != nil {
			t.Fatal(err)
		}
	}
	load(session.Plugin, map[string]any{"dir": root + "/sessions"})
	load(prompt.Plugin, nil)
	load(tools.Plugin, nil)
	load(llm.Plugin, nil)
	e.mock = mock.New("tiny", window)
	e.mock.Script = e.script
	load(kernel.Spec{Name: "test-env", Inject: []string{llm.ServiceKey, tools.ServiceKey}, Apply: func(c *kernel.Context) error {
		kernel.Use[*tools.Registry](c, tools.ServiceKey).Register(c, &tools.Tool{
			Name: "big_output", Category: tools.CategoryRead,
			Execute: func(context.Context, tools.Call) (tools.Result, error) {
				return tools.Text("BEGIN " + strings.Repeat("lorem ipsum dolor ", 170) + " END"), nil
			},
		})
		return kernel.Use[*llm.Registry](c, llm.ServiceKey).Register(c, e.mock)
	}}, nil)
	load(agent.Plugin, map[string]any{"root": root, "defaultModels": []string{"tiny/default"}})
	load(agentloop.Plugin, nil)
	for _, f := range pre {
		f(load)
	}
	load(compaction.Plugin, compCfg)
	load(memory.Plugin, nil)
	for _, p := range e.h.Plugins() {
		if p.State != kernel.StateActive {
			t.Fatalf("plugin not active: %+v", p)
		}
	}
	r := e.h.Root()
	e.agents = kernel.Use[*agent.Registry](r, agent.ServiceKey)
	e.tools = kernel.Use[*tools.Registry](r, tools.ServiceKey)
	e.comp = kernel.Use[*compaction.Service](r, agentloop.CompactionKey)
	e.mem = kernel.Use[*memory.Store](r, memory.ServiceKey)
	return e
}

func send(t *testing.T, a *agent.Agent, sess, text string) {
	t.Helper()
	if _, err := a.Enqueue(agent.Input{Text: text, Session: sess}); err != nil {
		t.Fatal(err)
	}
	if !agentloop.WaitIdle(a, 5*time.Second) {
		t.Fatal("agent did not become idle")
	}
	if st, d := a.Status(); st == agent.StatusFailed {
		t.Fatalf("turn failed: %s", d)
	}
}

func filler(i int) string {
	return fmt.Sprintf("message %d: ", i) + strings.Repeat(fmt.Sprintf("filler%d words go here ", i), 40)
}

// checkPairs asserts no compaction separates a tool call from its result.
func checkPairs(t *testing.T, events []session.Event, agentID string) {
	t.Helper()
	callSeq := map[string]int64{}
	resSeq := map[string]int64{}
	for _, e := range events {
		switch e.Type {
		case session.TypeAssistantMessage:
			var m session.AssistantMessage
			e.Decode(&m)
			for _, tc := range m.Message.ToolCalls() {
				callSeq[tc.ID] = e.Seq
			}
		case session.TypeToolResult:
			var r llm.ToolResult
			e.Decode(&r)
			resSeq[r.CallID] = e.Seq
		}
	}
	for _, e := range events {
		if e.Type != session.TypeCompaction {
			continue
		}
		var c session.CompactionData
		e.Decode(&c)
		for id, cs := range callSeq {
			if (cs <= c.UptoSeq) != (resSeq[id] <= c.UptoSeq) {
				t.Fatalf("compaction upto %d splits call %s (call %d, result %d)", c.UptoSeq, id, cs, resSeq[id])
			}
		}
	}
}

// checkRequests asserts every request is well-formed: tool results follow
// the assistant message that called them.
func checkRequests(t *testing.T, reqs []llm.Request) {
	t.Helper()
	for i, r := range reqs {
		open := map[string]bool{}
		for _, m := range r.Messages {
			switch m.Role {
			case llm.RoleAssistant:
				open = map[string]bool{}
				for _, tc := range m.ToolCalls() {
					open[tc.ID] = true
				}
			case llm.RoleTool:
				id := m.Parts[0].ToolResult.CallID
				if !open[id] {
					t.Fatalf("request %d: orphan tool result %s", i, id)
				}
			}
		}
	}
}

func compactions(events []session.Event) []session.CompactionData {
	var out []session.CompactionData
	for _, e := range events {
		if e.Type == session.TypeCompaction {
			var c session.CompactionData
			e.Decode(&c)
			out = append(out, c)
		}
	}
	return out
}

func lastEnd(t *testing.T, ss *session.Session) compaction.End {
	t.Helper()
	ev, ok := ss.Last(compaction.TypeEnd)
	if !ok {
		t.Fatal("no compaction/end")
	}
	var end compaction.End
	ev.Decode(&end)
	return end
}

func TestAutoCompactionThroughLoop(t *testing.T) {
	e := newEnv(t, t.TempDir(), nil)
	a, err := e.agents.Create(agent.Spec{Name: "Kai", Role: "engineer"})
	if err != nil {
		t.Fatal(err)
	}
	ss, _ := a.Session()
	sid := ss.ID()

	send(t, a, sid, "remember the zebracorn deployment plan please")
	send(t, a, sid, "/tool big_output {}")
	for i := 0; i < 12 && len(compactions(ss.Events())) == 0; i++ {
		send(t, a, sid, filler(i))
	}
	comps := compactions(ss.Events())
	if len(comps) == 0 {
		t.Fatal("compaction never triggered")
	}
	// start / summary / end all recorded
	if _, ok := ss.Last(compaction.TypeStart); !ok {
		t.Fatal("no compaction/start")
	}
	end := lastEnd(t, ss)
	if end.Method != "model" || end.Status != "completed" {
		t.Fatalf("end %+v", end)
	}
	c := comps[0]
	if !strings.Contains(c.Summary, "SUMMARY:") || c.After >= c.Before || c.Before == 0 {
		t.Fatalf("compaction %+v", c)
	}
	if e.sums.Load() == 0 {
		t.Fatal("model was not asked to summarise")
	}

	// Keep going past a second compaction with tool calls in between.
	for i := 20; i < 30; i++ {
		if i%3 == 0 {
			send(t, a, sid, "/tool big_output {}")
		} else {
			send(t, a, sid, filler(i))
		}
	}
	events := ss.Events()
	checkPairs(t, events, a.ID())
	checkRequests(t, e.mock.Requests())
	for i, r := range e.mock.Requests() {
		if est := llm.EstimateRequest(r); est > window {
			t.Fatalf("request %d over window: %d", i, est)
		}
	}

	// DeriveMessages starts with the summary and is smaller than the raw history.
	msgs := session.DeriveMessages(events, a.ID(), e.agents.Names())
	if !strings.HasPrefix(msgs[0].Text(), "[Summary of earlier conversation]") {
		t.Fatalf("first message %q", msgs[0].Text())
	}
	var raw []session.Event
	for _, ev := range events {
		if ev.Type != session.TypeCompaction {
			raw = append(raw, ev)
		}
	}
	full := session.DeriveMessages(raw, a.ID(), e.agents.Names())
	if llm.EstimateRequest(llm.Request{Messages: msgs}) >= llm.EstimateRequest(llm.Request{Messages: full}) || len(msgs) >= len(full) {
		t.Fatal("derived history did not shrink")
	}
	for _, m := range msgs {
		if strings.Contains(m.Text(), "zebracorn") {
			t.Fatal("first turn should have been compacted away")
		}
	}

	// history_search finds the compacted-away text; history_read returns it.
	ex := e.tools.Run(context.Background(), e.h.Root(), tools.Call{ID: "h1", Name: "history_search", Agent: a.ID(), Args: json.RawMessage(`{"query":"zebracorn plan"}`)})
	hits, _ := ex.Result.Data.([]memory.Hit)
	if ex.Result.IsError || len(hits) == 0 {
		t.Fatalf("history_search: %+v", ex.Result)
	}
	h := hits[0]
	if h.Session != sid || h.Seq > comps[len(comps)-1].UptoSeq || !strings.Contains(ex.Result.Content, "«zebracorn»") {
		t.Fatalf("hit %+v", h)
	}
	args, _ := json.Marshal(map[string]any{"session": h.Session, "from": h.Seq, "to": h.Seq + 3})
	rd := e.tools.Run(context.Background(), e.h.Root(), tools.Call{ID: "h2", Name: "history_read", Agent: a.ID(), Args: args})
	if rd.Result.IsError || !strings.Contains(rd.Result.Content, "remember the zebracorn deployment plan") {
		t.Fatalf("history_read: %q", rd.Result.Content)
	}
	// Another agent cannot read Kai's conversation.
	other, _ := e.agents.Create(agent.Spec{Name: "Otto"})
	rd = e.tools.Run(context.Background(), e.h.Root(), tools.Call{ID: "h3", Name: "history_read", Agent: other.ID(), Args: args})
	if !rd.Result.IsError {
		t.Fatal("history_read must be limited to the agent's sessions")
	}

	// The system prompt still says what compaction keeps.
	sys := kernel.Use[*prompt.Assembler](e.h.Root(), prompt.ServiceKey).Assemble(a.ID())
	if !strings.Contains(sys, "instructions and long-term memory (MEMORY.md) are always kept") || !strings.Contains(sys, "history_search") {
		t.Fatalf("system prompt: %s", sys)
	}
}

func TestFallbackSummaryWhenModelFails(t *testing.T) {
	e := newEnv(t, t.TempDir(), nil)
	e.fail.Store(true)
	a, _ := e.agents.Create(agent.Spec{Name: "Iris"})
	ss, _ := a.Session()
	send(t, a, ss.ID(), "first request about the walrus migration")
	send(t, a, ss.ID(), "/tool big_output {}")
	for i := 0; i < 12 && len(compactions(ss.Events())) == 0; i++ {
		send(t, a, ss.ID(), filler(i))
	}
	comps := compactions(ss.Events())
	if len(comps) == 0 {
		t.Fatal("compaction never triggered")
	}
	end := lastEnd(t, ss)
	if end.Method != "extractive" || !strings.Contains(end.Error, "summariser down") {
		t.Fatalf("end %+v", end)
	}
	if !strings.HasPrefix(comps[0].Summary, "[Extractive summary") || !strings.Contains(comps[0].Summary, "walrus migration") {
		t.Fatalf("summary %q", comps[0].Summary)
	}
	// big tool results are pruned in the extractive input too
	if end.Pruned == 0 {
		t.Fatalf("expected pruned tool results: %+v", end)
	}
	// the turn itself still completed
	if st, _ := a.Status(); st != agent.StatusIdle {
		t.Fatalf("status %s", st)
	}
	ev, _ := ss.Last(session.TypeTurnEnd)
	var te agentloop.TurnEnd
	ev.Decode(&te)
	if te.Status != "completed" {
		t.Fatalf("turn end %+v", te)
	}
}

func TestCutsNeverSplitToolPairs(t *testing.T) {
	st, _ := session.NewStore(nil, "")
	ss, _ := st.Create(session.Meta{Kind: "agent", Agents: []string{"k"}})
	call := func(ids ...string) session.AssistantMessage {
		m := llm.Message{Role: llm.RoleAssistant}
		for _, id := range ids {
			m.Parts = append(m.Parts, llm.Part{Type: "tool_call", ToolCall: &llm.ToolCall{ID: id, Name: "x", Args: json.RawMessage(`{}`)}})
		}
		return session.AssistantMessage{Message: m}
	}
	res := func(id string) llm.ToolResult {
		return llm.ToolResult{CallID: id, Name: "x", Content: strings.Repeat("r", 400)}
	}
	say := func(s string) session.AssistantMessage {
		return session.AssistantMessage{Message: llm.TextMessage(llm.RoleAssistant, s)}
	}
	for turn := 0; turn < 4; turn++ {
		p := fmt.Sprint(turn)
		ss.Append(session.TypeUserMessage, "", session.UserMessage{Text: strings.Repeat("u", 200)})
		ss.Append(session.TypeTurnStart, "k", nil)
		ss.Append(session.TypeAssistantMessage, "k", call("a"+p, "b"+p))
		ss.Append(session.TypeToolCall, "k", nil)
		ss.Append(session.TypeToolResult, "k", res("a"+p))
		ss.Append(session.TypeToolResult, "k", res("b"+p))
		ss.Append(session.TypeStepEnd, "k", nil)
		ss.Append(session.TypeAssistantMessage, "k", call("c"+p))
		ss.Append(session.TypeToolResult, "k", res("c"+p))
		ss.Append(session.TypeAssistantMessage, "k", say("done "+p))
		ss.Append(session.TypeTurnEnd, "k", nil)
	}
	events := ss.Events()
	cuts := compaction.Cuts(events, "k", 0)
	if len(cuts) == 0 {
		t.Fatal("no cuts")
	}
	seqOf := map[int64]session.Event{}
	for _, ev := range events {
		seqOf[ev.Seq] = ev
	}
	for keep := 0; keep < 3000; keep += 37 {
		upto, ok := compaction.ChooseCut(events, "k", 0, keep, 1<<30)
		if !ok {
			t.Fatal("no cut chosen")
		}
		// simulate the compaction event and verify the pairing invariant
		d, _ := json.Marshal(session.CompactionData{Summary: "s", UptoSeq: upto})
		withComp := append(append([]session.Event(nil), events...), session.Event{Seq: int64(len(events) + 1), Type: session.TypeCompaction, Agent: "k", Data: d})
		checkPairs(t, withComp, "k")
		msgs := session.DeriveMessages(withComp, "k", nil)
		checkRequests(t, []llm.Request{{Messages: msgs}})
		for _, m := range msgs {
			for _, p := range m.Parts {
				if p.ToolResult != nil && p.ToolResult.Content == session.InterruptedResult {
					t.Fatalf("cut %d produced an interrupted result", upto)
				}
			}
		}
		// turn boundaries are preferred: the next visible event is a user message
		next := seqOf[upto+1]
		if next.Type != session.TypeUserMessage && next.Type != session.TypeAssistantMessage {
			t.Fatalf("cut %d is not before a message (%s)", upto, next.Type)
		}
	}
	// With a small maxKeep and a long last turn, a step boundary is chosen.
	upto, _ := compaction.ChooseCut(events, "k", 0, 50, 60)
	if seqOf[upto+1].Type != session.TypeAssistantMessage {
		t.Fatalf("expected a step boundary, got %s", seqOf[upto+1].Type)
	}
}

func TestUsageBreakdownAndConversationControls(t *testing.T) {
	e := newEnv(t, t.TempDir(), map[string]any{"keepRecentTokens": 5})
	a, _ := e.agents.Create(agent.Spec{Name: "Lu"})
	ss, _ := a.Session()
	send(t, a, ss.ID(), "hello there")
	send(t, a, ss.ID(), "/tool big_output {}")
	if _, err := e.mem.Add(a.ID(), "Lu prefers short answers"); err != nil {
		t.Fatal(err)
	}

	b, err := e.comp.Usage(a.ID(), "")
	if err != nil {
		t.Fatal(err)
	}
	if b.Window != window || b.Threshold != 0.8 || b.Model.String() != "tiny/default" || b.Session != ss.ID() {
		t.Fatalf("%+v", b)
	}
	sum := 0
	seg := map[string]int{}
	for _, s := range b.Segments {
		seg[s.Key] = s.Tokens
		if s.Key != compaction.SegFree {
			sum += s.Tokens
		}
	}
	if sum != b.Used || b.Used+b.Free != b.Window {
		t.Fatalf("segments do not add up: %+v", b)
	}
	req := llm.Request{
		System:   agentloop.System(kernel.Use[*prompt.Assembler](e.h.Root(), prompt.ServiceKey), a),
		Messages: session.DeriveMessages(ss.Events(), a.ID(), e.agents.Names()),
		Tools:    e.tools.SchemasFor(a.ID(), ss.Meta().Kind),
	}
	if b.Used != llm.EstimateRequest(req) || b.Estimated != b.Used || b.Reported != 0 || b.ToolsManaged {
		t.Fatalf("used %d != estimate %d", b.Used, llm.EstimateRequest(req))
	}
	for _, k := range []string{compaction.SegSystem, compaction.SegMemory, compaction.SegTools, compaction.SegToolIO, compaction.SegMessages} {
		if seg[k] <= 0 {
			t.Fatalf("segment %s empty: %+v", k, b.Segments)
		}
	}

	// compact now
	r, err := e.comp.CompactNow(context.Background(), a.ID(), "")
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := e.comp.Usage(a.ID(), "")
	if b2.Compactions != 1 || b2.CompactedUpto != r.UptoSeq || b2.Used >= b.Used {
		t.Fatalf("after compact now: %+v (before %d)", b2, b.Used)
	}
	if ev, _ := ss.Last(compaction.TypeStart); !strings.Contains(string(ev.Data), `"manual"`) {
		t.Fatalf("start %s", ev.Data)
	}

	// new conversation: same agent, fresh context, memory kept
	ns, err := e.comp.NewConversation(a)
	if err != nil {
		t.Fatal(err)
	}
	if ns.ID() == ss.ID() || e.comp.CurrentSession(a) != ns.ID() {
		t.Fatalf("current %s", e.comp.CurrentSession(a))
	}
	b3, _ := e.comp.Usage(a.ID(), "")
	if b3.Session != ns.ID() || b3.Segments[5].Tokens != 0 || b3.Segments[4].Tokens != 0 || b3.Segments[1].Tokens == 0 {
		t.Fatalf("new conversation usage %+v", b3)
	}
	send(t, a, ns.ID(), "fresh start")
	msgs := session.DeriveMessages(ns.Events(), a.ID(), nil)
	if len(msgs) != 2 || msgs[0].Text() != "fresh start" {
		t.Fatalf("new conversation messages %+v", msgs)
	}
}

func TestKeptToolResultsPruned(t *testing.T) {
	e := newEnv(t, t.TempDir(), map[string]any{"keepRecentTokens": 2400, "pruneToolResultChars": 400})
	a, _ := e.agents.Create(agent.Spec{Name: "Pam"})
	ss, _ := a.Session()
	send(t, a, ss.ID(), filler(1))
	for i := 0; i < 3; i++ {
		send(t, a, ss.ID(), "/tool big_output {}")
	}
	if err := e.comp.Compact(context.Background(), a, ss, llm.ModelRef{Provider: "tiny", Model: "default"}, "manual"); err != nil {
		t.Fatal(err)
	}
	events := ss.Events()
	resultSeq := map[string]int64{}
	var lastResult string
	for _, ev := range events {
		if ev.Type == session.TypeToolResult {
			var r llm.ToolResult
			ev.Decode(&r)
			resultSeq[r.CallID] = ev.Seq
			lastResult = r.CallID
		}
	}
	var pruned []session.PrunedData
	var upto int64
	keptPruned := true
	for _, ev := range events {
		if ev.Type == compaction.TypeStart {
			var st compaction.Start
			ev.Decode(&st)
			upto = st.UptoSeq
		}
		if ev.Type != session.TypeToolPruned {
			continue
		}
		var p session.PrunedData
		ev.Decode(&p)
		pruned = append(pruned, p)
		// pruned results lie in the region the compaction kept
		if resultSeq[p.CallID] <= upto {
			keptPruned = false
		}
	}
	if len(pruned) == 0 {
		t.Fatalf("no tool/pruned events (end %+v)", lastEnd(t, ss))
	}
	kp := 0
	for _, ev := range events {
		if ev.Type == compaction.TypeEnd {
			var end compaction.End
			ev.Decode(&end)
			kp += end.KeptPruned
		}
	}
	if kp != len(pruned) {
		t.Fatalf("keptPruned %d, events %d", kp, len(pruned))
	}
	for _, p := range pruned {
		if p.CallID == lastResult {
			t.Fatal("the last steps' tool results must not be pruned")
		}
		if len(p.Content) > 600 || !strings.Contains(p.Content, fmt.Sprintf("history_read session=%s from=%d", ss.ID(), resultSeq[p.CallID])) {
			t.Fatalf("pruned content %q", p.Content)
		}
		// the original stays readable
		args, _ := json.Marshal(map[string]any{"session": ss.ID(), "from": resultSeq[p.CallID], "to": resultSeq[p.CallID]})
		rd := e.tools.Run(context.Background(), e.h.Root(), tools.Call{ID: "hr", Name: "history_read", Agent: a.ID(), Args: args})
		if rd.Result.IsError || !strings.Contains(rd.Result.Content, "lorem ipsum dolor lorem ipsum dolor") || !strings.Contains(rd.Result.Content, "END") {
			t.Fatalf("history_read: %q", rd.Result.Content)
		}
	}
	if !keptPruned {
		t.Fatal("a result outside the kept region was pruned")
	}
	// the live context shrinks (measured right after the prune events)
	cutAt := 0
	for i, ev := range events {
		if ev.Type == session.TypeToolPruned {
			cutAt = i + 1
		}
	}
	prefix := events[:cutAt]
	var prefixRaw []session.Event
	for _, ev := range prefix {
		if ev.Type != session.TypeToolPruned {
			prefixRaw = append(prefixRaw, ev)
		}
	}
	withPrune := llm.EstimateRequest(llm.Request{Messages: session.DeriveMessages(prefix, a.ID(), e.agents.Names())})
	without := llm.EstimateRequest(llm.Request{Messages: session.DeriveMessages(prefixRaw, a.ID(), e.agents.Names())})
	if withPrune >= without {
		t.Fatalf("context did not shrink: %d >= %d", withPrune, without)
	}
	// a second compaction does not prune the same results again
	n := len(pruned)
	send(t, a, ss.ID(), filler(2))
	_ = e.comp.Compact(context.Background(), a, ss, llm.ModelRef{Provider: "tiny", Model: "default"}, "manual")
	seen := map[string]int{}
	for _, ev := range ss.Events() {
		if ev.Type == session.TypeToolPruned {
			var p session.PrunedData
			ev.Decode(&p)
			if seen[p.CallID]++; seen[p.CallID] > 1 {
				t.Fatalf("pruned twice: %s (had %d)", p.CallID, n)
			}
		}
	}
}

// Summaries use the "summarize" task route (a cheap model) when one is
// configured, and fall back to the agent's own model when it fails.
func TestSummaryUsesSummarizeRoute(t *testing.T) {
	for _, cheapFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("cheapFails=%v", cheapFails), func(t *testing.T) {
			var cheapSums atomic.Int32
			cheap := mock.New("cheap", 0)
			cheap.Script = func(req llm.Request) (llm.Response, error) {
				if !strings.HasPrefix(req.System, compaction.SummaryMarker) {
					t.Errorf("cheap model got a non-summary request")
				}
				cheapSums.Add(1)
				if cheapFails {
					return llm.Response{}, &llm.Error{Kind: llm.ErrUnavailable, Provider: "cheap", Message: "down"}
				}
				return text("CHEAP SUMMARY of the walrus migration."), nil
			}
			e := newEnv(t, t.TempDir(), nil, func(load func(kernel.Spec, map[string]any)) {
				load(kernel.Spec{Name: "test-cheap", Inject: []string{llm.ServiceKey}, Apply: func(c *kernel.Context) error {
					return kernel.Use[*llm.Registry](c, llm.ServiceKey).Register(c, cheap)
				}}, nil)
				load(router.Plugin, map[string]any{"routes": []any{map[string]any{"task": "summarize", "model": "cheap/default"}}})
			})
			a, _ := e.agents.Create(agent.Spec{Name: "Iris"})
			ss, _ := a.Session()
			send(t, a, ss.ID(), "first request about the walrus migration")
			for i := 0; i < 12 && len(compactions(ss.Events())) == 0; i++ {
				send(t, a, ss.ID(), filler(i))
			}
			comps := compactions(ss.Events())
			if len(comps) == 0 {
				t.Fatal("compaction never triggered")
			}
			end := lastEnd(t, ss)
			if cheapSums.Load() == 0 {
				t.Fatal("summarize route not used")
			}
			if cheapFails {
				if end.Method != "model" || end.Model != nil || !strings.HasPrefix(comps[0].Summary, "SUMMARY:") || e.sums.Load() == 0 {
					t.Fatalf("expected fallback to the agent model: end %+v summary %q", end, comps[0].Summary)
				}
				return
			}
			if end.Method != "model" || end.Model == nil || end.Model.Provider != "cheap" || !strings.HasPrefix(comps[0].Summary, "CHEAP SUMMARY") {
				t.Fatalf("end %+v summary %q", end, comps[0].Summary)
			}
			if e.sums.Load() != 0 {
				t.Fatalf("agent model also summarised (%d)", e.sums.Load())
			}
		})
	}
}
