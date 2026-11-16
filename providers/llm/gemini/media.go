package gemini

// Image and video generation through the Gemini Interactions API, the
// path Google documents for its image (Nano Banana) and video (Gemini
// Omni) models (aistudio.google.com/docs/image-generation, /docs/omni,
// checked 2026-11):
//
//	POST {base}/v1beta/interactions   x-goog-api-key, Api-Revision: 2026-05-20
//	{"model": "...", "input": [{"type":"text","text":...},
//	                           {"type":"image","mime_type":...,"data":<base64>}],
//	 "response_format": {"type":"image","aspect_ratio":"16:9","image_size":"1K"}
//	                 or {"type":"video","aspect_ratio":"16:9","resolution":"720p",
//	                     "duration":"8s","delivery":"uri"},
//	 "store": false}
//	→ {"status":"completed","steps":[{"type":"model_output","content":[
//	     {"type":"image"|"video","mime_type":...,"data":<base64>} | {"uri":...}]}]}
//
// Generation is synchronous (no job to poll). Videos larger than ~4 MB
// (1080p / 4k) are asked for by URI: the file is polled until ACTIVE
// (GET /v1beta/files/{id}) and then downloaded with the same key — only
// from the API's own host. Imagen (:predict) and the Veo 3.1 preview
// models (:predictLongRunning) are shut down on the Gemini API, so they are
// not implemented.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
)

// APIRevision is the Interactions API revision the requests are written for.
const APIRevision = "2026-05-20"

const (
	defaultAPIBase = "https://generativelanguage.googleapis.com/"
	maxMediaJSON   = 400 << 20 // inline base64 video / images
	maxImageBytes  = 40 << 20
	maxImagesPerGo = 4
)

var (
	_ llm.ImageGenerator = (*Adapter)(nil)
	_ llm.VideoGenerator = (*Adapter)(nil)

	imageSizeRe = regexp.MustCompile(`^(512px|1K|2K|4K)$`)
	fileNameRe  = regexp.MustCompile(`/v1beta/(files/[A-Za-z0-9_-]+)`)
)

func (a *Adapter) apiBase() string {
	if a.baseURL != "" {
		return a.baseURL
	}
	return defaultAPIBase
}

func (a *Adapter) version() string {
	if a.apiVersion != "" {
		return a.apiVersion
	}
	return "v1beta"
}

func (a *Adapter) send(ctx context.Context, method, u string, body []byte) (*http.Response, string, error) {
	key, err := a.keys.Key(ctx, a.id)
	if err != nil {
		return nil, "", err
	}
	if key == "" {
		return nil, "", &llm.Error{Kind: llm.ErrAuth, Provider: a.id, Message: "no API key configured"}
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, key, &llm.Error{Kind: llm.ErrBadRequest, Provider: a.id, Message: "bad request url"}
	}
	req.Header.Set("x-goog-api-key", key)
	req.Header.Set("Api-Revision", APIRevision)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range a.headers {
		req.Header.Set(k, v)
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, key, transportError(ctx, a.id, err, key)
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, key, httpError(a.id, resp, key)
	}
	return resp, key, nil
}

type contentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	Data     string `json:"data,omitempty"`
	URI      string `json:"uri,omitempty"`
}

type interaction struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Steps  []struct {
		Type    string         `json:"type"`
		Content []contentBlock `json:"content"`
	} `json:"steps"`
	// Outputs is the flat form some revisions answer with.
	Outputs []contentBlock `json:"outputs"`
}

func (it interaction) blocks(typ string) []contentBlock {
	var out []contentBlock
	for _, s := range it.Steps {
		if s.Type != "" && s.Type != "model_output" {
			continue
		}
		for _, c := range s.Content {
			if c.Type == typ {
				out = append(out, c)
			}
		}
	}
	for _, c := range it.Outputs {
		if c.Type == typ {
			out = append(out, c)
		}
	}
	return out
}

func (a *Adapter) interact(ctx context.Context, body map[string]any) (interaction, string, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return interaction{}, "", err
	}
	resp, key, err := a.send(ctx, http.MethodPost, a.apiBase()+a.version()+"/interactions", b)
	if err != nil {
		return interaction{}, key, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxMediaJSON+1))
	if err != nil {
		return interaction{}, key, streamReadError(ctx, a.id, err)
	}
	if len(raw) > maxMediaJSON {
		return interaction{}, key, &llm.Error{Kind: llm.ErrBadRequest, Provider: a.id, Message: "response too large"}
	}
	var it interaction
	if err := json.Unmarshal(raw, &it); err != nil {
		return interaction{}, key, &llm.Error{Kind: llm.ErrUnavailable, Provider: a.id, Message: "unexpected response"}
	}
	if it.Status != "" && it.Status != "completed" {
		return it, key, &llm.Error{Kind: llm.ErrBadRequest, Provider: a.id, Message: "the generation ended " + it.Status}
	}
	return it, key, nil
}

func imageBlock(in llm.InputImage) contentBlock {
	return contentBlock{Type: "image", MimeType: in.Mime, Data: base64.StdEncoding.EncodeToString(in.Data)}
}

