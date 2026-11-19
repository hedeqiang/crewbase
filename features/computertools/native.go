package computertools

// The providers' own computer-use tools (docs/computer-use.md §5): Claude's
// computer_toolset_20260801, OpenAI's Responses `computer` tool and
// Gemini's computer_use. Adapters turn each native action into a tool call
// named "computer" (llm.ComputerCall); this file translates it into
// computer.Actions and runs them through the same policy and the same
// executor (Service.one) as computer_act: permission, block list,
// confirmations (coordinate clicks are hit-tested against the outline),
// the idle wait, stop and the action log.
//
// The native tool acts on one app: the one the agent observed or opened
// last with computer_observe / computer_open. Its screenshots show that
// app's window only, so its coordinates are window coordinates:
//
//   - Anthropic and OpenAI: screenshot pixels ÷ Screenshot.Scale;
//   - Gemini: 0–999 (of 1000) × the window size in points.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/computer"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/features/toolsbasic"
	"github.com/hedeqiang/crewbase/kernel"
)

// ToolNative is the tool behind the providers' own computer-use tools.
const ToolNative = llm.NativeComputer

// Messages of the native tool.
const (
	NoTargetMessage = "no app to act on: pick one with computer_apps, then computer_open or computer_observe it. " +
		"The computer tool acts on the app you observed or opened last, and its screenshots show that app's window only"
	SkippedMessage = "Not executed: an earlier computer action in this turn failed."
	noShotMessage  = "take a screenshot first: coordinates are pixels of the latest screenshot of this app's window"
)

// nativeNote tells the model how the two tool sets fit together (in
// computer_observe's description: static, true whether or not the
// provider's tool is declared).
const nativeNote = "If you also have the model provider's own computer tool, it acts on the app you last opened or observed here " +
	"(switch apps with computer_open / computer_observe), and its screenshots show that app's window only. Prefer these element actions (cheaper, background); " +
	"use that tool when you need pixels: clicking what the outline lacks, drawing, dragging."

// geometry maps native coordinates to window points.
type geometry struct {
	scale      float64 // screenshot pixels per point (0 = no screenshot yet)
	winW, winH float64 // window size in points
}

// nativeState is an agent's native-tool state.
type nativeState struct {
	app    string // target app (bundle id)
	shot   geometry
	failed string // the batch that failed (its later actions are skipped)
}

func (s *Service) nativeOf(agentID string) *nativeState {
	if s.native == nil {
		s.native = map[string]*nativeState{}
	}
	st := s.native[agentID]
	if st == nil {
		st = &nativeState{}
		s.native[agentID] = st
	}
	return st
}

// setTarget makes app the native tool's target for agent.
func (s *Service) setTarget(agentID, app string) {
	if app == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.nativeOf(agentID)
	if st.app != app {
		st.app, st.shot = app, geometry{}
	}
}

// targetApp is the native tool's target app for agent.
func (s *Service) targetApp(ctx context.Context, agentID string) (computer.App, bool) {
	s.mu.Lock()
	id := s.nativeOf(agentID).app
	s.mu.Unlock()
	if id == "" {
		return computer.App{}, false
	}
	app, _ := s.resolve(ctx, id)
	return app, true
}

// geometryOf is what native coordinates on app map with: the latest native
// screenshot, else the latest outline's window size.
func (s *Service) geometryOf(agentID, app string) geometry {
	s.mu.Lock()
	g := s.nativeOf(agentID).shot
	o := s.obs[agentID][app]
	s.mu.Unlock()
	if g.winW == 0 && o != nil {
		g.winW, g.winH = o.Window.Frame.W, o.Window.Frame.H
	}
	return g
}

func decodeNative(call tools.Call) (llm.ComputerCall, error) {
	c, ok := llm.DecodeComputerCall(llm.ToolCall{Name: call.Name, Args: call.Args})
	if !ok {
		return c, errors.New("malformed computer call")
	}
	return c, nil
}

// ---------------------------------------------------------------- translation

// nstep is one translated native step: an action, a screenshot or a wait.
type nstep struct {
	act  *computer.Action
	shot bool
	wait time.Duration
	note string // a step done without acting ("pointer moves are not performed")
}

func act(a computer.Action) nstep { return nstep{act: &a} }

