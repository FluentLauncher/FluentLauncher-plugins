// Generate the official plugins' PNG icons. Geometry lives here so assets can
// be regenerated without a graphics editor or an additional dependency.
package main

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

const scale = 4

var ink = color.RGBA{245, 250, 255, 255}

func main() {
	for _, name := range []string{"echo", "devtools", "clipboard", "screenshot"} {
		canvas := image.NewRGBA(image.Rect(0, 0, 256*scale, 256*scale))
		for y := range canvas.Bounds().Dy() {
			for x := range canvas.Bounds().Dx() {
				px, py := float64(x)/scale, float64(y)/scale
				cx, cy := math.Max(35-px, math.Max(px-220, 0)), math.Max(35-py, math.Max(py-220, 0))
				if cx*cx+cy*cy <= 35*35 {
					canvas.SetRGBA(x, y, color.RGBA{14, 99, 174, 255})
				}
			}
		}
		line := func(x0, y0, x1, y1 float64) { stroke(canvas, x0, y0, x1, y1, 11) }
		switch name {
		case "screenshot":
			for _, p := range [][4]float64{{67, 105, 67, 68}, {67, 68, 105, 68}, {151, 68, 188, 68}, {188, 68, 188, 105}, {188, 151, 188, 188}, {188, 188, 151, 188}, {105, 188, 67, 188}, {67, 188, 67, 151}} {
				line(p[0], p[1], p[2], p[3])
			}
		case "clipboard":
			for _, p := range [][4]float64{{90, 73, 72, 73}, {72, 73, 72, 193}, {72, 193, 184, 193}, {184, 193, 184, 73}, {184, 73, 166, 73}, {101, 62, 155, 62}, {101, 62, 101, 87}, {101, 87, 155, 87}, {155, 87, 155, 62}, {102, 122, 156, 122}, {102, 153, 145, 153}} {
				line(p[0], p[1], p[2], p[3])
			}
		case "devtools":
			for _, p := range [][4]float64{{91, 91, 55, 128}, {55, 128, 91, 165}, {165, 91, 201, 128}, {201, 128, 165, 165}, {141, 77, 115, 179}} {
				line(p[0], p[1], p[2], p[3])
			}
		case "echo":
			for _, p := range [][4]float64{{68, 82, 111, 124}, {111, 124, 68, 166}, {129, 172, 189, 172}} {
				line(p[0], p[1], p[2], p[3])
			}
		}
		icon := image.NewRGBA(image.Rect(0, 0, 256, 256))
		for y := range 256 {
			for x := range 256 {
				var r, g, b, a uint32
				for sy := range scale {
					for sx := range scale {
						p := canvas.RGBAAt(x*scale+sx, y*scale+sy)
						r += uint32(p.R)
						g += uint32(p.G)
						b += uint32(p.B)
						a += uint32(p.A)
					}
				}
				icon.SetRGBA(x, y, color.RGBA{uint8(r / (scale * scale)), uint8(g / (scale * scale)), uint8(b / (scale * scale)), uint8(a / (scale * scale))})
			}
		}
		dir := filepath.Join("plugins", name, "assets")
		if err := os.MkdirAll(dir, 0755); err != nil {
			panic(err)
		}
		f, err := os.Create(filepath.Join(dir, "icon.png"))
		if err != nil {
			panic(err)
		}
		if err := png.Encode(f, icon); err != nil {
			panic(err)
		}
		if err := f.Close(); err != nil {
			panic(err)
		}
	}
}

func stroke(img *image.RGBA, x0, y0, x1, y1, width float64) {
	dx, dy := x1-x0, y1-y0
	for y := int(math.Min(y0, y1)-width) * scale; y <= int(math.Max(y0, y1)+width)*scale; y++ {
		for x := int(math.Min(x0, x1)-width) * scale; x <= int(math.Max(x0, x1)+width)*scale; x++ {
			px, py := float64(x)/scale, float64(y)/scale
			t := math.Max(0, math.Min(1, ((px-x0)*dx+(py-y0)*dy)/(dx*dx+dy*dy)))
			if math.Hypot(px-x0-t*dx, py-y0-t*dy) <= width/2 {
				img.SetRGBA(x, y, ink)
			}
		}
	}
}
