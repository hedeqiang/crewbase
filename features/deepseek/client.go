// Package deepseek uses DeepSeek's standalone tool APIs, independent of the
// model an agent runs on (api-docs.deepseek.com/zh-cn/guides, 2026-11):
//
//	POST /v1/tools/search  JSON → web results            (¥5 / 1000 requests)
//	POST /v1/tools/fetch   JSON → a page as Markdown     (¥5 / 1000 requests)
//	POST /v1/tools/ocr     multipart → text per page     (¥1.5 / 1000 pages)
//	POST /v1/tools/asr     multipart (WAV / Ogg Opus, ≤ 8 MiB, ≤ 120 s) → text
//	POST /v1/tools/tts     JSON → audio/wav
//
// Plugin @crewbase/deepseek-tools gives agents web_search / web_fetch /
// ocr_file, provides the `ocr` service attachments use for scanned PDFs and
// images, and the voice API (/api/voice/*) the web UI dictates and reads
// aloud with. The key is the DeepSeek provider's (added in 设置 › 模型), or
// config keyRef; nothing is called without one.
package deepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// DefaultBaseURL is the DeepSeek API.
const DefaultBaseURL = "https://api.deepseek.com"

// Limits from the docs.
const (
	MaxQueryRunes = 128
	MaxCount      = 50
	MaxURL        = 8192
	MaxOCRBytes   = 100 << 20
	MaxASRBytes   = 8 << 20
	MaxTTSBytes   = 64 << 10
	MaxTTSChars   = 10000 // billable characters (CJK count twice)
)

// Client calls the tool APIs. Key returns the API key per call (so a key
// added later is picked up).
type Client struct {
	BaseURL string
	Key     func(ctx context.Context) (string, error)
	HTTP    *http.Client
}

// ErrNoKey means no DeepSeek key is configured.
var ErrNoKey = errors.New("no DeepSeek API key: add DeepSeek in 设置 › 模型")

// APIError is an error response.
type APIError struct {
	Status  int
	Message string
	Code    string
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	switch e.Status {
	case 401:
		return "DeepSeek rejected the API key (401)"
	case 402:
		return "DeepSeek account balance is insufficient (402)"
	case 429:
		return "DeepSeek rate limit reached; try again shortly (429)"
	}
	return fmt.Sprintf("DeepSeek %d: %s", e.Status, msg)
}

// Retryable reports transient failures (408, 429, 5xx).
func (e *APIError) Retryable() bool {
	return e.Status == 408 || e.Status == 429 || e.Status >= 500
}

func (c *Client) base() string {
	if c.BaseURL == "" {
		return DefaultBaseURL
	}
	return strings.TrimRight(c.BaseURL, "/")
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) do(ctx context.Context, path, ctype string, body io.Reader) (*http.Response, error) {
	if c.Key == nil {
		return nil, ErrNoKey
	}
	key, err := c.Key(ctx)
	if err != nil {
		return nil, err
	}
	if key == "" {
		return nil, ErrNoKey
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.base()+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", ctype)
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, scrubErr(err, key)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, decodeErr(resp)
	}
	return resp, nil
}

func scrubErr(err error, key string) error {
	if key != "" && strings.Contains(err.Error(), key) {
		return errors.New(strings.ReplaceAll(err.Error(), key, "***"))
	}
	return err
}

