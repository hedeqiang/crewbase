package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/bridge/jsonrpc"
	"github.com/hedeqiang/crewbase/core/llm"
)

// proc is one running ACP agent process and its JSON-RPC connection.
type proc struct {
	a      *Adapter
	cmd    *exec.Cmd
	conn   *jsonrpc.Conn
	stderr *ringBuffer
	exited chan struct{} // closed after the process was reaped
	info   initializeResult

	mu       sync.Mutex
	sessions map[string]*sessionState
}

// sessionState routes updates of one ACP session.
type sessionState struct {
	id  string
	cwd string

	mu   sync.Mutex
	turn *turn // active prompt turn, nil between turns
}

func (s *sessionState) current() *turn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turn
}

func (s *sessionState) setTurn(t *turn) {
	s.mu.Lock()
	s.turn = t
	s.mu.Unlock()
}

// startProc launches the agent and performs initialize.
func (a *Adapter) startProc(ctx context.Context) (*proc, error) {
	if len(a.cfg.Command) == 0 {
		return nil, &llm.Error{Kind: llm.ErrUnavailable, Provider: a.id, Message: "no ACP command configured"}
	}
	cmd := exec.Command(a.cfg.Command[0], a.cfg.Command[1:]...)
	cmd.Dir = a.cfg.Cwd
	cmd.Env = os.Environ()
	for k, v := range a.cfg.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	setProcessGroup(cmd)
	cmd.WaitDelay = 2 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	p := &proc{a: a, cmd: cmd, stderr: newRing(16 << 10), exited: make(chan struct{}), sessions: map[string]*sessionState{}}
	cmd.Stderr = p.stderr
	if err := cmd.Start(); err != nil {
		return nil, &llm.Error{Kind: llm.ErrUnavailable, Provider: a.id, Message: "start: " + err.Error()}
	}
	p.conn = jsonrpc.NewConn(stdout, stdin, jsonrpc.Options{
		OnRequest:      p.onRequest,
		OnNotification: p.onNotification,
		Trace:          a.trace,
	})

	waited := make(chan struct{})
	if !a.goFn(func(context.Context) {
		_ = cmd.Wait()
		close(waited)
	}) {
		killGroup(cmd, true)
		return nil, &llm.Error{Kind: llm.ErrUnavailable, Provider: a.id, Message: "adapter closed"}
	}
	if !a.goFn(func(base context.Context) {
		defer close(p.exited)
		_ = p.conn.Serve(base)
		// connection gone (EOF, Close, plugin dispose): stop the whole group
		killGroup(cmd, false)
		select {
		case <-waited:
		case <-time.After(2 * time.Second):
			killGroup(cmd, true)
			<-waited
		}
		// reap grandchildren the CLI left behind in its group
		killGroup(cmd, true)
		a.procExited(p)
	}) {
		killGroup(cmd, true)
		return nil, &llm.Error{Kind: llm.ErrUnavailable, Provider: a.id, Message: "adapter closed"}
	}

	ictx, cancel := context.WithTimeout(ctx, a.initTimeout)
	defer cancel()
	params := initializeParams{
		ProtocolVersion: protocolVersion,
		ClientCapabilities: map[string]any{
			"fs":       map[string]bool{"readTextFile": true, "writeTextFile": true},
			"terminal": false,
		},
		ClientInfo: implInfo{Name: "crewbase", Title: "Crewbase", Version: "0.1.0"},
	}
	if err := p.conn.Call(ictx, mInitialize, params, &p.info); err != nil {
		p.kill()
		return nil, p.classify(err, llm.ErrUnavailable)
	}
	return p, nil
}

// kill closes the connection and waits (bounded) for the process to go.
func (p *proc) kill() {
	_ = p.conn.Close()
	select {
	case <-p.exited:
	case <-time.After(5 * time.Second):
	}
}

func (p *proc) alive() bool {
	select {
	case <-p.conn.Done():
		return false
	default:
		return true
	}
}

// classify maps err using the stderr tail; when the connection died it first
// waits briefly for the process to exit so stderr is complete.
func (p *proc) classify(err error, fallback llm.ErrorKind) error {
	if errors.Is(err, jsonrpc.ErrClosed) || !p.alive() {
		select {
		case <-p.exited:
		case <-time.After(time.Second):
		}
		fallback = llm.ErrUnavailable
		if code := p.exitCode(); code != "" {
			err = fmt.Errorf("agent process exited (%s): %w", code, err)
		}
	}
	return classify(p.a.id, err, p.stderr.String(), fallback)
}

func (p *proc) exitCode() string {
	select {
	case <-p.exited:
	default:
		return ""
	}
	if ps := p.cmd.ProcessState; ps != nil {
		return ps.String()
	}
	return ""
}

func (p *proc) session(id string) *sessionState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sessions[id]
}

// newSession runs session/new and optionally selects a model.
func (p *proc) newSession(ctx context.Context, model string) (*sessionState, error) {
	var res newSessionResult
	if err := p.conn.Call(ctx, mSessionNew, newSessionParams{Cwd: p.a.cfg.Cwd, MCPServers: []any{}}, &res); err != nil {
		return nil, p.classify(err, "")
	}
	if res.SessionID == "" {
		return nil, &llm.Error{Kind: llm.ErrUnavailable, Provider: p.a.id, Message: "session/new returned no sessionId"}
	}
	if res.Models != nil {
		p.a.learnModels(res.Models.AvailableModels)
	}
	s := &sessionState{id: res.SessionID, cwd: p.a.cfg.Cwd}
	p.mu.Lock()
	p.sessions[s.id] = s
	p.mu.Unlock()
	if model != "" && model != defaultModel && (res.Models == nil || res.Models.CurrentModelID != model) {
		// unstable ACP extension; agents without it keep their default
		err := p.conn.Call(ctx, mSessionSetModel, map[string]string{"sessionId": s.id, "modelId": model}, nil)
		var je *jsonrpc.Error
		if err != nil && !(errors.As(err, &je) && je.Code == jsonrpc.CodeMethodNotFound) {
			return nil, p.classify(err, llm.ErrBadRequest)
		}
	}
	return s, nil
}

