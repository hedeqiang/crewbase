package llm

// Model kinds and the media generation contract.
//
// Every model has an output kind: chat (text, the default), image or
// video. ModelInfo.Kind carries it ("" reads as chat, so adapters that only
// serve chat models need no change). Chat pickers and chat routing only
// use chat models; image / video generation only uses models of that kind.
//
// Adapters that can generate media implement ImageGenerator and / or
// VideoGenerator next to Adapter. The media service (features/media)
// picks the model, calls the adapter and saves the result in the agent's
// workspace; adapters never touch the file system.

import (
	"context"
	"io"
	"strings"
)

// Model output kinds.
const (
	KindChat  = "chat"
	KindImage = "image"
	KindVideo = "video"
)

// CapImageEdit marks image models that take input images (edits,
// reference images).
const CapImageEdit = "image-edit"

// OutputKind is the model's kind, chat when unset.
func (m ModelInfo) OutputKind() string {
	switch m.Kind {
	case KindImage, KindVideo:
		return m.Kind
	}
	return KindChat
}

// ClassifyModel derives a model's output kind from its id, following the
// providers' published model names (checked 2026-11):
//
//	image: gpt-image-*, chatgpt-image-*, dall-e-* (OpenAI Images API);
//	       imagen-* (Google, shut down), gemini-*-image* and
//	       gemini-nano-banana* (Gemini native image generation)
//	video: sora-* (OpenAI Videos API, shut down 2026-09-24), veo-*
//	       (Google), gemini-omni-* (Gemini Omni video generation)
//
// Anything else is chat; gateways that know better say so themselves
// (magpie's "kind"), and user-added providers can mark a model by hand. Ids behind a gateway ("openai/gpt-image-2",
// "models/veo-3") are judged by their last segment.
func ClassifyModel(id string) string {
	l := strings.ToLower(id)
	if i := strings.LastIndex(l, "/"); i >= 0 {
		l = l[i+1:]
	}
	switch {
	case strings.HasPrefix(l, "gpt-image"), strings.HasPrefix(l, "chatgpt-image"), strings.HasPrefix(l, "dall-e"),
		strings.HasPrefix(l, "imagen"), strings.HasPrefix(l, "gemini-nano-banana"),
		strings.HasPrefix(l, "gemini-") && strings.Contains(l, "-image"):
		return KindImage
	case strings.HasPrefix(l, "sora-"), strings.HasPrefix(l, "veo-"), strings.HasPrefix(l, "gemini-omni"):
		return KindVideo
	}
	return KindChat
}

// EditsByID reports whether an image model id is known to take input
// images (OpenAI GPT image models, Gemini image models).
func EditsByID(id string) bool {
	l := strings.ToLower(id)
	if i := strings.LastIndex(l, "/"); i >= 0 {
		l = l[i+1:]
	}
	return strings.HasPrefix(l, "gpt-image") || strings.HasPrefix(l, "chatgpt-image") ||
		strings.HasPrefix(l, "gemini-") && ClassifyModel(l) == KindImage
}

// WithKind fills m.Kind from the id when the adapter left it empty.
func WithKind(m ModelInfo) ModelInfo {
	if m.Kind == "" {
		if k := ClassifyModel(m.ID); k != KindChat {
			m.Kind = k
		}
	}
	return m
}

// KindOf returns the output kind of ref: from its provider's listing when
// the model is listed there, else from its id.
func (r *Registry) KindOf(ctx context.Context, ref ModelRef) string {
	if a, ok := r.Get(ref.Provider); ok {
		if ms, err := a.Models(ctx); err == nil {
			for _, m := range ms {
				if m.ID == ref.Model {
					return m.OutputKind() // adapters classify what they list
				}
			}
		}
	}
	return ClassifyModel(ref.Model)
}

// InputImage is a picture passed to an image or video model (edit source,
// reference, first frame).
type InputImage struct {
	Name string
	Mime string
	Data []byte
}

// ImageRequest asks for images.
type ImageRequest struct {
	Model  string
	Prompt string
	// Size is "WIDTHxHEIGHT" (OpenAI) or "" for the model default.
	Size string
	// Aspect is "W:H" (Gemini); adapters that only take sizes map it.
	Aspect string
	// Quality is low | medium | high | "" (model default).
	Quality string
	N       int
	// Inputs are images to edit or use as references (empty = generate).
	Inputs []InputImage
}

// Image is one generated picture.
type Image struct {
	Mime string
	Data []byte
	// RevisedPrompt is what the provider actually drew, when it says.
	RevisedPrompt string
}

// ImageResult is what an image request produced.
type ImageResult struct {
	Images []Image
	Usage  Usage
}

// ImageGenerator is implemented by adapters that generate images.
type ImageGenerator interface {
	GenerateImages(ctx context.Context, req ImageRequest) (ImageResult, error)
}

// VideoRequest asks for one video.
type VideoRequest struct {
	Model  string
	Prompt string
	// Seconds is the clip length (0 = model default).
	Seconds int
	// Aspect is "16:9" | "9:16" ("" = default).
	Aspect string
	// Resolution is "720p" | "1080p" | … ("" = default).
	Resolution string
	// Size is "WIDTHxHEIGHT" when the caller knows the provider's sizes.
	Size string
	// Image is an optional first frame / reference.
	Image *InputImage
}

// VideoProgress reports a running video job.
type VideoProgress struct {
	Status  string // queued | in_progress | …
	Percent int    // 0..100, -1 unknown
	JobID   string // provider job id, when there is one
}

// Video is a finished clip. The caller closes Body.
type Video struct {
	Mime string
	Body io.ReadCloser
	// Size is the length when known (-1 otherwise).
	Size int64
}

// VideoGenerator is implemented by adapters that generate videos. It
// blocks until the video is ready (polling the provider's job as needed),
// calling progress (may be nil) along the way; ctx bounds the wait.
type VideoGenerator interface {
	GenerateVideo(ctx context.Context, req VideoRequest, progress func(VideoProgress)) (Video, error)
}
