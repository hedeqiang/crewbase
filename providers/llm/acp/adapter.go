package acp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/llm"
)

const defaultModel = "default"

// Options tune an Adapter (mostly for tests).
type Options struct {
	// AutoApprove answers every CLI permission request with allow_once,
	// bypassing Approve (explicit override).
	AutoApprove bool
	// Approve asks Crewbase whether a CLI permission request may proceed
	// (the plugin routes it to the approval service). Nil rejects every
	// request. Only approval.OutcomeAllowedOnce allows.
	Approve func(ctx context.Context, req approval.Request) approval.Outcome
	// IdleTimeout stops the process after this long without an active turn
	// (0 = 10 minutes, negative = never).
	IdleTimeout time.Duration
	// ToolNotes adds a text line per CLI tool call (default true via Plugin).
	ToolNotes bool
	// FilesChanged is told which workspace files a turn's CLI tools changed
	// (edit / delete / move tool calls and fs/write_text_file).
	FilesChanged func(agent, session string, paths []string)
	// StateDir keeps learned model lists across restarts (see models.go).
	StateDir string
	// CancelGrace is how long a cancelled turn waits for the agent to
	// acknowledge session/cancel (default 5s).
	CancelGrace time.Duration
	// InitTimeout bounds process start + initialize (default 90s; npx may
	// download the adapter on first use).
	InitTimeout time.Duration
	// Go runs background goroutines (kernel Context.Go in the plugin). It
	// returns false when the owner is shutting down. Default: plain goroutines.
	Go func(fn func(ctx context.Context)) bool
	// Trace sees raw JSON-RPC traffic (bearer tokens redacted).
	Trace func(dir string, msg []byte)
	// MCP opens a grant on Crewbase's MCP server for an ACP session of an
	// agent in a Crewbase session (see mcp.go); nil = Crewbase tools are
	// not offered. A nil grant skips the server for that session.
	MCP func(b MCPBinding) MCPGrant
	// Log receives operational messages (default slog.Default()).
	Log *slog.Logger
}

// Adapter is an llm.Adapter backed by one ACP agent process. A Crewbase
// step becomes one ACP session/prompt turn; the CLI runs its own tools.
type Adapter struct {
	id           string
	cfg          resolved
	autoApprove  bool
	approve      func(context.Context, approval.Request) approval.Outcome
	idle         time.Duration
	toolNotes    bool
	filesChanged func(agent, session string, paths []string)
	cancelGrace  time.Duration
	initTimeout  time.Duration
	goFn         func(fn func(ctx context.Context)) bool
	trace        func(string, []byte)
	stateDir     string // where learned model lists are cached ("" = not kept)
	mcp          func(MCPBinding) MCPGrant
	log          *slog.Logger

	startMu sync.Mutex // serialises process start
	mu      sync.Mutex
	p       *proc
	active  int
	timer   *time.Timer
	convs   map[string]*conv
	quota   llm.Quota

	// model lists (see models.go, shape.go)
	acpModels   []acpModel    // what the bridge reported
	native      []nativeModel // the CLI's own list
	// unsupported are models this account cannot use (the CLI said so);
	// they are listed switched off. Kept with the model cache.
	unsupported map[string]bool
	accountErr  string // see noteAccount
	entries     []modelEntry  // what is offered, shaped from both
	acpEfforts  map[string]bool
	learned     time.Time // when the lists were learned
	learnedWith string    // the versions they were learned with
	versions    string    // the CLI and bridge versions running now
	bridge      *llm.Bridge
	closed      bool

	// cliSessions: the ACP session each agent + Crewbase session ran in
	// last (see recorder.go)
	cliSessions map[string]cliSession
}

// conv is a Crewbase conversation mapped onto an ACP session.
type conv struct {
	p       *proc
	s       *sessionState
	prints  []string // fingerprints of the messages the session has seen
	busy    bool
	lastUse time.Time
}

// New creates an adapter for a resolved agent config.
func New(id string, cfg AgentConfig, opts Options) *Adapter {
	return newAdapter(id, resolve(cfg), opts)
}