// errUnsupported is a native action this computer cannot do.
func errUnsupported(what string) error {
	return fmt.Errorf("%s is not available here; use the computer_* tools (menu, press, key) instead", what)
}

// translate turns a native call into steps on app, mapping coordinates
// with g. It validates everything before anything runs.
func translate(c llm.ComputerCall, app string, g geometry) ([]nstep, error) {
	switch c.Vendor {
	case llm.ComputerAnthropic:
		return translateAnthropic(c.Action, c.Input, app, g)
	case llm.ComputerOpenAI:
		return translateOpenAI(c.Actions, app, g)
	case llm.ComputerGemini:
		return translateGemini(c.Action, c.Input, app, g)
	}
	return nil, fmt.Errorf("unknown computer tool vendor %q", c.Vendor)
}

// pixels maps screenshot pixels to window points.
func (g geometry) pixels(x, y float64) (float64, float64, error) {
	if g.scale <= 0 {
		return 0, 0, errors.New(noShotMessage)
	}
	return x / g.scale, y / g.scale, nil
}

// normalized maps Gemini's 0–999 grid to window points.
func (g geometry) normalized(x, y float64) (float64, float64, error) {
	if g.winW <= 0 || g.winH <= 0 {
		return 0, 0, errors.New("take a screenshot first: the window size is not known yet")
	}
	if x < 0 || y < 0 || x > 1000 || y > 1000 {
		return 0, 0, fmt.Errorf("coordinates (%v, %v) are outside 0–999", x, y)
	}
	return x / 1000 * g.winW, y / 1000 * g.winH, nil
}

