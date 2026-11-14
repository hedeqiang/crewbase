// Package browsertools is the consumer side of the `browser` capability:
// the model-facing browser_* tools (plugin @crewbase/browser-tools) and the
// takeover API for people (plugin @crewbase/browser-api, api.go).
//
// The model never receives screenshots: browser_snapshot returns a compact
// text outline with numbered interactive elements. While a person controls
// the browser every browser_* tool fails with "the user is controlling the
// browser" and nothing about the page is returned (and therefore nothing is
// logged). Typing into password / one-time-code / card fields is refused;
// the agent asks for a takeover instead (docs/03 §4.4, docs/06 §6).
package browsertools

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/browser"
	"github.com/hedeqiang/crewbase/capabilities/notify"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

// Config is the plugin config.
type Config struct {
	// MaxSnapshotChars caps the outline returned to the model (default 12000).
	MaxSnapshotChars int `json:"maxSnapshotChars,omitempty"`
	// ActionTimeout bounds one browser action (default 30s).
	ActionTimeout string `json:"actionTimeout,omitempty"`
	// TakeoverTimeout bounds browser_request_takeover (default 15m).
	TakeoverTimeout string `json:"takeoverTimeout,omitempty"`
}

// Tool names.
const (
	ToolOpen            = "browser_open"
	ToolSnapshot        = "browser_snapshot"
	ToolClick           = "browser_click"
	ToolType            = "browser_type"
	ToolBack            = "browser_back"
	ToolRequestTakeover = "browser_request_takeover"
)

// TakenOverMessage is the only thing the model learns while a person holds
// the browser.
const TakenOverMessage = "the user is controlling the browser; wait until they hand control back (do not retry in a loop)"

type bt struct {
	c        *kernel.Context
	b        browser.Browser
	max      int
	action   time.Duration
	takeover time.Duration
}

func (t *bt) agents() (*agent.Registry, bool) {
	return kernel.Lookup[*agent.Registry](t.c, agent.ServiceKey)
}

// fail maps provider errors to model-facing results, hiding everything
// while the browser is taken over.
func fail(err error) (tools.Result, error) {
	if errors.Is(err, browser.ErrTakenOver) {
		return tools.Errorf("%s", TakenOverMessage), nil
	}
	return tools.Errorf("browser: %v", err), nil
}

func (t *bt) session(ctx context.Context, call tools.Call) (*browser.Session, context.Context, context.CancelFunc, error) {
	if call.Agent == "" {
		return nil, nil, nil, errors.New("no agent for this call")
	}
	ctx, cancel := context.WithTimeout(ctx, t.action)
	s, err := t.b.Session(ctx, call.Agent)
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}
	return s, ctx, cancel, nil
}