func newAdapter(id string, r resolved, opts Options) *Adapter {
	a := &Adapter{id: id, cfg: r, autoApprove: opts.AutoApprove, approve: opts.Approve, idle: opts.IdleTimeout, toolNotes: opts.ToolNotes,
		cancelGrace: opts.CancelGrace, initTimeout: opts.InitTimeout, trace: opts.Trace, convs: map[string]*conv{}}
	if a.idle == 0 {
		a.idle = 10 * time.Minute
	}
	if a.cancelGrace <= 0 {
		a.cancelGrace = 5 * time.Second
	}
	if a.initTimeout <= 0 {
		a.initTimeout = 90 * time.Second
	}
	a.filesChanged = opts.FilesChanged
	a.stateDir = opts.StateDir
	a.bridge = bridgeOf(r)
	a.goFn = opts.Go
	a.mcp = opts.MCP
	a.log = opts.Log
	if a.log == nil {
		a.log = slog.Default()
	}
	if a.goFn == nil {
		a.goFn = func(fn func(context.Context)) bool { go fn(context.Background()); return true }
	}
	return a
}

func (a *Adapter) ID() string   { return a.id }
func (a *Adapter) Kind() string { return "subscription" }

// RunsOwnTools implements llm.OwnTools: the CLI runs its own tools and never
// sees Request.Tools (only Crewbase's MCP server tools, see mcp.go).
func (a *Adapter) RunsOwnTools() bool { return true }

// Models returns configured models, else those learned from the CLI and
// its bridge (see shape.go); a "default" that keeps the CLI's own choice
// until they are known. It never starts the process.
func (a *Adapter) Models(context.Context) ([]llm.ModelInfo, error) {
	if len(a.cfg.Models) > 0 {
		out := make([]llm.ModelInfo, 0, len(a.cfg.Models))
		for _, m := range a.cfg.Models {
			w := m.ContextWindow
			if w == 0 {
				w = a.cfg.ContextWindow
			}
			out = append(out, llm.ModelInfo{ID: m.ID, Provider: a.id, Name: m.Name, ContextWindow: w, Capabilities: []string{"code"}, Efforts: llm.NoEfforts, Enabled: true})
		}
		return out, nil
	}
	a.mu.Lock()
	es := a.entries
	a.mu.Unlock()
	out := make([]llm.ModelInfo, 0, len(es)+1)
	// "default" (the CLI's own choice) is listed only until the real
	// models are known; requests for it keep working either way.
	if len(es) == 0 {
		out = append(out, llm.ModelInfo{ID: defaultModel, Provider: a.id, Name: a.cfg.Name, ContextWindow: a.cfg.ContextWindow, Capabilities: []string{"code"}, Efforts: llm.NoEfforts, Enabled: true})
	}
	a.mu.Lock()
	unsup := maps.Clone(a.unsupported)
	a.mu.Unlock()
	for _, e := range es {
		mi := llm.ModelInfo{ID: e.ID, Provider: a.id, Name: e.Name, ContextWindow: a.cfg.ContextWindow, ContextEstimated: true, Capabilities: []string{"code"}, Efforts: llm.NoEfforts, Enabled: !unsup[e.ID]}
		if e.ContextWindow > 0 {
			mi.ContextWindow, mi.ContextEstimated = e.ContextWindow, false
		}
		if e.Vision {
			mi.Capabilities = append(mi.Capabilities, "vision")
		}
		if len(e.Efforts) > 0 {
			mi.Efforts = append([]string(nil), e.Efforts...)
		}
		out = append(out, mi)
	}
	return out, nil
}

// learnModels keeps the list a session/new reported.
func (a *Adapter) learnModels(ms []acpModel) {
	if len(ms) == 0 {
		return
	}
	a.mu.Lock()
	a.acpModels, a.learned = ms, time.Now()
	a.mu.Unlock()
	a.reshape()
	a.saveModels()
}

