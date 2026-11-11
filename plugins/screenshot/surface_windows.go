//go:build windows

package main

import (
	"errors"
	"image"
	"image/color"

	"github.com/FluentLauncher/FluentLauncher-plugins/sdk"
)

// The launcher uses a 180-degree linear gradient: start at the top, end at
// the bottom. Colors are opaque; only the shared opacity sets translucency.
func surfaceColor(t pluginsdk.Theme, y, height int) uint32 {
	if t.GradientEnd == nil || t.Backdrop == "mica" {
		return t.Background
	}
	end := *t.GradientEnd
	denom := max(1, height-1)
	y = max(0, min(y, denom))
	blend := func(shift uint) uint32 {
		return (((t.Background>>shift)&255)*uint32(denom-y) + ((end>>shift)&255)*uint32(y) + uint32(denom/2)) / uint32(denom)
	}
	return blend(16)<<16 | blend(8)<<8 | blend(0)
}

func surfacePanel(dc uintptr, r image.Rectangle, radius int, t pluginsdk.Theme) {
	if t.GradientEnd == nil || t.Backdrop == "mica" {
		roundPanel(dc, r, radius, t.Background, t.Border)
		return
	}
	call(gdi32, "SaveDC", dc)
	rgn := call(gdi32, "CreateRoundRectRgn", coord(r.Min.X), coord(r.Min.Y), coord(r.Max.X), coord(r.Max.Y), radius*2, radius*2)
	call(gdi32, "ExtSelectClipRgn", dc, rgn, 1)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		fill(dc, image.Rect(r.Min.X, y, r.Max.X, y+1), rgbRef(surfaceColor(t, y-r.Min.Y, r.Dy())))
	}
	// Outline with no brush, preserving both gradient endpoints.
	pen := call(gdi32, "CreatePen", 0, 1, rgbRef(t.Border))
	oldPen := call(gdi32, "SelectObject", dc, pen)
	oldBrush := call(gdi32, "SelectObject", dc, call(gdi32, "GetStockObject", 5))
	call(gdi32, "RoundRect", dc, coord(r.Min.X), coord(r.Min.Y), coord(r.Max.X), coord(r.Max.Y), radius*2, radius*2)
	call(gdi32, "SelectObject", dc, oldBrush)
	call(gdi32, "SelectObject", dc, oldPen)
	call(gdi32, "DeleteObject", pen)
	call(gdi32, "DeleteObject", rgn)
	call(gdi32, "RestoreDC", dc, coord(-1))
}

func materialAlpha(t pluginsdk.Theme, material string) uint8 {
	if material == "acrylic" || material == "mica" {
		return 1
	}
	if material == "opaque" {
		return 255
	}
	return uint8(max(1, int(surfaceOpacity(t)*255+0.5)))
}

// GDI draws RGB, without preserving alpha. Recover background alpha against
// the exact same gradient, keeping image pixels and foreground controls opaque.
type opaqueSurface struct {
	bounds image.Rectangle
	radius int
}

func inRound(p image.Point, r image.Rectangle, radius int) bool {
	if !p.In(r) {
		return false
	}
	radius = min(radius, min(r.Dx(), r.Dy())/2)
	dx, dy := max(radius-(p.X-r.Min.X), 0, p.X-(r.Max.X-radius-1)), max(radius-(p.Y-r.Min.Y), 0, p.Y-(r.Max.Y-radius-1))
	return dx*dx+dy*dy <= radius*radius
}

func surfaceAlpha(img *image.RGBA, t pluginsdk.Theme, plate image.Rectangle, radius int, alpha uint8, opaque []opaqueSurface) {
	radius = min(radius, min(plate.Dx(), plate.Dy())/2)
	for y := plate.Min.Y; y < plate.Max.Y; y++ {
		for x := plate.Min.X; x < plate.Max.X; x++ {
			c := img.RGBAAt(x, y)
			a := uint8(255)
			if uint32(c.R)<<16|uint32(c.G)<<8|uint32(c.B) == surfaceColor(t, y-plate.Min.Y, plate.Dy()) {
				a = alpha
				for _, s := range opaque {
					if inRound(image.Pt(x, y), s.bounds, s.radius) {
						a = 255
						break
					}
				}
			}
			dx, dy := max(radius-(x-plate.Min.X), 0, x-(plate.Max.X-radius-1)), max(radius-(y-plate.Min.Y), 0, y-(plate.Max.Y-radius-1))
			if dx*dx+dy*dy > radius*radius {
				a = 0
			}
			img.SetRGBA(x, y, color.RGBA{uint8(uint32(c.R) * uint32(a) / 255), uint8(uint32(c.G) * uint32(a) / 255), uint8(uint32(c.B) * uint32(a) / 255), a})
		}
	}
}

func presentLayered(hwnd uintptr, bounds image.Rectangle, img *image.RGBA) error {
	b, err := newBitmap(img)
	if err != nil {
		return err
	}
	defer b.close()
	return presentBitmap(hwnd, bounds, b)
}

func presentBitmap(hwnd uintptr, bounds image.Rectangle, b *bitmap) error {
	dc := call(user32, "GetDC", 0)
	defer call(user32, "ReleaseDC", 0, dc)
	mem := call(gdi32, "CreateCompatibleDC", dc)
	old := call(gdi32, "SelectObject", mem, b.handle)
	defer func() { call(gdi32, "SelectObject", mem, old); call(gdi32, "DeleteDC", mem) }()
	dest, size, src := point{int32(bounds.Min.X), int32(bounds.Min.Y)}, point{int32(bounds.Dx()), int32(bounds.Dy())}, point{}
	blend := [4]byte{0, 0, 255, 1}
	if call(user32, "UpdateLayeredWindow", hwnd, dc, ptr(&dest), ptr(&size), mem, ptr(&src), 0, ptr(&blend), 2) == 0 {
		return errors.New("无法呈现截图工具栏")
	}
	return nil
}
