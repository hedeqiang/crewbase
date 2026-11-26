package openai

// Append-only requests (OpenAI guide "Keep the prefix unchanged").
//
// On OpenAI's newer models a reasoning item is kept only while the
// conversation before it is unchanged: change the instructions, the tools,
// the request-level reasoning effort or an earlier input item and the API
// drops the reasoning items after the change (input_transformations,
// reason prefix_mismatch) and the prompt cache from there on. For those
// models (prefixModel) requests are built so they only grow at the end:
//
//   - the start of a conversation is kept as its base (prefixBase, in the
//     session through llm.Request.Store, so it survives a restart): the
//     system prompt's parts, the tools in order, the reasoning effort.
//     Every later request sends the base as it was;
//   - what changed since is appended where it was first seen, as the
//     guide says: a tool that appeared as an additional_tools item
//     (developer role), an effort change as a configuration_update item,
//     instruction parts that changed as a developer message (GPT-6.2 and
//     later take turn-scoped top-level instructions instead, so there the
//     base goes as the first developer message and the changed parts as
//     instructions, not kept). Tools that went away stay declared and are
//     left out of tool_choice's allowed_tools;
//   - the items appended at a step are recorded with the step's reply (a
//     llm.PartProviderBlock part, Meta[MetaPrelude]) and replayed at the
//     same place in every later request, so no state but the base lives
//     outside the session log;
//   - a change that cannot be appended (a tool whose definition changed,
//     a tool search namespace that went away) starts a new base; the
//     appended items of the old one are left out.
//
// The agent loop derives history append-only for these models too
// (llm.PrefixKeeper: the compaction summary after the turns it kept, tool
// results and images not shortened after they were sent).
//
// Responses that report input_transformations are counted (by reason, per
// adapter) and noted in the session's state; a prefix_mismatch is logged
// once per conversation, at debug level, with the removed item's id.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"github.com/hedeqiang/crewbase/core/llm"
)

// MetaPrelude is the items a request appended before a reply (a
// provider_block part leading the reply): JSON of prelude.
const MetaPrelude = "openai:prelude"

// gptGen is the generation of an OpenAI GPT model id (major, minor);
// ok=false for other ids ("o3", "codex-mini", another vendor's).
func gptGen(model string) (major, minor int, ok bool) {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	v := gptVersion.FindStringSubmatch(m)
	if v == nil {
		return 0, 0, false
	}
	major, _ = strconv.Atoi(v[1])
	if v[2] != "" {
		minor, _ = strconv.Atoi(v[2])
	}
	return major, minor, true
}

// prefixModel: the models built append-only, GPT-6 and later (the guide's
// "newer frontier models"; configuration_update is for GPT-6 and later).
func prefixModel(model string) bool {
	major, _, ok := gptGen(model)
	return ok && OpenAIFamily(model) && major >= 6
}

// turnInstructions: from GPT-6.2 top-level instructions are turn-scoped
// (appended after the input, billed uncached) instead of the prefix.
func turnInstructions(model string) bool {
	major, minor, ok := gptGen(model)
	return ok && (major > 6 || major == 6 && minor >= 2)
}

// KeepsPrefix (llm.PrefixKeeper) reports whether requests for model are
// built append-only: an OpenAI model of a prefixModel generation through
// the Responses API, on OpenAI's own API or a gateway (magpie). Other
// OpenAI-compatible providers and Chat Completions are not.
func (a *Adapter) KeepsPrefix(model string) bool {
	if (!a.official && a.kind != "gateway") || os.Getenv("CREWBASE_OPENAI_PREFIX") == "off" {
		return false
	}
	if !prefixModel(model) || a.routes.responsesMissing() || a.routes.prefixRefused(model) {
		return false
	}
	return a.routesToResponses(model)
}

// prefixBase is the start of a conversation as first sent.
type prefixBase struct {
	ID     string            `json:"id"`
	Parts  []llm.SystemPart  `json:"parts,omitempty"`
	Tools  []json.RawMessage `json:"tools,omitempty"`
	Effort string            `json:"effort,omitempty"`
}

// prelude is what one request appended (Meta[MetaPrelude]).
type prelude struct {
	Base  string            `json:"base"`
	From  string            `json:"from"` // the adapter (provider id)
	Items []json.RawMessage `json:"items"`
	// Parts are the instruction parts the developer message in Items set
	// ("" = removed), so later requests know the instructions in effect.
	Parts map[string]string `json:"parts,omitempty"`
}

