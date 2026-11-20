// Package media is the generation service (@crewbase/media): agents create
// images and videos by calling image / video models directly through
// their providers (llm.ImageGenerator / llm.VideoGenerator: OpenAI, Gemini,
// magpie). Which model: the tool's `model` argument, else the router's
// "image" / "video" task route, else the first available model of that
// kind (a local-only agent is never sent to a cloud model).
//
// Results are saved in the agent's workspace (generated/<time>-<slug>.<ext>,
// type checked from the bytes, size-capped, never replacing a file), the
// model only gets their paths, and a session event (media/generated) lets
// the conversation show them inline. Videos take minutes, so they run in
// the background like bash jobs: the tool returns at once and the result
// comes back to the conversation as a "job:" input.
//
// Tools appear only while a model of their kind exists: image_generate,
// image_edit (category write) and video_generate (category spend: Ask by
// default, it is billed per second) with video_status.
package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/router"
	"github.com/hedeqiang/crewbase/kernel"
)

// ServiceKey is the media service.
const ServiceKey = "media"

// Session events.
const (
	// TypeGenerated records one generated file (Generated).
	TypeGenerated = "media/generated"
	// TypeVideoJob records a background video job's state (JobEvent).
	TypeVideoJob = "media/video-job"
)

// Generated is the data of a media/generated event.
type Generated struct {
	Kind   string `json:"kind"` // image | video
	Path   string `json:"path"`
	Mime   string `json:"mime"`
	Size   int64  `json:"size"`
	Prompt string `json:"prompt,omitempty"`
	Model  string `json:"model"` // provider/model
	Call   string `json:"call,omitempty"`
	Job    string `json:"job,omitempty"`
}

