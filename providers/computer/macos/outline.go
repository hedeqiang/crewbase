package macos

import (
	"fmt"
	"hash/fnv"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hedeqiang/crewbase/capabilities/computer"
)

// Limits of an outline.
const (
	DefaultMaxNodes = 400
	maxChildren     = 200 // per node, so one huge list cannot eat the outline
	maxValueRunes   = 200
	maxMenuItems    = 60 // per menu
	maxMenuTotal    = 600
)

// roleNames turns AX roles into words; subroleNames win where they apply.
var roleNames = map[string]string{
	"AXApplication":         "application",
	"AXBrowser":             "browser",
	"AXBusyIndicator":       "busy indicator",
	"AXButton":              "button",
	"AXCell":                "cell",
	"AXCheckBox":            "checkbox",
	"AXColorWell":           "color well",
	"AXColumn":              "column",
	"AXComboBox":            "combo box",
	"AXDateField":           "date field",
	"AXDisclosureTriangle":  "disclosure triangle",
	"AXDrawer":              "drawer",
	"AXGrid":                "grid",
	"AXGroup":               "group",
	"AXGrowArea":            "grow area",
	"AXHandle":              "handle",
	"AXHeading":             "heading",
	"AXImage":               "image",
	"AXIncrementor":         "stepper",
	"AXLayoutArea":          "layout area",
	"AXLayoutItem":          "layout item",
	"AXLevelIndicator":      "level indicator",
	"AXLink":                "link",
	"AXList":                "list",
	"AXMenu":                "menu",
	"AXMenuBar":             "menu bar",
	"AXMenuBarItem":         "menu",
	"AXMenuButton":          "menu button",
	"AXMenuItem":            "menu item",
	"AXOutline":             "outline",
	"AXPopUpButton":         "pop-up button",
	"AXPopover":             "popover",
	"AXProgressIndicator":   "progress indicator",
	"AXRadioButton":         "radio button",
	"AXRadioGroup":          "radio group",
	"AXRelevanceIndicator":  "relevance indicator",
	"AXRow":                 "row",
	"AXRuler":               "ruler",
	"AXScrollArea":          "scroll area",
	"AXScrollBar":           "scroll bar",
	"AXSecureTextField":     "password field",
	"AXSheet":               "sheet",
	"AXSlider":              "slider",
	"AXSplitGroup":          "split group",
	"AXSplitter":            "splitter",
	"AXStaticText":          "text",
	"AXTabGroup":            "tab group",
	"AXTable":               "table",
	"AXTextArea":            "text area",
	"AXTextField":           "text field",
	"AXToolbar":             "toolbar",
	"AXUnknown":             "unknown",
	"AXValueIndicator":      "value indicator",
	"AXWebArea":             "web area",
	"AXWindow":              "window",
	"AXSegmentedControl":    "segmented control",
	"AXDockItem":            "dock item",
	"AXRulerMarker":         "ruler marker",
	"AXMatte":               "matte",
	"AXSystemWide":          "system",
	"AXDisclosedByRow":      "row",
	"AXListMarker":          "list marker",
	"AXTimeField":           "time field",
	"AXPage":                "page",
	"AXLayoutAreaContainer": "layout area",
}

var subroleNames = map[string]string{
	"AXSearchField":       "search field",
	"AXSecureTextField":   "password field",
	"AXTabButton":         "tab",
	"AXToggle":            "toggle",
	"AXSwitch":            "switch",
	"AXCloseButton":       "close button",
	"AXMinimizeButton":    "minimize button",
	"AXZoomButton":        "zoom button",
	"AXFullScreenButton":  "full screen button",
	"AXToolbarButton":     "toolbar button",
	"AXDialog":            "dialog",
	"AXSystemDialog":      "dialog",
	"AXFloatingWindow":    "floating window",
	"AXOutlineRow":        "row",
	"AXSortButton":        "sort button",
	"AXIncrementArrow":    "increment arrow",
	"AXDecrementArrow":    "decrement arrow",
	"AXTextLink":          "link",
	"AXContentList":       "list",
	"AXDefinitionList":    "list",
	"AXDescriptionList":   "list",
	"AXTableRow":          "row",
	"AXTextAttachment":    "attachment",
	"AXApplicationDialog": "dialog",
	"AXApplicationAlert":  "alert",
}

// roleName is the human words for an AX role (and subrole).
func roleName(role, subrole string) string {
	if n, ok := subroleNames[subrole]; ok {
		return n
	}
	if n, ok := roleNames[role]; ok {
		return n
	}
	return splitCamel(strings.TrimPrefix(role, "AX"))
}

// splitCamel turns "FancyWidget" into "fancy widget".
func splitCamel(s string) string {
	if s == "" {
		return "unknown"
	}
	var b strings.Builder
	prev := rune(0)
	for i, r := range s {
		if i > 0 && unicode.IsUpper(r) && !unicode.IsUpper(prev) {
			b.WriteByte(' ')
		}
		b.WriteRune(unicode.ToLower(r))
		prev = r
	}
	return b.String()
}

