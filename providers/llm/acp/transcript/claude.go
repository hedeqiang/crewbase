package transcript

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
)

// Claude Code transcript lines (one JSON object per line, all with "type"
// and "sessionId"). The ones read here:
//
//	api-request-shape  {shapeHash, shape: {system: [{type,text}], tools: [{name,description,input_schema,defer_loading?}]}}
//	                   the exact system blocks and tool schemas of requests, deduplicated by shapeHash
//	api-request        {id, timestamp, querySource, shapeHash, params: {model,...}, base, keep, tail: [hash]}
//	                   one per model call; its messages are the first keep messages of request base,
//	                   then the tail (verified: base is always the previous request of the file)
//	api-request-blob   {hash, message: {role, content}} a message not otherwise in the file
//	assistant / user   transcript messages; an assistant line is one content block of an API reply
//	                   (message.id groups them, apiBlockIndex orders them), requestRef is the
//	                   api-request id and message.usage its usage
//
// A tail hash names a blob, a user line's message or an assistant reply
// (one block or all of its blocks), see messageHash.

type claudeState struct {
	reqs     map[string]*claudeReq
	lastMain string
	shapes   map[string]span
	blobs    map[string]span
	msgs     map[string]msgRef // message hash -> transcript message
	groups   map[string]*claudeGroup
	dirty    map[string]bool // groups grown since their merged hash
	usage    map[string]claudeUsage
	// transcript order of main-thread user/assistant lines (fallback)
	turns []span
}

type claudeReq struct {
	id, base string
	keep     int
	tail     []string
	shape    string
	model    string
	at       time.Time
}

type msgRef struct {
	line  span   // a user line or blob
	group string // else: an assistant reply (message.id); single = one block
	block int    // with group: the block (>= 0), or -1 = all blocks
}

type claudeGroup struct {
	spans  []span
	blocks []int // apiBlockIndex of each span
}

type claudeUsage struct {
	u     llm.Usage
	model string
}

type claudeLine struct {
	Type        string                 `json:"type"`
	SessionID   string                 `json:"sessionId"`
	ID          string                 `json:"id"`
	Timestamp   string                 `json:"timestamp"`
	QuerySource *string                `json:"querySource"`
	ShapeHash   string                 `json:"shapeHash"`
	Base        string                 `json:"base"`
	Keep        int                    `json:"keep"`
	Tail        []string               `json:"tail"`
	Params      struct{ Model string } `json:"params"`
	Hash        string                 `json:"hash"`
	Message     json.RawMessage        `json:"message"`
	IsSidechain bool                   `json:"isSidechain"`
	RequestRef  string                 `json:"requestRef"`
	BlockIndex  int                    `json:"apiBlockIndex"`
}

type claudeMessage struct {
	ID      string          `json:"id"`
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
	Usage   *struct {
		Input       int `json:"input_tokens"`
		CacheRead   int `json:"cache_read_input_tokens"`
		CacheCreate int `json:"cache_creation_input_tokens"`
		Output      int `json:"output_tokens"`
	} `json:"usage"`
}

// mainSource reports whether a request's querySource is the main thread:
// the interactive REPL (repl_main_thread*), the SDK (sdk: what the ACP
// bridge runs) or unset. Sub-agents (agent:*), compaction, titles and other
// side queries are skipped.
func mainSource(q *string) bool {
	if q == nil {
		return true
	}
	s := *q
	return s == "" || s == "sdk" || strings.HasPrefix(s, "repl_main_thread")
}

func newClaudeState() *claudeState {
	return &claudeState{reqs: map[string]*claudeReq{}, shapes: map[string]span{}, blobs: map[string]span{},
		msgs: map[string]msgRef{}, groups: map[string]*claudeGroup{}, dirty: map[string]bool{}, usage: map[string]claudeUsage{}}
}