// JobEvent is the data of a media/video-job event.
type JobEvent struct {
	Job        string `json:"job"`
	Status     string `json:"status"` // started | done | failed
	Prompt     string `json:"prompt,omitempty"`
	Model      string `json:"model,omitempty"`
	Seconds    int    `json:"seconds,omitempty"`
	Aspect     string `json:"aspect,omitempty"`
	Resolution string `json:"resolution,omitempty"`
	Path       string `json:"path,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Config of the plugin.
type Config struct {
	// VideoTimeout bounds one video job (default 20m, at most 2h).
	VideoTimeout string `json:"videoTimeout"`
	// SyncInterval is how often the available models are rechecked
	// (tools appear / disappear); default 1m.
	SyncInterval string `json:"syncInterval"`
}

// Service is the media service.
type Service struct {
	c      *kernel.Context
	llms   *llm.Registry
	reg    *tools.Registry
	now    func() time.Time
	vidMax time.Duration

	mu    sync.Mutex
	subs  map[string]*kernel.Context // kind → tools registered while models of it exist
	avail map[string][]llm.ModelRef
	jobs  *jobs
}

// errNoModel is returned when no model of a kind can be used.
type errNoModel struct{ kind string }

func (e errNoModel) Error() string {
	return fmt.Sprintf("no %s model is available; ask the user to add one (Settings › Models: an OpenAI or Google Gemini key, or magpie with an %s model)", e.kind, e.kind)
}

// Available lists the enabled models of kind whose provider can generate
// that kind, in provider order.
func (s *Service) Available(ctx context.Context, kind string) []llm.ModelRef {
	var out []llm.ModelRef
	for _, ad := range s.llms.List() {
		if !canGenerate(ad, kind) {
			continue
		}
		mctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		ms, err := ad.Models(mctx)
		cancel()
		if err != nil {
			continue
		}
		for _, m := range ms {
			if m.Enabled && m.OutputKind() == kind {
				out = append(out, llm.ModelRef{Provider: ad.ID(), Model: m.ID})
			}
		}
	}
	return out
}

func canGenerate(ad llm.Adapter, kind string) bool {
	switch kind {
	case llm.KindImage:
		_, ok := ad.(llm.ImageGenerator)
		return ok
	case llm.KindVideo:
		_, ok := ad.(llm.VideoGenerator)
		return ok
	}
	return false
}

func (s *Service) router() *router.Router {
	r, ok := kernel.Lookup[agentloop.Router](s.c, agentloop.RouterKey)
	if !ok {
		return nil
	}
	rt, _ := r.(*router.Router)
	return rt
}

func (s *Service) info(ctx context.Context, ref llm.ModelRef) (llm.ModelInfo, bool) {
	ad, ok := s.llms.Get(ref.Provider)
	if !ok {
		return llm.ModelInfo{}, false
	}
	mctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ms, err := ad.Models(mctx)
	if err != nil {
		return llm.ModelInfo{}, false
	}
	for _, m := range ms {
		if m.ID == ref.Model {
			return m, true
		}
	}
	return llm.ModelInfo{}, false
}

// Pick chooses the model for kind. explicit is a "provider/model" the tool
// named ("" = choose). edit requires a model that takes input images.
func (s *Service) Pick(ctx context.Context, a *agent.Agent, kind, explicit string, edit bool) (llm.ModelRef, error) {
	rt := s.router()
	allowed := func(ref llm.ModelRef) bool { return rt == nil || a == nil || rt.Allowed(a, ref) }
	fits := func(ref llm.ModelRef) bool {
		ad, ok := s.llms.Get(ref.Provider)
		if !ok || !canGenerate(ad, kind) {
			return false
		}
		m, ok := s.info(ctx, ref)
		if !ok || m.OutputKind() != kind {
			return false
		}
		return !edit || slices.Contains(m.Capabilities, llm.CapImageEdit)
	}
	if explicit != "" {
		ref, err := llm.ParseRef(explicit)
		if err != nil {
			return llm.ModelRef{}, fmt.Errorf("model must be provider/model, e.g. %s", s.example(ctx, kind))
		}
		ref.Effort = ""
		if !fits(ref) {
			what := kind
			if edit {
				what = "image-editing"
			}
			return llm.ModelRef{}, fmt.Errorf("%s is not an available %s model; available: %s", ref, what, s.list(ctx, kind, edit))
		}
		if !allowed(ref) {
			return llm.ModelRef{}, fmt.Errorf("%s is not a local model, and this agent only uses local models", ref)
		}
		return ref, nil
	}
	if rt != nil {
		task := router.TaskImage
		if kind == llm.KindVideo {
			task = router.TaskVideo
		}
		if ref, ok := rt.RouteForAgent(task, a); ok && fits(ref) {
			return ref, nil
		}
	}
	for _, ref := range s.Available(ctx, kind) {
		if allowed(ref) && fits(ref) {
			return ref, nil
		}
	}
	if edit && len(s.Available(ctx, kind)) > 0 {
		return llm.ModelRef{}, errors.New("none of the available image models can edit images")
	}
	return llm.ModelRef{}, errNoModel{kind}
}

func (s *Service) list(ctx context.Context, kind string, edit bool) string {
	var out []string
	for _, ref := range s.Available(ctx, kind) {
		if edit {
			if m, ok := s.info(ctx, ref); !ok || !slices.Contains(m.Capabilities, llm.CapImageEdit) {
				continue
			}
		}
		out = append(out, ref.String())
	}
	if len(out) == 0 {
		return "none"
	}
	return strings.Join(out, ", ")
}

func (s *Service) example(ctx context.Context, kind string) string {
	if av := s.Available(ctx, kind); len(av) > 0 {
		return av[0].String()
	}
	if kind == llm.KindVideo {
		return "gemini/gemini-omni-1.2-flash"
	}
	return "openai/gpt-image-2"
}

// ImageArgs are the image tools' arguments.
type ImageArgs struct {
	Prompt  string   `json:"prompt"`
	Size    string   `json:"size,omitempty"`
	Aspect  string   `json:"aspect,omitempty"`
	Quality string   `json:"quality,omitempty"`
	N       int      `json:"n,omitempty"`
	Model   string   `json:"model,omitempty"`
	Images  []string `json:"images,omitempty"`
}

// ImageOutcome is what GenerateImages produced.
type ImageOutcome struct {
	Model   llm.ModelRef
	Files   []Saved
	Revised []string
	Usage   llm.Usage
}

// GenerateImages runs one image request for an agent and saves the results.
func (s *Service) GenerateImages(ctx context.Context, a *agent.Agent, workspace string, args ImageArgs) (ImageOutcome, error) {
	ref, err := s.Pick(ctx, a, llm.KindImage, args.Model, len(args.Images) > 0)
	if err != nil {
		return ImageOutcome{}, err
	}
	req := llm.ImageRequest{Model: ref.Model, Prompt: args.Prompt, Size: args.Size, Aspect: args.Aspect, Quality: args.Quality, N: args.N}
	for _, p := range args.Images {
		name, mime, data, err := ReadInput(workspace, p)
		if err != nil {
			return ImageOutcome{}, err
		}
		req.Inputs = append(req.Inputs, llm.InputImage{Name: name, Mime: mime, Data: data})
	}
	ad, _ := s.llms.Get(ref.Provider)
	gen, ok := ad.(llm.ImageGenerator)
	if !ok {
		return ImageOutcome{}, errNoModel{llm.KindImage}
	}
	res, err := gen.GenerateImages(ctx, req)
	if err != nil {
		return ImageOutcome{}, err
	}
	out := ImageOutcome{Model: ref, Usage: res.Usage}
	for _, img := range res.Images {
		sv, err := Save(workspace, llm.KindImage, args.Prompt, bytes.NewReader(img.Data), MaxImageBytes, s.now())
		if err != nil {
			if len(out.Files) > 0 {
				break // keep what was saved
			}
			return out, err
		}
		out.Files = append(out.Files, sv)
		Created.Emit(s.c, CreatedFile{Agent: a.ID(), Workspace: workspace, Kind: llm.KindImage, Prompt: args.Prompt, Saved: sv})
		if img.RevisedPrompt != "" {
			out.Revised = append(out.Revised, img.RevisedPrompt)
		}
	}
	return out, nil
}

// record appends a media/generated event (best effort: the file exists
// either way).
func record(ss *session.Session, agentID string, g Generated) {
	if ss != nil {
		_, _ = ss.Append(TypeGenerated, agentID, g)
	}
}

// sync registers or removes each kind's tools as models come and go.
func (s *Service) sync(ctx context.Context) {
	for _, kind := range []string{llm.KindImage, llm.KindVideo} {
		av := s.Available(ctx, kind)
		s.mu.Lock()
		sub := s.subs[kind]
		changed := !slices.Equal(av, s.avail[kind])
		s.avail[kind] = av
		switch {
		case len(av) > 0 && (sub == nil || changed):
			if sub != nil {
				sub.Dispose()
			}
			sub = s.c.Fork("")
			for _, t := range s.toolsFor(kind, av) {
				t.Load, t.Group = tools.LoadDeferred, "media"
				s.reg.Register(sub, t)
			}
			s.subs[kind] = sub
		case len(av) == 0 && sub != nil:
			sub.Dispose()
			delete(s.subs, kind)
		}
		s.mu.Unlock()
	}
}

// Sync rechecks the available models now (tests, settings changes).
func (s *Service) Sync(ctx context.Context) { s.sync(ctx) }

// Plugin is @crewbase/media.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/media",
	Inject:      []string{llm.ServiceKey, tools.ServiceKey, agent.ServiceKey},
	Description: "Image and video generation for agents (image_generate, image_edit, video_generate) with OpenAI, Gemini and magpie models.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		s := &Service{c: c, llms: kernel.Use[*llm.Registry](c, llm.ServiceKey), reg: kernel.Use[*tools.Registry](c, tools.ServiceKey),
			now: time.Now, vidMax: 20 * time.Minute, subs: map[string]*kernel.Context{}, avail: map[string][]llm.ModelRef{}}
		if cfg.VideoTimeout != "" {
			d, err := time.ParseDuration(cfg.VideoTimeout)
			if err != nil || d <= 0 || d > 2*time.Hour {
				return fmt.Errorf("media: videoTimeout must be a duration up to 2h")
			}
			s.vidMax = d
		}
		every := time.Minute
		if cfg.SyncInterval != "" {
			if every, err = time.ParseDuration(cfg.SyncInterval); err != nil {
				return fmt.Errorf("media: syncInterval: %w", err)
			}
		}
		s.jobs = &jobs{s: s, all: map[string]*videoJob{}}
		if err := c.Provide(ServiceKey, s); err != nil {
			return err
		}
		agent.Deleted.Listen(c, s.jobs.stopAll)
		kick := make(chan struct{}, 1)
		llm.AdaptersChanged.Listen(c, func(struct{}) {
			select {
			case kick <- struct{}{}:
			default:
			}
		})
		kick <- struct{}{} // first check off the start path (listing models may take seconds)
		c.Go(func(ctx context.Context) {
			var tick <-chan time.Time
			if every > 0 {
				t := time.NewTicker(every)
				defer t.Stop()
				tick = t.C
			}
			for {
				select {
				case <-ctx.Done():
					return
				case <-kick:
					// adapters register in bursts while the host starts
					select {
					case <-ctx.Done():
						return
					case <-time.After(500 * time.Millisecond):
					}
				case <-tick:
				}
				s.sync(ctx)
			}
		})
		return nil
	},
})
