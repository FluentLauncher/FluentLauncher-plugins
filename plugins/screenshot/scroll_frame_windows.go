//go:build windows

package main

import "image"

// The ring lives strictly outside the physical crop, so even an OS without
// display-affinity support cannot burn its pixels into the screenshot.
const sourceFrameWidth = 2

func (s *scrollWindow) openSourceFrame() bool {
	s.outline = s.d.openOutline(s.region, "长截图选区")
	return s.outline != 0
}

// openOutline shows a click-through ring around a region; 0 means failure.
func (d *desktop) openOutline(region image.Rectangle, title string) uintptr {
	bounds := region.Inset(-sourceFrameWidth)
	outline := d.createWindow("FLScreenshotOutline", title, wsExTopmost|wsExToolwindow|wsExNoactivate|wsExTransparent|wsExLayered, d.controller, bounds)
	if outline == 0 {
		return 0
	}
	outer := call(gdi32, "CreateRectRgn", 0, 0, bounds.Dx(), bounds.Dy())
	inner := call(gdi32, "CreateRectRgn", sourceFrameWidth, sourceFrameWidth, bounds.Dx()-sourceFrameWidth, bounds.Dy()-sourceFrameWidth)
	call(gdi32, "CombineRgn", outer, outer, inner, 4) // RGN_DIFF: no filled interior.
	call(gdi32, "DeleteObject", inner)
	if call(user32, "SetWindowRgn", outline, outer, 0) == 0 {
		call(gdi32, "DeleteObject", outer)
		call(user32, "DestroyWindow", outline)
		return 0
	}
	call(user32, "SetLayeredWindowAttributes", outline, 0, 255, 2)
	call(user32, "SetWindowDisplayAffinity", outline, 0x11)
	windowAt(outline, bounds, false)
	return outline
}

func (s *scrollWindow) paintSourceFrame(msg uint32, wparam, lparam uintptr) uintptr {
	return outlineMessage(s.outline, s.region, s.drawSourceFrame, msg, wparam, lparam)
}

func outlineMessage(hwnd uintptr, region image.Rectangle, draw func(uintptr, image.Rectangle), msg uint32, wparam, lparam uintptr) uintptr {
	switch msg {
	case wmPaint:
		var ps paintStruct
		dc := call(user32, "BeginPaint", hwnd, ptr(&ps))
		defer call(user32, "EndPaint", hwnd, ptr(&ps))
		draw(dc, image.Rectangle{Max: region.Size().Add(image.Pt(sourceFrameWidth*2, sourceFrameWidth*2))})
		return 0
	case 0x0014:
		return 1
	case 0x0084:
		return ^uintptr(0) // HTTRANSPARENT
	case 0x0021:
		return 3 // MA_NOACTIVATE
	}
	return call(user32, "DefWindowProcW", hwnd, msg, wparam, lparam)
}

func (s *scrollWindow) drawSourceFrame(dc uintptr, r image.Rectangle) {
	t := s.d.theme
	color := t.Accent
	if s.paused.Load() || s.preview || s.finishRequested.Load() {
		color = t.Border
	}
	frame(dc, r, t.Inverse, sourceFrameWidth)
	frame(dc, r, color, 1)
}

// Keep only the image outline, scrollbar and bottom toolbar in the window's
// region. The desktop remains visible and clickable in the surrounding gaps.
func (s *scrollWindow) updatePreviewRegion() {
	if s.hwnd == 0 {
		return
	}
	imageBounds := s.imageFrame()
	rgn := call(gdi32, "CreateRectRgn", coord(imageBounds.Min.X), coord(imageBounds.Min.Y), coord(imageBounds.Max.X), coord(imageBounds.Max.Y))
	bar := s.toolbar()
	radius := px(int(s.d.theme.Radius), uiScale(s.hwnd))
	barRgn := call(gdi32, "CreateRoundRectRgn", coord(bar.Min.X), coord(bar.Min.Y), coord(bar.Max.X), coord(bar.Max.Y), radius*2, radius*2)
	call(gdi32, "CombineRgn", rgn, rgn, barRgn, 2)
	call(gdi32, "DeleteObject", barRgn)
	if s.preview && s.result != nil && s.visibleRows() < s.result.Rect.Dy() {
		track, _ := s.scrollbar()
		r := winRect(track.Inset(-px(2, uiScale(s.hwnd))))
		scrollRgn := call(gdi32, "CreateRectRgn", coord(int(r.Left)), coord(int(r.Top)), coord(int(r.Right)), coord(int(r.Bottom)))
		call(gdi32, "CombineRgn", rgn, rgn, scrollRgn, 2)
		call(gdi32, "DeleteObject", scrollRgn)
	}
	if call(user32, "SetWindowRgn", s.hwnd, rgn, 0) == 0 {
		call(gdi32, "DeleteObject", rgn)
	}
	invalidate(s.hwnd)
}