// settle waits briefly for a navigation started by an action to finish.
func settle(ctx context.Context, s *browser.Session) error {
	time.Sleep(150 * time.Millisecond)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var st string
		err := s.Eval(ctx, readyJS, &st)
		if errors.Is(err, browser.ErrTakenOver) || ctx.Err() != nil {
			return err
		}
		if err == nil && st == "complete" {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

func (t *bt) snap(ctx context.Context, s *browser.Session, prefix string) (tools.Result, error) {
	var snapObj snapshot
	var err error
	for i := 0; i < 3; i++ { // the page may be mid-navigation
		if err = s.Eval(ctx, snapshotJS, &snapObj); err == nil || errors.Is(err, browser.ErrTakenOver) || ctx.Err() != nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		return fail(err)
	}
	return tools.Result{
		Content: prefix + snapObj.render(t.max),
		Data: map[string]any{"kind": "browser-snapshot", "url": snapObj.URL, "title": snapObj.Title,
			"refs": snapObj.Refs, "login": snapObj.Hints.Password, "otp": snapObj.Hints.OTP, "captcha": snapObj.Hints.Captcha},
	}, nil
}

func checkURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "about:blank" {
		return raw, nil
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("only http(s) URLs can be opened, got %q", raw)
	}
	return u.String(), nil
}

func refArg(call tools.Call) (int, error) {
	var a struct {
		Ref any `json:"ref"`
	}
	if err := call.Decode(&a); err != nil {
		return 0, err
	}
	var n int
	switch v := a.Ref.(type) {
	case float64:
		n = int(v)
	case string:
		_, err := fmt.Sscanf(strings.Trim(strings.TrimSpace(v), "[]"), "%d", &n)
		if err != nil {
			return 0, fmt.Errorf("ref must be a number like 3")
		}
	}
	if n <= 0 {
		return 0, fmt.Errorf("ref must be a positive element number from browser_snapshot")
	}
	return n, nil
}

const staleMsg = "element [%d] is not on the page any more (the page changed); call browser_snapshot and use the new numbers"

func (t *bt) open(ctx context.Context, call tools.Call) (tools.Result, error) {
	u, err := checkURL(call.String("url"))
	if err != nil {
		return tools.Errorf("%v", err), nil
	}
	s, ctx, cancel, err := t.session(ctx, call)
	if err != nil {
		return fail(err)
	}
	defer cancel()
	if err := s.Navigate(ctx, u); err != nil {
		return fail(err)
	}
	return t.snap(ctx, s, "")
}

func (t *bt) snapshotTool(ctx context.Context, call tools.Call) (tools.Result, error) {
	s, ctx, cancel, err := t.session(ctx, call)
	if err != nil {
		return fail(err)
	}
	defer cancel()
	return t.snap(ctx, s, "")
}

func (t *bt) click(ctx context.Context, call tools.Call) (tools.Result, error) {
	n, err := refArg(call)
	if err != nil {
		return tools.Errorf("%v", err), nil
	}
	s, ctx, cancel, err := t.session(ctx, call)
	if err != nil {
		return fail(err)
	}
	defer cancel()
	var r struct {
		Error   string  `json:"error"`
		Clicked bool    `json:"clicked"`
		X       float64 `json:"x"`
		Y       float64 `json:"y"`
	}
	if err := s.Eval(ctx, fmt.Sprintf(prepareClickJS, n), &r); err != nil {
		return fail(err)
	}
	if r.Error != "" {
		return tools.Errorf(staleMsg, n), nil
	}
	if !r.Clicked {
		if err := s.Click(ctx, r.X, r.Y); err != nil {
			return fail(err)
		}
	}
	if err := settle(ctx, s); err != nil {
		return fail(err)
	}
	return t.snap(ctx, s, fmt.Sprintf("Clicked [%d].\n", n))
}

func (t *bt) typeTool(ctx context.Context, call tools.Call) (tools.Result, error) {
	n, err := refArg(call)
	if err != nil {
		return tools.Errorf("%v", err), nil
	}
	var a struct {
		Text   string `json:"text"`
		Submit bool   `json:"submit"`
	}
	if err := call.Decode(&a); err != nil {
		return tools.Errorf("%v", err), nil
	}
	s, ctx, cancel, err := t.session(ctx, call)
	if err != nil {
		return fail(err)
	}
	defer cancel()
	var r struct {
		Error     string `json:"error"`
		Sensitive string `json:"sensitive"`
		OK        bool   `json:"ok"`
	}
	if err := s.Eval(ctx, fmt.Sprintf(prepareTypeJS, n), &r); err != nil {
		return fail(err)
	}
	switch {
	case r.Sensitive != "":
		return tools.Result{IsError: true, Content: fmt.Sprintf(
			"refused: [%d] is a %s field. Never type credentials, codes or card numbers yourself. "+
				"Call browser_request_takeover with a short reason so the user can fill it in, then continue.", n, r.Sensitive)}, nil
	case r.Error == "stale":
		return tools.Errorf(staleMsg, n), nil
	case r.Error != "":
		return tools.Errorf("cannot type into [%d]: %s", n, r.Error), nil
	}
	if a.Text != "" {
		if err := s.InsertText(ctx, a.Text); err != nil {
			return fail(err)
		}
	}
	prefix := fmt.Sprintf("Typed into [%d].\n", n)
	if a.Submit {
		if err := s.Key(ctx, "Enter"); err != nil {
			return fail(err)
		}
		if err := settle(ctx, s); err != nil {
			return fail(err)
		}
		prefix = fmt.Sprintf("Typed into [%d] and pressed Enter.\n", n)
	}
	return t.snap(ctx, s, prefix)
}

func (t *bt) back(ctx context.Context, call tools.Call) (tools.Result, error) {
	s, ctx, cancel, err := t.session(ctx, call)
	if err != nil {
		return fail(err)
	}
	defer cancel()
	if err := s.Back(ctx); err != nil {
		if errors.Is(err, browser.ErrTakenOver) {
			return fail(err)
		}
		return tools.Errorf("cannot go back: %v", err), nil
	}
	if err := settle(ctx, s); err != nil {
		return fail(err)
	}
	return t.snap(ctx, s, "Went back.\n")
}

// safeURL drops the query and fragment (they can carry login tokens).
func safeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.RawQuery, u.Fragment, u.User = "", "", nil
	return u.String()
}

