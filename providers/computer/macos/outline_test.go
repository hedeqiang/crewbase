package macos

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/computer"
)

// notes builds a Notes-like app: window at (100, 50) 800×600.
type notesApp struct {
	f                                    *fake
	app, win, newBtn, search, list, row1 ref
	body, pass, bar, fileMenu, newItem   ref
}

func buildNotes() *notesApp {
	f := newFake()
	n := &notesApp{f: f}
	n.app = f.add(0, rawNode{Role: "AXApplication", Title: "Notes"})
	n.win = f.add(0, rawNode{Role: "AXWindow", Title: "购物清单", X: 100, Y: 50, W: 800, H: 600, HasFrame: true, Main: true})
	f.nodes[n.app].attrs["AXFocusedWindow"] = n.win
	grp := f.add(n.win, rawNode{Role: "AXGroup", X: 100, Y: 50, W: 800, H: 40, HasFrame: true})
	n.newBtn = f.add(grp, rawNode{Role: "AXButton", Description: "新建备忘录", X: 110, Y: 60, W: 30, H: 20, HasFrame: true, Enabled: true, HasEnabled: true, Actions: []string{"AXPress", "AXShowMenu"}})
	n.search = f.add(grp, rawNode{Role: "AXTextField", Subrole: "AXSearchField", Placeholder: "搜索", Value: "", HasValue: true, Settable: true, X: 600, Y: 60, W: 200, H: 22, HasFrame: true, Enabled: true, HasEnabled: true})
	scroll := f.add(n.win, rawNode{Role: "AXScrollArea"})
	n.list = f.add(scroll, rawNode{Role: "AXOutline", Description: "备忘录"})
	n.row1 = f.add(n.list, rawNode{Role: "AXRow", Selected: true, X: 100, Y: 100, W: 200, H: 30, HasFrame: true, Enabled: true, HasEnabled: true})
	f.add(n.row1, rawNode{Role: "AXStaticText", Value: "购物清单", HasValue: true})
	n.body = f.add(n.win, rawNode{Role: "AXTextArea", Description: "正文", Value: "牛奶\n鸡蛋", HasValue: true, Settable: true, X: 300, Y: 100, W: 600, H: 500, HasFrame: true, Enabled: true, HasEnabled: true})
	n.pass = f.add(n.win, rawNode{Role: "AXTextField", Subrole: "AXSecureTextField", Title: "密码", Value: "••••", HasValue: true, Enabled: true, HasEnabled: true})

	n.bar = f.add(0, rawNode{Role: "AXMenuBar"})
	f.nodes[n.app].attrs["AXMenuBar"] = n.bar
	apple := f.add(n.bar, rawNode{Role: "AXMenuBarItem", Title: "Apple"})
	am := f.add(apple, rawNode{Role: "AXMenu"})
	f.add(am, rawNode{Role: "AXMenuItem", Title: "About This Mac", Enabled: true, HasEnabled: true})
	n.fileMenu = f.add(n.bar, rawNode{Role: "AXMenuBarItem", Title: "文件"})
	fm := f.add(n.fileMenu, rawNode{Role: "AXMenu"})
	n.newItem = f.add(fm, rawNode{Role: "AXMenuItem", Title: "新建备忘录", CmdChar: "N", Enabled: true, HasEnabled: true})
	f.add(fm, rawNode{Role: "AXMenuItem"}) // separator
	f.add(fm, rawNode{Role: "AXMenuItem", Title: "Export as PDF…", Enabled: false, HasEnabled: true})

	f.apps = []computer.App{{ID: "com.apple.Notes", Name: "备忘录", PID: 42, Active: true}}
	f.appEl[42] = n.app
	return n
}

func TestRoleName(t *testing.T) {
	for _, c := range []struct{ role, sub, want string }{
		{"AXButton", "", "button"},
		{"AXTextField", "", "text field"},
		{"AXTextField", "AXSearchField", "search field"},
		{"AXTextField", "AXSecureTextField", "password field"},
		{"AXRadioButton", "AXTabButton", "tab"},
		{"AXStaticText", "", "text"},
		{"AXWindow", "AXDialog", "dialog"},
		{"AXFancyNewWidget", "", "fancy new widget"},
		{"", "", "unknown"},
	} {
		if got := roleName(c.role, c.sub); got != c.want {
			t.Errorf("roleName(%q, %q) = %q, want %q", c.role, c.sub, got, c.want)
		}
	}
}

func TestOutlineNumbering(t *testing.T) {
	n := buildNotes()
	f := n.f
	raw, _ := f.Read(n.win)
	origin := computer.Rect{X: raw.X, Y: raw.Y, W: raw.W, H: raw.H}
	root, elems, truncated := buildOutline(f, n.win, raw, origin, 0, time.Time{})
	if truncated {
		t.Fatal("truncated")
	}
	if root.Role != "window" || root.Name != "购物清单" || root.ID != 0 {
		t.Fatalf("root = %+v", root)
	}
	got := render(root, 0)
	want := strings.Join([]string{
		`window "购物清单"`,
		`  [1] button "新建备忘录" (10,10 30×20) press`,
		`  [2] search field "搜索" = "" (500,10 200×22)`,
		`  outline "备忘录"`,
		`    [3] row selected (0,50 200×30)`,
		`      text = "购物清单"`,
		`  [4] text area "正文" = "牛奶\n鸡蛋" (200,50 600×500)`,
		`  [5] password field "密码" secure`,
	}, "\n")
	if got != want {
		t.Fatalf("outline:\n%s\nwant:\n%s", got, want)
	}
	if len(elems) != 5 || elems[0].ref != n.newBtn || elems[4].ref != n.pass || !elems[4].secure {
		t.Fatalf("elems = %+v", elems)
	}
	// only the numbered elements stay retained
	if got := f.leaks(); got != 5 {
		t.Fatalf("retained %d refs, want 5", got)
	}
	for _, e := range elems {
		f.Release(e.ref)
	}
	f.checkNoLeaks(t)
}

