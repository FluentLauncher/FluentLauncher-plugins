//go:build windows

package main

import (
	"image"
	"image/color"
	"math"
	"unsafe"

	"github.com/FluentLauncher/FluentLauncher-plugins/sdk"
)

func surfaceOpacity(t pluginsdk.Theme) float64 {
	if t.Opacity == nil || math.IsNaN(float64(*t.Opacity)) {
		return 1
	}
	return max(0, min(1, float64(*t.Opacity)))
}

// Only the popup background is translucent. Opaque text and controls are
// composed independently; SetLayeredWindowAttributes would fade them too.
func (p *popupWindow) applyMaterial() {
	t := p.d.theme
	p.material = applySurfaceMaterial(p.hwnd, t)
	radius := px(int(max(0, t.Radius)), p.scale)
	region := call(gdi32, "CreateRoundRectRgn", 0, 0, p.bounds.Dx()+1, p.bounds.Dy()+1, radius*2, radius*2)
	if call(user32, "SetWindowRgn", p.hwnd, region, 0) == 0 {
		call(gdi32, "DeleteObject", region)
	}
}

func applySurfaceMaterial(hwnd uintptr, t pluginsdk.Theme) string {
	dark := uint32(0)
	if t.Dark {
		dark = 1
	}
	call(dwmapi, "DwmSetWindowAttribute", hwnd, 20, ptr(&dark), 4)
	backdrop := uint32(1) // DWMSBT_NONE
	call(dwmapi, "DwmSetWindowAttribute", hwnd, 38, ptr(&backdrop), 4)
	clearMargins := [4]int32{}
	call(dwmapi, "DwmExtendFrameIntoClientArea", hwnd, ptr(&clearMargins))
	policy := struct{ State, Flags, Gradient, Animation uint32 }{}
	attribute := struct {
		Attribute int32
		Data      unsafe.Pointer
		Size      uintptr
	}{19, ptr(&policy), unsafe.Sizeof(policy)}
	proc := user32.NewProc("SetWindowCompositionAttribute")
	available := proc.Find() == nil
	if available {
		callAddress(proc.Addr(), hwnd, ptr(&attribute))
	}
	material := "none"
	if t.Backdrop == "acrylic" && available {
		policy.State = 4 // ACCENT_ENABLE_ACRYLICBLURBEHIND
		policy.Gradient = uint32(max(1, int(surfaceOpacity(t)*255)))<<24 | uint32(rgbRef(t.Background))
		if callAddress(proc.Addr(), hwnd, ptr(&attribute)) != 0 {
			material = "acrylic"
		} else {
			material = "opaque"
		}
	} else if t.Backdrop == "mica" {
		backdrop = 2 // DWMSBT_MAINWINDOW: native wallpaper-based Mica.
		margins := [4]int32{-1, -1, -1, -1}
		if call(dwmapi, "DwmSetWindowAttribute", hwnd, 38, ptr(&backdrop), 4) == 0 && call(dwmapi, "DwmExtendFrameIntoClientArea", hwnd, ptr(&margins)) == 0 {
			material = "mica"
		} else {
			material = "opaque"
		}
	}
	return material
}

func (p *popupWindow) layeredSurface() (*bitmap, error) {
	base := image.NewRGBA(image.Rect(0, 0, p.bounds.Dx(), p.bounds.Dy()))
	b, err := newBitmap(base)
	if err != nil {
		return nil, err
	}
	dc := call(gdi32, "CreateCompatibleDC", 0)
	old := call(gdi32, "SelectObject", dc, b.handle)
	p.draw(dc)
	img, err := b.pixels()
	call(gdi32, "SelectObject", dc, old)
	call(gdi32, "DeleteDC", dc)
	b.close()
	if err != nil {
		return nil, err
	}
	t := p.d.theme
	var opaque []opaqueSurface
	for i, it := range p.items {
		if i == p.hover && !it.Disabled {
			opaque = append(opaque, opaqueSurface{p.rows[i], px(4, p.scale)})
		}
		if it.Checked {
			r := p.rows[i]
			size := px(17, p.scale)
			y := (r.Min.Y + r.Max.Y - size) / 2
			opaque = append(opaque, opaqueSurface{image.Rect(r.Min.X+px(7, p.scale), y, r.Min.X+px(7, p.scale)+size, y+size), px(3, p.scale)})
		}
	}
	surfaceAlpha(img, t, img.Bounds(), px(int(t.Radius), p.scale), materialAlpha(t, p.material), opaque)
	return newBitmap(img)
}

