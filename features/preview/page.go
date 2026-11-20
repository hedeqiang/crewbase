package preview

// show_page: a visual reply. The agent answers with a page (a report, a
// dashboard, a chart, a comparison, a small interactive demo) that appears
// in the conversation itself, sandboxed on its own origin like every
// preview. Before it is shown it is rendered in a throwaway headless
// browser at a desktop and a phone size: the agent gets both screenshots
// (a vision model sees them) and the problems found (script errors, files
// that did not load, a blank page, a page wider than a phone). With
// problems the page is not shown until they are fixed (or the agent says
// they are intended); the person only sees a page that was checked.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/hedeqiang/crewbase/capabilities/browser"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

// EventPage is the session event of a page shown in the conversation.
const EventPage = "page/shown"

// PagesDir is where pages given as html are written (workspace-relative).
const PagesDir = "pages"

// shotsDir keeps the check screenshots (not the agent's work).
const shotsDir = ".crewbase/pages"

// checkSizes: a laptop and a phone.
var checkSizes = []browser.Size{{Width: 1280, Height: 800}, {Width: 390, Height: 844}}

// CheckTimeout bounds rendering one page.
var CheckTimeout = 40 * time.Second

// PageShown is the page/shown event data.
type PageShown struct {
	Title string `json:"title"`
	Path  string `json:"path"` // workspace-relative
	URL   string `json:"url"`  // may be stale; the UI asks again for the path
	Agent string `json:"agent,omitempty"`
	Call  string `json:"call,omitempty"`
	// Checked: rendered before it was shown (false: no browser to check with).
	Checked bool `json:"checked"`
	// Notes: problems the agent said were intended (force).
	Notes []string  `json:"notes,omitempty"`
	Time  time.Time `json:"time"`
}

const pageDescription = "Answer with a page the person sees in the conversation: a visual reply. Use it when the answer is better seen than read — " +
	"a report or summary with charts, a dashboard, a comparison table, a timeline, a diagram, a small interactive demo or calculator. " +
	"Keep plain answers as text; one page per answer.\n" +
	"Pass html (a whole page: CSS and JS inline; it is saved as pages/<title>.html) or path (an .html file in your workspace you wrote, " +
	"which may load files next to it). Make it look good at any width (it is shown about 700px wide, and on phones), in light and dark " +
	"(prefers-color-scheme), with real content (no lorem ipsum). It runs sandboxed on its own origin: no access to Crewbase; it may load " +
	"scripts from public CDNs (e.g. Chart.js from cdn.jsdelivr.net).\n" +
	"Before it is shown it is rendered at 1280×800 and 390×844: you get both screenshots and any problems (script errors, files that did " +
	"not load, a blank page, a page wider than a phone). If there are problems it is NOT shown: fix them and call show_page again. " +
	"Only if a problem is intended, pass force: true. Look at the screenshots too; if it looks wrong, fix it and call again (the new " +
	"version replaces the old one). After it is shown, say in a sentence or two what it shows — the page is the answer, do not repeat it."

func (m *Manager) pageTool(c *kernel.Context) *tools.Tool {
	return &tools.Tool{
		Name:        "show_page",
		Description: pageDescription,
		Params: tools.Schema(map[string]any{
			"title": tools.Prop("string", "Short title of the page, e.g. 三季度销售概览"),
			"html":  tools.Prop("string", "The whole page (<!doctype html>…), CSS and JS inline."),
			"path":  tools.Prop("string", "Or an .html file in your workspace (relative)."),
			"force": tools.Prop("boolean", "Show it even though the check found problems (only when they are intended)."),
		}, "title"),
		Category: tools.CategoryWrite,
		PathArgs: []string{"path"},
		Classify: func(call tools.Call) (string, string) {
			if call.String("html") == "" {
				return tools.CategoryRead, "show page " + call.String("path")
			}
			return tools.CategoryWrite, "show page " + call.String("title")
		},
		Execute: func(ctx context.Context, call tools.Call) (tools.Result, error) {
			var r browser.Renderer
			if b, ok := kernel.Lookup[browser.Browser](c, browser.ServiceKey); ok {
				r, _ = b.(browser.Renderer)
			}
			return m.showPage(ctx, call, r)
		},
	}
}

