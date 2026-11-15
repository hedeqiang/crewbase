package toolsbasic

// Background jobs: bash {background: true} starts the command and returns
// at once, so a build, a test suite or a dev server does not block the
// agent — it keeps answering and working meanwhile. job_output reads what
// the command has printed so far, job_stop ends it, job_list shows them.
// When a job ends on its own, the result comes back to the conversation as
// a new message, so the agent reacts to it without polling.
//
// Jobs run under the same sandbox policy as foreground commands, belong to
// the agent that started them, and end with the host (they do not survive
// a restart).

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/shell"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

// Job limits.
const (
	MaxJobsPerAgent   = 5
	DefaultJobTimeout = 30 * time.Minute
	MaxJobTimeout     = 2 * time.Hour
	jobLogBytes       = 256 << 10 // kept per job (the latest output)
	keepFinished      = 20        // finished jobs remembered per agent
)

type job struct {
	ID      string
	Agent   string
	Session string
	Command string
	Started time.Time

	cancel context.CancelFunc

	mu       sync.Mutex
	log      []byte // latest output, interleaved, at most jobLogBytes
	dropped  int64  // bytes dropped from the front
	done     bool
	stopped  bool // ended by job_stop
	result   shell.RunResult
	err      error
	finished time.Time
}

func (j *job) write(_ string, p []byte) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.log = append(j.log, p...)
	if over := len(j.log) - jobLogBytes; over > 0 {
		j.log = append(j.log[:0], j.log[over:]...)
		j.dropped += int64(over)
	}
}

// tail returns the last n bytes of output (whole lines when possible).
func (j *job) tail(n int) (string, int64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	b := j.log
	skipped := j.dropped
	if len(b) > n {
		cut := len(b) - n
		if i := strings.IndexByte(string(b[cut:]), '\n'); i >= 0 && i < 200 {
			cut += i + 1
		}
		skipped += int64(cut)
		b = b[cut:]
	}
	return string(b), skipped
}

func (j *job) state() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	switch {
	case !j.done:
		return "running " + time.Since(j.Started).Round(time.Second).String()
	case j.stopped:
		return "stopped"
	case j.err != nil:
		return "failed to run: " + j.err.Error()
	case j.result.TimedOut:
		return "timed out"
	}
	return fmt.Sprintf("exited %d after %s", j.result.ExitCode, j.result.Duration.Round(time.Second))
}

type jobs struct {
	b   *basic
	c   *kernel.Context
	mu  sync.Mutex
	seq int
	all map[string]*job // id → job
}

func (js *jobs) forAgent(agentID string) []*job {
	js.mu.Lock()
	defer js.mu.Unlock()
	var out []*job
	for _, j := range js.all {
		if j.Agent == agentID {
			out = append(out, j)
		}
	}
	// oldest first
	for i := 1; i < len(out); i++ {
		for k := i; k > 0 && out[k].Started.Before(out[k-1].Started); k-- {
			out[k], out[k-1] = out[k-1], out[k]
		}
	}
	return out
}

func (js *jobs) get(agentID, id string) (*job, bool) {
	js.mu.Lock()
	defer js.mu.Unlock()
	j, ok := js.all[id]
	if !ok || j.Agent != agentID {
		return nil, false
	}
	return j, true
}

// start runs req in the background for call.
func (js *jobs) start(call tools.Call, command string, req shell.ExecRequest) (tools.Result, error) {
	running := 0
	for _, j := range js.forAgent(call.Agent) {
		j.mu.Lock()
		if !j.done {
			running++
		}
		j.mu.Unlock()
	}
	if running >= MaxJobsPerAgent {
		return tools.Errorf("%d background jobs are already running; stop one (job_stop) or wait for it", running), nil
	}
	js.mu.Lock()
	js.seq++
	id := fmt.Sprintf("job%d", js.seq)
	js.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{ID: id, Agent: call.Agent, Command: command, Started: time.Now(), cancel: cancel}
	if call.Session != nil {
		j.Session = call.Session.ID()
	}
	req.Output = j.write
	js.mu.Lock()
	js.all[id] = j
	js.mu.Unlock()
	js.prune(call.Agent)
	js.c.Go(func(hostCtx context.Context) {
		// the host's shutdown ends jobs too
		stop := context.AfterFunc(hostCtx, cancel)
		defer stop()
		res, err := js.b.sh.Exec(ctx, req)
		j.mu.Lock()
		j.done, j.result, j.err, j.finished = true, res, err, time.Now()
		stopped := j.stopped
		j.mu.Unlock()
		cancel()
		if !stopped && hostCtx.Err() == nil {
			js.report(j)
		}
	})
	return tools.Text(fmt.Sprintf("Started %s in the background: %s\n"+
		"It keeps running while you work. Use job_output %s to see its output so far and job_stop %s to end it. "+
		"When it finishes you will get a message with the result, so you do not need to poll; carry on or end your turn.",
		id, oneLine(command, 200), id, id)), nil
}

