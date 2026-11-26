package memory

// The memory panel (plugin @crewbase/memory-panel-api): one place for a
// person to see, search, correct and delete what agents know across
// conversations — every agent's MEMORY.md entries, every team's TEAM.md
// entries, and working memory (task progress files, reference notes).
//
// Nothing new is stored for an entry: the files stay the source of truth.
// Where an entry came from is found in the history index (the memory_add or
// team_memory_add call that wrote it); when it was last recalled is a small
// side file (.memory/used.json) written when memory_search returns it.
// Every change made here is written to the audit log.
//
//	GET  /api/memories             → {entries, agents, teams}
//	PUT  /api/memories             {kind, agent|team, line, raw | name, text}
//	POST /api/memories/delete      {kind, agent|team, line, raw | name}
//	POST /api/memories/wrong       {kind, agent|team, line, raw, correction, tell}

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/audit"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/features/teams"
	"github.com/hedeqiang/crewbase/kernel"
)

// Entry kinds in the panel.
const (
	KindMemory = "memory" // a line of an agent's MEMORY.md
	KindTeam   = "team"   // a line of a team's TEAM.md
	KindTask   = "task"   // a task progress file (working memory)
	KindNote   = "note"   // a reference note (working memory)
)

// Audit record types written by the panel.
const (
	AuditEdited  = "memory/edited"
	AuditDeleted = "memory/deleted"
	AuditFlagged = "memory/flagged"
	AuditAdded   = "memory/added"
)

// PanelEntry is one thing an agent (or a team) knows.
type PanelEntry struct {
	Kind    string `json:"kind"`
	Agent   string `json:"agent,omitempty"`
	Team    string `json:"team,omitempty"`
	Text    string `json:"text"`
	Section string `json:"section,omitempty"` // the heading above a MEMORY.md / TEAM.md line
	Name    string `json:"name,omitempty"`    // task or note file name
	Line    int    `json:"line,omitempty"`    // 1-based line in the file
	Raw     string `json:"raw,omitempty"`     // the line as written (checked on change)
	By      string `json:"by,omitempty"`      // who added a team memory line
	// Saved is when it was written (a day for dated lines, RFC 3339 when
	// known exactly); Used when memory_search last recalled it.
	Saved   string     `json:"saved,omitempty"`
	Used    *time.Time `json:"used,omitempty"`
	Session string     `json:"session,omitempty"` // the conversation it came from
	Size    int64      `json:"size,omitempty"`
}

// PanelAgent names an agent in the list.
type PanelAgent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Team string `json:"team"`
}

// ---------------------------------------------------------------- parsing

var (
	datedMem  = regexp.MustCompile(`^- (\d{4}-\d{2}-\d{2}): (.*)$`)
	datedTeam = regexp.MustCompile(`^- (\d{4}-\d{2}-\d{2}) (.*?)(?: \(([^()]{1,60})\))?$`)
)

// memLine is a parsed MEMORY.md / TEAM.md line.
type memLine struct {
	line          int
	raw, text     string
	date, by, sec string
}

// parseLines lists the entries of a memory file: every non-empty line that
// is not a heading, with the heading it sits under.
func parseLines(text string, team bool) []memLine {
	var out []memLine
	sec := ""
	for i, l := range strings.Split(text, "\n") {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "#") {
			h := strings.TrimSpace(strings.TrimLeft(t, "#"))
			if strings.EqualFold(h, "memory") || strings.EqualFold(h, "team memory") {
				h = ""
			}
			sec = h
			continue
		}
		m := memLine{line: i + 1, raw: l, sec: sec}
		if team {
			if g := datedTeam.FindStringSubmatch(t); g != nil {
				m.date, m.text, m.by = g[1], g[2], g[3]
			}
		} else if g := datedMem.FindStringSubmatch(t); g != nil {
			m.date, m.text = g[1], g[2]
		}
		if m.text == "" {
			m.text = strings.TrimSpace(strings.TrimLeft(t, "-*• "))
		}
		if m.text == "" {
			continue
		}
		out = append(out, m)
	}
	return out
}

func normText(s string) string { return strings.Join(strings.Fields(s), " ") }

