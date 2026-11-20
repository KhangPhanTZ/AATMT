// Package imgutil downsizes captured screenshots before they are sent to a
// vision model. Vision input is billed by pixel area, so a 4K screenshot can
// cost several times more than the same image at ~1568px with no OCR loss.
package imgutil

import (
	"bytes"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	"image/png"
)

const MaxEdge = 1568

// Normalize decodes PNG/JPEG/GIF, downsizes so the longest edge is <= MaxEdge
// and re-encodes (PNG stays PNG to keep text crisp). Unknown formats are
// returned unchanged.
func Normalize(data []byte, mime string) (out []byte, outMime string, w, h int) {
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return data, mime, 0, 0
	}
	b := img.Bounds()
	w, h = b.Dx(), b.Dy()
	if w <= MaxEdge && h <= MaxEdge && (format == "png" || format == "jpeg") {
		return data, "image/" + format, w, h
	}
	nw, nh := w, h
	if w > MaxEdge || h > MaxEdge {
		if w >= h {
			nw, nh = MaxEdge, h*MaxEdge/w
		} else {
			nw, nh = w*MaxEdge/h, MaxEdge
		}
	}
	dst := resize(img, nw, nh)
	var buf bytes.Buffer
	if format == "jpeg" {
		_ = jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 88})
		return buf.Bytes(), "image/jpeg", nw, nh
	}
	_ = png.Encode(&buf, dst)
	return buf.Bytes(), "image/png", nw, nh
}

// resize does area-averaging (box filter) downscaling, good for screenshots.
func resize(src image.Image, nw, nh int) *image.RGBA {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		y0 := sb.Min.Y + y*sh/nh
		y1 := sb.Min.Y + max((y+1)*sh/nh, y*sh/nh+1)
		for x := 0; x < nw; x++ {
			x0 := sb.Min.X + x*sw/nw
			x1 := sb.Min.X + max((x+1)*sw/nw, x*sw/nw+1)
			var r, g, bl, a, n uint64
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					cr, cg, cb, ca := src.At(xx, yy).RGBA()
					r += uint64(cr)
					g += uint64(cg)
					bl += uint64(cb)
					a += uint64(ca)
					n++
				}
			}
			dst.Set(x, y, color.RGBA64{uint16(r / n), uint16(g / n), uint16(bl / n), uint16(a / n)})
		}
	}
	return dst
}
