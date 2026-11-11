//go:build windows

package main

import "image"

// Reuse a GUI-thread-owned back buffer. A WM_PAINT presents one complete
// frame instead of exposing the dimmed desktop/background between GDI draws.
type paintBuffer struct {
	dc, bitmap, original uintptr
	size                 image.Point
}

func (b *paintBuffer) close() {
	if b.dc == 0 {
		return
	}
	call(gdi32, "SelectObject", b.dc, b.original)
	call(gdi32, "DeleteObject", b.bitmap)
	call(gdi32, "DeleteDC", b.dc)
	*b = paintBuffer{}
}

func (b *paintBuffer) paint(hwnd uintptr, size image.Point, draw func(uintptr)) {
	var ps paintStruct
	dc := call(user32, "BeginPaint", hwnd, ptr(&ps))
	defer call(user32, "EndPaint", hwnd, ptr(&ps))
	if size.X <= 0 || size.Y <= 0 {
		return
	}
	if b.dc == 0 || b.size != size {
		b.close()
		b.dc = call(gdi32, "CreateCompatibleDC", dc)
		b.bitmap = call(gdi32, "CreateCompatibleBitmap", dc, size.X, size.Y)
		if b.dc == 0 || b.bitmap == 0 {
			if b.bitmap != 0 {
				call(gdi32, "DeleteObject", b.bitmap)
			}
			if b.dc != 0 {
				call(gdi32, "DeleteDC", b.dc)
			}
			*b = paintBuffer{}
			draw(dc)
			return
		}
		b.original = call(gdi32, "SelectObject", b.dc, b.bitmap)
		b.size = size
	}
	r := ps.Rect.image().Intersect(image.Rectangle{Max: size})
	if !r.Empty() {
		call(gdi32, "SaveDC", b.dc)
		call(gdi32, "IntersectClipRect", b.dc, coord(r.Min.X), coord(r.Min.Y), coord(r.Max.X), coord(r.Max.Y))
		draw(b.dc)
		call(gdi32, "RestoreDC", b.dc, coord(-1))
		call(gdi32, "BitBlt", dc, coord(r.Min.X), coord(r.Min.Y), r.Dx(), r.Dy(), b.dc, coord(r.Min.X), coord(r.Min.Y), srccopy)
	}
}