func translateAnthropic(member string, raw json.RawMessage, app string, g geometry) ([]nstep, error) {
	var in struct {
		Coordinate      []float64 `json:"coordinate"`
		StartCoordinate []float64 `json:"start_coordinate"`
		Text            string    `json:"text"`
		ScrollDirection string    `json:"scroll_direction"`
		ScrollAmount    int       `json:"scroll_amount"`
		Repeat          int       `json:"repeat"`
		Duration        float64   `json:"duration"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, fmt.Errorf("%s: %v", member, err)
		}
	}
	point := func(c []float64) (float64, float64, error) {
		if len(c) != 2 {
			return 0, 0, fmt.Errorf("%s needs coordinate [x, y]", member)
		}
		return g.pixels(c[0], c[1])
	}
	click := map[string]computer.ActionKind{"left_click": computer.Click, "right_click": computer.RightClick, "double_click": computer.DoubleClick,
		"triple_click": computer.TripleClick, "middle_click": computer.MiddleClick, "mouse_move": computer.MouseMove}
	// text on a click, drag or scroll is the keys held during it ("shift")
	mods := func() (string, error) {
		if in.Text == "" {
			return "", nil
		}
		return MapMods(in.Text)
	}
	switch member {
	case "screenshot":
		return []nstep{{shot: true}}, nil
	case "left_click", "right_click", "double_click", "triple_click", "middle_click", "mouse_move":
		m, err := mods()
		if err != nil {
			return nil, err
		}
		x, y, err := point(in.Coordinate)
		if err != nil {
			return nil, err
		}
		return []nstep{act(computer.Action{Kind: click[member], App: app, X: x, Y: y, Mods: m})}, nil
	case "left_click_drag":
		m, err := mods()
		if err != nil {
			return nil, err
		}
		x, y, err := point(in.StartCoordinate)
		if err != nil {
			return nil, err
		}
		tx, ty, err := point(in.Coordinate)
		if err != nil {
			return nil, err
		}
		return []nstep{act(computer.Action{Kind: computer.Drag, App: app, X: x, Y: y, ToX: tx, ToY: ty, Mods: m})}, nil
	case "scroll":
		m, err := mods()
		if err != nil {
			return nil, err
		}
		x, y, err := point(in.Coordinate)
		if err != nil {
			return nil, err
		}
		n := in.ScrollAmount
		if n <= 0 {
			n = 3
		}
		a := computer.Action{Kind: computer.Scroll, App: app, X: x, Y: y, Mods: m}
		if err := scrollBy(&a, in.ScrollDirection, n); err != nil {
			return nil, err
		}
		return []nstep{act(a)}, nil
	case "type":
		if in.Text == "" {
			return nil, errors.New("type needs text")
		}
		return []nstep{act(computer.Action{Kind: computer.Type, App: app, Text: in.Text})}, nil
	case "key":
		keys, err := MapKeys(in.Text)
		if err != nil {
			return nil, err
		}
		n := max(in.Repeat, 1)
		if n > 100 {
			return nil, errors.New("repeat is at most 100")
		}
		out := make([]nstep, n)
		for i := range out {
			out[i] = act(computer.Action{Kind: computer.Key, App: app, Keys: keys})
		}
		return out, nil
	case "wait":
		return []nstep{{wait: seconds(in.Duration, 1)}}, nil
	case "zoom":
		// a closer look at part of the screen: the next screenshot is the
		// window again (our shots are already of one app at full scale)
		return []nstep{{shot: true, note: "zoom: here is the window at full size; screenshots of one app are not scaled down"}}, nil
	case "left_mouse_down", "left_mouse_up", "hold_key", "cursor_position":
		return nil, errUnsupported(member)
	}
	return nil, fmt.Errorf("unknown computer action %q", member)
}

// openAIAction is one entry of a computer_call's actions[].
type openAIAction struct {
	Type    string                   `json:"type"`
	Button  string                   `json:"button"`
	X       *float64                 `json:"x"`
	Y       *float64                 `json:"y"`
	Keys    []string                 `json:"keys"`
	Path    []struct{ X, Y float64 } `json:"path"`
	ScrollX float64                  `json:"scroll_x"`
	ScrollY float64                  `json:"scroll_y"`
	Text    string                   `json:"text"`
	Ms      float64                  `json:"ms"`
}

func translateOpenAI(raw json.RawMessage, app string, g geometry) ([]nstep, error) {
	var as []openAIAction
	if err := json.Unmarshal(raw, &as); err != nil {
		return nil, fmt.Errorf("actions: %v", err)
	}
	if len(as) == 0 {
		return nil, errors.New("actions is empty")
	}
	var out []nstep
	for i, a := range as {
		step := func() ([]nstep, error) {
			point := func() (float64, float64, error) {
				if a.X == nil || a.Y == nil {
					return 0, 0, fmt.Errorf("%s needs x and y", a.Type)
				}
				return g.pixels(*a.X, *a.Y)
			}
			switch a.Type {
			case "screenshot":
				return []nstep{{shot: true}}, nil
			case "click", "double_click":
				var m string
				if len(a.Keys) > 0 {
					var err error
					if m, err = MapMods(strings.Join(a.Keys, "+")); err != nil {
						return nil, err
					}
				}
				kind := computer.Click
				switch {
				case a.Type == "double_click":
					kind = computer.DoubleClick
				case a.Button == "right":
					kind = computer.RightClick
				case a.Button == "wheel" || a.Button == "middle":
					kind = computer.MiddleClick
				case a.Button != "" && a.Button != "left":
					return nil, errUnsupported("the " + a.Button + " button")
				}
				x, y, err := point()
				if err != nil {
					return nil, err
				}
				return []nstep{act(computer.Action{Kind: kind, App: app, X: x, Y: y, Mods: m})}, nil
			case "drag":
				if len(a.Path) < 2 {
					return nil, errors.New("drag needs a path of at least two points")
				}
				p, q := a.Path[0], a.Path[len(a.Path)-1]
				x, y, err := g.pixels(p.X, p.Y)
				if err != nil {
					return nil, err
				}
				tx, ty, _ := g.pixels(q.X, q.Y)
				return []nstep{act(computer.Action{Kind: computer.Drag, App: app, X: x, Y: y, ToX: tx, ToY: ty})}, nil
			case "move":
				x, y, err := point()
				if err != nil {
					return nil, err
				}
				return []nstep{act(computer.Action{Kind: computer.MouseMove, App: app, X: x, Y: y})}, nil
			case "scroll":
				x, y, err := point()
				if err != nil {
					return nil, err
				}
				sa := computer.Action{Kind: computer.Scroll, App: app, X: x, Y: y,
					DX: lines(a.ScrollX / g.scale), DY: lines(a.ScrollY / g.scale)}
				if sa.DX == 0 && sa.DY == 0 {
					return nil, errors.New("scroll needs scroll_x or scroll_y")
				}
				return []nstep{act(sa)}, nil
			case "keypress":
				keys, err := MapKeys(strings.Join(a.Keys, "+"))
				if err != nil {
					return nil, err
				}
				return []nstep{act(computer.Action{Kind: computer.Key, App: app, Keys: keys})}, nil
			case "type":
				if a.Text == "" {
					return nil, errors.New("type needs text")
				}
				return []nstep{act(computer.Action{Kind: computer.Type, App: app, Text: a.Text})}, nil
			case "wait":
				return []nstep{{wait: seconds(a.Ms/1000, 1)}}, nil
			}
			return nil, fmt.Errorf("unknown computer action %q", a.Type)
		}
		ss, err := step()
		if err != nil {
			return nil, fmt.Errorf("action %d: %w", i+1, err)
		}
		out = append(out, ss...)
	}
	return out, nil
}

func translateGemini(fn string, raw json.RawMessage, app string, g geometry) ([]nstep, error) {
	var in struct {
		X          *float64 `json:"x"`
		Y          *float64 `json:"y"`
		StartX     *float64 `json:"start_x"`
		StartY     *float64 `json:"start_y"`
		EndX       *float64 `json:"end_x"`
		EndY       *float64 `json:"end_y"`
		Text       string   `json:"text"`
		PressEnter bool     `json:"press_enter"`
		Seconds    float64  `json:"seconds"`
		Key        string   `json:"key"`
		Keys       []string `json:"keys"`
		Direction  string   `json:"direction"`
		Magnitude  float64  `json:"magnitude_in_pixels"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, fmt.Errorf("%s: %v", fn, err)
		}
	}
	point := func(x, y *float64) (float64, float64, error) {
		if x == nil || y == nil {
			return 0, 0, fmt.Errorf("%s needs x and y", fn)
		}
		return g.normalized(*x, *y)
	}
	click := map[string]computer.ActionKind{"click": computer.Click, "right_click": computer.RightClick, "double_click": computer.DoubleClick}
	switch fn {
	case "take_screenshot":
		return []nstep{{shot: true}}, nil
	case "click", "right_click", "double_click":
		x, y, err := point(in.X, in.Y)
		if err != nil {
			return nil, err
		}
		return []nstep{act(computer.Action{Kind: click[fn], App: app, X: x, Y: y})}, nil
	case "move", "hover_at":
		x, y, err := point(in.X, in.Y)
		if err != nil {
			return nil, err
		}
		return []nstep{act(computer.Action{Kind: computer.MouseMove, App: app, X: x, Y: y})}, nil
	case "drag_and_drop":
		x, y, err := point(in.StartX, in.StartY)
		if err != nil {
			return nil, err
		}
		tx, ty, err := point(in.EndX, in.EndY)
		if err != nil {
			return nil, err
		}
		return []nstep{act(computer.Action{Kind: computer.Drag, App: app, X: x, Y: y, ToX: tx, ToY: ty})}, nil
	case "scroll":
		x, y, err := point(in.X, in.Y)
		if err != nil {
			return nil, err
		}
		m := in.Magnitude
		if m <= 0 {
			m = 300
		}
		a := computer.Action{Kind: computer.Scroll, App: app, X: x, Y: y}
		if err := scrollBy(&a, in.Direction, max(lines(m), 1)); err != nil {
			return nil, err
		}
		return []nstep{act(a)}, nil
	case "type":
		if in.Text == "" {
			return nil, errors.New("type needs text")
		}
		out := []nstep{act(computer.Action{Kind: computer.Type, App: app, Text: in.Text})}
		if in.PressEnter {
			out = append(out, act(computer.Action{Kind: computer.Key, App: app, Keys: "return"}))
		}
		return out, nil
	case "press_key":
		keys, err := MapKeys(in.Key)
		if err != nil {
			return nil, err
		}
		return []nstep{act(computer.Action{Kind: computer.Key, App: app, Keys: keys})}, nil
	case "hotkey":
		keys, err := MapKeys(strings.Join(in.Keys, "+"))
		if err != nil {
			return nil, err
		}
		return []nstep{act(computer.Action{Kind: computer.Key, App: app, Keys: keys})}, nil
	case "wait":
		return []nstep{{wait: seconds(in.Seconds, 1)}}, nil
	}
	if slices.Contains(llm.GeminiComputerExcluded, fn) {
		return nil, errUnsupported(fn)
	}
	return nil, fmt.Errorf("unknown computer action %q", fn)
}

