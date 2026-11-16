// Package computertools is the consumer side of the `computer` capability
// (docs/computer-use.md): the model-facing computer_* tools and their
// policy (plugin @crewbase/computer-tools), and the settings / stop API for
// people (plugin @crewbase/computer-api, api.go).
//
// Policy, in the order a call meets it:
//
//   - the master switch (default off, Settings › 电脑操作): while off the
//     tools are not registered at all;
//   - stop: a per-agent or global stop cancels what runs and refuses new
//     calls until resumed or until the person's next message;
//   - browsers are refused with a pointer to the browser_* tools;
//   - the default block list (keychain and password managers, terminals,
//     System Settings, Crewbase, banks / brokers / crypto) and apps the
//     person blocked are refused, never asked about;
//   - read-only agents may only list and observe;
//   - per agent × app permission, keyed by bundle id, in the rules engine
//     as category "computer:<bundle id>": block (an agent rule), allow (a
//     remembered choice) or ask (the default). Asking happens once per
//     agent, app and conversation; "以后都允许" remembers it;
//   - consequential actions (send / delete / pay buttons and menu items,
//     return in a mail or chat text field) are confirmed every time, never
//     remembered, as categories publish / delete / payment;
//   - typing into secure (password) fields is refused;
//   - foreground actions (coordinates) wait until the person has not
//     touched the keyboard or mouse for 2s, up to 15s, also between the
//     steps of a batch;
//   - scripts and shortcuts (computer_script / computer_shortcut, when the
//     provider is a computer.Scripter) are confirmed every time, never
//     remembered, and refused for blocked apps and shell escapes
//     (scripts.go).
//
// On-screen text reaches the model inside an untrusted <screen> block.
// Every action is logged as a computer/action session event (no image
// bytes); screenshots the model asked for are saved in the agent
// workspace (generated/) and referenced by path. Who operates which app
// right now is published as computer/state (SSE "computer.state").
package computertools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/capabilities/computer"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/media"
	"github.com/hedeqiang/crewbase/features/rules"
	"github.com/hedeqiang/crewbase/features/toolsbasic"
	"github.com/hedeqiang/crewbase/kernel"
)

// ServiceKey is the key of the computer-tools service (*Service).
const ServiceKey = "computer-tools"

// Tool names.
const (
	ToolApps    = "computer_apps"
	ToolObserve = "computer_observe"
	ToolAct     = "computer_act"
	ToolBatch   = "computer_act_batch"
	ToolOpen    = "computer_open"
)

// EventAction is the session event logged for every observation and action.
const EventAction = "computer/action"

// CategoryPrefix + bundle id is the rules-engine category of operating an app.
const CategoryPrefix = "computer:"

// Category is the rules category of operating app (a bundle id).
func Category(app string) string { return CategoryPrefix + app }

// Messages the model sees.
const (
	BusyMessage    = "the user is using the computer; try again shortly"
	StoppedMessage = "the user stopped computer use. Do not retry: tell them in one line what you were doing and wait for their next message"
	OffMessage     = "computer use is turned off; the user can turn it on in Settings › 电脑操作"
)

// Permission values in settings.
const (
	PermAllow = "allow"
	PermAsk   = "ask"
	PermBlock = "block"
)

// ActionLog is the data of a computer/action session event.
type ActionLog struct {
	Call        string   `json:"call,omitempty"`
	Agent       string   `json:"agent"`
	App         string   `json:"app"`
	AppName     string   `json:"appName,omitempty"`
	Action      string   `json:"action"` // observe | open | press | …
	Element     int      `json:"element,omitempty"`
	ElementName string   `json:"elementName,omitempty"`
	ElementRole string   `json:"elementRole,omitempty"`
	Keys        string   `json:"keys,omitempty"`
	Menu        []string `json:"menu,omitempty"`
	Text        string   `json:"text,omitempty"` // typed text, shortened
	Foreground  bool     `json:"foreground,omitempty"`
	Step        int      `json:"step,omitempty"` // 1-based, in a batch
	Steps       int      `json:"steps,omitempty"`
	OK          bool     `json:"ok"`
	Done        bool     `json:"done,omitempty"`
	Note        string   `json:"note,omitempty"`
	Error       string   `json:"error,omitempty"`
	Screenshot  string   `json:"screenshot,omitempty"` // workspace path
}