func decodeErr(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	e := &APIError{Status: resp.StatusCode}
	var env struct {
		Error struct {
			Message string `json:"message"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &env) == nil && env.Error.Message != "" {
		e.Message = env.Error.Message
		if env.Error.Code != nil {
			e.Code = fmt.Sprint(env.Error.Code)
		}
	} else {
		e.Message = strings.TrimSpace(string(b))
		if len(e.Message) > 300 {
			e.Message = e.Message[:300]
		}
	}
	return e
}

func (c *Client) postJSON(ctx context.Context, path string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, path, "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(out)
}

// ---------------------------------------------------------------- search

// SearchRequest is POST /v1/tools/search.
type SearchRequest struct {
	Query       string `json:"query"`
	Count       int    `json:"count,omitempty"`
	OriQuestion string `json:"ori_question,omitempty"`
	NeedContent bool   `json:"need_content,omitempty"`
}

// SearchResult is one web result.
type SearchResult struct {
	RerankScore float64 `json:"rerank_score"`
	Title       string  `json:"title"`
	URL         string  `json:"url"`
	Snippet     string  `json:"snippet"`
	Summary     string  `json:"summary"`
	Content     string  `json:"content"`
	SiteName    *string `json:"site_name"`
	Date        *string `json:"date"`
	Authority   int     `json:"authority"`
}

// Search runs a web search.
func (c *Client) Search(ctx context.Context, in SearchRequest) ([]SearchResult, error) {
	in.Query = clipRunes(strings.TrimSpace(in.Query), MaxQueryRunes)
	if in.Query == "" {
		return nil, errors.New("query is empty")
	}
	in.OriQuestion = clipRunes(strings.TrimSpace(in.OriQuestion), MaxQueryRunes)
	if in.Count < 0 || in.Count > MaxCount {
		in.Count = min(max(in.Count, 1), MaxCount)
	}
	var out struct {
		Results []SearchResult `json:"results"`
	}
	if err := c.postJSON(ctx, "/v1/tools/search", in, &out); err != nil {
		return nil, err
	}
	return out.Results, nil
}

// Page is a fetched page.
type Page struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

// Fetch reads one page as Markdown.
func (c *Client) Fetch(ctx context.Context, url string) (Page, error) {
	var p Page
	if len(url) > MaxURL {
		return p, fmt.Errorf("the URL is longer than %d characters", MaxURL)
	}
	err := c.postJSON(ctx, "/v1/tools/fetch", map[string]string{"url": url}, &p)
	return p, err
}

// ---------------------------------------------------------------- ocr

// OCR purposes.
const (
	PurposeText   = "text_recognition"
	PurposeLayout = "rich_layout_recognition"
)

// OCRResult is the recognition of a document.
type OCRResult struct {
	PageCount int `json:"page_count"`
	Pages     []struct {
		Index        int    `json:"index"`
		Text         string `json:"text"`
		FinishReason string `json:"finish_reason"`
	} `json:"pages"`
}

// Text joins the pages, with page headings for multi-page documents and
// the grounding markers (<｜ref｜>, <｜box｜>, coordinates) removed.
func (r OCRResult) Text() string {
	var b strings.Builder
	for i, p := range r.Pages {
		if len(r.Pages) > 1 {
			if i > 0 {
				b.WriteString("\n\n")
			}
			fmt.Fprintf(&b, "## 第 %d 页\n\n", p.Index+1)
		}
		b.WriteString(CleanOCR(p.Text))
		if p.FinishReason == "length" {
			b.WriteString("\n[本页文字过长，识别结果不完整]")
		}
	}
	return strings.TrimSpace(b.String())
}

// OCR recognizes the text of a PDF, Word / PowerPoint file or image. The
// server keeps the connection alive with newlines before the JSON body.
func (c *Client) OCR(ctx context.Context, name string, data []byte, purpose string) (OCRResult, error) {
	var r OCRResult
	if len(data) > MaxOCRBytes {
		return r, fmt.Errorf("the file is larger than %d MiB", MaxOCRBytes>>20)
	}
	if purpose == "" {
		purpose = PurposeText
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("purpose", purpose)
	fw, err := mw.CreateFormFile("file", safeFileName(name, "document"))
	if err != nil {
		return r, err
	}
	fw.Write(data)
	mw.Close()
	resp, err := c.do(ctx, "/v1/tools/ocr", mw.FormDataContentType(), &body)
	if err != nil {
		return r, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return r, err
	}
	var env struct {
		OCRResult
		Error *struct {
			Message string `json:"message"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(b), &env); err != nil {
		return r, fmt.Errorf("DeepSeek OCR: unreadable response: %w", err)
	}
	if env.Error != nil {
		return r, &APIError{Status: 200, Message: env.Error.Message, Code: fmt.Sprint(env.Error.Code)}
	}
	return env.OCRResult, nil
}

// ---------------------------------------------------------------- voice

// Transcript is an ASR result.
type Transcript struct {
	Text       string `json:"text"`
	DurationMS int64  `json:"duration_ms"`
}

// Transcribe turns speech (16-bit PCM WAV or Ogg Opus, ≤ 8 MiB, ≤ 120 s)
// into text; the language is detected.
func (c *Client) Transcribe(ctx context.Context, name string, audio []byte) (Transcript, error) {
	var t Transcript
	if len(audio) > MaxASRBytes {
		return t, fmt.Errorf("the recording is larger than %d MiB", MaxASRBytes>>20)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", safeFileName(name, "speech.wav"))
	if err != nil {
		return t, err
	}
	fw.Write(audio)
	mw.Close()
	resp, err := c.do(ctx, "/v1/tools/asr", mw.FormDataContentType(), &body)
	if err != nil {
		return t, err
	}
	defer resp.Body.Close()
	err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&t)
	return t, err
}

// Voices of text to speech.
var Voices = []string{"stella", "tide"}

// Speak synthesizes text (Markdown is filtered by the service) as WAV.
func (c *Client) Speak(ctx context.Context, text, voice string) ([]byte, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("nothing to read")
	}
	text = ClipBillable(text, MaxTTSChars)
	if voice != "tide" {
		voice = "stella"
	}
	b, _ := json.Marshal(map[string]any{"text": text, "voice_id": voice, "format": "wav", "filter_markdown": true})
	if len(b) > MaxTTSBytes {
		return nil, errors.New("the text is too long to read aloud")
	}
	resp, err := c.do(ctx, "/v1/tools/tts", "application/json", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// up to 30 minutes of 24 kHz 16-bit mono
	return io.ReadAll(io.LimitReader(resp.Body, 100<<20))
}

// ---------------------------------------------------------------- helpers

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// ClipBillable cuts s to n billable characters (CJK and other wide
// characters count 2, the rest 1), at a sentence end when one is near.
func ClipBillable(s string, n int) string {
	used := 0
	for i, r := range s {
		w := 1
		if r > 0x2E7F {
			w = 2
		}
		if used+w > n {
			cut := s[:i]
			if j := strings.LastIndexAny(cut, "。！？.!?\n"); j > len(cut)*3/4 {
				cut = cut[:j+1]
			}
			return cut
		}
		used += w
	}
	return s
}

func safeFileName(name, def string) string {
	name = strings.Map(func(r rune) rune {
		if r == '"' || r == '\\' || r == '/' || r < 0x20 {
			return '_'
		}
		return r
	}, name)
	if strings.TrimSpace(name) == "" {
		return def
	}
	return name
}

var markerRe = strings.NewReplacer("<｜ref｜>", "", "<｜/ref｜>", "", "<｜det｜>", "", "<｜/det｜>", "", "<｜box｜>", "", "<｜/box｜>", "")

// CleanOCR removes grounding markers and their coordinate lists.
func CleanOCR(s string) string {
	s = coordRe.ReplaceAllString(s, "")
	return strings.TrimSpace(markerRe.Replace(s))
}

// timeout helpers used by the plugin
const (
	searchTimeout = 30 * time.Second
	fetchTimeout  = 60 * time.Second
	ocrTimeout    = 190 * time.Second
	asrTimeout    = 60 * time.Second
	ttsTimeout    = 120 * time.Second
)
