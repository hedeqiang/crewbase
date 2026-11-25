package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/mcp"
	"github.com/hedeqiang/crewbase/kernel"
)

// A server implementing the MCP tasks extension (2026-07-28) with the SDK's
// receiving middleware and custom methods. With a client that declares the
// extension on the call, its tools run as tasks:
//
//	build    {polls, interval}  completed after polls tasks/get, "step i/n" on the way
//	broken                      failed with a JSON-RPC error
//	confirm                     input_required (a form), then accept:<name>
//	forever  {ttl}              working until cancelled
//	slow     {ms}               completed ms after it started
//
// Without the extension on the call they answer plainly ("plain:<tool>").
// The log (one line per event, in a file for the stdio child) is what the
// tests check: "call <tool> tasks=<bool>", "get <id> <unix ms>",
// "update <id> <responses>", "cancel <id>", "nocap <method>", and on HTTP
// "name <method> <Mcp-Name>".

const (
	tasksEnv   = "CREWBASE_MCP_TEST_TASKS"   // stdio child: serve the tasks server, log to this file
	noTasksEnv = "CREWBASE_MCP_TEST_NOTASKS" // ...without advertising the extension
)

type taskLog struct {
	mu    sync.Mutex
	file  string
	lines []string
}

func (l *taskLog) add(format string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := fmt.Sprintf(format, a...)
	if l.file != "" {
		f, err := os.OpenFile(l.file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintln(f, s)
			f.Close()
		}
		return
	}
	l.lines = append(l.lines, s)
}

func (l *taskLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == "" {
		return append([]string(nil), l.lines...)
	}
	b, _ := os.ReadFile(l.file)
	var out []string
	for _, s := range strings.Split(string(b), "\n") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// with returns the log lines starting with prefix.
func (l *taskLog) with(prefix string) []string {
	var out []string
	for _, s := range l.all() {
		if strings.HasPrefix(s, prefix) {
			out = append(out, s)
		}
	}
	return out
}

type wireTask struct {
	TaskID         string         `json:"taskId"`
	Status         string         `json:"status"`
	StatusMessage  string         `json:"statusMessage,omitempty"`
	CreatedAt      string         `json:"createdAt"`
	LastUpdatedAt  string         `json:"lastUpdatedAt"`
	TTLMs          *int64         `json:"ttlMs"`
	PollIntervalMs *int64         `json:"pollIntervalMs,omitempty"`
	InputRequests  map[string]any `json:"inputRequests,omitempty"`
	Result         any            `json:"result,omitempty"`
	Error          any            `json:"error,omitempty"`
}

type taskResult struct {
	sdk.ResultBase
	ResultType string `json:"resultType"`
	wireTask
}

type ackResult struct {
	sdk.ResultBase
	ResultType string `json:"resultType"`
}

type taskIDParams struct {
	sdk.ParamsBase
	TaskID string `json:"taskId"`
}

type taskUpdateParams struct {
	sdk.ParamsBase
	TaskID         string                     `json:"taskId"`
	InputResponses map[string]json.RawMessage `json:"inputResponses"`
}

type srvTask struct {
	id, tool  string
	args      struct{ Polls, Interval, TTL, Ms int }
	created   time.Time
	polls     int
	answer    json.RawMessage
	answeredN int // polls since the answer came
	cancelled bool
}

type taskServer struct {
	log   *taskLog
	mu    sync.Mutex
	n     int
	tasks map[string]*srvTask
}

func declared(meta map[string]any) bool {
	caps, _ := meta[sdk.MetaKeyClientCapabilities].(map[string]any)
	ext, _ := caps["extensions"].(map[string]any)
	_, ok := ext[mcp.TasksExtension]
	return ok
}

func (ts *taskServer) view(t *srvTask) wireTask {
	ms := func(n int) *int64 { v := int64(n); return &v }
	w := wireTask{TaskID: t.id, Status: "working", CreatedAt: t.created.UTC().Format(time.RFC3339Nano), LastUpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), PollIntervalMs: ms(20)}
	if t.args.Interval > 0 {
		w.PollIntervalMs = ms(t.args.Interval)
	}
	if t.args.TTL > 0 {
		w.TTLMs = ms(t.args.TTL)
	}
	text := func(s string) any {
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": s}}}
	}
	if t.cancelled {
		w.Status, w.StatusMessage = "cancelled", "stopped on request"
		return w
	}
	switch t.tool {
	case "build":
		n := max(t.args.Polls, 1)
		if t.polls >= n {
			w.Status, w.Result = "completed", text(fmt.Sprintf("built after %d polls", t.polls))
		} else {
			w.StatusMessage = fmt.Sprintf("step %d/%d", t.polls+1, n)
		}
	case "broken":
		if t.polls > 0 {
			w.Status, w.StatusMessage = "failed", "Tool execution failed: API rate limit exceeded"
			w.Error = map[string]any{"code": -32603, "message": "API rate limit exceeded"}
		}
	case "confirm":
		switch {
		case t.answer == nil || t.answeredN < 1:
			// the answer shows one poll late (eventually consistent): the
			// same key is still outstanding then
			w.Status, w.StatusMessage = "input_required", "waiting for a name"
			w.InputRequests = map[string]any{"ok": map[string]any{"method": "elicitation/create", "params": map[string]any{
				"mode": "form", "message": "Name the release.",
				"requestedSchema": map[string]any{"type": "object", "required": []string{"name"}, "properties": map[string]any{"name": map[string]any{"type": "string"}}},
			}}}
		default:
			var r struct {
				Action  string         `json:"action"`
				Content map[string]any `json:"content"`
			}
			_ = json.Unmarshal(t.answer, &r)
			out := r.Action
			if r.Action == "accept" {
				out += ":" + fmt.Sprint(r.Content["name"])
			}
			w.Status, w.Result = "completed", text(out)
		}
	case "forever":
		w.StatusMessage = "still going"
	case "slow":
		if time.Since(t.created) >= time.Duration(t.args.Ms)*time.Millisecond {
			w.Status, w.Result = "completed", text("slow done")
		}
	}
	return w
}