// replaceText swaps the text of a line, keeping its bullet, date and author.
func replaceText(m memLine, text string, team bool) string {
	text = normText(text)
	switch {
	case m.date != "" && team:
		l := "- " + m.date + " " + text
		if m.by != "" {
			l += " (" + m.by + ")"
		}
		return l
	case m.date != "":
		return "- " + m.date + ": " + text
	case strings.HasPrefix(strings.TrimSpace(m.raw), "- "):
		return "- " + text
	}
	return text
}

// edit applies fn to the line (line, raw) of text; fn returns the new line,
// or "" with drop to remove it. The line must still read raw (it is found
// elsewhere in the file if lines moved), else the change is refused.
func edit(text string, line int, raw string, team bool, fn func(m memLine) (string, bool)) (string, memLine, error) {
	lines := strings.Split(text, "\n")
	idx := -1
	if line >= 1 && line <= len(lines) && lines[line-1] == raw {
		idx = line - 1
	} else {
		for i, l := range lines {
			if l == raw {
				idx = i
				break
			}
		}
	}
	if idx < 0 || strings.TrimSpace(raw) == "" {
		return "", memLine{}, gateway.Conflict("这条记忆已经变了，刷新后再试")
	}
	var m memLine
	for _, x := range parseLines(text, team) {
		if x.line == idx+1 {
			m = x
		}
	}
	if m.line == 0 {
		return "", memLine{}, gateway.Conflict("这一行不是一条记忆")
	}
	nl, drop := fn(m)
	if drop {
		lines = append(lines[:idx], lines[idx+1:]...)
	} else {
		lines[idx] = nl
	}
	return strings.Join(lines, "\n"), m, nil
}

// ---------------------------------------------------------------- recall

func usedPath(a *agent.Agent) string { return filepath.Join(memDir(a), "used.json") }

var usedMu sync.Mutex

func readUsed(a *agent.Agent) map[string]time.Time {
	out := map[string]time.Time{}
	b, err := os.ReadFile(usedPath(a))
	if err == nil {
		_ = json.Unmarshal(b, &out)
	}
	if out == nil { // a file holding null: callers write to the map
		out = map[string]time.Time{}
	}
	return out
}

// noteUsed records that memory_search returned these MEMORY.md lines.
func (s *Store) noteUsed(agentID string, hits []Hit) {
	a, ok := s.agents.Get(agentID)
	if !ok || len(hits) == 0 {
		return
	}
	lines := strings.Split(a.Memory(), "\n")
	byLine := map[int]memLine{}
	for _, m := range parseLines(strings.Join(lines, "\n"), false) {
		byLine[m.line] = m
	}
	usedMu.Lock()
	defer usedMu.Unlock()
	u := readUsed(a)
	now := time.Now().UTC()
	for _, h := range hits {
		if m, ok := byLine[int(h.Seq)]; ok && h.Kind == "memory" {
			u[normText(m.text)] = now
		}
	}
	// keep the file bounded: only entries still in MEMORY.md
	live := map[string]bool{}
	for _, m := range byLine {
		live[normText(m.text)] = true
	}
	for k := range u {
		if !live[k] {
			delete(u, k)
		}
	}
	b, _ := json.Marshal(u)
	_ = os.MkdirAll(memDir(a), 0o700)
	_ = writeAtomic(usedPath(a), b)
}

// origin is where an entry was written: the conversation and time of the
// memory_add / team_memory_add call.
type origin struct {
	session string
	time    string
}

