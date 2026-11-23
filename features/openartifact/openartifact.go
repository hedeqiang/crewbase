// Package openartifact is @crewbase/open-artifact: Open Artifact
// (github.com/JimLiu/open-artifact) inside Crewbase. Agents make slides,
// designs, design systems, whiteboards, sheets, dashboards, docs and short
// films as plain folders of files; the person opens them in a real editor
// (in the 预览 panel), edits them, comments, exports, and "Send to
// Crewbase" brings the comments back into the conversation that made them.
//
// Crewbase is a harness of the Open Artifact daemon (one per computer,
// shared with the person's other agent apps), through its stdio bridge:
//
//   - tools: Artifact, ArtifactData, ArtifactComments, from the daemon,
//     registered as Crewbase tools (the "artifact" family). Their action
//     kinds pick the approval category; paths and the artifacts a call
//     touches are confined to the agent's workspace outside host mode.
//   - sessions: a conversation is attached when its agent first uses the
//     tools (one Open Artifact session per conversation and agent).
//   - wakes: what the owner sends from a page starts a turn; a
//     collaborator's comment or a finished task is a notice in the
//     conversation, never the person's words.
//   - pages: Artifact open shows the page in the 预览 panel.
//   - files the agent writes itself in an artifact's folder are reported,
//     so they are not taken for the person's edits.
//   - the skill (/open-artifact <type> …) is written to the skills folder.
package openartifact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

// ServiceKey is the key of the service (*Service).
const ServiceKey = "open-artifact"

// Harness is Crewbase's name with the daemon.
const Harness = "crewbase"

// Config of @crewbase/open-artifact.
type Config struct {
	Dir      string `json:"dir"`      // the data directory (runtime in <dir>/apps/open-artifact)
	Registry string `json:"registry"` // npm registry (default https://registry.npmjs.org)
	// Home is OPEN_ARTIFACT_HOME ("" = Open Artifact's own default, shared
	// with the person's other agent apps).
	Home string `json:"home"`
	Node string `json:"node"` // the node binary ("" = from PATH)
}

// state is kept in <dir>/apps/open-artifact/state.json.
type state struct {
	Enabled bool   `json:"enabled"`
	Version string `json:"version,omitempty"` // the runtime in use
	Skill   string `json:"skillEtag,omitempty"`
}

// Status is what the settings page shows.
type Status struct {
	Enabled   bool     `json:"enabled"`
	Installed []string `json:"installed"`
	Version   string   `json:"version,omitempty"` // in use
	Latest    string   `json:"latest,omitempty"`
	Node      NodeInfo `json:"node"`
	Running   bool     `json:"running"`
	Daemon    string   `json:"daemon,omitempty"` // the daemon's version
	Error     string   `json:"error,omitempty"`
	Home      string   `json:"home"`
	HomeURL   string   `json:"homeUrl,omitempty"` // the daemon's home page (the library)
	Tools     []string `json:"tools,omitempty"`
	Sessions  int      `json:"sessions"`
	Busy      string   `json:"busy,omitempty"` // installing | updating
}

// toolDef is a tool as tools.list gives it.
type toolDef struct {
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	InputSchema   map[string]any    `json:"inputSchema"`
	Annotations   map[string]any    `json:"annotations"`
	Actions       map[string]string `json:"actions"`
	RemoteActions []string          `json:"remoteActions"`
}

// att is an attached conversation.
type att struct {
	Session   string // Crewbase session
	Agent     string
	Workspace string
	Roots     []string // where its artifacts may be (workspace and held folders)
}

// Service is the Open Artifact integration.
type Service struct {
	c      *kernel.Context
	cfg    Config
	apps   string
	agents *agent.Registry
	reg    *tools.Registry
	client *http.Client

	mu       sync.Mutex
	st       state
	conn     *conn
	status   Status
	toolCtx  *kernel.Context
	toolSig  string
	attached map[string]att    // key → conversation
	roots    map[string]string // artifact url → its folder
	kick     chan struct{}
}