// reshape recomputes the offered models from the raw lists.
func (a *Adapter) reshape() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = shapeModels(a.cfg.ID, a.acpModels, a.native)
	// the levels the bridge itself spells in "<model>/<level>" ids
	a.acpEfforts = map[string]bool{}
	for _, m := range a.acpModels {
		if _, lvl, ok := strings.Cut(m.ModelID, "/"); ok && effortOf(lvl) != "" {
			a.acpEfforts[effortOf(lvl)] = true
		}
	}
}

// entry returns the offered model id, if known.
func (a *Adapter) entry(id string) (modelEntry, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if e := findEntry(a.entries, id); e != nil {
		return *e, true
	}
	return modelEntry{}, false
}

// setModelID is the id session/set_model takes for model at effort. Old
// ids (a variant someone picked before variants were folded) pass as is.
func (a *Adapter) setModelID(model, effort string) string {
	e, ok := a.entry(model)
	if !ok {
		return model
	}
	if e.Native {
		return a.codexWire(e, effort)
	}
	return e.wire(effort)
}

// codexWire is the "<slug>/<effort>" id codex-acp < 0.9 takes for a model
// of the CLI's own list. The level goes along only when the bridge spells
// it itself (its copy of Codex may not know newer ones; a bare slug then
// keeps the configured level).
func (a *Adapter) codexWire(e modelEntry, effort string) string {
	if effort == "" || !slices.Contains(e.Efforts, effort) {
		effort = e.DefaultEffort
	}
	a.mu.Lock()
	ok := a.acpEfforts[effort]
	a.mu.Unlock()
	if effort == "" || !ok {
		return e.ID
	}
	return e.ID + "/" + effort
}

// Quota is known only after the CLI reported a limit (ACP has no quota API):
// remaining 0 until the reported reset time, unknown otherwise.
func (a *Adapter) Quota(context.Context) llm.Quota {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.quota.Known && !a.quota.ResetsAt.IsZero() && time.Now().After(a.quota.ResetsAt) {
		a.quota = llm.Quota{}
	}
	return a.quota
}

// Running reports whether the agent process is up.
func (a *Adapter) Running() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.p != nil && a.p.alive()
}

// Pid returns the agent process id (0 when not running).
func (a *Adapter) Pid() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.p == nil || a.p.cmd.Process == nil {
		return 0
	}
	return a.p.cmd.Process.Pid
}

// AgentVersion is the agentInfo.version reported by initialize ("" before).
func (a *Adapter) AgentVersion() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.p == nil || a.p.info.AgentInfo == nil {
		return ""
	}
	return a.p.info.AgentInfo.Version
}

// Close stops the process (whole group) and forgets sessions.
func (a *Adapter) Close() {
	a.mu.Lock()
	a.closed = true
	p := a.p
	a.p = nil
	a.convs = map[string]*conv{}
	if a.timer != nil {
		a.timer.Stop()
	}
	a.mu.Unlock()
	if p != nil {
		p.kill()
		p.revokeAll()
	}
}

func (a *Adapter) procExited(p *proc) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.p == p {
		a.p = nil
	}
	for k, c := range a.convs {
		if c.p == p {
			delete(a.convs, k)
		}
	}
	p.revokeAll()
}

// ensureProc returns a live process, starting one on demand.
func (a *Adapter) ensureProc(ctx context.Context) (*proc, error) {
	a.startMu.Lock()
	defer a.startMu.Unlock()
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil, &llm.Error{Kind: llm.ErrUnavailable, Provider: a.id, Message: "adapter closed"}
	}
	if a.p != nil && a.p.alive() {
		p := a.p
		a.mu.Unlock()
		return p, nil
	}
	a.mu.Unlock()
	p, err := a.startProc(ctx)
	if err != nil {
		a.noteQuota(err, "")
		return nil, err
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		p.kill()
		return nil, &llm.Error{Kind: llm.ErrUnavailable, Provider: a.id, Message: "adapter closed"}
	}
	a.p = p
	a.mu.Unlock()
	return p, nil
}

