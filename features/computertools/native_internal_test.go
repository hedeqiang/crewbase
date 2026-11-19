package computertools

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/computer"
	"github.com/hedeqiang/crewbase/core/llm"
)

// Retina window 800×600 points, screenshot downscaled to 1280×960 pixels
// (1.6 pixels per point).
var retina = geometry{scale: 1.6, winW: 800, winH: 600}

func steps(t *testing.T, c llm.ComputerCall, g geometry) []nstep {
	t.Helper()
	out, err := translate(c, "com.app", g)
	if err != nil {
		t.Fatalf("%+v: %v", c, err)
	}
	return out
}

func one(t *testing.T, c llm.ComputerCall, g geometry) computer.Action {
	t.Helper()
	ss := steps(t, c, g)
	if len(ss) != 1 || ss[0].act == nil {
		t.Fatalf("want one action, got %+v", ss)
	}
	return *ss[0].act
}

func anth(action, input string) llm.ComputerCall {
	return llm.ComputerCall{Vendor: llm.ComputerAnthropic, Action: action, Input: json.RawMessage(input)}
}

func gem(fn, args string) llm.ComputerCall {
	return llm.ComputerCall{Vendor: llm.ComputerGemini, Action: fn, Input: json.RawMessage(args)}
}

func TestTranslateAnthropic(t *testing.T) {
	for _, tc := range []struct {
		action, input string
		want          computer.Action
	}{
		{"left_click", `{"coordinate":[640,480]}`, computer.Action{Kind: computer.Click, X: 400, Y: 300}},
		{"right_click", `{"coordinate":[16,32]}`, computer.Action{Kind: computer.RightClick, X: 10, Y: 20}},
		{"double_click", `{"coordinate":[160,160]}`, computer.Action{Kind: computer.DoubleClick, X: 100, Y: 100}},
		{"left_click_drag", `{"start_coordinate":[160,0],"coordinate":[320,160]}`, computer.Action{Kind: computer.Drag, X: 100, Y: 0, ToX: 200, ToY: 100}},
		{"scroll", `{"coordinate":[160,160],"scroll_direction":"up","scroll_amount":5}`, computer.Action{Kind: computer.Scroll, X: 100, Y: 100, DY: -5}},
		{"scroll", `{"coordinate":[160,160],"scroll_direction":"right"}`, computer.Action{Kind: computer.Scroll, X: 100, Y: 100, DX: 3}},
		{"type", `{"text":"你好\n"}`, computer.Action{Kind: computer.Type, Text: "你好\n"}},
		{"key", `{"text":"Return"}`, computer.Action{Kind: computer.Key, Keys: "return"}},
		{"key", `{"text":"super+shift+Z"}`, computer.Action{Kind: computer.Key, Keys: "cmd+shift+z"}},
	} {
		got := one(t, anth(tc.action, tc.input), retina)
		tc.want.App = "com.app"
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s %s: %+v, want %+v", tc.action, tc.input, got, tc.want)
		}
	}
	// key repeat
	if ss := steps(t, anth("key", `{"text":"Tab","repeat":4}`), retina); len(ss) != 4 || ss[3].act.Keys != "tab" {
		t.Fatalf("repeat: %+v", ss)
	}
	if ss := steps(t, anth("screenshot", `{}`), geometry{}); len(ss) != 1 || !ss[0].shot {
		t.Fatalf("screenshot: %+v", ss)
	}
	if ss := steps(t, anth("wait", `{"duration":2}`), retina); ss[0].wait != 2*time.Second {
		t.Fatalf("wait: %+v", ss)
	}
	for _, bad := range []struct{ action, input, want string }{
		{"left_click", `{"coordinate":[1,2]}`, "screenshot first"}, // no screenshot yet
		{"left_click", `{}`, "needs coordinate"},
		{"left_click", `{"coordinate":[1,2],"text":"a"}`, "not a modifier"},
		{"hold_key", `{"text":"shift","duration":1}`, "not available"},
		{"scroll", `{"coordinate":[1,2],"scroll_direction":"sideways"}`, "direction"},
		{"teleport", `{}`, "unknown"},
	} {
		g := retina
		if bad.want == "screenshot first" {
			g = geometry{winW: 800, winH: 600}
		}
		if _, err := translate(anth(bad.action, bad.input), "com.app", g); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s %s: %v, want %q", bad.action, bad.input, err, bad.want)
		}
	}
}