func (st *claudeState) line(b []byte, at span, session string) {
	var l claudeLine
	if json.Unmarshal(b, &l) != nil {
		return
	}
	if l.SessionID != "" && l.SessionID != session {
		return // never another session's lines
	}
	switch l.Type {
	case "api-request-shape":
		if l.ShapeHash != "" {
			st.shapes[l.ShapeHash] = at
		}
	case "api-request-blob":
		if l.Hash != "" {
			st.blobs[l.Hash] = at
		}
	case "api-request":
		if l.ID == "" {
			return
		}
		q := &claudeReq{id: l.ID, base: l.Base, keep: l.Keep, tail: l.Tail, shape: l.ShapeHash, model: l.Params.Model}
		q.at, _ = time.Parse(time.RFC3339Nano, l.Timestamp)
		st.reqs[l.ID] = q
		if mainSource(l.QuerySource) {
			st.lastMain = l.ID
		}
	case "user":
		if l.IsSidechain {
			return
		}
		var m claudeMessage
		if json.Unmarshal(l.Message, &m) != nil || len(m.Content) == 0 {
			return
		}
		st.turns = append(st.turns, at)
		if h, err := messageHash(m.Role, m.Content); err == nil {
			st.msgs[h] = msgRef{line: at}
		}
	case "assistant":
		if l.IsSidechain {
			return
		}
		var m claudeMessage
		if json.Unmarshal(l.Message, &m) != nil {
			return
		}
		st.turns = append(st.turns, at)
		if m.ID != "" {
			g := st.groups[m.ID]
			if g == nil {
				g = &claudeGroup{}
				st.groups[m.ID] = g
			}
			g.spans = append(g.spans, at)
			g.blocks = append(g.blocks, l.BlockIndex)
			st.dirty[m.ID] = true
			if h, err := messageHash("assistant", m.Content); err == nil {
				st.msgs[h] = msgRef{group: m.ID, block: len(g.spans) - 1}
			}
		}
		if m.Usage != nil && l.RequestRef != "" {
			u := m.Usage
			st.usage[l.RequestRef] = claudeUsage{model: m.Model, u: llm.Usage{
				InputTokens: u.Input + u.CacheRead + u.CacheCreate, OutputTokens: u.Output,
				CacheRead: u.CacheRead, CacheWrite: u.CacheCreate}}
		}
	}
}

// Claude reads the last main-thread request of Claude Code session id from
// its transcript at path.
func (r *Reader) Claude(path, id string) (*Composition, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrNotFound
	}
	defer f.Close()
	fs := r.state("claude\x00" + path + "\x00" + id)
	fs.mu.Lock()
	defer fs.mu.Unlock()
	err = r.advance(fs, f, func() { fs.claude = newClaudeState() }, func(b []byte, at span) { fs.claude.line(b, at, id) })
	if err != nil {
		return nil, err
	}
	st := fs.claude
	st.mergeDirty(f)
	out := &Composition{CLI: ClaudeCode, Session: id, Path: path}
	q := st.reqs[st.lastMain]
	if q == nil {
		// no request records (an older Claude Code): the transcript
		if !st.fromTranscript(f, out) {
			return nil, ErrNotFound
		}
		return out, nil
	}
	out.Model, out.At = q.model, q.at
	if u, ok := st.usage[q.id]; ok {
		out.Usage, out.Counted = u.u, true
		if out.Model == "" {
			out.Model = u.model
		}
	}
	if sp, ok := st.shapes[q.shape]; ok {
		if b, err := readSpan(f, sp); err == nil {
			out.SystemRecorded, out.ToolsRecorded = true, true
			out.System, out.Tools = parseShape(b)
		}
	}
	hashes, ok := st.chain(q)
	if !ok {
		// the chain starts before the parsed window: the transcript
		st.fromTranscript(f, out)
		return out, nil
	}
	for _, h := range hashes {
		m, ok := st.resolve(f, h)
		if !ok {
			out.Unresolved++
			continue
		}
		out.Messages = append(out.Messages, m)
	}
	return out, nil
}

// chain is the message hashes of request q: the first keep of its base's,
// then its tail. false when a base is missing.
func (st *claudeState) chain(q *claudeReq) ([]string, bool) {
	var path []*claudeReq
	seen := map[string]bool{}
	for c := q; ; {
		if seen[c.id] {
			return nil, false
		}
		seen[c.id] = true
		path = append(path, c)
		if c.base == "" {
			break
		}
		b := st.reqs[c.base]
		if b == nil {
			return nil, false
		}
		c = b
	}
	var hs []string
	for i := len(path) - 1; i >= 0; i-- {
		c := path[i]
		if c.base != "" {
			if c.keep < 0 || c.keep > len(hs) {
				return nil, false
			}
			hs = hs[:c.keep]
		} else {
			hs = hs[:0]
		}
		hs = append(hs, c.tail...)
	}
	return hs, true
}

