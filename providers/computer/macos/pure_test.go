package macos

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"strings"
	"testing"

	"github.com/hedeqiang/crewbase/capabilities/computer"
)

func TestParseKeys(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []keyStroke
	}{
		{"cmd+shift+n", []keyStroke{{0x2D, flagCommand | flagShift}}},
		{"return", []keyStroke{{0x24, 0}}},
		{"Enter", []keyStroke{{0x24, 0}}},
		{"esc", []keyStroke{{0x35, 0}}},
		{"tab", []keyStroke{{0x30, 0}}},
		{"down", []keyStroke{{0x7D, 0}}},
		{"f5", []keyStroke{{0x60, 0}}},
		{"F12", []keyStroke{{0x6F, 0}}},
		{"ctrl+alt+delete", []keyStroke{{0x33, flagControl | flagOption}}},
		{"⌘+⇧+z", []keyStroke{{0x06, flagCommand | flagShift}}},
		{"cmd+A", []keyStroke{{0x00, flagCommand | flagShift}}},
		{"cmd++", []keyStroke{{0x18, flagCommand | flagShift}}},
		{"cmd+=", []keyStroke{{0x18, flagCommand}}},
		{"shift+/", []keyStroke{{0x2C, flagShift}}},
		{"?", []keyStroke{{0x2C, flagShift}}},
		{"option+left", []keyStroke{{0x7B, flagOption}}},
		{"space", []keyStroke{{0x31, 0}}},
		{"pagedown", []keyStroke{{0x79, 0}}},
		{"ctrl+a ctrl+e", []keyStroke{{0x00, flagControl}, {0x0E, flagControl}}},
		{"cmd+1", []keyStroke{{0x12, flagCommand}}},
	} {
		got, err := parseKeys(c.in)
		if err != nil {
			t.Errorf("parseKeys(%q): %v", c.in, err)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("parseKeys(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("parseKeys(%q)[%d] = %+v, want %+v", c.in, i, got[i], c.want[i])
			}
		}
	}
	for _, bad := range []string{"", "cmd", "cmd+", "hyper+x", "f25", "cmd+nope", "shift"} {
		if _, err := parseKeys(bad); err == nil {
			t.Errorf("parseKeys(%q): want an error", bad)
		}
	}
}

func TestToScreen(t *testing.T) {
	// main display 1440×900 at the origin; a second display to the left
	// (negative x) and one above (negative y)
	screens := []computer.Rect{{X: 0, Y: 0, W: 1440, H: 900}, {X: -1920, Y: 0, W: 1920, H: 1080}, {X: 0, Y: -1117, W: 1728, H: 1117}}
	for _, c := range []struct {
		win    computer.Rect
		x, y   float64
		gx, gy float64
		err    string
	}{
		{win: computer.Rect{X: 100, Y: 50, W: 800, H: 600}, x: 10, y: 20, gx: 110, gy: 70},
		{win: computer.Rect{X: -1800, Y: 100, W: 800, H: 600}, x: 30, y: 40, gx: -1770, gy: 140},
		{win: computer.Rect{X: 200, Y: -1000, W: 800, H: 600}, x: 5, y: 5, gx: 205, gy: -995},
		{win: computer.Rect{X: 100, Y: 50, W: 800, H: 600}, x: 800, y: 20, err: "outside the window"},
		{win: computer.Rect{X: 100, Y: 50, W: 800, H: 600}, x: -1, y: 20, err: "outside the window"},
		// a window hanging off the bottom of the main display, below the
		// left one too
		{win: computer.Rect{X: 1000, Y: 800, W: 800, H: 600}, x: 100, y: 300, err: "off screen"},
		{win: computer.Rect{}, x: 1, y: 1, err: "no size"},
	} {
		gx, gy, err := toScreen(c.win, c.x, c.y, screens)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("toScreen(%+v, %g, %g) err = %v, want %q", c.win, c.x, c.y, err, c.err)
			}
			continue
		}
		if err != nil || gx != c.gx || gy != c.gy {
			t.Errorf("toScreen(%+v, %g, %g) = %g, %g, %v; want %g, %g", c.win, c.x, c.y, gx, gy, err, c.gx, c.gy)
		}
	}
	if i := screenOf(screens, computer.Rect{X: -1500, Y: 200, W: 800, H: 600}); i != 1 {
		t.Errorf("screenOf left window = %d", i)
	}
	if i := screenOf(screens, computer.Rect{X: 1300, Y: 100, W: 400, H: 300}); i != 0 {
		t.Errorf("screenOf straddling window = %d", i)
	}
	if i := screenOf(screens, computer.Rect{X: 100, Y: -900, W: 400, H: 300}); i != 2 {
		t.Errorf("screenOf top window = %d", i)
	}
}