// aspectOf turns "1536x1024" into "3:2" when it is a common ratio.
func aspectOf(size string) string {
	w, h := 0, 0
	if _, err := fmt.Sscanf(size, "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
		return ""
	}
	g := gcd(w, h)
	r := fmt.Sprintf("%d:%d", w/g, h/g)
	switch r {
	case "1:1", "2:3", "3:2", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9":
		return r
	}
	return ""
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// GenerateImages implements llm.ImageGenerator. Gemini answers one image
// per call, so N > 1 makes N calls (at most 4).
func (a *Adapter) GenerateImages(ctx context.Context, req llm.ImageRequest) (llm.ImageResult, error) {
	input := []contentBlock{{Type: "text", Text: req.Prompt}}
	for _, in := range req.Inputs {
		input = append(input, imageBlock(in))
	}
	body := map[string]any{"model": req.Model, "input": input, "store": false}
	rf := map[string]any{}
	aspect := req.Aspect
	if aspect == "" {
		aspect = aspectOf(req.Size)
	}
	if aspect != "" {
		rf["aspect_ratio"] = aspect
	}
	if imageSizeRe.MatchString(req.Size) {
		rf["image_size"] = req.Size
	}
	if len(rf) > 0 {
		rf["type"] = "image"
		body["response_format"] = rf
	}
	n := min(max(req.N, 1), maxImagesPerGo)
	var res llm.ImageResult
	for range n {
		it, _, err := a.interact(ctx, body)
		if err != nil {
			if len(res.Images) > 0 {
				return res, nil // keep what was already paid for
			}
			return res, err
		}
		for _, c := range it.blocks("image") {
			data, err := base64.StdEncoding.DecodeString(c.Data)
			if err != nil || len(data) == 0 || len(data) > maxImageBytes {
				continue
			}
			res.Images = append(res.Images, llm.Image{Mime: http.DetectContentType(data), Data: data})
		}
	}
	if len(res.Images) == 0 {
		return res, &llm.Error{Kind: llm.ErrUnavailable, Provider: a.id, Message: "the model returned no image (it may have declined the prompt)"}
	}
	return res, nil
}

// GenerateVideo implements llm.VideoGenerator.
func (a *Adapter) GenerateVideo(ctx context.Context, req llm.VideoRequest, progress func(llm.VideoProgress)) (llm.Video, error) {
	var input any = req.Prompt
	if req.Image != nil {
		input = []contentBlock{{Type: "text", Text: req.Prompt}, imageBlock(*req.Image)}
	}
	rf := map[string]any{"type": "video"}
	if req.Aspect != "" {
		rf["aspect_ratio"] = req.Aspect
	}
	if req.Resolution != "" {
		rf["resolution"] = req.Resolution
	}
	if req.Seconds > 0 {
		rf["duration"] = fmt.Sprintf("%ds", req.Seconds)
	}
	// Large results must come by URI (inline answers are limited).
	byURI := req.Resolution == "1080p" || req.Resolution == "4k"
	if byURI {
		rf["delivery"] = "uri"
	}
	if progress != nil {
		progress(llm.VideoProgress{Status: "in_progress", Percent: -1})
	}
	it, _, err := a.interact(ctx, map[string]any{"model": req.Model, "input": input, "response_format": rf, "store": false, "background": false})
	if err != nil {
		return llm.Video{}, err
	}
	vs := it.blocks("video")
	if len(vs) == 0 {
		return llm.Video{}, &llm.Error{Kind: llm.ErrUnavailable, Provider: a.id, Message: "the model returned no video (it may have declined the prompt)"}
	}
	v := vs[0]
	mime := v.MimeType
	if !strings.HasPrefix(mime, "video/") {
		mime = "video/mp4"
	}
	if v.Data != "" {
		data, err := base64.StdEncoding.DecodeString(v.Data)
		if err != nil {
			return llm.Video{}, &llm.Error{Kind: llm.ErrUnavailable, Provider: a.id, Message: "the video is not valid base64"}
		}
		return llm.Video{Mime: mime, Body: io.NopCloser(bytes.NewReader(data)), Size: int64(len(data))}, nil
	}
	return a.downloadFile(ctx, v.URI, mime, progress)
}

// downloadFile waits for a URI-delivered file to be ACTIVE and opens it.
func (a *Adapter) downloadFile(ctx context.Context, uri, mime string, progress func(llm.VideoProgress)) (llm.Video, error) {
	u, err := url.Parse(uri)
	base, _ := url.Parse(a.apiBase())
	if err != nil || base == nil || u.Host != base.Host || u.Scheme != base.Scheme {
		// never send the key to another host
		return llm.Video{}, &llm.Error{Kind: llm.ErrUnavailable, Provider: a.id, Message: "the video link is not on the API host"}
	}
	m := fileNameRe.FindStringSubmatch(u.Path)
	if m == nil {
		return llm.Video{}, &llm.Error{Kind: llm.ErrUnavailable, Provider: a.id, Message: "unexpected video link"}
	}
	every := a.pollEvery
	if every <= 0 {
		every = 5 * time.Second
	}
	for {
		resp, key, err := a.send(ctx, http.MethodGet, a.apiBase()+a.version()+"/"+m[1], nil)
		if err != nil {
			return llm.Video{}, err
		}
		var f struct {
			State string `json:"state"`
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if json.Unmarshal(raw, &f) != nil {
			return llm.Video{}, &llm.Error{Kind: llm.ErrUnavailable, Provider: a.id, Message: sanitize("unexpected file status", key)}
		}
		switch f.State {
		case "ACTIVE":
			resp, _, err := a.send(ctx, http.MethodGet, u.String(), nil)
			if err != nil {
				return llm.Video{}, err
			}
			return llm.Video{Mime: mime, Body: resp.Body, Size: resp.ContentLength}, nil
		case "FAILED":
			return llm.Video{}, &llm.Error{Kind: llm.ErrUnavailable, Provider: a.id, Message: "the video file failed to process"}
		}
		if progress != nil {
			progress(llm.VideoProgress{Status: "processing", Percent: -1})
		}
		select {
		case <-ctx.Done():
			return llm.Video{}, ctx.Err()
		case <-time.After(every):
		}
	}
}