func (p *proc) onNotification(method string, params json.RawMessage) {
	if method != mSessionUpdate {
		return
	}
	var n sessionNotification
	if json.Unmarshal(params, &n) != nil {
		return
	}
	s := p.session(n.SessionID)
	if s == nil {
		return
	}
	t := s.current()
	if t == nil {
		return // between turns (e.g. available_commands_update)
	}
	var u sessionUpdate
	if json.Unmarshal(n.Update, &u) != nil {
		return
	}
	t.update(u)
}

func (p *proc) onRequest(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case mRequestPermission:
		return p.permission(params)
	case mFSRead:
		var rp readParams
		if err := json.Unmarshal(params, &rp); err != nil {
			return nil, jsonrpc.NewError(jsonrpc.CodeInvalidParams, err.Error(), nil)
		}
		s := p.session(rp.SessionID)
		if s == nil {
			return nil, jsonrpc.NewError(jsonrpc.CodeInvalidParams, "unknown session", nil)
		}
		content, err := readText(s.cwd, rp.Path, rp.Line, rp.Limit)
		if err != nil {
			return nil, err
		}
		return map[string]string{"content": content}, nil
	case mFSWrite:
		var wp writeParams
		if err := json.Unmarshal(params, &wp); err != nil {
			return nil, jsonrpc.NewError(jsonrpc.CodeInvalidParams, err.Error(), nil)
		}
		s := p.session(wp.SessionID)
		if s == nil {
			return nil, jsonrpc.NewError(jsonrpc.CodeInvalidParams, "unknown session", nil)
		}
		if err := writeText(s.cwd, wp.Path, wp.Content); err != nil {
			return nil, err
		}
		if t := s.current(); t != nil {
			t.note(fmt.Sprintf("[%s wrote %s]", p.a.cfg.Name, wp.Path))
		}
		return map[string]any{}, nil
	}
	// terminal/* and anything else: not offered (capabilities say so)
	return nil, jsonrpc.ErrMethodNotFound
}

// permission answers session/request_permission. Without autoApprove every
// request is rejected; Crewbase approval routing is a TODO.
func (p *proc) permission(params json.RawMessage) (any, error) {
	var pp permissionParams
	if err := json.Unmarshal(params, &pp); err != nil {
		return nil, jsonrpc.NewError(jsonrpc.CodeInvalidParams, err.Error(), nil)
	}
	s := p.session(pp.SessionID)
	var t *turn
	if s != nil {
		t = s.current()
	}
	if t == nil || t.cancelled() {
		return permissionResult{Outcome: permissionOutcome{Outcome: "cancelled"}}, nil
	}
	pick := func(kinds ...string) string {
		for _, k := range kinds {
			for _, o := range pp.Options {
				if o.Kind == k {
					return o.OptionID
				}
			}
		}
		return ""
	}
	title := pp.ToolCall.Title
	if title == "" {
		title = pp.ToolCall.ToolCallID
	}
	if p.a.autoApprove {
		if id := pick("allow_once", "allow_always"); id != "" {
			t.note(fmt.Sprintf("[auto-approved: %s]", title))
			return permissionResult{Outcome: permissionOutcome{Outcome: "selected", OptionID: id}}, nil
		}
	}
	t.note(fmt.Sprintf("[permission denied by Crewbase: %s]", title))
	if id := pick("reject_once", "reject_always"); id != "" {
		return permissionResult{Outcome: permissionOutcome{Outcome: "selected", OptionID: id}}, nil
	}
	return permissionResult{Outcome: permissionOutcome{Outcome: "cancelled"}}, nil
}

// within resolves path (absolute, per ACP) and checks it stays inside root,
// following symlinks of the existing part of the path.
func within(root, path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", jsonrpc.NewError(jsonrpc.CodeInvalidParams, "path must be absolute", nil)
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", jsonrpc.NewError(jsonrpc.CodeInternalError, "session cwd: "+err.Error(), nil)
	}
	clean := filepath.Clean(path)
	// resolve the deepest existing ancestor
	existing, rest := clean, ""
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}
	real, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", jsonrpc.NewError(jsonrpc.CodeInternalError, err.Error(), nil)
	}
	real = filepath.Join(real, rest)
	rel, err := filepath.Rel(rootReal, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", jsonrpc.NewError(-32001, "access outside the session directory is denied", map[string]string{"path": path})
	}
	return real, nil
}

func readText(root, path string, line, limit int) (string, error) {
	real, err := within(root, path)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(real)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", jsonrpc.NewError(-32002, "Resource not found: "+path, map[string]string{"uri": path})
		}
		return "", err
	}
	s := string(b)
	if line <= 0 && limit <= 0 {
		return s, nil
	}
	lines := strings.SplitAfter(s, "\n")
	start := 0
	if line > 0 {
		start = min(line-1, len(lines))
	}
	end := len(lines)
	if limit > 0 {
		end = min(start+limit, len(lines))
	}
	return strings.Join(lines[start:end], ""), nil
}

func writeText(root, path, content string) error {
	real, err := within(root, path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(real); err == nil {
		mode = st.Mode().Perm()
	}
	return os.WriteFile(real, []byte(content), mode)
}