func newTaskServer(log *taskLog, advertise bool) *sdk.Server {
	ts := &taskServer{log: log, tasks: map[string]*srvTask{}}
	caps := &sdk.ServerCapabilities{Tools: &sdk.ToolCapabilities{}}
	if advertise {
		caps.AddExtension(mcp.TasksExtension, nil)
	}
	s := sdk.NewServer(&sdk.Implementation{Name: "crewbase-tasks", Version: "1.0.0"}, &sdk.ServerOptions{Capabilities: caps})
	for _, name := range []string{"build", "broken", "confirm", "forever", "slow"} {
		s.AddTool(&sdk.Tool{Name: name, InputSchema: map[string]any{"type": "object"}, Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}},
			func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "plain:" + name}}}, nil
			})
	}
	s.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			r, ok := req.(*sdk.CallToolRequest)
			if method != "tools/call" || !ok {
				return next(ctx, method, req)
			}
			has := false
			if c := r.ClientCapabilities(); c != nil {
				_, has = c.Extensions[mcp.TasksExtension]
			}
			log.add("call %s tasks=%v", r.Params.Name, has)
			if !has || !advertise {
				return next(ctx, method, req)
			}
			ts.mu.Lock()
			ts.n++
			t := &srvTask{id: fmt.Sprintf("task-%d-%d", os.Getpid(), ts.n), tool: r.Params.Name, created: time.Now()}
			_ = json.Unmarshal(r.Params.Arguments, &t.args)
			ts.tasks[t.id] = t
			// CreateTaskResult is a plain Task: the seed state, no payload
			w := ts.view(t)
			if w.Status != "working" {
				w.Status, w.StatusMessage = "working", ""
			}
			w.InputRequests, w.Result, w.Error = nil, nil, nil
			ts.mu.Unlock()
			return &taskResult{ResultType: "task", wireTask: w}, nil
		}
	})
	lookup := func(method string, p sdk.Params, id string) (*srvTask, error) {
		if !declared(p.GetMeta()) {
			log.add("nocap %s", method)
			return nil, &jsonrpc.Error{Code: -32021, Message: "Missing required client capability"}
		}
		t := ts.tasks[id]
		if t == nil {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "Failed to retrieve task: Task not found"}
		}
		return t, nil
	}
	_ = sdk.AddReceivingCustomMethod(s, "tasks/get", func(_ context.Context, _ *sdk.ServerSession, p *taskIDParams) (*taskResult, error) {
		ts.mu.Lock()
		defer ts.mu.Unlock()
		t, err := lookup("tasks/get", p, p.TaskID)
		if err != nil {
			return nil, err
		}
		log.add("get %s %d", t.id, time.Now().UnixMilli())
		t.polls++
		if t.answer != nil {
			t.answeredN++
		}
		return &taskResult{ResultType: "complete", wireTask: ts.view(t)}, nil
	})
	_ = sdk.AddReceivingCustomMethod(s, "tasks/update", func(_ context.Context, _ *sdk.ServerSession, p *taskUpdateParams) (*ackResult, error) {
		ts.mu.Lock()
		defer ts.mu.Unlock()
		t, err := lookup("tasks/update", p, p.TaskID)
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(p.InputResponses)
		log.add("update %s %s", t.id, b)
		if r, ok := p.InputResponses["ok"]; ok && t.answer == nil {
			t.answer = r
		}
		return &ackResult{ResultType: "complete"}, nil
	})
	_ = sdk.AddReceivingCustomMethod(s, "tasks/cancel", func(_ context.Context, _ *sdk.ServerSession, p *taskIDParams) (*ackResult, error) {
		ts.mu.Lock()
		defer ts.mu.Unlock()
		t, err := lookup("tasks/cancel", p, p.TaskID)
		if err != nil {
			return nil, err
		}
		log.add("cancel %s", t.id)
		t.cancelled = true
		return &ackResult{ResultType: "complete"}, nil
	})
	return s
}

