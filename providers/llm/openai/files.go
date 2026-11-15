package openai

// Attachments on Chat Completions:
//   - images go inline as image_url data URLs to vision models;
//   - PDFs go inline as `file` parts (file_data) to models that list "pdf"
//     (magpie reports it per model) and to OpenAI's own vision models;
//   - DeepSeek (direct API, api-docs.deepseek.com/zh-cn/guides/document_parsing):
//     PDF / Word / PowerPoint are uploaded once to POST /files with purpose
//     text_recognition and referenced as {"type":"file","file_id":...}; the
//     ids are kept per workspace (uploads/.remote/<provider>.json) so a file
//     is parsed (and billed) once, not on every turn;
//   - anything else, or an upload that failed, is sent as text.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	sdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"

	"github.com/hedeqiang/crewbase/core/llm"
)

// visionModel guesses image input for models whose capabilities are unknown.
var visionModel = regexp.MustCompile(`(?i)(^|/)(gpt-4o|gpt-4\.1|gpt-5|gpt-6|o3|o4|grok-4|grok-2-vision|claude|gemini|pixtral|llama-4|deepseek-flash|kimi-k2\.5|kimi-latest)|(-vl|vl-|vision|qwen3\.5|glm-4\.\dv|glm-5v)`)

const (
	mimeDOC  = "application/msword"
	mimeDOCX = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	mimePPT  = "application/vnd.ms-powerpoint"
	mimePPTX = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
)

// dsDocument reports types DeepSeek parses through its Files API.
func dsDocument(mime string) bool {
	switch mime {
	case "application/pdf", mimeDOC, mimeDOCX, mimePPT, mimePPTX:
		return true
	}
	return false
}

// directDeepSeek is the DeepSeek API itself (its Files API is not
// available through gateways such as magpie).
func (a *Adapter) directDeepSeek() bool { return a.effortStyle == "deepseek" }

func (a *Adapter) modelCap(model, c string) (has, known bool) {
	if s, ok := findSpec(a.static, model); ok && len(s.Capabilities) > 0 {
		return hasCap(s.Capabilities, c), true
	}
	return false, false
}

// AcceptsFile implements llm.FileAccepter.
func (a *Adapter) AcceptsFile(model, mime string) bool {
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		if has, known := a.modelCap(model, "vision"); known {
			return has
		}
		return visionModel.MatchString(model)
	}
	if a.directDeepSeek() && dsDocument(mime) {
		return true
	}
	if mime == "application/pdf" {
		if has, known := a.modelCap(model, "pdf"); known && has {
			return true
		}
		// OpenAI's own API reads PDFs on its vision models
		return strings.HasPrefix(a.baseURL, "https://api.openai.com") && a.AcceptsFile(model, "image/png")
	}
	return false
}

// userParts maps a user message holding attachments to content parts.
func (a *Adapter) userParts(parts []llm.Part, files map[string]string) []sdk.ChatCompletionContentPartUnionParam {
	var out []sdk.ChatCompletionContentPartUnionParam
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			out = append(out, sdk.TextContentPart(text.String()))
			text.Reset()
		}
	}
	for _, p := range parts {
		switch p.Type {
		case "text":
			text.WriteString(p.Text)
		case llm.PartAttachment:
			at := p.Attachment
			if at == nil {
				continue
			}
			if text.Len() > 0 {
				text.WriteString("\n\n")
			}
			switch {
			case len(at.Data) > 0 && at.IsImage():
				text.WriteString(at.Fallback())
				flush()
				out = append(out, sdk.ImageContentPart(sdk.ChatCompletionContentPartImageImageURLParam{
					URL: "data:" + at.Mime + ";base64," + base64.StdEncoding.EncodeToString(at.Data),
				}))
			case len(at.Data) > 0 && files[at.SHA256] != "":
				text.WriteString(at.Fallback())
				flush()
				// DeepSeek takes the id flat ({"type":"file","file_id":…}),
				// not nested under "file" as OpenAI's SDK writes it
				out = append(out, param.Override[sdk.ChatCompletionContentPartUnionParam](map[string]string{"type": "file", "file_id": files[at.SHA256]}))
			case len(at.Data) > 0 && at.IsPDF() && !a.directDeepSeek():
				text.WriteString(at.Fallback())
				flush()
				out = append(out, sdk.FileContentPart(sdk.ChatCompletionContentPartFileFileParam{
					Filename: param.NewOpt(at.Name),
					FileData: param.NewOpt("data:application/pdf;base64," + base64.StdEncoding.EncodeToString(at.Data)),
				}))
			case at.Alt != "":
				text.WriteString(at.Alt) // native path unavailable (upload failed)
			default:
				text.WriteString(at.Fallback())
			}
		}
	}
	flush()
	return out
}

