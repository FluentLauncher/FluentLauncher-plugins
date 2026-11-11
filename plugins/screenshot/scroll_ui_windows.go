//go:build windows

package main

import (
	"fmt"
	"image"
)

func (s *scrollWindow) showPreview() {
	s.preview = true
	s.hover = -1
	s.focus = 0
	s.offset = 0
	s.updatePreviewRegion()
	invalidate(s.outline)
}

func (s *scrollWindow) toolbar() image.Rectangle {
	scale := uiScale(s.hwnd)
	return image.Rect(0, s.bounds.Dy()-px(64, scale), s.bounds.Dx(), s.bounds.Dy())
}

// Visual order keeps the destructive action away from the confirmation at
// the far right, while existing action IDs remain stable for direct commands.
func (s *scrollWindow) buttonActions() []int {
	if s.preview {
		return []int{0, 1, 2, 3, 4}
	}
	return []int{0, 2, 1}
}

func (s *scrollWindow) buttons() []image.Rectangle {
	scale := uiScale(s.hwnd)
	pad, gap, h := px(6, scale), px(4, scale), px(32, scale)
	count := len(s.buttonActions())
	w := (s.bounds.Dx() - pad*2 - gap*(count-1)) / count
	result := make([]image.Rectangle, count)
	y := s.bounds.Dy() - pad - h
	for i := range result {
		x := pad + i*(w+gap)
		result[i] = image.Rect(x, y, x+w, y+h)
	}
	return result
}

func (s *scrollWindow) view() image.Rectangle {
	scale := uiScale(s.hwnd)
	right := px(2, scale)
	height := s.toolbar().Min.Y - px(12, scale)
	if s.preview && s.result != nil && float64(s.result.Rect.Dy())*min(1, float64(s.bounds.Dx()-px(4, scale))/float64(s.result.Rect.Dx())) > float64(height) {
		right = px(14, scale)
	}
	return image.Rect(px(2, scale), px(2, scale), s.bounds.Dx()-right, s.toolbar().Min.Y-px(10, scale))
}

func (s *scrollWindow) imageDestination() image.Rectangle {
	v := s.view()
	w, h := s.region.Dx(), s.region.Dy()
	if s.preview && s.result != nil {
		w, h = s.result.Rect.Dx(), s.visibleRows()
	}
	if !s.preview && s.thumbnail != nil {
		w, h = s.thumbnail.width, s.thumbnail.height
	}
	factor := min(1, float64(v.Dx())/float64(w), float64(v.Dy())/float64(h))
	if s.preview {
		factor = s.previewScale()
	}
	size := image.Pt(max(1, int(float64(w)*factor)), max(1, int(float64(h)*factor)))
	pos := image.Pt(v.Min.X+(v.Dx()-size.X)/2, v.Max.Y-size.Y)
	return image.Rectangle{Min: pos, Max: pos.Add(size)}
}

func (s *scrollWindow) imageFrame() image.Rectangle { return s.imageDestination().Inset(-1) }

func (s *scrollWindow) buttonHint(index int) string {
	if index < 0 || index >= len(s.buttonActions()) {
		return ""
	}
	action := s.buttonActions()[index]
	if s.preview {
		return []string{"复制 · Ctrl+C", "保存 · Ctrl+S", "原地贴图", "继续滚动采集", "关闭 · Esc"}[action]
	}
	if action == 0 {
		if s.paused.Load() {
			return "继续滚动采集"
		}
		return "暂停采集"
	}
	if action == 1 {
		return "完成并检查长图"
	}
	return "取消 · Esc"
}

func (s *scrollWindow) visibleRows() int {
	view := s.view()
	return min(s.result.Rect.Dy(), max(1, int(float64(view.Dy())/s.previewScale())))
}

func (s *scrollWindow) previewScale() float64 {
	return min(1, float64(s.view().Dx())/float64(s.result.Rect.Dx()))
}

func (s *scrollWindow) scrollbar() (image.Rectangle, image.Rectangle) {
	v := s.view()
	scale := uiScale(s.hwnd)
	track := image.Rect(v.Max.X+px(6, scale), v.Min.Y, v.Max.X+px(12, scale), v.Max.Y)
	rows := s.visibleRows()
	remaining := s.result.Rect.Dy() - rows
	h := max(px(24, scale), track.Dy()*rows/s.result.Rect.Dy())
	h = min(h, track.Dy())
	y := track.Min.Y
	if remaining > 0 {
		y += (track.Dy() - h) * s.offset / remaining
	}
	return track, image.Rect(track.Min.X, y, track.Max.X, y+h)
}

func (s *scrollWindow) scrollBy(delta int) {
	s.offset = max(0, min(s.offset+delta, s.result.Rect.Dy()-s.visibleRows()))
	invalidate(s.hwnd)
}

func (s *scrollWindow) dragScrollbar(point image.Point) {
	track, thumb := s.scrollbar()
	length := track.Dy() - thumb.Dy()
	if length <= 0 {
		return
	}
	y := max(0, min(point.Y-track.Min.Y-thumb.Dy()/2, length))
	s.offset = (s.result.Rect.Dy() - s.visibleRows()) * y / length
	invalidate(s.hwnd)
}