func TestTranslateOpenAI(t *testing.T) {
	c := llm.ComputerCall{Vendor: llm.ComputerOpenAI, Actions: json.RawMessage(`[
		{"type":"click","button":"left","x":640,"y":480},
		{"type":"click","button":"right","x":16,"y":16},
		{"type":"double_click","x":160,"y":160},
		{"type":"drag","path":[{"x":0,"y":0},{"x":80,"y":80},{"x":160,"y":320}]},
		{"type":"scroll","x":160,"y":160,"scroll_x":0,"scroll_y":320},
		{"type":"keypress","keys":["CTRL","A"]},
		{"type":"type","text":"hi"},
		{"type":"move","x":1,"y":1},
		{"type":"wait"},
		{"type":"screenshot"}
	]`)}
	ss := steps(t, c, retina)
	want := []computer.Action{
		{Kind: computer.Click, X: 400, Y: 300},
		{Kind: computer.RightClick, X: 10, Y: 10},
		{Kind: computer.DoubleClick, X: 100, Y: 100},
		{Kind: computer.Drag, X: 0, Y: 0, ToX: 100, ToY: 200},
		{Kind: computer.Scroll, X: 100, Y: 100, DY: 5}, // 320 px = 200 points = 5 lines
		{Kind: computer.Key, Keys: "ctrl+a"},
		{Kind: computer.Type, Text: "hi"},
	}
	if len(ss) != 10 {
		t.Fatalf("%d steps", len(ss))
	}
	for i, w := range want {
		w.App = "com.app"
		if ss[i].act == nil || !reflect.DeepEqual(*ss[i].act, w) {
			t.Errorf("step %d: %+v, want %+v", i, ss[i].act, w)
		}
	}
	if ss[7].act == nil || ss[7].act.Kind != computer.MouseMove || ss[8].wait != time.Second || !ss[9].shot {
		t.Fatalf("move / wait / screenshot: %+v", ss[7:])
	}
	// one bad action fails the whole call before anything runs
	bad := llm.ComputerCall{Vendor: llm.ComputerOpenAI, Actions: json.RawMessage(`[{"type":"type","text":"x"},{"type":"click","button":"back","x":1,"y":1}]`)}
	if _, err := translate(bad, "com.app", retina); err == nil || !strings.Contains(err.Error(), "action 2") {
		t.Fatalf("bad batch: %v", err)
	}
}

func TestTranslateGemini(t *testing.T) {
	g := geometry{winW: 800, winH: 600} // no screenshot needed: 0–999 of the window
	for _, tc := range []struct {
		fn, args string
		want     computer.Action
	}{
		{"click", `{"x":500,"y":500,"intent":"open"}`, computer.Action{Kind: computer.Click, X: 400, Y: 300}},
		{"click", `{"x":0,"y":999}`, computer.Action{Kind: computer.Click, X: 0, Y: 599.4}},
		{"right_click", `{"x":250,"y":100}`, computer.Action{Kind: computer.RightClick, X: 200, Y: 60}},
		{"double_click", `{"x":125,"y":500}`, computer.Action{Kind: computer.DoubleClick, X: 100, Y: 300}},
		{"drag_and_drop", `{"start_x":0,"start_y":0,"end_x":500,"end_y":1000}`, computer.Action{Kind: computer.Drag, ToX: 400, ToY: 600}},
		{"scroll", `{"x":500,"y":500,"direction":"down"}`, computer.Action{Kind: computer.Scroll, X: 400, Y: 300, DY: 8}},
		{"press_key", `{"key":"Enter"}`, computer.Action{Kind: computer.Key, Keys: "return"}},
		{"hotkey", `{"keys":["Control","c"]}`, computer.Action{Kind: computer.Key, Keys: "ctrl+c"}},
	} {
		got := one(t, gem(tc.fn, tc.args), g)
		tc.want.App = "com.app"
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s %s: %+v, want %+v", tc.fn, tc.args, got, tc.want)
		}
	}
	ss := steps(t, gem("type", `{"text":"你好","press_enter":true}`), g)
	if len(ss) != 2 || ss[0].act.Text != "你好" || ss[1].act.Keys != "return" {
		t.Fatalf("type + enter: %+v", ss)
	}
	// retina screenshots do not change Gemini's maths
	if a := one(t, gem("click", `{"x":500,"y":500}`), retina); a.X != 400 || a.Y != 300 {
		t.Fatalf("retina: %+v", a)
	}
	for _, bad := range []struct{ fn, args, want string }{
		{"click", `{"x":1200,"y":5}`, "outside"},
		{"triple_click", `{"x":1,"y":1}`, "not available"},
		{"click", `{"x":1}`, "needs x and y"},
	} {
		if _, err := translate(gem(bad.fn, bad.args), "com.app", g); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: %v, want %q", bad.fn, err, bad.want)
		}
	}
	if _, err := translate(gem("click", `{"x":1,"y":1}`), "com.app", geometry{}); err == nil {
		t.Error("window size unknown but click accepted")
	}
}

