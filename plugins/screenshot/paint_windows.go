//go:build windows

package main

import (
	"errors"
	"image"
)

// Reuse a GUI-thread-owned back buffer. A WM_PAINT presents one complete
// frame instead of exposing the dimmed desktop/background between GDI draws.
type paintBuffer struct {
	dc, bitmap, original uintptr
	size                 image.Point
}

func (b *paintBuffer) pixels() (*image.RGBA, error) {
	if b.dc == 0 {
		return nil, errors.New("截图绘图缓冲尚未就绪")
	}
	imageBuffer, err := newBitmap(image.NewRGBA(image.Rectangle{Max: b.size}))
	if err != nil {
		return nil, err
	}
	defer imageBuffer.close()
	dc := call(gdi32, "CreateCompatibleDC", 0)
	old := call(gdi32, "SelectObject", dc, imageBuffer.handle)
	defer func() { call(gdi32, "SelectObject", dc, old); call(gdi32, "DeleteDC", dc) }()
	if call(gdi32, "BitBlt", dc, 0, 0, b.size.X, b.size.Y, b.dc, 0, 0, srccopy) == 0 {
		return nil, errors.New("无法读取截图绘图缓冲")
	}
	return imageBuffer.pixels()
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
	if !b.ensure(dc, size) {
		draw(dc)
		return
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

func (b *paintBuffer) ensure(dc uintptr, size image.Point) bool {
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
			return false
		}
		b.original = call(gdi32, "SelectObject", b.dc, b.bitmap)
		b.size = size
	}
	return true
}

// Layered windows publish complete RGBA frames; their WM_PAINT update region
// may be empty after UpdateLayeredWindow. Never use it to clip the next frame.
func (b *paintBuffer) render(hwnd uintptr, size image.Point, draw func(uintptr)) error {
	dc := call(user32, "GetDC", hwnd)
	defer call(user32, "ReleaseDC", hwnd, dc)
	if !b.ensure(dc, size) {
		return errors.New("无法创建工具栏绘图缓冲")
	}
	draw(b.dc)
	return nil
}