func (s *scrollWindow) paint() {
	s.paintBuffer.paint(s.hwnd, s.bounds.Size(), s.draw)
}

func (s *scrollWindow) draw(dc uintptr) {
	t, scale := s.d.theme, uiScale(s.hwnd)
	r := image.Rect(0, 0, s.bounds.Dx(), s.bounds.Dy())
	fill(dc, r, rgbRef(t.Background))
	bar := s.toolbar()
	roundPanel(dc, bar, px(int(t.Radius), scale), t.Background, t.Border)
	labels := []string{"暂停", "完成", "取消"}
	icons := []string{"pause", "check", "close"}
	if s.paused.Load() {
		labels[0] = "继续"
	}
	if s.preview {
		labels = []string{"复制", "保存", "贴图", "继续", "关闭"}
		icons = []string{"copy", "save", "pin", "start", "close"}
	}
	for i, r := range s.buttons() {
		action := s.buttonActions()[i]
		bg, fg := t.Background, t.Text
		disabled := s.busy
		if !s.preview && s.finishRequested.Load() && action != 2 {
			disabled = true
		}
		if i == s.hover && !disabled {
			bg = t.SurfaceHover
		}
		primary := 1
		if s.preview {
			primary = 0
		}
		if action == primary {
			bg, fg = t.Accent, t.AccentText
		}
		if disabled {
			fg = t.TextMuted
			bg = t.Surface
		}
		if action == 2 && !s.preview && i == s.hover {
			fg = t.Danger
		}
		roundPanel(dc, r, px(4, scale), bg, bg)
		size := px(18, scale)
		x := (r.Min.X + r.Max.X - size) / 2
		if !s.preview {
			x = r.Min.X + px(9, scale)
		}
		y := (r.Min.Y + r.Max.Y - size) / 2
		iconName := icons[action]
		if !s.preview && action == 0 && s.paused.Load() {
			iconName = "start"
		}
		icon(dc, iconName, image.Rect(x, y, x+size, y+size), fg)
		if !s.preview {
			text(dc, labels[action], image.Rect(x+size+px(4, scale), r.Min.Y, r.Max.X-px(3, scale), r.Max.Y), rgbRef(fg))
		}
		if call(user32, "GetFocus") == s.hwnd && i == s.focus {
			focusColor := t.Accent
			if action == primary {
				focusColor = t.AccentText
			}
			frame(dc, r.Inset(px(2, scale)), focusColor, 2)
		}
	}
	caption := fmt.Sprintf("%d × %d · %d 帧", s.region.Dx(), max(s.region.Dy(), s.height), max(1, s.frames))
	if s.paused.Load() {
		caption = "已暂停 · " + caption
	}
	if s.finishRequested.Load() && !s.preview {
		caption = "正在生成完整图片…"
	}
	if s.busy {
		caption = "正在输出完整图片…"
	}
	if s.notice != "" && !s.preview {
		caption = s.notice
	}
	hintIndex := s.hover
	if hintIndex < 0 && call(user32, "GetFocus") == s.hwnd {
		hintIndex = s.focus
	}
	if hint := s.buttonHint(hintIndex); hint != "" && !s.busy && (s.preview || (!s.finishRequested.Load() && s.notice == "")) {
		caption = hint
	}
	textAligned(dc, caption, image.Rect(bar.Min.X+px(10, scale), bar.Min.Y+px(3, scale), bar.Max.X-px(10, scale), bar.Min.Y+px(25, scale)), rgbRef(t.TextMuted), false)
	if !s.preview {
		if s.thumbnail != nil {
			s.thumbnail.draw(dc, s.imageDestination(), image.Rect(0, 0, s.thumbnail.width, s.thumbnail.height))
		} else {
			text(dc, "正在准备预览…", s.view(), rgbRef(t.TextMuted))
		}
		frame(dc, s.imageFrame(), t.Border, 1)
		return
	}
	rows := s.visibleRows()
	dest := s.imageDestination()
	s.bitmap.draw(dc, dest, image.Rect(0, s.offset, s.result.Rect.Dx(), s.offset+rows))
	frame(dc, s.imageFrame(), t.Border, 1)
	track, thumb := s.scrollbar()
	if rows < s.result.Rect.Dy() {
		roundPanel(dc, track, px(3, scale), t.Surface, t.Surface)
		roundPanel(dc, thumb, px(3, scale), t.Accent, t.Accent)
	}
}

