package openai

// Image and video generation in OpenAI's shapes (checked against
// developers.openai.com/api/reference, 2026-11):
//
//	POST /images/generations  JSON {model, prompt, n, size, quality}
//	POST /images/edits        multipart: model, prompt, n, size, quality, image[] (≤ 16)
//	                          → {data: [{b64_json | url, revised_prompt}], usage}
//	POST /videos              JSON {model, prompt, seconds, size} (multipart with
//	                          input_reference when a first frame is given)
//	GET  /videos/{id}         → {id, status: queued|in_progress|completed|failed, progress, error}
//	GET  /videos/{id}/content → the MP4
//
// GPT image models always answer base64; other models behind
// OpenAI-compatible gateways may answer a URL, which is fetched (http/https
// only, size-capped). OpenAI shut its own Videos API (Sora) down on
// 2026-09-24; the same shape is what gateways such as magpie serve for
// their video models, so it stays here for them.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
)

// Media limits.
const (
	maxImageJSON  = 160 << 20 // an images response (base64 of up to 10 images)
	MaxImageBytes = 40 << 20  // one decoded image
	maxVideoPoll  = 1 << 20
)

var (
	_ llm.ImageGenerator = (*Adapter)(nil)
	_ llm.VideoGenerator = (*Adapter)(nil)
)

// SetPollInterval changes how often video jobs are polled (tests).
func (a *Adapter) SetPollInterval(d time.Duration) { a.pollEvery = d }

// do sends one request with the key and headers, returning the response
// for 2xx and a classified error otherwise.
func (a *Adapter) do(ctx context.Context, method, path string, body io.Reader, ctype string) (*http.Response, string, error) {
	key, err := a.keys.Key(ctx, a.id)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, method, a.baseURL+path, body)
	if err != nil {
		return nil, key, &llm.Error{Kind: llm.ErrBadRequest, Provider: a.id, Message: "bad request url"}
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
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

func (a *Adapter) doJSON(ctx context.Context, method, path string, in, out any, limit int64) error {
	var body io.Reader
	ctype := ""
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body, ctype = bytes.NewReader(b), "application/json"
	}
	resp, key, err := a.do(ctx, method, path, body, ctype)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return a.decode(ctx, resp, out, limit, key)
}

func (a *Adapter) decode(ctx context.Context, resp *http.Response, out any, limit int64, key string) error {
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return streamReadError(ctx, a.id, err)
	}
	if int64(len(b)) > limit {
		return &llm.Error{Kind: llm.ErrBadRequest, Provider: a.id, Message: "response too large"}
	}
	if err := json.Unmarshal(b, out); err != nil {
		return &llm.Error{Kind: llm.ErrUnavailable, Provider: a.id, Message: sanitize("unexpected response: "+err.Error(), key)}
	}
	return nil
}

// sizeFor maps an aspect ratio to a size every GPT image model takes.
func sizeFor(aspect string) string {
	w, h, ok := ratio(aspect)
	switch {
	case !ok:
		return ""
	case w > h:
		return "1536x1024"
	case w < h:
		return "1024x1536"
	}
	return "1024x1024"
}

func ratio(aspect string) (int, int, bool) {
	a, b, ok := strings.Cut(aspect, ":")
	if !ok {
		return 0, 0, false
	}
	w, err1 := strconv.Atoi(strings.TrimSpace(a))
	h, err2 := strconv.Atoi(strings.TrimSpace(b))
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}

