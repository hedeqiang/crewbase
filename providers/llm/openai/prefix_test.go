package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hedeqiang/crewbase/core/llm"
)

// memStore is an llm.Store in memory (the agent loop keeps it in the
// session's side file).
type memStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (s *memStore) Load(k string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[k]
	return v, ok
}

func (s *memStore) Save(k string, v []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string][]byte{}
	}
	s.m[k] = append([]byte(nil), v...)
	return nil
}

// turnGateway answers every Responses request with a reasoning item and a
// line of text (numbered per request); transformations, when set, go in
// the completed response.
func turnGateway(transformations string) *gateway {
	n := 0
	var mu sync.Mutex
	return &gateway{responses: func(w http.ResponseWriter, _ map[string]any) {
		mu.Lock()
		n++
		i := n
		mu.Unlock()
		out := fmt.Sprintf(`{"type":"reasoning","id":"rs_%d","summary":[{"type":"summary_text","text":"think %d"}],"encrypted_content":"ENC_%d"},`+
			`{"type":"message","id":"msg_%d","role":"assistant","status":"completed","content":[{"type":"output_text","text":"answer %d","annotations":[]}]}`, i, i, i, i, i)
		tr := ""
		if transformations != "" {
			tr = `,"input_transformations":` + transformations
		}
		sse(w, `{"type":"response.completed","sequence_number":1,"response":{"id":"resp_`+fmt.Sprint(i)+`","status":"completed","output":[`+out+`],"usage":{"input_tokens":10,"output_tokens":2}`+tr+`}}`)
	}}
}

func gatewayAdapter(t *testing.T, url string) *Adapter {
	return newAdapter(t, url, func(pc *ProviderConfig) {
		pc.ID, pc.Kind, pc.EffortStyle, pc.Responses = "magpie", "gateway", "auto", ResponsesAuto
		pc.Models = []ModelSpec{
			{ID: "openai/gpt-6.1-sol", Capabilities: []string{"tools", "reasoning", "vision"}},
			{ID: "openai/gpt-6.2-sol", Capabilities: []string{"tools", "reasoning", "vision"}},
			{ID: "openai/gpt-5.5", Capabilities: []string{"tools", "reasoning"}},
			{ID: "claude/claude-opus-5-5", Capabilities: []string{"tools", "reasoning"}},
		}
	})
}

var (
	readTool  = llm.ToolSchema{Name: "read_file", Description: "Read a file", Parameters: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}
	writeTool = llm.ToolSchema{Name: "write_file", Description: "Write a file", Parameters: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}
)

// convo is one conversation through an adapter: each say sends the
// history plus a user message and keeps the reply.
type convo struct {
	t     *testing.T
	a     *Adapter
	g     *gateway
	store *memStore
	req   llm.Request
}

func (c *convo) say(text string) map[string]any {
	c.t.Helper()
	c.req.Messages = append(c.req.Messages, llm.TextMessage(llm.RoleUser, text))
	c.req.Store = c.store
	resp, err := c.a.Stream(context.Background(), c.req, func(llm.Chunk) error { return nil })
	if err != nil {
		c.t.Fatal(err)
	}
	c.req.Messages = append(c.req.Messages, resp.Message)
	_, b := c.g.last()
	return b
}