// runTaskStdioServer serves the tasks server over stdio (the test binary
// as a child process).
func runTaskStdioServer() {
	s := newTaskServer(&taskLog{file: os.Getenv(tasksEnv)}, os.Getenv(noTasksEnv) == "")
	if err := s.Run(context.Background(), &sdk.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, "tasks server:", err)
	}
}

type taskEnv struct {
	*env
	log   *taskLog
	inbox *approval.Inbox
	ss    *session.Session
}

// newTaskEnv connects server "jobs" (the tasks server) over transport.
func newTaskEnv(t *testing.T, transport string, cfg map[string]any, advertise bool) *taskEnv {
	t.Helper()
	log := &taskLog{}
	var srv mcp.Server
	switch transport {
	case "stdio":
		log.file = filepath.Join(t.TempDir(), "tasks.log")
		env := map[string]string{tasksEnv: log.file}
		if !advertise {
			env[noTasksEnv] = "1"
		}
		srv = stdioServer("jobs", env)
	case "http":
		// stateless: the SDK serves 2026-07-28 over HTTP only so
		s := newTaskServer(log, advertise)
		h := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return s }, &sdk.StreamableHTTPOptions{Stateless: true})
		hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if m := r.Header.Get("Mcp-Method"); strings.HasPrefix(m, "tasks/") {
				log.add("name %s %s", m, r.Header.Get("Mcp-Name"))
			}
			h.ServeHTTP(w, r)
		}))
		t.Cleanup(hs.Close) // before newEnv: closed after the host stopped
		srv = mcp.Server{Name: "jobs", Transport: mcp.TransportHTTP, URL: hs.URL, Enabled: true}
	}
	e := newEnv(t, cfg, load{session.Plugin, nil}, load{approval.Plugin, nil}, load{approval.DesktopPlugin, nil})
	if _, err := e.svc.Add(srv); err != nil {
		t.Fatal(err)
	}
	if st := e.waitState(t, "jobs", mcp.StateReady); st.Tasks != advertise {
		t.Fatalf("status tasks %v, want %v", st.Tasks, advertise)
	}
	r := e.h.Root()
	ss, err := kernel.Use[*session.Store](r, session.ServiceKey).Create(session.Meta{Kind: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	return &taskEnv{env: e, log: log, inbox: kernel.Use[*approval.Inbox](r, approval.InboxKey), ss: ss}
}

func (e *taskEnv) start(ctx context.Context, tool, id, args string) <-chan tools.Execution {
	ch := make(chan tools.Execution, 1)
	go func() {
		ch <- e.tools.Run(ctx, e.h.Root(), tools.Call{ID: id, Name: "jobs__" + tool, Args: json.RawMessage(args), Session: e.ss})
	}()
	return ch
}

func (e *taskEnv) run(t *testing.T, tool, args string) tools.Result {
	t.Helper()
	select {
	case x := <-e.start(context.Background(), tool, "call_"+tool, args):
		return x.Result
	case <-time.After(20 * time.Second):
		t.Fatal("the call did not end")
	}
	return tools.Result{}
}

func (e *taskEnv) progress(callID string) []string {
	var out []string
	for _, ev := range e.ss.Events() {
		var d tools.ProgressData
		if ev.Type == session.TypeToolProgress && ev.Decode(&d) == nil && d.CallID == callID {
			out = append(out, d.Text)
		}
	}
	return out
}

func eachTransport(t *testing.T, f func(t *testing.T, transport string)) {
	for _, tr := range []string{"stdio", "http"} {
		t.Run(tr, func(t *testing.T) { f(t, tr) })
	}
}

// A task polled to completion: the call returns the task's result, the
// step shows the status messages, the capability went on the call.
func TestTaskCompletes(t *testing.T) {
	eachTransport(t, func(t *testing.T, tr string) {
		e := newTaskEnv(t, tr, nil, true)
		res := e.run(t, "build", `{"polls":3}`)
		if res.IsError || res.Content != "built after 3 polls" {
			t.Fatalf("%+v", res)
		}
		if got := e.log.with("call build"); len(got) != 1 || got[0] != "call build tasks=true" {
			t.Fatalf("calls %v", got)
		}
		if n := len(e.log.with("get ")); n != 3 {
			t.Fatalf("%d polls", n)
		}
		if nc := e.log.with("nocap"); len(nc) != 0 {
			t.Fatalf("tasks/* without the capability: %v", nc)
		}
		if p := e.progress("call_build"); strings.Join(p, "|") != "step 1/3|step 2/3|step 3/3" {
			t.Fatalf("progress %q", p)
		}
		if tr == "http" { // Mcp-Name carries the task id
			names := e.log.with("name tasks/get ")
			if len(names) != 3 || !strings.HasPrefix(names[0], "name tasks/get task-") {
				t.Fatalf("names %v", names)
			}
		}
	})
}

func TestTaskFails(t *testing.T) {
	eachTransport(t, func(t *testing.T, tr string) {
		e := newTaskEnv(t, tr, nil, true)
		res := e.run(t, "broken", `{}`)
		if !res.IsError || !strings.Contains(res.Content, "task failed: API rate limit exceeded (code -32603)") {
			t.Fatalf("%+v", res)
		}
	})
}

// input_required: the elicitation goes to the person once (the same key
// polled again is not asked again), the answer goes back with tasks/update.
func TestTaskInputRequired(t *testing.T) {
	eachTransport(t, func(t *testing.T, tr string) {
		e := newTaskEnv(t, tr, nil, true)
		ch := e.start(context.Background(), "confirm", "call_conf", `{}`)
		var req approval.Request
		waitFor(t, "the question", func() bool {
			p := e.inbox.Pending()
			if len(p) > 0 {
				req = p[0]
			}
			return len(p) > 0
		})
		if req.Elicit == nil || req.Elicit.Server != "jobs" || req.Parent != "call_conf" || !strings.Contains(req.Elicit.Message, "Name the release") {
			t.Fatalf("%+v %+v", req, req.Elicit)
		}
		e.inbox.Decide(req.CallID, approval.Decision{Outcome: approval.OutcomeAllowedOnce, Values: map[string]any{"name": "v2"}})
		var x tools.Execution
		select {
		case x = <-ch:
		case <-time.After(15 * time.Second):
			t.Fatalf("the call did not end: %v", e.log.all())
		}
		if x.Result.Content != "accept:v2" {
			t.Fatalf("%+v", x.Result)
		}
		asked := 0
		for _, ev := range e.ss.Events() {
			if ev.Type == session.TypeApprovalAsked {
				asked++
			}
		}
		if up := e.log.with("update "); asked != 1 || len(up) != 1 || !strings.Contains(up[0], `"ok":{"action":"accept","content":{"name":"v2"}}`) {
			t.Fatalf("asked %d, updates %v", asked, up)
		}
	})
}

// A stopped turn sends tasks/cancel.
func TestTaskCancelledByCaller(t *testing.T) {
	eachTransport(t, func(t *testing.T, tr string) {
		e := newTaskEnv(t, tr, nil, true)
		ctx, cancel := context.WithCancel(context.Background())
		ch := e.start(ctx, "forever", "call_fv", `{}`)
		waitFor(t, "polling", func() bool { return len(e.log.with("get ")) >= 2 })
		cancel()
		select {
		case x := <-ch:
			if !x.Result.IsError || x.Result.Content != "cancelled" {
				t.Fatalf("%+v", x.Result)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the call did not end")
		}
		waitFor(t, "tasks/cancel", func() bool { return len(e.log.with("cancel task-")) == 1 })
	})
}

// A server without the extension: no capability on the call, plain results.
func TestTaskServerWithoutExtension(t *testing.T) {
	eachTransport(t, func(t *testing.T, tr string) {
		e := newTaskEnv(t, tr, nil, false)
		res := e.run(t, "build", `{"polls":3}`)
		if res.IsError || res.Content != "plain:build" {
			t.Fatalf("%+v", res)
		}
		if got := e.log.with("call build"); len(got) != 1 || got[0] != "call build tasks=false" {
			t.Fatalf("calls %v", got)
		}
		if n := len(e.log.with("get ")); n != 0 {
			t.Fatalf("%d polls", n)
		}
	})
}

// pollIntervalMs is honoured between polls.
func TestTaskPollInterval(t *testing.T) {
	e := newTaskEnv(t, "http", nil, true)
	start := time.Now()
	if res := e.run(t, "build", `{"polls":4,"interval":150}`); res.Content != "built after 4 polls" {
		t.Fatalf("%+v", res)
	}
	var at []int64
	for _, l := range e.log.with("get ") {
		f := strings.Fields(l)
		n, _ := strconv.ParseInt(f[2], 10, 64)
		at = append(at, n)
	}
	if len(at) != 4 || at[0]-start.UnixMilli() < 140 {
		t.Fatalf("polls at %v (start %d)", at, start.UnixMilli())
	}
	for i := 1; i < len(at); i++ {
		if d := at[i] - at[i-1]; d < 140 {
			t.Fatalf("polls %dms apart: %v", d, at)
		}
	}
}

// A task may outlive callTimeout: its time is its ttlMs, capped by
// taskTimeout; running out of it cancels the task.
func TestTaskOutlivesCallTimeout(t *testing.T) {
	eachTransport(t, func(t *testing.T, tr string) {
		e := newTaskEnv(t, tr, map[string]any{"callTimeout": "200ms", "taskTimeout": "5s"}, true)
		if res := e.run(t, "slow", `{"ms":900}`); res.IsError || res.Content != "slow done" {
			t.Fatalf("%+v", res)
		}
		// its ttlMs is shorter than taskTimeout
		start := time.Now()
		res := e.run(t, "forever", `{"ttl":400}`)
		if !res.IsError || !strings.Contains(res.Content, "timed out") || time.Since(start) > 4*time.Second {
			t.Fatalf("%+v after %v", res, time.Since(start))
		}
		waitFor(t, "tasks/cancel", func() bool { return len(e.log.with("cancel task-")) == 1 })
	})
	// taskTimeout caps a task without ttlMs
	e := newTaskEnv(t, "http", map[string]any{"callTimeout": "100ms", "taskTimeout": "500ms"}, true)
	start := time.Now()
	res := e.run(t, "forever", `{}`)
	if !res.IsError || !strings.Contains(res.Content, "timed out") || time.Since(start) < 450*time.Millisecond {
		t.Fatalf("%+v after %v", res, time.Since(start))
	}
}