func hasAttachment(parts []llm.Part) bool {
	for _, p := range parts {
		if p.Type == llm.PartAttachment && p.Attachment != nil {
			return true
		}
	}
	return false
}

// ---- DeepSeek Files API ----

type remoteFile struct {
	ID      string    `json:"id"`
	Expires time.Time `json:"expires"`
}

// remoteTTL is how long uploads are kept by DeepSeek (expires_after).
const remoteTTL = 30 * 24 * time.Hour

var remoteMu sync.Mutex // guards the per-workspace id files

func (a *Adapter) remotePath(workspace string) string {
	return filepath.Join(workspace, "uploads", ".remote", a.id+".json")
}

func (a *Adapter) loadRemote(workspace string) map[string]remoteFile {
	m := map[string]remoteFile{}
	if workspace == "" {
		return m
	}
	if b, err := os.ReadFile(a.remotePath(workspace)); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func (a *Adapter) saveRemote(workspace string, m map[string]remoteFile) {
	if workspace == "" {
		return
	}
	now := time.Now()
	for k, v := range m {
		if now.After(v.Expires) {
			delete(m, k)
		}
	}
	p := a.remotePath(workspace)
	if os.MkdirAll(filepath.Dir(p), 0o755) != nil {
		return
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	tmp := p + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, p)
	}
}

// uploadDocuments makes sure every document attachment in req has a
// DeepSeek file id; failures are left out (the text form is sent instead).
func (a *Adapter) uploadDocuments(ctx context.Context, req llm.Request, key string) map[string]string {
	if !a.directDeepSeek() {
		return nil
	}
	var todo []*llm.Attachment
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			if at := p.Attachment; p.Type == llm.PartAttachment && at != nil && len(at.Data) > 0 && dsDocument(at.Mime) && at.SHA256 != "" {
				todo = append(todo, at)
			}
		}
	}
	if len(todo) == 0 {
		return nil
	}
	remoteMu.Lock()
	defer remoteMu.Unlock()
	known := a.loadRemote(req.Workspace)
	out := map[string]string{}
	changed := false
	soon := time.Now().Add(time.Hour)
	for _, at := range todo {
		if r, ok := known[at.SHA256]; ok && r.Expires.After(soon) {
			out[at.SHA256] = r.ID
			continue
		}
		id, err := a.uploadFile(ctx, key, at)
		if err != nil {
			continue
		}
		known[at.SHA256] = remoteFile{ID: id, Expires: time.Now().Add(remoteTTL)}
		out[at.SHA256] = id
		changed = true
	}
	if changed {
		a.saveRemote(req.Workspace, known)
	}
	return out
}

// uploadFile sends one document to POST /files. purpose must precede
// file in the form, so the multipart body is written by hand.
func (a *Adapter) uploadFile(ctx context.Context, key string, at *llm.Attachment) (string, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("purpose", "text_recognition")
	_ = w.WriteField("expires_after[anchor]", "created_at")
	_ = w.WriteField("expires_after[seconds]", fmt.Sprint(int(remoteTTL/time.Second)))
	fw, err := w.CreateFormFile("file", at.Name)
	if err != nil {
		return "", err
	}
	_, _ = fw.Write(at.Data)
	if err := w.Close(); err != nil {
		return "", err
	}
	// parsing happens before the response; allow for it
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(a.baseURL, "/")+"/files", &body)
	if err != nil {
		return "", err
	}
	hr.Header.Set("Content-Type", w.FormDataContentType())
	hr.Header.Set("Authorization", "Bearer "+key)
	for k, v := range a.headers {
		hr.Header.Set(k, v)
	}
	resp, err := a.httpClient.Do(hr)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("upload %s: %s: %s", at.Name, resp.Status, strings.TrimSpace(string(b)))
	}
	var f struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &f); err != nil || f.ID == "" {
		return "", fmt.Errorf("upload %s: unexpected response", at.Name)
	}
	return f.ID, nil
}