func js(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// samePrefix fails unless next's input starts with every item of prev's
// input (byte for byte as JSON), and instructions and tools are the same.
func samePrefix(t *testing.T, prev, next map[string]any) {
	t.Helper()
	if js(t, prev["instructions"]) != js(t, next["instructions"]) {
		t.Fatalf("instructions changed:\n%v\n%v", prev["instructions"], next["instructions"])
	}
	if js(t, prev["tools"]) != js(t, next["tools"]) {
		t.Fatalf("tools changed:\n%s\n%s", js(t, prev["tools"]), js(t, next["tools"]))
	}
	if js(t, prev["reasoning"]) != js(t, next["reasoning"]) {
		t.Fatalf("reasoning changed: %v %v", prev["reasoning"], next["reasoning"])
	}
	pi, ni := prev["input"].([]any), next["input"].([]any)
	if len(ni) < len(pi) {
		t.Fatalf("input shrank: %d → %d", len(pi), len(ni))
	}
	for i := range pi {
		if js(t, pi[i]) != js(t, ni[i]) {
			t.Fatalf("input item %d changed:\n%s\n%s", i, js(t, pi[i]), js(t, ni[i]))
		}
	}
}

func items(t *testing.T, b map[string]any, typ string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, it := range b["input"].([]any) {
		m := it.(map[string]any)
		if m["type"] == typ || typ == "developer" && m["role"] == "developer" {
			out = append(out, m)
		}
	}
	return out
}

func newConvo(t *testing.T, model string, g *gateway) *convo {
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	return &convo{t: t, a: gatewayAdapter(t, srv.URL), g: g, store: &memStore{},
		req: llm.Request{Model: model, System: "You are Iris.\n\nMemory: none.", Effort: "medium", Tools: []llm.ToolSchema{readTool},
			SystemParts: []llm.SystemPart{{Name: "agent-identity", Text: "You are Iris."}, {Name: "agent-memory", Text: "Memory: none."}}}}
}

// Nothing changed: every request starts with the one before, byte for
// byte, and the reasoning items are replayed where they came.
func TestPrefixStable(t *testing.T) {
	c := newConvo(t, "openai/gpt-6.1-sol", turnGateway(""))
	b1 := c.say("hi")
	b2 := c.say("more")
	b3 := c.say("and more")
	samePrefix(t, b1, b2)
	samePrefix(t, b2, b3)
	if b1["instructions"] != "You are Iris.\n\nMemory: none." {
		t.Fatalf("instructions %v", b1["instructions"])
	}
	in := b3["input"].([]any)
	want := []string{`"role":"user"`, `"id":"rs_1"`, `"answer 1"`, `"role":"user"`, `"id":"rs_2"`, `"answer 2"`, `"role":"user"`}
	if len(in) != len(want) {
		t.Fatalf("input %s", js(t, in))
	}
	for i, w := range want {
		if !strings.Contains(js(t, in[i]), w) {
			t.Fatalf("item %d %s, want %s", i, js(t, in[i]), w)
		}
	}
	if len(items(t, b3, "additional_tools"))+len(items(t, b3, "configuration_update"))+len(items(t, b3, "developer")) != 0 {
		t.Fatalf("appended items with nothing changed: %s", js(t, in))
	}
	// after a restart (a new adapter, the same session store) it is the same
	again := gatewayAdapter(t, c.a.baseURL)
	c.a = again
	samePrefix(t, b3, c.say("after restart"))
}

// A tool that appears mid-conversation is an additional_tools item where
// it appeared; the tools list stays as it was. One that goes away stays
// declared and is left out of allowed_tools.
func TestPrefixToolAdded(t *testing.T) {
	c := newConvo(t, "openai/gpt-6.1-sol", turnGateway(""))
	b1 := c.say("hi")
	c.req.Tools = []llm.ToolSchema{readTool, writeTool}
	b2 := c.say("now write")
	samePrefix(t, b1, b2)
	at := items(t, b2, "additional_tools")
	if len(at) != 1 || at[0]["role"] != "developer" {
		t.Fatalf("additional_tools %s", js(t, b2["input"]))
	}
	if tl := at[0]["tools"].([]any); len(tl) != 1 || tl[0].(map[string]any)["name"] != "write_file" {
		t.Fatalf("added %v", tl)
	}
	// before the user message that asked for it
	in := b2["input"].([]any)
	if js(t, in[len(in)-2]) != js(t, at[0]) || in[len(in)-1].(map[string]any)["role"] != "user" {
		t.Fatalf("additional_tools not before the new user message: %s", js(t, in))
	}
	if b2["tool_choice"] != nil {
		t.Fatalf("tool_choice %v", b2["tool_choice"])
	}
	b3 := c.say("again")
	samePrefix(t, b2, b3)
	if len(items(t, b3, "additional_tools")) != 1 {
		t.Fatal("additional_tools not replayed once")
	}
	// read_file goes away
	c.req.Tools = []llm.ToolSchema{writeTool}
	b4 := c.say("only write now")
	samePrefix(t, b3, b4)
	tc, _ := b4["tool_choice"].(map[string]any)
	if tc["type"] != "allowed_tools" || tc["mode"] != "auto" || js(t, tc["tools"]) != `[{"name":"write_file","type":"function"}]` {
		t.Fatalf("tool_choice %v", b4["tool_choice"])
	}
}

// An effort change is a configuration_update item; reasoning.effort stays.
func TestPrefixEffortChange(t *testing.T) {
	c := newConvo(t, "openai/gpt-6.1-sol", turnGateway(""))
	b1 := c.say("hi")
	c.req.Effort = "high"
	b2 := c.say("think harder")
	samePrefix(t, b1, b2)
	if r := b2["reasoning"].(map[string]any); r["effort"] != "medium" {
		t.Fatalf("reasoning %v", r)
	}
	cu := items(t, b2, "configuration_update")
	if len(cu) != 1 || js(t, cu[0]) != `{"reasoning":{"effort":"high"},"type":"configuration_update"}` {
		t.Fatalf("configuration_update %s", js(t, b2["input"]))
	}
	// the guide: before the next user message
	in := b2["input"].([]any)
	if js(t, in[len(in)-2]) != js(t, cu[0]) || in[len(in)-1].(map[string]any)["role"] != "user" {
		t.Fatalf("configuration_update not before the user message: %s", js(t, in))
	}
	b3 := c.say("still high")
	samePrefix(t, b2, b3)
	if len(items(t, b3, "configuration_update")) != 1 {
		t.Fatal("update not replayed once")
	}
	c.req.Effort = "medium"
	b4 := c.say("back")
	if cu := items(t, b4, "configuration_update"); len(cu) != 2 || cu[1]["reasoning"].(map[string]any)["effort"] != "medium" {
		t.Fatalf("back to medium: %v", cu)
	}
}

// Before GPT-6.2 changed instructions are an appended developer message;
// from GPT-6.2 the base is the first developer message and the change is
// turn-scoped top-level instructions.
func TestPrefixInstructions(t *testing.T) {
	c := newConvo(t, "openai/gpt-6.1-sol", turnGateway(""))
	b1 := c.say("hi")
	c.req.SystemParts[1].Text = "Memory: likes tea."
	c.req.System = "You are Iris.\n\nMemory: likes tea."
	b2 := c.say("hello")
	samePrefix(t, b1, b2)
	dev := items(t, b2, "developer")
	if len(dev) != 1 || !strings.Contains(dev[0]["content"].(string), `<part name="agent-memory">`+"\nMemory: likes tea.") ||
		strings.Contains(dev[0]["content"].(string), "You are Iris.") {
		t.Fatalf("developer update %s", js(t, b2["input"]))
	}
	b3 := c.say("again")
	samePrefix(t, b2, b3)
	if len(items(t, b3, "developer")) != 1 {
		t.Fatal("update not replayed once")
	}

	c = newConvo(t, "openai/gpt-6.2-sol", turnGateway(""))
	b1 = c.say("hi")
	in := b1["input"].([]any)
	if first := in[0].(map[string]any); first["role"] != "developer" || first["content"] != "You are Iris.\n\nMemory: none." || b1["instructions"] != nil {
		t.Fatalf("6.2 base %s instructions %v", js(t, in[0]), b1["instructions"])
	}
	c.req.SystemParts[1].Text = "Memory: likes tea."
	b2 = c.say("hello")
	if s, _ := b2["instructions"].(string); !strings.Contains(s, "Memory: likes tea.") || strings.Contains(s, "You are Iris.") {
		t.Fatalf("turn-scoped instructions %v", b2["instructions"])
	}
	delete(b1, "instructions")
	b2i := b2["instructions"]
	delete(b2, "instructions")
	samePrefix(t, b1, b2)
	if len(items(t, b2, "developer")) != 1 {
		t.Fatal("6.2 appended a developer message")
	}
	b3 = c.say("again")
	if b3["instructions"] != b2i {
		t.Fatal("turn-scoped instructions not sent again")
	}
}

// A tool whose definition changed starts a new base (and the old base's
// appended items are left out).
func TestPrefixRebase(t *testing.T) {
	c := newConvo(t, "openai/gpt-6.1-sol", turnGateway(""))
	c.say("hi")
	c.req.Tools = []llm.ToolSchema{readTool, writeTool}
	c.say("add")
	changed := readTool
	changed.Description = "Read a file (v2)"
	c.req.Tools = []llm.ToolSchema{changed, writeTool}
	b := c.say("changed")
	if len(items(t, b, "additional_tools")) != 0 || !strings.Contains(js(t, b["tools"]), "(v2)") || !strings.Contains(js(t, b["tools"]), "write_file") {
		t.Fatalf("rebase %s / %s", js(t, b["tools"]), js(t, b["input"]))
	}
}

// Only OpenAI models of GPT-6 and later on Responses, on OpenAI's API or a
// gateway; never without a store.
func TestKeepsPrefixScope(t *testing.T) {
	mag := gatewayAdapter(t, "http://x")
	official := newAdapter(t, "http://x", func(pc *ProviderConfig) { pc.Preset = "openai" })
	other := newAdapter(t, "http://x", func(pc *ProviderConfig) { pc.Responses = ResponsesAlways })
	never := newAdapter(t, "http://x", func(pc *ProviderConfig) { pc.Preset = "openai"; pc.Responses = ResponsesNever })
	for _, c := range []struct {
		a     *Adapter
		model string
		want  bool
	}{
		{mag, "openai/gpt-6.1-sol", true}, {mag, "codex/gpt-6.2-astra", true}, {mag, "alchemypay-openai/gpt-6.2-sol", true},
		{mag, "openai/gpt-5.5", false}, {mag, "claude/claude-opus-5-5", false}, {mag, "deepseek/deepseek-v4-pro", false},
		{official, "gpt-6.2-sol", true}, {official, "gpt-5.6-sol", false}, {official, "gpt-4.1", false}, {official, "o3", false},
		{other, "gpt-6.2-sol", false}, {never, "gpt-6.2-sol", false},
	} {
		if got := c.a.KeepsPrefix(c.model); got != c.want {
			t.Errorf("%s %s: %v", c.a.id, c.model, got)
		}
	}
	// no store (e.g. a compaction summary request): as before
	g := turnGateway("")
	srv := httptest.NewServer(g)
	defer srv.Close()
	a := gatewayAdapter(t, srv.URL)
	if _, err := a.Stream(context.Background(), llm.Request{Model: "openai/gpt-6.2-sol", System: "S", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "x")}}, func(llm.Chunk) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, b := g.last(); b["instructions"] != "S" {
		t.Fatalf("storeless request %v", b)
	}
}