// mergeDirty indexes the merged hash of every assistant reply that grew.
func (st *claudeState) mergeDirty(f *os.File) {
	for id := range st.dirty {
		g := st.groups[id]
		delete(st.dirty, id)
		if g == nil || len(g.spans) < 2 {
			continue
		}
		content, ok := st.groupContent(f, g, -1)
		if !ok {
			continue
		}
		raw, _ := json.Marshal(content)
		if h, err := messageHash("assistant", raw); err == nil {
			st.msgs[h] = msgRef{group: id, block: -1}
		}
	}
}

// groupContent is the content blocks of a reply (block -1 = all, in
// apiBlockIndex order).
func (st *claudeState) groupContent(f *os.File, g *claudeGroup, block int) ([]json.RawMessage, bool) {
	idx := make([]int, 0, len(g.spans))
	if block >= 0 {
		idx = append(idx, block)
	} else {
		for i := range g.spans {
			idx = append(idx, i)
		}
		// stable insertion sort by block index (few blocks)
		for i := 1; i < len(idx); i++ {
			for j := i; j > 0 && g.blocks[idx[j]] < g.blocks[idx[j-1]]; j-- {
				idx[j], idx[j-1] = idx[j-1], idx[j]
			}
		}
	}
	var out []json.RawMessage
	for _, i := range idx {
		b, err := readSpan(f, g.spans[i])
		if err != nil {
			return nil, false
		}
		var l claudeLine
		var m claudeMessage
		if json.Unmarshal(b, &l) != nil || json.Unmarshal(l.Message, &m) != nil {
			return nil, false
		}
		var blocks []json.RawMessage
		if json.Unmarshal(m.Content, &blocks) != nil {
			return nil, false
		}
		out = append(out, blocks...)
	}
	return out, true
}

// resolve is the message a hash names.
func (st *claudeState) resolve(f *os.File, h string) (llm.Message, bool) {
	if sp, ok := st.blobs[h]; ok {
		return lineMessage(f, sp)
	}
	ref, ok := st.msgs[h]
	if !ok {
		return llm.Message{}, false
	}
	if ref.group == "" {
		return lineMessage(f, ref.line)
	}
	g := st.groups[ref.group]
	if g == nil {
		return llm.Message{}, false
	}
	blocks, ok := st.groupContent(f, g, ref.block)
	if !ok {
		return llm.Message{}, false
	}
	raw, _ := json.Marshal(blocks)
	return anthropicMessage("assistant", raw), true
}

func lineMessage(f *os.File, sp span) (llm.Message, bool) {
	b, err := readSpan(f, sp)
	if err != nil {
		return llm.Message{}, false
	}
	var l claudeLine
	var m claudeMessage
	if json.Unmarshal(b, &l) != nil || json.Unmarshal(l.Message, &m) != nil {
		return llm.Message{}, false
	}
	return anthropicMessage(m.Role, m.Content), true
}

// fromTranscript fills out's messages (and usage, when not set) from the
// main-thread user/assistant lines, consecutive blocks of one reply joined.
func (st *claudeState) fromTranscript(f *os.File, out *Composition) bool {
	if len(st.turns) == 0 {
		return false
	}
	out.FromTranscript = true
	out.Messages = nil
	lastID := ""
	for _, sp := range st.turns {
		b, err := readSpan(f, sp)
		if err != nil {
			continue
		}
		var l claudeLine
		var m claudeMessage
		if json.Unmarshal(b, &l) != nil || json.Unmarshal(l.Message, &m) != nil {
			continue
		}
		msg := anthropicMessage(m.Role, m.Content)
		if m.Role == "assistant" && m.ID != "" && m.ID == lastID && len(out.Messages) > 0 {
			last := &out.Messages[len(out.Messages)-1]
			last.Parts = append(last.Parts, msg.Parts...)
		} else {
			out.Messages = append(out.Messages, msg)
		}
		lastID = ""
		if m.Role == "assistant" {
			lastID = m.ID
			if m.Usage != nil && !out.Counted {
				// the latest reply's usage (overwritten as we go)
				u := m.Usage
				out.Usage = llm.Usage{InputTokens: u.Input + u.CacheRead + u.CacheCreate, OutputTokens: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheCreate}
				if m.Model != "" {
					out.Model = m.Model
				}
			}
		}
	}
	if out.Usage.InputTokens > 0 {
		out.Counted = true
	}
	return true
}