type imagesResponse struct {
	Data []struct {
		B64JSON       string `json:"b64_json"`
		URL           string `json:"url"`
		RevisedPrompt string `json:"revised_prompt"`
	} `json:"data"`
	OutputFormat string `json:"output_format"`
	Usage        *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// GenerateImages implements llm.ImageGenerator (generations, or edits
// when req.Inputs are given).
func (a *Adapter) GenerateImages(ctx context.Context, req llm.ImageRequest) (llm.ImageResult, error) {
	size := req.Size
	if size == "" {
		size = sizeFor(req.Aspect)
	}
	n := req.N
	if n <= 0 {
		n = 1
	}
	var out imagesResponse
	if len(req.Inputs) == 0 {
		body := map[string]any{"model": req.Model, "prompt": req.Prompt, "n": n}
		if size != "" {
			body["size"] = size
		}
		if req.Quality != "" {
			body["quality"] = req.Quality
		}
		if err := a.doJSON(ctx, http.MethodPost, "/images/generations", body, &out, maxImageJSON); err != nil {
			return llm.ImageResult{}, err
		}
	} else {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fields := [][2]string{{"model", req.Model}, {"prompt", req.Prompt}, {"n", strconv.Itoa(n)}}
		if size != "" {
			fields = append(fields, [2]string{"size", size})
		}
		if req.Quality != "" {
			fields = append(fields, [2]string{"quality", req.Quality})
		}
		for _, f := range fields {
			_ = mw.WriteField(f[0], f[1])
		}
		for _, in := range req.Inputs {
			if err := filePart(mw, "image[]", in); err != nil {
				return llm.ImageResult{}, err
			}
		}
		_ = mw.Close()
		resp, key, err := a.do(ctx, http.MethodPost, "/images/edits", &buf, mw.FormDataContentType())
		if err != nil {
			return llm.ImageResult{}, err
		}
		err = a.decode(ctx, resp, &out, maxImageJSON, key)
		resp.Body.Close()
		if err != nil {
			return llm.ImageResult{}, err
		}
	}
	var res llm.ImageResult
	if out.Usage != nil {
		res.Usage = llm.Usage{InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens}
	}
	for _, d := range out.Data {
		var data []byte
		switch {
		case d.B64JSON != "":
			b, err := base64.StdEncoding.DecodeString(d.B64JSON)
			if err != nil {
				return res, &llm.Error{Kind: llm.ErrUnavailable, Provider: a.id, Message: "the image is not valid base64"}
			}
			data = b
		case d.URL != "":
			b, err := a.fetchURL(ctx, d.URL, MaxImageBytes)
			if err != nil {
				return res, err
			}
			data = b
		default:
			continue
		}
		if len(data) > MaxImageBytes {
			return res, &llm.Error{Kind: llm.ErrBadRequest, Provider: a.id, Message: "the image is too large"}
		}
		res.Images = append(res.Images, llm.Image{Mime: http.DetectContentType(data), Data: data, RevisedPrompt: d.RevisedPrompt})
	}
	if len(res.Images) == 0 {
		return res, &llm.Error{Kind: llm.ErrUnavailable, Provider: a.id, Message: "the provider returned no image"}
	}
	return res, nil
}

func filePart(mw *multipart.Writer, field string, in llm.InputImage) error {
	h := make(textproto.MIMEHeader)
	name := strings.NewReplacer(`"`, "", "\\", "", "\r", "", "\n", "").Replace(in.Name)
	if name == "" {
		name = "image.png"
	}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, field, name))
	h.Set("Content-Type", in.Mime)
	w, err := mw.CreatePart(h)
	if err != nil {
		return err
	}
	_, err = w.Write(in.Data)
	return err
}

// fetchURL downloads a result a provider answered by link (no key sent:
// such links are pre-signed).
func (a *Adapter) fetchURL(ctx context.Context, raw string, limit int64) ([]byte, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, &llm.Error{Kind: llm.ErrUnavailable, Provider: a.id, Message: "the provider returned an unusable link"}
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, transportError(ctx, a.id, err, "")
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, &llm.Error{Kind: llm.ErrUnavailable, Provider: a.id, Message: "downloading the result failed: " + resp.Status}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, streamReadError(ctx, a.id, err)
	}
	if int64(len(b)) > limit {
		return nil, &llm.Error{Kind: llm.ErrBadRequest, Provider: a.id, Message: "the result is too large"}
	}
	return b, nil
}