// Live is one agent operating an app right now.
type Live struct {
	Agent      string `json:"agent"`
	App        string `json:"app"`
	AppName    string `json:"appName,omitempty"`
	Action     string `json:"action"`
	Foreground bool   `json:"foreground,omitempty"`
	// Paused: waiting for the person to stop using the keyboard / mouse.
	Paused bool      `json:"paused,omitempty"`
	Since  time.Time `json:"since"`
	// Window is the target window on screen (points, from the agent's
	// latest observation), for the desktop shell's border; nil = unknown.
	Window *computer.Rect `json:"window,omitempty"`
}

// State is what the desktop shell needs for its indicator: who operates
// what, and what is stopped.
type State struct {
	Live       []Live   `json:"live"`
	StoppedAll bool     `json:"stoppedAll"`
	Stopped    []string `json:"stopped"`
}

// Changed is emitted whenever State changes.
var Changed = kernel.NewEmit[State]("computer/state")

// Config is the plugin config.
type Config struct {
	// Dir holds computer.json (master switch, block list changes); empty
	// keeps it in memory.
	Dir string `json:"dir"`
	// MaxOutlineChars caps the outline given to the model (default 12000).
	MaxOutlineChars int `json:"maxOutlineChars,omitempty"`
	// MaxNodes caps the provider's outline (0 = provider default).
	MaxNodes int `json:"maxNodes,omitempty"`
	// ScreenshotEdge is the long edge of screenshots in pixels (default 1280).
	ScreenshotEdge int `json:"screenshotEdge,omitempty"`
	// ActionTimeout bounds one call (default 60s).
	ActionTimeout string `json:"actionTimeout,omitempty"`
	// IdleNeed is how long the person must not have touched the machine
	// before a foreground action (default 2s); IdleWait how long to wait
	// for that (default 15s).
	IdleNeed string `json:"idleNeed,omitempty"`
	IdleWait string `json:"idleWait,omitempty"`
}

// persisted is computer.json.
type persisted struct {
	Version int  `json:"version"`
	Enabled bool `json:"enabled"`
	// Unblocked are default-blocked apps the person allowed (entry keys:
	// a bundle id, or "name:<name>").
	Unblocked []string `json:"unblocked,omitempty"`
	// Blocked are further apps blocked for every agent (bundle ids).
	Blocked []string `json:"blocked,omitempty"`
	// Names remembers app names by bundle id, for settings.
	Names map[string]string `json:"names,omitempty"`
}

type pendingAsk struct {
	agent, session, cat string
	remember            bool // a remembered "以后都允许" turns into the app's allow
}

// Service is the computer-tools service: the policy and the tools.
type Service struct {
	c     *kernel.Context
	comp  computer.Computer
	reg   *tools.Registry
	rules *rules.Engine
	path  string

	maxChars int
	maxNodes int
	edge     int
	timeout  time.Duration
	idleNeed time.Duration
	idleWait time.Duration
	poll     time.Duration

	mu       sync.Mutex
	st       persisted
	toolsCtx *kernel.Context
	obs      map[string]map[string]*computer.Observation // agent → app id → latest outline
	apps     []computer.App
	appsAt   time.Time
	stopAll  bool
	stopped  map[string]bool
	inflight map[string]map[int]context.CancelFunc // agent → running calls
	seq      int
	live     map[string]Live // agent → what it does now
	pending  map[string]pendingAsk
	native   map[string]*nativeState // agent → the native computer tool's target
}

// errors of the tool layer
var (
	errBusy    = errors.New(BusyMessage)
	errStopped = errors.New(StoppedMessage)
)

