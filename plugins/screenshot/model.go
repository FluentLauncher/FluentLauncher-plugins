package main

import (
	"encoding/binary"
	"image"
	"math"
)

// All geometry is in physical desktop pixels, including negative monitor origins.
type pinState struct {
	Original         image.Rectangle
	Bounds           image.Rectangle
	Shadow           bool
	Locked           bool
	Hidden           bool
	Topmost          bool
	Taskbar          bool
	Opacity          int
	Angle            int
	MirrorX, MirrorY bool
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
	natural := p.naturalSize()
	w := min(max(int(math.Round(float64(p.Bounds.Dx())*factor)), min(8, natural.X)), max(natural.X, min(natural.X*4, 8192)))
	h := max(1, int(math.Round(float64(w)*float64(natural.Y)/float64(natural.X))))
	p.Bounds.Max = p.Bounds.Min.Add(image.Pt(w, h))
}

func (p *pinState) naturalSize() image.Point {
	size := p.Original.Size()
	if p.Angle%2 != 0 {
		return image.Pt(size.Y, size.X)
	}
	return size
}

func transformed(source *image.RGBA, state pinState) *image.RGBA {
	if state.Angle == 0 && !state.MirrorX && !state.MirrorY {
		return source
	}
	size := source.Bounds().Size()
	if state.Angle%2 != 0 {
		size = image.Pt(size.Y, size.X)
	}
	out := image.NewRGBA(image.Rectangle{Max: size})
	for y := 0; y < size.Y; y++ {
		for x := 0; x < size.X; x++ {
			u, v := x, y
			if state.MirrorX {
				u = size.X - 1 - u
			}
			if state.MirrorY {
				v = size.Y - 1 - v
			}
			sx, sy := u, v
			switch state.Angle {
			case 1:
				sx, sy = v, source.Rect.Dy()-1-u
			case 2:
				sx, sy = source.Rect.Dx()-1-u, source.Rect.Dy()-1-v
			case 3:
				sx, sy = source.Rect.Dx()-1-v, u
			}
			out.SetRGBA(x, y, source.RGBAAt(source.Rect.Min.X+sx, source.Rect.Min.Y+sy))
		}
	}
	return out
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
