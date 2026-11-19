package macos

import (
	"fmt"
	"strconv"
	"strings"
)

// CGEvent modifier flags.
const (
	flagShift   uint64 = 0x00020000
	flagControl uint64 = 0x00040000
	flagOption  uint64 = 0x00080000
	flagCommand uint64 = 0x00100000
	flagFn      uint64 = 0x00800000
)

// keyStroke is one key press with modifiers.
type keyStroke struct {
	Code  uint16
	Flags uint64
}

var modifierNames = map[string]uint64{
	"cmd": flagCommand, "command": flagCommand, "⌘": flagCommand, "meta": flagCommand, "super": flagCommand, "win": flagCommand,
	"shift": flagShift, "⇧": flagShift,
	"alt": flagOption, "opt": flagOption, "option": flagOption, "⌥": flagOption,
	"ctrl": flagControl, "control": flagControl, "ctl": flagControl, "⌃": flagControl,
	"fn": flagFn,
}

// keyCodes are macOS virtual key codes (kVK_*, ANSI positions).
var keyCodes = map[string]uint16{
	"a": 0x00, "s": 0x01, "d": 0x02, "f": 0x03, "h": 0x04, "g": 0x05, "z": 0x06, "x": 0x07,
	"c": 0x08, "v": 0x09, "b": 0x0B, "q": 0x0C, "w": 0x0D, "e": 0x0E, "r": 0x0F, "y": 0x10,
	"t": 0x11, "1": 0x12, "2": 0x13, "3": 0x14, "4": 0x15, "6": 0x16, "5": 0x17, "=": 0x18,
	"9": 0x19, "7": 0x1A, "-": 0x1B, "8": 0x1C, "0": 0x1D, "]": 0x1E, "o": 0x1F, "u": 0x20,
	"[": 0x21, "i": 0x22, "p": 0x23, "l": 0x25, "j": 0x26, "'": 0x27, "k": 0x28, ";": 0x29,
	"\\": 0x2A, ",": 0x2B, "/": 0x2C, "n": 0x2D, "m": 0x2E, ".": 0x2F, "`": 0x32,

	"return": 0x24, "enter": 0x24, "tab": 0x30, "space": 0x31, "delete": 0x33, "backspace": 0x33,
	"escape": 0x35, "esc": 0x35, "forwarddelete": 0x75, "del": 0x75,
	"home": 0x73, "end": 0x77, "pageup": 0x74, "pagedown": 0x79, "help": 0x72,
	"left": 0x7B, "right": 0x7C, "down": 0x7D, "up": 0x7E,
	"keypadenter": 0x4C,

	"f1": 0x7A, "f2": 0x78, "f3": 0x63, "f4": 0x76, "f5": 0x60, "f6": 0x61, "f7": 0x62, "f8": 0x64,
	"f9": 0x65, "f10": 0x6D, "f11": 0x67, "f12": 0x6F, "f13": 0x69, "f14": 0x6B, "f15": 0x71,
	"f16": 0x6A, "f17": 0x40, "f18": 0x4F, "f19": 0x50, "f20": 0x5A,
}

var keyAliases = map[string]string{
	"ret": "return", "cr": "return", "⏎": "return", "↩": "return",
	"⇥": "tab", "spacebar": "space", " ": "space",
	"bksp": "backspace", "⌫": "backspace", "⌦": "forwarddelete",
	"pgup": "pageup", "pgdn": "pagedown", "page_up": "pageup", "page_down": "pagedown",
	"arrowleft": "left", "arrowright": "right", "arrowup": "up", "arrowdown": "down",
	"←": "left", "→": "right", "↑": "up", "↓": "down", "⎋": "escape",
	"plus": "=", "minus": "-", "comma": ",", "period": ".", "slash": "/", "backslash": "\\",
	"semicolon": ";", "quote": "'", "backquote": "`", "grave": "`", "equal": "=", "equals": "=",
	"bracketleft": "[", "bracketright": "]",
}

// shifted characters map to their key with shift ("?" is shift+/).
var shifted = map[string]string{
	"!": "1", "@": "2", "#": "3", "$": "4", "%": "5", "^": "6", "&": "7", "*": "8", "(": "9", ")": "0",
	"_": "-", "+": "=", "{": "[", "}": "]", "|": "\\", ":": ";", "\"": "'", "<": ",", ">": ".", "?": "/", "~": "`",
}

// parseKeys parses "cmd+shift+n", "return", "esc", "ctrl+a ctrl+e" (a
// space-separated sequence) into key strokes.
func parseKeys(s string) ([]keyStroke, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("no keys given")
	}
	var out []keyStroke
	for _, combo := range strings.Fields(s) {
		k, err := parseCombo(combo)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, nil
}

func parseCombo(combo string) (keyStroke, error) {
	parts := strings.Split(combo, "+")
	// "cmd++" means cmd and the "+" key
	if strings.HasSuffix(combo, "++") {
		parts = append(strings.Split(strings.TrimSuffix(combo, "++"), "+"), "+")
	} else if combo == "+" {
		parts = []string{"+"}
	}
	var k keyStroke
	key := ""
	for i, p := range parts {
		lp := strings.ToLower(strings.TrimSpace(p))
		if lp == "" {
			return k, fmt.Errorf("bad key combo %q", combo)
		}
		if f, ok := modifierNames[lp]; ok && i < len(parts)-1 {
			k.Flags |= f
			continue
		}
		if i != len(parts)-1 {
			return k, fmt.Errorf("unknown modifier %q in %q (use cmd, shift, alt, ctrl, fn)", p, combo)
		}
		key = p
	}
	code, shift, err := keyCode(key)
	if err != nil {
		return k, fmt.Errorf("%w in %q", err, combo)
	}
	k.Code = code
	if shift {
		k.Flags |= flagShift
	}
	return k, nil
}

// keyCode finds the virtual key code of a key name; shift reports a
// shifted character ("A", "?").
func keyCode(name string) (uint16, bool, error) {
	if name == "" {
		return 0, false, fmt.Errorf("no key")
	}
	if c, ok := shifted[name]; ok {
		return keyCodes[c], true, nil
	}
	shift := false
	if len(name) == 1 && name[0] >= 'A' && name[0] <= 'Z' {
		shift = true
	}
	l := strings.ToLower(name)
	if a, ok := keyAliases[l]; ok {
		l = a
	}
	if c, ok := keyCodes[l]; ok {
		return c, shift, nil
	}
	// modifier pressed alone ("shift")
	if _, ok := modifierNames[l]; ok {
		return 0, false, fmt.Errorf("a modifier needs a key (e.g. cmd+n)")
	}
	if strings.HasPrefix(l, "f") {
		if n, err := strconv.Atoi(l[1:]); err == nil {
			return 0, false, fmt.Errorf("no key f%d (f1–f20)", n)
		}
	}
	return 0, false, fmt.Errorf("unknown key %q", name)
}

// modFlags turns held keys ("cmd", "shift+alt", "") into CGEvent flags.
func modFlags(mods string) (uint64, error) {
	mods = strings.TrimSpace(mods)
	if mods == "" {
		return 0, nil
	}
	var f uint64
	for _, part := range strings.FieldsFunc(strings.ToLower(mods), func(r rune) bool { return r == '+' || r == ' ' || r == ',' }) {
		m, ok := modifierNames[part]
		if !ok {
			return 0, fmt.Errorf("unknown modifier %q (use cmd, shift, alt, ctrl, fn)", part)
		}
		f |= m
	}
	return f, nil
}