func newService(c *kernel.Context, comp computer.Computer, reg *tools.Registry, eng *rules.Engine, cfg Config) (*Service, error) {
	s := &Service{c: c, comp: comp, reg: reg, rules: eng,
		maxChars: cfg.MaxOutlineChars, maxNodes: cfg.MaxNodes, edge: cfg.ScreenshotEdge,
		timeout: 60 * time.Second, idleNeed: 2 * time.Second, idleWait: 15 * time.Second, poll: 200 * time.Millisecond,
		obs: map[string]map[string]*computer.Observation{}, stopped: map[string]bool{},
		inflight: map[string]map[int]context.CancelFunc{}, live: map[string]Live{}, pending: map[string]pendingAsk{},
		st: persisted{Version: 1, Names: map[string]string{}},
	}
	if s.maxChars <= 0 {
		s.maxChars = 12000
	}
	if s.edge <= 0 {
		s.edge = 1280
	}
	for _, d := range []struct {
		raw string
		dst *time.Duration
		key string
	}{{cfg.ActionTimeout, &s.timeout, "actionTimeout"}, {cfg.IdleNeed, &s.idleNeed, "idleNeed"}, {cfg.IdleWait, &s.idleWait, "idleWait"}} {
		if d.raw == "" {
			continue
		}
		v, err := time.ParseDuration(d.raw)
		if err != nil {
			return nil, fmt.Errorf("computer-tools: %s: %w", d.key, err)
		}
		*d.dst = v
	}
	if s.idleWait < time.Second {
		s.poll = 20 * time.Millisecond
	}
	if cfg.Dir != "" {
		if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
			return nil, err
		}
		s.path = filepath.Join(cfg.Dir, "computer.json")
		if b, err := os.ReadFile(s.path); err == nil {
			if err := json.Unmarshal(b, &s.st); err != nil {
				return nil, fmt.Errorf("computer-tools: %s: %w", s.path, err)
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		if s.st.Names == nil {
			s.st.Names = map[string]string{}
		}
	}
	return s, nil
}

func (s *Service) saveLocked() error {
	if s.path == "" {
		return nil
	}
	b, _ := json.MarshalIndent(s.st, "", "  ")
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// ---------------------------------------------------------------- switch

// Enabled reports the master switch.
func (s *Service) Enabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.Enabled
}

// SetEnabled turns computer use on or off. Off cancels what runs and
// removes the tools.
func (s *Service) SetEnabled(on bool) error {
	s.mu.Lock()
	if s.st.Enabled == on {
		s.mu.Unlock()
		return nil
	}
	s.st.Enabled = on
	err := s.saveLocked()
	s.mu.Unlock()
	s.syncTools()
	if !on {
		s.cancelAll()
	}
	s.changed()
	return err
}

// syncTools registers the tools while the switch is on.
func (s *Service) syncTools() {
	s.mu.Lock()
	on, cur := s.st.Enabled, s.toolsCtx
	if on && cur == nil {
		s.toolsCtx = s.c.Fork("")
		cur = s.toolsCtx
		s.mu.Unlock()
		for _, t := range s.tools() {
			s.reg.Register(cur, t)
		}
		return
	}
	if !on && cur != nil {
		s.toolsCtx = nil
		s.mu.Unlock()
		cur.Dispose()
		return
	}
	s.mu.Unlock()
}

// Permissions reports the system permissions.
func (s *Service) Permissions() computer.Permissions { return s.comp.Permissions() }

// ---------------------------------------------------------------- stop

// Stop stops agent's computer use: what runs is cancelled and new calls
// are refused until Resume or the person's next message to it.
func (s *Service) Stop(agentID string) {
	s.mu.Lock()
	s.stopped[agentID] = true
	cancels := s.cancelsLocked(agentID)
	s.mu.Unlock()
	for _, c := range cancels {
		c()
	}
	s.changed()
}

// StopAll stops every agent (the global hotkey): agents operating apps now
// stay stopped until resumed or messaged; the rest may continue once the
// person resumes or sends any message.
func (s *Service) StopAll() {
	s.mu.Lock()
	s.stopAll = true
	var cancels []context.CancelFunc
	for a := range s.inflight {
		s.stopped[a] = true
		cancels = append(cancels, s.cancelsLocked(a)...)
	}
	for a := range s.live {
		s.stopped[a] = true
	}
	s.mu.Unlock()
	for _, c := range cancels {
		c()
	}
	s.changed()
}

// Resume lifts agent's stop and the global stop ("" = every agent).
func (s *Service) Resume(agentID string) {
	s.mu.Lock()
	s.stopAll = false
	if agentID == "" {
		s.stopped = map[string]bool{}
	} else {
		delete(s.stopped, agentID)
	}
	s.mu.Unlock()
	s.changed()
}

// Stopped reports whether agent's computer use is stopped.
func (s *Service) Stopped(agentID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopAll || s.stopped[agentID]
}

func (s *Service) cancelsLocked(agentID string) []context.CancelFunc {
	var out []context.CancelFunc
	for _, c := range s.inflight[agentID] {
		out = append(out, c)
	}
	return out
}

func (s *Service) cancelAll() {
	s.mu.Lock()
	var cancels []context.CancelFunc
	for a := range s.inflight {
		cancels = append(cancels, s.cancelsLocked(a)...)
	}
	s.mu.Unlock()
	for _, c := range cancels {
		c()
	}
}

// track runs a call under a context Stop can cancel.
func (s *Service) track(ctx context.Context, agentID string) (context.Context, func()) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	s.mu.Lock()
	s.seq++
	id := s.seq
	if s.inflight[agentID] == nil {
		s.inflight[agentID] = map[int]context.CancelFunc{}
	}
	s.inflight[agentID][id] = cancel
	s.mu.Unlock()
	return ctx, func() {
		cancel()
		s.mu.Lock()
		delete(s.inflight[agentID], id)
		if len(s.inflight[agentID]) == 0 {
			delete(s.inflight, agentID)
		}
		s.mu.Unlock()
	}
}

