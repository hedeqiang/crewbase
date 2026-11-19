package deepseek

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/attachments"
	"github.com/hedeqiang/crewbase/features/browsertools"
	"github.com/hedeqiang/crewbase/features/providers"
	"github.com/hedeqiang/crewbase/features/toolsbasic"
	"github.com/hedeqiang/crewbase/kernel"
)

// ServiceKey is the key of the *Service.
const ServiceKey = "deepseek-tools"

// DefaultKeyRefs are tried after the DeepSeek provider's own key.
var DefaultKeyRefs = []string{"vault:crewbase/deepseek", "env:DEEPSEEK_API_KEY"}

// Config is the plugin config.
type Config struct {
	// KeyRef overrides where the key comes from (default: the DeepSeek
	// provider added in 设置 › 模型, then DefaultKeyRefs).
	KeyRef  string `json:"keyRef,omitempty"`
	BaseURL string `json:"baseURL,omitempty"`
	// Tools: "auto" (default) registers web_search / web_fetch / ocr_file
	// while a key is configured; "off" never.
	Tools string `json:"tools,omitempty"`
	// OCR: "auto" (default) recognizes scanned PDFs, legacy Office files
	// and images for models that cannot see them; "off" never.
	OCR string `json:"ocr,omitempty"`
	// AllowHosts limits web_fetch in read-only and sandbox modes, as for
	// the browser (empty = no limit).
	AllowHosts []string `json:"allowHosts,omitempty"`
	// MaxFetchChars caps a fetched page shown to the model (default 40000).
	MaxFetchChars int `json:"maxFetchChars,omitempty"`
}

// Service is the DeepSeek tool APIs with the key resolved per use.
type Service struct {
	Client *Client
	c      *kernel.Context
	cfg    Config

	mu      sync.Mutex
	key     string
	keyAt   time.Time
	keyFrom string
}

const keyTTL = time.Minute

// Key returns the configured key ("" and ErrNoKey when there is none).
func (s *Service) Key(ctx context.Context) (string, error) {
	s.mu.Lock()
	if s.key != "" && time.Since(s.keyAt) < keyTTL {
		k := s.key
		s.mu.Unlock()
		return k, nil
	}
	s.mu.Unlock()
	cs, ok := kernel.Lookup[*credentials.Service](s.c, credentials.ServiceKey)
	if !ok {
		return "", ErrNoKey
	}
	var refs []string
	if s.cfg.KeyRef != "" {
		refs = []string{s.cfg.KeyRef}
	} else {
		if ps, ok := kernel.Lookup[*providers.Service](s.c, providers.ServiceKey); ok {
			if ref, ok := ps.KeyRefFor("deepseek", "api.deepseek.com"); ok {
				refs = append(refs, ref)
			}
		}
		refs = append(refs, DefaultKeyRefs...)
	}
	for _, ref := range refs {
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		v, err := cs.Resolve(rctx, ref)
		cancel()
		if err == nil && strings.TrimSpace(v) != "" {
			s.mu.Lock()
			s.key, s.keyAt, s.keyFrom = strings.TrimSpace(v), time.Now(), ref
			s.mu.Unlock()
			return strings.TrimSpace(v), nil
		}
	}
	s.forget()
	return "", ErrNoKey
}

func (s *Service) forget() {
	s.mu.Lock()
	s.key, s.keyFrom = "", ""
	s.mu.Unlock()
}

// Available reports whether a key is configured.
func (s *Service) Available(ctx context.Context) bool {
	k, err := s.Key(ctx)
	return err == nil && k != ""
}

// ---------------------------------------------------------------- OCR service

var _ attachments.OCR = (*Service)(nil)

// Recognize implements attachments.OCR.
func (s *Service) Recognize(ctx context.Context, name, mime string, data []byte) (string, error) {
	if s.cfg.OCR == "off" {
		return "", errors.New("OCR is off")
	}
	ctx, cancel := context.WithTimeout(ctx, ocrTimeout)
	defer cancel()
	r, err := s.Client.OCR(ctx, name, data, PurposeText)
	if err != nil {
		return "", err
	}
	return r.Text(), nil
}

// OCRAvailable implements attachments.OCR.
func (s *Service) OCRAvailable(ctx context.Context) bool {
	return s.cfg.OCR != "off" && s.Available(ctx)
}

// ---------------------------------------------------------------- tools

var coordRe = regexp.MustCompile(`\[\[\s*\d+(?:\.\d+)?(?:\s*,\s*\d+(?:\.\d+)?){3}\s*\](?:\s*,\s*\[\s*\d+(?:\.\d+)?(?:\s*,\s*\d+(?:\.\d+)?){3}\s*\])*\]`)

