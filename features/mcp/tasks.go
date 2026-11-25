package mcp

// MCP Tasks extension (io.modelcontextprotocol/tasks, protocol revision
// 2026-07-28) — the client side.
//
// A server that advertises the extension (server/discover capabilities) may
// answer a tools/call that declared it with a CreateTaskResult
// (resultType "task") instead of the tool's result. The call then polls
// tasks/get, honouring pollIntervalMs, until the task is completed (its
// result is what tools/call would have returned), failed (a JSON-RPC error)
// or cancelled. A task waiting on input_required carries inputRequests:
// each elicitation goes to the person through the approval cards
// (elicit.go) once per key, and the answers go back with tasks/update. A
// stopped turn sends tasks/cancel. While a task runs, the call's timeout is
// the task's ttlMs, capped by the taskTimeout setting (2h), instead of
// callTimeout; the step shows the task's statusMessage (tools.Progress).
//
// TEMPORARY STAND-IN. The official Go SDK (go-sdk v1.9.0-pre.1) has no
// Tasks support yet (modelcontextprotocol/go-sdk#626). This is built on the
// SDK's extension points only, and is meant to be replaced by the SDK's own
// support when it lands:
//
//   - A sending middleware (Client.AddSendingMiddleware) declares the
//     extension in the per-request client capabilities (_meta) of
//     tools/call and tasks/*, and only on calls made by a caller that
//     handles task results, to servers that advertise it.
//   - tasks/get, tasks/update and tasks/cancel go through
//     AddSendingCustomMethod / CallCustomMethod.
//   - The one thing the SDK cannot do yet: it decodes every tools/call
//     answer into CallToolResult, which drops taskId and the rest of a
//     CreateTaskResult. So the raw answer to that one call is read on its way
//     in, unchanged: by a Connection wrapper on stdio, and by the HTTP
//     client's RoundTripper on streamable HTTP (wrapping the streamable
//     Connection would hide the SDK's unexported session-update hook from
//     it, which sets the protocol headers). The same RoundTripper sets the
//     Mcp-Name routing header (= taskId) the spec requires on tasks/* over
//     HTTP, which the SDK sets only for its own methods.
//
// Not resumed after a daemon restart: the turn that waits for the result
// does not survive one (the agent loop picks the turn up again with a note
// and the model re-plans), stdio servers are restarted with the daemon so
// their tasks are gone, and a late result would have no call to go to. A
// task cut off by a shutdown is sent tasks/cancel like any stopped call
// (best effort), so its work does not run on unclaimed.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hedeqiang/crewbase/core/tools"
)

// TasksExtension is the extension identifier.
const TasksExtension = "io.modelcontextprotocol/tasks"

const (
	methodGetTask    = "tasks/get"
	methodUpdateTask = "tasks/update"
	methodCancelTask = "tasks/cancel"
	// tasksMinProtocol is the first protocol revision with the extension.
	tasksMinProtocol = "2026-07-28"
	// Polling: the server's pollIntervalMs within these bounds; 1s without one.
	defaultTaskPoll = time.Second
	minTaskPoll     = 10 * time.Millisecond
	maxTaskPoll     = 5 * time.Minute
	// maxTaskPollErrors failed tasks/get in a row end the call.
	maxTaskPollErrors = 3
	// taskCancelWait bounds the tasks/cancel sent for a stopped call.
	taskCancelWait = 5 * time.Second
	// maxSniff bounds what is buffered to read a tools/call answer.
	maxSniff = 16 << 20
)

// Task statuses.
const (
	taskWorking       = "working"
	taskInputRequired = "input_required"
	taskCompleted     = "completed"
	taskFailed        = "failed"
	taskCancelled     = "cancelled"
)

// ---------------------------------------------------------------- wire

// task is a Task (and DetailedTask: the status-specific fields).
type task struct {
	TaskID         string                     `json:"taskId"`
	Status         string                     `json:"status"`
	StatusMessage  string                     `json:"statusMessage,omitempty"`
	CreatedAt      string                     `json:"createdAt,omitempty"`
	LastUpdatedAt  string                     `json:"lastUpdatedAt,omitempty"`
	TTLMs          *int64                     `json:"ttlMs"`
	PollIntervalMs *int64                     `json:"pollIntervalMs,omitempty"`
	InputRequests  map[string]json.RawMessage `json:"inputRequests,omitempty"`
	Result         json.RawMessage            `json:"result,omitempty"`
	Error          *taskError                 `json:"error,omitempty"`
}