// onUserMessage resumes the agents a person's message goes to.
func (s *Service) onUserMessage(a session.Appended) {
	if a.Event.Type != session.TypeUserMessage {
		return
	}
	var m session.UserMessage
	if a.Event.Decode(&m) != nil {
		return
	}
	targets := []string{}
	if m.To != "" {
		targets = append(targets, m.To)
	} else if st, ok := kernel.Lookup[*session.Store](s.c, session.ServiceKey); ok {
		if ss, err := st.Get(a.Session); err == nil {
			targets = ss.Meta().Agents
		}
	}
	s.mu.Lock()
	was := s.stopAll
	s.stopAll = false
	n := 0
	for _, t := range targets {
		if s.stopped[t] {
			delete(s.stopped, t)
			n++
		}
	}
	s.mu.Unlock()
	if was || n > 0 {
		s.changed()
	}
}

// ---------------------------------------------------------------- live state

// State reports who operates what now, and what is stopped.
func (s *Service) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateLocked()
}

func (s *Service) stateLocked() State {
	st := State{Live: []Live{}, Stopped: []string{}, StoppedAll: s.stopAll}
	for _, l := range s.live {
		st.Live = append(st.Live, l)
	}
	sort.Slice(st.Live, func(i, j int) bool { return st.Live[i].Agent < st.Live[j].Agent })
	for a := range s.stopped {
		st.Stopped = append(st.Stopped, a)
	}
	sort.Strings(st.Stopped)
	return st
}

func (s *Service) changed() {
	Changed.Emit(s.c, s.State())
}

func (s *Service) setLive(l Live) {
	s.mu.Lock()
	if cur, ok := s.live[l.Agent]; ok && cur.App == l.App {
		l.Since = cur.Since
	} else {
		l.Since = time.Now().UTC()
	}
	if o := s.obs[l.Agent][l.App]; l.Window == nil && o != nil && o.Window.Frame.W > 0 {
		f := o.Window.Frame
		l.Window = &f
	}
	s.live[l.Agent] = l
	s.mu.Unlock()
	s.changed()
}

func (s *Service) clearLive(agentID string) {
	s.mu.Lock()
	_, ok := s.live[agentID]
	delete(s.live, agentID)
	s.mu.Unlock()
	if ok {
		s.changed()
	}
}