// prefixPlan is how one request keeps its prefix.
type prefixPlan struct {
	base  prefixBase
	fresh bool // the base was made by this request (kept once it succeeds)
	turn  bool // turn-scoped instructions (GPT-6.2+)
	// declared tools (base + additional_tools replayed), by key
	declared map[string]json.RawMessage
	// namespaces declared (tool search)
	namespaces map[string]bool
	// new items to append, and the instruction parts their developer
	// message set (recorded with the reply)
	items    []json.RawMessage
	setParts map[string]string
	// turnText: turn-scoped instructions (changed parts), GPT-6.2+
	turnText string
	// allowed restricts tool_choice when declared tools went away
	allowed []map[string]any
}

// storeKey is the Store key of the base.
func (a *Adapter) storeKey() string { return "openai:" + a.id + ":base" }

// toolKey identifies a top-level tool entry: type and name.
func toolKey(raw json.RawMessage) string {
	var t struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	_ = json.Unmarshal(raw, &t)
	return t.Type + ":" + t.Name
}

func rawTools(tl []any) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(tl))
	for _, t := range tl {
		b, err := json.Marshal(t)
		if err == nil {
			out = append(out, b)
		}
	}
	return out
}

// currentParts are req's instruction parts.
func currentParts(req llm.Request) []llm.SystemPart {
	if len(req.SystemParts) > 0 {
		return req.SystemParts
	}
	if req.System != "" {
		return []llm.SystemPart{{Name: "system", Text: req.System}}
	}
	return nil
}

func joinParts(ps []llm.SystemPart) string {
	var s []string
	for _, p := range ps {
		if p.Text != "" {
			s = append(s, p.Text)
		}
	}
	return strings.Join(s, "\n\n")
}

func newBase(parts []llm.SystemPart, tools []json.RawMessage, effort string) prefixBase {
	b := prefixBase{Parts: parts, Tools: tools, Effort: effort}
	raw, _ := json.Marshal(b)
	h := sha256.Sum256(raw)
	b.ID = hex.EncodeToString(h[:8])
	return b
}

// preludes are req's recorded preludes of base, in order.
func (a *Adapter) preludes(req llm.Request, base string) []prelude {
	var out []prelude
	for _, m := range req.Messages {
		if m.Role != llm.RoleAssistant {
			continue
		}
		for _, p := range m.Parts {
			if pr, ok := a.preludeOf(p, base); ok {
				out = append(out, pr)
			}
		}
	}
	return out
}

func (a *Adapter) preludeOf(p llm.Part, base string) (prelude, bool) {
	raw := p.Meta[MetaPrelude]
	if p.Type != llm.PartProviderBlock || raw == "" {
		return prelude{}, false
	}
	var pr prelude
	if json.Unmarshal([]byte(raw), &pr) != nil || pr.Base != base || pr.From != a.id {
		return prelude{}, false
	}
	return pr, true
}

// planPrefix works out how req keeps its prefix; tl are the tools it
// would send now.
func (a *Adapter) planPrefix(req llm.Request, tl []any) *prefixPlan {
	if req.Store == nil || !a.KeepsPrefix(req.Model) {
		return nil
	}
	parts := currentParts(req)
	tools := rawTools(tl)
	effort := ""
	if a.effortFor(req.Model) {
		effort = responsesEffort(req.Effort)
	}
	p := &prefixPlan{turn: turnInstructions(req.Model)}
	if b, ok := req.Store.Load(a.storeKey()); ok && json.Unmarshal(b, &p.base) == nil && p.base.ID != "" {
		why := p.plan(a, req, parts, tools, effort)
		if why == "" {
			return p
		}
		slog.Debug("openai: conversation prefix restarted", "provider", a.id, "model", req.Model, "agent", req.Agent, "session", req.Session, "why", why)
	}
	p = &prefixPlan{turn: turnInstructions(req.Model), base: newBase(parts, tools, effort), fresh: true}
	_ = p.plan(a, req, parts, tools, effort)
	return p
}

