package media

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

var (
	sizeRe    = regexp.MustCompile(`^(auto|[1-9][0-9]{1,3}x[1-9][0-9]{1,3}|512px|1K|2K|4K)$`)
	aspectRe  = regexp.MustCompile(`^[1-9][0-9]?:[1-9][0-9]?$`)
	resRe     = regexp.MustCompile(`^(360p|480p|720p|1080p|4k)$`)
	qualities = map[string]bool{"": true, "low": true, "medium": true, "high": true}
)

const (
	maxImagesPerCall = 4
	maxPromptRunes   = 8000
)

// short trims s to n runes for summaries.
func short(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func fmtSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d B", n)
}

func (a ImageArgs) check() error {
	if strings.TrimSpace(a.Prompt) == "" {
		return fmt.Errorf("prompt is required")
	}
	if len([]rune(a.Prompt)) > maxPromptRunes {
		return fmt.Errorf("prompt is too long (at most %d characters)", maxPromptRunes)
	}
	if a.Size != "" && !sizeRe.MatchString(a.Size) {
		return fmt.Errorf("size must be WIDTHxHEIGHT (e.g. 1024x1024), auto, or 1K / 2K / 4K")
	}
	if a.Aspect != "" && !aspectRe.MatchString(a.Aspect) {
		return fmt.Errorf("aspect must look like 16:9")
	}
	if !qualities[a.Quality] {
		return fmt.Errorf("quality must be low, medium or high")
	}
	if a.N < 0 || a.N > maxImagesPerCall {
		return fmt.Errorf("n must be 1 to %d", maxImagesPerCall)
	}
	if len(a.Images) > MaxInputImages {
		return fmt.Errorf("at most %d input images", MaxInputImages)
	}
	return nil
}

// imageSummary is the approval-card / step line for an image call.
func imageSummary(c tools.Call, edit bool) string {
	var a ImageArgs
	_ = c.Decode(&a)
	verb := "生成图片"
	if edit {
		verb = "修改图片"
	}
	var parts []string
	if a.N > 1 {
		parts = append(parts, fmt.Sprintf("%d 张", a.N))
	}
	if a.Size != "" && a.Size != "auto" {
		parts = append(parts, a.Size)
	} else if a.Aspect != "" {
		parts = append(parts, a.Aspect)
	}
	if edit && len(a.Images) > 0 {
		parts = append(parts, fmt.Sprintf("参考 %d 张图", len(a.Images)))
	}
	s := verb + "：" + short(a.Prompt, 40)
	if len(parts) > 0 {
		s += "（" + strings.Join(parts, "，") + "）"
	}
	return s
}

// VideoArgs are video_generate's arguments.
type VideoArgs struct {
	Prompt     string `json:"prompt"`
	Seconds    int    `json:"seconds,omitempty"`
	Aspect     string `json:"aspect,omitempty"`
	Resolution string `json:"resolution,omitempty"`
	Image      string `json:"image,omitempty"`
	Model      string `json:"model,omitempty"`
}

func (a VideoArgs) check() error {
	if strings.TrimSpace(a.Prompt) == "" {
		return fmt.Errorf("prompt is required")
	}
	if len([]rune(a.Prompt)) > maxPromptRunes {
		return fmt.Errorf("prompt is too long (at most %d characters)", maxPromptRunes)
	}
	if a.Seconds < 0 || a.Seconds > 60 {
		return fmt.Errorf("seconds must be 1 to 60 (models accept fewer; see the tool description)")
	}
	if a.Aspect != "" && a.Aspect != "16:9" && a.Aspect != "9:16" {
		return fmt.Errorf("aspect must be 16:9 or 9:16")
	}
	if a.Resolution != "" && !resRe.MatchString(a.Resolution) {
		return fmt.Errorf("resolution must be 360p, 480p, 720p, 1080p or 4k")
	}
	return nil
}