// scrollBy sets a scroll of n lines in direction.
func scrollBy(a *computer.Action, direction string, n int) error {
	switch strings.ToLower(direction) {
	case "down":
		a.DY = n
	case "up":
		a.DY = -n
	case "right":
		a.DX = n
	case "left":
		a.DX = -n
	default:
		return fmt.Errorf("scroll direction must be up, down, left or right, got %q", direction)
	}
	return nil
}

// lines turns a distance in points into scroll lines (about 40 points a
// line; any non-zero distance scrolls at least one).
func lines(points float64) int {
	if points == 0 || math.IsNaN(points) || math.IsInf(points, 0) {
		return 0
	}
	n := int(math.Round(math.Abs(points) / 40))
	if n == 0 {
		n = 1
	}
	if points < 0 {
		return -n
	}
	return n
}

// seconds is a wait, defaulting to def and capped at 30s.
func seconds(v, def float64) time.Duration {
	if v <= 0 {
		v = def
	}
	return time.Duration(min(v, 30) * float64(time.Second))
}

// keyNames maps the vendors' key names (xdotool for Claude, upper case
// for OpenAI, words for Gemini) to the provider's ("cmd+shift+n").
var keyNames = map[string]string{
	"super": "cmd", "meta": "cmd", "cmd": "cmd", "command": "cmd", "win": "cmd", "windows": "cmd", "os": "cmd",
	"ctrl": "ctrl", "control": "ctrl", "control_l": "ctrl", "control_r": "ctrl",
	"alt": "alt", "option": "alt", "alt_l": "alt", "alt_r": "alt",
	"shift": "shift", "shift_l": "shift", "shift_r": "shift",
	"return": "return", "enter": "return", "kp_enter": "enter",
	"backspace": "backspace", "delete": "forwarddelete", "del": "forwarddelete",
	"escape": "escape", "esc": "escape", "tab": "tab", "space": "space",
	"page_down": "pagedown", "pagedown": "pagedown", "next": "pagedown",
	"page_up": "pageup", "pageup": "pageup", "prior": "pageup",
	"home": "home", "end": "end",
	"up": "up", "down": "down", "left": "left", "right": "right",
	"arrowup": "up", "arrowdown": "down", "arrowleft": "left", "arrowright": "right",
}