func TestOutlineLimit(t *testing.T) {
	f := newFake()
	win := f.add(0, rawNode{Role: "AXWindow", HasFrame: true, W: 100, H: 100})
	for i := 0; i < 50; i++ {
		f.add(win, rawNode{Role: "AXButton", Title: "b", Actions: []string{"AXPress"}})
	}
	raw, _ := f.Read(win)
	root, elems, truncated := buildOutline(f, win, raw, computer.Rect{}, 10, time.Time{})
	if !truncated || len(elems) != 9 || len(root.Children) != 9 {
		t.Fatalf("truncated=%v elems=%d kids=%d", truncated, len(elems), len(root.Children))
	}
	for _, e := range elems {
		f.Release(e.ref)
	}
	f.checkNoLeaks(t)

	// deadline already passed: root only
	_, elems, truncated = buildOutline(f, win, raw, computer.Rect{}, 400, time.Now().Add(-time.Second))
	if !truncated || len(elems) != 0 {
		t.Fatalf("deadline: truncated=%v elems=%d", truncated, len(elems))
	}
	f.checkNoLeaks(t)
}

func TestVersionHash(t *testing.T) {
	a := computer.Element{Role: "window", Children: []computer.Element{{ID: 1, Role: "button", Name: "OK"}}}
	b := computer.Element{Role: "window", Children: []computer.Element{{ID: 1, Role: "button", Name: "OK"}}}
	if hashOutline("t", a, nil) != hashOutline("t", b, nil) {
		t.Fatal("same outline, different versions")
	}
	if hashOutline("t", a, nil) == 0 {
		t.Fatal("version 0")
	}
	b.Children[0].Value = "x"
	if hashOutline("t", a, nil) == hashOutline("t", b, nil) {
		t.Fatal("value change kept the version")
	}
	if hashOutline("t", a, nil) == hashOutline("u", a, nil) {
		t.Fatal("title change kept the version")
	}
	// nesting matters, not just the sequence
	c := computer.Element{Role: "window", Children: []computer.Element{{Role: "group", Children: []computer.Element{{ID: 1, Role: "button", Name: "OK"}}}}}
	d := computer.Element{Role: "window", Children: []computer.Element{{Role: "group"}, {ID: 1, Role: "button", Name: "OK"}}}
	if hashOutline("t", c, nil) == hashOutline("t", d, nil) {
		t.Fatal("structure change kept the version")
	}
}

func TestMenus(t *testing.T) {
	n := buildNotes()
	menus := buildMenus(n.f, n.bar, time.Time{})
	n.f.checkNoLeaks(t)
	if len(menus) != 2 || menus[1].Name != "文件" || len(menus[1].Children) != 2 {
		t.Fatalf("menus = %+v", menus)
	}
	it := menus[1].Children[0]
	if it.Name != "新建备忘录" || it.Value != "⌘N" || !it.Enabled {
		t.Fatalf("item = %+v", it)
	}
	if menus[1].Children[1].Enabled {
		t.Fatal("disabled item reported enabled")
	}
}

func TestMenuShortcut(t *testing.T) {
	for _, c := range []struct {
		ch   string
		mods int
		want string
	}{{"n", 0, "⌘N"}, {"N", 1, "⇧⌘N"}, {"s", 3, "⌥⇧⌘S"}, {"f", 4, "⌃⌘F"}, {"x", 8, "X"}, {"", 0, ""}} {
		if got := menuShortcut(c.ch, c.mods); got != c.want {
			t.Errorf("menuShortcut(%q, %d) = %q, want %q", c.ch, c.mods, got, c.want)
		}
	}
}

// render prints an outline the way tests compare it.
func render(e computer.Element, depth int) string {
	var b strings.Builder
	b.WriteString(strings.Repeat("  ", depth))
	if e.ID > 0 {
		b.WriteString("[" + itoa(e.ID) + "] ")
	}
	b.WriteString(e.Role)
	if e.Name != "" {
		b.WriteString(` "` + e.Name + `"`)
	}
	if e.Value != "" || (e.Role == "search field") {
		b.WriteString(` = "` + strings.ReplaceAll(e.Value, "\n", `\n`) + `"`)
	}
	if e.Selected {
		b.WriteString(" selected")
	}
	if e.Secure {
		b.WriteString(" secure")
	}
	if e.Frame != (computer.Rect{}) && depth > 0 {
		b.WriteString(" (" + ftoa(e.Frame.X) + "," + ftoa(e.Frame.Y) + " " + ftoa(e.Frame.W) + "×" + ftoa(e.Frame.H) + ")")
	}
	if len(e.Actions) > 0 {
		b.WriteString(" " + strings.Join(e.Actions, ","))
	}
	for _, c := range e.Children {
		b.WriteString("\n" + render(c, depth+1))
	}
	return b.String()
}

func itoa(i int) string     { return strconv.Itoa(i) }
func ftoa(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }
