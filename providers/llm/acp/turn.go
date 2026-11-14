package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/hedeqiang/crewbase/core/llm"
)

// turn collects one session/prompt turn's updates into chunks and a message.
type turn struct {
	agentName string
	toolNotes bool
	onChunk   func(llm.Chunk) error
	abort     func() // asks the agent to stop (session/cancel)
	// request context (who is asking), for permission requests
	ctx            context.Context
	agent, session string

	mu     sync.Mutex
	parts  []llm.Part
	tools  map[string]toolInfo // toolCallId -> title/kind
	err    error               // first onChunk error
	cancel bool
	used   int // tokens reported by usage_update
	// changed lists files the CLI changed this turn (first seen order)
	changed []string
}

// fileChanged records a path the CLI changed.
func (t *turn) fileChanged(p string) {
	if p == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, x := range t.changed {
		if x == p {
			return
		}
	}
	if len(t.changed) < 200 {
		t.changed = append(t.changed, p)
	}
}

func (t *turn) changedFiles() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.changed...)
}

// editPaths reads the paths a tool call touches: its locations and any
// diff content blocks.
func editPaths(u sessionUpdate) []string {
	var out []string
	for _, l := range u.Locations {
		out = append(out, l.Path)
	}
	var items []struct {
		Type string `json:"type"`
		Path string `json:"path"`
	}
	if json.Unmarshal(u.Content, &items) == nil {
		for _, it := range items {
			if it.Type == "diff" {
				out = append(out, it.Path)
			}
		}
	}
	return out
}

// changes reports whether a CLI tool kind changes files.
func changes(kind string) bool { return kind == "edit" || kind == "delete" || kind == "move" }

func newTurn(name string, notes bool, onChunk func(llm.Chunk) error, abort func()) *turn {
	if onChunk == nil {
		onChunk = func(llm.Chunk) error { return nil }
	}
	return &turn{agentName: name, toolNotes: notes, onChunk: onChunk, abort: abort, tools: map[string]toolInfo{}, ctx: context.Background()}
}

func (t *turn) cancelled() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cancel || t.err != nil
}

func (t *turn) markCancelled() {
	t.mu.Lock()
	t.cancel = true
	t.mu.Unlock()
}

// emit appends to the message and streams the chunk. Caller holds no lock.
func (t *turn) emit(typ, text string) {
	if text == "" {
		return
	}
	t.mu.Lock()
	if t.err != nil {
		t.mu.Unlock()
		return
	}
	if n := len(t.parts); n > 0 && t.parts[n-1].Type == typ {
		t.parts[n-1].Text += text
	} else {
		t.parts = append(t.parts, llm.Part{Type: typ, Text: text})
	}
	err := t.onChunk(llm.Chunk{Type: typ, Text: text})
	if err != nil {
		t.err = err
	}
	abort := t.abort
	t.mu.Unlock()
	if err != nil && abort != nil {
		abort()
	}
}

// note adds a visible one-line annotation on its own line.
func (t *turn) note(s string) {
	t.mu.Lock()
	prefix := ""
	if n := len(t.parts); n > 0 && t.parts[n-1].Type == "text" {
		if txt := t.parts[n-1].Text; txt != "" && !strings.HasSuffix(txt, "\n") {
			prefix = "\n"
		}
	}
	t.mu.Unlock()
	t.emit("text", prefix+s+"\n")
}

func blockText(raw json.RawMessage) string {
	var b struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		URI      string `json:"uri"`
		Resource *struct {
			Text string `json:"text"`
			URI  string `json:"uri"`
		} `json:"resource"`
	}
	if json.Unmarshal(raw, &b) != nil {
		return ""
	}
	switch b.Type {
	case "text":
		return b.Text
	case "resource_link":
		return b.URI
	case "resource":
		if b.Resource != nil {
			return b.Resource.Text
		}
	}
	return ""
}

func (t *turn) update(u sessionUpdate) {
	switch u.SessionUpdate {
	case "agent_message_chunk":
		t.emit("text", blockText(u.Content))
	case "agent_thought_chunk":
		t.emit("thinking", blockText(u.Content))
	case "tool_call":
		title := u.Title
		if title == "" {
			title = u.Kind
		}
		t.mu.Lock()
		t.tools[u.ToolCallID] = toolInfo{title: title, kind: u.Kind, paths: editPaths(u)}
		t.mu.Unlock()
		if t.toolNotes {
			t.note(fmt.Sprintf("[%s tool: %s]", t.agentName, title))
		}
	case "tool_call_update":
		if u.Status == "completed" {
			t.mu.Lock()
			info := t.tools[u.ToolCallID]
			t.mu.Unlock()
			kind := u.Kind
			if kind == "" {
				kind = info.kind
			}
			paths := editPaths(u)
			if len(paths) == 0 {
				paths = info.paths // locations were on the tool_call
			}
			if changes(kind) || hasDiff(u) {
				for _, p := range paths {
					t.fileChanged(p)
				}
			}
			if len(paths) > 0 {
				t.mu.Lock()
				info.paths = paths
				t.tools[u.ToolCallID] = info
				t.mu.Unlock()
			}
		}
		if u.Status != "failed" || !t.toolNotes {
			return
		}
		t.mu.Lock()
		title := t.tools[u.ToolCallID].title
		t.mu.Unlock()
		if u.Title != "" {
			title = u.Title
		}
		t.note(fmt.Sprintf("[%s tool failed: %s]", t.agentName, title))
	case "plan":
		var b strings.Builder
		b.WriteString("Plan:\n")
		for _, e := range u.Entries {
			fmt.Fprintf(&b, "- [%s] %s\n", e.Status, e.Content)
		}
		t.emit("thinking", b.String())
	case "usage_update":
		t.mu.Lock()
		t.used = u.Used
		t.mu.Unlock()
	}
}

// result returns the accumulated message and the first onChunk error.
func (t *turn) result() (llm.Message, int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return llm.Message{Role: llm.RoleAssistant, Parts: append([]llm.Part(nil), t.parts...)}, t.used, t.err
}

type toolInfo struct {
	title, kind string
	paths       []string
}

func hasDiff(u sessionUpdate) bool {
	var items []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(u.Content, &items) != nil {
		return false
	}
	for _, it := range items {
		if it.Type == "diff" {
			return true
		}
	}
	return false
}

// tool returns what the turn saw about a CLI tool call.
func (t *turn) tool(id string) toolInfo {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.tools[id]
}