// ---------------------------------------------------------------- apps

// runningApps lists apps, cached briefly (Classify and Execute both need it).
func (s *Service) runningApps(ctx context.Context, fresh bool) ([]computer.App, error) {
	s.mu.Lock()
	if !fresh && time.Since(s.appsAt) < 3*time.Second && s.apps != nil {
		out := s.apps
		s.mu.Unlock()
		return out, nil
	}
	s.mu.Unlock()
	apps, err := s.comp.Apps(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.apps, s.appsAt = apps, time.Now()
	dirty := false
	for _, a := range apps {
		if a.ID != "" && a.Name != "" && s.st.Names[a.ID] != a.Name {
			s.st.Names[a.ID] = a.Name
			dirty = true
		}
	}
	if dirty {
		_ = s.saveLocked()
	}
	s.mu.Unlock()
	return apps, nil
}

// resolve maps what the model wrote ("备忘录", "Notes", "com.apple.Notes")
// to an app: a running one first, then the known lists, then names seen
// before. running is false for apps that are not running.
func (s *Service) resolve(ctx context.Context, name string) (app computer.App, running bool) {
	name = strings.TrimSpace(name)
	apps, _ := s.runningApps(ctx, false)
	for _, a := range apps {
		if strings.EqualFold(a.ID, name) {
			return a, true
		}
	}
	for _, a := range apps {
		if strings.EqualFold(a.Name, name) {
			return a, true
		}
	}
	for _, list := range [][]known{blockedByDefault, browsers, messaging} {
		if k, ok := find(list, name, name); ok && len(k.IDs) > 0 {
			return computer.App{ID: k.IDs[0], Name: name}, false
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, n := range s.st.Names {
		if strings.EqualFold(n, name) {
			return computer.App{ID: id, Name: n}, false
		}
	}
	if n := s.st.Names[name]; n != "" {
		return computer.App{ID: name, Name: n}, false
	}
	return computer.App{ID: name, Name: name}, false
}

// blockedWhy reports why an app is blocked for everyone ("" = it is not).
func (s *Service) blockedWhy(app computer.App) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.st.Blocked {
		if strings.EqualFold(b, app.ID) {
			return "the user blocked it in Settings › 电脑操作"
		}
	}
	group, ok := defaultBlocked(app.ID, app.Name)
	if !ok {
		return ""
	}
	key := entryKey(app.ID, app.Name)
	for _, u := range s.st.Unblocked {
		if strings.EqualFold(u, key) {
			return ""
		}
	}
	switch group {
	case GroupPasswords:
		return "it holds passwords; agents never operate it"
	case GroupTerminal:
		return "it is a terminal; agents never operate one (use your own tools for commands)"
	case GroupSystem:
		return "System Settings changes security and privacy; agents never operate it"
	case GroupSelf:
		return "agents never operate Crewbase itself"
	case GroupBrowser:
		return "it is a web browser, blocked by default: use the browser_* tools for websites; if a site does not work there, ask the user to allow this browser in Settings › 电脑操作 (浏览器 in the blocked list), then try again"
	}
	return "it handles money (bank, broker or crypto); agents never operate it"
}

// permission is agent's effective permission for app, before the
// conversation grant: allow, ask or block.
func (s *Service) permission(agentID string, app computer.App) string {
	if s.blockedWhy(app) != "" {
		return PermBlock
	}
	v, _ := s.rules.Evaluate(agentID, Category(app.ID))
	switch v {
	case tools.Allow:
		return PermAllow
	case tools.Ask:
		return PermAsk
	}
	return PermBlock
}

// ---------------------------------------------------------------- observations

func (s *Service) remember(agentID string, o *computer.Observation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.obs[agentID] == nil {
		s.obs[agentID] = map[string]*computer.Observation{}
	}
	s.obs[agentID][o.App.ID] = o
	if o.App.Name != "" && s.st.Names[o.App.ID] != o.App.Name {
		s.st.Names[o.App.ID] = o.App.Name
		_ = s.saveLocked()
	}
}

func (s *Service) latest(agentID, app string) *computer.Observation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.obs[agentID][app]
}

func (s *Service) forget(agentID, app string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.obs[agentID], app)
}

