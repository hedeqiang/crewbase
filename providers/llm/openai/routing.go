package openai

// Which API a request goes through.
//
// OpenAI recommends the Responses API, and its newer reasoning models
// need it once function tools and a reasoning effort are combined (Chat
// Completions answers 400 "Function tools with reasoning_effort are not
// supported for gpt-6.1-sol in /v1/chat/completions. To use function
// tools, use /v1/responses ..."). The per-provider "responses" option:
//
//   - auto (default for the openai preset; magpie sets it): OpenAI
//     reasoning-family models (responsesModel) and native computer use go
//     through Responses, everything else through Chat Completions;
//   - always: every chat request goes through Responses;
//   - never: Chat Completions only (the default for other presets).
//
// Fallbacks, remembered per provider for the life of the process:
//
//   - a Responses request answered 404 / 405 with no sign of a model
//     error (a gateway without the endpoint) is sent once more through
//     Chat Completions, and the provider stops using Responses;
//   - a Chat Completions request answered with the 400 above is sent once
//     more through Responses, and that model keeps using it (unless the
//     provider is explicitly "never", or Responses is known to be missing).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"

	sdk "github.com/openai/openai-go/v3"

	"github.com/hedeqiang/crewbase/core/llm"
)

// ResponsesMode is the "responses" provider option.
type ResponsesMode string

const (
	ResponsesAuto   ResponsesMode = "auto"
	ResponsesAlways ResponsesMode = "always"
	ResponsesNever  ResponsesMode = "never"
)

// UnmarshalJSON takes "auto" | "always" | "never", or a bool as older
// configs wrote it (true = auto, false = never).
func (m *ResponsesMode) UnmarshalJSON(b []byte) error {
	var on bool
	if json.Unmarshal(b, &on) == nil {
		*m = ResponsesNever
		if on {
			*m = ResponsesAuto
		}
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("responses: want auto, always, never or a bool")
	}
	*m = ResponsesMode(strings.ToLower(strings.TrimSpace(s)))
	return nil
}

// routeState is what an adapter learned about its endpoints. magpie
// rebuilds its inner adapter whenever the catalog changes and hands the
// state on (InheritRoutes), so it lives behind a pointer.
type routeState struct {
	mu          sync.Mutex
	noResponses bool             // the endpoint is missing (404 / 405)
	needs       map[string]bool  // models Chat Completions refused (400 above)
	noComputer  map[string]bool  // models whose requests refused the computer tool
	noPrefix    map[string]bool  // models whose requests refused append-only items (prefix.go)
	drops       map[string]int64 // reasoning items withheld, by reason (input_transformations)
}

func (r *routeState) prefixRefused(model string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.noPrefix[model]
}

func (r *routeState) setPrefixRefused(model string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.noPrefix == nil {
		r.noPrefix = map[string]bool{}
	}
	r.noPrefix[model] = true
}

func (r *routeState) computerRefused(model string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.noComputer[model]
}

func (r *routeState) setComputerRefused(model string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.noComputer == nil {
		r.noComputer = map[string]bool{}
	}
	r.noComputer[model] = true
}

func (r *routeState) responsesMissing() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.noResponses
}

func (r *routeState) setResponsesMissing() {
	r.mu.Lock()
	r.noResponses = true
	r.needs = nil
	r.mu.Unlock()
}

func (r *routeState) needsResponses(model string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.needs[model]
}

func (r *routeState) setNeedsResponses(model string, on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.needs == nil {
		r.needs = map[string]bool{}
	}
	if on {
		r.needs[model] = true
	} else {
		delete(r.needs, model)
	}
}

// InheritRoutes keeps what old learned about its endpoints (magpie hands
// it from one catalog rebuild to the next).
func (a *Adapter) InheritRoutes(old *Adapter) {
	if old != nil && old.routes != nil {
		a.routes = old.routes
	}
}

// ResponsesMode reports the provider's "responses" option in effect.
func (a *Adapter) ResponsesMode() ResponsesMode { return a.respMode }

// useResponses reports whether req goes through the Responses API.
func (a *Adapter) useResponses(req llm.Request) bool {
	if a.routes.responsesMissing() {
		return false
	}
	return a.routesToResponses(req.Model) || a.computerTool(req)
}

