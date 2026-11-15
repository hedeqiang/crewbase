// Command icongen renders the desktop app's icons from the brand mark
// (docs/brand/crewbase-mark.svg, the source of truth):
//
//   - resources/icon.png: the 1024×1024 app icon (the mark on Apple's icon
//     grid: an 824 px body centred with 100 px margins);
//   - assets/tray.png: the 32×32 (16 pt @2x) menu bar template icon, the
//     mark's white shapes drawn black on transparent, keeping their
//     opacities, without the tile. A shape marked data-tray="cut" is
//     punched out of the shapes below it (the pupils), and data-tray="skip"
//     leaves it out (the highlights);
//   - with -web DIR: DIR/icon-{180,192,512}.png, the full-bleed (maskable)
//     PWA and touch icons, the tile drawn square edge to edge.
//
// It understands the subset of SVG the mark uses: <rect> and <circle> with
// fill, opacity, rx and data-tray. Run it with `go generate ./apps/desktop`.
package main

import (
	"encoding/xml"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type shape struct {
	circle        bool
	x, y, w, h, r float64 // rect: x y w h rx; circle: x y = centre, r
	fill          color.NRGBA
	opacity       float64
	tray          string // data-tray: "", "cut" or "skip"
}

func (s shape) inside(px, py float64) bool {
	if s.circle {
		dx, dy := px-s.x, py-s.y
		return dx*dx+dy*dy <= s.r*s.r
	}
	if px < s.x || py < s.y || px > s.x+s.w || py > s.y+s.h {
		return false
	}
	r := math.Min(s.r, math.Min(s.w, s.h)/2)
	if r <= 0 {
		return true
	}
	cx := math.Max(s.x+r, math.Min(px, s.x+s.w-r))
	cy := math.Max(s.y+r, math.Min(py, s.y+s.h-r))
	dx, dy := px-cx, py-cy
	return dx*dx+dy*dy <= r*r
}

func attr(se xml.StartElement, name string, def float64) float64 {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			v, err := strconv.ParseFloat(strings.TrimSpace(a.Value), 64)
			if err == nil {
				return v
			}
		}
	}
	return def
}

func str(se xml.StartElement, name string) string {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			return strings.TrimSpace(a.Value)
		}
	}
	return ""
}

func fill(se xml.StartElement) color.NRGBA {
	for _, a := range se.Attr {
		if a.Name.Local == "fill" {
			v := strings.TrimPrefix(strings.TrimSpace(a.Value), "#")
			if len(v) == 3 {
				v = string([]byte{v[0], v[0], v[1], v[1], v[2], v[2]})
			}
			if n, err := strconv.ParseUint(v, 16, 32); err == nil && len(v) == 6 {
				return color.NRGBA{uint8(n >> 16), uint8(n >> 8), uint8(n), 255}
			}
		}
	}
	return color.NRGBA{A: 255}
}

// parse returns the shapes in paint order and the viewBox size.
func parse(path string) ([]shape, float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	d := xml.NewDecoder(f)
	var out []shape
	size := 32.0
	for {
		t, err := d.Token()
		if err != nil {
			break
		}
		se, ok := t.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "svg":
			for _, a := range se.Attr {
				if a.Name.Local == "viewBox" {
					if p := strings.Fields(a.Value); len(p) == 4 {
						size, _ = strconv.ParseFloat(p[2], 64)
					}
				}
			}
		case "rect":
			out = append(out, shape{x: attr(se, "x", 0), y: attr(se, "y", 0), w: attr(se, "width", 0), h: attr(se, "height", 0),
				r: attr(se, "rx", 0), fill: fill(se), opacity: attr(se, "opacity", 1), tray: str(se, "data-tray")})
		case "circle":
			out = append(out, shape{circle: true, x: attr(se, "cx", 0), y: attr(se, "cy", 0), r: attr(se, "r", 0),
				fill: fill(se), opacity: attr(se, "opacity", 1), tray: str(se, "data-tray")})
		}
	}
	if len(out) == 0 {
		return nil, 0, fmt.Errorf("%s: no shapes", path)
	}
	return out, size, nil
}

