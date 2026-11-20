package media

// Background video jobs: video_generate starts the provider's job and
// returns at once; the job waits for the video (polling inside the
// adapter), saves it and tells the agent with a "job:" input, like a bash
// background job. Jobs belong to the agent and end with the host (a
// restart loses a running job; the provider may still bill it).

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

// MaxVideoJobsPerAgent caps concurrent video jobs of one agent.
const MaxVideoJobsPerAgent = 2

const keepFinishedJobs = 20

type videoJob struct {
	ID      string
	Agent   string
	Session *session.Session
	Args    VideoArgs
	Model   llm.ModelRef
	Started time.Time
	cancel  context.CancelFunc

	mu       sync.Mutex
	status   string // the provider's (queued, in_progress, …), then done | failed
	percent  int
	stopped  bool // ended by deleting the agent
	saved    Saved
	err      error
	finished time.Time
}

func (j *videoJob) state() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.stopped {
		return "stopped"
	}
	switch j.status {
	case "done":
		return fmt.Sprintf("done in %s: %s (%s)", j.finished.Sub(j.Started).Round(time.Second), j.saved.Path, fmtSize(j.saved.Size))
	case "failed":
		return "failed: " + j.err.Error()
	}
	s := "running " + time.Since(j.Started).Round(time.Second).String()
	if j.status != "" {
		s += " (" + j.status
		if j.percent > 0 {
			s += fmt.Sprintf(", %d%%", j.percent)
		}
		s += ")"
	}
	return s
}

func (j *videoJob) running() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.finished.IsZero()
}

type jobs struct {
	s   *Service
	mu  sync.Mutex
	seq int
	all map[string]*videoJob
}

func (js *jobs) forAgent(id string) []*videoJob {
	js.mu.Lock()
	defer js.mu.Unlock()
	var out []*videoJob
	for _, j := range js.all {
		if j.Agent == id {
			out = append(out, j)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Started.Before(out[b].Started) })
	return out
}

func (js *jobs) start(ctx context.Context, call tools.Call, a VideoArgs) (tools.Result, error) {
	running := 0
	for _, j := range js.forAgent(call.Agent) {
		if j.running() {
			running++
		}
	}
	if running >= MaxVideoJobsPerAgent {
		return tools.Errorf("%d videos are already being generated; wait for one to finish (video_status)", running), nil
	}
	s := js.s
	ag := s.agentOf(call.Agent)
	ref, err := s.Pick(ctx, ag, llm.KindVideo, a.Model, false)
	if err != nil {
		return tools.Errorf("%v", err), nil
	}
	var img *llm.InputImage
	if a.Image != "" {
		name, mime, data, err := ReadInput(call.Workspace, a.Image)
		if err != nil {
			return tools.Errorf("%v", err), nil
		}
		img = &llm.InputImage{Name: name, Mime: mime, Data: data}
	}
	ad, _ := s.llms.Get(ref.Provider)
	gen, ok := ad.(llm.VideoGenerator)
	if !ok {
		return tools.Errorf("%v", errNoModel{llm.KindVideo}), nil
	}
	js.mu.Lock()
	js.seq++
	id := fmt.Sprintf("vid%d", js.seq)
	js.mu.Unlock()
	jctx, cancel := context.WithTimeout(context.Background(), s.vidMax)
	j := &videoJob{ID: id, Agent: call.Agent, Session: call.Session, Args: a, Model: ref, Started: s.now(), cancel: cancel, status: "queued"}
	js.mu.Lock()
	js.all[id] = j
	js.mu.Unlock()
	js.prune(call.Agent)
	j.event("started", "")
	workspace := call.Workspace
	s.c.Go(func(hostCtx context.Context) {
		stop := context.AfterFunc(hostCtx, cancel)
		defer stop()
		defer cancel()
		req := llm.VideoRequest{Model: ref.Model, Prompt: a.Prompt, Seconds: a.Seconds, Aspect: a.Aspect, Resolution: a.Resolution, Image: img}
		v, err := gen.GenerateVideo(jctx, req, func(p llm.VideoProgress) {
			j.mu.Lock()
			j.status, j.percent = p.Status, p.Percent
			j.mu.Unlock()
		})
		var saved Saved
		if err == nil {
			saved, err = Save(workspace, llm.KindVideo, a.Prompt, v.Body, MaxVideoBytes, s.now())
			v.Body.Close()
			if err == nil {
				sid := ""
				if j.Session != nil {
					sid = j.Session.ID()
				}
				Created.Emit(s.c, CreatedFile{Agent: j.Agent, Session: sid, Workspace: workspace, Kind: llm.KindVideo, Prompt: a.Prompt, Saved: saved})
			}
		}
		if err != nil && jctx.Err() == context.DeadlineExceeded && hostCtx.Err() == nil {
			err = fmt.Errorf("no video after %s; the provider may still finish and bill it", s.vidMax)
		}
		j.mu.Lock()
		stopped := j.stopped
		j.finished = s.now()
		switch {
		case stopped:
		case err != nil:
			j.status, j.err = "failed", err
		default:
			j.status, j.saved = "done", saved
		}
		j.mu.Unlock()
		if stopped || hostCtx.Err() != nil {
			return
		}
		if err != nil {
			j.event("failed", err.Error())
		} else {
			record(j.Session, j.Agent, Generated{Kind: llm.KindVideo, Path: saved.Path, Mime: saved.Mime, Size: saved.Size, Prompt: short(a.Prompt, 500), Model: ref.String(), Call: call.ID, Job: id})
			j.event("done", "")
		}
		js.report(j)
	})
	return tools.Result{Content: fmt.Sprintf("Started %s: generating the video with %s in the background (usually a few minutes). "+
		"You will get a message with the saved file when it is done, so you do not need to poll; tell the user it is on its way and carry on or end your turn. "+
		"video_status %s shows its progress.", id, ref, id), Data: map[string]string{"job": id}}, nil
}