func (a *Adapter) begin() {
	a.mu.Lock()
	a.active++
	if a.timer != nil {
		a.timer.Stop()
	}
	a.mu.Unlock()
}

func (a *Adapter) end() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.active--
	if a.active > 0 || a.idle < 0 || a.closed {
		return
	}
	if a.timer != nil {
		a.timer.Stop()
	}
	a.timer = time.AfterFunc(a.idle, a.idleStop)
}

func (a *Adapter) idleStop() {
	a.mu.Lock()
	if a.active > 0 || a.p == nil {
		a.mu.Unlock()
		return
	}
	p := a.p
	a.p = nil
	a.mu.Unlock()
	p.kill()
}

func (a *Adapter) noteQuota(err error, model string) {
	le, ok := err.(*llm.Error)
	if ok && le.Kind == llm.ErrModelUnavailable {
		a.markUnsupported(model)
	}
	if !ok || le.Kind != llm.ErrQuotaExhausted {
		return
	}
	a.mu.Lock()
	a.quota = llm.Quota{Known: true, Remaining: 0, ResetsAt: le.ResetsAt}
	a.mu.Unlock()
}

// Stream runs one ACP prompt turn.
func (a *Adapter) Stream(ctx context.Context, req llm.Request, onChunk func(llm.Chunk) error) (llm.Response, error) {
	if len(req.Messages) == 0 {
		return llm.Response{}, &llm.Error{Kind: llm.ErrBadRequest, Provider: a.id, Message: "no messages"}
	}
	a.begin()
	defer a.end()
	p, err := a.ensureProc(ctx)
	if err != nil {
		return llm.Response{}, err
	}

	prints := fingerprints(req.Messages)
	cwd := a.cfg.Cwd
	if req.Workspace != "" {
		cwd = req.Workspace
	}
	own := req.OwnWorkspace && req.Workspace != ""
	key := convKey(req, cwd)
	var moved []string
	if own {
		// CLI config in an agent's workspace was written by an agent: move
		// it aside, and start over if a session may have loaded it
		var qerr error
		moved, qerr = agent.QuarantineCLIConfig(cwd, time.Now().UTC().Format("20060102T150405.000000000"))
		if qerr != nil {
			return llm.Response{}, &llm.Error{Kind: llm.ErrUnavailable, Provider: a.id,
				Message: "cannot move CLI configuration out of the agent workspace: " + qerr.Error()}
		}
		if len(moved) > 0 {
			a.forget(key)
		}
	}
	c, prompt := a.claim(key, p, prints, req)
	defer a.release(key, c)
	if c.s == nil {
		var bind *MCPBinding
		if req.Agent != "" && req.Session != "" {
			bind = &MCPBinding{Agent: req.Agent, Session: req.Session, Workspace: req.Workspace, Mode: req.Mode, Own: own}
		}
		s, err := p.newSession(ctx, req.Model, req.Effort, cwd, own, bind)
		if err != nil {
			a.noteQuota(err, req.Model)
			return llm.Response{}, err
		}
		c.s = s
	} else if err := p.selectModel(ctx, c.s, req.Model, req.Effort); err != nil {
		// another model or level in the same conversation
		c.prints = nil
		return llm.Response{}, err
	}
	a.noteSession(req.Agent, req.Session, c.s)

	t := newTurn(a.cfg.Name, a.toolNotes, onChunk, func() {
		_ = p.conn.Notify(mSessionCancel, map[string]string{"sessionId": c.s.id})
	})
	t.ctx, t.agent, t.session = ctx, req.Agent, req.Session
	c.s.setTurn(t)
	defer c.s.setTurn(nil)
	if g := c.s.grant; g != nil {
		// MCP calls run while this turn does; cancelling it cancels them
		end := g.Turn(ctx, req.Mode)
		defer end()
	}
	if len(moved) > 0 {
		t.note(fmt.Sprintf("[Crewbase moved CLI configuration out of the agent workspace into .crewbase-quarantine/: %s]", strings.Join(moved, ", ")))
	}

	type callRes struct {
		res promptResult
		err error
	}
	callCtx, callCancel := context.WithCancel(context.Background())
	defer callCancel()
	done := make(chan callRes, 1)
	go func() {
		var r promptResult
		err := p.conn.Call(callCtx, mSessionPrompt, promptParams{SessionID: c.s.id, Prompt: []contentBlock{{Type: "text", Text: prompt}}}, &r)
		done <- callRes{r, err}
	}()

	var r callRes
	select {
	case r = <-done:
	case <-ctx.Done():
		t.markCancelled()
		_ = p.conn.Notify(mSessionCancel, map[string]string{"sessionId": c.s.id})
		select {
		case <-done:
		case <-time.After(a.cancelGrace):
			// agent ignored the cancel: its session state is unknown
			c.prints = nil
		}
		c.s.setTurn(nil)
		c.prints = nil // the turn's reply is not recorded by Crewbase
		return llm.Response{}, ctx.Err()
	}
	if ch := t.changedFiles(); len(ch) > 0 && a.filesChanged != nil {
		a.filesChanged(req.Agent, req.Session, ch)
	}
	msg, used, chunkErr := t.result()
	if chunkErr != nil {
		c.prints = nil
		return llm.Response{}, chunkErr
	}
	if r.err != nil {
		c.prints = nil
		err := p.classify(r.err, "")
		a.noteQuota(err, req.Model)
		return llm.Response{}, err
	}
	// Some CLIs report a limit as an ordinary reply (e.g. Claude Code's
	// "Claude AI usage limit reached|<epoch>").
	if txt := strings.TrimSpace(msg.Text()); len(txt) < 400 && reLimitBanner.MatchString(txt) {
		c.prints = nil
		retry, resets := resetHints(txt, time.Now())
		err := &llm.Error{Kind: llm.ErrQuotaExhausted, Provider: a.id, Message: txt, RetryAfter: retry, ResetsAt: resets}
		a.noteQuota(err, req.Model)
		return llm.Response{}, err
	}
	// ...or say the account is suspended (Antigravity: "This service has
	// been disabled in this account for violation of Terms of Service").
	if txt := strings.TrimSpace(msg.Text()); len(txt) < 400 && reAccountBanner.MatchString(txt) {
		c.prints = nil
		err := &llm.Error{Kind: llm.ErrAuth, Provider: a.id, Message: strings.TrimLeft(txt, "⚠ \n")}
		a.noteAccount(err)
		return llm.Response{}, err
	}

	stop := "end"
	switch r.res.StopReason {
	case "max_tokens":
		stop = "max_tokens"
	case "cancelled":
		if ctx.Err() != nil {
			return llm.Response{}, ctx.Err()
		}
		// cancelled by the agent itself (e.g. user pressed stop in the CLI)
		c.prints = nil
		return llm.Response{}, &llm.Error{Kind: llm.ErrUnavailable, Provider: a.id, Message: "agent cancelled the turn"}
	}
	c.prints = append(prints, fingerprint(msg))
	a.mu.Lock()
	if a.quota.Known && a.quota.Remaining == 0 {
		a.quota = llm.Quota{} // worked again: back to unknown
		a.accountErr = ""
	}
	a.mu.Unlock()

	out := llm.EstimateTokens(msg.Text())
	in := llm.EstimateTokens(prompt)
	if used > 0 {
		in = max(used-out, in)
	}
	return llm.Response{Message: msg, Usage: llm.Usage{InputTokens: in, OutputTokens: out, Context: used}, StopReason: stop}, nil
}

