//go:build windows

package main

import (
	"context"
	"image"

	"github.com/FluentLauncher/FluentLauncher-plugins/sdk"
)

func rgbRef(rgb uint32) uintptr { return uintptr((rgb&255)<<16 | rgb&0xff00 | (rgb>>16)&255) }
func refRGB(c uintptr) uint32   { return uint32((c&255)<<16 | c&0xff00 | (c>>16)&255) }

func defaultTheme() pluginsdk.Theme {
	return pluginsdk.Theme{Background: refRGB(systemColor(5)), Surface: refRGB(systemColor(15)), SurfaceHover: refRGB(systemColor(13)), Border: refRGB(systemColor(16)), Text: refRGB(systemColor(8)), TextMuted: refRGB(systemColor(17)), Accent: refRGB(systemColor(13)), AccentText: refRGB(systemColor(14)), Danger: refRGB(systemColor(8)), Inverse: refRGB(systemColor(8)), InverseText: refRGB(systemColor(5)), Radius: 8, Spacing: 4}
}

func (h handler) SetTheme(ctx context.Context, theme pluginsdk.Theme) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return h.desktop.post(func() { h.desktop.applyTheme(theme) })
}

func (d *desktop) applyTheme(theme pluginsdk.Theme) {
	d.theme = theme
	if d.scrolling != nil && d.scrolling.captureUIHidden {
		d.scrolling.themeDeferred = true
		return
	}
	if d.selection != nil {
		invalidate(d.selection.hwnd)
	}
	if d.scrolling != nil {
		invalidate(d.scrolling.hwnd)
	}
	for _, hwnd := range d.pinOrder {
		p := d.pins[hwnd]
		p.shadowBitmap.close()
		p.shadowBitmap = nil
		p.syncWindow()
		invalidate(p.hwnd)
	}
	if d.selection != nil {
		windowAt(d.selection.hwnd, d.selection.desktopBounds, false)
	}
	if d.scrolling != nil {
		windowAt(d.scrolling.hwnd, d.scrolling.bounds, false)
	}
	for p := d.menuRoot; p != nil; p = p.child {
		windowAt(p.hwnd, p.bounds, false)
		invalidate(p.hwnd)
	}
}

func uiScale(hwnd uintptr) float64 { return max(1, float64(call(user32, "GetDpiForWindow", hwnd))/96) }
func px(n int, scale float64) int  { return int(float64(n)*scale + 0.5) }

func roundPanel(dc uintptr, r image.Rectangle, radius int, background, border uint32) {
	brush := call(gdi32, "CreateSolidBrush", rgbRef(background))
	pen := call(gdi32, "CreatePen", 0, 1, rgbRef(border))
	oldBrush := call(gdi32, "SelectObject", dc, brush)
	oldPen := call(gdi32, "SelectObject", dc, pen)
	call(gdi32, "RoundRect", dc, coord(r.Min.X), coord(r.Min.Y), coord(r.Max.X), coord(r.Max.Y), radius*2, radius*2)
	call(gdi32, "SelectObject", dc, oldBrush)
	call(gdi32, "SelectObject", dc, oldPen)
	call(gdi32, "DeleteObject", brush)
	call(gdi32, "DeleteObject", pen)
}

func line(dc uintptr, a, b image.Point, c uint32, width int) {
	pen := call(gdi32, "CreatePen", 0, max(1, width), rgbRef(c))
	old := call(gdi32, "SelectObject", dc, pen)
	call(gdi32, "MoveToEx", dc, coord(a.X), coord(a.Y), 0)
	call(gdi32, "LineTo", dc, coord(b.X), coord(b.Y))
	call(gdi32, "SelectObject", dc, old)
	call(gdi32, "DeleteObject", pen)
}

// Small stroke icons share a 20-unit canvas; no font glyphs or emoji.
func icon(dc uintptr, name string, r image.Rectangle, c uint32) {
	p := func(x, y int) image.Point { return image.Pt(r.Min.X+x*r.Dx()/20, r.Min.Y+y*r.Dy()/20) }
	l := func(x, y, x2, y2 int) { line(dc, p(x, y), p(x2, y2), c, max(1, r.Dx()/12)) }
	box := func(x, y, x2, y2 int) { l(x, y, x2, y); l(x2, y, x2, y2); l(x2, y2, x, y2); l(x, y2, x, y) }
	switch name {
	case "scroll":
		box(3, 2, 12, 18)
		l(5, 6, 10, 6)
		l(5, 10, 10, 10)
		l(16, 4, 16, 15)
		l(13, 12, 16, 15)
		l(16, 15, 19, 12)
	case "cursor":
		l(4, 2, 4, 16)
		l(4, 16, 8, 12)
		l(8, 12, 12, 18)
		l(12, 18, 14, 17)
		l(14, 17, 10, 11)
		l(10, 11, 16, 11)
		l(16, 11, 4, 2)
	case "start":
		l(6, 3, 16, 10)
		l(16, 10, 6, 17)
		l(6, 17, 6, 3)
	case "copy":
		box(7, 7, 17, 17)
		l(3, 13, 3, 3)
		l(3, 3, 13, 3)
	case "save":
		box(3, 3, 17, 17)
		box(6, 3, 13, 8)
		box(6, 12, 14, 17)
	case "pin":
		l(6, 3, 14, 3)
		l(7, 3, 7, 8)
		l(13, 3, 13, 8)
		l(7, 8, 4, 12)
		l(13, 8, 16, 12)
		l(4, 12, 16, 12)
		l(10, 12, 10, 18)
	case "close":
		l(5, 5, 15, 15)
		l(5, 15, 15, 5)
	case "check":
		l(4, 10, 8, 14)
		l(8, 14, 16, 5)
	case "right":
		l(7, 4, 13, 10)
		l(13, 10, 7, 16)
	case "left":
		l(13, 4, 7, 10)
		l(7, 10, 13, 16)
	case "size":
		box(4, 4, 16, 16)
		l(4, 10, 16, 10)
		l(10, 4, 10, 16)
	case "rotate":
		l(5, 7, 5, 3)
		l(5, 3, 9, 3)
		l(5, 3, 15, 7)
		l(15, 7, 15, 15)
		l(15, 15, 5, 15)
	case "mirror":
		l(10, 2, 10, 18)
		l(6, 5, 2, 10)
		l(2, 10, 6, 15)
		l(14, 5, 18, 10)
		l(18, 10, 14, 15)
	case "opacity":
		box(3, 3, 17, 17)
		l(3, 17, 17, 3)
		l(3, 11, 11, 3)
	case "file":
		box(5, 2, 15, 18)
		l(8, 7, 12, 7)
		l(8, 11, 12, 11)
	case "reset":
		l(5, 4, 5, 9)
		l(5, 9, 10, 9)
		l(5, 9, 9, 4)
		l(9, 4, 15, 7)
		l(15, 7, 15, 15)
		l(15, 15, 6, 15)
	case "hide":
		box(3, 5, 17, 15)
		l(2, 18, 18, 2)
	case "info":
		box(3, 3, 17, 17)
		l(10, 8, 10, 14)
		l(10, 5, 10, 6)
	case "shadow":
		box(3, 3, 13, 13)
		l(7, 17, 17, 17)
		l(17, 17, 17, 7)
	case "taskbar":
		box(3, 3, 17, 17)
		l(3, 13, 17, 13)
	}
}