// actionName turns "AXShowMenu" into "show menu".
func actionName(a string) string { return splitCamel(strings.TrimPrefix(a, "AX")) }

// noiseActions do not make an element worth numbering on their own.
var noiseActions = map[string]bool{
	"AXShowMenu":        true,
	"AXScrollToVisible": true,
	"AXShowDefaultUI":   true,
	"AXShowAlternateUI": true,
}

// structural roles are dropped from the outline when they have no name and
// nothing to act on; their children move up.
var structural = map[string]bool{
	"AXGroup":          true,
	"AXScrollArea":     true,
	"AXSplitGroup":     true,
	"AXLayoutArea":     true,
	"AXLayoutItem":     true,
	"AXUnknown":        true,
	"AXSplitter":       true,
	"AXScrollBar":      true,
	"AXMatte":          true,
	"AXGrowArea":       true,
	"AXRuler":          true,
	"AXColumn":         true,
	"AXValueIndicator": true,
}

// focusable roles take typing.
var focusable = map[string]bool{
	"AXTextField":       true,
	"AXTextArea":        true,
	"AXSecureTextField": true,
	"AXComboBox":        true,
	"AXDateField":       true,
	"AXTimeField":       true,
}

// selectable roles are acted on by selecting them.
var selectable = map[string]bool{
	"AXRow":  true,
	"AXCell": true,
}

func (n rawNode) secure() bool {
	return n.Role == "AXSecureTextField" || n.Subrole == "AXSecureTextField"
}

// name is the best label of a node.
func (n rawNode) name() string {
	for _, s := range []string{n.Title, n.Description, n.Label, n.Help, n.Placeholder} {
		if s = strings.TrimSpace(s); s != "" {
			return truncate(s, maxValueRunes)
		}
	}
	return ""
}

func (n rawNode) meaningfulActions() []string {
	var out []string
	for _, a := range n.Actions {
		if !noiseActions[a] {
			out = append(out, a)
		}
	}
	return out
}

// actionable: the model can do something with it (press, set, focus,
// select).
func (n rawNode) actionable() bool {
	if len(n.meaningfulActions()) > 0 || n.Settable || focusable[n.Role] || n.secure() || selectable[n.Role] {
		return true
	}
	return false
}

func truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max]) + "…"
}

// elemInfo is what Act needs of a numbered element.
type elemInfo struct {
	ref    ref
	raw    rawNode
	frame  computer.Rect // window points
	secure bool
}

// tnode is a node of the tree while it is collected.
type tnode struct {
	ref  ref
	raw  rawNode
	kids []*tnode
}

// buildOutline walks the tree under root breadth first, reading at most
// limit nodes (root included) until deadline, and returns the outline with
// window-relative frames, the numbered elements (their refs retained; the
// caller releases them) and whether it stopped early. root stays owned by
// the caller.
func buildOutline(src nodeSource, root ref, rootRaw rawNode, origin computer.Rect, limit int, deadline time.Time) (computer.Element, []elemInfo, bool) {
	if limit <= 0 {
		limit = DefaultMaxNodes
	}
	top := &tnode{ref: root, raw: rootRaw}
	queue := []*tnode{top}
	count := 1
	truncated := false
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if count >= limit || (!deadline.IsZero() && time.Now().After(deadline)) {
			truncated = true
			break
		}
		kids, total, err := src.Children(n.ref, min(maxChildren, limit-count))
		if err != nil {
			continue
		}
		if total > len(kids) {
			truncated = true
		}
		for i, k := range kids {
			if count >= limit {
				for _, rest := range kids[i:] {
					src.Release(rest)
				}
				truncated = true
				break
			}
			raw, err := src.Read(k)
			if err != nil {
				src.Release(k)
				continue
			}
			c := &tnode{ref: k, raw: raw}
			n.kids = append(n.kids, c)
			queue = append(queue, c)
			count++
		}
	}

	var elems []elemInfo
	var emit func(n *tnode) []computer.Element
	emit = func(n *tnode) []computer.Element {
		e := toElement(n.raw, origin)
		act := n.raw.actionable()
		if act && n != top {
			elems = append(elems, elemInfo{ref: n.ref, raw: n.raw, frame: e.Frame, secure: n.raw.secure()})
			e.ID = len(elems)
		}
		var kids []computer.Element
		for _, k := range n.kids {
			kids = append(kids, emit(k)...)
		}
		e.Children = kids
		keep := e.ID != 0 || n == top
		if !keep && n.ref != 0 {
			src.Release(n.ref)
		}
		if n != top && !act && e.Name == "" && e.Value == "" && (structural[n.raw.Role] || n.raw.Role == "") {
			return kids // structure only: children move up
		}
		return []computer.Element{e}
	}
	out := emit(top)
	if len(out) != 1 {
		return computer.Element{Role: "window", Children: out}, elems, truncated
	}
	return out[0], elems, truncated
}

