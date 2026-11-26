package llm

// Append-only history, for providers that drop work when the start of a
// conversation changes (OpenAI's newer models drop the reasoning items
// that follow a changed prefix; guide "Keep the prefix unchanged").

// PrefixKeeper is optionally implemented by adapters whose requests for
// model should only ever grow at the end: the agent loop then derives
// the history append-only (a compaction summary where the compaction
// happened instead of in front of the kept turns, earlier tool results
// and images left as they were sent) and hands the request a Store and
// the system prompt's parts.
type PrefixKeeper interface {
	KeepsPrefix(model string) bool
}

// KeepsPrefix reports whether ad wants append-only history for model.
func KeepsPrefix(ad Adapter, model string) bool {
	pk, ok := ad.(PrefixKeeper)
	return ok && pk.KeepsPrefix(model)
}

// Store is state an adapter keeps for one agent's conversation in a
// session (kept with the session, so it survives a restart). Keys are the
// adapter's own; values are small JSON documents.
type Store interface {
	Load(key string) ([]byte, bool)
	Save(key string, value []byte) error
}

// SystemPart is one named part of Request.System, in order: the agent's
// prompt sections, then what was added to them. Joined with blank lines
// they are the system prompt.
type SystemPart struct {
	Name string `json:"name"`
	Text string `json:"text"`
}