// parseShape reads an api-request-shape line.
func parseShape(b []byte) ([]Block, []Tool) {
	var l struct {
		Shape struct {
			System json.RawMessage   `json:"system"`
			Tools  []json.RawMessage `json:"tools"`
		} `json:"shape"`
	}
	if json.Unmarshal(b, &l) != nil {
		return nil, nil
	}
	var blocks []Block
	var str string
	if json.Unmarshal(l.Shape.System, &str) == nil {
		if str != "" {
			blocks = append(blocks, Block{Name: blockName(str), Text: str})
		}
	} else {
		var sys []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		_ = json.Unmarshal(l.Shape.System, &sys)
		for _, s := range sys {
			blocks = append(blocks, Block{Name: blockName(s.Text), Text: s.Text})
		}
	}
	var tools []Tool
	for _, raw := range l.Shape.Tools {
		var t struct {
			Name        string         `json:"name"`
			Type        string         `json:"type"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"input_schema"`
			Defer       bool           `json:"defer_loading"`
		}
		if json.Unmarshal(raw, &t) != nil {
			continue
		}
		name := t.Name
		if name == "" {
			name = t.Type // a server tool
		}
		tools = append(tools, Tool{Name: name, Group: ToolGroup(name), Description: t.Description, Schema: t.InputSchema,
			SchemaChars: len(raw), Deferred: t.Defer})
	}
	return blocks, tools
}

// anthropicMessage converts an Anthropic message (content a string or
// blocks). Tool results become RoleTool messages when that is all they
// hold; Claude Code's internal "system" messages are sent as user content.
func anthropicMessage(role string, content json.RawMessage) llm.Message {
	m := llm.Message{Role: llm.RoleUser}
	if role == "assistant" {
		m.Role = llm.RoleAssistant
	}
	var s string
	if json.Unmarshal(content, &s) == nil {
		m.Parts = []llm.Part{{Type: "text", Text: s}}
		return m
	}
	var blocks []struct {
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		Thinking  string          `json:"thinking"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Input     json.RawMessage `json:"input"`
		ToolUseID string          `json:"tool_use_id"`
		Content   json.RawMessage `json:"content"`
		IsError   bool            `json:"is_error"`
		Source    struct {
			MediaType string `json:"media_type"`
		} `json:"source"`
	}
	_ = json.Unmarshal(content, &blocks)
	results := 0
	for _, b := range blocks {
		switch b.Type {
		case "text":
			m.Parts = append(m.Parts, llm.Part{Type: "text", Text: b.Text})
		case "thinking":
			m.Parts = append(m.Parts, llm.Part{Type: "thinking", Text: b.Thinking})
		case "redacted_thinking":
			m.Parts = append(m.Parts, llm.Part{Type: "thinking", Text: "[redacted thinking]"})
		case "tool_use", "server_tool_use":
			args := b.Input
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			m.Parts = append(m.Parts, llm.Part{Type: "tool_call", ToolCall: &llm.ToolCall{ID: b.ID, Name: b.Name, Args: args}})
		case "tool_result":
			results++
			m.Parts = append(m.Parts, llm.Part{Type: "tool_result", ToolResult: &llm.ToolResult{CallID: b.ToolUseID, Content: resultText(b.Content), IsError: b.IsError}})
		case "image", "document":
			m.Parts = append(m.Parts, llm.Part{Type: "text", Text: "[" + b.Type + " " + b.Source.MediaType + "]"})
		default:
			if b.Type != "" {
				m.Parts = append(m.Parts, llm.Part{Type: "text", Text: "[" + b.Type + "]"})
			}
		}
	}
	if results > 0 && results == len(m.Parts) {
		m.Role = llm.RoleTool
	}
	return m
}

// resultText flattens a tool_result's content (a string or blocks).
func resultText(c json.RawMessage) string {
	if len(c) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(c, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(c, &blocks) != nil {
		return string(c)
	}
	var b strings.Builder
	for i, x := range blocks {
		if i > 0 {
			b.WriteString("\n")
		}
		if x.Type == "text" {
			b.WriteString(x.Text)
		} else {
			b.WriteString("[" + x.Type + "]")
		}
	}
	return b.String()
}