// toElement converts a raw node, frame relative to origin.
func toElement(n rawNode, origin computer.Rect) computer.Element {
	e := computer.Element{
		Role:     roleName(n.Role, n.Subrole),
		Name:     n.name(),
		Enabled:  n.Enabled || !n.HasEnabled,
		Focused:  n.Focused,
		Selected: n.Selected,
		Secure:   n.secure(),
	}
	if n.HasValue && !e.Secure {
		v := truncate(n.Value, maxValueRunes)
		if v != e.Name {
			e.Value = v
		}
	}
	if n.HasFrame {
		e.Frame = computer.Rect{X: round1(n.X - origin.X), Y: round1(n.Y - origin.Y), W: round1(n.W), H: round1(n.H)}
	}
	for _, a := range n.meaningfulActions() {
		e.Actions = append(e.Actions, actionName(a))
	}
	return e
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }

// hashOutline is the Version of an outline: it changes when anything the
// model sees changes. Never 0 (0 means "do not check").
func hashOutline(title string, root computer.Element, menus []computer.Element) int64 {
	h := fnv.New64a()
	fmt.Fprintf(h, "%s\x00", title)
	var walk func(e computer.Element, depth int)
	walk = func(e computer.Element, depth int) {
		fmt.Fprintf(h, "%d|%d|%s|%s|%s|%g,%g,%g,%g|%t%t%t%t|%s\x00", depth, e.ID, e.Role, e.Name, e.Value,
			e.Frame.X, e.Frame.Y, e.Frame.W, e.Frame.H, e.Enabled, e.Focused, e.Selected, e.Secure, strings.Join(e.Actions, ","))
		for _, c := range e.Children {
			walk(c, depth+1)
		}
	}
	walk(root, 0)
	for _, m := range menus {
		walk(m, 0)
	}
	v := int64(h.Sum64() & math.MaxInt64)
	if v == 0 {
		v = 1
	}
	return v
}

// menuShortcut formats a menu item's key equivalent ("⇧⌘N").
func menuShortcut(char string, mods int) string {
	if char == "" {
		return ""
	}
	var b strings.Builder
	if mods&4 != 0 {
		b.WriteString("⌃")
	}
	if mods&2 != 0 {
		b.WriteString("⌥")
	}
	if mods&1 != 0 {
		b.WriteString("⇧")
	}
	if mods&8 == 0 {
		b.WriteString("⌘")
	}
	b.WriteString(strings.ToUpper(char))
	return b.String()
}

// buildMenus reads the menu bar: top-level menus and their items, one
// level deep. All refs are released.
func buildMenus(src nodeSource, bar ref, deadline time.Time) []computer.Element {
	tops, _, err := src.Children(bar, 40)
	if err != nil {
		return nil
	}
	defer func() {
		for _, t := range tops {
			src.Release(t)
		}
	}()
	var out []computer.Element
	total := 0
	for _, t := range tops {
		if !deadline.IsZero() && time.Now().After(deadline) {
			break
		}
		raw, err := src.Read(t)
		if err != nil || strings.TrimSpace(raw.Title) == "" {
			continue
		}
		m := computer.Element{Role: "menu", Name: raw.Title, Enabled: raw.Enabled || !raw.HasEnabled}
		for _, it := range menuItems(src, t, maxMenuItems) {
			if total < maxMenuTotal {
				m.Children = append(m.Children, it)
				total++
			}
		}
		out = append(out, m)
	}
	return out
}

// menuItems reads the items of a menu bar item or a menu item with a
// submenu.
func menuItems(src nodeSource, item ref, max int) []computer.Element {
	refs := menuChildren(src, item, max)
	defer func() {
		for _, r := range refs {
			src.Release(r)
		}
	}()
	var out []computer.Element
	for _, r := range refs {
		raw, err := src.Read(r)
		if err != nil || strings.TrimSpace(raw.Title) == "" {
			continue // separators have no title
		}
		e := computer.Element{Role: "menu item", Name: raw.Title, Enabled: raw.Enabled || !raw.HasEnabled, Selected: raw.Selected}
		e.Value = menuShortcut(raw.CmdChar, raw.CmdMods)
		out = append(out, e)
	}
	return out
}

// menuChildren returns the items under a menu bar item or menu item (the
// children of its AXMenu), retained.
func menuChildren(src nodeSource, item ref, max int) []ref {
	kids, _, err := src.Children(item, 4)
	if err != nil {
		return nil
	}
	var menu ref
	for _, k := range kids {
		if menu == 0 {
			if raw, err := src.Read(k); err == nil && raw.Role == "AXMenu" {
				menu = k
				continue
			}
		}
		src.Release(k)
	}
	if menu == 0 {
		return nil
	}
	defer src.Release(menu)
	items, _, err := src.Children(menu, max)
	if err != nil {
		return nil
	}
	return items
}