// claim finds a reusable session for the conversation or prepares a new one,
// and returns the prompt text to send.
func (a *Adapter) claim(key string, p *proc, prints []string, req llm.Request) (*conv, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if c := a.convs[key]; c != nil && !c.busy && c.p == p && c.s != nil && isPrefix(c.prints, prints) && len(prints) > len(c.prints) &&
		(c.s.grant == nil || !c.s.grant.Stale()) {
		c.busy = true
		c.lastUse = time.Now()
		return c, renderDelta(req.Messages[len(c.prints):])
	}
	c := &conv{p: p, busy: true, lastUse: time.Now()}
	if old := a.convs[key]; old == nil || !old.busy {
		if old != nil {
			old.drop()
		}
		a.convs[key] = c
	}
	a.gcLocked()
	return c, renderFresh(req)
}

// forget drops the idle conversation of key, so the next turn starts a new
// ACP session.
func (a *Adapter) forget(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if c := a.convs[key]; c != nil && !c.busy {
		delete(a.convs, key)
		c.drop()
	}
}

func (a *Adapter) release(key string, c *conv) {
	a.mu.Lock()
	defer a.mu.Unlock()
	c.busy = false
	switch {
	case a.convs[key] != c:
		c.drop() // a concurrent one-off conversation: its session is not kept
	case c.prints == nil:
		delete(a.convs, key)
		c.drop()
	}
}