func newService(c *kernel.Context, cfg Config, agents *agent.Registry, reg *tools.Registry) (*Service, error) {
	if cfg.Registry == "" {
		cfg.Registry = "https://registry.npmjs.org"
	}
	s := &Service{c: c, cfg: cfg, apps: filepath.Join(cfg.Dir, "apps", "open-artifact"), agents: agents, reg: reg,
		client: &http.Client{Timeout: 10 * time.Minute}, attached: map[string]att{}, roots: map[string]string{}, kick: make(chan struct{}, 1)}
	if err := os.MkdirAll(s.apps, 0o755); err != nil {
		return nil, err
	}
	if b, err := os.ReadFile(filepath.Join(s.apps, "state.json")); err == nil {
		_ = json.Unmarshal(b, &s.st)
	}
	return s, nil
}

func (s *Service) saveLocked() error {
	b, _ := json.MarshalIndent(s.st, "", "  ")
	tmp := filepath.Join(s.apps, "state.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.apps, "state.json"))
}

func (s *Service) wake() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// home is OPEN_ARTIFACT_HOME as the daemon will use it.
func (s *Service) home() string {
	if s.cfg.Home != "" {
		return s.cfg.Home
	}
	if h := os.Getenv("OPEN_ARTIFACT_HOME"); h != "" {
		return h
	}
	d, _ := os.UserHomeDir()
	return filepath.Join(d, ".agents", "open-artifact")
}

// homeURL is the daemon's home page, from <home>/daemon.json (only the port
// is read; the token stays where it is).
func (s *Service) homeURL() string {
	b, err := os.ReadFile(filepath.Join(s.home(), "daemon.json"))
	if err != nil {
		return ""
	}
	var d struct {
		Port int `json:"port"`
	}
	if json.Unmarshal(b, &d) != nil || d.Port == 0 {
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:%d/", d.Port)
}

// Status is the current state.
func (s *Service) Status(ctx context.Context) Status {
	s.mu.Lock()
	st := s.status
	st.Enabled, st.Version = s.st.Enabled, s.st.Version
	st.Sessions = len(s.attached)
	s.mu.Unlock()
	st.Installed = installed(s.apps)
	if st.Installed == nil {
		st.Installed = []string{}
	}
	st.Node = findNode(ctx, s.cfg.Node)
	st.Home = s.home()
	if st.Running {
		st.HomeURL = s.homeURL()
	}
	return st
}

// Latest asks the registry for the newest version.
func (s *Service) Latest(ctx context.Context) (string, error) {
	rel, err := latest(ctx, s.client, s.cfg.Registry, "")
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.status.Latest = rel.Version
	s.mu.Unlock()
	return rel.Version, nil
}

// Install downloads a version ("" = the latest), switches to it and turns
// the integration on.
func (s *Service) Install(ctx context.Context, version string) (Status, error) {
	s.mu.Lock()
	if s.status.Busy != "" {
		s.mu.Unlock()
		return s.Status(ctx), errors.New("an install is already running")
	}
	s.status.Busy = "installing"
	s.mu.Unlock()
	err := s.install(ctx, version)
	s.mu.Lock()
	s.status.Busy = ""
	s.mu.Unlock()
	return s.Status(ctx), err
}

func (s *Service) install(ctx context.Context, version string) error {
	if n := findNode(ctx, s.cfg.Node); !n.OK {
		return errors.New(n.Problem)
	}
	rel, err := latest(ctx, s.client, s.cfg.Registry, version)
	if err != nil {
		return err
	}
	_, err = install(ctx, s.client, s.apps, rel)
	// the latest was published moments ago and is not downloadable yet: the
	// version before it now (it is offered as an update once it is there)
	for _, o := range rel.Older {
		if version != "" || !errors.Is(err, errNotYet) {
			break
		}
		if _, err = install(ctx, s.client, s.apps, o); err == nil {
			rel.Version = o.Version
		}
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.st.Version, s.st.Enabled = rel.Version, true
	s.status.Latest = ""
	err = s.saveLocked()
	s.mu.Unlock()
	s.prune(rel.Version)
	s.restart()
	return err
}

// prune keeps the version in use and the one before it.
func (s *Service) prune(keep string) {
	vs := installed(s.apps)
	kept := 0
	for _, v := range vs {
		if v == keep {
			continue
		}
		if kept < 1 {
			kept++
			continue
		}
		_ = os.RemoveAll(filepath.Join(s.apps, v))
	}
}

// SetEnabled turns the integration on or off.
func (s *Service) SetEnabled(on bool) error {
	s.mu.Lock()
	s.st.Enabled = on
	err := s.saveLocked()
	s.mu.Unlock()
	s.restart()
	return err
}

// restart ends the bridge; the loop starts it again when it should run.
func (s *Service) restart() {
	s.mu.Lock()
	c := s.conn
	s.mu.Unlock()
	if c != nil {
		go c.close()
	}
	s.wake()
}

// ---------------------------------------------------------------- the loop

func (s *Service) runtimeDir() string {
	s.mu.Lock()
	v := s.st.Version
	s.mu.Unlock()
	if v == "" {
		return ""
	}
	d := filepath.Join(s.apps, v)
	if ok, _ := runtimeAt(d); !ok {
		return ""
	}
	return d
}

func (s *Service) loop(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		s.mu.Lock()
		on := s.st.Enabled
		s.mu.Unlock()
		dir := s.runtimeDir()
		node := findNode(ctx, s.cfg.Node)
		if !on || dir == "" || !node.OK {
			if !on {
				s.dropTools()
			}
			s.setDown("")
			if on && dir != "" && !node.OK {
				s.setDown(node.Problem)
			}
			select {
			case <-ctx.Done():
				return
			case <-s.kick:
			case <-time.After(time.Minute):
			}
			continue
		}
		start := time.Now()
		err := s.session(ctx, node.Path, dir)
		s.setDown(errText(err))
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-s.kick:
			backoff = time.Second
		case <-time.After(backoff):
			backoff = min(backoff*2, time.Minute)
		}
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *Service) setDown(why string) {
	s.mu.Lock()
	s.conn = nil
	s.status.Running, s.status.Error = false, why
	s.mu.Unlock()
}

// session runs one bridge until it ends.
func (s *Service) session(ctx context.Context, node, dir string) error {
	env := os.Environ()
	if s.cfg.Home != "" {
		env = append(env, "OPEN_ARTIFACT_HOME="+s.cfg.Home)
	}
	logf, _ := os.OpenFile(filepath.Join(s.apps, "bridge.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	var errw io.Writer = io.Discard
	if logf != nil {
		defer logf.Close()
		errw = logf
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c, err := startConn(cctx, []string{node, filepath.Join(dir, "scripts", "open-artifact.mjs"), "harness", "--stdio", "--harness", Harness},
		env, s.apps, s.handle, s.notify, errw)
	if err != nil {
		return err
	}
	defer c.close()
	hctx, hcancel := context.WithTimeout(ctx, 2*time.Minute)
	defer hcancel()
	hello, err := c.call(hctx, "hello", s.helloParams(dir))
	if err != nil {
		return fmt.Errorf("hello: %w", err)
	}
	var h struct {
		Daemon string `json:"daemon"`
	}
	_ = json.Unmarshal(hello, &h)
	if err := s.loadTools(hctx, c); err != nil {
		return err
	}
	s.installSkill(hctx, c)
	s.mu.Lock()
	s.conn = c
	s.status.Running, s.status.Error, s.status.Daemon = true, "", h.Daemon
	again := make([]string, 0, len(s.attached))
	for k := range s.attached {
		again = append(again, k)
	}
	s.mu.Unlock()
	for _, k := range again { // a new bridge: attach our conversations again
		s.mu.Lock()
		a := s.attached[k]
		s.mu.Unlock()
		s.attach(hctx, c, k, a)
	}
	select {
	case <-ctx.Done():
		return nil
	case <-c.done:
		return c.err
	}
}

func (s *Service) helloParams(dir string) map[string]any {
	ver := ""
	if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		var p struct {
			Version string `json:"version"`
		}
		_ = json.Unmarshal(b, &p)
		ver = p.Version
	}
	// no origins declared: the page works in the 预览 frame as it is, and
	// declaring some would make the daemon refuse other apps' frames
	open := map[string]any{"embed": "frame", "remote": "deny"}
	return map[string]any{
		"harness": Harness,
		"caps": map[string]any{
			"agentName": "Crewbase",
			"tools":     "native",
			"wake":      map[string]any{"resume": true},
			"open":      open,
			"commands":  map[string]any{"names": []string{"open-artifact"}},
			"library":   "cards",
		},
		"profile": map[string]any{
			"title": "Crewbase",
			"rules": "In Crewbase the Artifact, ArtifactData and ArtifactComments tools load on demand: if they are not in your tools yet, find them with tool_search (query \"artifact\") first. " +
				"Keep artifacts inside your workspace (the default folder is). Pages open in the person's 预览 panel.",
		},
		"runtime": map[string]any{"dir": dir, "version": ver},
		"kit":     map[string]any{"name": "crewbase", "version": "1"},
	}
}

// ---------------------------------------------------------------- calls from the bridge

func (s *Service) handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "wake.deliver":
		return s.deliver(ctx, params), nil
	case "host.open":
		return s.hostOpen(params), nil
	case "shell.event":
		return map[string]any{"ok": true}, nil
	}
	return nil, fmt.Errorf("unknown method %s", method)
}

func (s *Service) notify(method string, params json.RawMessage) {
	if method != "bridge.reattached" {
		return
	}
	// a session attached again after the daemon restarted, with what waited
	var r struct {
		Params struct {
			Session string `json:"session"`
		} `json:"params"`
		Inbox []json.RawMessage `json:"inbox"`
	}
	if json.Unmarshal(params, &r) != nil {
		return
	}
	for _, m := range r.Inbox {
		s.deliverInbox(r.Params.Session, m)
	}
}

// key is the Open Artifact session of a conversation and agent.
func key(sessionID, agentID string) string { return sessionID + "~" + agentID }

func splitKey(k string) (string, string, bool) {
	i := strings.LastIndex(k, "~")
	if i <= 0 || i == len(k)-1 {
		return "", "", false
	}
	return k[:i], k[i+1:], true
}

// wakeParams is wake.deliver's payload (the parts read).
type wakeParams struct {
	Session    string `json:"session"`
	Artifact   string `json:"artifact"`
	ArtifactID string `json:"artifactId"`
	Text       string `json:"text"`
	Source     struct {
		Reason  string `json:"reason"`
		Summary string `json:"summary"`
		Title   string `json:"title"`
		ViewURL string `json:"viewUrl"`
		From    struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"from"`
	} `json:"source"`
}

func (s *Service) deliver(ctx context.Context, raw json.RawMessage) map[string]any {
	var w wakeParams
	if json.Unmarshal(raw, &w) != nil || w.Text == "" {
		return map[string]any{"status": "failed"}
	}
	sid, aid, ok := splitKey(w.Session)
	if !ok {
		return map[string]any{"status": "failed"}
	}
	a, ok := s.agents.Get(aid)
	if !ok {
		return map[string]any{"status": "failed"}
	}
	ss, err := s.agents.Sessions().Get(sid)
	if err != nil {
		return map[string]any{"status": "failed"}
	}
	s.mu.Lock()
	_, live := s.attached[w.Session]
	s.mu.Unlock()
	status := "delivered"
	if !live {
		status = "resumed" // a conversation from before a restart: it lives on in Crewbase
	}
	if w.Source.From.Kind == "owner" {
		// the person, on their own page: their message, a turn
		if _, err := a.Enqueue(agent.Input{Session: sid, Text: w.Text, From: "Open Artifact", Tag: "open-artifact:" + w.ArtifactID}); err != nil {
			return map[string]any{"status": "failed"}
		}
		return map[string]any{"status": status}
	}
	// a collaborator or a finished task: a notice the agent reads with its
	// next turn, never the person's words
	text := w.Text
	if w.Source.From.Kind != "system" {
		text = "[Notice from Open Artifact — not the person's own words]\n" + text
	}
	if err := a.Inject(ss, text, "open-artifact"); err != nil {
		return map[string]any{"status": "failed"}
	}
	return map[string]any{"status": status}
}

func (s *Service) deliverInbox(session string, m json.RawMessage) {
	var w map[string]any
	if json.Unmarshal(m, &w) != nil {
		return
	}
	if _, ok := w["session"]; !ok {
		w["session"] = session
	}
	b, _ := json.Marshal(w)
	s.deliver(context.Background(), b)
}

// hostOpen shows a page in the conversation's 预览 panel.
func (s *Service) hostOpen(raw json.RawMessage) map[string]any {
	var o struct {
		Session  string `json:"session"`
		URL      string `json:"url"`
		ViewURL  string `json:"view_url"`
		ViewURL2 string `json:"viewUrl"`
		Title    string `json:"title"`
	}
	if json.Unmarshal(raw, &o) != nil {
		return map[string]any{"opened": false}
	}
	view := o.ViewURL
	if view == "" {
		view = o.ViewURL2
	}
	sid, aid, ok := splitKey(o.Session)
	if !ok || !loopback(view) {
		return map[string]any{"opened": false}
	}
	ss, err := s.agents.Sessions().Get(sid)
	if err != nil {
		return map[string]any{"opened": false}
	}
	title := o.Title
	if title == "" {
		title = "Open Artifact"
	}
	if _, err := ss.Append(EventPreviewOpened, aid, map[string]any{"title": title, "url": view, "kind": "url", "agent": aid, "source": "open-artifact",
		"artifact": o.URL, "time": time.Now().UTC()}); err != nil {
		return map[string]any{"opened": false}
	}
	return map[string]any{"opened": true}
}

// EventPreviewOpened is features/preview's event (the 预览 panel opens it).
const EventPreviewOpened = "preview/opened"

// EventArtifact is a card in the conversation: an artifact a turn made or changed.
const EventArtifact = "open-artifact/artifact"

func loopback(u string) bool {
	return strings.HasPrefix(u, "http://127.0.0.1:") || strings.HasPrefix(u, "http://localhost:") || strings.HasPrefix(u, "http://[::1]:")
}

// ---------------------------------------------------------------- sessions

func (s *Service) current() (*conn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		if !s.st.Enabled {
			return nil, errors.New("Open Artifact is turned off (Settings › Open Artifact)")
		}
		if s.status.Error != "" {
			return nil, errors.New("Open Artifact is not running: " + s.status.Error)
		}
		return nil, errors.New("Open Artifact is starting; try again in a moment")
	}
	return s.conn, nil
}

// may limits what a conversation may do by its agent's mode.
func may(mode string, ephemeral bool) map[string]any {
	switch {
	case mode == agent.ModeReadOnly:
		return map[string]any{"create": false, "modify": false, "remove": false}
	case ephemeral:
		return map[string]any{"remove": false}
	}
	return nil
}

func (s *Service) attach(ctx context.Context, c *conn, k string, a att) error {
	p := map[string]any{"session": k, "workspace": a.Workspace, "status": "running"}
	if ag, ok := s.agents.Get(a.Agent); ok {
		sp := ag.Spec()
		p["title"] = sp.Name
		if m := may(ag.Mode(), sp.Ephemeral); m != nil {
			p["may"] = m
		}
		if sp.Ephemeral {
			p["role"] = "subagent"
		}
	}
	res, err := c.call(ctx, "session.attach", p)
	if err != nil {
		return err
	}
	var r struct {
		Inbox []json.RawMessage `json:"inbox"`
	}
	_ = json.Unmarshal(res, &r)
	for _, m := range r.Inbox {
		go s.deliverInbox(k, m)
	}
	return nil
}

// ensure attaches the call's conversation once.
func (s *Service) ensure(ctx context.Context, c *conn, call tools.Call) (string, error) {
	if call.Session == nil {
		return "", errors.New("Open Artifact tools work in a conversation")
	}
	k := key(call.Session.ID(), call.Agent)
	a := att{Session: call.Session.ID(), Agent: call.Agent, Workspace: call.Workspace, Roots: append([]string{call.Workspace}, call.Roots...)}
	s.mu.Lock()
	prev, ok := s.attached[k]
	s.attached[k] = a
	s.mu.Unlock()
	if ok && prev.Workspace == a.Workspace {
		return k, nil
	}
	if err := s.attach(ctx, c, k, a); err != nil {
		s.mu.Lock()
		delete(s.attached, k)
		s.mu.Unlock()
		return "", err
	}
	return k, nil
}

// turnStatus tells the daemon a conversation's turn started or ended.
func (s *Service) turnStatus(sessionID, agentID, status string) {
	k := key(sessionID, agentID)
	s.mu.Lock()
	a, ok := s.attached[k]
	c := s.conn
	s.mu.Unlock()
	if !ok || c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = c.call(ctx, "session.status", map[string]any{"session": k, "workspace": a.Workspace, "status": status})
}

// wrote reports files the agent wrote itself in its workspace.
func (s *Service) wrote(sessionID, agentID string, paths []string) {
	k := key(sessionID, agentID)
	s.mu.Lock()
	a, ok := s.attached[k]
	c := s.conn
	s.mu.Unlock()
	if !ok || c == nil || len(paths) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = c.call(ctx, "agent.wrote", map[string]any{"paths": paths, "caller": map[string]any{"session": k, "workspace": a.Workspace}})
}

// ---------------------------------------------------------------- skill

func (s *Service) skillsDir() string { return filepath.Join(s.cfg.Dir, "skills") }

// installSkill writes /open-artifact into the skills folder when it changed.
func (s *Service) installSkill(ctx context.Context, c *conn) {
	res, err := c.call(ctx, "skill.get", map[string]any{"command": "open-artifact", "agentName": "Crewbase", "tools": "native", "opens": "host"})
	if err != nil {
		return
	}
	var sk struct {
		Name         string `json:"name"`
		Description  string `json:"description"`
		ArgumentHint string `json:"argumentHint"`
		Body         string `json:"body"`
		Etag         string `json:"etag"`
	}
	if json.Unmarshal(res, &sk) != nil || sk.Body == "" {
		return
	}
	dir := filepath.Join(s.skillsDir(), "open-artifact")
	file := filepath.Join(dir, "SKILL.md")
	s.mu.Lock()
	same := s.st.Skill == sk.Etag && sk.Etag != ""
	s.mu.Unlock()
	if _, err := os.Stat(file); same && err == nil {
		return
	}
	// a SKILL.md there that we did not write stays
	if b, err := os.ReadFile(file); err == nil && !strings.Contains(string(b), "open-artifact-etag:") {
		return
	}
	desc := strings.ReplaceAll(sk.Description, "\n", " ")
	text := fmt.Sprintf("---\nname: open-artifact\ndescription: %s\nargument-hint: %s\nopen-artifact-etag: %s\n---\n\n%s\n", yamlString(desc), yamlString(sk.ArgumentHint), sk.Etag, sk.Body)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	if os.WriteFile(file, []byte(text), 0o644) == nil {
		s.mu.Lock()
		s.st.Skill = sk.Etag
		_ = s.saveLocked()
		s.mu.Unlock()
	}
}

// yamlString is v as a double-quoted YAML string (JSON's escapes are YAML's).
func yamlString(v string) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return strings.TrimSpace(b.String())
}

// ---------------------------------------------------------------- plugin

// Plugin is @crewbase/open-artifact.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/open-artifact",
	Inject:      []string{agent.ServiceKey, tools.ServiceKey},
	Description: "Open Artifact: agents make slides, designs, whiteboards, sheets, dashboards, docs and films the person edits, comments on and exports; comments come back to the conversation.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		if cfg.Dir == "" {
			if cfg.Dir, err = os.MkdirTemp("", "crewbase-oa-"); err != nil {
				return err
			}
		}
		s, err := newService(c, cfg, kernel.Use[*agent.Registry](c, agent.ServiceKey), kernel.Use[*tools.Registry](c, tools.ServiceKey))
		if err != nil {
			return err
		}
		if err := c.Provide(ServiceKey, s); err != nil {
			return err
		}
		s.hooks(c)
		c.Go(s.loop)
		return nil
	},
})