// Reasoning another model wrote is still left out; the prelude parts of
// another provider are not replayed.
func TestPrefixForeignItems(t *testing.T) {
	c := newConvo(t, "openai/gpt-6.1-sol", turnGateway(""))
	c.say("hi")
	foreign := llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{
		{Type: "thinking", Text: "x", Meta: map[string]string{MetaReasoning: `{"type":"reasoning","id":"rs_x","encrypted_content":"E"}`, MetaReasoningFrom: "magpie/codex/gpt-6.1-sol"}},
		{Type: llm.PartProviderBlock, Meta: map[string]string{MetaPrelude: `{"base":"zzz","from":"magpie","items":[{"type":"configuration_update","reasoning":{"effort":"low"}}]}`}},
		{Type: "text", Text: "from codex"}}}
	c.req.Messages = append(c.req.Messages, llm.TextMessage(llm.RoleUser, "q"), foreign)
	b := c.say("next")
	s := js(t, b["input"])
	if strings.Contains(s, "rs_x") || strings.Contains(s, `"low"`) || !strings.Contains(s, "from codex") {
		t.Fatalf("foreign items %s", s)
	}
}

// input_transformations are counted per reason and noted in the store.
func TestPrefixTransformations(t *testing.T) {
	c := newConvo(t, "openai/gpt-6.1-sol", turnGateway(`[{"type":"reasoning","id":"rs_old","action":"removed","reason":"prefix_mismatch"}]`))
	c.say("hi")
	c.say("again")
	if n := c.a.Transformations()["prefix_mismatch"]; n != 2 {
		t.Fatalf("counted %d", n)
	}
	b, _ := c.store.Load(c.a.dropKey())
	var note dropNote
	if json.Unmarshal(b, &note) != nil || note.Counts["prefix_mismatch"] != 2 || !note.Logged || note.Last != "rs_old" {
		t.Fatalf("note %s", b)
	}
}

