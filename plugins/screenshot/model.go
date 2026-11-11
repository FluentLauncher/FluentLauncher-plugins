package main

import (
	"encoding/binary"
	"image"
	"math"
)

// All geometry is in physical desktop pixels, including negative monitor origins.
type pinState struct {
	Original image.Rectangle
	Bounds   image.Rectangle
	Shadow   bool
	Locked   bool
	Hidden   bool
}

func (p *pinState) move(dx, dy int) {
	if !p.Locked {
		p.Bounds = p.Bounds.Add(image.Pt(dx, dy))
	}
}

func (p *pinState) nudge(key uint32, shift bool) bool {
	step := 1
	if shift {
		step = 10
	}
	switch key {
	case 0x25:
		p.move(-step, 0)
	case 0x26:
		p.move(0, -step)
	case 0x27:
		p.move(step, 0)
	case 0x28:
		p.move(0, step)
	default:
		return false
	}
	return true
}

func (p *pinState) zoom(factor float64) {
	if p.Locked {
		return
	}
	w := min(max(int(math.Round(float64(p.Bounds.Dx())*factor)), min(8, p.Original.Dx())), max(p.Original.Dx(), min(p.Original.Dx()*4, 8192)))
	h := max(1, int(math.Round(float64(w)*float64(p.Original.Dy())/float64(p.Original.Dx()))))
	p.Bounds.Max = p.Bounds.Min.Add(image.Pt(w, h))
}

func orderedRect(a, b image.Point) image.Rectangle {
	return image.Rect(min(a.X, b.X), min(a.Y, b.Y), max(a.X, b.X), max(a.Y, b.Y))
}

// makeDIB is CF_DIB: a 40-byte header followed by opaque bottom-up BGRA pixels.
func makeDIB(img *image.RGBA) []byte {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	data := make([]byte, 40+w*h*4)
	binary.LittleEndian.PutUint32(data, 40)
	binary.LittleEndian.PutUint32(data[4:], uint32(w))
	binary.LittleEndian.PutUint32(data[8:], uint32(h))
	binary.LittleEndian.PutUint16(data[12:], 1)
	binary.LittleEndian.PutUint16(data[14:], 32)
	binary.LittleEndian.PutUint32(data[20:], uint32(w*h*4))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := img.RGBAAt(img.Rect.Min.X+x, img.Rect.Min.Y+y)
			o := 40 + ((h-1-y)*w+x)*4
			data[o], data[o+1], data[o+2], data[o+3] = c.B, c.G, c.R, 255
		}
	}
	return data
}