// drop ends the conversation's ACP session for Crewbase: its MCP grant is
// revoked (ACP v1 has no session close; the CLI keeps it until exit).
func (c *conv) drop() {
	if c.s != nil {
		c.p.dropSession(c.s)
	}
}

// gcLocked bounds the conversation table.
func (a *Adapter) gcLocked() {
	const maxConvs = 64
	for len(a.convs) > maxConvs {
		var oldK string
		var oldT time.Time
		for k, c := range a.convs {
			if !c.busy && (oldK == "" || c.lastUse.Before(oldT)) {
				oldK, oldT = k, c.lastUse
			}
		}
		if oldK == "" {
			return
		}
		a.convs[oldK].drop()
		delete(a.convs, oldK)
	}
}

func isPrefix(prefix, all []string) bool {
	if len(prefix) == 0 || len(prefix) > len(all) {
		return false
	}
	for i := range prefix {
		if prefix[i] != all[i] {
			return false
		}
	}
	return true
}

// convKey identifies a conversation: the asking agent and Crewbase session
// when known, else system prompt + first message. The session cwd is part
// of the key so a changed workspace starts a new ACP session.
func convKey(req llm.Request, cwd string) string {
	if req.Agent != "" || req.Session != "" {
		return "s\x00" + req.Agent + "\x00" + req.Session + "\x00" + cwd
	}
	h := sha256.New()
	h.Write([]byte(cwd))
	h.Write([]byte{0})
	h.Write([]byte(req.System))
	h.Write([]byte{0})
	h.Write([]byte(fingerprint(req.Messages[0])))
	return hex.EncodeToString(h.Sum(nil))
}

// fingerprint hashes the model-visible content of a message. Only text,
// tool calls and tool results count: thinking parts (and anything else this
// bridge does not render) are ignored, whether they carry another
// provider's Meta or were dropped on replay.
func fingerprint(m llm.Message) string {
	h := sha256.New()
	h.Write([]byte(m.Role))
	for _, p := range m.Parts {
		switch p.Type {
		case "text", "tool_call", "tool_result", llm.PartAttachment:
		default:
			continue
		}
		h.Write([]byte{0})
		h.Write([]byte(p.Type))
		h.Write([]byte{0})
		h.Write([]byte(p.Text))
		if p.Attachment != nil {
			fmt.Fprintf(h, "\x00%s", p.Attachment.Path)
		}
		if p.ToolCall != nil {
			fmt.Fprintf(h, "\x00%s\x00%s\x00%s", p.ToolCall.ID, p.ToolCall.Name, p.ToolCall.Args)
		}
		if p.ToolResult != nil {
			fmt.Fprintf(h, "\x00%s\x00%s\x00%t", p.ToolResult.CallID, p.ToolResult.Content, p.ToolResult.IsError)
		}
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

func fingerprints(ms []llm.Message) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = fingerprint(m)
	}
	return out
}

