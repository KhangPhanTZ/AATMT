// Command mkicon renders the AATMT logo (same geometry as web/static/favicon.svg)
// to PNG and to a multi-resolution Windows .ico, using only the standard library.
//
//	go run ./tools/mkicon -ico dist/aatmt.ico -png dist/aatmt.png
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
)

// Logo geometry on a 64×64 grid (mirrors favicon.svg).
var (
	ink   = color.NRGBA{0x11, 0x11, 0x11, 0xff}
	paper = color.NRGBA{0xff, 0xff, 0xff, 0xff}
	red   = color.NRGBA{0xe5, 0x32, 0x2d, 0xff}
	// the "A": triangle with a triangular notch cut from its base
	aShape           = [][2]float64{{14, 50}, {32, 14}, {50, 50}, {40, 50}, {32, 34}, {24, 50}}
	dotX, dotY, dotR = 32.0, 45.0, 4.5
)

// Sizes Windows uses for app icons (Explorer, taskbar, Start, high-DPI).
var icoSizes = []int{16, 20, 24, 32, 40, 48, 64, 128, 256}

// supersample is the anti-aliasing grid per pixel (n×n samples).
const supersample = 4

func inPolygon(x, y float64, pts [][2]float64) bool {
	in := false
	for i, j := 0, len(pts)-1; i < len(pts); j, i = i, i+1 {
		xi, yi, xj, yj := pts[i][0], pts[i][1], pts[j][0], pts[j][1]
		if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
			in = !in
		}
	}
	return in
}

func sample(x, y float64) color.NRGBA {
	if math.Hypot(x-dotX, y-dotY) <= dotR {
		return red
	}
	if inPolygon(x, y, aShape) {
		return paper
	}
	return ink
}

func render(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	scale := 64.0 / float64(size)
	n := float64(supersample * supersample)
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var r, g, b float64
			for sy := 0; sy < supersample; sy++ {
				for sx := 0; sx < supersample; sx++ {
					c := sample((float64(px)+(float64(sx)+0.5)/supersample)*scale, (float64(py)+(float64(sy)+0.5)/supersample)*scale)
					r, g, b = r+float64(c.R), g+float64(c.G), b+float64(c.B)
				}
			}
			img.SetNRGBA(px, py, color.NRGBA{uint8(r / n), uint8(g / n), uint8(b / n), 0xff})
		}
	}
	return img
}

// writeICO stores each size as a PNG entry (supported since Windows Vista).
func writeICO(path string) error {
	var images [][]byte
	for _, s := range icoSizes {
		var buf bytes.Buffer
		if err := png.Encode(&buf, render(s)); err != nil {
			return err
		}
		images = append(images, buf.Bytes())
	}
	var out bytes.Buffer
	_ = binary.Write(&out, binary.LittleEndian, [3]uint16{0, 1, uint16(len(icoSizes))})
	offset := 6 + 16*len(icoSizes)
	for i, s := range icoSizes {
		dim := uint8(s)
		if s >= 256 {
			dim = 0 // 0 means 256 in the ICO directory
		}
		out.Write([]byte{dim, dim, 0, 0})
		_ = binary.Write(&out, binary.LittleEndian, [2]uint16{1, 32})
		_ = binary.Write(&out, binary.LittleEndian, [2]uint32{uint32(len(images[i])), uint32(offset)})
		offset += len(images[i])
	}
	for _, b := range images {
		out.Write(b)
	}
	return os.WriteFile(path, out.Bytes(), 0o644)
}

func main() {
	icoPath := flag.String("ico", "", "write a Windows .ico here")
	pngPath := flag.String("png", "", "write a PNG here")
	pngSize := flag.Int("size", 256, "PNG size in pixels")
	flag.Parse()
	if *icoPath == "" && *pngPath == "" {
		log.Fatal("nothing to do: pass -ico and/or -png")
	}
	if *icoPath != "" {
		if err := writeICO(*icoPath); err != nil {
			log.Fatal(err)
		}
	}
	if *pngPath != "" {
		f, err := os.Create(*pngPath)
		if err != nil {
			log.Fatal(err)
		}
		if err := png.Encode(f, render(*pngSize)); err != nil {
			log.Fatal(err)
		}
		_ = f.Close()
	}
}