func (j *videoJob) event(status, errMsg string) {
	if j.Session == nil {
		return
	}
	j.mu.Lock()
	ev := JobEvent{Job: j.ID, Status: status, Prompt: short(j.Args.Prompt, 500), Model: j.Model.String(), Seconds: j.Args.Seconds,
		Aspect: j.Args.Aspect, Resolution: j.Args.Resolution, Path: j.saved.Path, Error: errMsg}
	j.mu.Unlock()
	_, _ = j.Session.Append(TypeVideoJob, j.Agent, ev)
}

func (js *jobs) prune(agentID string) {
	var finished []*videoJob
	for _, j := range js.forAgent(agentID) {
		if !j.running() {
			finished = append(finished, j)
		}
	}
	if extra := len(finished) - keepFinishedJobs; extra > 0 {
		js.mu.Lock()
		for _, j := range finished[:extra] {
			delete(js.all, j.ID)
		}
		js.mu.Unlock()
	}
}

// report tells the agent its video is ready (or failed).
func (js *jobs) report(j *videoJob) {
	reg, ok := kernel.Lookup[*agent.Registry](js.s.c, agent.ServiceKey)
	if !ok {
		return
	}
	a, ok := reg.Get(j.Agent)
	if !ok {
		return
	}
	j.mu.Lock()
	status, saved, err := j.status, j.saved, j.err
	j.mu.Unlock()
	var b strings.Builder
	if status == "done" {
		fmt.Fprintf(&b, "[后台视频生成完成] %s「%s」：已保存到 %s（%s，模型 %s）。视频已经显示在对话里。\n", j.ID, short(j.Args.Prompt, 60), saved.Path, fmtSize(saved.Size), j.Model)
		b.WriteString("如果它是你计划里的一步，就接着做下一步；否则简短告诉用户视频好了即可。")
	} else {
		fmt.Fprintf(&b, "[后台视频生成失败] %s「%s」：%v\n", j.ID, short(j.Args.Prompt, 60), err)
		b.WriteString("告诉用户失败的原因；不要自动重试（每次生成都要付费），除非用户要求。")
	}
	in := agent.Input{Text: b.String(), From: "job", Tag: "job:" + j.ID}
	if j.Session != nil {
		in.Session = j.Session.ID()
	}
	_, _ = a.Enqueue(in)
}

func (js *jobs) describe(agentID, id string) string {
	list := js.forAgent(agentID)
	if len(list) == 0 {
		return "No video jobs."
	}
	var b strings.Builder
	for _, j := range list {
		if id != "" && j.ID != id {
			continue
		}
		fmt.Fprintf(&b, "%s  %s  %s  %s\n", j.ID, j.state(), j.Model, short(j.Args.Prompt, 80))
	}
	if b.Len() == 0 {
		return fmt.Sprintf("No video job %q.", id)
	}
	return b.String()
}

// stopAll ends an agent's jobs (agent deleted).
func (js *jobs) stopAll(agentID string) {
	for _, j := range js.forAgent(agentID) {
		j.mu.Lock()
		if j.finished.IsZero() {
			j.stopped = true
		}
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
