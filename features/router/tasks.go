package router

import (
	"context"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/llm"
)

// Task kinds used for routing by task type (M-5).
const (
	TaskRoutine   = "routine"
	TaskSummarize = "summarize" // web page summaries, log cleanup
	TaskCode      = "code"
	TaskLongDoc   = "long-doc"
	TaskTitle     = "title" // short session topics (cheap model)
	TaskBrief     = "brief" // an agent's instructions written from its name
	// Media tasks route to image / video models (features/media). They are
	// never picked by Classify: the generation tools ask for them.
	TaskImage   = "image"
	TaskVideo   = "video"
	TaskDefault = ""
)

// Tasks lists every routable task kind (the settings UI offers these).
var Tasks = []string{TaskTitle, TaskBrief, TaskRoutine, TaskSummarize, TaskCode, TaskLongDoc, TaskImage, TaskVideo}

// TaskKind is the model kind a task needs: image / video for the media
// tasks, chat for everything else.
func TaskKind(task string) string {
	switch task {
	case TaskImage:
		return llm.KindImage
	case TaskVideo:
		return llm.KindVideo
	}
	return llm.KindChat
}

// Route sends a task kind to a model. Routes only apply when the model is
// usable; otherwise the agent's normal order is used.
type Route struct {
	Task  string `json:"task"`
	Model string `json:"model"` // provider/model[@effort]
}

var (
	// Only an explicit request to summarise counts, at the start of the
	// message ("总结一下这篇…", "Summarize this page") — not a passing
	// mention ("implement the summary view").
	summarizeLead = []string{"summarize", "summarise", "tl;dr", "clean up this log", "clean up the log", "总结", "概括", "摘要一下", "帮我总结", "帮我概括", "清洗日志", "日志清洗"}
	codeLead      = []string{"implement", "refactor", "fix the bug", "write a test", "write tests", "实现", "重构", "修复", "写测试", "写单测"}
)

func leads(t string, words []string) bool {
	t = strings.TrimSpace(strings.ToLower(t))
	for _, w := range words {
		if strings.HasPrefix(t, w) {
			return true
		}
		// allow a short polite prefix: "please summarize", "请帮我总结"
		if i := strings.Index(t, w); i >= 0 && i <= 12 {
			return true
		}
	}
	return false
}

// Classify guesses the task kind of an input. It is deliberately
// conservative: unclear inputs stay on the agent's own model order.
func Classify(in agent.Input) string {
	if strings.HasPrefix(in.Tag, "routine:") {
		return TaskRoutine
	}
	if len([]rune(in.Text)) > 60000 {
		return TaskLongDoc
	}
	if leads(in.Text, summarizeLead) {
		return TaskSummarize
	}
	if leads(in.Text, codeLead) {
		return TaskCode
	}
	return TaskDefault
}

// local reports whether ref is served on this machine.
func (r *Router) local(ref llm.ModelRef) bool {
	ad, ok := r.llms.Get(ref.Provider)
	return ok && ad.Kind() == "local"
}

// localOnly reports whether every model of the agent runs locally: such an
// agent's content must never be routed to a remote provider.
func (r *Router) localOnly(a *agent.Agent) bool {
	ms := a.Spec().Models
	if len(ms) == 0 {
		return false
	}
	for _, m := range ms {
		if !r.local(m) {
			return false
		}
	}
	return true
}

// allowed reports whether a route to ref may carry a's content.
func (r *Router) allowed(a *agent.Agent, ref llm.ModelRef) bool {
	if a == nil || !r.localOnly(a) {
		return true
	}
	return r.local(ref)
}

// SelectFor implements agentloop.InputSelector.
func (r *Router) SelectFor(a *agent.Agent, in agent.Input) (llm.ModelRef, bool) {
	if task := Classify(in); task != TaskDefault {
		if ref, ok := r.RouteForAgent(task, a); ok {
			return ref, true
		}
	}
	return r.Select(a)
}

// RouteFor returns the first usable model routed for task.
func (r *Router) RouteFor(task string) (llm.ModelRef, bool) { return r.RouteForAgent(task, nil) }

// RouteForAgent is RouteFor restricted to models allowed to see a's
// content (a local-only agent only routes to local models).
func (r *Router) RouteForAgent(task string, a *agent.Agent) (llm.ModelRef, bool) {
	r.mu.Lock()
	routes := append([]Route(nil), r.routes...)
	r.mu.Unlock()
	for _, rt := range routes {
		if rt.Task != task {
			continue
		}
		if ref, err := llm.ParseRef(rt.Model); err == nil && r.usable(ref) && r.allowed(a, ref) && r.kindOf(ref) == TaskKind(task) {
			return ref, true
		}
	}
	return llm.ModelRef{}, false
}

// kindOf is the model's output kind (from its provider's listing, else
// its id), so a chat task never lands on an image model and back.
func (r *Router) kindOf(ref llm.ModelRef) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return r.llms.KindOf(ctx, ref)
}

// Allowed reports whether a's content may go to ref (a local-only agent
// only uses local models).
func (r *Router) Allowed(a *agent.Agent, ref llm.ModelRef) bool { return r.allowed(a, ref) }

// SetRoutes replaces the task routes.
func (r *Router) SetRoutes(routes []Route) {
	r.mu.Lock()
	r.routes = append([]Route(nil), routes...)
	r.mu.Unlock()
}

// Routes returns the task routes.
func (r *Router) Routes() []Route {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Route(nil), r.routes...)
}