func videoSummary(c tools.Call) string {
	var a VideoArgs
	_ = c.Decode(&a)
	var parts []string
	if a.Seconds > 0 {
		parts = append(parts, fmt.Sprintf("%d 秒", a.Seconds))
	}
	if a.Resolution != "" {
		parts = append(parts, a.Resolution)
	}
	if a.Aspect != "" {
		parts = append(parts, a.Aspect)
	}
	if a.Image != "" {
		parts = append(parts, "首帧 "+a.Image)
	}
	s := "生成视频：" + short(a.Prompt, 40)
	if len(parts) > 0 {
		s += "（" + strings.Join(parts, "，") + "）"
	}
	return s
}

func modelsNote(av []llm.ModelRef) string {
	if len(av) == 0 {
		return ""
	}
	var ids []string
	for i, r := range av {
		if i == 8 {
			ids = append(ids, "…")
			break
		}
		ids = append(ids, r.String())
	}
	return " Available: " + strings.Join(ids, ", ") + "."
}

func (s *Service) agentOf(id string) *agent.Agent {
	reg, ok := kernel.Lookup[*agent.Registry](s.c, agent.ServiceKey)
	if !ok {
		return nil
	}
	a, _ := reg.Get(id)
	return a
}

// imageResult is the text the model gets: paths, never the bytes.
func imageResult(out ImageOutcome, edit bool) string {
	var b strings.Builder
	verb := "Generated"
	if edit {
		verb = "Edited"
	}
	fmt.Fprintf(&b, "%s %d image(s) with %s, saved in your workspace:\n", verb, len(out.Files), out.Model)
	for _, f := range out.Files {
		fmt.Fprintf(&b, "- %s (%s, %s)\n", f.Path, f.Mime, fmtSize(f.Size))
	}
	if len(out.Revised) > 0 {
		fmt.Fprintf(&b, "The provider drew: %s\n", short(out.Revised[0], 300))
	}
	b.WriteString("They are already shown to the user in the conversation. Refer to them by path (e.g. to attach, edit or publish them); do not describe pixels you have not seen.")
	return b.String()
}