// plan fills p for its base; it returns why the base cannot be kept
// ("" when it can).
func (p *prefixPlan) plan(a *Adapter, req llm.Request, parts []llm.SystemPart, tools []json.RawMessage, effort string) string {
	p.declared = map[string]json.RawMessage{}
	p.namespaces = map[string]bool{}
	declare := func(raw json.RawMessage) {
		k := toolKey(raw)
		p.declared[k] = raw
		if strings.HasPrefix(k, "namespace:") {
			p.namespaces[strings.TrimPrefix(k, "namespace:")] = true
		}
	}
	for _, t := range p.base.Tools {
		declare(t)
	}
	inEffect := map[string]string{}
	for _, sp := range p.base.Parts {
		inEffect[sp.Name] = sp.Text
	}
	eff := p.base.Effort
	for _, pr := range a.preludes(req, p.base.ID) {
		for _, it := range pr.Items {
			var x struct {
				Type      string            `json:"type"`
				Tools     []json.RawMessage `json:"tools"`
				Reasoning struct {
					Effort string `json:"effort"`
				} `json:"reasoning"`
			}
			if json.Unmarshal(it, &x) != nil {
				continue
			}
			switch x.Type {
			case "additional_tools":
				for _, t := range x.Tools {
					declare(t)
				}
			case "configuration_update":
				if x.Reasoning.Effort != "" {
					eff = x.Reasoning.Effort
				}
			}
		}
		for n, t := range pr.Parts {
			if t == "" {
				delete(inEffect, n)
			} else {
				inEffect[n] = t
			}
		}
	}
	// tools
	cur := map[string]bool{}
	var added []json.RawMessage
	for _, t := range tools {
		k := toolKey(t)
		cur[k] = true
		old, ok := p.declared[k]
		switch {
		case !ok:
			added = append(added, t)
		case !jsonEqual(old, t):
			return "tool " + k + " changed"
		}
	}
	var gone []string
	for k := range p.declared {
		if !cur[k] {
			gone = append(gone, k)
		}
	}
	if len(gone) > 0 {
		for k := range p.declared {
			if !strings.HasPrefix(k, "function:") && k != "computer:" {
				// allowed_tools is only sent with function and computer tools
				return "tool " + strings.Join(gone, ", ") + " went away beside tool search"
			}
		}
		for _, t := range tools {
			var e map[string]any
			if strings.HasPrefix(toolKey(t), "function:") {
				e = map[string]any{"type": "function", "name": strings.TrimPrefix(toolKey(t), "function:")}
			} else {
				e = map[string]any{"type": "computer"}
			}
			p.allowed = append(p.allowed, e)
		}
	}
	if len(added) > 0 {
		var tu []responses.ToolUnionParam
		for _, t := range added {
			tu = append(tu, param.Override[responses.ToolUnionParam](t))
			declare(t)
		}
		p.add(responses.ResponseInputItemAdditionalToolsParam{Tools: tu})
	}
	// reasoning effort
	if effort != "" && effort != eff {
		p.add(responses.ResponseConfigurationUpdateItemParam{
			Reasoning: responses.ResponseConfigurationUpdateItemParamReasoning{Effort: shared.ReasoningEffort(effort)},
		})
	}
	// instructions
	changed := map[string]string{}
	now := map[string]bool{}
	for _, sp := range parts {
		now[sp.Name] = true
		if inEffect[sp.Name] != sp.Text {
			changed[sp.Name] = sp.Text
		}
	}
	for n := range inEffect {
		if !now[n] {
			changed[n] = ""
		}
	}
	if len(changed) > 0 {
		text := instructionUpdate(parts, changed)
		if p.turn {
			p.turnText = text
		} else {
			p.add(responses.EasyInputMessageParam{Role: responses.EasyInputMessageRoleDeveloper,
				Content: responses.EasyInputMessageContentUnionParam{OfString: param.NewOpt(text)}})
			p.setParts = changed
		}
	}
	return ""
}

func (p *prefixPlan) add(item any) {
	b, err := json.Marshal(item)
	if err != nil {
		return
	}
	p.items = append(p.items, b)
}

// record is the prelude part to lead the reply with (nil when nothing
// was appended).
func (p *prefixPlan) record(from string) *llm.Part {
	if p == nil || len(p.items) == 0 {
		return nil
	}
	pr := prelude{Base: p.base.ID, From: from, Items: p.items, Parts: p.setParts}
	b, err := json.Marshal(pr)
	if err != nil {
		return nil
	}
	return &llm.Part{Type: llm.PartProviderBlock, Meta: map[string]string{MetaPrelude: string(b)}}
}