// A provider that refuses an append-only item gets the request as before,
// and that model is not built append-only again.
func TestPrefixRefused(t *testing.T) {
	n := 0
	g := &gateway{}
	g.responses = func(w http.ResponseWriter, body map[string]any) {
		n++
		if strings.Contains(js(t, body["input"]), "configuration_update") {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"message":"Invalid value: 'configuration_update'.","type":"invalid_request_error"}}`))
			return
		}
		turnGateway("").responses(w, body)
	}
	c := newConvo(t, "openai/gpt-6.1-sol", g)
	c.say("hi")
	c.req.Effort = "high"
	b := c.say("harder")
	if r := b["reasoning"].(map[string]any); r["effort"] != "high" || c.a.KeepsPrefix("openai/gpt-6.1-sol") {
		t.Fatalf("fallback %v", b)
	}
}

// The computer tool, once declared, stays: turned off it is left out of
// allowed_tools, and the recorded computer calls keep their form.
func TestPrefixComputer(t *testing.T) {
	g := &gateway{}
	srv := httptest.NewServer(g)
	defer srv.Close()
	a := newAdapter(t, srv.URL, func(pc *ProviderConfig) { pc.Preset = "openai"; pc.EffortStyle = "openai" })
	store := &memStore{}
	apps := llm.ToolSchema{Name: "computer_apps", Description: "list", Parameters: map[string]any{"type": "object"}}
	req := llm.Request{Model: "gpt-6.1-sol", System: "sys", Effort: "low", NativeTools: []string{llm.NativeComputer}, Store: store,
		Tools: []llm.ToolSchema{apps}, Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "search penguin")}}
	g.responses = func(w http.ResponseWriter, _ map[string]any) { sse(w, computerEvents...) }
	resp, err := a.Stream(context.Background(), req, func(llm.Chunk) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	_, b1 := g.last()
	req.Messages = append(req.Messages, resp.Message, llm.Message{Role: llm.RoleTool, Parts: []llm.Part{{Type: "tool_result",
		ToolResult: &llm.ToolResult{CallID: "call_002", Name: llm.NativeComputer, Content: "Did click.",
			Attachments: []llm.Attachment{{Path: "generated/s.png", Mime: "image/png", Data: []byte{0x89, 'P', 'N', 'G'}}}}}}})
	// the person turns computer use off
	req.NativeTools = nil
	g.responses = turnGateway("").responses
	if _, err := a.Stream(context.Background(), req, func(llm.Chunk) error { return nil }); err != nil {
		t.Fatal(err)
	}
	_, b2 := g.last()
	samePrefix(t, b1, b2)
	s := js(t, b2["input"])
	if !strings.Contains(s, `"type":"computer_call"`) || !strings.Contains(s, `"type":"computer_call_output"`) {
		t.Fatalf("computer calls rewritten: %s", s)
	}
	if js(t, b2["tool_choice"]) != `{"mode":"auto","tools":[{"name":"computer_apps","type":"function"}],"type":"allowed_tools"}` {
		t.Fatalf("tool_choice %s", js(t, b2["tool_choice"]))
	}
}

// With hosted tool search the namespaces and tool_search stay in place;
// the search items are replayed.
func TestPrefixToolSearch(t *testing.T) {
	g := &gateway{responses: func(w http.ResponseWriter, _ map[string]any) {
		sse(w, completed(searchOutput, `{"input_tokens":500,"output_tokens":20}`))
	}}
	srv := httptest.NewServer(g)
	defer srv.Close()
	a := newAdapter(t, srv.URL, func(pc *ProviderConfig) { pc.Preset = "openai" })
	tools := []llm.ToolSchema{{Name: "read_file", Description: "Read a file"},
		{Name: "github__create_issue", Description: "Open an issue", Deferred: true, Group: "github"}}
	req := llm.Request{Model: "gpt-6.2-sol", Effort: "high", Tools: tools, Store: &memStore{}, Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "file a bug")}}
	resp, err := a.Stream(context.Background(), req, func(llm.Chunk) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	_, b1 := g.last()
	req.Messages = append(req.Messages, resp.Message, llm.Message{Role: llm.RoleTool, Parts: []llm.Part{{Type: "tool_result",
		ToolResult: &llm.ToolResult{CallID: "call_s1", Content: "#1"}}}})
	// a tool joins (not deferred): additional_tools beside the namespaces
	req.Tools = append(req.Tools, writeTool)
	if _, err := a.Stream(context.Background(), req, func(llm.Chunk) error { return nil }); err != nil {
		t.Fatal(err)
	}
	_, b2 := g.last()
	samePrefix(t, b1, b2)
	s := js(t, b2["input"])
	for _, w := range []string{`"type":"tool_search_call"`, `"type":"tool_search_output"`, `"namespace":"crew_github"`, `"type":"additional_tools"`} {
		if !strings.Contains(s, w) {
			t.Fatalf("missing %s in %s", w, s)
		}
	}
	if !strings.Contains(js(t, b2["tools"]), `"type":"tool_search"`) {
		t.Fatalf("tools %s", js(t, b2["tools"]))
	}
}
