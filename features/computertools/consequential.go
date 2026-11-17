package computertools

import (
	"regexp"
	"strings"

	"github.com/hedeqiang/crewbase/capabilities/computer"
	"github.com/hedeqiang/crewbase/core/tools"
)

// Consequential actions are confirmed by the person every time (never
// remembered). The detection is deliberately simple: the name of the
// element pressed or clicked (or the last menu item chosen) contains a word
// for sending, deleting or paying; or return is pressed in a text field of
// a mail or chat app; or text with a line break is typed there. The
// categories reuse the rules engine's publish / delete / payment, so the
// person's existing rules apply too (payment is always blocked).

var (
	// English words match whole words ("Send", "Send Now", not "Sender");
	// Chinese ones anywhere in the name.
	payWords     = words(`pay|pay now|purchase|buy|buy now|checkout|place order`, `支付|付款|购买|确认付款|立即购买|结账|下单|结算`)
	deleteWords  = words(`delete|delete forever|erase|move to trash|empty trash`, `删除|移到废纸篓|清空废纸篓|抹掉`)
	// social and chat apps label posting differently: X 发帖 / Post / Tweet,
	// Weibo 发微博, 小红书 发布笔记, WeChat 发送, Slack Send, LinkedIn Share…
	publishWords = words(`send|send now|submit|post|post all|publish|reply|reply all|tweet|repost|retweet|share|comment|upload|go live`,
		`发送|提交|发布|发表|发帖|发推|转发|转推|回复|评论|发微博|分享|上传|发出|投递|确认发送|立即发送`)
)

type wordSet struct {
	en *regexp.Regexp
	zh []string
}

func words(en, zh string) wordSet {
	return wordSet{en: regexp.MustCompile(`(?i)\b(` + en + `)\b`), zh: strings.Split(zh, "|")}
}

func (w wordSet) in(s string) bool {
	if s == "" {
		return false
	}
	if w.en.MatchString(s) {
		return true
	}
	for _, z := range w.zh {
		if strings.Contains(s, z) {
			return true
		}
	}
	return false
}

// actionable roles: what a press or click on triggers something. Rows,
// cells, text and lists are left out ("最近删除" is a folder, not a
// button), unknown roles are kept.
func actionable(role string) bool {
	r := strings.ToLower(role)
	for _, no := range []string{"row", "cell", "text", "list", "outline", "table", "group", "image", "heading", "tab", "scroll", "window"} {
		if strings.Contains(r, no) {
			return false
		}
	}
	return true
}

// nameCategory classifies a control by its name.
func nameCategory(name string) string {
	switch {
	case payWords.in(name):
		return tools.CategoryPayment
	case deleteWords.in(name):
		return tools.CategoryDelete
	case publishWords.in(name):
		return tools.CategoryPublish
	}
	return ""
}

// textInput reports whether a role takes typed text.
func textInput(role string) bool {
	r := strings.ToLower(role)
	return strings.Contains(r, "text") || strings.Contains(r, "search") || strings.Contains(r, "combo")
}

// sendKey reports whether keys is return / enter with any modifiers.
func sendKey(keys string) bool {
	parts := strings.Split(strings.ToLower(strings.ReplaceAll(keys, " ", "")), "+")
	last := parts[len(parts)-1]
	return last == "return" || last == "enter" || last == "kp_enter"
}

// Consequence is why an action needs the person's confirmation.
type Consequence struct {
	Category string // publish | delete | payment
	What     string // the control or key, for the card ("发送", "return")
}

// consequence classifies one action against the app's latest outline (nil
// when not observed yet). messaging: return in a text field sends there.
func consequence(a computer.Action, obs *computer.Observation, messaging bool) (Consequence, bool) {
	var el *computer.Element
	if obs != nil {
		switch {
		case a.Element > 0:
			el = findElement(&obs.Root, a.Element)
		case a.Kind == computer.Click || a.Kind == computer.DoubleClick:
			el = hitTest(&obs.Root, a.X, a.Y)
		}
	}
	switch a.Kind {
	case computer.Press, computer.Click, computer.DoubleClick:
		if el != nil && actionable(el.Role) {
			if c := nameCategory(el.Name); c != "" {
				return Consequence{c, el.Name}, true
			}
		}
	case computer.Menu:
		if n := len(a.Path); n > 0 {
			if c := nameCategory(a.Path[n-1]); c != "" {
				return Consequence{c, a.Path[n-1]}, true
			}
		}
	case computer.Key:
		if messaging && sendKey(a.Keys) {
			// unknown focus counts as a text field: better one card too many
			if f := focused(obs); f == nil || textInput(f.Role) {
				return Consequence{tools.CategoryPublish, a.Keys}, true
			}
		}
	case computer.Type:
		if messaging && strings.ContainsAny(a.Text, "\r\n") {
			return Consequence{tools.CategoryPublish, "return"}, true
		}
	}
	return Consequence{}, false
}

// rank orders categories by gravity, for a batch.
func rank(cat string) int {
	switch cat {
	case tools.CategoryPayment:
		return 3
	case tools.CategoryDelete:
		return 2
	case tools.CategoryPublish:
		return 1
	}
	return 0
}

func findElement(e *computer.Element, id int) *computer.Element {
	if e == nil {
		return nil
	}
	if e.ID == id {
		return e
	}
	for i := range e.Children {
		if f := findElement(&e.Children[i], id); f != nil {
			return f
		}
	}
	return nil
}

// hitTest finds the deepest numbered element whose frame holds (x, y).
func hitTest(e *computer.Element, x, y float64) *computer.Element {
	var best *computer.Element
	var walk func(e *computer.Element)
	walk = func(e *computer.Element) {
		f := e.Frame
		inside := f.W > 0 && f.H > 0 && x >= f.X && x < f.X+f.W && y >= f.Y && y < f.Y+f.H
		if inside && e.ID > 0 {
			best = e
		}
		for i := range e.Children {
			walk(&e.Children[i])
		}
	}
	walk(e)
	return best
}

func focused(obs *computer.Observation) *computer.Element {
	if obs == nil {
		return nil
	}
	var out *computer.Element
	var walk func(e *computer.Element)
	walk = func(e *computer.Element) {
		if e.Focused {
			out = e
		}
		for i := range e.Children {
			walk(&e.Children[i])
		}
	}
	walk(&obs.Root)
	return out
}