// ---------------------------------------------------------------- policy

// isComputerTool reports whether name is one of ours.
func isComputerTool(name string) bool {
	switch name {
	case ToolApps, ToolObserve, ToolAct, ToolBatch, ToolOpen, ToolNative:
		return true
	}
	return isScriptTool(name)
}

// acts reports whether a tool changes an app (not allowed read-only).
func acts(name string) bool {
	return name == ToolAct || name == ToolBatch || name == ToolOpen || name == ToolNative
}

// middleware is the tools/pre-execute listener enforcing the policy. It is
// prepended, so it sees the decision after the rules engine and has the
// last word (it only ever tightens).
func (s *Service) middleware(ctx context.Context, d *tools.Decision, next kernel.Next[*tools.Decision, *tools.Decision]) (*tools.Decision, error) {
	if d.Call == nil || d.Tool == nil || !isComputerTool(d.Tool.Name) {
		return next(ctx, d)
	}
	call := d.Call
	if !s.Enabled() {
		return d.DenyWith(OffMessage), nil
	}
	if s.Stopped(call.Agent) {
		return d.DenyWith(StoppedMessage), nil
	}
	name := d.Tool.Name
	if isScriptTool(name) {
		return s.scriptPolicy(ctx, d, next) // scripts.go
	}
	if name == ToolApps {
		return next(ctx, d)
	}
	if acts(name) && toolsbasic.EffectiveMode(call.Mode) == agent.ModeReadOnly {
		return d.DenyWith("read-only mode: you may list and observe apps (computer_apps, computer_observe) but not operate them; ask the user to switch to sandbox mode"), nil
	}
	var app computer.App
	safety := false
	if name == ToolNative {
		var ok bool
		if app, ok = s.targetApp(ctx, call.Agent); !ok {
			return d.DenyWith(NoTargetMessage), nil
		}
		if c, err := decodeNative(*call); err == nil {
			safety = len(c.Safety) > 0
		}
	} else {
		app, _ = s.resolve(ctx, call.String("app"))
	}
	if app.ID == "" {
		return d.DenyWith("app is required"), nil
	}
	if why := s.blockedWhy(app); why != "" {
		return d.DenyWith(fmt.Sprintf("%s is blocked: %s. Do not try other ways to reach it; tell the user what you need.", app.Name, why)), nil
	}
	cat := Category(app.ID)
	sid := ""
	if call.Session != nil {
		sid = call.Session.ID()
	}
	perm := s.permission(call.Agent, app)
	if perm == PermBlock {
		return d.DenyWith(fmt.Sprintf("you are not allowed to operate %s (the user blocked it for you in Settings › 电脑操作)", app.Name)), nil
	}
	ask := perm == PermAsk && !(sid != "" && s.rules.SessionGranted(call.Agent, sid, cat))
	cons := rank(d.Category) > 0
	if cons || safety {
		d.Rememberable = false
	}
	out, err := next(ctx, d)
	if err != nil || out == nil || out.Verdict == tools.Deny {
		return out, err
	}
	switch {
	case safety:
		// the provider's own safety check: the person confirms every time,
		// and only then is the acknowledgement sent back
		out.RequireApproval("computer: the model provider asks the user to confirm this action")
		out.Rememberable, out.ConfirmRemember = false, false
		cons = true
	case cons:
		// confirmed every time, whatever was remembered for the category
		out.RequireApproval(fmt.Sprintf("computer: %s is confirmed every time", out.Category))
		out.Rememberable, out.ConfirmRemember = false, false
	case ask:
		verb := "操作"
		if name == ToolObserve {
			verb = "查看"
		}
		out.RequireApproval(fmt.Sprintf("computer: first time %s %s in this conversation", verb, app.Name))
		out.Rememberable = true
	}
	if out.Verdict == tools.Ask {
		s.mu.Lock()
		if len(s.pending) > 1024 {
			s.pending = map[string]pendingAsk{}
		}
		s.pending[call.ID] = pendingAsk{agent: call.Agent, session: sid, cat: cat, remember: !cons}
		s.mu.Unlock()
	}
	return out, nil
}

