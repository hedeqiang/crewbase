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
	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/tools"
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
	mcpSkip  sync.Once
}

// sessionState routes updates of one ACP session.
type sessionState struct {
	id  string
	cwd string
	// own: cwd is an agent's own workspace (llm.Request.OwnWorkspace); its
	// CLI config may not be written (see cliconfig.go)
	own bool
	// grant gives the CLI Crewbase's MCP server (nil = not offered)
	grant MCPGrant

	mu   sync.Mutex
	turn *turn // active prompt turn, nil between turns
}

// revoke ends the session's MCP grant.
func (s *sessionState) revoke() {
	if s != nil && s.grant != nil {
		s.grant.Revoke()
	}
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
	afterStart(cmd)
	p.conn = jsonrpc.NewConn(stdout, stdin, jsonrpc.Options{
		OnRequest:      p.onRequest,
		OnNotification: p.onNotification,
		Trace:          traceFn(a.trace),
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

// traceFn redacts bearer tokens (the MCP grant's) from traced traffic.
func traceFn(f func(string, []byte)) func(string, []byte) {
	if f == nil {
		return nil
	}
	return func(dir string, msg []byte) { f(dir, redactBearer(msg)) }
}

// revokeAll ends the MCP grants of every session of the process.
func (p *proc) revokeAll() {
	p.mu.Lock()
	ss := make([]*sessionState, 0, len(p.sessions))
	for _, s := range p.sessions {
		ss = append(ss, s)
	}
	p.mu.Unlock()
	for _, s := range ss {
		s.revoke()
	}
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

// newSession runs session/new in cwd and optionally selects a model. own
// marks cwd as an agent's own workspace. With bind (an agent in a Crewbase
// session) the CLI is given Crewbase's MCP server when it takes http MCP.
func (p *proc) newSession(ctx context.Context, model, cwd string, own bool, bind *MCPBinding) (*sessionState, error) {
	var res newSessionResult
	params := newSessionParams{Cwd: cwd, MCPServers: []any{}, Meta: sessionMeta(p.a.cfg.ID, own)}
	grant := p.mcpGrant(bind)
	if grant != nil {
		name, url, tok := grant.Server()
		params.MCPServers = append(params.MCPServers, mcpServerHTTP{Type: "http", Name: name, URL: url,
			Headers: []httpHeader{{Name: "Authorization", Value: "Bearer " + tok}}})
	}
	if err := p.conn.Call(ctx, mSessionNew, params, &res); err != nil {
		if grant != nil {
			grant.Revoke()
		}
		return nil, p.classify(err, "")
	}
	if res.SessionID == "" {
		if grant != nil {
			grant.Revoke()
		}
		return nil, &llm.Error{Kind: llm.ErrUnavailable, Provider: p.a.id, Message: "session/new returned no sessionId"}
	}
	if res.Models != nil {
		p.a.learnModels(res.Models.AvailableModels)
	}
	s := &sessionState{id: res.SessionID, cwd: cwd, own: own, grant: grant}
	p.mu.Lock()
	p.sessions[s.id] = s
	p.mu.Unlock()
	if !p.alive() {
		// the process went away meanwhile; its sessions were revoked already
		s.revoke()
	}
	if model != "" && model != defaultModel && (res.Models == nil || res.Models.CurrentModelID != model) {
		// unstable ACP extension; agents without it keep their default
		err := p.conn.Call(ctx, mSessionSetModel, map[string]string{"sessionId": s.id, "modelId": model}, nil)
		var je *jsonrpc.Error
		if err != nil && !(errors.As(err, &je) && je.Code == jsonrpc.CodeMethodNotFound) {
			p.dropSession(s)
			return nil, p.classify(err, llm.ErrBadRequest)
		}
	}
	return s, nil
}

// dropSession forgets s and revokes its grant.
func (p *proc) dropSession(s *sessionState) {
	p.mu.Lock()
	if p.sessions[s.id] == s {
		delete(p.sessions, s.id)
	}
	p.mu.Unlock()
	s.revoke()
}

// mcpGrant opens an MCP grant for bind, or nil (no bridge, no binding, or
// the agent does not take http MCP servers).
func (p *proc) mcpGrant(bind *MCPBinding) MCPGrant {
	a := p.a
	if a.mcp == nil || bind == nil || bind.Agent == "" || bind.Session == "" {
		return nil
	}
	if !httpMCP(p.info.AgentCapabilities) {
		p.mcpSkip.Do(func() {
			a.log.Info("acp: agent does not advertise http MCP servers; Crewbase tools not offered", "agent", a.cfg.ID)
		})
		return nil
	}
	return a.mcp(*bind)
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
		if s.own && touchesCLIConfig(s.cwd, []string{wp.Path}) != "" {
			if t := s.current(); t != nil {
				t.note(fmt.Sprintf("[write refused: %s is CLI configuration in the agent workspace]", wp.Path))
			}
			return nil, jsonrpc.NewError(-32001, "writing CLI configuration in this workspace is denied", map[string]string{"path": wp.Path})
		}
		if err := writeText(s.cwd, wp.Path, wp.Content); err != nil {
			return nil, err
		}
		if t := s.current(); t != nil {
			t.note(fmt.Sprintf("[%s wrote %s]", p.a.cfg.Name, wp.Path))
			t.fileChanged(wp.Path)
		}
		return map[string]any{}, nil
	}
	// terminal/* and anything else: not offered (capabilities say so)
	return nil, jsonrpc.ErrMethodNotFound
}

// permission answers session/request_permission. autoApprove allows every
// request; otherwise it is routed to Crewbase approvals (Options.Approve) and
// only an allowed-once answer selects the CLI's allow_once option. Everything
// else (no approver, rejected, timeout) selects reject_once.
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
	seen := t.tool(pp.ToolCall.ToolCallID)
	title := pp.ToolCall.Title
	if title == "" {
		title = seen.title
	}
	if title == "" {
		title = pp.ToolCall.ToolCallID
	}
	kind := pp.ToolCall.Kind
	if kind == "" {
		kind = seen.kind
	}
	if s.own && !readOnlyKind(kind) {
		paths := append([]string(nil), seen.paths...)
		for _, l := range pp.ToolCall.Locations {
			paths = append(paths, l.Path)
		}
		paths = append(paths, editPaths(sessionUpdate{Content: pp.ToolCall.Content})...)
		paths = append(paths, rawInputPaths(pp.ToolCall.RawInput)...)
		if hit := touchesCLIConfig(s.cwd, paths); hit != "" {
			// never offered, not even with autoApprove: it would let the
			// CLI widen its own permissions on the next turn
			t.note(fmt.Sprintf("[permission denied by Crewbase: %s changes CLI configuration (%s)]", title, hit))
			if id := pick("reject_once", "reject_always"); id != "" {
				return permissionResult{Outcome: permissionOutcome{Outcome: "selected", OptionID: id}}, nil
			}
			return permissionResult{Outcome: permissionOutcome{Outcome: "cancelled"}}, nil
		}
	}
	if s.grant != nil {
		name := metaToolName(pp.ToolCall.Meta)
		if name == "" {
			name = seen.name
		}
		if tool := crewbaseToolName(kind, pp.ToolCall.Title, name, pp.ToolCall.Content); tool != "" && s.grant.Allows(tool) {
			// Crewbase's own tool: the call runs through Crewbase's tool
			// pipeline, which asks the person itself when its rules say so
			if id := pick("allow_once"); id != "" {
				t.note(fmt.Sprintf("[Crewbase tool %s: Crewbase's own policy applies]", tool))
				return permissionResult{Outcome: permissionOutcome{Outcome: "selected", OptionID: id}}, nil
			}
		}
	}
	if p.a.autoApprove {
		if id := pick("allow_once", "allow_always"); id != "" {
			t.note(fmt.Sprintf("[auto-approved: %s]", title))
			return permissionResult{Outcome: permissionOutcome{Outcome: "selected", OptionID: id}}, nil
		}
	}
	outcome := approval.OutcomeUnavailable
	if p.a.approve != nil {
		outcome = p.a.approve(t.ctx, approval.Request{
			CallID:   "acp/" + t.session + "/" + pp.ToolCall.ToolCallID,
			Session:  t.session,
			Agent:    t.agent,
			Tool:     p.a.id,
			Category: permissionCategory(kind),
			Summary:  title,
			Reason:   "the " + p.a.cfg.Name + " CLI asks for permission",
		})
	}
	if outcome == approval.OutcomeAllowedOnce {
		if id := pick("allow_once"); id != "" {
			t.note(fmt.Sprintf("[approved: %s]", title))
			return permissionResult{Outcome: permissionOutcome{Outcome: "selected", OptionID: id}}, nil
		}
	}
	t.note(fmt.Sprintf("[permission denied by Crewbase: %s]", title))
	if id := pick("reject_once", "reject_always"); id != "" {
		return permissionResult{Outcome: permissionOutcome{Outcome: "selected", OptionID: id}}, nil
	}
	return permissionResult{Outcome: permissionOutcome{Outcome: "cancelled"}}, nil
}

// permissionCategory maps an ACP tool kind to a Crewbase action category;
// unknown kinds are treated as command execution.
func permissionCategory(kind string) string {
	switch kind {
	case "read", "fetch", "search":
		return tools.CategoryRead
	case "edit", "move":
		return tools.CategoryWrite
	case "delete":
		return tools.CategoryDelete
	}
	return tools.CategoryExec
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