// prune forgets the oldest finished jobs of an agent.
func (js *jobs) prune(agentID string) {
	var finished []*job
	for _, j := range js.forAgent(agentID) {
		j.mu.Lock()
		if j.done {
			finished = append(finished, j)
		}
		j.mu.Unlock()
	}
	if extra := len(finished) - keepFinished; extra > 0 {
		js.mu.Lock()
		for _, j := range finished[:extra] {
			delete(js.all, j.ID)
		}
		js.mu.Unlock()
	}
}

// report tells the agent a job ended on its own.
func (js *jobs) report(j *job) {
	reg, ok := kernel.Lookup[*agent.Registry](js.c, agent.ServiceKey)
	if !ok {
		return
	}
	a, ok := reg.Get(j.Agent)
	if !ok {
		return
	}
	out, skipped := j.tail(6000)
	var b strings.Builder
	fmt.Fprintf(&b, "[后台命令结束] %s `%s`：%s。\n", j.ID, oneLine(j.Command, 200), j.state())
	if strings.TrimSpace(out) != "" {
		if skipped > 0 {
			fmt.Fprintf(&b, "最后的输出（前面还有 %d 字节，用 job_output 查看）：\n", skipped)
		} else {
			b.WriteString("输出：\n")
		}
		b.WriteString("```\n" + strings.TrimRight(out, "\n") + "\n```\n")
	}
	if j.result.Blocked {
		b.WriteString("沙箱拦截了这个命令的某个操作；不要原样重试。\n")
	}
	b.WriteString("如果它是你手头计划里的一步，就接着做下一步；失败就根据输出找原因。如果没有后续步骤，简短告诉用户结果即可——不要为了找事做去翻工作区。")
	_, _ = a.Enqueue(agent.Input{Session: j.Session, Text: b.String(), From: "job", Tag: "job:" + j.ID})
}

func (js *jobs) tools() []*tools.Tool {
	return []*tools.Tool{
		{
			Name:        "job_output",
			Description: "Show what a background job (bash with background: true) has printed so far, and whether it is still running.",
			Params: tools.Schema(map[string]any{
				"id":        tools.Prop("string", "Job id, e.g. job3."),
				"max_bytes": tools.Prop("integer", "How much of the latest output to return (default 8000)."),
			}, "id"),
			Category: tools.CategoryNone,
			Execute: func(_ context.Context, call tools.Call) (tools.Result, error) {
				j, ok := js.get(call.Agent, call.String("id"))
				if !ok {
					return tools.Errorf("no background job %q (job_list shows yours)", call.String("id")), nil
				}
				var a struct {
					MaxBytes int `json:"max_bytes"`
				}
				_ = call.Decode(&a)
				if a.MaxBytes <= 0 || a.MaxBytes > 64000 {
					a.MaxBytes = 8000
				}
				out, skipped := j.tail(a.MaxBytes)
				var b strings.Builder
				fmt.Fprintf(&b, "%s: %s\n", j.ID, j.state())
				if skipped > 0 {
					fmt.Fprintf(&b, "(%d earlier bytes not shown)\n", skipped)
				}
				if out == "" {
					b.WriteString("(no output yet)\n")
				} else {
					b.WriteString(out)
				}
				return tools.Text(b.String()), nil
			},
		},
		{
			Name:        "job_stop",
			Description: "Stop a background job (and everything it started).",
			Params:      tools.Schema(map[string]any{"id": tools.Prop("string", "Job id.")}, "id"),
			Category:    tools.CategoryNone,
			Execute: func(_ context.Context, call tools.Call) (tools.Result, error) {
				j, ok := js.get(call.Agent, call.String("id"))
				if !ok {
					return tools.Errorf("no background job %q", call.String("id")), nil
				}
				j.mu.Lock()
				done := j.done
				if !done {
					j.stopped = true
				}
				j.mu.Unlock()
				if done {
					return tools.Text(j.ID + " had already ended: " + j.state()), nil
				}
				j.cancel()
				return tools.Text("Stopped " + j.ID + "."), nil
			},
		},
		{
			Name:        "job_list",
			Description: "List your background jobs and their state.",
			Params:      tools.Schema(map[string]any{}),
			Category:    tools.CategoryNone,
			Execute: func(_ context.Context, call tools.Call) (tools.Result, error) {
				list := js.forAgent(call.Agent)
				if len(list) == 0 {
					return tools.Text("No background jobs."), nil
				}
				var b strings.Builder
				for _, j := range list {
					fmt.Fprintf(&b, "%s  %s  %s\n", j.ID, j.state(), oneLine(j.Command, 120))
				}
				return tools.Text(b.String()), nil
			},
		},
	}
}

// stopAll ends an agent's jobs (agent deleted).
func (js *jobs) stopAll(agentID string) {
	for _, j := range js.forAgent(agentID) {
		j.mu.Lock()
		j.stopped = true
		j.mu.Unlock()
		j.cancel()
	}
	js.mu.Lock()
	for id, j := range js.all {
		if j.Agent == agentID {
			delete(js.all, id)
		}
	}
	js.mu.Unlock()
}