func (s *Service) tools() []*tools.Tool {
	maxFetch := s.cfg.MaxFetchChars
	if maxFetch <= 0 {
		maxFetch = 40000
	}
	return []*tools.Tool{
		{
			Name: "web_search",
			Description: "Search the web (DeepSeek Search) and return ranked results with title, URL, date and a summary. " +
				"Operators in the query: site:example.com, -site:, \"exact phrase\", -word, lang:zh,en, after:2026-01-01, before:2026. " +
				"Use web_fetch to read a result in full. Cite the URLs you rely on.",
			Params: tools.Schema(map[string]any{
				"query":        tools.Prop("string", "Search query, up to 128 characters."),
				"count":        tools.Prop("integer", "Number of results, 1–20 (default 8)."),
				"question":     tools.Prop("string", "The user's original question, if it differs from the query (improves ranking)."),
				"need_content": tools.Prop("boolean", "Also return page bodies (long; default false)."),
			}, "query"),
			Category: tools.CategoryRead,
			Classify: func(call tools.Call) (string, string) {
				return tools.CategoryRead, "web_search: " + clipRunes(call.String("query"), 80)
			},
			Execute: s.search,
		},
		{
			Name: "web_fetch",
			Description: "Read a web page (DeepSeek Fetch) and return its title and body as Markdown. " +
				"For pages that need a login, scripts or interaction, use the browser tools instead.",
			Params: tools.Schema(map[string]any{
				"url":       tools.Prop("string", "http(s) URL of the page."),
				"max_chars": tools.Prop("integer", fmt.Sprintf("Cap on returned characters (default %d).", maxFetch)),
			}, "url"),
			Category: tools.CategoryRead,
			Classify: func(call tools.Call) (string, string) {
				return tools.CategoryRead, "web_fetch: " + clipRunes(call.String("url"), 120)
			},
			Execute: func(ctx context.Context, call tools.Call) (tools.Result, error) { return s.fetch(ctx, call, maxFetch) },
		},
		{
			Name: "ocr_file",
			Description: "Recognize the text of a scanned PDF, an image (photo, screenshot) or a Word / PowerPoint file in the workspace (DeepSeek OCR), page by page. " +
				"Use it when read_file cannot read the file's text. Billed per page.",
			Params: tools.Schema(map[string]any{
				"path":   tools.Prop("string", "File path (relative to the workspace)."),
				"layout": tools.Prop("boolean", "Keep rich layout (tables, formulas) — slower; default false."),
			}, "path"),
			Category: tools.CategoryRead,
			PathArgs: []string{"path"},
			Classify: func(call tools.Call) (string, string) {
				return tools.CategoryRead, "ocr_file: " + call.String("path")
			},
			Execute: s.ocrFile,
		},
	}
}

func toolErr(err error) tools.Result {
	if errors.Is(err, ErrNoKey) {
		return tools.Errorf("%v", err)
	}
	var ae *APIError
	if errors.As(err, &ae) && ae.Retryable() {
		return tools.Errorf("%v (temporary; you may retry once)", err)
	}
	return tools.Errorf("%v", err)
}

