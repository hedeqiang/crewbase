package macos

import (
	"bytes"
	"image"
	"image/png"
)

// DefaultMaxEdge is the long edge of screenshots, in pixels.
const DefaultMaxEdge = 1280

// fitSize is the size of a w×h image whose long edge is at most maxEdge,
// keeping the aspect ratio; f is the factor applied (≤ 1).
func fitSize(w, h, maxEdge int) (int, int, float64) {
	long := max(w, h)
	if maxEdge <= 0 || long <= maxEdge {
		return w, h, 1
	}
	f := float64(maxEdge) / float64(long)
	nw := max(1, int(float64(w)*f+0.5))
	nh := max(1, int(float64(h)*f+0.5))
	if w >= h {
		nw = maxEdge
	} else {
		nh = maxEdge
	}
	return nw, nh, f
}

// downscale resizes img to w×h with a box filter (each output pixel is the
// area-weighted average of the source pixels it covers).
func downscale(img image.Image, w, h int) *image.RGBA {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	src := toRGBA(img)
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	fx := float64(sw) / float64(w)
	fy := float64(sh) / float64(h)
	for y := 0; y < h; y++ {
		y0, y1 := float64(y)*fy, float64(y+1)*fy
		for x := 0; x < w; x++ {
			x0, x1 := float64(x)*fx, float64(x+1)*fx
			var r, g, bl, a, wt float64
			for sy := int(y0); sy < sh && float64(sy) < y1; sy++ {
				wy := min(y1, float64(sy+1)) - max(y0, float64(sy))
				if wy <= 0 {
					continue
				}
				row := src.Pix[sy*src.Stride:]
				for sx := int(x0); sx < sw && float64(sx) < x1; sx++ {
					wx := min(x1, float64(sx+1)) - max(x0, float64(sx))
					if wx <= 0 {
						continue
					}
					k := wx * wy
					p := row[sx*4 : sx*4+4]
					r += float64(p[0]) * k
					g += float64(p[1]) * k
					bl += float64(p[2]) * k
					a += float64(p[3]) * k
					wt += k
				}
			}
			if wt == 0 {
				continue
			}
			o := dst.Pix[y*dst.Stride+x*4:]
			o[0] = uint8(r/wt + 0.5)
			o[1] = uint8(g/wt + 0.5)
			o[2] = uint8(bl/wt + 0.5)
			o[3] = uint8(a/wt + 0.5)
		}
	}
	return dst
}

func toRGBA(img image.Image) *image.RGBA {
	if r, ok := img.(*image.RGBA); ok && r.Rect.Min == (image.Point{}) {
		return r
	}
	b := img.Bounds()
	r := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			r.Set(x, y, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return r
}

// fitPNG decodes a PNG of a window pointsW points wide and returns it with
// its long edge at most maxEdge, its size and its Scale (pixels per window
// point).
func fitPNG(data []byte, pointsW float64, maxEdge int) ([]byte, int, int, float64, error) {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, 0, err
	}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	base := 1.0
	if pointsW > 0 {
		base = float64(w) / pointsW
	}
	nw, nh, _ := fitSize(w, h, maxEdge)
	if nw == w && nh == h {
		return data, w, h, base, nil
	}
	small := downscale(img, nw, nh)
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, small); err != nil {
		return nil, 0, 0, 0, err
	}
	return buf.Bytes(), nw, nh, base * float64(nw) / float64(w), nil
}