// origins finds, in the history index, every memory_add and
// team_memory_add call: agent → text → origin (and team text → origin).
func (s *Store) origins() (mem map[string]map[string]origin, team map[string]origin) {
	mem, team = map[string]map[string]origin{}, map[string]origin{}
	rows, err := s.db.Query(`SELECT agent, session, time, text FROM docs WHERE docs MATCH '"memory add"' AND kind = 'message' AND role = 'tool_call' ORDER BY rowid`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var ag, sess, tm, text string
		if rows.Scan(&ag, &sess, &tm, &text) != nil {
			continue
		}
		name, args, _ := strings.Cut(text, " ")
		var x struct {
			Text string `json:"text"`
			Note string `json:"note"`
			// call_tool (a deferred tool called by name)
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		_ = json.Unmarshal([]byte(args), &x)
		if name == "call_tool" && len(x.Arguments) > 0 {
			name = x.Name
			_ = json.Unmarshal(x.Arguments, &x)
		}
		o := origin{session: sess, time: tm}
		switch name {
		case "memory_add":
			if t := normText(x.Text); t != "" {
				if mem[ag] == nil {
					mem[ag] = map[string]origin{}
				}
				mem[ag][t] = o
			}
		case "team_memory_add":
			if t := normText(x.Note); t != "" {
				team[t] = o
			}
		}
	}
	return
}

// ---------------------------------------------------------------- panel

type panel struct {
	c      *kernel.Context
	st     *Store
	agents *agent.Registry
}

func (p *panel) working() *Working {
	w, _ := kernel.Lookup[*Working](p.c, WorkingKey)
	return w
}

func (p *panel) teams() *teams.Service {
	t, _ := kernel.Lookup[*teams.Service](p.c, teams.ServiceKey)
	return t
}

func (p *panel) audit(r *gateway.Request, typ string, e PanelEntry, data map[string]any) {
	a, ok := kernel.Lookup[*audit.Service](p.c, audit.ServiceKey)
	if !ok {
		return
	}
	actor := "user"
	if r != nil && r.DeviceID != "" {
		actor = r.DeviceID
	}
	if data == nil {
		data = map[string]any{}
	}
	data["kind"] = e.Kind
	if e.Team != "" {
		data["team"] = e.Team
	}
	if e.Name != "" {
		data["name"] = e.Name
	}
	if _, err := a.Record(audit.Entry{Type: typ, Actor: actor, Agent: e.Agent, Data: data}); err != nil {
		p.c.Log().Error("audit write failed", "type", typ, "err", err)
	}
}

// listable agents keep memory: not blank chats or sub-agents.
func listable(a *agent.Agent) bool {
	sp := a.Spec()
	return !a.IsChat() && !sp.Ephemeral
}

func (p *panel) list() map[string]any {
	mem, teamOrigin := p.st.origins()
	w := p.working()
	var entries []PanelEntry
	var ags []PanelAgent
	teamSet := map[string]bool{}
	for _, a := range p.agents.List() {
		if !listable(a) {
			continue
		}
		sp := a.Spec()
		ags = append(ags, PanelAgent{ID: a.ID(), Name: sp.Name, Team: sp.TeamID()})
		teamSet[sp.TeamID()] = true
		usedMu.Lock()
		used := readUsed(a)
		moved := readMoved(a)
		usedMu.Unlock()
		for _, m := range parseLines(a.Memory(), false) {
			e := PanelEntry{Kind: KindMemory, Agent: a.ID(), Team: sp.TeamID(), Text: m.text, Section: m.sec, Line: m.line, Raw: m.raw, Saved: m.date}
			if o, ok := mem[a.ID()][normText(m.text)]; ok {
				e.Session, e.Saved = o.session, o.time
			} else if o, ok := moved[normText(m.text)]; ok {
				e.Session, e.Saved = o.Session, o.Time
			}
			if u, ok := used[normText(m.text)]; ok {
				e.Used = &u
			}
			entries = append(entries, e)
		}
		if w != nil && w.eligible(a) {
			for _, t := range w.Tasks(a) {
				body, _ := w.ReadTask(a, t.Name)
				entries = append(entries, PanelEntry{Kind: KindTask, Agent: a.ID(), Team: sp.TeamID(), Name: t.Name, Text: oneLine(body, 600), Section: t.Title, Saved: t.Updated.Format(time.RFC3339)})
			}
			for _, n := range w.Notes(a) {
				entries = append(entries, PanelEntry{Kind: KindNote, Agent: a.ID(), Team: sp.TeamID(), Name: n.Name, Text: n.Title, Size: n.Size, Saved: n.Updated.Format(time.RFC3339)})
			}
		}
	}
	type teamView struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	var tvs []teamView
	if ts := p.teams(); ts != nil {
		for _, t := range ts.List() {
			tvs = append(tvs, teamView{ID: t.ID, Name: t.Name})
			teamSet[t.ID] = true
		}
		ids := make([]string, 0, len(teamSet))
		for id := range teamSet {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			for _, m := range parseLines(ts.Memory(id), true) {
				e := PanelEntry{Kind: KindTeam, Team: id, Text: m.text, Section: m.sec, Line: m.line, Raw: m.raw, By: m.by, Saved: m.date}
				if o, ok := teamOrigin[normText(m.text)]; ok {
					e.Session, e.Saved = o.session, o.time
				}
				entries = append(entries, e)
			}
		}
	}
	if entries == nil {
		entries = []PanelEntry{}
	}
	if ags == nil {
		ags = []PanelAgent{}
	}
	if tvs == nil {
		tvs = []teamView{}
	}
	return map[string]any{"entries": entries, "agents": ags, "teams": tvs}
}

// change is a request to edit, delete or correct an entry.
type change struct {
	Kind       string `json:"kind"`
	Agent      string `json:"agent"`
	Team       string `json:"team"`
	Line       int    `json:"line"`
	Raw        string `json:"raw"`
	Name       string `json:"name"`
	Text       string `json:"text"`
	Correction string `json:"correction"`
	Tell       bool   `json:"tell"`
}

const maxEntryChars = 2000

func (p *panel) agent(id string) (*agent.Agent, error) {
	a, ok := p.agents.Get(id)
	if !ok || !listable(a) {
		return nil, gateway.NotFound()
	}
	return a, nil
}

// rewriteMemory changes one line of a's MEMORY.md and re-indexes it.
func (p *panel) rewriteMemory(a *agent.Agent, line int, raw string, fn func(m memLine) (string, bool)) (memLine, error) {
	path := filepath.Join(a.Home(), "MEMORY.md")
	p.st.mu.Lock()
	cur, err := os.ReadFile(path)
	if err != nil {
		p.st.mu.Unlock()
		return memLine{}, gateway.NotFound()
	}
	var newLine string
	var dropped bool
	next, m, err := edit(string(cur), line, raw, false, func(m memLine) (string, bool) {
		newLine, dropped = fn(m)
		return newLine, dropped
	})
	if err == nil {
		err = writeAtomic(path, []byte(next))
	}
	p.st.mu.Unlock()
	if err != nil {
		return memLine{}, err
	}
	if !dropped {
		if nl := parseLines(newLine, false); len(nl) == 1 {
			p.carry(a, m.text, nl[0].text, next)
		}
	}
	return m, p.st.refreshMemory(a, true)
}

// savedOrigin is where an edited entry first came from (.memory/moved.json):
// an edit changes the text, so the memory_add call no longer matches it.
type savedOrigin struct {
	Session string `json:"session"`
	Time    string `json:"time"`
}

func movedPath(a *agent.Agent) string { return filepath.Join(memDir(a), "moved.json") }

func readMoved(a *agent.Agent) map[string]savedOrigin {
	out := map[string]savedOrigin{}
	if b, err := os.ReadFile(movedPath(a)); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	if out == nil { // a file holding null: callers write to the map
		out = map[string]savedOrigin{}
	}
	return out
}

// carry keeps an edited entry's origin and last recall under its new text.
func (p *panel) carry(a *agent.Agent, before, after, file string) {
	oldK, newK := normText(before), normText(after)
	if oldK == newK {
		return
	}
	mem, _ := p.st.origins()
	usedMu.Lock()
	defer usedMu.Unlock()
	moved := readMoved(a)
	if o, ok := mem[a.ID()][oldK]; ok {
		moved[newK] = savedOrigin{Session: o.session, Time: o.time}
	} else if o, ok := moved[oldK]; ok {
		moved[newK] = o
	}
	live := map[string]bool{}
	for _, m := range parseLines(file, false) {
		live[normText(m.text)] = true
	}
	for k := range moved {
		if !live[k] {
			delete(moved, k)
		}
	}
	b, _ := json.Marshal(moved)
	_ = os.MkdirAll(memDir(a), 0o700)
	_ = writeAtomic(movedPath(a), b)
	if u := readUsed(a); !u[oldK].IsZero() {
		u[newK] = u[oldK]
		delete(u, oldK)
		b, _ := json.Marshal(u)
		_ = writeAtomic(usedPath(a), b)
	}
}

func (p *panel) rewriteTeam(team string, line int, raw string, fn func(m memLine) (string, bool)) (memLine, error) {
	ts := p.teams()
	if ts == nil {
		return memLine{}, gateway.NotFound()
	}
	next, m, err := edit(ts.Memory(team), line, raw, true, fn)
	if err != nil {
		return memLine{}, err
	}
	return m, ts.SetMemory(team, next)
}

func checkText(t string) (string, error) {
	t = normText(t)
	if t == "" {
		return "", gateway.BadRequest("记忆内容不能为空")
	}
	if utf8.RuneCountInString(t) > maxEntryChars {
		return "", gateway.BadRequest("一条记忆不超过 %d 字", maxEntryChars)
	}
	return t, nil
}

func (p *panel) update(r *gateway.Request, x change) (any, error) {
	e := PanelEntry{Kind: x.Kind, Agent: x.Agent, Team: x.Team, Name: x.Name}
	switch x.Kind {
	case KindMemory, KindTeam:
		text, err := checkText(x.Text)
		if err != nil {
			return nil, err
		}
		var m memLine
		fn := func(m memLine) (string, bool) { return replaceText(m, text, x.Kind == KindTeam), false }
		if x.Kind == KindMemory {
			a, err := p.agent(x.Agent)
			if err != nil {
				return nil, err
			}
			m, err = p.rewriteMemory(a, x.Line, x.Raw, fn)
			if err != nil {
				return nil, err
			}
		} else {
			if m, err = p.rewriteTeam(x.Team, x.Line, x.Raw, fn); err != nil {
				return nil, err
			}
		}
		p.audit(r, AuditEdited, e, map[string]any{"before": m.text, "after": text})
		return map[string]any{"text": text}, nil
	case KindTask, KindNote:
		a, err := p.agent(x.Agent)
		if err != nil {
			return nil, err
		}
		w := p.working()
		if w == nil || !w.eligible(a) {
			return nil, gateway.NotFound()
		}
		if strings.TrimSpace(x.Text) == "" {
			return nil, gateway.BadRequest("内容不能为空")
		}
		var before string
		if x.Kind == KindTask {
			before, _ = w.ReadTask(a, x.Name)
			if _, err := w.WriteTask(a, x.Name, x.Text, false); err != nil {
				return nil, gateway.BadRequest("%v", err)
			}
		} else {
			before, _ = w.ReadNote(a, x.Name, 0)
			if _, err := w.WriteNote(a, x.Name, x.Text); err != nil {
				return nil, gateway.BadRequest("%v", err)
			}
		}
		p.audit(r, AuditEdited, e, map[string]any{"before": oneLine(before, 300), "after": oneLine(x.Text, 300)})
		return map[string]any{"text": x.Text}, nil
	}
	return nil, gateway.BadRequest("unknown kind %q", x.Kind)
}

func (p *panel) remove(r *gateway.Request, x change) (any, error) {
	e := PanelEntry{Kind: x.Kind, Agent: x.Agent, Team: x.Team, Name: x.Name}
	drop := func(memLine) (string, bool) { return "", true }
	switch x.Kind {
	case KindMemory:
		a, err := p.agent(x.Agent)
		if err != nil {
			return nil, err
		}
		m, err := p.rewriteMemory(a, x.Line, x.Raw, drop)
		if err != nil {
			return nil, err
		}
		p.audit(r, AuditDeleted, e, map[string]any{"text": m.text})
	case KindTeam:
		m, err := p.rewriteTeam(x.Team, x.Line, x.Raw, drop)
		if err != nil {
			return nil, err
		}
		p.audit(r, AuditDeleted, e, map[string]any{"text": m.text})
	case KindTask, KindNote:
		a, err := p.agent(x.Agent)
		if err != nil {
			return nil, err
		}
		w := p.working()
		if w == nil || !w.eligible(a) {
			return nil, gateway.NotFound()
		}
		var before string
		if x.Kind == KindTask {
			before, _ = w.ReadTask(a, x.Name)
			if err := w.DeleteTask(a, x.Name); err != nil {
				return nil, gateway.BadRequest("%v", err)
			}
		} else {
			before, _ = w.ReadNote(a, x.Name, 0)
			if _, err := w.WriteNote(a, x.Name, ""); err != nil {
				return nil, gateway.BadRequest("%v", err)
			}
		}
		p.audit(r, AuditDeleted, e, map[string]any{"text": oneLine(before, 300)})
	default:
		return nil, gateway.BadRequest("unknown kind %q", x.Kind)
	}
	return map[string]any{"ok": true}, nil
}

// wrong handles 「这条不对」: the entry leaves memory (or is replaced by the
// correction), and with tell the agent is told in its main conversation
// (an injected note on its next request: no turn is started, nothing is
// spent).
func (p *panel) wrong(r *gateway.Request, x change) (any, error) {
	if x.Kind != KindMemory && x.Kind != KindTeam {
		return nil, gateway.BadRequest("only memory entries can be marked wrong")
	}
	corr := normText(x.Correction)
	if utf8.RuneCountInString(corr) > maxEntryChars {
		return nil, gateway.BadRequest("一条记忆不超过 %d 字", maxEntryChars)
	}
	fn := func(m memLine) (string, bool) {
		if corr == "" {
			return "", true
		}
		return replaceText(m, corr, x.Kind == KindTeam), false
	}
	e := PanelEntry{Kind: x.Kind, Agent: x.Agent, Team: x.Team}
	var m memLine
	var err error
	var a *agent.Agent
	if x.Kind == KindMemory {
		if a, err = p.agent(x.Agent); err != nil {
			return nil, err
		}
		m, err = p.rewriteMemory(a, x.Line, x.Raw, fn)
	} else {
		m, err = p.rewriteTeam(x.Team, x.Line, x.Raw, fn)
	}
	if err != nil {
		return nil, err
	}
	told := false
	if x.Tell && a != nil {
		note := fmt.Sprintf("The person reviewed your long-term memory and marked this entry as wrong: %q. It has been removed from MEMORY.md.", m.text)
		if corr != "" {
			note = fmt.Sprintf("The person reviewed your long-term memory and marked this entry as wrong: %q. It was corrected to: %q. Rely on the correction from now on.", m.text, corr)
		}
		if sess, err := p.agents.Sessions().Get(a.Spec().Session); err == nil {
			told = a.Inject(sess, note, "memory-panel") == nil
		}
	}
	p.audit(r, AuditFlagged, e, map[string]any{"text": m.text, "correction": corr, "told": told})
	return map[string]any{"ok": true, "told": told}, nil
}

// DeleteTask removes a task progress file (without archiving it).
func (w *Working) DeleteTask(a *agent.Agent, name string) error {
	n := cleanName(name)
	if n == "" {
		return errors.New("task name is empty")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := os.Remove(filepath.Join(tasksDir(a), n+".md")); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no active task %q", n)
		}
		return err
	}
	return nil
}