func TestMapKeys(t *testing.T) {
	for in, want := range map[string]string{
		"Return": "return", "KP_Enter": "enter", "BackSpace": "backspace", "Delete": "forwarddelete",
		"ctrl+s": "ctrl+s", "alt+Tab": "alt+tab", "super+c": "cmd+c", "cmd+shift+n": "cmd+shift+n",
		"Page_Down": "pagedown", "Escape": "escape", "CTRL+ALT+DELETE": "ctrl+alt+forwarddelete",
		"F5": "f5", "A": "a", "ctrl+a ctrl+e": "ctrl+a ctrl+e", "cmd++": "cmd++", "ArrowUp": "up",
	} {
		got, err := MapKeys(in)
		if err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "ctrl+"} {
		if _, err := MapKeys(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// The actions the native tools used to refuse.
func TestTranslateMoreActions(t *testing.T) {
	retina := geometry{scale: 2, winW: 800, winH: 600}
	anth := func(action, input string) llm.ComputerCall {
		return llm.ComputerCall{Vendor: llm.ComputerAnthropic, Action: action, Input: json.RawMessage(input)}
	}
	for _, c := range []struct {
		action, input string
		want          computer.Action
	}{
		{"triple_click", `{"coordinate":[20,40]}`, computer.Action{Kind: computer.TripleClick, X: 10, Y: 20}},
		{"middle_click", `{"coordinate":[20,40]}`, computer.Action{Kind: computer.MiddleClick, X: 10, Y: 20}},
		{"mouse_move", `{"coordinate":[20,40]}`, computer.Action{Kind: computer.MouseMove, X: 10, Y: 20}},
		{"left_click", `{"coordinate":[20,40],"text":"cmd"}`, computer.Action{Kind: computer.Click, X: 10, Y: 20, Mods: "cmd"}},
		{"left_click_drag", `{"start_coordinate":[0,0],"coordinate":[20,40],"text":"shift"}`, computer.Action{Kind: computer.Drag, ToX: 10, ToY: 20, Mods: "shift"}},
		{"scroll", `{"coordinate":[20,40],"scroll_direction":"down","scroll_amount":2,"text":"ctrl"}`, computer.Action{Kind: computer.Scroll, X: 10, Y: 20, DY: 2, Mods: "ctrl"}},
	} {
		ss, err := translate(anth(c.action, c.input), "com.app", retina)
		if err != nil || len(ss) != 1 || ss[0].act == nil {
			t.Fatalf("%s: %v %+v", c.action, err, ss)
		}
		c.want.App = "com.app"
		if !reflect.DeepEqual(*ss[0].act, c.want) {
			t.Errorf("%s: %+v, want %+v", c.action, *ss[0].act, c.want)
		}
	}
	if ss, err := translate(anth("zoom", `{"region":[0,0,10,10]}`), "com.app", retina); err != nil || len(ss) != 1 || !ss[0].shot || ss[0].note == "" {
		t.Fatalf("zoom: %v %+v", err, ss)
	}
	oa := llm.ComputerCall{Vendor: llm.ComputerOpenAI, Actions: json.RawMessage(`[{"type":"click","button":"wheel","x":20,"y":40},{"type":"click","button":"left","keys":["SHIFT"],"x":20,"y":40}]`)}
	ss, err := translate(oa, "com.app", retina)
	if err != nil || ss[0].act.Kind != computer.MiddleClick || ss[1].act.Mods != "shift" {
		t.Fatalf("openai: %v %+v %+v", err, ss[0].act, ss[1].act)
	}
}