// MapKeys maps a vendor chord ("ctrl+s", "Return", "CTRL+A", "Control+c",
// "super+shift+z") to the computer's key syntax. Several chords separated
// by spaces are kept in order.
func MapKeys(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("key needs a key, e.g. Return or ctrl+s")
	}
	var chords []string
	for _, chord := range strings.Fields(s) {
		parts := strings.Split(chord, "+")
		if strings.HasSuffix(chord, "++") {
			parts = append(strings.Split(strings.TrimSuffix(chord, "++"), "+"), "+")
		}
		for i, p := range parts {
			if p == "" {
				return "", fmt.Errorf("bad key %q", chord)
			}
			l := strings.ToLower(p)
			switch {
			case keyNames[l] != "":
				parts[i] = keyNames[l]
			case len(l) > 1 && l[0] == 'f' && strings.Trim(l[1:], "0123456789") == "":
				parts[i] = l // f1 … f20
			case len([]rune(p)) == 1:
				parts[i] = l // a letter: the key, not shift+letter
			default:
				parts[i] = l
			}
		}
		chords = append(chords, strings.Join(parts, "+"))
	}
	return strings.Join(chords, " "), nil
}

// ---------------------------------------------------------------- tool

func (s *Service) nativeTool() *tools.Tool {
	return &tools.Tool{Name: ToolNative, Native: true, Category: tools.CategoryWrite, Unconfined: true,
		Description: "The model provider's own computer-use tool, on the app last opened or observed with computer_open / computer_observe.",
		Params: tools.Schema(map[string]any{"vendor": tools.Prop("string", "the provider"),
			"action": tools.Prop("string", "the native action")}, "vendor"),
		Classify: s.classifyNative, Execute: s.runNative}
}