func (p *popupWindow) paintLayered() {
	b, err := p.layeredSurface()
	if err != nil {
		return
	}
	defer b.close()
	_ = presentBitmap(p.hwnd, p.bounds, b)
}

// Selection already freezes the desktop. Its toolbar composites against that
// same snapshot, so no live window can leak into an otherwise frozen capture.
func (s *selectionWindow) paintFrozenPanel(dc uintptr, bar image.Rectangle) {
	t := s.d.theme
	if s.panelCache == nil || s.panelBounds != bar {
		s.panelCache.close()
		s.panelCache = nil
		s.panelBounds = bar
		img := crop(s.image, bar)
		if t.Backdrop == "acrylic" {
			img = blurPanel(img, px(6, uiScale(s.hwnd)))
		}
		a := surfaceOpacity(t)
		if t.Backdrop == "mica" {
			a = 1
		}
		for y := 0; y < img.Rect.Dy(); y++ {
			for x := 0; x < img.Rect.Dx(); x++ {
				c := img.RGBAAt(x, y)
				// The area outside the crop uses the selection's dimmed snapshot.
				factor := 1.0
				if !image.Pt(x+bar.Min.X, y+bar.Min.Y).In(s.selected) {
					factor = 0.6
				}
				blend := func(v uint8, tint uint32) uint8 { return uint8(float64(v)*factor*(1-a) + float64(tint)*a + 0.5) }
				tint := surfaceColor(t, y, img.Rect.Dy())
				img.SetRGBA(x, y, color.RGBA{blend(c.R, (tint>>16)&255), blend(c.G, (tint>>8)&255), blend(c.B, tint&255), 255})
			}
		}
		s.panelCache, _ = newBitmap(img)
	}
	if s.panelCache != nil {
		call(gdi32, "SaveDC", dc)
		radius := px(int(t.Radius), uiScale(s.hwnd))
		rgn := call(gdi32, "CreateRoundRectRgn", coord(bar.Min.X), coord(bar.Min.Y), coord(bar.Max.X), coord(bar.Max.Y), radius*2, radius*2)
		call(gdi32, "ExtSelectClipRgn", dc, rgn, 1)
		s.panelCache.draw(dc, bar, image.Rect(0, 0, bar.Dx(), bar.Dy()))
		call(gdi32, "DeleteObject", rgn)
		call(gdi32, "RestoreDC", dc, coord(-1))
	} else {
		roundPanel(dc, bar, px(int(t.Radius), uiScale(s.hwnd)), t.Background, t.Border)
	}
}

// A separable box blur keeps repaint cost bounded by the small toolbar area.
func blurPanel(src *image.RGBA, radius int) *image.RGBA {
	pass := func(src *image.RGBA, horizontal bool) *image.RGBA {
		out := image.NewRGBA(src.Bounds())
		w, h := src.Rect.Dx(), src.Rect.Dy()
		outer, inner := h, w
		if !horizontal {
			outer, inner = w, h
		}
		for a := 0; a < outer; a++ {
			var sums [3]int
			pixel := func(b int) color.RGBA {
				b = max(0, min(b, inner-1))
				if horizontal {
					return src.RGBAAt(b, a)
				}
				return src.RGBAAt(a, b)
			}
			add := func(c color.RGBA, sign int) {
				sums[0] += int(c.R) * sign
				sums[1] += int(c.G) * sign
				sums[2] += int(c.B) * sign
			}
			for i := -radius; i <= radius; i++ {
				add(pixel(i), 1)
			}
			for b := 0; b < inner; b++ {
				c := color.RGBA{uint8(sums[0] / (radius*2 + 1)), uint8(sums[1] / (radius*2 + 1)), uint8(sums[2] / (radius*2 + 1)), 255}
				if horizontal {
					out.SetRGBA(b, a, c)
				} else {
					out.SetRGBA(a, b, c)
				}
				add(pixel(b-radius), -1)
				add(pixel(b+radius+1), 1)
			}
		}
		return out
	}
	return pass(pass(src, true), false)
}