func (s *Service) search(ctx context.Context, call tools.Call) (tools.Result, error) {
	var a struct {
		Query       string `json:"query"`
		Count       int    `json:"count"`
		Question    string `json:"question"`
		NeedContent bool   `json:"need_content"`
	}
	if err := call.Decode(&a); err != nil {
		return tools.Errorf("invalid arguments: %v", err), nil
	}
	if a.Count <= 0 {
		a.Count = 8
	}
	a.Count = min(a.Count, 20)
	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()
	res, err := s.Client.Search(ctx, SearchRequest{Query: a.Query, Count: a.Count, OriQuestion: a.Question, NeedContent: a.NeedContent})
	if err != nil {
		return toolErr(err), nil
	}
	if len(res) == 0 {
		return tools.Text(fmt.Sprintf("No results for %q.", a.Query)), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d results for %q:\n", len(res), a.Query)
	for i, r := range res {
		fmt.Fprintf(&b, "\n[%d] %s\n%s\n", i+1, oneLine(r.Title), r.URL)
		var meta []string
		if r.SiteName != nil && *r.SiteName != "" {
			meta = append(meta, *r.SiteName)
		}
		if r.Date != nil && *r.Date != "" {
			meta = append(meta, (*r.Date)[:min(10, len(*r.Date))])
		}
		if r.Authority >= 3 {
			meta = append(meta, "authoritative")
		}
		if len(meta) > 0 {
			b.WriteString(strings.Join(meta, " · ") + "\n")
		}
		text := r.Summary
		if text == "" {
			text = r.Snippet
		}
		b.WriteString(strings.TrimSpace(text) + "\n")
		if a.NeedContent && r.Content != "" {
			c, _ := clip(r.Content, 6000)
			b.WriteString("<content>\n" + c + "\n</content>\n")
		}
	}
	out, _ := clip(b.String(), 100_000)
	return tools.Text(out), nil
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func clip(s string, n int) (string, bool) {
	r := []rune(s)
	if len(r) <= n {
		return s, false
	}
	return string(r[:n]), true
}

func (s *Service) fetch(ctx context.Context, call tools.Call, maxChars int) (tools.Result, error) {
	var a struct {
		URL      string `json:"url"`
		MaxChars int    `json:"max_chars"`
	}
	if err := call.Decode(&a); err != nil {
		return tools.Errorf("invalid arguments: %v", err), nil
	}
	u, err := url.Parse(strings.TrimSpace(a.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return tools.Errorf("web_fetch needs an http(s) URL with a host and no credentials"), nil
	}
	if len(s.cfg.AllowHosts) > 0 && toolsbasic.EffectiveMode(call.Mode) != agent.ModeHost && !browsertools.HostAllowed(s.cfg.AllowHosts, u.Hostname()) {
		return tools.Errorf("%s is not on the allowlist for this run mode; ask the user to allow it or switch to host mode", u.Hostname()), nil
	}
	if a.MaxChars <= 0 || a.MaxChars > 200_000 {
		a.MaxChars = maxChars
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	p, err := s.Client.Fetch(ctx, u.String())
	if err != nil {
		var ae *APIError
		if errors.As(err, &ae) && ae.Status == 403 {
			return tools.Errorf("DeepSeek Fetch refused this page (safety policy); try the browser tools"), nil
		}
		if errors.As(err, &ae) && ae.Status == 404 {
			return tools.Errorf("the page had no readable content (it may need scripts or a login); try the browser tools"), nil
		}
		return toolErr(err), nil
	}
	body, cut := clip(strings.TrimSpace(p.Content), a.MaxChars)
	var b strings.Builder
	if p.Title != "" {
		b.WriteString("# " + oneLine(p.Title) + "\n")
	}
	b.WriteString(u.String() + "\n\n" + body)
	if cut {
		fmt.Fprintf(&b, "\n\n[page truncated at %d characters; pass max_chars for more]", a.MaxChars)
	}
	return tools.Text(b.String()), nil
}

func (s *Service) ocrFile(ctx context.Context, call tools.Call) (tools.Result, error) {
	var a struct {
		Path   string `json:"path"`
		Layout bool   `json:"layout"`
	}
	if err := call.Decode(&a); err != nil {
		return tools.Errorf("invalid arguments: %v", err), nil
	}
	p, err := toolsbasic.ResolvePath(call, a.Path)
	if err != nil {
		return tools.Errorf("%v", err), nil
	}
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return tools.Errorf("%s is not a file", a.Path), nil
	}
	if st.Size() > MaxOCRBytes {
		return tools.Errorf("the file is larger than %d MiB", MaxOCRBytes>>20), nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return tools.Errorf("%v", err), nil
	}
	purpose := PurposeText
	if a.Layout {
		purpose = PurposeLayout
	}
	ctx, cancel := context.WithTimeout(ctx, ocrTimeout)
	defer cancel()
	r, err := s.Client.OCR(ctx, st.Name(), data, purpose)
	if err != nil {
		return toolErr(err), nil
	}
	text := r.Text()
	if text == "" {
		return tools.Text(fmt.Sprintf("No text found in %s (%d pages).", a.Path, r.PageCount)), nil
	}
	out, cut := clip(text, 100_000)
	if cut {
		out += "\n\n[text truncated at 100000 characters]"
	}
	return tools.Text(fmt.Sprintf("Text of %s (%d pages):\n\n%s", a.Path, r.PageCount, out)), nil
}

// ---------------------------------------------------------------- plugin

// Plugin is @crewbase/deepseek-tools.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/deepseek-tools",
	Inject:      []string{tools.ServiceKey},
	Optional:    []string{credentials.ServiceKey, providers.ServiceKey},
	Description: "DeepSeek Search / Fetch / OCR for any model (web_search, web_fetch, ocr_file), OCR of attachments, cloud speech.",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		s := &Service{c: c, cfg: cfg}
		s.Client = &Client{BaseURL: cfg.BaseURL, Key: s.Key}
		if err := c.Provide(ServiceKey, s); err != nil {
			return err
		}
		if err := c.Provide(attachments.OCRKey, attachments.OCR(s)); err != nil {
			return err
		}
		if cfg.Tools == "off" {
			return nil
		}
		reg := kernel.Use[*tools.Registry](c, tools.ServiceKey)
		var (
			mu  sync.Mutex
			sub *kernel.Context // tools registered while a key exists
		)
		sync := func(ctx context.Context) {
			ok := s.Available(ctx)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case ok && sub == nil:
				sub = c.Fork("")
				for _, t := range s.tools() {
					reg.Register(sub, t)
				}
			case !ok && sub != nil:
				sub.Dispose()
				sub = nil
			}
		}
		recheck := make(chan struct{}, 1)
		credentials.RefChanged.Listen(c, func(credentials.RefChange) {
			s.forget()
			select {
			case recheck <- struct{}{}:
			default:
			}
		})
		c.Go(func(ctx context.Context) {
			// providers mount their keys while the host starts
			t := time.NewTimer(2 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-recheck:
					// a provider saves its entry right after its key
					t.Reset(time.Second)
					continue
				case <-t.C:
				}
				sync(ctx)
				t.Reset(5 * time.Minute)
			}
		})
		return nil
	},
})