func (m *Manager) showPage(ctx context.Context, call tools.Call, r browser.Renderer) (tools.Result, error) {
	var args struct {
		Title string `json:"title"`
		HTML  string `json:"html"`
		Path  string `json:"path"`
		Force bool   `json:"force"`
	}
	if err := call.Decode(&args); err != nil {
		return tools.Errorf("invalid arguments: %v", err), nil
	}
	args.Title, args.Path = strings.TrimSpace(args.Title), strings.TrimSpace(args.Path)
	if args.Title == "" {
		return tools.Errorf("give the page a title"), nil
	}
	if (args.HTML == "") == (args.Path == "") {
		return tools.Errorf("pass html (the whole page) or path (an .html file in your workspace), not both"), nil
	}
	rel := args.Path
	if args.HTML != "" {
		if len(args.HTML) > MaxSnippet {
			return tools.Errorf("the page is too large (at most %d MB); put data or images in files next to it and pass path", MaxSnippet>>20), nil
		}
		var err error
		if rel, err = writePage(call.Workspace, args.Title, args.HTML); err != nil {
			return tools.Errorf("show_page: %v", err), nil
		}
	} else if ext := strings.ToLower(filepath.Ext(rel)); ext != ".html" && ext != ".htm" {
		return tools.Errorf("path must be an .html file"), nil
	}
	o, err := m.OpenPath(call.Agent, call.Workspace, rel)
	if err != nil {
		return tools.Errorf("show_page: %v", err), nil
	}
	shown := PageShown{Title: args.Title, Path: o.Path, URL: o.URL, Agent: call.Agent, Call: call.ID, Time: time.Now().UTC()}

	var b strings.Builder
	var atts []llm.Attachment
	var problems []string
	if r != nil {
		cctx, cancel := context.WithTimeout(ctx, CheckTimeout)
		res, err := r.Render(cctx, o.URL, browser.RenderOptions{Sizes: checkSizes})
		cancel()
		if err != nil {
			fmt.Fprintf(&b, "The page could not be checked (%v); it is shown unchecked.\n", err)
		} else {
			shown.Checked = true
			problems = pageProblems(res)
			atts = saveShots(call.Workspace, o.Path, res)
		}
	} else {
		b.WriteString("There is no browser here to check the page with; it is shown unchecked.\n")
	}

	if len(problems) > 0 && !args.Force {
		b.WriteString("NOT shown yet — the check found problems:\n")
		for _, p := range problems {
			b.WriteString("- " + p + "\n")
		}
		fmt.Fprintf(&b, "Fix them in %s (or pass html again) and call show_page again; pass force: true only if they are intended.", o.Path)
		if len(atts) > 0 {
			b.WriteString(" The screenshots (desktop, phone) are attached.")
		}
		return tools.Result{Content: b.String(), Attachments: atts}, nil
	}
	if args.Force {
		shown.Notes = problems
	}
	if call.Session != nil {
		if _, err := call.Session.Append(EventPage, call.Agent, shown); err != nil {
			return tools.Errorf("the page could not be recorded: %v", err), nil
		}
	}
	fmt.Fprintf(&b, "Shown to the person in the conversation: %q (%s).", args.Title, o.Path)
	if shown.Checked {
		if len(problems) == 0 {
			b.WriteString(" Checked at desktop and phone size: no script errors, everything loaded, nothing wider than the screen.")
		} else {
			b.WriteString(" Shown with the problems you said were intended.")
		}
		if len(atts) > 0 {
			b.WriteString(" Look at the screenshots (attached): if something looks off, fix it and call show_page again; the new version replaces this one.")
		}
	}
	b.WriteString(" Now say briefly what it shows; do not repeat its content.")
	return tools.Result{Content: b.String(), Data: shown, Attachments: atts}, nil
}

// pageProblems are what is wrong with a rendered page.
func pageProblems(r browser.Rendered) []string {
	var out []string
	for _, e := range r.Errors {
		out = append(out, "script error: "+e)
	}
	for _, f := range r.Failed {
		out = append(out, "did not load: "+f)
	}
	for _, s := range r.Shots {
		if s.TextChars == 0 && s.Elements < 3 {
			out = append(out, fmt.Sprintf("the page is blank at %d×%d", s.Width, s.Height))
			break
		}
	}
	for _, s := range r.Shots {
		if s.Width < 600 && s.Overflow > 8 {
			out = append(out, fmt.Sprintf("at %dpx wide (a phone) the page is %dpx wider than the screen; let it wrap or scroll inside its own box", s.Width, s.Overflow))
		}
	}
	return out
}

// saveShots writes the screenshots to the workspace for the model to see.
func saveShots(ws, page string, r browser.Rendered) []llm.Attachment {
	sum := sha256.Sum256([]byte(page))
	base := hex.EncodeToString(sum[:6])
	var out []llm.Attachment
	for _, s := range r.Shots {
		if len(s.PNG) == 0 {
			continue
		}
		name := fmt.Sprintf("%s-%dx%d.png", base, s.Width, s.Height)
		rel := shotsDir + "/" + name
		p := filepath.Join(ws, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			continue
		}
		if err := os.WriteFile(p, s.PNG, 0o644); err != nil {
			continue
		}
		label := "desktop"
		if s.Width < 600 {
			label = "phone"
		}
		out = append(out, llm.Attachment{Path: rel, Name: fmt.Sprintf("%s %d×%d.png", label, s.Width, s.Height), Mime: "image/png", Size: int64(len(s.PNG))})
	}
	return out
}

// writePage saves html as pages/<title>.html in the workspace.
func writePage(ws, title, html string) (string, error) {
	if ws == "" {
		return "", fmt.Errorf("no workspace")
	}
	rel := PagesDir + "/" + slug(title) + ".html"
	p := filepath.Join(ws, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	return rel, os.WriteFile(p, []byte(html), 0o644)
}

// slug is a file name from a title (letters of any script, digits, -).
func slug(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range title {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
		if b.Len() > 60 {
			break
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		sum := sha256.Sum256([]byte(title))
		s = "page-" + hex.EncodeToString(sum[:3])
	}
	return s
}
