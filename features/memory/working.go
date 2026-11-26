package memory

// Working memory (plugin @crewbase/memory-working), after the memory part
// of "always-on agents": things an agent needs between turns that are not
// long-term facts.
//
//   - What it already told you (.memory/told.jsonl): messages it sent on
//     its own — routine reports, goal rounds, follow-ups, background
//     results, approvals coming back — so the next report does not repeat
//     the last one. The latest few are in the system prompt.
//   - A progress file per task in progress (tasks/<name>.md): where it is,
//     what is done, what is next. The prompt lists the active ones (name,
//     age, first line); the agent reads one when it picks the task up, and
//     marks it done when finished (moved to tasks/done/).
//   - Reference notes (notes/*.md, *.txt): material you hand it. Only an
//     index is in the prompt (name, first line, size); the agent reads a
//     note when it is relevant.
//
// The prompt carries only these short lists, which change when the agent
// reports or updates a task, not every turn, so it stays cache-friendly.

import (
	"bufio"
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
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// Limits of working memory.
const (
	ToldKeep      = 40   // entries kept in told.jsonl
	ToldShown     = 8    // entries in the prompt
	ToldChars     = 220  // characters kept per entry
	TasksShown    = 12   // active tasks in the prompt
	TaskMaxChars  = 8000 // a progress file
	DoneKeep      = 30   // finished progress files kept
	NotesShown    = 30   // notes in the index
	NoteMaxBytes  = 512 << 10
	NoteReadChars = 12000
)

// ToldEntry is one message an agent sent on its own.
type ToldEntry struct {
	Time    time.Time `json:"time"`
	Session string    `json:"session"`
	Via     string    `json:"via"` // routine | goal | followup | job | delegation | approval | workflow
	Text    string    `json:"text"`
}

// TaskInfo describes a progress file.
type TaskInfo struct {
	Name    string    `json:"name"`
	Title   string    `json:"title"` // first line
	Updated time.Time `json:"updated"`
	Done    bool      `json:"done,omitempty"`
}

// NoteInfo describes a reference note.
type NoteInfo struct {
	Name    string    `json:"name"`
	Title   string    `json:"title"`
	Size    int64     `json:"size"`
	Updated time.Time `json:"updated"`
}

// Working is the working-memory service (key "memory.working").
type Working struct {
	agents *agent.Registry
	mu     sync.Mutex
	now    func() time.Time
}

// WorkingKey is the service key.
const WorkingKey = "memory.working"

// proactive tags: turns nobody typed, whose reply is a report.
var proactive = []string{"routine:", "goal:", "followup:", "job:", "delegation:", "approval:", "workflow:"}

func viaOf(tag string) string {
	for _, p := range proactive {
		if strings.HasPrefix(tag, p) {
			return strings.TrimSuffix(p, ":")
		}
	}
	return ""
}

func (w *Working) eligible(a *agent.Agent) bool {
	s := a.Spec()
	return !a.IsChat() && !s.Ephemeral
}

// ---------------------------------------------------------------- told

func toldPath(a *agent.Agent) string { return filepath.Join(memDir(a), "told.jsonl") }

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > n {
		r := []rune(s)
		s = string(r[:n-1]) + "…"
	}
	return s
}

// Told returns a's recorded messages, newest last.
func (w *Working) Told(a *agent.Agent) []ToldEntry {
	f, err := os.Open(toldPath(a))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []ToldEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var e ToldEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Text != "" {
			out = append(out, e)
		}
	}
	return out
}