// instructionUpdate is the developer message (or turn-scoped
// instructions) of changed parts, in the order of parts, removed ones last.
func instructionUpdate(parts []llm.SystemPart, changed map[string]string) string {
	var b strings.Builder
	b.WriteString("Update to your instructions. These parts changed since the conversation started; each replaces the part of that name given before:\n")
	for _, sp := range parts {
		if t, ok := changed[sp.Name]; ok && t != "" {
			fmt.Fprintf(&b, "\n<part name=%q>\n%s\n</part>\n", sp.Name, t)
		}
	}
	var removed []string
	for n, t := range changed {
		if t == "" {
			removed = append(removed, n)
		}
	}
	sort.Strings(removed)
	for _, n := range removed {
		fmt.Fprintf(&b, "\n<part name=%q>(no longer applies)</part>\n", n)
	}
	return strings.TrimRight(b.String(), "\n")
}

// insertItems puts appended items into input before the user messages it
// ends with (the guide: a configuration_update goes before the next user
// message), else at the end. Requests and replays place them alike, so
// a replayed item lands where it was first sent.
func insertItems(input []any, items []json.RawMessage) []any {
	if len(items) == 0 {
		return input
	}
	at := len(input)
	for at > 0 {
		m, ok := input[at-1].(map[string]any)
		if !ok || m["role"] != "user" || m["type"] != nil {
			break
		}
		at--
	}
	out := make([]any, 0, len(input)+len(items))
	out = append(out, input[:at]...)
	for _, it := range items {
		out = append(out, it)
	}
	return append(out, input[at:]...)
}

func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return string(a) == string(b)
	}
	ca, _ := json.Marshal(x)
	cb, _ := json.Marshal(y)
	return string(ca) == string(cb)
}

// apply sets the prefix-keeping fields of body.
func (p *prefixPlan) apply(body map[string]any, input []any) {
	if len(p.base.Tools) > 0 {
		tl := make([]any, len(p.base.Tools))
		for i, t := range p.base.Tools {
			tl[i] = t
		}
		body["tools"] = tl
	} else {
		delete(body, "tools")
	}
	delete(body, "instructions")
	sys := joinParts(p.base.Parts)
	if p.turn {
		if sys != "" {
			first := responses.EasyInputMessageParam{Role: responses.EasyInputMessageRoleDeveloper,
				Content: responses.EasyInputMessageContentUnionParam{OfString: param.NewOpt(sys)}}
			input = append([]any{first}, input...)
		}
		if p.turnText != "" {
			body["instructions"] = p.turnText
		}
	} else if sys != "" {
		body["instructions"] = sys
	}
	body["input"] = insertItems(input, p.items)
	if r, ok := body["reasoning"].(map[string]any); ok {
		if p.base.Effort != "" {
			r["effort"] = p.base.Effort
		} else {
			delete(r, "effort")
		}
		if r["effort"] == "none" {
			delete(r, "summary")
		} else {
			r["summary"] = "detailed"
		}
	}
	if len(p.allowed) > 0 {
		body["tool_choice"] = responses.ToolChoiceAllowedParam{Mode: responses.ToolChoiceAllowedModeAuto, Tools: p.allowed}
	}
}

// done keeps a new base once its request succeeded.
func (p *prefixPlan) done(a *Adapter, req llm.Request) {
	if p == nil || !p.fresh || req.Store == nil {
		return
	}
	b, err := json.Marshal(p.base)
	if err == nil {
		err = req.Store.Save(a.storeKey(), b)
	}
	if err != nil {
		slog.Warn("openai: keeping the conversation prefix failed", "provider", a.id, "session", req.Session, "err", err)
	}
}

// ---------------------------------------------------------------- diagnostics

// Transformations reports how many reasoning items OpenAI said it
// withheld from requests of this adapter, by reason (input_transformations).
func (a *Adapter) Transformations() map[string]int64 {
	a.routes.mu.Lock()
	defer a.routes.mu.Unlock()
	out := map[string]int64{}
	for k, v := range a.routes.drops {
		out[k] = v
	}
	return out
}

// dropNote is the Store record of withheld reasoning in a conversation.
type dropNote struct {
	Counts map[string]int `json:"counts"`
	// Logged: a prefix_mismatch was logged for this conversation
	Logged bool   `json:"logged,omitempty"`
	Last   string `json:"last,omitempty"` // the last removed item id
}

func (a *Adapter) dropKey() string { return "openai:" + a.id + ":dropped" }