// videoSize maps aspect + resolution to OpenAI's WIDTHxHEIGHT.
func videoSize(req llm.VideoRequest) string {
	if req.Size != "" {
		return req.Size
	}
	if req.Aspect == "" && req.Resolution == "" {
		return ""
	}
	long, short := 1280, 720
	if strings.HasPrefix(req.Resolution, "1080") {
		long, short = 1920, 1080
	}
	if req.Aspect == "9:16" {
		return fmt.Sprintf("%dx%d", short, long)
	}
	return fmt.Sprintf("%dx%d", long, short)
}

type videoJob struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Progress int    `json:"progress"`
	Error    *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// GenerateVideo implements llm.VideoGenerator: create, poll, download.
func (a *Adapter) GenerateVideo(ctx context.Context, req llm.VideoRequest, progress func(llm.VideoProgress)) (llm.Video, error) {
	var job videoJob
	size := videoSize(req)
	if req.Image == nil {
		body := map[string]any{"model": req.Model, "prompt": req.Prompt}
		if req.Seconds > 0 {
			body["seconds"] = strconv.Itoa(req.Seconds)
		}
		if size != "" {
			body["size"] = size
		}
		if err := a.doJSON(ctx, http.MethodPost, "/videos", body, &job, maxVideoPoll); err != nil {
			return llm.Video{}, err
		}
	} else {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("model", req.Model)
		_ = mw.WriteField("prompt", req.Prompt)
		if req.Seconds > 0 {
			_ = mw.WriteField("seconds", strconv.Itoa(req.Seconds))
		}
		if size != "" {
			_ = mw.WriteField("size", size)
		}
		if err := filePart(mw, "input_reference", *req.Image); err != nil {
			return llm.Video{}, err
		}
		_ = mw.Close()
		resp, key, err := a.do(ctx, http.MethodPost, "/videos", &buf, mw.FormDataContentType())
		if err != nil {
			return llm.Video{}, err
		}
		err = a.decode(ctx, resp, &job, maxVideoPoll, key)
		resp.Body.Close()
		if err != nil {
			return llm.Video{}, err
		}
	}
	if job.ID == "" || strings.ContainsAny(job.ID, "/?#") {
		return llm.Video{}, &llm.Error{Kind: llm.ErrUnavailable, Provider: a.id, Message: "the provider returned no usable video id"}
	}
	every := a.pollEvery
	if every <= 0 {
		every = 10 * time.Second
	}
	for {
		if progress != nil {
			progress(llm.VideoProgress{Status: job.Status, Percent: job.Progress, JobID: job.ID})
		}
		switch job.Status {
		case "completed":
			resp, _, err := a.do(ctx, http.MethodGet, "/videos/"+url.PathEscape(job.ID)+"/content", nil, "")
			if err != nil {
				return llm.Video{}, err
			}
			mime := resp.Header.Get("Content-Type")
			if mime == "" || !strings.HasPrefix(mime, "video/") {
				mime = "video/mp4"
			}
			return llm.Video{Mime: mime, Body: resp.Body, Size: resp.ContentLength}, nil
		case "failed", "cancelled", "expired":
			msg := "the video job " + job.Status
			if job.Error != nil && job.Error.Message != "" {
				msg += ": " + job.Error.Message
			}
			return llm.Video{}, &llm.Error{Kind: llm.ErrBadRequest, Provider: a.id, Message: msg}
		}
		select {
		case <-ctx.Done():
			return llm.Video{}, ctx.Err()
		case <-time.After(every):
		}
		id := job.ID
		if err := a.doJSON(ctx, http.MethodGet, "/videos/"+url.PathEscape(id), nil, &job, maxVideoPoll); err != nil {
			if llm.KindOf(err) == llm.ErrRateLimited || llm.KindOf(err) == llm.ErrUnavailable {
				job = videoJob{ID: id, Status: "in_progress", Progress: job.Progress}
				continue // a blip while polling: keep waiting
			}
			return llm.Video{}, err
		}
		if job.ID == "" {
			job.ID = id
		}
	}
}