// classifyNative is classify for native calls: the app's category, or
// publish / delete / payment when a step hits such a control. Clicks are
// hit-tested against a fresh outline of the window when one can be had.
func (s *Service) classifyNative(call tools.Call) (string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	app, ok := s.targetApp(ctx, call.Agent)
	if !ok {
		return "", "电脑操作（未选择应用）"
	}
	sum := "操作 " + app.Name
	c, err := decodeNative(call)
	if err != nil {
		return Category(app.ID), sum
	}
	steps, err := translate(c, app.ID, s.geometryOf(call.Agent, app.ID))
	if err != nil {
		return Category(app.ID), sum
	}
	obs := s.latest(call.Agent, app.ID)
	for _, st := range steps {
		if st.act != nil && st.act.Clicks() {
			// coordinates hit what is there now, not what was observed
			if o, err := s.comp.Observe(ctx, app.ID, s.maxNodes); err == nil {
				obs = &o
			}
			break
		}
	}
	msg := isMessaging(app.ID, app.Name)
	best, what := "", ""
	for _, st := range steps {
		if st.act == nil {
			continue
		}
		if cc, ok := consequence(*st.act, obs, msg); ok && rank(cc.Category) > rank(best) {
			best, what = cc.Category, describeCons(*st.act, cc)
		}
	}
	if len(c.Safety) > 0 {
		var ms []string
		for _, sc := range c.Safety {
			if sc.Message != "" {
				ms = append(ms, sc.Message)
			} else if sc.Code != "" {
				ms = append(ms, sc.Code)
			}
		}
		sum += "（模型服务商要求确认：" + strings.Join(ms, "；") + "）"
	}
	if best != "" {
		return best, fmt.Sprintf("在 %s %s", app.Name, what)
	}
	return Category(app.ID), sum
}

// runNative runs a native call: its steps in order, stopping at the first
// error; the later calls of a failed batch are skipped.
func (s *Service) runNative(ctx context.Context, call tools.Call) (tools.Result, error) {
	c, err := decodeNative(call)
	if err != nil {
		return tools.Errorf("%v", err), nil
	}
	if c.Batch != "" {
		s.mu.Lock()
		failed := s.nativeOf(call.Agent).failed == c.Batch
		s.mu.Unlock()
		if failed {
			return tools.Errorf("%s", SkippedMessage), nil
		}
	}
	app, ok := s.targetApp(ctx, call.Agent)
	if !ok {
		return tools.Errorf("%s", NoTargetMessage), nil
	}
	ctx, done := s.track(ctx, call.Agent)
	defer done()
	defer s.clearLive(call.Agent)
	// OpenAI and Gemini answer every call with a screenshot
	always := c.Vendor == llm.ComputerOpenAI || c.Vendor == llm.ComputerGemini
	res := s.nativeSteps(ctx, call, c, app)
	if res.IsError && c.Batch != "" {
		s.mu.Lock()
		s.nativeOf(call.Agent).failed = c.Batch
		s.mu.Unlock()
	}
	if always && len(res.Attachments) == 0 && !errors.Is(ctx.Err(), context.Canceled) && !s.Stopped(call.Agent) {
		if at, err := s.nativeShot(ctx, call, app); err == nil {
			res.Attachments = []llm.Attachment{at}
		}
	}
	if len(c.Safety) > 0 {
		// reached only after the person approved the card (middleware)
		res.Content = llm.SafetyAcknowledged + "\n" + res.Content
	}
	return res, nil
}