func (s *Service) imageTool(edit bool, av []llm.ModelRef) *tools.Tool {
	props := map[string]any{
		"prompt":  tools.Prop("string", "What to draw: subject, composition, style, text to render. Be specific."),
		"size":    tools.Prop("string", "OpenAI models: 1024x1024, 1536x1024 (landscape), 1024x1536 (portrait) or auto; gpt-image-2 models also take other WIDTHxHEIGHT (multiples of 16). Gemini models: 1K, 2K or 4K. Omit for the default."),
		"aspect":  tools.Prop("string", "Aspect ratio instead of a size, e.g. 1:1, 16:9, 9:16, 4:3, 3:2."),
		"quality": map[string]any{"type": "string", "enum": []string{"low", "medium", "high"}, "description": "OpenAI models: lower is faster and cheaper. Omit for the default."},
		"n":       tools.Prop("integer", fmt.Sprintf("How many images (1-%d, default 1). Each one is billed.", maxImagesPerCall)),
		"model":   tools.Prop("string", "provider/model of an image model; omit to use the one set for image generation."+modelsNote(av)),
	}
	name, desc, req := "image_generate", "Create images from a text prompt with an image model. The images are saved in your workspace under generated/ and shown to the user.", []string{"prompt"}
	var pathArgs []string
	if edit {
		name = "image_edit"
		desc = "Edit images or make a new one from reference images in your workspace (e.g. change the background, combine products into one scene). Results are saved under generated/ and shown to the user."
		props["images"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": fmt.Sprintf("Workspace paths of PNG / JPEG / WebP images to edit or use as references (1-%d).", MaxInputImages)}
		props["prompt"] = tools.Prop("string", "What to change or create from the images.")
		req = []string{"images", "prompt"}
		pathArgs = []string{"images"}
	}
	return &tools.Tool{
		Name: name, Description: desc, Params: tools.Schema(props, req...),
		Category: tools.CategoryWrite, PathArgs: pathArgs,
		Classify: func(c tools.Call) (string, string) { return tools.CategoryWrite, imageSummary(c, edit) },
		Execute: func(ctx context.Context, call tools.Call) (tools.Result, error) {
			var a ImageArgs
			if err := call.Decode(&a); err != nil {
				return tools.Errorf("bad arguments: %v", err), nil
			}
			if !edit {
				a.Images = nil
			} else if len(a.Images) == 0 {
				return tools.Errorf("images is required: the workspace paths of the pictures to edit"), nil
			}
			if err := a.check(); err != nil {
				return tools.Errorf("%v", err), nil
			}
			out, err := s.GenerateImages(ctx, s.agentOf(call.Agent), call.Workspace, a)
			if err != nil {
				return tools.Errorf("image generation failed: %v", err), nil
			}
			for _, f := range out.Files {
				record(call.Session, call.Agent, Generated{Kind: llm.KindImage, Path: f.Path, Mime: f.Mime, Size: f.Size, Prompt: short(a.Prompt, 500), Model: out.Model.String(), Call: call.ID})
			}
			return tools.Result{Content: imageResult(out, edit), Data: out.Files}, nil
		},
	}
}

func (s *Service) videoTools(av []llm.ModelRef) []*tools.Tool {
	gen := &tools.Tool{
		Name: "video_generate",
		Description: "Create a short video from a text prompt (optionally starting from an image in your workspace) with a video model. " +
			"It takes minutes, so it runs in the background: you get a job id now and a message with the saved file when it is done (generated/…mp4, shown to the user). " +
			"Each second is billed, so the user approves each video.",
		Params: tools.Schema(map[string]any{
			"prompt":     tools.Prop("string", "The shot: subject, action, camera, style, sound."),
			"seconds":    tools.Prop("integer", "Length in seconds (Gemini Omni: 3-15, default 10; OpenAI-style video models: 4, 8 or 12). Omit for the default."),
			"aspect":     map[string]any{"type": "string", "enum": []string{"16:9", "9:16"}, "description": "16:9 landscape (default) or 9:16 portrait."},
			"resolution": map[string]any{"type": "string", "enum": []string{"360p", "720p", "1080p", "4k"}, "description": "Default 720p; higher costs more and takes longer."},
			"image":      tools.Prop("string", "Optional workspace path of a PNG / JPEG / WebP image to start from."),
			"model":      tools.Prop("string", "provider/model of a video model; omit to use the one set for video generation."+modelsNote(av)),
		}, "prompt"),
		Category: tools.CategorySpend,
		PathArgs: []string{"image"},
		Classify: func(c tools.Call) (string, string) { return tools.CategorySpend, videoSummary(c) },
		Execute: func(ctx context.Context, call tools.Call) (tools.Result, error) {
			var a VideoArgs
			if err := call.Decode(&a); err != nil {
				return tools.Errorf("bad arguments: %v", err), nil
			}
			if err := a.check(); err != nil {
				return tools.Errorf("%v", err), nil
			}
			return s.jobs.start(ctx, call, a)
		},
	}
	status := &tools.Tool{
		Name:        "video_status",
		Description: "Show your video generation jobs (or one, by id) and their state. You do not need to poll: a message arrives when a video is done.",
		Params:      tools.Schema(map[string]any{"id": tools.Prop("string", "Job id, e.g. vid2 (omit for all).")}),
		Category:    tools.CategoryNone,
		Execute: func(_ context.Context, call tools.Call) (tools.Result, error) {
			return tools.Text(s.jobs.describe(call.Agent, call.String("id"))), nil
		},
	}
	return []*tools.Tool{gen, status}
}

func (s *Service) toolsFor(kind string, av []llm.ModelRef) []*tools.Tool {
	if kind == llm.KindVideo {
		return s.videoTools(av)
	}
	out := []*tools.Tool{s.imageTool(false, av)}
	var editable []llm.ModelRef
	for _, r := range av {
		if m, ok := s.info(context.Background(), r); ok && hasEdit(m) {
			editable = append(editable, r)
		}
	}
	if len(editable) > 0 {
		out = append(out, s.imageTool(true, editable))
	}
	return out
}

func hasEdit(m llm.ModelInfo) bool {
	for _, c := range m.Capabilities {
		if c == llm.CapImageEdit {
			return true
		}
	}
	return false
}