func TestMatchWindow(t *testing.T) {
	wins := []cgWindow{
		{ID: 1, Layer: 25, Frame: computer.Rect{X: 0, Y: 0, W: 30, H: 30}}, // status item
		{ID: 2, Layer: 0, Frame: computer.Rect{X: 10, Y: 10, W: 500, H: 400}},
		{ID: 3, Layer: 0, Frame: computer.Rect{X: 100, Y: 50, W: 800, H: 600}},
	}
	if w, _ := matchWindow(wins, computer.Rect{X: 100, Y: 50, W: 800, H: 600}); w.ID != 3 {
		t.Errorf("exact frame → %d", w.ID)
	}
	if w, _ := matchWindow(wins, computer.Rect{}); w.ID != 2 {
		t.Errorf("no frame → %d, want the frontmost normal window", w.ID)
	}
	if w, _ := matchWindow(wins, computer.Rect{X: 900, Y: 900, W: 10, H: 10}); w.ID != 2 {
		t.Errorf("no match → %d, want the frontmost normal window", w.ID)
	}
	if _, ok := matchWindow(wins[:1], computer.Rect{}); ok {
		t.Error("only a status item: want no window")
	}
}

func TestMatchTitle(t *testing.T) {
	titles := []string{"File", "Edit", "Save As…", "Save", "Export as PDF...", "新建备忘录"}
	for _, c := range []struct {
		want string
		idx  int
	}{
		{"Save", 3},           // exact beats prefix of "Save As…"
		{"save", 3},           // case-insensitive
		{"Save As...", 2},     // ellipses count the same
		{"save as", 2},        // without the ellipsis
		{"Export as PDF…", 4}, //
		{"Exp", 4},            // prefix
		{"新建", 5},             // prefix, Chinese
		{"Print", -1},         //
		{"", -1},              //
	} {
		if got := matchTitle(titles, c.want); got != c.idx {
			t.Errorf("matchTitle(%q) = %d, want %d", c.want, got, c.idx)
		}
	}
}

func TestFitSize(t *testing.T) {
	for _, c := range []struct{ w, h, max, ww, wh int }{
		{2560, 1600, 1280, 1280, 800},
		{1600, 2560, 1280, 800, 1280},
		{1000, 600, 1280, 1000, 600},
		{3000, 1, 1280, 1280, 1},
		{2000, 1000, 0, 2000, 1000},
	} {
		w, h, _ := fitSize(c.w, c.h, c.max)
		if w != c.ww || h != c.wh {
			t.Errorf("fitSize(%d, %d, %d) = %d×%d, want %d×%d", c.w, c.h, c.max, w, h, c.ww, c.wh)
		}
	}
}

func TestDownscale(t *testing.T) {
	// 4×2: left half black, right half white → 2×1: black, white
	img := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			c := color.RGBA{0, 0, 0, 255}
			if x >= 2 {
				c = color.RGBA{255, 255, 255, 255}
			}
			img.Set(x, y, c)
		}
	}
	out := downscale(img, 2, 1)
	if got := out.RGBAAt(0, 0); got != (color.RGBA{0, 0, 0, 255}) {
		t.Errorf("left = %v", got)
	}
	if got := out.RGBAAt(1, 0); got != (color.RGBA{255, 255, 255, 255}) {
		t.Errorf("right = %v", got)
	}
	// 3→2 straddles a pixel: the middle source column is split
	img3 := image.NewRGBA(image.Rect(0, 0, 3, 1))
	img3.Set(0, 0, color.RGBA{0, 0, 0, 255})
	img3.Set(1, 0, color.RGBA{90, 90, 90, 255})
	img3.Set(2, 0, color.RGBA{180, 180, 180, 255})
	o := downscale(img3, 2, 1)
	// left: (0×1 + 90×0.5)/1.5 = 30; right: (90×0.5 + 180×1)/1.5 = 150
	if a, b := o.RGBAAt(0, 0).R, o.RGBAAt(1, 0).R; a != 30 || b != 150 {
		t.Errorf("straddle = %d, %d; want 30, 150", a, b)
	}
}

func TestFitPNGScale(t *testing.T) {
	// a Retina capture of a 1000×500-point window: 2000×1000 pixels
	img := image.NewRGBA(image.Rect(0, 0, 2000, 1000))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	_, w, h, scale, err := fitPNG(buf.Bytes(), 1000, 1280)
	if err != nil {
		t.Fatal(err)
	}
	if w != 1280 || h != 640 || math.Abs(scale-1.28) > 1e-9 {
		t.Fatalf("fit = %d×%d scale %g, want 1280×640 scale 1.28", w, h, scale)
	}
	data, w, h, scale, err := fitPNG(buf.Bytes(), 1000, 4000)
	if err != nil || w != 2000 || h != 1000 || scale != 2 || !bytes.Equal(data, buf.Bytes()) {
		t.Fatalf("no downscale = %d×%d scale %g (%v)", w, h, scale, err)
	}
}
