package router

import (
	"strings"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/llm"
)

// Task kinds used for routing by task type (M-5).
const (
	TaskRoutine   = "routine"
	TaskSummarize = "summarize" // web page summaries, log cleanup
	TaskCode      = "code"
	TaskLongDoc   = "long-doc"
	TaskDefault   = ""
)

// Route sends a task kind to a model. Routes only apply when the model is
// usable; otherwise the agent's normal order is used.
type Route struct {
	Task  string `json:"task"`
	Model string `json:"model"` // provider/model[@effort]
}

var (
	summarizeWords = []string{"summarize", "summarise", "summary", "tl;dr", "clean up the log", "log cleanup", "摘要", "总结", "概括", "日志清洗", "清洗日志"}
	codeWords      = []string{"implement", "refactor", "fix the bug", "write a test", "unit test", "code review", "编码", "实现", "重构", "修复", "写测试", "代码"}
)

// Classify guesses the task kind of an input.
func Classify(in agent.Input) string {
	if strings.HasPrefix(in.Tag, "routine:") {
		return TaskRoutine
	}
	t := strings.ToLower(in.Text)
	if len([]rune(in.Text)) > 60000 {
		return TaskLongDoc
	}
	for _, w := range summarizeWords {
		if strings.Contains(t, w) {
			return TaskSummarize
		}
	}
	for _, w := range codeWords {
		if strings.Contains(t, w) {
			return TaskCode
		}
	}
	return TaskDefault
}

// SelectFor implements agentloop.InputSelector.
func (r *Router) SelectFor(a *agent.Agent, in agent.Input) (llm.ModelRef, bool) {
	if task := Classify(in); task != TaskDefault {
		r.mu.Lock()
		routes := append([]Route(nil), r.routes...)
		r.mu.Unlock()
		for _, rt := range routes {
			if rt.Task != task {
				continue
			}
			ref, err := llm.ParseRef(rt.Model)
			if err == nil && r.usable(ref) {
				return ref, true
			}
		}
	}
	return r.Select(a)
}

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
