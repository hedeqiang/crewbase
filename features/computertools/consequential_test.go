package computertools

import (
	"testing"

	"github.com/hedeqiang/crewbase/capabilities/computer"
	"github.com/hedeqiang/crewbase/core/tools"
)

func TestNameCategory(t *testing.T) {
	for name, want := range map[string]string{
		"发送": tools.CategoryPublish, "Send": tools.CategoryPublish, "Send Now": tools.CategoryPublish, "提交订单": tools.CategoryPublish,
		"Submit": tools.CategoryPublish, "发布": tools.CategoryPublish,
		"删除": tools.CategoryDelete, "Delete": tools.CategoryDelete, "Move to Trash": tools.CategoryDelete, "移到废纸篓": tools.CategoryDelete,
		"支付": tools.CategoryPayment, "确认付款": tools.CategoryPayment, "Buy Now": tools.CategoryPayment, "Pay": tools.CategoryPayment,
		"购买": tools.CategoryPayment, "Purchase": tools.CategoryPayment,
		// not consequential
		"Sender": "", "Payment settings": "", "新建备忘录": "", "Sent": "", "Posture": "", "": "",
	} {
		if got := nameCategory(name); got != want {
			t.Errorf("%q: got %q, want %q", name, got, want)
		}
	}
}

func TestConsequence(t *testing.T) {
	obs := &computer.Observation{Root: computer.Element{Role: "window", Children: []computer.Element{
		{ID: 1, Role: "button", Name: "Send", Frame: computer.Rect{X: 0, Y: 0, W: 10, H: 10}},
		{ID: 2, Role: "row", Name: "Deleted Items"},
		{ID: 3, Role: "text area", Name: "Message", Focused: true},
	}}}
	noFocus := &computer.Observation{Root: computer.Element{Children: []computer.Element{{ID: 1, Role: "button", Name: "x", Focused: true}}}}
	cases := []struct {
		name string
		a    computer.Action
		obs  *computer.Observation
		msg  bool
		want string
	}{
		{"press send", computer.Action{Kind: computer.Press, Element: 1}, obs, false, tools.CategoryPublish},
		{"click on send", computer.Action{Kind: computer.Click, X: 5, Y: 5}, obs, false, tools.CategoryPublish},
		{"click elsewhere", computer.Action{Kind: computer.Click, X: 50, Y: 50}, obs, false, ""},
		{"row named deleted", computer.Action{Kind: computer.Press, Element: 2}, obs, false, ""},
		{"menu delete", computer.Action{Kind: computer.Menu, Path: []string{"Edit", "Delete"}}, obs, false, tools.CategoryDelete},
		{"return in chat", computer.Action{Kind: computer.Key, Keys: "return"}, obs, true, tools.CategoryPublish},
		{"cmd+enter in mail", computer.Action{Kind: computer.Key, Keys: "cmd+enter"}, obs, true, tools.CategoryPublish},
		{"return unobserved", computer.Action{Kind: computer.Key, Keys: "return"}, nil, true, tools.CategoryPublish},
		{"return on a button", computer.Action{Kind: computer.Key, Keys: "return"}, noFocus, true, ""},
		{"return elsewhere", computer.Action{Kind: computer.Key, Keys: "return"}, obs, false, ""},
		{"newline typed in chat", computer.Action{Kind: computer.Type, Text: "ok\n"}, obs, true, tools.CategoryPublish},
		{"text typed in chat", computer.Action{Kind: computer.Type, Text: "ok"}, obs, true, ""},
	}
	for _, c := range cases {
		got, _ := consequence(c.a, c.obs, c.msg)
		if got.Category != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got.Category, c.want)
		}
	}
}

func TestKnownApps(t *testing.T) {
	for _, c := range []struct{ id, name string }{
		{"com.apple.keychainaccess", ""}, {"", "Bitwarden"}, {"", "Dashlane"}, {"", "LastPass"}, {"", "Enpass"}, {"", "Keeper"},
		{"com.googlecode.iterm2", ""}, {"", "Warp"}, {"", "Ghostty"}, {"", "kitty"}, {"", "WezTerm"}, {"", "Alacritty"},
		{"com.apple.systempreferences", ""}, {"app.crewbase.desktop", ""}, {"", "富途牛牛"}, {"", "Ledger Live"},
	} {
		if _, ok := defaultBlocked(c.id, c.name); !ok {
			t.Errorf("%s %s not blocked by default", c.id, c.name)
		}
	}
	if _, ok := defaultBlocked("com.apple.Notes", "备忘录"); ok {
		t.Error("Notes blocked")
	}
	for _, n := range []string{"Safari", "Google Chrome", "Arc", "Microsoft Edge", "Firefox"} {
		if !IsBrowser("", n) {
			t.Errorf("%s is a browser", n)
		}
	}
}