// noteTransformations counts and notes a response's input_transformations.
func (a *Adapter) noteTransformations(req llm.Request, raw []json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var ts []responses.ResponseInputTransformation
	for _, r := range raw {
		var t responses.ResponseInputTransformation
		if json.Unmarshal(r, &t) == nil && t.Reason != "" {
			ts = append(ts, t)
		}
	}
	if len(ts) == 0 {
		return
	}
	a.routes.mu.Lock()
	if a.routes.drops == nil {
		a.routes.drops = map[string]int64{}
	}
	for _, t := range ts {
		a.routes.drops[t.Reason]++
	}
	a.routes.mu.Unlock()
	if req.Store == nil {
		for _, t := range ts {
			slog.Debug("openai: reasoning item withheld", "provider", a.id, "model", req.Model, "reason", t.Reason, "item", t.ID)
		}
		return
	}
	var n dropNote
	if b, ok := req.Store.Load(a.dropKey()); ok {
		_ = json.Unmarshal(b, &n)
	}
	if n.Counts == nil {
		n.Counts = map[string]int{}
	}
	for _, t := range ts {
		n.Counts[t.Reason]++
		n.Last = t.ID
		if t.Reason == "prefix_mismatch" && !n.Logged {
			n.Logged = true
			slog.Debug("openai: reasoning dropped, the conversation prefix changed", "provider", a.id, "model", req.Model,
				"agent", req.Agent, "session", req.Session, "item", t.ID)
		}
	}
	if b, err := json.Marshal(n); err == nil {
		_ = req.Store.Save(a.dropKey(), b)
	}
}

// Prompt cache diagnostics, for measuring (CREWBASE_OPENAI_CACHE_DIAG=1):
// each request of a conversation names the one before as
// prompt_cache_options.comparison_response_id and the response's
// prompt_cache_diagnostics is logged at debug level.
func cacheDiag() bool { return os.Getenv("CREWBASE_OPENAI_CACHE_DIAG") == "1" }

func (a *Adapter) lastKey() string { return "openai:" + a.id + ":last-response" }

func (a *Adapter) setCacheDiag(req llm.Request, body map[string]any) {
	if !cacheDiag() || req.Store == nil {
		return
	}
	if b, ok := req.Store.Load(a.lastKey()); ok {
		var id string
		if json.Unmarshal(b, &id) == nil && id != "" {
			body["prompt_cache_options"] = responses.ResponseNewParamsPromptCacheOptions{ComparisonResponseID: param.NewOpt(id)}
		}
	}
}

func (a *Adapter) noteCacheDiag(req llm.Request, id string, diag json.RawMessage) {
	if !cacheDiag() || req.Store == nil {
		return
	}
	if len(diag) > 0 && string(diag) != "null" {
		slog.Debug("openai: prompt cache diagnostics", "provider", a.id, "model", req.Model, "session", req.Session, "diagnostics", string(diag))
	}
	if id != "" {
		b, _ := json.Marshal(id)
		_ = req.Store.Save(a.lastKey(), b)
	}
}

// dumpBody writes a request body to $CREWBASE_OPENAI_DUMP_DIR (debugging
// and measuring only: bodies hold the whole conversation).
func dumpBody(body map[string]any) {
	dir := os.Getenv("CREWBASE_OPENAI_DUMP_DIR")
	if dir == "" {
		return
	}
	b, err := json.MarshalIndent(body, "", " ")
	if err != nil {
		return
	}
	n := dumpSeq.Add(1)
	_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("req-%04d.json", n)), b, 0o600)
}

var dumpSeq atomic.Int64

// dumpResponse writes what a response said about its input (usage,
// input_transformations, cache diagnostics) next to the request bodies.
func dumpResponse(id string, usage any, transformations []json.RawMessage, diag json.RawMessage) {
	dir := os.Getenv("CREWBASE_OPENAI_DUMP_DIR")
	if dir == "" {
		return
	}
	b, err := json.MarshalIndent(map[string]any{"id": id, "usage": usage, "input_transformations": transformations,
		"prompt_cache_diagnostics": diag}, "", " ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("resp-%04d.json", dumpSeq.Load())), b, 0o600)
}

// refusedPrefix reports whether err says the provider does not take an
// item append-only requests use.
func refusedPrefix(err error) bool {
	var le *llm.Error
	if !errors.As(err, &le) || le.Kind != llm.ErrBadRequest {
		return false
	}
	m := strings.ToLower(le.Message)
	for _, w := range []string{"configuration_update", "additional_tools", "allowed_tools", "developer"} {
		if strings.Contains(m, w) {
			return true
		}
	}
	return false
}
