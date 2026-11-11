//go:build windows

package main

import "image"

// The ring lives strictly outside the physical crop, so even an OS without
// display-affinity support cannot burn its pixels into the screenshot.
const sourceFrameWidth = 2

func (s *scrollWindow) openSourceFrame() bool {
	bounds := s.region.Inset(-sourceFrameWidth)
	s.outline = s.d.createWindow("FLScreenshotOutline", "长截图选区", wsExTopmost|wsExToolwindow|wsExNoactivate|wsExTransparent|wsExLayered, s.d.controller, bounds)
	if s.outline == 0 {
		return false
	}
	outer := call(gdi32, "CreateRectRgn", 0, 0, bounds.Dx(), bounds.Dy())
	inner := call(gdi32, "CreateRectRgn", sourceFrameWidth, sourceFrameWidth, bounds.Dx()-sourceFrameWidth, bounds.Dy()-sourceFrameWidth)
	call(gdi32, "CombineRgn", outer, outer, inner, 4) // RGN_DIFF: no filled interior.
	call(gdi32, "DeleteObject", inner)
	if call(user32, "SetWindowRgn", s.outline, outer, 0) == 0 {
		call(gdi32, "DeleteObject", outer)
		call(user32, "DestroyWindow", s.outline)
		s.outline = 0
		return false
	}
	call(user32, "SetLayeredWindowAttributes", s.outline, 0, 255, 2)
	call(user32, "SetWindowDisplayAffinity", s.outline, 0x11)
	windowAt(s.outline, bounds, false)
	return true
}

func (s *scrollWindow) paintSourceFrame(msg uint32, wparam, lparam uintptr) uintptr {
	switch msg {
	case wmPaint:
		var ps paintStruct
		dc := call(user32, "BeginPaint", s.outline, ptr(&ps))
		defer call(user32, "EndPaint", s.outline, ptr(&ps))
		r := image.Rectangle{Max: s.region.Size().Add(image.Pt(sourceFrameWidth*2, sourceFrameWidth*2))}
		s.drawSourceFrame(dc, r)
		return 0
	case 0x0014:
		return 1
	case 0x0084:
		return ^uintptr(0) // HTTRANSPARENT
	case 0x0021:
		return 3 // MA_NOACTIVATE
	}
	return call(user32, "DefWindowProcW", s.outline, msg, wparam, lparam)
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