func (s *Service) nativeSteps(ctx context.Context, call tools.Call, c llm.ComputerCall, app computer.App) tools.Result {
	steps, err := translate(c, app.ID, s.geometryOf(call.Agent, app.ID))
	if err != nil {
		return tools.Errorf("%v", err)
	}
	var lines []string
	var shot *llm.Attachment
	acts := 0
	for _, st := range steps {
		if st.act != nil {
			acts++
		}
	}
	i := 0
	for n, st := range steps {
		fail := func(r tools.Result) tools.Result {
			if len(steps) > 1 {
				r.Content = strings.Join(append(lines, r.Content), "\n")
				if n+1 < len(steps) {
					r.Content += "\nStopped here; the remaining actions were not run."
				}
			}
			return r
		}
		if err := s.checkStop(ctx, call.Agent); err != nil {
			return fail(errResult(err))
		}
		switch {
		case st.shot:
			at, err := s.nativeShot(ctx, call, app)
			if err != nil {
				return fail(errResult(err))
			}
			shot = &at
			g := s.geometryOf(call.Agent, app.ID)
			lines = append(lines, fmt.Sprintf("Screenshot of %s's window (%.0f×%.0f points, saved as %s).", app.Name, g.winW, g.winH, at.Path))
			if st.note != "" {
				lines = append(lines, st.note)
			}
		case st.wait > 0:
			select {
			case <-ctx.Done():
				return fail(errResult(ctx.Err()))
			case <-time.After(st.wait):
			}
			lines = append(lines, fmt.Sprintf("Waited %s.", st.wait))
		case st.note != "":
			lines = append(lines, st.note)
		case st.act != nil:
			line, r := s.one(ctx, call, app, *st.act, i, acts, acts > 1)
			i++
			if r != nil {
				return fail(*r)
			}
			lines = append(lines, line)
		}
	}
	res := tools.Text(strings.Join(lines, "\n"))
	if res.Content == "" {
		res.Content = "OK"
	}
	if shot != nil {
		res.Attachments = []llm.Attachment{*shot}
	}
	return res
}

// nativeShot captures the target window, saves it and records its
// geometry for the next coordinates.
func (s *Service) nativeShot(ctx context.Context, call tools.Call, app computer.App) (llm.Attachment, error) {
	s.setLive(Live{Agent: call.Agent, App: app.ID, AppName: app.Name, Action: "screenshot"})
	shot, err := s.comp.Screenshot(ctx, app.ID, s.edge)
	if err != nil {
		s.log(call, ActionLog{App: app.ID, AppName: app.Name, Action: "screenshot", Error: err.Error()})
		return llm.Attachment{}, err
	}
	at, err := saveShot(call.Workspace, app, shot)
	if err != nil {
		s.log(call, ActionLog{App: app.ID, AppName: app.Name, Action: "screenshot", Error: err.Error()})
		return llm.Attachment{}, err
	}
	if shot.Scale > 0 {
		s.mu.Lock()
		if st := s.nativeOf(call.Agent); st.app == app.ID {
			st.shot = geometry{scale: shot.Scale, winW: float64(shot.Width) / shot.Scale, winH: float64(shot.Height) / shot.Scale}
		}
		s.mu.Unlock()
	}
	s.log(call, ActionLog{App: app.ID, AppName: app.Name, Action: "screenshot", OK: true, Screenshot: at.Path})
	return at, nil
}

// ---------------------------------------------------------------- request

// offerNative adds the provider's computer tool to requests of models
// that have one, while computer use is on and the agent may act.
func (s *Service) offerNative(ctx context.Context, x agentloop.RequestArgs, next kernel.Next[agentloop.RequestArgs, *llm.Request]) (*llm.Request, error) {
	req, err := next(ctx, x)
	if err != nil || req == nil || x.Agent == nil {
		return req, err
	}
	if !s.Enabled() || req.Wants(llm.NativeComputer) || llm.ComputerTool(req.Model, nil) == "" ||
		toolsbasic.EffectiveMode(x.Agent.Mode()) == agent.ModeReadOnly {
		return req, nil
	}
	if _, ok := s.reg.Get(x.Agent.ID(), ToolNative); !ok {
		return req, nil
	}
	// adapters declare it only where they can (others ignore it)
	req.NativeTools = append(slices.Clone(req.NativeTools), llm.NativeComputer)
	return req, nil
}

// heldKeys are the keys a click, drag or scroll may hold.
var heldKeys = map[string]bool{"cmd": true, "shift": true, "alt": true, "ctrl": true, "fn": true}

// MapMods maps a vendor's held keys ("SHIFT", "Meta", "shift+ctrl") to
// ours ("shift", "cmd+shift"), refusing anything but modifier keys.
func MapMods(s string) (string, error) {
	m, err := MapKeys(strings.ReplaceAll(strings.TrimSpace(s), " ", "+"))
	if err != nil {
		return "", err
	}
	for _, k := range strings.Split(m, "+") {
		if !heldKeys[k] {
			return "", fmt.Errorf("%q is not a modifier key: only cmd, shift, alt, ctrl and fn can be held during a click", k)
		}
	}
	return m, nil
}