type taskError struct {
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type taskParams struct {
	sdk.ParamsBase
	TaskID string `json:"taskId"`
}

type updateTaskParams struct {
	sdk.ParamsBase
	TaskID         string               `json:"taskId"`
	InputResponses sdk.InputResponseMap `json:"inputResponses"`
}

type getTaskResult struct {
	sdk.ResultBase
	ResultType string `json:"resultType,omitempty"`
	task
}

type taskAck struct {
	sdk.ResultBase
	ResultType string `json:"resultType,omitempty"`
}

// addTaskMethods registers the extension's methods and its middleware on a
// client.
func addTaskMethods(cl *sdk.Client) {
	_ = sdk.AddSendingCustomMethod[*taskParams, *getTaskResult](cl, methodGetTask)
	_ = sdk.AddSendingCustomMethod[*updateTaskParams, *taskAck](cl, methodUpdateTask)
	_ = sdk.AddSendingCustomMethod[*taskParams, *taskAck](cl, methodCancelTask)
	cl.AddSendingMiddleware(tasksMiddleware)
}

// declaresTasks reports whether the server advertised the extension on a
// protocol revision that has it.
func declaresTasks(cs *sdk.ClientSession) bool {
	if cs == nil {
		return false
	}
	ir := cs.InitializeResult()
	if ir == nil || ir.Capabilities == nil || ir.ProtocolVersion < tasksMinProtocol {
		return false
	}
	_, ok := ir.Capabilities.Extensions[TasksExtension]
	return ok
}

// tasksMiddleware declares the extension in the per-request capabilities of
// a tools/call whose caller reads task results (a slot in its context) and
// of the extension's own methods.
func tasksMiddleware(next sdk.MethodHandler) sdk.MethodHandler {
	return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
		declare := false
		switch method {
		case "tools/call":
			cs, _ := req.GetSession().(*sdk.ClientSession)
			declare = taskSlotFrom(ctx) != nil && declaresTasks(cs)
		case methodGetTask, methodUpdateTask, methodCancelTask:
			declare = true
		}
		if declare && sdk.HasParams(req) {
			declareTasks(req.GetParams())
		}
		return next(ctx, method, req)
	}
}

// declareTasks adds the extension to the client capabilities the SDK put in
// p's _meta (CallTool and CallCustomMethod hand middleware their own copy
// of the params and a capabilities value built for this request).
func declareTasks(p sdk.Params) {
	m := maps.Clone(p.GetMeta())
	if m == nil {
		return // a session below 2026-07-28: no per-request capabilities
	}
	switch v := m[sdk.MetaKeyClientCapabilities].(type) {
	case interface{ AddExtension(string, map[string]any) }:
		v.AddExtension(TasksExtension, nil)
	default:
		caps := map[string]any{}
		if v != nil {
			b, err := json.Marshal(v)
			if err != nil || json.Unmarshal(b, &caps) != nil {
				return
			}
		}
		ext, _ := caps["extensions"].(map[string]any)
		ext = maps.Clone(ext)
		if ext == nil {
			ext = map[string]any{}
		}
		ext[TasksExtension] = map[string]any{}
		caps["extensions"] = ext
		m[sdk.MetaKeyClientCapabilities] = caps
	}
	p.SetMeta(m)
}

// ---------------------------------------------------------------- reading the answer

// taskSlot receives the CreateTaskResult a tools/call was answered with.
type taskSlot struct {
	mu   sync.Mutex
	t    *task
	err  error
	done bool
}

type taskSlotKey struct{}

func withTaskSlot(ctx context.Context, s *taskSlot) context.Context {
	return context.WithValue(ctx, taskSlotKey{}, s)
}

func taskSlotFrom(ctx context.Context) *taskSlot {
	s, _ := ctx.Value(taskSlotKey{}).(*taskSlot)
	return s
}

// close ends the call the slot is for (the stdio wrapper forgets it).
func (s *taskSlot) close() {
	s.mu.Lock()
	s.done = true
	s.mu.Unlock()
}

func (s *taskSlot) closed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done
}

func (s *taskSlot) get() (*task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.t, s.err
}