func (w *Working) recordTold(e agentloop.TurnEndEvent) {
	via := viaOf(e.Input.Tag)
	text := strings.TrimSpace(e.Text)
	if via == "" || e.Status != "completed" || text == "" || strings.EqualFold(text, "NO_REPLY") {
		return
	}
	a, ok := w.agents.Get(e.Agent)
	if !ok || !w.eligible(a) {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	xs := append(w.Told(a), ToldEntry{Time: w.now().UTC(), Session: e.Session, Via: via, Text: oneLine(text, ToldChars)})
	if len(xs) > ToldKeep {
		xs = xs[len(xs)-ToldKeep:]
	}
	var b strings.Builder
	for _, x := range xs {
		raw, _ := json.Marshal(x)
		b.Write(raw)
		b.WriteByte('\n')
	}
	_ = os.MkdirAll(memDir(a), 0o700)
	_ = writeAtomic(toldPath(a), []byte(b.String()))
}

// ---------------------------------------------------------------- tasks

var nameRe = regexp.MustCompile(`[^\p{L}\p{N}._-]+`)

// cleanName turns a task or note name into a safe file stem.
func cleanName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(strings.TrimSuffix(s, ".md"), ".txt")
	s = nameRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-.")
	if utf8.RuneCountInString(s) > 60 {
		s = string([]rune(s)[:60])
	}
	return s
}

func tasksDir(a *agent.Agent) string { return filepath.Join(a.Home(), "tasks") }

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "#>-* "))
		if l != "" {
			return oneLine(l, 100)
		}
	}
	return ""
}