// routesToResponses: requests for model go through Responses (computer
// use aside), as far as the provider's option and what was learned say.
func (a *Adapter) routesToResponses(model string) bool {
	if a.routes.needsResponses(model) {
		return true
	}
	switch a.respMode {
	case ResponsesAlways:
		return true
	case ResponsesAuto:
		return a.responsesModel(model)
	}
	return false
}

// openAIReasoningID matches the bare ids of OpenAI's reasoning models.
var openAIReasoningID = regexp.MustCompile(`^(o\d|gpt-[5-9]|codex)`)

// OpenAIFamily reports whether model id names an OpenAI model: a gateway
// prefix of OpenAI's ("openai/…", "codex/…", "<anything>-openai/…"), or a
// bare GPT-5 / GPT-6 / o-series id.
func OpenAIFamily(model string) bool {
	m := strings.ToLower(model)
	segs := strings.Split(m, "/")
	for _, p := range segs[:len(segs)-1] {
		if p == "openai" || p == "codex" || strings.Contains(p, "openai") {
			return true
		}
	}
	return len(segs) == 1 && openAIReasoningID.MatchString(m)
}

// responsesModel: an OpenAI model that reasons. A capability list (the
// provider's static models, magpie's catalog) decides when it has one;
// otherwise the id does.
func (a *Adapter) responsesModel(model string) bool {
	if !OpenAIFamily(model) {
		return false
	}
	if s, ok := findSpec(a.static, model); ok && len(s.Capabilities) > 0 {
		return hasCap(s.Capabilities, "reasoning")
	}
	base := strings.ToLower(model)
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	return openAIReasoningID.MatchString(base)
}

// errNoEndpoint wraps the error of a Responses request the server has no
// route for.
type errNoEndpoint struct{ err error }

func (e *errNoEndpoint) Error() string { return e.err.Error() }
func (e *errNoEndpoint) Unwrap() error { return e.err }

// missingEndpoint reports whether err (from the SDK) says the server has
// no /responses route: 405, or 404 whose body is not about a model (OpenAI
// and magpie answer an unknown model with 404 too: "magpie knows no
// model …", code model_not_found).
func missingEndpoint(err error) bool {
	var ae *sdk.Error
	if !errors.As(err, &ae) {
		return false
	}
	switch ae.StatusCode {
	case http.StatusMethodNotAllowed:
		return true
	case http.StatusNotFound:
		msg, code, _ := parseErrorBody([]byte(ae.RawJSON()))
		if ae.Message != "" {
			msg = ae.Message
		}
		return !strings.Contains(strings.ToLower(msg+" "+code+" "+ae.Code), "model")
	}
	return false
}

// streamResponsesOrChat sends req through Responses, falling back to Chat
// Completions once (and for good) when the endpoint is missing.
func (a *Adapter) streamResponsesOrChat(ctx context.Context, req llm.Request, onChunk func(llm.Chunk) error) (llm.Response, error) {
	resp, err := a.streamResponses(ctx, req, onChunk)
	var ne *errNoEndpoint
	if errors.As(err, &ne) {
		a.routes.setResponsesMissing()
		return a.streamChat(ctx, req, onChunk)
	}
	return resp, err
}

// needsResponsesAPI reports whether a Chat Completions error says the
// request has to go through the Responses API.
func needsResponsesAPI(err error) bool {
	var le *llm.Error
	if !errors.As(err, &le) || le.Kind != llm.ErrBadRequest {
		return false
	}
	m := strings.ToLower(le.Message)
	return strings.Contains(m, "/v1/responses") || strings.Contains(m, "responses api") || strings.Contains(m, "use /responses")
}

// canSelfHeal: a Chat Completions failure that Responses would fix, on a
// provider that may have it.
func (a *Adapter) canSelfHeal(err error) bool {
	if a.respExplicit && a.respMode == ResponsesNever {
		return false
	}
	return needsResponsesAPI(err) && !a.routes.responsesMissing()
}

// selfHeal retries a request Chat Completions refused through Responses.
// When the provider has no Responses endpoint either, the original error
// stands.
func (a *Adapter) selfHeal(ctx context.Context, req llm.Request, onChunk func(llm.Chunk) error, chatErr error) (llm.Response, error) {
	a.routes.setNeedsResponses(req.Model, true)
	resp, err := a.streamResponses(ctx, req, onChunk)
	var ne *errNoEndpoint
	if errors.As(err, &ne) {
		a.routes.setResponsesMissing()
		return llm.Response{}, chatErr
	}
	return resp, err
}