// render draws shapes into a px×px image; unit→pixel is p = (u-ox)*scale+off.
func render(shapes []shape, px int, scale, ox, oy, off float64) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, px, px))
	const ss = 4 // 4×4 supersampling
	for y := 0; y < px; y++ {
		for x := 0; x < px; x++ {
			var r, g, b, a float64 // premultiplied accumulation
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					u := (float64(x)+(float64(sx)+0.5)/ss-off)/scale + ox
					v := (float64(y)+(float64(sy)+0.5)/ss-off)/scale + oy
					var cr, cg, cb, ca float64
					for _, s := range shapes {
						if !s.inside(u, v) {
							continue
						}
						sa := s.opacity
						if s.tray == "cut" {
							// Erase what is below, by this shape's opacity.
							cr, cg, cb, ca = cr*(1-sa), cg*(1-sa), cb*(1-sa), ca*(1-sa)
							continue
						}
						cr = float64(s.fill.R)/255*sa + cr*(1-sa)
						cg = float64(s.fill.G)/255*sa + cg*(1-sa)
						cb = float64(s.fill.B)/255*sa + cb*(1-sa)
						ca = sa + ca*(1-sa)
					}
					r, g, b, a = r+cr, g+cg, b+cb, a+ca
				}
			}
			n := float64(ss * ss)
			r, g, b, a = r/n, g/n, b/n, a/n
			if a == 0 {
				continue
			}
			img.SetNRGBA(x, y, color.NRGBA{uint8(math.Round(r / a * 255)), uint8(math.Round(g / a * 255)), uint8(math.Round(b / a * 255)), uint8(math.Round(a * 255))})
		}
	}
	return img
}

func write(path string, img image.Image) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		log.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		log.Fatal(err)
	}
	if err := f.Close(); err != nil {
		log.Fatal(err)
	}
	fmt.Println("wrote", path)
}

func main() {
	src := flag.String("svg", "../../docs/brand/crewbase-mark.svg", "the brand mark")
	icon := flag.String("icon", "resources/icon.png", "the 1024×1024 app icon")
	tray := flag.String("tray", "assets/tray.png", "the 32×32 tray template icon")
	web := flag.String("web", "", "if set, write the web app's full-bleed icon-{180,192,512}.png here")
	flag.Parse()

	shapes, size, err := parse(*src)
	if err != nil {
		log.Fatal(err)
	}
	// The colour icons paint every shape as drawn; data-tray is for the tray.
	trayShapes := shapes
	shapes = append([]shape(nil), shapes...)
	for i := range shapes {
		shapes[i].tray = ""
	}

	// App icon: the tile on Apple's grid, 824 of 1024 px.
	const body = 824.0
	write(*icon, render(shapes, 1024, body/size, 0, 0, (1024-body)/2))

	// Web: the tile square to the edge (maskable; the OS applies its own mask).
	if *web != "" {
		square := append([]shape(nil), shapes...)
		if t := square[0]; !t.circle && t.w >= size && t.h >= size {
			square[0].r = 0
		}
		for _, px := range []int{180, 192, 512} {
			write(filepath.Join(*web, fmt.Sprintf("icon-%d.png", px)), render(square, px, float64(px)/size, 0, 0, 0))
		}
	}

	// Tray: the glyphs alone (the first full-size rect is the tile), black.
	var glyphs []shape
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for i, s := range trayShapes {
		if i == 0 && !s.circle && s.w >= size && s.h >= size || s.tray == "skip" {
			continue
		}
		s.fill = color.NRGBA{A: 255}
		glyphs = append(glyphs, s)
		x0, y0, x1, y1 := s.x, s.y, s.x+s.w, s.y+s.h
		if s.circle {
			x0, y0, x1, y1 = s.x-s.r, s.y-s.r, s.x+s.r, s.y+s.r
		}
		minX, minY = math.Min(minX, x0), math.Min(minY, y0)
		maxX, maxY = math.Max(maxX, x1), math.Max(maxY, y1)
	}
	const px, margin = 32.0, 2.0
	scale := (px - 2*margin) / math.Max(maxX-minX, maxY-minY)
	// Centre the glyphs' bounding box in the canvas.
	cx, cy := (minX+maxX)/2, (minY+maxY)/2
	write(*tray, render(glyphs, int(px), scale, cx, cy, px/2))
}