func list(dir string, exts ...string) []os.DirEntry {
	es, _ := os.ReadDir(dir)
	var out []os.DirEntry
	for _, e := range es {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		for _, x := range exts {
			if strings.HasSuffix(strings.ToLower(e.Name()), x) {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

func headOf(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	b := make([]byte, n)
	k, _ := f.Read(b)
	return string(b[:k])
}

// Tasks lists a's active progress files, most recently updated first.
func (w *Working) Tasks(a *agent.Agent) []TaskInfo {
	var out []TaskInfo
	for _, e := range list(tasksDir(a), ".md") {
		st, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, TaskInfo{Name: strings.TrimSuffix(e.Name(), ".md"), Title: firstLine(headOf(filepath.Join(tasksDir(a), e.Name()), 600)), Updated: st.ModTime().UTC()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out
}

// WriteTask replaces a task's progress file; done moves it to tasks/done/.
func (w *Working) WriteTask(a *agent.Agent, name, text string, done bool) (string, error) {
	n := cleanName(name)
	if n == "" {
		return "", errors.New("task name is empty")
	}
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) > TaskMaxChars {
		return "", fmt.Errorf("progress is %d characters; keep it under %d (where it stands, what is done, what is next)", utf8.RuneCountInString(text), TaskMaxChars)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	dir := tasksDir(a)
	if err := os.MkdirAll(filepath.Join(dir, "done"), 0o700); err != nil {
		return "", err
	}
	p := filepath.Join(dir, n+".md")
	if text != "" {
		if err := writeAtomic(p, []byte(text+"\n")); err != nil {
			return "", err
		}
	} else if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("no task %q yet: give its progress text", n)
	}
	if !done {
		return n, nil
	}
	dst := filepath.Join(dir, "done", w.now().Format("20060102-150405")+"-"+n+".md")
	if err := os.Rename(p, dst); err != nil {
		return "", err
	}
	if old := list(filepath.Join(dir, "done"), ".md"); len(old) > DoneKeep {
		sort.Slice(old, func(i, j int) bool { return old[i].Name() < old[j].Name() })
		for _, e := range old[:len(old)-DoneKeep] {
			_ = os.Remove(filepath.Join(dir, "done", e.Name()))
		}
	}
	return n, nil
}

// ReadTask returns a task's progress file.
func (w *Working) ReadTask(a *agent.Agent, name string) (string, error) {
	n := cleanName(name)
	b, err := os.ReadFile(filepath.Join(tasksDir(a), n+".md"))
	if err != nil {
		return "", fmt.Errorf("no active task %q", n)
	}
	return string(b), nil
}

// ---------------------------------------------------------------- notes

func notesDir(a *agent.Agent) string { return filepath.Join(a.Home(), "notes") }

// Notes lists a's reference notes by name.
func (w *Working) Notes(a *agent.Agent) []NoteInfo {
	var out []NoteInfo
	for _, e := range list(notesDir(a), ".md", ".txt") {
		st, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, NoteInfo{Name: e.Name(), Title: firstLine(headOf(filepath.Join(notesDir(a), e.Name()), 600)), Size: st.Size(), Updated: st.ModTime().UTC()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// notePath resolves a note name inside notes/ (no other directory). A
// name exactly as listed (Notes: files put there by hand or by the agent's
// file tools may have spaces, brackets or ".MD") is used as it is; other
// names are cleaned, as new notes are saved under.
func notePath(a *agent.Agent, name string) (string, error) {
	if name != "" && name == filepath.Base(name) && name != "." && name != ".." && !strings.ContainsAny(name, `/\`) {
		if st, err := os.Stat(filepath.Join(notesDir(a), name)); err == nil && st.Mode().IsRegular() && noteExt(name) {
			return filepath.Join(notesDir(a), name), nil
		}
	}
	ext := ".md"
	if strings.HasSuffix(strings.ToLower(name), ".txt") {
		ext = ".txt"
	}
	n := cleanName(name)
	if n == "" {
		return "", errors.New("note name is empty")
	}
	return filepath.Join(notesDir(a), n+ext), nil
}

func noteExt(name string) bool {
	l := strings.ToLower(name)
	return strings.HasSuffix(l, ".md") || strings.HasSuffix(l, ".txt")
}

// ReadNote returns part of a note, from rune offset from.
func (w *Working) ReadNote(a *agent.Agent, name string, from int) (string, error) {
	p, err := notePath(a, name)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("no note %q (see the notes list in your instructions)", name)
	}
	r := []rune(string(b))
	if from < 0 || from > len(r) {
		from = 0
	}
	end := min(len(r), from+NoteReadChars)
	out := string(r[from:end])
	if end < len(r) {
		out += fmt.Sprintf("\n\n[… %d more characters; continue with from=%d]", len(r)-end, end)
	}
	return out, nil
}

// WriteNote saves (or with empty text removes) a note.
func (w *Working) WriteNote(a *agent.Agent, name, text string) (string, error) {
	p, err := notePath(a, name)
	if err != nil {
		return "", err
	}
	if len(text) > NoteMaxBytes {
		return "", fmt.Errorf("note is larger than %d KB", NoteMaxBytes>>10)
	}
	if !utf8.ValidString(text) {
		return "", errors.New("notes are text (Markdown or plain text)")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if strings.TrimSpace(text) == "" {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return "", err
		}
		return filepath.Base(p), nil
	}
	if err := os.MkdirAll(notesDir(a), 0o700); err != nil {
		return "", err
	}
	return filepath.Base(p), writeAtomic(p, []byte(text))
}

// ---------------------------------------------------------------- prompt

func (w *Working) render(id string) string {
	a, ok := w.agents.Get(id)
	if !ok || !w.eligible(a) {
		return ""
	}
	var b strings.Builder
	if ts := w.Tasks(a); len(ts) > 0 {
		b.WriteString("Tasks in progress (your progress files; read one with task_progress_read before continuing it, update it with task_progress as you go, done=true when finished):\n")
		for i, t := range ts {
			if i == TasksShown {
				fmt.Fprintf(&b, "- … %d more\n", len(ts)-i)
				break
			}
			// when, not how long ago: the section stays the same from one
			// request to the next (providers keep a prompt cache on it)
			fmt.Fprintf(&b, "- %s (updated %s): %s\n", t.Name, t.Updated.Local().Format("01-02 15:04"), t.Title)
		}
	}
	if ns := w.Notes(a); len(ns) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("Reference notes you were given (only the index is here; read one with note_read when it is relevant, and do not guess its contents):\n")
		for i, n := range ns {
			if i == NotesShown {
				fmt.Fprintf(&b, "- … %d more\n", len(ns)-i)
				break
			}
			fmt.Fprintf(&b, "- %s — %s (%s)\n", n.Name, n.Title, size(n.Size))
		}
	}
	if told := w.Told(a); len(told) > 0 {
		if len(told) > ToldShown {
			told = told[len(told)-ToldShown:]
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("What you already told the user on your own recently (do not repeat it; report only what is new or changed):\n")
		for _, e := range told {
			fmt.Fprintf(&b, "- %s, %s: %s\n", e.Time.Local().Format("01-02 15:04"), e.Via, e.Text)
		}
	}
	return b.String()
}

func size(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%d KB", (n+512)/1024)
}

// ---------------------------------------------------------------- tools

func (w *Working) registerTools(c *kernel.Context, reg *tools.Registry) {
	agentOf := func(call tools.Call) (*agent.Agent, error) {
		a, ok := w.agents.Get(call.Agent)
		if !ok || !w.eligible(a) {
			return nil, errors.New("only agents (not chats or sub-agents) keep task progress and notes")
		}
		return a, nil
	}
	reg.Register(c, &tools.Tool{
		Name: "task_progress",
		Description: "Write the progress file of a task you are working on across turns or days: where it stands, what is done, what is next, open questions. " +
			"It replaces the previous text. Set done=true when the task is finished (the file is archived). " +
			"Use it for multi-step or long-running work, not for one-off answers.",
		Category: tools.CategoryWrite,
		Params: tools.Schema(map[string]any{
			"task": tools.Prop("string", "short task name, e.g. weekly-report or migrate-billing"),
			"text": tools.Prop("string", "the full progress note (Markdown, first line = what the task is)"),
			"done": tools.Prop("boolean", "true when the task is finished"),
		}, "task"),
		Classify: func(call tools.Call) (string, string) {
			return "", "task progress: " + truncate(call.String("task"), 60)
		},
		Execute: func(_ context.Context, call tools.Call) (tools.Result, error) {
			a, err := agentOf(call)
			if err != nil {
				return tools.Errorf("task_progress: %v", err), nil
			}
			var x struct {
				Task string `json:"task"`
				Text string `json:"text"`
				Done bool   `json:"done"`
			}
			_ = call.Decode(&x)
			n, err := w.WriteTask(a, x.Task, x.Text, x.Done)
			if err != nil {
				return tools.Errorf("task_progress: %v", err), nil
			}
			if x.Done {
				return tools.Text("Task " + n + " marked done and archived."), nil
			}
			return tools.Text("Progress of " + n + " saved."), nil
		},
	})
	reg.Register(c, &tools.Tool{
		Name:        "task_progress_read",
		Description: "Read the progress file of one of your tasks in progress (listed in your instructions).",
		Category:    tools.CategoryRead,
		Params:      tools.Schema(map[string]any{"task": tools.Prop("string", "task name")}, "task"),
		Execute: func(_ context.Context, call tools.Call) (tools.Result, error) {
			a, err := agentOf(call)
			if err != nil {
				return tools.Errorf("task_progress_read: %v", err), nil
			}
			s, err := w.ReadTask(a, call.String("task"))
			if err != nil {
				return tools.Errorf("task_progress_read: %v", err), nil
			}
			return tools.Text(s), nil
		},
	})
	reg.Register(c, &tools.Tool{
		Name: "note_read", Load: tools.LoadDeferred, Group: "notes",
		Description: "Read one of the reference notes you were given (listed in your instructions). Long notes come in parts; continue with from.",
		Category:    tools.CategoryRead,
		Params: tools.Schema(map[string]any{
			"name": tools.Prop("string", "note file name, e.g. pricing.md"),
			"from": tools.Prop("integer", "character offset to continue from"),
		}, "name"),
		Execute: func(_ context.Context, call tools.Call) (tools.Result, error) {
			a, err := agentOf(call)
			if err != nil {
				return tools.Errorf("note_read: %v", err), nil
			}
			var x struct {
				Name string `json:"name"`
				From int    `json:"from"`
			}
			_ = call.Decode(&x)
			s, err := w.ReadNote(a, x.Name, x.From)
			if err != nil {
				return tools.Errorf("note_read: %v", err), nil
			}
			return tools.Text(s), nil
		},
	})
}

func writeAtomic(p string, b []byte) error {
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// WorkingPlugin provides working memory: the told record, task progress
// files and reference notes, with their prompt section and tools.
var WorkingPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/memory-working",
	Inject:      []string{agent.ServiceKey, tools.ServiceKey, prompt.ServiceKey},
	Description: "Working memory: what the agent already told you, per-task progress files, reference notes (indexed in the prompt, read on demand).",
	Apply: func(c *kernel.Context) error {
		w := &Working{agents: kernel.Use[*agent.Registry](c, agent.ServiceKey), now: time.Now}
		agentloop.TurnEnded.Listen(c, w.recordTold)
		kernel.Use[*prompt.Assembler](c, prompt.ServiceKey).Register(c, &prompt.Fragment{Name: "working-memory", Order: 22, Render: w.render})
		w.registerTools(c, kernel.Use[*tools.Registry](c, tools.ServiceKey))
		return c.Provide(WorkingKey, w)
	},
})

// WorkingAPIPlugin serves working memory to the UI:
//
//	GET    /api/agents/{id}/working       → {tasks, notes, told}
//	GET    /api/agents/{id}/tasks/{name}  → {name, text}
//	GET    /api/agents/{id}/notes/{name}  → {name, text}
//	PUT    /api/agents/{id}/notes/{name}  {text} (desktop only)
//	DELETE /api/agents/{id}/notes/{name}  (desktop only)
var WorkingAPIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/memory-working-api",
	Inject:      []string{gateway.ServiceKey, WorkingKey},
	Description: "Task progress, reference notes and the told record of an agent, for the UI.",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		w := kernel.Use[*Working](c, WorkingKey)
		get := func(r *gateway.Request) (*agent.Agent, error) {
			a, ok := w.agents.Get(r.Params["id"])
			if !ok || !w.eligible(a) {
				return nil, gateway.NotFound()
			}
			return a, nil
		}
		mobile := []string{"mobile"}
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/agents/{id}/working", Devices: mobile,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				a, err := get(r)
				if err != nil {
					return nil, err
				}
				out := map[string]any{"tasks": nonNil(w.Tasks(a)), "notes": nonNil(w.Notes(a)), "told": nonNil(w.Told(a))}
				return out, nil
			}})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/agents/{id}/tasks/{name}", Devices: mobile,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				a, err := get(r)
				if err != nil {
					return nil, err
				}
				text, err := w.ReadTask(a, r.Params["name"])
				if err != nil {
					return nil, gateway.NotFound()
				}
				return map[string]any{"name": cleanName(r.Params["name"]), "text": text}, nil
			}})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/agents/{id}/notes/{name}", Devices: mobile,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				a, err := get(r)
				if err != nil {
					return nil, err
				}
				p, err := notePath(a, r.Params["name"])
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				b, err := os.ReadFile(p)
				if err != nil {
					return nil, gateway.NotFound()
				}
				return map[string]any{"name": filepath.Base(p), "text": string(b)}, nil
			}})
		g.Handle(c, gateway.Route{Method: "PUT", Path: "/api/agents/{id}/notes/{name}", MaxBody: NoteMaxBytes + 4096,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				a, err := get(r)
				if err != nil {
					return nil, err
				}
				var b struct {
					Text string `json:"text"`
				}
				if err := r.Decode(&b); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				if strings.TrimSpace(b.Text) == "" {
					return nil, gateway.BadRequest("note is empty")
				}
				n, err := w.WriteNote(a, r.Params["name"], b.Text)
				if err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return map[string]any{"name": n}, nil
			}})
		g.Handle(c, gateway.Route{Method: "DELETE", Path: "/api/agents/{id}/notes/{name}",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				a, err := get(r)
				if err != nil {
					return nil, err
				}
				if _, err := w.WriteNote(a, r.Params["name"], ""); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				return nil, nil
			}})
		return nil
	},
})

func nonNil[T any](xs []T) []T {
	if xs == nil {
		return []T{}
	}
	return xs
}