func (t *bt) requestTakeover(ctx context.Context, call tools.Call) (tools.Result, error) {
	reason := strings.TrimSpace(call.String("reason"))
	if reason == "" {
		reason = "login"
	}
	reason = truncate(strings.Join(strings.Fields(reason), " "), 200)
	if call.Agent == "" {
		return tools.Errorf("no agent for this call"), nil
	}
	// Make sure there is a browser to take over (no-op if one runs or the
	// user already holds it).
	sctx, cancel := context.WithTimeout(ctx, t.action)
	_, err := t.b.Session(sctx, call.Agent)
	cancel()
	if err != nil && !errors.Is(err, browser.ErrTakenOver) {
		return fail(err)
	}

	detail := "takeover:" + reason
	agents, hasAgents := t.agents()
	var a *agent.Agent
	if hasAgents {
		a, _ = agents.Get(call.Agent)
	}
	if a != nil {
		sid := ""
		if call.Session != nil {
			sid = call.Session.ID()
		}
		a.SetStatusIn(agent.StatusWaiting, detail, sid)
	}
	if n, ok := kernel.Lookup[*notify.Service](t.c, notify.ServiceKey); ok {
		name := call.Agent
		if a != nil {
			name = a.Spec().Name
		}
		sess := ""
		if call.Session != nil {
			sess = call.Session.ID()
		}
		go n.Send(context.WithoutCancel(ctx), notify.Notification{Kind: notify.KindNeedsYou, Urgent: true,
			Agent: call.Agent, Session: sess, Title: name + " 等你接管浏览器", Body: reason})
	}

	wctx, wcancel := context.WithTimeout(ctx, t.takeover)
	st, err := t.b.AwaitHandback(wctx, call.Agent)
	wcancel()
	if a != nil && ctx.Err() == nil {
		if cur, d := a.Status(); cur == agent.StatusWaiting && d == detail {
			a.SetStatus(agent.StatusRunning, "")
		}
	}
	switch {
	case ctx.Err() != nil:
		return tools.Errorf("cancelled while waiting for the user"), nil
	case err != nil:
		if cur := t.b.State(call.Agent); cur.TakenOverBy != "" {
			return tools.Errorf("%s (still controlled after %s)", TakenOverMessage, t.takeover), nil
		}
		return tools.Errorf("nobody took over the browser within %s; tell the user what you need and stop, or try another way", t.takeover), nil
	}
	where := safeURL(st.URL)
	return tools.Result{
		Content: fmt.Sprintf("control returned; page now at %s (%q). Call browser_snapshot to continue.", where, st.Title),
		Data:    map[string]any{"kind": "browser-takeover", "reason": reason, "url": where},
	}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Tools returns the tool definitions bound to b. Exported for tests.
func (t *bt) tools() []*tools.Tool {
	refProp := map[string]any{"type": "integer", "description": "element number [n] from the latest browser_snapshot"}
	return []*tools.Tool{
		{Name: ToolOpen, Category: tools.CategoryRead,
			Description: "Open a web page in your own browser (it keeps your logins) and return a text snapshot with numbered interactive elements.",
			Params:      tools.Schema(map[string]any{"url": tools.Prop("string", "http(s) URL")}, "url"),
			Classify: func(c tools.Call) (string, string) {
				return "", "open " + truncate(c.String("url"), 200)
			},
			Execute: t.open},
		{Name: ToolSnapshot, Category: tools.CategoryRead,
			Description: "Return the current page as text: title, URL and an outline where interactive elements are numbered [n]. No screenshots.",
			Params:      tools.Schema(map[string]any{}), Execute: t.snapshotTool},
		{Name: ToolClick, Category: tools.CategoryRead,
			Description: "Click element [n] from the latest snapshot; returns the new snapshot.",
			Params:      tools.Schema(map[string]any{"ref": refProp}, "ref"),
			Classify: func(c tools.Call) (string, string) {
				n, _ := refArg(c)
				return "", fmt.Sprintf("click [%d] in the browser", n)
			},
			Execute: t.click},
		{Name: ToolType, Category: tools.CategoryWrite,
			Description: "Type text into input [n] (replacing its content); submit=true presses Enter. Refused for password, one-time-code and card fields: use browser_request_takeover for those.",
			Params: tools.Schema(map[string]any{"ref": refProp, "text": tools.Prop("string", "text to type"),
				"submit": tools.Prop("boolean", "press Enter afterwards")}, "ref", "text"),
			Classify: func(c tools.Call) (string, string) {
				n, _ := refArg(c)
				return "", fmt.Sprintf("type %q into [%d] in the browser", truncate(c.String("text"), 80), n)
			},
			Execute: t.typeTool},
		{Name: ToolBack, Category: tools.CategoryRead,
			Description: "Go back to the previous page; returns the new snapshot.",
			Params:      tools.Schema(map[string]any{}), Execute: t.back},
		{Name: ToolRequestTakeover, Category: tools.CategoryNone,
			Description: "Ask the user to take over your browser for a login, 2FA code or captcha. Blocks until they hand control back; you stay logged in afterwards. Never ask for or type credentials yourself.",
			Params:      tools.Schema(map[string]any{"reason": tools.Prop("string", "what the user should do, e.g. \"sign in to GitHub\"")}, "reason"),
			Execute:     t.requestTakeover},
	}
}

// Plugin registers the browser_* tools.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/browser-tools",
	Inject:      []string{tools.ServiceKey, browser.ServiceKey},
	Optional:    []string{agent.ServiceKey, notify.ServiceKey},
	Description: "browser_open / snapshot / click / type / back / request_takeover (text snapshots only; no screenshots to the model).",
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		t := &bt{c: c, b: kernel.Use[browser.Browser](c, browser.ServiceKey), max: cfg.MaxSnapshotChars,
			action: 30 * time.Second, takeover: 15 * time.Minute}
		if t.max <= 0 {
			t.max = 12000
		}
		if cfg.ActionTimeout != "" {
			if t.action, err = time.ParseDuration(cfg.ActionTimeout); err != nil {
				return fmt.Errorf("browser-tools: actionTimeout: %w", err)
			}
		}
		if cfg.TakeoverTimeout != "" {
			if t.takeover, err = time.ParseDuration(cfg.TakeoverTimeout); err != nil {
				return fmt.Errorf("browser-tools: takeoverTimeout: %w", err)
			}
		}
		reg := kernel.Use[*tools.Registry](c, tools.ServiceKey)
		for _, tool := range t.tools() {
			reg.Register(c, tool)
		}
		return nil
	},
})