func (s *scrollWindow) handle(msg uint32, wparam, lparam uintptr) uintptr {
	switch msg {
	case 0x0014:
		return 1
	case wmPaint:
		s.paint()
		return 0
	case 0x0318: // WM_PRINTCLIENT: render a complete client for native QA.
		s.draw(wparam)
		return 0
	case wmClose:
		if !s.busy {
			s.close(true)
		}
		return 0
	case wmMouseMove:
		p := mousePoint(lparam)
		previous := s.hover
		s.hover = -1
		for i, r := range s.buttons() {
			if p.In(r) {
				s.hover = i
			}
		}
		if s.dragScroll && s.preview {
			s.dragScrollbar(p)
		} else if previous != s.hover {
			invalidate(s.hwnd)
		}
		return 0
	case wmLButtonDown:
		call(user32, "SetFocus", s.hwnd)
		if s.preview && !s.busy {
			track, _ := s.scrollbar()
			if mousePoint(lparam).In(track.Inset(-px(5, uiScale(s.hwnd)))) {
				s.dragScroll = true
				call(user32, "SetCapture", s.hwnd)
				s.dragScrollbar(mousePoint(lparam))
			}
		}
		return 0
	case wmLButtonUp:
		if s.dragScroll {
			s.dragScroll = false
			call(user32, "ReleaseCapture")
			return 0
		}
		if s.busy {
			return 0
		}
		for i, r := range s.buttons() {
			if mousePoint(lparam).In(r) {
				s.focus = i
				s.activate(s.buttonActions()[i])
				break
			}
		}
		return 0
	case 0x0215:
		s.dragScroll = false
		return 0
	case wmMouseWheel:
		if s.preview {
			s.scrollBy(int(-float64(int16(wparam>>16)) / s.previewScale()))
		}
		return 0
	case wmKeyDown:
		if s.busy {
			return 0
		}
		switch wparam {
		case 0x1b:
			s.close(true)
		case 0x09:
			delta := 1
			if shiftDown() {
				delta = -1
			}
			s.focus = (s.focus + delta + len(s.buttons())) % len(s.buttons())
			invalidate(s.hwnd)
		case 0x0d, 0x20:
			s.activate(s.buttonActions()[s.focus])
		case 'C':
			if s.preview && ctrlDown() {
				s.activate(0)
			}
		case 'S':
			if s.preview && ctrlDown() {
				s.activate(1)
			}
		case 0x21:
			if s.preview {
				s.scrollBy(-s.visibleRows() * 4 / 5)
			}
		case 0x22:
			if s.preview {
				s.scrollBy(s.visibleRows() * 4 / 5)
			}
		case 0x24:
			if s.preview {
				s.offset = 0
				invalidate(s.hwnd)
			}
		case 0x23:
			if s.preview {
				s.offset = s.result.Rect.Dy() - s.visibleRows()
				invalidate(s.hwnd)
			}
		case 0x26:
			if s.preview {
				s.scrollBy(-40)
			}
		case 0x28:
			if s.preview {
				s.scrollBy(40)
			}
		}
		return 0
	}
	return call(user32, "DefWindowProcW", s.hwnd, msg, wparam, lparam)
}

func (s *scrollWindow) activate(index int) {
	if s.busy {
		return
	}
	if !s.preview {
		if s.finishRequested.Load() && index != 2 {
			return
		}
		switch index {
		case 0:
			s.paused.Store(!s.paused.Load())
		case 1:
			s.finishRequested.Store(true)
			s.cancel()
		case 2:
			s.close(true)
		}
		invalidate(s.outline)
		invalidate(s.hwnd)
		return
	}
	switch index {
	case 0:
		s.busy = true
		s.d.outputImage(s.result, s.region, false, func(err error) {
			s.busy = false
			if s.d.scrolling != s {
				return
			}
			if err == nil {
				s.close(false)
			} else {
				s.d.report(s.hwnd, err)
				invalidate(s.hwnd)
			}
		})
	case 1:
		s.busy = true
		s.d.saveAs(s.hwnd, s.result, func(err error, saved bool) {
			s.busy = false
			if s.d.scrolling != s {
				return
			}
			if saved {
				s.close(false)
			} else {
				s.d.report(s.hwnd, err)
				invalidate(s.hwnd)
			}
		})
	case 2:
		bounds := image.Rectangle{Min: s.region.Min, Max: s.region.Min.Add(s.result.Rect.Size())}
		monitor := currentMonitor(s.region.Min)
		factor := min(1.0, float64(monitor.Dy()*4/5)/float64(bounds.Dy()), float64(monitor.Dx()*4/5)/float64(bounds.Dx()))
		size := image.Pt(max(1, int(float64(bounds.Dx())*factor)), max(1, int(float64(bounds.Dy())*factor)))
		pos := image.Pt(max(monitor.Min.X, min(bounds.Min.X, monitor.Max.X-size.X)), max(monitor.Min.Y, min(bounds.Min.Y, monitor.Max.Y-size.Y)))
		// Size the window before its first shadow allocation; a tall result
		// must not create a screen-sized shadow for tens of thousands of rows.
		if _, err := s.d.createPin(s.result, bounds, image.Rectangle{Min: pos, Max: pos.Add(size)}, s.bitmap); err != nil {
			s.d.report(s.hwnd, err)
			return
		}
		s.bitmap = nil
		s.close(false)
	case 3:
		s.resume()
	case 4:
		s.close(true)
	}
}