// onDecided turns an approved card into the app's conversation grant, and
// "以后都允许" into its remembered allow.
func (s *Service) onDecided(d approval.DecidedData) {
	s.mu.Lock()
	p, ok := s.pending[d.CallID]
	delete(s.pending, d.CallID)
	s.mu.Unlock()
	if !ok || d.Outcome != approval.OutcomeAllowedOnce {
		return
	}
	s.rules.GrantSession(p.agent, p.session, p.cat)
	if d.Remember && p.remember {
		if _, err := s.rules.Remember(p.agent, p.cat); err != nil {
			s.c.Log().Warn("computer-tools: remembering app permission failed", "err", err)
		}
	}
}

// SetPermission sets agent's permission for app (a bundle id).
func (s *Service) SetPermission(agentID, app, perm string) error {
	if agentID == "" || app == "" {
		return errors.New("agent and app are required")
	}
	cat := Category(app)
	switch perm {
	case PermAllow:
		if err := s.rules.SetAgentRule(agentID, cat, tools.Ask); err != nil {
			return err
		}
		if ok, err := s.rules.Remember(agentID, cat); err != nil {
			return err
		} else if !ok {
			return errors.New("this permission cannot be allowed (an administrator rule forbids it)")
		}
	case PermAsk:
		if err := s.rules.SetAgentRule(agentID, cat, tools.Ask); err != nil {
			return err
		}
		if err := s.rules.Revoke(agentID, cat); err != nil {
			return err
		}
		s.rules.RevokeSessions(agentID, cat)
	case PermBlock:
		if err := s.rules.Revoke(agentID, cat); err != nil {
			return err
		}
		s.rules.RevokeSessions(agentID, cat)
		if err := s.rules.SetAgentRule(agentID, cat, rules.Block); err != nil {
			return err
		}
	default:
		return fmt.Errorf("permission must be allow, ask or block, got %q", perm)
	}
	s.changed()
	return nil
}

// Permissions per agent for every app with a setting: agent → app → perm.
func (s *Service) AgentPermissions() map[string]map[string]string {
	out := map[string]map[string]string{}
	set := func(a, app, p string) {
		if out[a] == nil {
			out[a] = map[string]string{}
		}
		out[a][app] = p
	}
	for _, ch := range s.rules.Remembered() {
		if strings.HasPrefix(ch.Category, CategoryPrefix) && !ch.Overridden {
			set(ch.Agent, strings.TrimPrefix(ch.Category, CategoryPrefix), PermAllow)
		}
	}
	if reg, ok := kernel.Lookup[*agent.Registry](s.c, agent.ServiceKey); ok {
		for _, a := range reg.List() {
			for cat, v := range s.rules.AgentRules(a.ID()) {
				if strings.HasPrefix(cat, CategoryPrefix) && v == rules.Block {
					set(a.ID(), strings.TrimPrefix(cat, CategoryPrefix), PermBlock)
				}
			}
		}
	}
	return out
}

// SetBlocked blocks an app for every agent, or unblocks it (a default
// entry key or a bundle id).
func (s *Service) SetBlocked(key string, blocked bool) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("app is required")
	}
	name := strings.TrimPrefix(key, "name:")
	_, isDefault := defaultBlocked(key, name)
	if isDefault {
		key = entryKey(key, name)
	}
	without := func(xs []string, x string) []string {
		var out []string
		for _, y := range xs {
			if !strings.EqualFold(y, x) {
				out = append(out, y)
			}
		}
		return out
	}
	s.mu.Lock()
	s.st.Unblocked = without(s.st.Unblocked, key)
	s.st.Blocked = without(s.st.Blocked, key)
	switch {
	case blocked && !isDefault:
		s.st.Blocked = append(s.st.Blocked, key)
	case !blocked && isDefault:
		s.st.Unblocked = append(s.st.Unblocked, key)
	}
	err := s.saveLocked()
	s.mu.Unlock()
	if blocked {
		s.cancelAll() // an app just blocked may be in use
	}
	s.changed()
	return err
}