// offerResult takes a tools/call result as it came over the wire.
func (s *taskSlot) offerResult(raw json.RawMessage) {
	var probe struct {
		ResultType string `json:"resultType"`
	}
	if json.Unmarshal(raw, &probe) != nil || probe.ResultType != "task" {
		return
	}
	var t task
	err := json.Unmarshal(raw, &t)
	if err == nil && (t.TaskID == "" || t.Status == "") {
		err = errors.New("no taskId or status")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.t != nil || s.err != nil {
		return
	}
	if err != nil {
		s.err = fmt.Errorf("the server answered with a task that cannot be read: %v", err)
		return
	}
	s.t = &t
}

// offerMessage takes one JSON-RPC message read off a tools/call's HTTP
// response and reports whether it was the response (reading can stop).
func (s *taskSlot) offerMessage(raw []byte) bool {
	var msg struct {
		Method string          `json:"method"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &msg) != nil || msg.Method != "" {
		return false
	}
	if len(msg.Result) > 0 {
		s.offerResult(msg.Result)
		return true
	}
	return len(msg.Error) > 0
}

// taskTransport wraps a stdio transport so the answer to a tools/call made
// with a task slot is seen before the SDK decodes it.
type taskTransport struct{ sdk.Transport }

func (t taskTransport) Connect(ctx context.Context) (sdk.Connection, error) {
	c, err := t.Transport.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &taskConn{Connection: c, pending: map[jsonrpc.ID]*taskSlot{}}, nil
}

type taskConn struct {
	sdk.Connection
	mu      sync.Mutex
	pending map[jsonrpc.ID]*taskSlot
}

func (c *taskConn) Write(ctx context.Context, msg jsonrpc.Message) error {
	if r, ok := msg.(*jsonrpc.Request); ok && r.Method == "tools/call" && r.IsCall() {
		if s := taskSlotFrom(ctx); s != nil {
			c.mu.Lock()
			for id, p := range c.pending { // calls that ended without an answer
				if p.closed() {
					delete(c.pending, id)
				}
			}
			c.pending[r.ID] = s
			c.mu.Unlock()
		}
	}
	return c.Connection.Write(ctx, msg)
}

func (c *taskConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	msg, err := c.Connection.Read(ctx)
	if r, ok := msg.(*jsonrpc.Response); ok && err == nil {
		c.mu.Lock()
		s := c.pending[r.ID]
		delete(c.pending, r.ID)
		c.mu.Unlock()
		if s != nil && r.Error == nil {
			s.offerResult(r.Result)
		}
	}
	return msg, err
}

// DropResponse forwards the SDK's optional connection interface.
func (c *taskConn) DropResponse(id jsonrpc.ID) {
	if d, ok := c.Connection.(interface{ DropResponse(jsonrpc.ID) }); ok {
		d.DropResponse(id)
	}
}

type taskNameKey struct{}

// withTaskName marks a tasks/* call for the Mcp-Name header (HTTP).
func withTaskName(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, taskNameKey{}, id)
}

// taskHTTP is the streamable HTTP side: Mcp-Name on tasks/*, and the
// answer to a tools/call made with a task slot read as it streams by.
type taskHTTP struct{ base http.RoundTripper }

func (t *taskHTTP) RoundTrip(r *http.Request) (*http.Response, error) {
	if id, ok := r.Context().Value(taskNameKey{}).(string); ok && strings.HasPrefix(r.Header.Get("Mcp-Method"), "tasks/") && r.Header.Get("Mcp-Name") == "" {
		r = r.Clone(r.Context())
		r.Header.Set("Mcp-Name", headerValue(id))
	}
	resp, err := t.base.RoundTrip(r)
	if err != nil || resp.Body == nil {
		return resp, err
	}
	if s := taskSlotFrom(r.Context()); s != nil {
		mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
		switch mt {
		case "application/json":
			resp.Body = &jsonSniff{ReadCloser: resp.Body, s: s}
		case "text/event-stream":
			resp.Body = &sseSniff{ReadCloser: resp.Body, s: s}
		}
	}
	return resp, nil
}

// headerValue is a header-safe value: as is, or =?base64?…?= wrapped (the
// SDK's rule for Mcp-Name and Mcp-Param-*).
func headerValue(s string) string {
	wrap := strings.HasPrefix(s, "=?base64?") && strings.HasSuffix(s, "?=")
	if s != "" && (s[0] == ' ' || s[0] == '\t' || s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		wrap = true
	}
	for _, c := range s {
		if c < 0x20 || c > 0x7e {
			wrap = true
		}
	}
	if wrap {
		return "=?base64?" + base64.StdEncoding.EncodeToString([]byte(s)) + "?="
	}
	return s
}

// jsonSniff reads a JSON response body as it passes (read at its end, before
// the reader sees EOF).
type jsonSniff struct {
	io.ReadCloser
	s    *taskSlot
	buf  []byte
	done bool
}

func (x *jsonSniff) Read(p []byte) (int, error) {
	n, err := x.ReadCloser.Read(p)
	if !x.done {
		x.buf = append(x.buf, p[:n]...)
		if len(x.buf) > maxSniff {
			x.done, x.buf = true, nil
		} else if err != nil {
			x.done = true
			x.s.offerMessage(x.buf)
			x.buf = nil
		}
	}
	return n, err
}

// sseSniff reads the events of an SSE response body as they pass, each one
// before the reader can have seen its end.
type sseSniff struct {
	io.ReadCloser
	s       *taskSlot
	line    []byte
	data    []byte
	hasData bool
	done    bool
}

func (x *sseSniff) Read(p []byte) (int, error) {
	n, err := x.ReadCloser.Read(p)
	if !x.done {
		x.feed(p[:n])
	}
	return n, err
}

func (x *sseSniff) feed(b []byte) {
	for len(b) > 0 && !x.done {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			x.line = append(x.line, b...)
			break
		}
		x.line = append(x.line, b[:i]...)
		b = b[i+1:]
		x.endLine(bytes.TrimSuffix(x.line, []byte{'\r'}))
		x.line = x.line[:0]
	}
	if len(x.line)+len(x.data) > maxSniff {
		x.done, x.line, x.data = true, nil, nil
	}
}

func (x *sseSniff) endLine(l []byte) {
	switch {
	case len(l) == 0:
		if x.hasData && x.s.offerMessage(x.data) {
			x.done = true
		}
		x.data, x.hasData = x.data[:0], false
	case bytes.HasPrefix(l, []byte("data:")):
		v := bytes.TrimPrefix(l[5:], []byte(" "))
		if x.hasData {
			x.data = append(x.data, '\n')
		}
		x.data, x.hasData = append(x.data, v...), true
	}
}

// resultIsTask reports whether the SDK decoded a task answer it was not
// meant to see (its resultType survives in CallToolResult's encoding).
func resultIsTask(res *sdk.CallToolResult) bool {
	b, err := json.Marshal(res)
	if err != nil {
		return false
	}
	var probe struct {
		ResultType string `json:"resultType"`
	}
	return json.Unmarshal(b, &probe) == nil && probe.ResultType == "task"
}

// ---------------------------------------------------------------- following a task

func getTask(ctx context.Context, cs *sdk.ClientSession, id string) (*task, error) {
	res, err := sdk.CallCustomMethod[*taskParams, *getTaskResult](withTaskName(ctx, id), cs, methodGetTask, &taskParams{TaskID: id})
	if err != nil {
		return nil, err
	}
	if res.ResultType == "task" {
		return nil, errors.New("tasks/get: invalid response (a task handle)")
	}
	if res.TaskID != id || res.Status == "" {
		return nil, fmt.Errorf("tasks/get: invalid response (task %q, status %q)", res.TaskID, res.Status)
	}
	t := res.task
	return &t, nil
}

func updateTask(ctx context.Context, cs *sdk.ClientSession, id string, answers sdk.InputResponseMap) error {
	_, err := sdk.CallCustomMethod[*updateTaskParams, *taskAck](withTaskName(ctx, id), cs, methodUpdateTask, &updateTaskParams{TaskID: id, InputResponses: answers})
	return err
}

// cancelTask asks the server to stop a task (cooperative; best effort, in
// the background: the stopped call does not wait for it).
func (c *conn) cancelTask(ctx context.Context, sess *session, id string) {
	ctx, stop := context.WithTimeout(context.WithoutCancel(ctx), taskCancelWait)
	go func() {
		defer stop()
		if _, err := sdk.CallCustomMethod[*taskParams, *taskAck](withTaskName(ctx, id), sess.cs, methodCancelTask, &taskParams{TaskID: id}); err != nil {
			c.ctx.Log().Debug("mcp: tasks/cancel failed", "server", c.srv.Name, "task", id, "err", sess.scrubErr(err))
		}
	}()
}

// taskBudget is how long a task may run: its ttlMs (from when it was handed
// over: no clock skew), capped by taskTimeout.
func (c *conn) taskBudget(t *task) time.Duration {
	d := c.s.t.task
	if t.TTLMs != nil && *t.TTLMs >= 0 {
		d = min(d, time.Duration(*t.TTLMs)*time.Millisecond)
	}
	return d
}

func pollInterval(t *task) time.Duration {
	if t.PollIntervalMs == nil || *t.PollIntervalMs <= 0 {
		return defaultTaskPoll
	}
	return min(max(time.Duration(*t.PollIntervalMs)*time.Millisecond, minTaskPoll), maxTaskPoll)
}

// awaitTask follows the task a tools/call was answered with to its end.
// ctx is the call's context, whose clock now follows the task.
func (c *conn) awaitTask(ctx context.Context, sess *session, a *activeCall, t *task) tools.Result {
	name, id := a.call.Name, t.TaskID
	budget := c.taskBudget(t)
	a.clock.set(budget)
	answered := map[string]bool{}
	shown := ""
	errs := 0
	for {
		if b := c.taskBudget(t); b != budget { // ttlMs MAY change
			a.clock.extend(b - budget)
			budget = b
		}
		if msg := strings.TrimSpace(t.StatusMessage); msg != shown && msg != "" && (t.Status == taskWorking || t.Status == taskInputRequired) {
			shown = msg
			tools.Progress(a.call, msg)
		}
		switch t.Status {
		case taskCompleted:
			var res sdk.CallToolResult
			if len(t.Result) == 0 || json.Unmarshal(t.Result, &res) != nil {
				return tools.Errorf("MCP %s: task %s completed without a readable result", name, id)
			}
			return ConvertResult(&res)
		case taskFailed:
			msg := "unknown error"
			if t.Error != nil {
				msg = fmt.Sprintf("%s (code %d)", t.Error.Message, t.Error.Code)
			} else if t.StatusMessage != "" {
				msg = t.StatusMessage
			}
			return tools.Errorf("MCP %s: task failed: %s", name, sess.scrubErr(errors.New(msg)))
		case taskCancelled:
			msg := "the server cancelled the task"
			if t.StatusMessage != "" {
				msg += ": " + t.StatusMessage
			}
			return tools.Errorf("MCP %s: %s", name, msg)
		case taskInputRequired:
			if err := c.answerTask(ctx, sess, a, t, answered); err != nil {
				if ctx.Err() == nil {
					c.cancelTask(ctx, sess, id)
					return tools.Errorf("MCP %s: %v", name, sess.scrubErr(err))
				}
			}
		}
		if ctx.Err() == nil {
			sleepCtx(ctx, pollInterval(t))
		}
		if ctx.Err() != nil {
			return c.taskStopped(ctx, sess, name, id)
		}
		nt, err := getTask(withActive(ctx, a), sess.cs, id)
		if err != nil {
			if ctx.Err() != nil {
				return c.taskStopped(ctx, sess, name, id)
			}
			var we *jsonrpc.Error
			if errs++; errs >= maxTaskPollErrors || (errors.As(err, &we) && we.Code == jsonrpc.CodeInvalidParams) {
				// gone (expired, purged) or the server keeps failing
				return tools.Errorf("MCP %s: task %s: %v", name, id, sess.scrubErr(err))
			}
			continue
		}
		errs = 0
		t = nt
	}
}

// taskStopped ends a call whose context ended while its task ran: the
// task is cancelled (the person stopped the turn, the call timed out, or
// the daemon is going away).
func (c *conn) taskStopped(ctx context.Context, sess *session, name, id string) tools.Result {
	c.cancelTask(ctx, sess, id)
	if context.Cause(ctx) == errCallTimeout {
		return tools.Errorf("MCP %s: %v (the task ran out of time and was cancelled)", name, errCallTimeout)
	}
	return tools.Errorf("cancelled")
}

// answerTask answers the task's new input requests: elicitations through
// the person (each key once over the task's life), then tasks/update. A
// server asking for anything else (sampling, roots: never declared) cannot
// be served, so the call ends.
func (c *conn) answerTask(ctx context.Context, sess *session, a *activeCall, t *task, answered map[string]bool) error {
	keys := make([]string, 0, len(t.InputRequests))
	for k := range t.InputRequests {
		if !answered[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	answers := sdk.InputResponseMap{}
	var unsupported []string
	for _, k := range keys {
		raw := t.InputRequests[k]
		var probe struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(raw, &probe)
		if probe.Method != "elicitation/create" {
			unsupported = append(unsupported, fmt.Sprintf("%q", probe.Method))
			continue
		}
		var m sdk.InputRequestMap
		wrapped, _ := json.Marshal(map[string]json.RawMessage{k: raw})
		p, ok := (*sdk.ElicitParams)(nil), false
		if json.Unmarshal(wrapped, &m) == nil {
			p, ok = m[k].(*sdk.ElicitParams)
		}
		if !ok {
			answers[k] = cancelled
		} else {
			res, err := c.s.elicit(withActive(ctx, a), c.srv.Name, p)
			if err != nil || res == nil {
				res = cancelled
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			answers[k] = res
		}
		answered[k] = true
	}
	if len(answers) > 0 {
		if err := updateTask(withActive(ctx, a), sess.cs, t.TaskID, answers); err != nil {
			return fmt.Errorf("tasks/update: %w", err)
		}
	}
	if len(unsupported) > 0 {
		return fmt.Errorf("the task asks for %s, which Crewbase does not answer", strings.Join(unsupported, ", "))
	}
	return nil
}