// renderMessage renders a message as plain text for an ACP prompt.
func renderMessage(m llm.Message) string {
	var b strings.Builder
	for _, p := range m.Parts {
		switch p.Type {
		case "text":
			b.WriteString(p.Text)
		case llm.PartAttachment:
			// The CLI agent works in the workspace and reads files with its
			// own tools; it gets the path, not the contents.
			if at := p.Attachment; at != nil {
				fmt.Fprintf(&b, "\n[attached file %q (%s) is in the working directory at %s]\n", at.Name, at.Mime, at.Path)
			}
		case "tool_call":
			if p.ToolCall != nil {
				fmt.Fprintf(&b, "\n[called tool %s with %s]\n", p.ToolCall.Name, p.ToolCall.Args)
			}
		case "tool_result":
			if r := p.ToolResult; r != nil {
				status := "result"
				if r.IsError {
					status = "error"
				}
				fmt.Fprintf(&b, "[tool %s %s]\n%s", r.Name, status, r.Content)
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func roleLabel(r llm.Role) string {
	switch r {
	case llm.RoleAssistant:
		return "Assistant"
	case llm.RoleTool:
		return "Tool"
	}
	return "User"
}

func renderTranscript(ms []llm.Message) string {
	var b strings.Builder
	for _, m := range ms {
		t := renderMessage(m)
		if t == "" {
			continue
		}
		fmt.Fprintf(&b, "<%s>\n%s\n</%s>\n", strings.ToLower(roleLabel(m.Role)), t, strings.ToLower(roleLabel(m.Role)))
	}
	return b.String()
}

// renderFresh builds the first prompt of a new ACP session: Crewbase's
// system prompt and earlier messages (so a model switch loses nothing),
// then the latest message.
func renderFresh(req llm.Request) string {
	last := req.Messages[len(req.Messages)-1]
	prior := req.Messages[:len(req.Messages)-1]
	if req.System == "" && len(prior) == 0 && last.Role == llm.RoleUser {
		return renderMessage(last)
	}
	var b strings.Builder
	if req.System != "" {
		b.WriteString("<instructions>\n" + strings.TrimSpace(req.System) + "\n</instructions>\n\n")
	}
	if len(prior) > 0 {
		b.WriteString("Conversation so far (earlier turns may have been answered by other assistants):\n")
		b.WriteString(renderTranscript(prior))
		b.WriteString("\n")
	}
	if last.Role == llm.RoleUser {
		b.WriteString("Current message:\n" + renderMessage(last))
	} else {
		b.WriteString(renderTranscript([]llm.Message{last}))
		b.WriteString("\nContinue.")
	}
	return b.String()
}

// renderDelta renders messages appended since the session's last turn.
func renderDelta(ms []llm.Message) string {
	if len(ms) == 1 && ms[0].Role == llm.RoleUser {
		return renderMessage(ms[0])
	}
	var b strings.Builder
	b.WriteString("New messages since your last reply:\n")
	b.WriteString(renderTranscript(ms))
	if ms[len(ms)-1].Role != llm.RoleUser {
		b.WriteString("\nContinue.")
	}
	return b.String()
}

// markUnsupported records that this account cannot use model (the CLI
// refused it for the plan): it is listed switched off from now on, and
// the choice is kept with the model cache until the list is learned again.
func (a *Adapter) markUnsupported(model string) {
	if model == "" || model == defaultModel {
		return
	}
	a.mu.Lock()
	if a.unsupported[model] {
		a.mu.Unlock()
		return
	}
	if a.unsupported == nil {
		a.unsupported = map[string]bool{}
	}
	a.unsupported[model] = true
	a.mu.Unlock()
	a.saveModels()
}

// noteAccount keeps an account-level refusal so the provider shows it as
// its error until a turn succeeds again.
func (a *Adapter) noteAccount(err *llm.Error) {
	a.mu.Lock()
	a.accountErr = err.Message
	a.mu.Unlock()
}

// AccountError is the latest account-level refusal, "" when none.
func (a *Adapter) AccountError() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.accountErr
}