// PanelAPIPlugin serves the memory panel.
var PanelAPIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/memory-panel-api",
	Inject:      []string{gateway.ServiceKey, ServiceKey, agent.ServiceKey},
	Optional:    []string{WorkingKey, teams.ServiceKey, audit.ServiceKey},
	Description: "The memory panel: every agent's and team's memory and working memory, searchable, editable and deletable (changes audited).",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		p := &panel{c: c, st: kernel.Use[*Store](c, ServiceKey), agents: kernel.Use[*agent.Registry](c, agent.ServiceKey)}
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/memories",
			Handle: func(context.Context, *gateway.Request) (any, error) { return p.list(), nil }})
		body := func(f func(*gateway.Request, change) (any, error)) func(context.Context, *gateway.Request) (any, error) {
			return func(_ context.Context, r *gateway.Request) (any, error) {
				var x change
				if err := r.Decode(&x); err != nil {
					return nil, err
				}
				return f(r, x)
			}
		}
		g.Handle(c, gateway.Route{Method: "PUT", Path: "/api/memories", MaxBody: NoteMaxBytes + 4096, Handle: body(p.update)})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/memories/delete", Handle: body(p.remove)})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/memories/wrong", Handle: body(p.wrong)})
		return nil
	},
})

// auditAdd records an agent's memory_add in the audit log.
func (s *Store) auditAdd(c tools.Call, line string) {
	a, ok := kernel.Lookup[*audit.Service](s.c, audit.ServiceKey)
	if !ok {
		return
	}
	e := audit.Entry{Type: AuditAdded, Actor: c.Agent, Agent: c.Agent, Data: map[string]any{"kind": KindMemory, "text": line}}
	if c.Session != nil {
		e.Session = c.Session.ID()
	}
	_, _ = a.Record(e)
}
