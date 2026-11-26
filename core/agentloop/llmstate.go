package agentloop

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/session"
)

// llmStateFile is the session side file of adapters' per-conversation
// state (llm.Store): agent id → adapter key → JSON document.
const llmStateFile = "llm-state.json"

// llmStateMu serialises the side file's read-modify-write across the
// agents of a session.
var llmStateMu sync.Mutex

// sessionStore is an llm.Store kept in a session's side file, for one
// agent. Without a session directory (an in-memory store) values live in
// the store itself for the life of the process.
type sessionStore struct {
	ss    *session.Session
	agent string
}

// memStates keeps the values of sessions without a directory.
var memStates sync.Map // agent\x00session\x00key → []byte

func (s sessionStore) memKey(key string) string {
	return s.agent + "\x00" + s.ss.ID() + "\x00" + key
}

func (s sessionStore) Load(key string) ([]byte, bool) {
	if s.ss.Dir() == "" {
		v, ok := memStates.Load(s.memKey(key))
		if !ok {
			return nil, false
		}
		return v.([]byte), true
	}
	llmStateMu.Lock()
	defer llmStateMu.Unlock()
	all := s.read()
	v, ok := all[s.agent][key]
	return v, ok && len(v) > 0
}

func (s sessionStore) Save(key string, value []byte) error {
	if s.ss.Dir() == "" {
		memStates.Store(s.memKey(key), append([]byte(nil), value...))
		return nil
	}
	llmStateMu.Lock()
	defer llmStateMu.Unlock()
	all := s.read()
	if all[s.agent] == nil {
		all[s.agent] = map[string]json.RawMessage{}
	}
	all[s.agent][key] = json.RawMessage(value)
	b, err := json.Marshal(all)
	if err != nil {
		return err
	}
	return s.ss.WriteState(llmStateFile, b)
}

func (s sessionStore) read() map[string]map[string]json.RawMessage {
	all := map[string]map[string]json.RawMessage{}
	if b, err := s.ss.ReadState(llmStateFile); err == nil {
		_ = json.Unmarshal(b, &all)
	}
	return all
}

// systemParts names the parts of system: the agent's prompt sections, the
// tool search section (when it follows them), then whatever Request
// listeners added; a system prompt that does not start with the sections
// is one part ("system").
func systemParts(secs []prompt.Section, section, system string) []llm.SystemPart {
	if system == "" {
		return nil
	}
	blocks := splitSystem(secs, system)
	var out []llm.SystemPart
	for _, b := range blocks {
		if b.Name != "additions" {
			out = append(out, llm.SystemPart{Name: b.Name, Text: b.Text})
			continue
		}
		rest := b.Text
		if section != "" {
			if after, ok := strings.CutPrefix(rest, section); ok {
				out = append(out, llm.SystemPart{Name: "tool-search", Text: section})
				rest = strings.TrimSpace(after)
			}
		}
		if rest != "" {
			out = append(out, llm.SystemPart{Name: "additions", Text: rest})
		}
	}
	return out
}