// BlockList is the block list for settings.
type BlockList struct {
	Default   []BlockedEntryState `json:"default"`
	User      []BlockedEntryState `json:"user"`
	Unblocked []string            `json:"unblocked"`
}

// BlockedEntryState is a block-list row and whether it applies.
type BlockedEntryState struct {
	BlockedEntry
	Key     string `json:"key"`
	Blocked bool   `json:"blocked"`
}

// Blocks lists the default and the person's block list.
func (s *Service) Blocks() BlockList {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := BlockList{Default: []BlockedEntryState{}, User: []BlockedEntryState{}, Unblocked: append([]string{}, s.st.Unblocked...)}
	un := map[string]bool{}
	for _, u := range s.st.Unblocked {
		un[strings.ToLower(u)] = true
	}
	for _, e := range DefaultBlocked() {
		key := e.ID
		if key == "" {
			key = "name:" + e.Name
		}
		if n := s.st.Names[e.ID]; n != "" {
			e.Name = n
		}
		out.Default = append(out.Default, BlockedEntryState{BlockedEntry: e, Key: key, Blocked: !un[strings.ToLower(key)]})
	}
	for _, id := range s.st.Blocked {
		n := s.st.Names[id]
		if n == "" {
			n = id
		}
		out.User = append(out.User, BlockedEntryState{BlockedEntry: BlockedEntry{ID: id, Name: n}, Key: id, Blocked: true})
	}
	return out
}

// Names returns the app names seen, by bundle id.
func (s *Service) Names() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.st.Names))
	for k, v := range s.st.Names {
		out[k] = v
	}
	return out
}

// ---------------------------------------------------------------- logging

func (s *Service) log(call tools.Call, l ActionLog) {
	l.Call, l.Agent = call.ID, call.Agent
	if call.Session != nil {
		if _, err := call.Session.Append(EventAction, call.Agent, l); err != nil {
			s.c.Log().Warn("computer-tools: action log failed", "err", err)
		}
	}
}

// saveShot writes a screenshot to the agent workspace (generated/).
func saveShot(workspace string, app computer.App, shot computer.Screenshot) (llm.Attachment, error) {
	if len(shot.PNG) == 0 {
		return llm.Attachment{}, errors.New("empty screenshot")
	}
	sv, err := media.Save(workspace, "image", "screenshot "+app.ID, bytes.NewReader(shot.PNG), media.MaxImageBytes, time.Now())
	if err != nil {
		return llm.Attachment{}, err
	}
	sum := sha256.Sum256(shot.PNG)
	return llm.Attachment{Path: sv.Path, Name: path.Base(sv.Path), Mime: sv.Mime, Size: sv.Size, SHA256: hex.EncodeToString(sum[:])}, nil
}

// ---------------------------------------------------------------- plugin

// Plugin registers the computer_* tools and their policy. Without a
// computer provider it stays pending: no tools, no API.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/computer-tools",
	Inject:      []string{tools.ServiceKey, computer.ServiceKey, rules.ServiceKey},
	Optional:    []string{agent.ServiceKey, session.ServiceKey, approval.ServiceKey},
	Description: "computer_apps / observe / act / act_batch / open (and script / shortcut / shortcuts where the provider runs scripts): operate native apps through the accessibility outline and their own scripting, with per-app permission, confirmations and stop.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		s, err := newService(c, kernel.Use[computer.Computer](c, computer.ServiceKey),
			kernel.Use[*tools.Registry](c, tools.ServiceKey), kernel.Use[*rules.Engine](c, rules.ServiceKey), cfg)
		if err != nil {
			return err
		}
		tools.PreExecute.Prepend(c, s.middleware)
		approval.Decided.Listen(c, s.onDecided)
		session.OnEvent.Listen(c, s.onUserMessage)
		agentloop.Request.Use(c, s.offerNative)
		s.syncTools()
		return c.Provide(ServiceKey, s)
	},
})
